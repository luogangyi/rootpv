# gVisor shim 启动分组与退出定位

日期：2026-10-20。固定组合：Linux AMD64、宿主机内核 5.10（厂商构建）、
Kubernetes 1.37.1、containerd 2.1.1、官方 gVisor release-20261012.0、systrap。
环境名称、地址、节点名、容器 ID、私有仓库与原始进程快照仅保存在私有归档。

## 原因

官方 shim 的 manager.Start 调用 GetRuntimeOptions 决定 socket grouping，
按 /run/containerd/runsc/config.toml、/etc/containerd/runsc/config.toml 和当前目录的
config.toml 查找全局配置。handler 的 options.ConfigPath 在后续 CreateTask 中才被读取。
因此仅在 ConfigPath 设置 grouping=true，不能让启动阶段按 sandbox ID 复用 shim。
在本次 CRI 组合中，额外启动的应用容器 shim 没有承载 task，删除 Pod 后仍然存在。

参考固定版本源码：[manager.Start / getEnableGrouping](https://github.com/google/gvisor/blob/release-20261012.0/pkg/shim/v1/manager.go)、
[GetRuntimeOptions](https://github.com/google/gvisor/blob/release-20261012.0/pkg/shim/v1/runsc/options.go)。

## 原生对照

两组均为原 RuntimeClass 下的双容器 Pod，未启用 RootPV。源 handler 的 ConfigPath 保持不变，
其中已有 grouping=true；仅补充全局配置 /etc/containerd/runsc/config.toml 的 grouping=true。

| 配置 | 运行时 shim 数 | 删除后的新增闲置 shim |
| --- | --- | --- |
| 只有 handler ConfigPath | 3 | 2 |
| 全局配置启用 grouping，第 1 轮 | 1 | 0 |
| 全局配置启用 grouping，第 2 轮 | 1 | 0 |

修正后的两轮均在删除后自动退出所有新 runsc/shim/gofer/Sentry 进程，未手工终止它们。
对照组的两个旧闲置 shim 经测试 ID、任务/容器记录、子进程、可执行文件和启动时间核验后定向清理。

## RootPV 前置检查

发现与 reconcile 要求固定全局路径中优先生效的文件显式 grouping=true；不依赖当前工作目录。
缺失、关闭、非法类型或不可读时，不发布该 adapter 就绪，也不生成新的 Shadow handler。
全局配置的路径和内容纳入节点解析摘要；源 handler 和全局配置由节点管理员管理，RootPV 不自动改写。

本地回归覆盖缺失/关闭/未设置/非法类型、优先级、路径和内容摘要变化，以及 reconcile 失败关闭。
此前的 [隔离功能记录](gvisor-2026-10-20.md)保留为首次运行的历史证据；完整在线准入验证单独记录。
后续 [在线功能验收](gvisor-online-2026-10-20.md)中，三代 RootPV Pod 均只启动一个分组 shim，
每次删除后 runsc/shim/gofer/Sentry 自动退出，未人工终止运行时进程。
