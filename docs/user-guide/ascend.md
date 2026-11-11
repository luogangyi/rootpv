# Ascend 910C runtime

RootPV 通过现有 `io.containerd.runc.v2` adapter 委托 Ascend Docker Runtime。
device plugin 分配 NPU，原厂 runtime/hook 注入设备和驱动，RootPV 只替换选定容器的根文件系统。
不按资源名称猜测 runtime，不解析芯片 ID，也不在 PVC 中保存 NPU 计算或显存状态。
已验证组合与边界见 [910C 验证记录](../../tests/e2e/results/ascend-910c-2026-11-11.md)。

## 前提与接入

1. 安装适配硬件的驱动、Ascend Docker Runtime 和 device plugin，使用包含 A3 算子包的兼容 CANN 镜像。
2. 先用同一镜像、资源请求和源 RuntimeClass 验证普通 Pod 的实际 NPU 计算。
3. 使用当前 Helm Chart 或 `deploy/manifests` 的自动发现/catalog、`overlayfs` 和支持 OverlayFS 的 Filesystem PVC。
   ARM64 节点须部署 ARM64 或多架构 RootPV 镜像，并按部署指南设置节点安装标签。
4. 在业务 Pod 中保留 `runtimeClassName: ascend`，添加标准 RootPV 注解与 PVC。
   Webhook 自动改为 `persistent-rootfs-ascend`，shadow handler 委托原厂二进制。

以 containerd 2.x、配置 version 3 为例，源 handler 配置如下。将它合并到现有配置或已导入的文件中，
保留原节点默认 runtime；`SystemdCgroup` 应与 kubelet/原 runc handler 一致。

```toml
version = 3
[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.ascend]
  runtime_type = "io.containerd.runc.v2"
[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.ascend.options]
  BinaryName = "/usr/local/Ascend/Ascend-Docker-Runtime/ascend-docker-runtime"
  SystemdCgroup = true
```

```yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: ascend
handler: ascend
```

仅在装有 Ascend runtime 的节点部署此 handler；异构集群应为 RuntimeClass 配置适当的
`scheduling.nodeSelector`。`BinaryName` 必须是存在且可执行的绝对路径。
containerd 1.x version 2 使用 `io.containerd.grpc.v1.cri` 表，不要同时写入两个版本的表。

实测官方 7.3.0 安装器不支持本环境的 containerd v3 配置，并且 cgroup v1 分支尝试配置旧的
`io.containerd.runtime.v1.linux`。安装后必须检查生效配置，使用上面的 runc-v2 handler；
不能把安装器生成的旧 runtime type 作为 RootPV 的接入方式。
Chart 不安装驱动/runtime/device plugin，也不替管理员切换默认 runtime。

device plugin 7.3.0 根据自身环境中的 `ASCEND_DOCKER_RUNTIME=True` 判断是否通过厂商 runtime
注入设备，仅设置 `useAscendDocker=true` 不够。默认 runtime 保持 `runc` 时，应让 device plugin
自身使用 `runtimeClassName: ascend`，确认日志显示采用 Ascend runtime 分配模式。
该环境标记由原厂 runtime 注入，不应手工伪造来跳过部署检查。

## RootPV 如何接入 Ascend

配置内容需要适配 Ascend，生成配置的 Go 逻辑复用通用 runc-v2 adapter：

| 阶段 | 配置与责任 |
| --- | --- |
| 管理员准备 | 安装原厂组件，配置 `ascend` handler 的原厂 `BinaryName` 和源 `RuntimeClass/ascend` |
| node-agent 发现 | 读取 containerd 生效配置及 imports，解析原厂二进制，发布 `ascend-runc-v2` inventory |
| catalog 与节点配置 | 生成 `persistent-rootfs-ascend` RuntimeClass；复制源 handler 为 `persistent-rootfs-ascend-runc-v2`，保留原选项并将 `BinaryName` 换为 wrapper 别名；registry 保存原厂委托路径 |
| Webhook 准入 | 对开启 RootPV 的 Pod，将 `runtimeClassName: ascend` 改为 `persistent-rootfs-ascend`，注入 PVC anchor 和路由信息 |
| 创建容器 | wrapper/rootfsd 准备 PVC 写层，委托 Ascend runtime；device plugin 分配设备，原厂 runtime/hook 注入设备与驱动 |

```text
Pod: runtimeClassName=ascend + RootPV 注解 + NPU 资源请求
  → Webhook: runtimeClassName=persistent-rootfs-ascend
  → containerd: persistent-rootfs-ascend-runc-v2
  → rootfs-runtime-wrapper-ascend-runc-v2
  → ascend-docker-runtime → runc（原厂 hook 注入设备/驱动）
```

只请求 `huawei.com/Ascend910` 而省略 RuntimeClass，不会自动选择 Ascend；未启用 RootPV 的 Pod
不会被改成持久根目录 runtime。Pod 重建后文件可保留，芯片会重新分配，计算/显存状态不会保存在 PVC。

直接 YAML 安装现已接入同一条链路，见 [直接安装与旧实例迁移](deployment.md#直接使用-deploymanifests)。
两种安装方式都不负责安装 Ascend 驱动、原厂 runtime 或 device plugin。

## 资源分配与持久化

使用节点实际公布的资源键，例如 `huawei.com/Ascend910`。不要在业务容器手工指定
`ASCEND_VISIBLE_DEVICES` 或挂载整个宿主 `/dev` 来绕过 device plugin。
没有 NPU 请求的 sidecar 可显式设置 `ASCEND_VISIBLE_DEVICES=void`，并验证没有计算设备节点。
RootPV 不提供额外的设备安全沙箱；特权容器不在这一隔离保证内。

910C 一张物理卡可以包含多个计算芯片，资源数量不应直接解释为物理卡数。
本次 plugin 按芯片公布资源，单 Pod 请求 1 或 2 个资源均完成计算；请求 2 个资源不保证来自同一卡，
也不证明 HCCL、同卡配对或多机通信可用。需要这些拓扑约束时，先按厂商方案配置调度并单独验证。
重建 Pod 可能获得不同芯片，业务应按容器内逻辑设备编号重新初始化。

[完整测试 YAML](../../tests/e2e/manifests/ascend.yaml) 包含 Namespace、静态 local PV、PVC、NPU app 和普通 sidecar。
替换节点名，在该节点创建 `/var/lib/rootpv/ascend-test-data`，确认该目录所在文件系统支持 OverlayFS，
再选择与驱动兼容的 A3 镜像。local PV 声明的容量不是磁盘配额。

```bash
kubectl apply -f tests/e2e/manifests/ascend.yaml
kubectl -n rootpv-test-ascend wait --for=condition=Ready pod/ascend-rootpv-test --timeout=180s
kubectl -n rootpv-test-ascend logs ascend-rootpv-test -c app
kubectl -n rootpv-test-ascend delete pod ascend-rootpv-test --wait=true
kubectl apply -f tests/e2e/manifests/ascend.yaml
```

重建后 marker 应保持不变、计数递增，sidecar 重新使用临时根目录。
该清单检查设备注入和文件持久化；验收还应执行实际算子并核对结果，不能仅以设备节点或 `npu-smi` 为依据。
本次使用 CANN `aclnnAdd`，对每个分配设备逐元素比对 CPU 预期值，见验证记录。
完成后删除清单资源；`Retain` PV 的节点目录仍保留，确认数据无需保留后再清理专用目录。

## 版本与未覆盖范围

RootPV 没有硬编码 Ascend 驱动或 runtime 的版本号，但尚未确定最低支持版本，也不承诺
某个版本及其后续版本全部兼容。接入要求是可用的 runc-v2 handler、绝对 `BinaryName`、
正确的设备分配与原厂 hook；这些接口条件不能代替实际版本验证。

| 组件 | 已验证版本 |
| --- | --- |
| Ascend 驱动 / 固件 | 25.5.6 / 7.8.0.10.220 |
| Ascend Docker Runtime / device plugin | 均为 7.3.0 |
| CANN | 8.5.0，A3 算子包 |
| containerd / 底层 runc | 2.1.1 / 1.4.0 |

这是一组实测组合，不是最低版本清单。厂商的
[版本兼容表](https://github.com/Ascend/mind-cluster/blob/master/docs/zh/release_notes.md)
将 MindCluster 7.3.0 对应到 CANN 8.5.x、HDK 25.5.x，并要求组件配套使用；
该系列范围不表示 RootPV 已测试每一个补丁版本。更换版本时，先验证同一镜像的普通 NPU Pod，
再验证 RootPV 下的实际计算、持久化重建、sidecar 隔离和删除清理。

RootPV 不链接 CANN/DCMI，也不主动复制宿主驱动。每次创建容器由当前 Ascend runtime/hook 注入驱动。
驱动、固件、CANN A3 算子包和 device plugin 仍须遵循厂商兼容要求；固定版本通过不代表其他版本或升级路径通过。
驱动升级前停止 NPU Pod，按厂商流程升级并检查宿主健康，再分别验证普通 Pod 和 RootPV Pod 的计算与清理。
持久化目录中的旧应用库、环境配置或手工复制的驱动文件可能遮蔽新库，不要在其中安装宿主驱动包。

本次未改变硬件分区模式，未验证 vNPU、动态切分、共享超分、CDI、HCCL、多机训练、驱动升级、
节点重启/断电恢复、动态 CSI 和性能。不得把非切分芯片的结果外推到这些模式。

参考：[官方 MindCluster 源码](https://gitcode.com/Ascend/mind-cluster/tree/v7.3.0)、
[CANN A3 镜像构建](https://github.com/Ascend/cann-container-image/blob/main/cann/8.5.0-a3-ubuntu22.04-py3.11/Dockerfile)。
