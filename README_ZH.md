# RootPV

[English](README.md) · [贡献指南](CONTRIBUTING.md) · [许可证](LICENSE)

RootPV 为 Kubernetes 容器提供可持久化的可写根文件系统。它将 OverlayFS 的可写增量层保存到 PersistentVolumeClaim（PVC），同时继续由原生 containerd `overlayfs` snapshotter 管理不可变镜像层。

它适用于需要在 Pod 重建后保留容器内改动的开发工作空间、AI/ML 环境、交互式 Notebook，以及初始化耗时较长的工作负载。

> RootPV 不是镜像构建或备份系统，也不能替代应用数据持久化。生产镜像仍应通过常规的构建和发布流程交付。

## 工作原理

```text
Pod annotations + PVC
          |
          v
准入控制器 -- 选择 RootPV RuntimeClass，并注入 PVC anchor mount
          |
          v
原生 containerd overlayfs snapshotter -- 提供不可变镜像 lower layers
          |
          v
RootPV runtime adapter -- runc-v2 使用 OCI wrapper，Kata + QEMU 使用 task shim proxy
          |
          v
PVC：upperdir + workdir + metadata
```

RootPV 包含以下组件：

- `persistent-rootfs-controller`：校验显式启用的 Pod，并完成 RootPV 所需的变更。
- `persistent-rootfsd`：以 DaemonSet 运行，解析 PVC 挂载、管理元数据并持有单写租约。
- `rootfs-runtime-wrapper`：在调用真实 OCI runtime 前准备 OverlayFS 根文件系统。
- `containerd-shim-rootpv-kata-v2`：代理 Kata + QEMU 的 task 创建和删除，接入持久化根文件系统。
- 用于部署的 Helm Chart 和 Kubernetes manifests。

RootPV 仅使用 Kubernetes、containerd、OCI runtime-spec 和 Linux VFS 的公开接口。它不会 patch、替换或 fork containerd，也不接管 containerd 的镜像和 snapshot 垃圾回收。

## 当前范围与限制

- 使用原生 containerd `overlayfs` snapshotter 的 Linux 节点。
- 基于 PVC 的单写持久根文件系统。
- 严格校验基础镜像身份，防止将已有可写层意外复用到不兼容镜像。
- 基于 `io.containerd.runc.v2` 的 runtime adapter；节点已正确安装且可发现的设备专用 runtime 也可接入。
- Kata + QEMU task-shim adapter；源 handler 需显式配置 `runtime_path` 和 QEMU `ConfigPath`，见 [Kata 使用指南](docs/user-guide/kata-qemu.md)。

当前范围不包括：非 containerd 运行时、非 `overlayfs` snapshotter、rootless 或非特权节点安装、一个根文件系统的读写共享、跨镜像链自动 Reset/Rebase，以及将可写层自动转换为 OCI 镜像。

完整端到端基线覆盖 Linux x86_64、Kubernetes v1.29 系列、containerd 2.1.1（厂商构建）、
`io.containerd.runc.v2` 运行时，以及动态供给的 ReadWriteOnce 块存储 PVC。在相同平台上，官方
containerd 1.6.39、1.7.35、2.0.12 和静态 2.1.9 已额外完成动态 PVC 持久化与 Pod 重建验证；这些版本检查
尚未覆盖完整的恢复、升级、并发和性能用例。当前基线还完成了 rootfsd 与 containerd 服务重启各 3 轮、
节点重启 3 轮、10 实例并发隔离、同一路径互斥，以及 RootPV Helm 升级/回滚验证。

Kata + QEMU 的限定功能验证组合为 Kubernetes 1.34.3、containerd 2.1.1、Kata 4.2.0 Rust、
QEMU 11.0.1 和 ext4 local RWO PVC，已覆盖持久化、sidecar、单写互斥、rootfsd 重启与正常删除。
动态 CSI PVC、节点/VM 故障恢复和性能尚未验证，不能由该功能结果推导；见
[验证记录](tests/e2e/results/kata-qemu-2026-10-14.md)。

不支持将运行中的 containerd 2.1 状态直接降级为 1.6：候选 1.6.28 可以启动，但不能重用 2.1 的运行时
状态。跨大版本回退应在排空节点后按受控迁移流程重建运行时状态。当前 Helm 卸载会停止 RootPV 节点代理，
但不会自动移除已写入 containerd 配置的 RootPV runtime handler；该卸载清理能力仍待补充。

其他容器运行时、snapshotter、CPU 架构、存储后端、节点重启/故障恢复和设备专用运行时，尚未完成
同等范围的端到端验证。镜像可以构建为某一架构，并不等同于该架构已进入验证过的兼容范围。

## 兼容性验证矩阵

此表记录的是验证证据，而非理论兼容性。仅在对应验证记录发布后才会写入**已验证**列；
**未验证**列中的组合不构成支持承诺。

| 维度 | 组合或范围 | 已验证 | 未验证 |
| --- | --- | --- | --- |
| 基线平台 | linux/amd64；Kubernetes v1.29 系列；containerd 2.1.1（厂商构建）；`overlayfs`；原生 `io.containerd.runc.v2`；ext4 的 RWO 块存储 PVC | 功能和性能 E2E；rootfsd、containerd 服务和节点重启各连续 3 轮；10 实例并发隔离；同路径单写互斥；RootPV Helm 升级/回滚 | 跨 containerd 大版本的受控迁移；卸载后自动清理节点 runtime handler |
| Kubernetes 与 containerd | Kubernetes v1.27、v1.28、v1.30 及后续受支持系列；containerd 1.6、1.7、2.0 和其他 2.1 组合 | Kubernetes v1.29 + 官方 containerd 1.6.39、1.7.35、2.0.12 和静态 2.1.9：动态 PVC 持久化与 Pod 重建 | 其他全部配对；官方构建上的恢复、升级和性能 |
| CPU 架构与内核 | linux/arm64 及其他受支持 Linux 内核/发行版组合 | — | 基线用例与 OverlayFS 恢复覆盖 |
| 存储 | XFS 的 RWO 块存储 PVC | — | OverlayFS 挂载、持久化和清理覆盖 |
| runc-v2 adapter | `BinaryName` delegate 非 wrapper 的自定义 `io.containerd.runc.v2` runtime | — | adapter 发现、生命周期、信号、退出码和恢复 |
| GPU runtime | NVIDIA（环境具备时分别覆盖 MIG/vGPU）、AMD ROCm、Intel 和 MThreads | — | 设备工作负载、持久化、清理和各设备切分形态 |
| NPU runtime | Ascend 及其他 runc-v2 兼容加速器；整卡和虚拟/切分形态分别记录 | — | 厂商工作负载、持久化、清理和隔离 |
| Kata + QEMU | Linux AMD64；Kubernetes 1.34.3；containerd 2.1.1；Kata 4.2.0 Rust；QEMU 11.0.1；ext4 local RWO PVC | [持久化、sidecar 隔离、单写互斥、rootfsd 重启、普通 Kata 与清理](tests/e2e/results/kata-qemu-2026-10-14.md) | 动态 CSI PVC、节点/VM 故障恢复、性能与设备 |
| 其他独立 shim | Kata CLH、runsc 和 QEMU adapter 范围之外的 task shim | gVisor：RootPV opt-in 被拒绝；普通 gVisor Pod 正常运行且未被 RootPV 修改 | Kata CLH、gVisor RootPV 工作负载与其他独立 shim；Kata QEMU 接入见 [Kata 使用指南](docs/user-guide/kata-qemu.md) |

测试用例、验收标准和待寻找的环境见[兼容性验证方案与详细矩阵](docs/design/04-compatibility-validation-plan.md)。

## 安装与部署

RootPV 的协议键与 API group 使用 openfuyao.cn。已有安装升级前，请先阅读
[域名迁移指南](docs/user-guide/domain-migration.md)，同步更新组件、节点配置和业务 YAML。
新域名已在 Kubernetes v1.37.1、ext4 local PVC 上通过 [runc/Kata 限定冒烟](tests/e2e/results/domain-smoke-2026-10-14.md)，
该记录不代表完成整个平台兼容性验证。

### 前提条件

- 运行 Linux 节点和 containerd 的 Kubernetes 集群。
- 可提供适合 OverlayFS `upperdir`/`workdir` 的 filesystem 类型 ReadWriteOnce StorageClass。
- Helm 3 与 `kubectl`。
- 具备集群管理员权限。节点代理为特权组件，会在选定节点安装 runtime wrapper 和 RootPV runtime handler。
- 已明确选定可安装 RootPV 节点组件的节点；建议先从少量 canary 节点开始。

两种 Helm 安装路径都会仅在带标节点上更新 containerd runtime 配置，并在必要时 reload 或 restart
containerd。部署前请检查 `charts/rootpv/values.yaml` 中的节点路径、containerd socket 和重启设置，
并按集群运维规范安排发布窗口。

Kata 功能要求当前 proxy shim 和 Chart installer；使用本分支构建镜像，或核实 CI 制品包含这些改动。

存储策略 values 当前渲染为 ConfigMap，Controller 尚未读取该文件来强制执行 StorageClass allowlist。
请验证实际 PVC 文件系统和 OverlayFS 能力。

### 1. 使用社区 CI 制品快速开始

可直接使用 openFuyao CI 发布的镜像和 OCI Chart 进行快速评估。请将 `<storage-class>` 替换为上述选定的
StorageClass。

```bash
export ROOTPV_IMAGE_REPOSITORY=openfuyao-0qfqnk.swr-pro.myhuaweicloud.com/openfuyao/rootpv
export ROOTPV_IMAGE_TAG=latest
export ROOTPV_CHART=oci://openfuyao-0qfqnk.swr-pro.myhuaweicloud.com/charts/rootpv
export ROOTPV_CHART_VERSION=0.0.0-latest

kubectl label node <node-name> openfuyao.cn/persistent-rootfs=true --overwrite

helm upgrade --install rootpv "${ROOTPV_CHART}" \
  --version "${ROOTPV_CHART_VERSION}" \
  --namespace rootpv-system \
  --create-namespace \
  --set "image.repository=${ROOTPV_IMAGE_REPOSITORY}" \
  --set "image.tag=${ROOTPV_IMAGE_TAG}" \
  --set "image.pullPolicy=Always" \
  --set-string 'config.storagePolicy.allowStorageClasses[0]=<storage-class>' \
  --wait --timeout 10m
```

`latest` 和 `0.0.0-latest` 均为 CI 制品，内容可能随构建变化。需要可复现或生产部署时，请固定镜像
digest 和正式发布的 Chart 版本。安装后继续执行[验证部署](#4-验证部署)。

### 2. 构建并部署自定义制品

为目标架构构建镜像，并推送到选定节点可访问的镜像仓库：

```bash
make image IMAGE=<image-repository>:<image-tag>
make push IMAGE=<image-repository>:<image-tag>
```

如需发布多架构镜像，请先配置镜像仓库认证，再执行 `make push-multiarch IMAGE=<image-repository>:<image-tag>`。

镜像可用后，请在两种部署方式中选择其一：

- 常规安装使用下方 Helm 命令。Helm 负责 values 管理、发布记录、升级和卸载。
- 仅当无法使用 Helm，或 GitOps/IaC 工具必须接管渲染后的 Kubernetes 资源时，才使用第 3 节的原生
  manifests 方式。它替代下方 Helm 命令，不能与 Helm 在同一个 RootPV 安装中同时使用。

推荐的 Helm 路径是使用本地检出的 Chart 和刚刚构建的镜像进行部署。仅为需要运行 RootPV 节点代理的节点打标：

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

### 3. 备选方案：应用渲染后的 Kubernetes 清单

GitOps 或 kubectl 管理资源时，使用本分支 Chart 和第 2 节构建的镜像渲染清单。
Helm 仅在渲染阶段需要，目标安装无需 Helm release；同一组资源保持单一管理方。

```bash
helm template rootpv charts/rootpv \
  --namespace rootpv-system --include-crds \
  --set image.repository=<image-repository> \
  --set image.tag=<image-tag> \
  --set-string 'config.storagePolicy.allowStorageClasses[0]=<storage-class>' \
  > rootpv-rendered.yaml

kubectl create namespace rootpv-system --dry-run=client -o yaml | kubectl apply -f -
kubectl label node <node-name> openfuyao.cn/persistent-rootfs=true --overwrite
kubectl apply -f rootpv-rendered.yaml
```

deploy/manifests 的旧静态 installer 使用固定 runc/NVIDIA handler，不安装 Kata proxy。
自动发现与 Kata 安装应使用当前 Chart 的渲染结果。离线重新渲染会生成新 TLS 证书；
应用新 Secret 和 caBundle 时，需同时重启 Controller，使其加载匹配证书。
详细步骤见 [部署指南](docs/user-guide/deployment.md)。

### 4. 验证部署

任一部署路径完成后，执行以下检查：

```bash
kubectl -n rootpv-system rollout status deploy/persistent-rootfs-controller --timeout=180s
kubectl -n rootpv-system rollout status ds/persistent-rootfsd --timeout=180s
kubectl get runtimeclass -l openfuyao.cn/managed-by=rootpv
kubectl get nodes -l openfuyao.cn/persistent-rootfs-ready=true
```

### 5. 验证持久根目录工作负载

创建使用上述 StorageClass 的 PVC 后，通过目标容器、PVC volume、PVC 内相对路径和严格镜像策略
显式启用 RootPV：

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: rootpv-demo
  annotations:
    openfuyao.cn/persistent-rootfs: "true"
    openfuyao.cn/rootfs-container: app
    openfuyao.cn/rootfs-volume: rootfs-data
    openfuyao.cn/rootfs-path: containers/app
    openfuyao.cn/rootfs-base-image-policy: Strict
spec:
  containers:
    - name: app
      image: <your-image>
      command: ["/bin/sh", "-ec", "echo ready; sleep 3600"]
  volumes:
    - name: rootfs-data
      persistentVolumeClaim:
        claimName: rootpv-data
```

上述 YAML 用于已安装并启用 RootPV Mutating Webhook 的集群。保留 `volumes` 声明即可，
持久化 rootfs 无需用户手写 `volumeMounts`。在 Pod 保存之前，Webhook 会自动为 `app` 容器
添加以下字段（这是注入后的 Pod 配置片段，不是需要额外应用的清单）：

```yaml
env:
  - name: ROOTPV_POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
volumeMounts:
  - name: rootfs-data
    mountPath: /var/run/rootpv/rootfs-anchor/rootfs-data
    readOnly: true
    subPathExpr: .rootpv-anchor/$(ROOTPV_POD_NAME)/app
```

最终 Pod 需要这个 anchor 挂载：它引用 `rootfs-data`，使 kubelet 将 PVC 挂载到节点，
并提供 RootPV 定位 PVC 所需的挂载。`subPathExpr` 使用 Pod 名和容器名，仅将 PVC 中的
`<PVC>/.rootpv-anchor/rootpv-demo/app` 子目录挂载到容器的 anchor 路径；`readOnly: true`
使该挂载只读，不会将整个 PVC 或其中的 `upper`、`work`、`meta` 目录暴露给应用。
可以省略的是用户提交 YAML 中的这些字段，因为 Webhook 会自动补齐；最终 Pod 中注入的
anchor 挂载不能删除，仅声明 `volumes` 而没有容器挂载也不能替代它。

`openfuyao.cn/rootfs-path: containers/app` 指定的是 **PVC 内部的数据目录**，RootPV 在其中
保存 `upper`、`work` 和 `meta`。OCI wrapper 或 Kata proxy 将镜像层与持久化的 `upper` 合并，
作为容器根文件系统 `/`；该注解不会设置容器内的 `mountPath`。若把整个 `rootfs-data`
卷挂载到 `/data`，应用还会看到 `/data/containers/app/{upper,work,meta}`。
这个挂载对 rootfs 持久化没有必要，还会让应用直接修改 RootPV 的底层数据，因此不要添加
该挂载，也不要挂载这些内部目录。若应用需要独立的数据卷挂载，应另外声明业务 PVC 和挂载。

创建 Pod 后，可检查 Webhook 是否已注入 `ROOTPV_POD_NAME` 和只读 anchor：

```bash
kubectl get pod rootpv-demo -o yaml
```

更多配置和运维信息请参阅[文档索引](docs/README.md)、[部署指南](docs/user-guide/deployment.md)和 [Helm Chart 文档](charts/rootpv/README.md)。

### 6. Kata + QEMU 工作负载

提交原 RuntimeClass：

```yaml
spec:
  runtimeClassName: kata-qemu
```

QEMU adapter 将其映射为 persistent-rootfs-kata-qemu，保留原 scheduling 和 overhead。
PVC 仍通过 Webhook 注入的只读 anchor 挂载；未被选中的 sidecar 保持普通根文件系统。
Kata 发现时若无法编码 handler options 或读取摘要所需的配置，会报告 runtime_digest_unavailable；
reconcile 阶段生成摘要失败时返回错误，不安装该 handler。
原 handler 的安装要求、非默认路径和操作步骤见 [Kata 使用指南](docs/user-guide/kata-qemu.md)。
[完整测试 YAML](tests/e2e/manifests/kata-qemu.yaml) 包含 Namespace、PV、PVC 和双容器 Pod。
原 handler 名称不同于 kata-qemu 时，adapter ID 由 catalog 推导，检查实际生成结果。

## 验证结果

2026-09-03 的 runc 基线覆盖以下功能场景，见 [功能记录](tests/e2e/results/functional-2026-09-03.md)。
QEMU adapter 的独立功能证据见 [2026-10-14 Kata 记录](tests/e2e/results/kata-qemu-2026-10-14.md)。

| 功能测试用例 | 结果 |
| --- | --- |
| Controller、节点代理与 RootPV 就绪节点检查 | 通过 |
| 未显式启用 RootPV 的 Pod 保持普通运行时路径 | 通过 |
| 指向不存在目标容器的配置由准入阶段拒绝 | 通过 |
| 动态 PVC 工作负载注入预期 RuntimeClass 和 runtime adapter | 通过 |
| 可写根目录标记在 Pod 删除并重建后仍可读取 | 通过 |
| 测试命名空间与 PVC 回收 | 通过 |

性能测试使用 1 轮预热、每个路径 3 轮正式测量和每轮 64MiB 同步写入。两条路径固定使用相同
镜像、节点、写入量和 Pod 生命周期。下表为 [2026-09-03 runc 测量](tests/e2e/results/performance-2026-09-03.md)，
Kata 性能尚未测量：

| 指标 | 未启用 RootPV | 启用 RootPV | 性能损耗占比 |
| --- | ---: | ---: | ---: |
| Pod 创建至 Running（平均 / P95） | 5500 / 5700 ms | 14560 / 15553 ms | 平均 +164.73%；P95 +172.86% |
| 根文件系统顺序写入（平均 / P95） | 1910 / 1972 ms | 1995 / 2066 ms | 平均 +4.45%；P95 +4.77% |
| Pod 删除与运行时清理（平均 / P95） | 32336 / 32440 ms | 32247 / 32519 ms | 平均 -0.28%；P95 +0.24% |
| 根文件系统顺序写入吞吐量 | 33.50 MiB/s | 32.08 MiB/s | 4.24% |

未启用 RootPV 时写入容器可写层，启用 RootPV 时写入持久根目录。因此表中的差异表示持久根目录
的端到端成本，并非仅 runtime wrapper 的开销。这些数据仅为小样本参考而非性能 SLO；应仅与相同
节点、存储后端、镜像缓存状态和集群负载下的历史结果比较。

## 开发

项目使用 Go 1.24 或更高版本。

```bash
make test
make build
make helm-lint
make helm-template
```

提交修改前，请使用 `gofmt` 格式化 Go 代码并运行上述检查。贡献流程见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 安全边界

RootPV 是特权节点组件，会访问宿主机挂载、containerd 状态和 runtime 配置。请仅在可信集群和受控节点上安装。对于显式启用 RootPV 的工作负载，若身份、挂载或生命周期状态无法被安全确认，RootPV 会拒绝继续执行；未启用 RootPV 的工作负载保持原有运行时路径。

## 许可证

Copyright (c) 2026 OpenFuyao Contributors.

RootPV 采用 [木兰宽松许可证，第 2 版](LICENSE)（Mulan PSL v2）。
