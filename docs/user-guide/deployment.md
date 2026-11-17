# RootPV 部署指南

## 架构与前提

当前协议键统一使用 openfuyao.cn；已有安装升级前先阅读 [域名迁移指南](domain-migration.md)。

RootPV 默认在原生 containerd overlayfs snapshotter 上把容器可写层持久化到 PVC。
CRI-O + runc 后端改用 containers/storage overlay，安装值、备份、存储保护与卸载步骤见
[CRI-O 指南](cri-o.md)。下文安装命令及直接 YAML 默认面向 containerd，不能仅替换 socket 用于 CRI-O。
runc-v2 handler 使用 rootfs-runtime-wrapper 的 OCI CLI 路径；Kata + QEMU 使用
containerd-shim-rootpv-kata-v2 的 task API 代理路径。两者共用 admission、自动发现
catalog、rootfsd 和 PVC 单写者租约。RootPV 不安装 containerd、CRI-O、Kata、QEMU、驱动或 guest kernel，也不切换 kubelet endpoint。

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

同一组资源由 Helm release 或 GitOps 管理其中一种方式持有。`deploy/manifests` 的直接安装入口
也使用自动发现/catalog，步骤见下一节；不要同时由两种安装方式管理同一实例。

离线 helm template 会生成新的 TLS Secret 和 webhook caBundle。再次渲染并应用更新证书时，
同时重新启动 Controller，使其加载匹配的证书：

```bash
kubectl -n rootpv-system rollout restart deployment/persistent-rootfs-controller
```

## 直接使用 deploy/manifests

无需 Helm 时，可以继续使用 `kubectl apply -f deploy/manifests` 的安装入口。
这些清单现在与 Chart 使用同一套 runtime 自动发现：安装通用 wrapper/proxy，发布 inventory，
生成 catalog、shadow RuntimeClass、containerd handler 和本地 registry。
不再运行只生成 runc/NVIDIA 的旧 `configure-containerd-rootpv.sh`，也不预建固定 RuntimeClass。
已配置好的 Ascend runc-v2 handler 按相同方式接入。

Controller 仅在安装命名空间创建 catalog ConfigMap，并读取/更新 `rootpv-runtime-adapters`；
node-agent 仅可读取该 ConfigMap。RuntimeClass 仍使用集群级权限。
node-agent 的 `/host` 挂载只读；containerd 配置、`/etc/rootpv` 和 wrapper 安装目录
通过单独的可写挂载更新。节点代理仍为 privileged，这项收紧不构成宿主机安全隔离。

准备当前代码构建的镜像；ARM64 节点需要 ARM64 或多架构镜像。复制清单到本地发布目录，
把所有 `rootpv:latest` 替换成已推送的镜像标签。核对 DaemonSet 的宿主路径、
`allowedOverlaySnapshotRoots` 和 `--containerd-restart-command`；默认重启命令要求宿主使用 systemd。
非 systemd 宿主必须先替换该参数，并验证实际加载了新 runtime handler，再部署业务 Pod。

先应用 namespace、RBAC、ConfigMap 和 Service，准备 TLS 后再启用 Webhook：

```bash
# /path/to/rootpv-manifests 为已经修改镜像与节点路径的清单副本。
ROOTPV_MANIFESTS=/path/to/rootpv-manifests
kubectl apply -f "$ROOTPV_MANIFESTS/00-namespace.yaml"
kubectl apply -f deploy/crds
kubectl apply -f "$ROOTPV_MANIFESTS/10-rbac.yaml" -f "$ROOTPV_MANIFESTS/20-config.yaml" \
  -f "$ROOTPV_MANIFESTS/40-service.yaml"

ROOTPV_TLS_DIR=$(mktemp -d)
chmod 700 "$ROOTPV_TLS_DIR"
openssl req -x509 -nodes -newkey rsa:2048 -days 365 \
  -keyout "$ROOTPV_TLS_DIR/tls.key" -out "$ROOTPV_TLS_DIR/tls.crt" \
  -subj /CN=persistent-rootfs-webhook.rootpv-system.svc \
  -addext 'subjectAltName=DNS:persistent-rootfs-webhook.rootpv-system.svc,DNS:persistent-rootfs-webhook.rootpv-system.svc.cluster.local'
kubectl -n rootpv-system create secret tls persistent-rootfs-webhook-tls \
  --cert="$ROOTPV_TLS_DIR/tls.crt" --key="$ROOTPV_TLS_DIR/tls.key" --dry-run=client -o yaml | kubectl apply -f -
ROOTPV_CA=$(base64 < "$ROOTPV_TLS_DIR/tls.crt" | tr -d '\n')
sed "s|caBundle: Cg==|caBundle: $ROOTPV_CA|" "$ROOTPV_MANIFESTS/60-webhook.yaml" \
  > "$ROOTPV_TLS_DIR/webhook.yaml"
cp "$ROOTPV_TLS_DIR/webhook.yaml" "$ROOTPV_MANIFESTS/60-webhook.yaml"
rm -rf "$ROOTPV_TLS_DIR"

kubectl label node <node-name> openfuyao.cn/persistent-rootfs=true --overwrite
kubectl apply -f "$ROOTPV_MANIFESTS/50-controller-deployment.yaml" -f "$ROOTPV_MANIFESTS/50-daemonset.yaml"
kubectl -n rootpv-system rollout status deploy/persistent-rootfs-controller --timeout=180s
kubectl -n rootpv-system rollout status ds/persistent-rootfsd --timeout=180s
kubectl apply -f "$ROOTPV_MANIFESTS/60-webhook.yaml"
```

后续使用这份已配置镜像和 CA 的副本执行 `kubectl apply -f "$ROOTPV_MANIFESTS"`。
不要重新应用仓库中的占位 CA；更换证书时同步更新 Secret、caBundle 并重启 Controller。
Ascend 的源 handler、源 RuntimeClass、驱动/runtime/plugin 仍须预先安装，见 [Ascend 指南](ascend.md)。
业务 Pod 显式使用 `runtimeClassName: ascend`；资源名本身不会触发 Ascend 路由。

**已有旧静态实例迁移**：先停止使用该实例的 RootPV 工作负载，保留 PVC，并备份节点配置。
旧 `persistent-rootfs-*` RuntimeClass 的 handler 与新生成值不同，Kubernetes 不允许原地修改 handler；
在确认没有 Pod 引用后，按名称删除本实例原先静态创建的 RuntimeClass，再启动新 Controller 重建。
从发布目录移除旧 `30-runtimeclasses.yaml`，不要再次应用它，也不要删除原厂 `ascend`/`nvidia` 等源 RuntimeClass。
迁移后按下节检查 catalog 与 adapter 就绪，再重建业务 Pod；旧的厂商能力标签不能代替新 adapter 就绪标签。

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

## gVisor 接入

自动发现支持 io.containerd.runsc.v1。源 options 必须包含 TypeUrl 和绝对 ConfigPath，
shim TOML 中提供绝对 binary_name。生成的私有配置保留平台等选项，并设置 overlay2=none；
原生 shim 的全局启动配置还需 grouping=true，仅在 handler ConfigPath 设置不足以控制 shim 分组。
原生 gVisor handler 不变。原 RuntimeClass 为 gvisor 时，提交相同 RootPV 注解并使用
runtimeClassName: gvisor。要求 flat-lowerdirs，详见 [gVisor 指南](gvisor.md)。
真实 Webhook/catalog 与节点自动 reconcile 的限定验证见 [在线记录](../../tests/e2e/results/gvisor-online-2026-10-20.md)。

自动配置需要设置 `--containerd-restart-command`（Helm 已提供）；命令为空时，node-agent
会记录一次错误并停用 adapter 自动配置，不修改 handler、registry 或 adapter 就绪标签。

仅做手工隔离测试时，node-agent 可同时设置 --runtime-adapter-discovery=false 与
--publish-node-capabilities=false，避免自动改写 handler、registry 或节点标签；
这不构成正常自动发现安装路径，需要测试者预先准备路由和准入结果。默认仍启用 capability 发布。

## NVIDIA GPU 接入

先安装匹配的驱动、NVIDIA Container Toolkit 和 device plugin，验证普通 GPU Pod 能完成计算。
沿用原 `runtimeClassName: nvidia` 或已配置的模式专用 RuntimeClass，申请 GPU 资源并添加 RootPV 注解；
containerd 原生 CDI 也可使用节点默认 adapter。RootPV 保留设备分配与注入，不安装驱动或修改 MIG 切分。
接入步骤、CDI/legacy 区别、自定义节点路径和驱动升级边界见 [NVIDIA 指南](nvidia.md)，
整卡实测结果见 [验证记录](../../tests/e2e/results/nvidia-2026-11-08.md)。

## Ascend 接入

先验证原厂 runtime/device plugin 下的普通 NPU 计算，再用当前 Chart 或 `deploy/manifests` 自动发现 `ascend` runc-v2 handler。
ARM64 节点使用匹配架构的 RootPV 镜像；业务保留源 RuntimeClass 并添加 RootPV 注解。
containerd v3 配置、plugin 自身 RuntimeClass、芯片分配单位和版本边界见 [Ascend 指南](ascend.md)，
可复用 [完整 local PV 清单](../../tests/e2e/manifests/ascend.yaml)。
RootPV 不额外限定驱动/runtime 的数字版本；910C 已验证驱动 25.5.6、runtime/plugin 7.3.0、
CANN 8.5.0 A3 与 containerd 2.1.1。该组合不是最低版本要求，其他组合和升级路径须单独验收。
安装器执行成功不代表 handler 配置正确：核对生效配置为 `io.containerd.runc.v2`，并确认 plugin
实际通过 Ascend runtime 分配设备，详情见 [版本要求](ascend.md#版本与未覆盖范围)。

950DT 继续使用相同 runc-v2 接入方式；准备 26.1.0 plugin 的 UMDK、实际 socket 路径和资源键，
见 [950 接入说明](ascend.md#950dt-接入与当前验证状态)与 [诊断 YAML](../../tests/e2e/manifests/ascend-950.yaml)。
本环境按厂商方案配置 `udma` 并重启后完成实际计算与 RootPV 生命周期验证，见 [950 记录](../../tests/e2e/results/ascend-950-2026-11-12.md)；该参数配置不由 RootPV 自动修改。
