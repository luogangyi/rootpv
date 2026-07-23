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
RootPV OCI runtime wrapper -- 挂载单层 OverlayFS，再委托真实 OCI runtime
          |
          v
PVC：upperdir + workdir + metadata
```

RootPV 包含以下组件：

- `persistent-rootfs-controller`：校验显式启用的 Pod，并完成 RootPV 所需的变更。
- `persistent-rootfsd`：以 DaemonSet 运行，解析 PVC 挂载、管理元数据并持有单写租约。
- `rootfs-runtime-wrapper`：在调用真实 OCI runtime 前准备 OverlayFS 根文件系统。
- 用于部署的 Helm Chart 和 Kubernetes manifests。

RootPV 仅使用 Kubernetes、containerd、OCI runtime-spec 和 Linux VFS 的公开接口。它不会 patch、替换或 fork containerd，也不接管 containerd 的镜像和 snapshot 垃圾回收。

## 当前范围

- 使用原生 containerd `overlayfs` snapshotter 的 Linux 节点。
- 基于 PVC 的单写持久根文件系统。
- 严格校验基础镜像身份，防止将已有可写层意外复用到不兼容镜像。
- 基于 `io.containerd.runc.v2` 的 runtime adapter；节点已正确安装且可发现的设备专用 runtime 也可接入。

首个版本不支持：非 containerd 运行时、非 `overlayfs` snapshotter、rootless 或非特权节点安装、一个根文件系统的读写共享、跨镜像链自动 Reset/Rebase，以及将可写层自动转换为 OCI 镜像。

## 快速开始

### 前提条件

- 运行 Linux 节点和 containerd 的 Kubernetes 集群。
- 可动态或静态提供 filesystem 类型 PVC 的存储类；该 PVC 必须适合承载 OverlayFS 的 `upperdir` 和 `workdir`。
- Helm 3 与 `kubectl`。
- 已明确选定可安装 RootPV 节点组件的节点。

构建并推送 RootPV 镜像后，为需要运行节点组件的节点打标：

```bash
kubectl label node <node-name> ecloud.com/persistent-rootfs=true --overwrite

helm upgrade --install rootpv charts/rootpv \
  --namespace rootpv-system \
  --create-namespace \
  --set image.repository=<image-repository> \
  --set image.tag=<image-tag>

kubectl -n rootpv-system rollout status deploy/persistent-rootfs-controller --timeout=180s
kubectl -n rootpv-system rollout status ds/persistent-rootfsd --timeout=180s
```

创建 PVC 后，通过目标容器、PVC volume、PVC 内相对路径和严格镜像策略显式启用 RootPV：

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: rootpv-demo
  annotations:
    ecloud.com/persistent-rootfs: "true"
    ecloud.com/rootfs-container: app
    ecloud.com/rootfs-volume: rootfs-data
    ecloud.com/rootfs-path: containers/app
    ecloud.com/rootfs-base-image-policy: Strict
spec:
  containers:
    - name: app
      image: <your-image>
      command: ["/bin/sh", "-ec", "echo ready; sleep 3600"]
      volumeMounts:
        - name: rootfs-data
          mountPath: /data
  volumes:
    - name: rootfs-data
      persistentVolumeClaim:
        claimName: rootpv-data
```

更多配置和运维信息请参阅[文档索引](docs/README.md)、[部署指南](docs/user-guide/deployment.md)和 [Helm Chart 文档](charts/rootpv/README.md)。

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
