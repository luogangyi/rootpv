# RootPV 部署指南

## 架构与前提

当前协议键统一使用 openfuyao.cn；已有安装升级前先阅读 [域名迁移指南](domain-migration.md)。

RootPV 在原生 containerd overlayfs snapshotter 上把容器可写层持久化到 PVC。
runc-v2 handler 使用 rootfs-runtime-wrapper 的 OCI CLI 路径；Kata + QEMU 使用
containerd-shim-rootpv-kata-v2 的 task API 代理路径。两者共用 admission、自动发现
catalog、rootfsd 和 PVC 单写者租约。RootPV 不安装 containerd、Kata、QEMU、驱动或 guest kernel。

安装前确认目标节点的原 runtime 可运行普通 Pod，PVC 使用适合 OverlayFS upper/work 的
Filesystem + ReadWriteOnce 存储，并验证实际文件系统支持所需 OverlayFS 选项。

运行状态位于 /run/rootpv/rootfs，节点 registry 与 wrapper 配置位于 /etc/rootpv。
业务 Pod 的 PVC anchor 为 .rootpv-anchor/<pod>/<container>，Webhook 注入
ROOTPV_POD_NAME 和只读 subPathExpr mount。

## 构建和安装

Kata 功能需要包含新 proxy shim 的镜像。使用本分支源码构建并推送到节点可访问的仓库，
不要仅以 latest 标签推断镜像包含本分支改动：

```bash
make image IMAGE=<image-repository>:<image-tag>
make push IMAGE=<image-repository>:<image-tag>

kubectl label node <node-name> openfuyao.cn/persistent-rootfs=true --overwrite

helm upgrade --install rootpv charts/rootpv \
  --namespace rootpv-system \
  --create-namespace \
  --set image.repository=<image-repository> \
  --set image.tag=<image-tag> \
  --set-string 'config.storagePolicy.allowStorageClasses[0]=<storage-class>' \
  --wait --timeout 10m
```

installer 把 OCI wrapper 和 Kata proxy shim 安装到选定节点的 /usr/local/bin。
node-agent 读取 containerd 主配置及 imports，生成 Shadow handler、0600 registry 和
adapter 就绪标签；源 handler 与导入文件保持原状。配置变更后的 containerd 重载/重启由
nodeAgent.runtimeConfigurator.restartCommand 控制。

如果 kubelet 或 containerd 数据目录不同于默认值，通过 values 文件设置：

```yaml
nodeAgent:
  kubeletRoot: /custom/kubelet
  hostPaths:
    kubelet: /custom/kubelet
    containerdLib: /custom/containerd
  allowedOverlaySnapshotRoots:
  - /custom/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots
```

kubeletRoot 必须是 kubelet 实际使用的宿主机路径；hostPaths.kubelet 与其匹配。
allowedOverlaySnapshotRoots 同样使用 shim 在宿主机看到的真实路径。
更多参数见 [Helm Chart 说明](../../charts/rootpv/README.md)。
当前 config.storagePolicy 仅渲染为 ConfigMap，Controller 尚未读取该文件来强制执行
StorageClass allowlist；设置类名不能代替存储能力验证或作为已落实的准入隔离保证。

## GitOps 或直接应用渲染后的清单

需要 Kubernetes YAML 时，使用本分支 Chart 渲染当前安装资源。目标集群只需要 kubectl；
Helm 只在渲染阶段使用：

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

同一组资源由 Helm release 或 GitOps 管理其中一种方式持有。deploy/manifests 的旧静态安装器
仍采用固定 runc/NVIDIA handler，不是当前 Kata 自动发现安装路径。

离线 helm template 会生成新的 TLS Secret 和 webhook caBundle。再次渲染并应用更新证书时，
同时重新启动 Controller，使其加载匹配的证书：

```bash
kubectl -n rootpv-system rollout restart deployment/persistent-rootfs-controller
```

## 检查就绪

```bash
kubectl -n rootpv-system rollout status deploy/persistent-rootfs-controller --timeout=180s
kubectl -n rootpv-system rollout status ds/persistent-rootfsd --timeout=180s
kubectl get runtimeclass -l openfuyao.cn/managed-by=rootpv
kubectl get nodes -l openfuyao.cn/persistent-rootfs-ready=true
kubectl -n rootpv-system get configmap rootpv-runtime-adapters -o yaml
```

对于 Kata，还需出现 persistent-rootfs-kata-qemu，且目标节点带有
openfuyao.cn/rootpv-adapter-kata-qemu-v2 标签。Pod 能力标签与具体 adapter 就绪标签均需确认。
若 inventory 报告 runtime_digest_unavailable，检查 Kata ConfigPath 的可读性和 node-agent 日志；
摘要生成失败的 handler 不会被标记 resolved，reconcile 阶段的摘要错误会阻止此次配置和 registry 写入。

## 启用持久 rootfs

已有名为 rootpv-data 的 PVC 时，提交：

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: rootpv-demo
  annotations:
    openfuyao.cn/persistent-rootfs: "true"
    openfuyao.cn/rootfs-container: app
    openfuyao.cn/rootfs-volume: data
    openfuyao.cn/rootfs-path: containers/app
    openfuyao.cn/rootfs-base-image-policy: Strict
spec:
  containers:
  - name: app
    image: <your-image>
    command: ["/bin/sh", "-ec", "echo ready; sleep 3600"]
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: rootpv-data
```

Webhook 向目标容器注入只读 PVC anchor，并选择 Shadow RuntimeClass。用户提交的 YAML
可只声明 volumes；不要手工删除最终 Pod 中的 anchor，也不要把持久写层的 backing PVC
整体挂到 /data 供应用直接修改 upper/work/meta。

选择设备 runtime 时使用其原 RuntimeClass，例如 nvidia；选择 Kata + QEMU 时增加
spec.runtimeClassName: kata-qemu。Kata 由原 RuntimeClass 保留节点选择和资源开销，
非目标 sidecar 使用普通根文件系统。

完整 Kata 测试清单与重建检查见 [Kata 使用指南](kata-qemu.md)，支持边界见
[兼容性方案](../design/04-compatibility-validation-plan.md)。

## 卸载与节点配置清理

先删除所有 RootPV workload，确认 runtime state、merged 挂载和 PVC 租约已释放，
再执行：

```bash
helm uninstall rootpv -n rootpv-system
kubectl delete namespace rootpv-system --ignore-not-found
```

Helm uninstall 当前不会自动恢复 containerd 配置或删除生成的节点 handler、别名和
RootPV Shadow RuntimeClass。需要按安装前备份完成节点配置恢复、验证 containerd 配置
实际生效，并在确认无存量 task 后移除本次安装的 RootPV 文件。保留原 runc/Kata handler、
原 RuntimeClass 和原 Kata/QEMU 安装；共享安装不能直接照搬临时测试实例的全量清理步骤。
