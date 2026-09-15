# RootPV 兼容性验证结果

- 日期：2026-09-14
- 平台：Kubernetes v1.29 系列；linux/amd64；ReadWriteOnce ext4 块存储 PVC
- 关联结果：[功能测试](functional-2026-09-14.md)、[性能测试](performance-2026-09-14.md)

## 已完成组合

| 组合 | 验证内容 | 结果 |
| --- | --- | --- |
| containerd 2.1.1+aiinfra + stock `io.containerd.runc.v2` | 功能、性能、动态 PVC 持久化与清理 | 通过；详细数据见关联结果 |
| containerd 2.1.1+aiinfra + stock `io.containerd.runc.v2` | 写入持久根目录标记后重启 rootfsd 节点代理；确认原 Pod 仍运行，删除并重建 Pod 后读取并更新标记 | 通过 |
| 官方静态 containerd 2.1.9 + stock `io.containerd.runc.v2` | 在隔离节点以官方校验过的静态二进制替换厂商构建；写入持久根目录标记后删除并重建 Pod | 通过；测试后已恢复节点原始二进制、配置与 cordon 状态 |
| 官方 containerd 1.6.39 + stock `io.containerd.runc.v2` | 在隔离节点切换运行时；动态供给 ext4 PVC，写入根目录标记后删除并重建 Pod，读回并更新标记 | 通过；切换运行时后需重建节点 CSI 插件 Pod 以刷新其 socket/挂载命名空间，再执行 PVC 用例；测试后已删除命名空间、移除 RootPV 节点标签并恢复节点原始二进制、配置与 cordon 状态 |
| 官方 containerd 1.7.35 + stock `io.containerd.runc.v2` | 在隔离节点切换运行时；动态供给 ext4 PVC，写入根目录标记后删除并重建 Pod，读回并更新标记 | 通过；测试后已删除命名空间、移除 RootPV 节点标签并恢复节点原始二进制、配置与 cordon 状态 |
| 官方 containerd 2.0.12 + stock `io.containerd.runc.v2` | 在隔离节点切换运行时；动态供给 ext4 PVC，写入根目录标记后删除并重建 Pod，读回并更新标记 | 通过；测试后已删除命名空间、移除 RootPV 节点标签并恢复节点原始二进制、配置与 cordon 状态 |
| containerd 1.7 + gVisor/runsc | RootPV opt-in Pod 被 admission 拒绝；未 opt-in 的 gVisor Pod 正常运行且未出现 RootPV mutation annotation | 通过 |

gVisor 的结果仅验证了当前架构的 fail-closed 边界，不表示 RootPV 能够在 gVisor 中提供持久根目录。

## 尚未覆盖

- containerd 重启、节点重启、升级/回滚、并发和卸载；
- 官方 containerd 1.6.39、1.7.35、2.0.12 的恢复、升级/回滚、并发、卸载和性能用例；
- Kata 和其他独立 shim 的负向用例；
- GPU、真实 NPU、arm64、XFS、其他 Kubernetes/containerd 版本组合。
