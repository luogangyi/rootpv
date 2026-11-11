# RootPV Helm Chart

该 Chart 部署 RootPV Controller、node-agent、OCI wrapper 和 Kata + QEMU task shim proxy，
保留原生 containerd overlayfs。node-agent 在带标节点上自动生成 catalog 对应的 Shadow handler
及 registry。镜像和 Chart 不安装 containerd、Kata、QEMU、guest kernel 或设备驱动。

## 安装

当前 annotation、label 和 CRD/RBAC 使用 openfuyao.cn；旧安装升级见
[域名迁移指南](../../docs/user-guide/domain-migration.md)。

使用包含当前实现的 RootPV 镜像；Kata 接入需要镜像中存在
/usr/local/bin/containerd-shim-rootpv-kata-v2。源码构建见 [部署指南](../../docs/user-guide/deployment.md)。

```bash
kubectl label node <node-name> openfuyao.cn/persistent-rootfs=true --overwrite

helm upgrade --install rootpv charts/rootpv \
  --namespace rootpv-system \
  --create-namespace \
  --set image.repository=<image-repository> \
  --set image.tag=<image-tag> \
  --set-string 'config.storagePolicy.allowStorageClasses[0]=<storage-class>' \
  --wait --timeout 10m
```

node-agent 在 /run/rootpv/rootfs 保存运行状态，在 /etc/rootpv 保存 0600 的
rootpv-runtime-adapters.yaml 和 rootfs-runtime-wrapper.yaml。

## 路径与运行时配置

| 参数 | 用途 | 默认值 |
| --- | --- | --- |
| image.repository / image.tag | Controller、agent 和 installer 的同一 RootPV 镜像 | rootpv / latest |
| runtimeAdapterDiscovery.enabled | 自动发现源 handler 并生成 catalog、Shadow RuntimeClass 与节点 registry | true |
| nodeAgent.nodeSelector | 选择安装 RootPV 的节点 | openfuyao.cn/persistent-rootfs=true |
| nodeAgent.overlayMode | 持久化 Overlay 模式；Kata 要求 flat-lowerdirs | flat-lowerdirs |
| nodeAgent.kubeletRoot | shim 和 agent 使用的真实 kubelet 路径 | /var/lib/kubelet |
| nodeAgent.hostPaths.kubelet | kubelet 宿主机目录挂载来源，应匹配 kubeletRoot | /var/lib/kubelet |
| nodeAgent.hostPaths.containerdLib | containerd 宿主机数据目录 | /var/lib/containerd |
| nodeAgent.allowedOverlaySnapshotRoots | 宿主机只读快照路径白名单 | /var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots |
| nodeAgent.runtimeConfigurator.restartCommand | Shadow handler 变更后的 containerd 重载/重启命令 | 见 values.yaml |
| config.storagePolicy.allowStorageClasses | 渲染到存储策略 ConfigMap 的类名配置；当前没有强制准入 allowlist 实现 | 空类名、rootpv-local |

例如使用自定义 kubelet/containerd 目录：

```yaml
nodeAgent:
  kubeletRoot: /custom/kubelet
  hostPaths:
    kubelet: /custom/kubelet
    containerdLib: /custom/containerd
  allowedOverlaySnapshotRoots:
  - /custom/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots
```

路径必须对应真实节点配置。安装会修改选定节点的 containerd 主配置并可能重启服务。

`/host` 挂载只读；`hostPaths.containerdEtc`、`hostPaths.etcRootpv` 和
`hostPaths.usrLocalBin` 通过独立可写挂载供节点代理更新配置和 wrapper。
节点代理仍为 privileged，不应将只读挂载视为宿主机安全隔离。
ConfigMap 权限通过安装命名空间的 Role 授予：Controller 可创建 catalog，
仅能读取/更新 `rootpv-runtime-adapters`，node-agent 仅可读取该 ConfigMap。
RuntimeClass 仍使用集群级权限。使用 `rbac.create=false` 时也需自行提供这些命名空间权限。

values.yaml 中 supportedRuntimeTypes 是当前类型清单；修改该列表不会为任意 shim 增加实现。
实际兼容性判断由 discovery 和 runtime adapter 代码执行。
config.storagePolicy 当前渲染到 ConfigMap，但 Controller 没有读取该文件来强制执行类名准入；
它不能替代对实际文件系统、PVC 模式和 OverlayFS 能力的验证。

## Kata + QEMU

先独立验证原 kata-qemu RuntimeClass 和 source handler。handler 必须显式配置
runtime_path、options.ConfigPath，且 Kata TOML 明确使用 QEMU。受支持的类型为
io.containerd.kata-qemu.v2，以及 QEMU 配置下的 io.containerd.kata.v2。

installer 同时安装 OCI wrapper 和 Kata proxy shim。node-agent 读取主配置与 imports，
克隆源 handler 的 options 和 annotations；runc 修改 BinaryName，Kata 修改 runtime_path。
原 RuntimeClass 的 scheduling、tolerations 和 overhead 保留到 Shadow RuntimeClass。
业务 Pod 继续填写 runtimeClassName: kata-qemu。
发现时若无法生成完整的 Kata 配置摘要，handler 会保持 unresolved（runtime_digest_unavailable）；
reconcile 阶段生成摘要失败会返回错误，停止此次配置和 registry 写入。排查步骤见 [Kata 指南](../../docs/user-guide/kata-qemu.md)。

rootfsd、Controller 和 installer 应使用同一版本镜像。仅启用 discovery 的当前安装路径
接入 Kata；`deploy/manifests` 直接安装现也使用同一套自动发现/catalog 和 proxy shim。

## 验证与测试

```bash
kubectl -n rootpv-system rollout status deploy/persistent-rootfs-controller --timeout=180s
kubectl -n rootpv-system rollout status ds/persistent-rootfsd --timeout=180s
kubectl get runtimeclass -l openfuyao.cn/managed-by=rootpv
kubectl -n rootpv-system get configmap rootpv-runtime-adapters -o yaml
kubectl get nodes -L openfuyao.cn/persistent-rootfs-ready \
  -L openfuyao.cn/rootpv-adapter-kata-qemu-v2
helm -n rootpv-system status rootpv
```

完整 Pod/PV/PVC 测试见 [Kata 清单](../../tests/e2e/manifests/kata-qemu.yaml)
和 [运行步骤](../../docs/user-guide/kata-qemu.md)。限定功能证据见
[2026-10-14 验证记录](../../tests/e2e/results/kata-qemu-2026-10-14.md)。

静态检查：

```bash
helm lint charts/rootpv
helm template rootpv charts/rootpv --namespace rootpv-system --include-crds
```

## 卸载

先清理 RootPV workload 和活跃写层租约，再卸载 Chart。节点 handler、生成的别名、
registry 与 Shadow RuntimeClass 当前需根据安装备份另行清理；完整步骤和共享安装边界见
[部署指南](../../docs/user-guide/deployment.md#卸载与节点配置清理)。

## gVisor

当前源码支持 io.containerd.runsc.v1，通过原生 shim 的配置文件 binary_name 使用 OCI wrapper，
无需额外 RootPV shim。TypeUrl=io.containerd.runsc.v1.options、绝对 ConfigPath 和 binary_name
必须显式配置；启动阶段的全局 /run/containerd/runsc/config.toml 或
/etc/containerd/runsc/config.toml 必须 grouping=true，按此优先级检查。
仅在 handler ConfigPath 设置 grouping 不够。生成的 /etc/rootpv/runsc/<adapter-id>.toml 为 0600，保留原 flags 并设置
overlay2=none，原配置不改写。使用 runtimeClassName: gvisor；详见
[gVisor 指南](../../docs/user-guide/gvisor.md)和 [完整测试清单](../../tests/e2e/manifests/gvisor.yaml)。
Chart 渲染的专用在线部署已验证真实准入、catalog 和 shim 自动退出，见
[限定组合记录](../../tests/e2e/results/gvisor-online-2026-10-20.md)。

## NVIDIA GPU

Chart 沿用自动发现的 runc-v2 NVIDIA handler，保留原 `BinaryName` 委托和设备注入配置。
节点须先安装驱动、Toolkit 和 device plugin，并验证普通 GPU Pod；Chart 不安装或切换这些组件。
GPU Operator 的 v3 导入配置按生效 CRI 表发现。节点使用自定义 containerd/kubelet 目录时，
同步配置 hostPaths、`kubeletRoot`、`allowedOverlaySnapshotRoots`，并确保容器内可访问原始绝对快照路径。
CDI/legacy、驱动与 MIG 要求见 [NVIDIA 指南](../../docs/user-guide/nvidia.md)，
可复用 [local PV 测试清单](../../tests/e2e/manifests/nvidia.yaml)。

## Ascend 910C

已安装的 Ascend Docker Runtime 通过 `io.containerd.runc.v2` 和绝对 `options.BinaryName` 自动发现，
无需增加 Chart 支持类型。ARM64 节点选择匹配架构的镜像，先通过普通 NPU Pod 的计算验收。
Chart 不安装厂商组件，也不修改 NPU 切分或默认 runtime；containerd v3 与 device plugin 的部署细节
见 [Ascend 指南](../../docs/user-guide/ascend.md)和 [完整测试 YAML](../../tests/e2e/manifests/ascend.yaml)。
Chart 不按 Ascend 驱动/runtime 版本过滤节点；这不代表所有版本均已兼容。
当前实测驱动 25.5.6、runtime/plugin 7.3.0、CANN 8.5.0 A3 和 containerd 2.1.1，
最低支持版本尚未确定，其他组合按 [版本要求](../../docs/user-guide/ascend.md#版本与未覆盖范围) 验证。
