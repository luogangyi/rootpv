# CRI-O

CRI-O 接入沿用 RootPV 的注解、Webhook/catalog、单写租约和 Flat Overlay。
镜像层来自 CRI-O 的 containers/storage；RootPV 为活跃写层保存镜像引用，
并将 CRI-O 创建的工作目录等初始化内容放入独立的只读 lower。
PVC 中只保存应用的可写增量。

本分支的 CRI-O 范围为 **官方 CRI-O + conmon + runc**，不修改 CRI-O 或 runc 上游源码。
CRI-O + Kata/gVisor 的旧实验实现、诊断补丁与历史结果保留在独立
[`cri-o-with-kata-and-gvisor` 分支](https://gitcode.com/luogangyi/rootpv/tree/cri-o-with-kata-and-gvisor)，
不属于本分支的安装或验收范围。现有 containerd 的 Kata/gVisor/NVIDIA/Ascend 支持保持原有范围。
设计见 [CRI-O 适配设计](../design/07-cri-o-runtime-adapter-design.md)，
实际完成项和未覆盖项见 [在线验证记录](../../tests/e2e/results/cri-o-runc-2026-11-16.md)。

## 前提

- 固定验证基线为 Linux AMD64、Kubernetes 1.37、CRI-O 1.37.3、conmon、
  内核 OverlayFS、rootful、文件系统类型 PVC。尚未覆盖的版本和架构需要单独验证。
- 不支持 rootless、userns/idmapped rootfs、非空 SELinux mount label、
  FUSE/composefs、额外 image store、运行时内部拉取镜像或 checkpoint/restore。
- 节点预先安装并验证原生 runtime、CNI、CSI；Chart 不安装或切换 kubelet 的 CRI。
- 一个节点只运行一个管理该节点的 RootPV node-agent。混合集群可部署共享控制面和
  按节点标签区分的 agent，不能让两套 agent 同时写 registry/readiness。
- 启用前备份原 runtime 配置、服务配置和需要保留的工作负载/PVC。

CRI-O 配置必须关闭两条自动清空存储路径：

```toml
[crio]
internal_wipe = false
internal_repair = false
```

这是实际故障验证后的要求。1.37.3 的 internal_repair 会在异常退出后的修复失败时清空
store，独立于 internal_wipe。镜像引用只能保护正常 GC，不能保护 wipe。
RootPV 检测到上述任一项开启会拒绝启用新持久化根目录；不会替管理员修改这些全局选项。

## 安装参数

从本分支构建同一版本的 Controller、agent、OCI wrapper 与 storage helper：

```bash
docker build --platform linux/amd64 -t <registry>/rootpv:<tag> .
```

helper 是独立 Go 模块，固定 containers/storage v1.63.0，对应本次 CRI-O 基线；
其 Go 1.25 工具链不会升级主模块的 containerd 依赖。helper 需要宿主 root 权限和对
graphroot/runroot 的写权限，以使用 containers/storage 的锁和维护镜像引用，并非只读查询。
每次调用最多等待 30 秒；调用方更短的 deadline 优先。超时或锁错误会返回失败并保留 source
journal 供恢复流程重试，不强制解锁、删除引用记录或关闭 CRI-O 的 store。
已完成的在线用例未出现锁等待阻断，不代表任意负载下都没有锁竞争。
需要单独构建时：

```bash
cd tools/crio-storage
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -tags 'exclude_graphdriver_btrfs,exclude_graphdriver_zfs' \
  -o rootpv-crio-storage .
```

根目录的 `go test ./...` 不会遍历该独立模块。在 Linux 环境进入此目录，执行
`go test -tags 'exclude_graphdriver_btrfs,exclude_graphdriver_zfs' ./...` 可检查引用归属、
幂等释放、镜像身份和挂载传播选项，无需操作真实 CRI-O store。

准备 values；所有路径必须与节点正在运行的 CRI-O 和 kubelet 一致：

```yaml
image:
  repository: <registry>/rootpv
  tag: <tag>
nodeAgent:
  runtimeProvider: cri-o
  overlayMode: flat-lowerdirs
  hostPID: true
  nodeSelector:
    openfuyao.cn/persistent-rootfs: "true"
    openfuyao.cn/rootpv-cri: cri-o
  crio:
    socket: /var/run/crio/crio.sock
    configDir: /etc/crio/crio.conf.d
    storageRoot: /var/lib/containers/storage
    runRoot: /run/containers/storage
    sourceRoot: /var/lib/rootpv/crio-sources
    restartCommand: "nsenter -t 1 -m -u -i -n -p -r -w -- systemctl restart crio"
```

```bash
kubectl label node <node> openfuyao.cn/persistent-rootfs=true openfuyao.cn/rootpv-cri=cri-o
helm upgrade --install rootpv charts/rootpv -n rootpv-system --create-namespace -f crio-values.yaml
```

需要直接 YAML 时，用同一份 values 执行 helm template，再审核生成的 DaemonSet、
hostPath 与 RBAC；仓库默认 deploy/manifests 仍是 containerd 配置。

node-agent 只生成独立的 99-rootpv.conf、wrapper 别名和 registry，保留原生 handler。
CRI-O 1.37.3 的 SIGHUP 可能只更新配置接口，实际创建仍使用旧表；因此激活执行受控
服务重启，核对 daemon 的 boot ID/PID/start time 变化以及生效配置后才发布 readiness。
仅 registry/catalog generation 变化不会要求重启。
源 handler 消失或校验失败时，撤销 readiness，并将之前的 shadow/registry 标为 retired。
新的 create 会被拒绝，探测与旧容器清理入口保留；这些配置不会自动删除，排空后按下文清理。
恢复有效源配置后，reconcile 会重新启用对应 adapter。

## 使用与验证

业务 Pod 继续使用原生 RuntimeClass 和原有 openfuyao.cn 注解。
Webhook 会选择对应 Shadow RuntimeClass，保留原 scheduling、tolerations 与 overhead。
目录中的 runc-v2 等名称表示 runtime 接口家族，不表示 CRI-O 连接了 containerd daemon。

完整 local PV、RuntimeClass 与双容器示例见
[cri-o.yaml](../../tests/e2e/manifests/cri-o.yaml)。先创建其中的 Namespace、RuntimeClass、PV/PVC，
等待 `persistent-rootfs-crio-runc` 生成并就绪后再提交 Pod；不要把首次 catalog 同步延迟误判为准入故障。
可在 app 中创建 `/tmp/rootpv-restart` 验证同 Pod 重启，再读取 `/rootpv-start-count`。

```yaml
metadata:
  annotations:
    openfuyao.cn/persistent-rootfs: "true"
    openfuyao.cn/rootfs-container: app
    openfuyao.cn/rootfs-volume: rootfs
    openfuyao.cn/rootfs-path: containers/app
    openfuyao.cn/rootfs-base-image-policy: Strict
spec:
  runtimeClassName: <native-runtimeclass>
  containers:
  - name: app
    image: <image>
    command: ["/bin/sh", "-ec", "sleep 86400"]
  volumes:
  - name: rootfs
    persistentVolumeClaim:
      claimName: <filesystem-pvc>
```

先验证原生 Pod，再检查真实 Webhook 注入、根目录写入和 Pod 删除/重建。
并发使用相同 PVC/path 必须被单写租约拒绝；未选中的 sidecar 应使用新镜像根目录。
删除后核对 RootPV state、source journal、挂载与 runtime 进程是否正常退出。
动态 EBS 测试应串行创建、重建和删除 PVC。

CRI-O 会保留已退出的容器供日志查询，因此 OCI 写层回收使用正式 CRI 状态，
并保留后续 Remove 所需的委托路由。同 Pod 重启可能需要等待一次恢复扫描和 kubelet 的退避。

Strict 使用 OCI DiffID ChainID，并以 cri-o-overlay 区分后端。
不要直接将 containerd 的旧写层交给 CRI-O；跨 CRI 迁移尚未验证。

## 支持边界

发现器只接纳 `runtime_type = "oci"` 且入口文件名为 `runc` 的显式可执行路径，
不接纳平台路径覆盖。handler 名称可以自定义，shadow 保留原 handler 的 monitor、
cgroup、注解和安全配置，仅将 OCI 执行入口指向 RootPV wrapper。
部署前仍需管理员确认该路径确实是官方 runc；文件名检查不是二进制来源认证。

CRI-O 下的 crun、Kata、gVisor、NVIDIA、Ascend 和 conmon-rs 不在本次支持范围内，
不会发布对应 RootPV readiness；未启用 RootPV 的原生工作负载仍由管理员配置决定。
不能把 containerd 的设备测试结果推导为 CRI-O 设备支持。
跨 CRI 迁移同一持久化写层、SELinux、userns 和其他版本需另行验证。

从旧实验分支切换到本分支前，先排空 CRI-O 的 RootPV Pod，确认 state、source journal、
镜像引用和挂载清理完成，备份并移走旧 RootPV drop-in/registry，再部署新 agent。
不要直接删除仍被旧容器使用的 VM handler 或把本分支当作在运行 VM 的滚动升级。

## 回退与清理

停止新的 RootPV 调度并删除或迁移相关 Pod，确认租约、挂载、state 和镜像引用清理后，
再删除 RootPV drop-in 并重启 CRI-O。最后移除 wrapper 别名、registry 和 helper；
保留原生 handler 和 PVC 内容。
回退到不识别 CRI-O 状态的旧版 RootPV 前必须排空节点。
存在未清理状态时保留清理入口，不使用 storage prune/wipe、强制删锁或 lazy unmount。
