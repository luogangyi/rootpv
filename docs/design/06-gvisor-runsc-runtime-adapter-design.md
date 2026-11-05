# gVisor runsc runtime adapter

日期：2026-10-20。范围：containerd 的 io.containerd.runsc.v1、Linux、overlayfs、RootPV flat-lowerdirs。

## Shim 启动分组

原生 shim 的 manager.Start 在 CreateTask 之前调用 GetRuntimeOptions，只读取固定的全局配置路径，
此时不会读取 handler 的 ConfigPath。对照实测：仅在 ConfigPath 设置 grouping=true 时，
双容器 Pod 启动三个 shim，删除后两个空闲 shim 残留；在全局配置启用分组后，
同一 Pod 只启动一个 shim，删除后自动退出。

RootPV 要求在 /run/containerd/runsc/config.toml 或 /etc/containerd/runsc/config.toml 显式
设置 grouping=true，按原生 shim 的优先级读取；第一份存在的文件不满足条件时不得回退。
该文件内容和路径纳入节点解析摘要。RootPV 验证此条件，不自动改写共享的原生全局配置。

## 接入边界

保留原 containerd-shim-runsc-v1，由它负责 sandbox、gofer、exec、task 生命周期与日志。
在它调用 runsc 的 OCI CLI 处使用现有 RootPV wrapper，复用 PVC 身份、镜像链、单写者租约与正常卸载流程。
该类型使用 gVisor shim TOML 的 binary_name，不使用 runc shim 的 options.BinaryName。

自动发现和安装要求合并 imports 后只有一个 CRI runtime 配置表：
`io.containerd.grpc.v1.cri` 或 `io.containerd.cri.v1.runtime`。两者并存时停止自动配置，
由管理员确认当前 containerd 使用的插件并清理过时表后再接入。

源 handler 必须显式配置 options.TypeUrl=io.containerd.runsc.v1.options、绝对 ConfigPath，
配置内提供绝对 binary_name。发现和 reconcile 均验证真实可执行路径，并将 handler、shim 配置内容
纳入节点摘要。缺失或不安全配置失败关闭，不猜测 runsc 路径。

## Shadow handler

每个 adapter 生成 rootfs-runtime-wrapper-<adapter-id> 别名与独立 runsc shim 配置。
保留源 shim 配置字段、runsc flags、runtime_path、annotations、RuntimeClass scheduling 和 overhead，
仅将生成配置的 binary_name 替换为 wrapper 别名，并设置 runsc_config.overlay2=none。
源 handler 与源配置文件保持原状。生成配置路径位于节点 registry 同目录的 runsc 子目录，权限 0600。

关闭 gVisor 内部临时 overlay 是持久化正确性的必要条件：应用写入必须经过 gofer 落到 RootPV merged，
否则 runsc 默认临时 overlay 会隐藏 PVC 写层。sandbox 和未选中的 sidecar 仍使用普通 containerd 快照，
其写入随各自快照清理；普通 gVisor Pod 继续使用原 handler 和原 overlay 配置。

目标容器要求 flat-lowerdirs。拒绝可替换根文件系统的 dev.gvisor.spec.rootfs.* annotations，
并检查执行参数中的 overlay2 必须为 none，避免绕过持久化路径。rootfsd 不可用或配置摘要不匹配时
不得退回普通根文件系统。

## 生命周期与验证

复用 rootpv-delegation-v1 状态，固定真实 runsc 和完整全局 flags；删除与恢复继续调用该 runsc。
创建失败需确认 runtime 清理及 merged 卸载后释放租约，普通进程命令直接委托。
删除和恢复先删除 runsc/gofer，关闭根目录引用，再正常卸载并验证挂载消失，最后释放租约。
events --stats、ps --format、wait 与 exec 的 shim 参数都应保留容器身份并路由到固定 delegate。

单元测试覆盖发现、生成配置、摘要变化、原配置保持、缺失配置、普通/目标容器路由和不安全 override。
节点验证先独立运行原 gVisor Pod，再测试双容器、两轮重建、内容/权限/符号链接、单写者互斥、
rootfsd 重启恢复、普通 Pod、删除清理和原有工作负载状态。结果另存脱敏记录。
已完成 [真实在线 Webhook/catalog 功能验收](../../tests/e2e/results/gvisor-online-2026-10-20.md)：
三代重建、准入校验、摘要一致性和分组 shim 自动退出均通过，测试资源及节点配置已恢复。
动态 CSI、性能、GPU、checkpoint、节点或 Sentry/gofer 崩溃不由一次功能验证推导。

参考：[gVisor shim 配置](https://gvisor.dev/docs/user_guide/containerd/configuration/)、
[gVisor 文件系统](https://gvisor.dev/docs/user_guide/filesystem/)。

本版本将配置读取固定为单次快照，并用内容摘要生成独立 shim TOML，保留可能仍被引用的历史文件。
配置变更先撤回就绪、记录待重载标记，成功重载后才重新发布就绪。创建中断保留真实 delegate 与参数；
恢复仅在原 mount namespace 可验证时删除 runsc，随后重新查找持有者并核验宿主机挂载。
原 namespace 在恢复前已消失时保留状态和租约，要求人工核验，不引入宿主机 namespace 回退或状态迁移工具。
