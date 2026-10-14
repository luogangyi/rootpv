# Kata + QEMU 持久化根文件系统

RootPV 通过 `containerd-shim-rootpv-kata-v2` 代理 Kata 的 containerd task API。
用户继续指定原生 `runtimeClassName: kata-qemu`；RootPV 准入将启用持久化根文件系统的
Pod 映射到 `persistent-rootfs-kata-qemu`。普通 Kata Pod 使用原生 handler。

## 前提与安装

- 在目标节点预先安装并验证 Kata + QEMU；RootPV 不安装 Kata、QEMU 或 guest kernel。
- 使用 containerd、`overlayfs` snapshotter、特权节点 agent、`flat-lowerdirs` 和可用于 OverlayFS upper/work 的 PVC 文件系统。
- 源 handler 的 `runtime_type` 为 `io.containerd.kata-qemu.v2` 或 `io.containerd.kata.v2`。
- 源 handler 显式配置绝对路径 `runtime_path` 与 `options.ConfigPath`。Kata TOML 必须仅配置
  `[hypervisor.qemu]`；指定 `runtime.hypervisor_name` 时必须为 `qemu`。
- QEMU 的共享文件系统配置必须能够将宿主机容器根文件系统共享给 guest；隔离测试环境 使用 Kata 默认 virtio-fs 配置。

使用本分支构建 RootPV 镜像并通过 Helm 安装。镜像与节点 installer 均包含新的 Kata shim。
节点 agent 会读取 containerd 的显式 imports 和 glob imports，保留源配置文件，生成 shadow handler，
并在本地受信任 registry 中记录原 Kata shim 路径及配置摘要。Shadow handler 保留 `ConfigPath`、
Kata annotations、设备选项、RuntimeClass overhead 和 scheduling；仅替换 `runtime_path` 并增加 RootPV annotation 白名单。

如果 kubelet 与 containerd 使用非默认数据目录，应同时设置路径和宿主机挂载：

```yaml
nodeAgent:
  kubeletRoot: /apps/data/kubelet
  hostPaths:
    kubelet: /apps/data/kubelet
    containerdLib: /apps/data/containerd
  allowedOverlaySnapshotRoots:
  - /apps/data/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots
```

手工安装时，构建并放置 shim：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -o dist/containerd-shim-rootpv-kata-v2-linux-amd64 \
  ./cmd/containerd-shim-rootpv-kata-v2
```

`scripts/install-runtime-wrapper.sh` 会安装同目录的上述产物；其他架构或文件名可通过
`ROOTPV_KATA_SHIM_BINARY` 指定。节点 registry 和 handler 仍由自动发现流程生成。

## Pod 使用

在已有 RootPV Pod 中增加原 RuntimeClass 即可，PVC、anchor mount 和单写者约束与 runc 相同：

```yaml
metadata:
  annotations:
    ecloud.com/persistent-rootfs: "true"
    ecloud.com/rootfs-container: app
    ecloud.com/rootfs-volume: rootfs-data
    ecloud.com/rootfs-path: applications/example
    ecloud.com/rootfs-base-image-policy: Strict
spec:
  runtimeClassName: kata-qemu
```

目标容器的镜像只读层来自 containerd 快照元数据，持久化 upper/work 来自 rootfsd 验证的 PVC。
代理先挂载单层 OverlayFS，再通过 task `Create.Rootfs` 将它作为 bind mount 交给 Kata。
Kata 继续负责 VM、guest 容器、exec、网络、设备和日志。Sandbox 与未被选中的 sidecar 使用普通快照。

每个 sandbox 固定原始 Kata shim 的委托路径。通过 Unix socket 内核凭据验证 root 身份，在 sandbox 创建后取得 Kata Connect 返回的 shim PID，
在容器创建前登记租约的 delegate 身份；Kata Rust runtime 在 sandbox Create 前不提供可用的 Connect RPC。
删除时先完成 Kata task Delete，再正常卸载 RootPV OverlayFS，确认卸载后才释放 PVC 租约。
失败时保留状态及租约，供重试和 rootfsd 恢复。

## 支持边界

本适配器只支持 QEMU + flat-lowerdirs 的普通容器创建。CLH、gVisor、其他独立 shim、
nested-rootfs 和从 checkpoint 恢复不进入此适配器。`io.containerd.kata.v2` 仅在本机配置明确为 QEMU 时接入。
现有 runc 的 OCI wrapper 会拒绝将 Kata shim 当作 OCI binary 执行。

代理或 Kata shim 仍存活时，rootfsd 恢复逻辑保留 Kata 写层租约。两者均退出后，恢复逻辑清理记录的
mount namespace 内的 OverlayFS，不会将 Kata binary 作为 runc 调用。节点重启、故障注入和性能基线
应单独验证；当前集群功能验证记录见 `tests/e2e/results/kata-qemu-2026-10-14.md`。
