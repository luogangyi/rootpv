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
- QEMU 的共享文件系统配置必须能够将宿主机容器根文件系统共享给 guest；本次功能验证使用 Kata 默认 virtio-fs 配置。

使用包含本分支实现的 RootPV 镜像并通过 Helm 安装；完整命令见 [部署指南](deployment.md)。
不能仅凭 CI latest 标签确认镜像中已有新 shim。镜像与节点 installer 均包含新的 Kata shim。
节点 agent 会读取 containerd 的显式 imports 和 glob imports，保留源配置文件，生成 shadow handler，
并在本地受信任 registry 中记录原 Kata shim 路径及配置摘要。Shadow handler 保留 `ConfigPath`、
Kata annotations、设备选项、RuntimeClass overhead 和 scheduling；仅替换 `runtime_path` 并增加 RootPV annotation 白名单。

摘要必须包含完整的源 handler 配置与 Kata 配置内容。发现阶段编码 options 或读取配置失败时，
handler 保持 unresolved，原因是 `runtime_digest_unavailable`；reconcile 阶段发生同类错误时，
返回错误并停止此次配置及 registry 写入。检查 ConfigPath、文件权限及 node-agent 日志后重试。

如果 kubelet 与 containerd 使用非默认数据目录，应同时设置路径和宿主机挂载：

```yaml
nodeAgent:
  kubeletRoot: /custom/kubelet
  hostPaths:
    kubelet: /custom/kubelet
    containerdLib: /custom/containerd
  allowedOverlaySnapshotRoots:
  - /custom/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots
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

在已有 RootPV Pod 中增加原 RuntimeClass 即可，PVC、anchor mount 和单写者约束与 runc 相同。
以下示例假设原 RuntimeClass 和 handler 都叫 kata-qemu；其他名称以自动生成的 catalog 为准：

```yaml
metadata:
  annotations:
    openfuyao.cn/persistent-rootfs: "true"
    openfuyao.cn/rootfs-container: app
    openfuyao.cn/rootfs-volume: rootfs-data
    openfuyao.cn/rootfs-path: applications/example
    openfuyao.cn/rootfs-base-image-policy: Strict
spec:
  runtimeClassName: kata-qemu
```

目标容器的镜像只读层来自 containerd 快照元数据，持久化 upper/work 来自 rootfsd 验证的 PVC。
代理先挂载单层 OverlayFS，再通过 task `Create.Rootfs` 将它作为 bind mount 交给 Kata。
Kata 继续负责 VM、guest 容器、exec、网络、设备和日志。Sandbox 与未被选中的 sidecar 使用普通快照。

只需声明 PVC volume；Webhook 为目标容器注入只读 subPath anchor。持久化 backing PVC
无需整体挂到 /data，避免应用直接改写 upper/work/meta。

每个 sandbox 固定原始 Kata shim 的委托路径。通过 Unix socket 内核凭据验证 root 身份，在 sandbox 创建后取得 Kata Connect 返回的 shim PID，
在容器创建前登记租约的 delegate 身份；Kata Rust runtime 在 sandbox Create 前不提供可用的 Connect RPC。
删除时先完成 Kata task Delete，再正常卸载 RootPV OverlayFS，确认卸载后才释放 PVC 租约。
失败时保留状态及租约，供重试和 rootfsd 恢复。

## 完整测试 YAML 与重建验证

仓库提供 [完整测试清单](../../tests/e2e/manifests/kata-qemu.yaml)，包含 Namespace、
静态 local PV、PVC 和 app/sidecar Pod。使用前把 rootpv-worker-1 替换为目标 Kata 节点，
把测试镜像替换为节点可拉取或已缓存的同一镜像；两轮保持镜像不变，满足 Strict 策略。

清单的 local PV 路径为 /var/lib/rootpv/kata-test-data。先在选定节点创建该目录，
确认它所在的文件系统支持 OverlayFS upper/work。PV/PVC 通过 volumeName 与 claimRef
静态绑定，类名为 rootpv-local。实际文件系统须满足 OverlayFS 要求；存储策略 ConfigMap
目前不构成强制类名准入保证。此清单不验证动态 CSI provisioning。

```bash
# 在目标节点执行
mkdir -p /var/lib/rootpv/kata-test-data

# 在具有集群访问权限、持有修改后清单的终端执行
kubectl get runtimeclass kata-qemu persistent-rootfs-kata-qemu
kubectl apply -f tests/e2e/manifests/kata-qemu.yaml
kubectl -n rootpv-test-kata-qemu wait \
  --for=condition=Ready pod/kata-rootpv-test --timeout=180s
kubectl -n rootpv-test-kata-qemu logs kata-rootpv-test -c app
kubectl -n rootpv-test-kata-qemu logs kata-rootpv-test -c sidecar
```

新目录的首次运行应显示 persistent-start-count=1、guest kernel 和 first-pod-uid。
Pod 应为 2/2 Ready。可检查实际的 RuntimeClass 与 adapter：

```bash
kubectl -n rootpv-test-kata-qemu get pod kata-rootpv-test -o yaml
```

实际 runtimeClassName 应为 persistent-rootfs-kata-qemu，adapter annotation 为
kata-qemu-v2，目标 app 带有只读 PVC anchor。sidecar 看不到 app 的根目录标记。

保留 Namespace、PV 和 PVC，仅删除并重建 Pod：

```bash
kubectl -n rootpv-test-kata-qemu delete pod kata-rootpv-test --wait=true
kubectl apply -f tests/e2e/manifests/kata-qemu.yaml
kubectl -n rootpv-test-kata-qemu wait \
  --for=condition=Ready pod/kata-rootpv-test --timeout=180s
kubectl -n rootpv-test-kata-qemu logs kata-rootpv-test -c app
kubectl -n rootpv-test-kata-qemu logs kata-rootpv-test -c sidecar
```

第二轮 persistent-start-count 应为 2；current-pod-uid 改变，first-pod-uid 保持首次值。
sidecar 的脚本会确认其根目录标记在这次创建前不存在。重复测试使用已有数据目录时，计数继续增加。

结束后删除测试资源：

```bash
kubectl delete namespace rootpv-test-kata-qemu --wait=true
kubectl delete pv rootpv-test-kata-qemu-pv
```

PV 使用 Retain，节点目录及持久化数据保留。需要重新从 1 开始时，在确认测试 Pod 和挂载已清理后，
重置专用测试目录。不要只删除 PVC 后复用仍绑定旧 PVC UID 的 Retain PV。
RootPV 的安装卸载步骤见 [部署指南](deployment.md#卸载与节点配置清理)。

## 常见问题

| 现象 | 检查项 |
| --- | --- |
| persistent-rootfs-kata-qemu 不存在 | Controller/agent 是否运行当前镜像，源 RuntimeClass 是否存在，inventory 中 handler 是否 resolved |
| handler 为 unresolved | 检查 runtime_path、ConfigPath、QEMU TOML、shim 文件是否可执行，以及 imports 是否可读取 |
| Pod Pending | 检查 Shadow RuntimeClass 的原调度条件、adapter 就绪标签、PVC 绑定与 local PV 节点亲和性 |
| 准入拒绝 PVC | 确认 backing volume 是 PVC、目标容器存在，并使用 Strict |
| persistent_upper_in_use | 确认同一 PVC/rootfs-path 的旧 Pod 已完成删除和租约释放 |
| 重建计数仍为 1 | 确认复用同一 PVC/rootfs-path，实际 Pod 经过 RootPV mutation，marker 写在容器根目录 |
| Kata 创建失败 | 收集 Pod events、controller/rootfsd 日志及 containerd journal；确认原 Kata Pod 可独立运行 |

## 支持边界

本适配器只支持 QEMU + flat-lowerdirs 的普通容器创建。CLH、gVisor、其他独立 shim、
nested-rootfs 和从 checkpoint 恢复不进入此适配器。`io.containerd.kata.v2` 仅在本机配置明确为 QEMU 时接入。
现有 runc 的 OCI wrapper 会拒绝将 Kata shim 当作 OCI binary 执行。

代理或 Kata shim 仍存活时，rootfsd 恢复逻辑保留 Kata 写层租约。两者均退出后，恢复逻辑清理记录的
mount namespace 内的 OverlayFS，不会将 Kata binary 作为 runc 调用。节点重启、故障注入和性能基线
应单独验证；[2026-10-14 功能验证记录](../../tests/e2e/results/kata-qemu-2026-10-14.md)
覆盖 Rust runtime、io.containerd.kata-qemu.v2 与 ext4 local PVC。通用 io.containerd.kata.v2
和 Go Kata runtime 仅有配置/单元测试覆盖，动态 CSI PVC、节点/VM 故障恢复和性能仍待验证。

其他运行时通过各自 adapter 接入；gVisor 支持见 [独立使用指南](gvisor.md)，不会经过 Kata proxy。
