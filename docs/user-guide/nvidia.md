# NVIDIA GPU runtime

RootPV 通过现有 `io.containerd.runc.v2` adapter 委托节点上的 NVIDIA Container Runtime。
GPU 分配仍由 Kubernetes device plugin 完成；RootPV 保留 OCI 设备、挂载、环境变量和 hooks，
仅切换选定容器的根文件系统。无需在 RootPV 内配置 GPU 型号、驱动库目录或设备 UUID。
已验证组合和未覆盖范围见 [NVIDIA 验证记录](../../tests/e2e/results/nvidia-2026-11-08.md)。

## 前提与接入

- 节点已安装匹配 GPU 的驱动、NVIDIA Container Toolkit 和 device plugin。
- 先验证同一镜像、同一 GPU 资源请求、不带 RootPV annotations 的普通 Pod 能完成 GPU 计算。
- 使用当前 Helm Chart 的自动发现与 catalog；原 handler 的 `options.BinaryName` 必须是可执行的绝对路径。
  GPU Operator 的 `/usr/local/nvidia/toolkit/nvidia-container-runtime` 入口可以直接使用，保留其配套配置。
- 使用 `overlayfs` 和兼容 OverlayFS 的 Filesystem PVC。节点自定义 containerd/kubelet 数据目录时，
  同步配置 Chart 的 hostPaths、`kubeletRoot` 和 `allowedOverlaySnapshotRoots`，容器内须能访问原始绝对快照路径。

支持以下接入方式，必须先满足对应原生运行时的配置要求：

| 设备注入方式 | Pod 配置 | RootPV 路由 |
| --- | --- | --- |
| containerd 原生 CDI | 可以省略 RuntimeClass，申请 GPU 资源 | 节点默认 adapter；保留 containerd 注入的设备和 hooks |
| NVIDIA CDI runtime | `runtimeClassName: nvidia` 或已有的 `nvidia-cdi` | 克隆源 handler，委托原 `BinaryName` |
| NVIDIA legacy hook | 已配置的 `nvidia-legacy`，或明确设为 `legacy` 模式的 `nvidia`；device plugin 使用 envvar 策略 | 委托原 legacy runtime，由其执行 NVIDIA hook |

NVIDIA handler 名称不用于猜测设备类型。仅填写 `nvidia.com/gpu` 不会把不支持 GPU 的默认 runtime 自动变成 NVIDIA runtime。
GPU Operator 使用 v2 主配置并导入 v3 配置时，RootPV 按生效版本选择 v3 CRI runtime 表，忽略失效的旧表，保留源配置。

device plugin 的分配策略与 NVIDIA runtime 的注入模式是两项配置。Toolkit 1.19 的 `auto` 模式可能使用
[JIT-CDI](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/1.19.1/release-notes.html)，
因此 `envvar` 分配不代表一定执行 legacy hook；应检查实际 OCI hooks。
本次 legacy 对照还发现，环境额外启用的 GDS/GDRCopy/MOFED 可在 OCI 中追加 `NVIDIA_VISIBLE_DEVICES=void`，
使普通 Pod 的 legacy hook 跳过驱动注入。基础 GPU 验收在 Pod 中将 `NVIDIA_GDS`、`NVIDIA_GDRCOPY`、
`NVIDIA_MOFED` 设为 `disabled` 后通过；这些额外功能须另行验证。

普通 GPU 业务容器申请 `resources.limits`，不要手工设置 `NVIDIA_VISIBLE_DEVICES=all` 绕过设备分配。
未申请 GPU 的 sidecar 应使用普通镜像，或显式设置 `NVIDIA_VISIBLE_DEVICES=void`，避免 CUDA 镜像的 `all` 默认值暴露额外设备。
RootPV 不提供额外的 GPU 安全沙箱，也不为特权容器约束设备访问。

## 完整测试清单

[nvidia.yaml](../../tests/e2e/manifests/nvidia.yaml) 包含 Namespace、静态 local PV、PVC、GPU app 和普通 sidecar。
替换节点名称，在目标节点创建 PV 目录，并选择与 GPU/驱动匹配的镜像；local PV 的容量声明不是磁盘配额。

```bash
kubectl apply -f tests/e2e/manifests/nvidia.yaml
kubectl -n rootpv-test-nvidia wait --for=condition=Ready pod/nvidia-rootpv-test --timeout=180s
kubectl -n rootpv-test-nvidia logs nvidia-rootpv-test -c app
kubectl -n rootpv-test-nvidia exec nvidia-rootpv-test -c app -- nvidia-smi
kubectl -n rootpv-test-nvidia delete pod nvidia-rootpv-test --wait=true
kubectl apply -f tests/e2e/manifests/nvidia.yaml
```

重建后 marker 不变、计数递增，sidecar 从新根目录启动。GPU UUID 可能重新分配；持久化文件不绑定某张 GPU。
`nvidia-smi` 仅验证管理接口，验收时还应运行适合该 GPU 的 CUDA/PyTorch 计算并核对结果。
完成后删除测试 namespace/PV；`Retain` 策略保留节点目录，确认无需数据后再清理该专用目录。

## 驱动版本与升级

RootPV 不链接 NVML/CUDA，也不主动复制宿主驱动。每次创建容器时，由当前 Toolkit/CDI 注入节点的驱动文件。
应用镜像的 CUDA、GPU 架构支持、内核驱动和 Toolkit 仍必须相互兼容；RootPV 不绕过 `NVIDIA_REQUIRE_CUDA` 检查。
按 [NVIDIA CUDA 兼容说明](https://docs.nvidia.com/deploy/cuda-compatibility/minor-version-compatibility.html)
选择组合，不能仅凭驱动主版本推断所有 GPU 或 CUDA 特性均可用。

升级驱动前停止 GPU Pod，按 NVIDIA 流程完成驱动升级，确认宿主 `nvidia-smi` 正常，再刷新 CDI 并重新创建 Pod。
Toolkit 生成的符号链接、挂载目标和 `ld.so.cache` 等普通文件可能留在 PVC 写层，升级后仍须检查实际加载的库和 GPU 计算。
不要在持久化根目录内安装宿主驱动包或将注入的驱动库复制为应用依赖；自行保存的旧库、符号链接、`LD_LIBRARY_PATH`
和 CUDA compatibility 包仍可能遮蔽新驱动，应随应用维护。保留 PVC 不等于保存 GPU 进程、显存或驱动状态。

[Toolkit CDI 文档](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/cdi-support.html)
说明 v1.18 起提供自动刷新服务，但驱动移除和 MIG 重配置仍需主动刷新。
GPU Operator 管理的 CDI 文件应通过该 Operator/device plugin 的流程重新生成，避免另写同名设备定义造成冲突。
实际驱动升级/降级仍需单独验收；一次固定版本测试不能代表所有驱动分支。

## MIG 与其他分区模式

MIG 需要支持该能力的 GPU、驱动和已创建的 GPU/Compute Instances。
按 [NVIDIA MIG 前提](https://docs.nvidia.com/datacenter/tesla/mig-user-guide/getting-started-with-mig.html)
配置 device plugin：`single` 策略通常公布 `nvidia.com/gpu`，`mixed` 则公布具体的 `nvidia.com/mig-<profile>`。
以节点实际 `allocatable` 为准，替换 YAML 中的资源键；无需修改 RootPV adapter 或手写 MIG UUID。

RootPV 不解析 MIG UUID，因此不会依赖旧驱动 `MIG-GPU-…/GI/CI` 与新驱动 `MIG-…` 的命名差异。
CUDA 对 MIG 实例枚举的限制随驱动变化，参见 [MIG 设备命名与枚举](https://docs.nvidia.com/datacenter/tesla/mig-user-guide/mig-device-names.html)。
重配置 MIG 前先删除相关 Pod，再重新生成设备清单、等待 device plugin 更新资源并重建 Pod。
验收应检查分配实例的计算、显存上限、相邻实例隔离、持久化重建和清理。

RTX 5090 不提供 MIG。本次测试硬件不能验证 MIG、vGPU 或 time-slicing 的隔离与生命周期，不能将完整 GPU 的结果外推到这些模式。
