# gVisor 在线 Webhook/catalog 功能验收

日期：2026-10-20。实现提交：2c1f868；shim 启动分组检查：4dd897d。
环境名、节点名、地址、私有仓库、Pod/容器 ID 和原始日志仅保存在私有归档。
本记录是指定组合的在线功能证据，不替代完整发布兼容性认证。

## 固定组合与部署

| 项目 | 实测 |
| --- | --- |
| 平台 | Linux AMD64；宿主机内核 5.10（厂商构建） |
| Kubernetes / containerd | v1.37.1 / 2.1.1 |
| gVisor | 官方 release-20261012.0；systrap；内核 ABI 标识 4.19.0-gvisor |
| 源 RuntimeClass / handler | gvisor / runsc；io.containerd.runsc.v1 |
| 源配置 | 显式 runtime_path、TypeUrl、ConfigPath、binary_name；全局启动配置 grouping=true |
| Shadow | persistent-rootfs-gvisor / persistent-rootfs-runsc-v1；独立配置 overlay2=none |
| 存储 | 静态 ext4 local Filesystem + ReadWriteOnce PVC；Strict 策略 |
| 应用镜像 | 预缓存 nginx 1.21.4 测试镜像；三代使用相同内容 |

使用同一分支四个静态程序和缓存基础镜像离线构建 RootPV 测试镜像。
Controller、DaemonSet、Service、TLS、RBAC 来自 Chart 渲染，启用真实自动发现与 capability 发布。
为并行测试调整两个 WebhookConfiguration 的资源名，namespaceSelector 仅匹配专用测试 namespace，
Controller 和 node-agent 均固定到单一测试节点。未使用离线 admission helper 注入 Pod。
本次未验证发布镜像构建流水线或 Helm release 升级/回滚。

## 在线结果

| 用例 | 结果 |
| --- | --- |
| Inventory | node-agent 自动发现 runsc，resolved=true；库存 NodeUID 与节点 UID 一致 |
| Catalog 与 Shadow | Controller 自动生成目录和 Shadow RuntimeClass；保留源 scheduling，并增加 adapter 就绪选择器 |
| 节点 reconcile | 自动安装 handler/registry 并发布就绪；目录、registry 的 generation 和准入摘要一致 |
| 配置边界 | registry 与生成的 runsc 配置均为 0600；源 overlay2=root:self 保留，Shadow 为 none |
| 原生 gVisor | 普通双容器 Pod 2/2 Ready，未新增 RootPV 注解或 anchor；删除后相关运行时进程自动退出 |
| MutatingWebhook | 原始 Pod 使用 gvisor 且无内部证据；真实 server dry-run 和 CREATE 自动生成 adapter 证据、Shadow 路由及只读 anchor |
| 校验拒绝 | rootfs-container 不存在时 CREATE dry-run 被拒绝；伪造 adapter ID 的 UPDATE dry-run 被独立 ValidatingWebhook 拒绝，现有 Pod 未改变 |
| 三代重建 | 计数 1→2→3；current UID 改变，first UID 保留；payload、0640 权限和符号链接保留 |
| Sidecar | 无 RootPV anchor，看不到 app 标记，每代从新根目录启动 |
| 单写者 | 同 PVC/path 的竞争 Pod 因 persistent_upper_in_use 被阻止；原 app 数据保持可读 |
| CRI 与进程查询 | 真实 CRI stats 和 wrapper ps 查询成功 |
| node-agent 重启 | 删除测试 agent Pod，由 DaemonSet 重建；活跃 app 数据与计数保持，后续删除/重建正常 |
| 自动退出与租约 | 每代只有一个分组 shim；每次删除后 runsc/shim/gofer/Sentry 进程均为零，RootPV state 为空；lease=released，flock 可重新独占 |

在线测试的原生 Pod、三个持久化代次和竞争 Pod 均经正常 Kubernetes 删除清理，
没有人工终止运行时进程。该结果验证了 [全局 grouping 修正](gvisor-shim-lifecycle-2026-10-20.md)
在 RootPV 在线链路中的作用；首次隔离测试的人工清理事实保留在 [历史记录](gvisor-2026-10-20.md)。

## 恢复与回归边界

临时 Controller、DaemonSet、Service/TLS、RBAC、WebhookConfiguration、测试 namespace、PV/PVC
和新增 Shadow RuntimeClass 已删除。节点 containerd 主配置与安装前备份字节一致，测试安装的
wrapper、proxy、别名、registry 和生成配置已移除；节点 RootPV 标签与 inventory 恢复到测试前状态。
保留原生 gVisor 安装、源 RuntimeClass/handler、全局 grouping 修正、私有诊断镜像和 Retain 数据目录。

目标节点原 10 个 Pod 与既有 RootPV 的 2 个组件 Pod，其 UID、phase、Ready、restartCount、containerID
均未改变。既有 RuntimeClass 定义与原 Webhook 配置未改变，测试节点保持 Ready。

本次未进行全局配置失效注入或 Webhook 中断测试；相应在线拒绝/恢复仍需独立验证。
动态 CSI、性能、设备、KVM 平台、checkpoint、节点/Sentry/gofer 故障恢复也不在本次范围。
本地分组检查、优先级和摘要变化回归见 [shim 生命周期记录](gvisor-shim-lifecycle-2026-10-20.md)。

可复用清单：[gvisor.yaml](../manifests/gvisor.yaml)；操作步骤：[gVisor 指南](../../../docs/user-guide/gvisor.md)。
