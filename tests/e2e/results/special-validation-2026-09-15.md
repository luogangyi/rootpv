# RootPV 专项恢复验证记录

- 日期：2026-09-15
- 平台：Kubernetes v1.29 系列；linux/amd64；containerd 2.1.1（厂商构建）；stock `io.containerd.runc.v2`；`overlayfs`；RWO ext4 块存储 PVC
- 节点范围：已 cordon 的专用测试节点；执行前已迁走普通工作负载，测试后仅保留节点 DaemonSet 与系统组件
- 测试制品：基于本次工作区源码构建的临时镜像，digest 为 `sha256:cdf1a830deaadbc15612586a6969a03c28dab87a784aefbbd0f987844e5447b9`

## 已完成结果

| 编号 | 轮次 | 验证内容 | 结果 | 说明 |
| --- | --- | --- | --- | --- |
| R01 | 3/3 | 重启 rootfsd 节点代理；确认运行中 Pod；删除并重建 RootPV Pod | 通过 | 原 Pod 未非预期重启；重建后均读回并更新持久化标记 |
| R02 | 1/3 | 重启 containerd；普通 PVC 与 RootPV 对照 | 通过 | 普通 PVC 标记保持；RootPV 的孤儿 task 被回收，重建 Pod 读回并更新标记 |
| R02 | 2/3 | 重启 containerd；RootPV 自动恢复 | 通过 | kubelet 自动重建 RootPV 容器，标记可读写 |
| R02 | 3/3 | 重启 containerd；RootPV 自动恢复 | 通过 | kubelet 自动重建 RootPV 容器，标记可读写 |
| R03 | 3/3 | 连续重启已 cordon 节点 | 通过 | 每轮均确认节点、containerd、kubelet、CSI 和 rootfsd 恢复；RootPV 标记均可读写 |
| C07-A | 1/1 | 并发创建 10 个独立 PVC/RootPV Pod，并全部删除重建 | 通过 | 10 个动态 ext4 PVC 均成功绑定；每个实例重建后均读回自身标记，未发生串写 |
| C07-B | 1/1 | 两个 Pod 并发申请同一 PVC 的同一路径 rootfs | 通过 | 仅一个 Pod 成功运行；另一个被 runtime wrapper 拒绝创建，已验证单写互斥 |
| U01 | 1/1 | 用当前源码构建镜像升级 Helm release，再回滚到社区镜像 | 通过 | 升级期创建的 RootPV 标记在回滚后读回，并可继续写入 |
| U02 | 1/1 | 将运行中节点从 containerd 2.1.1（厂商构建） 直接切换到 1.6.28，再恢复 | 不通过（直接降级） | 1.6.28 可启动，但不能重用 2.1 运行时状态，原 sandbox 保持 NotReady；已按校验和恢复 2.1.1（厂商构建），恢复后新的 RootPV PVC 用例通过 |
| U03 | 1/1 | 卸载 Helm release，验证普通 PVC，再使用保存的 values 重装 | 部分通过 | 卸载后 RootPV socket 消失，普通 PVC 正常；但 containerd 配置残留 RootPV handler。重装后 RootPV 动态 PVC 用例通过 |

## 发现并修复的问题

containerd 服务重启后，旧 runc 进程可能暂时存活，但其 containerd task 已不再存在。此前的恢复逻辑未回收这类
`rootpv-delegation-v1` 状态，导致 overlay 和 lease 残留，并阻塞 PVC 卸载。

本次修复以 containerd task 元数据作为存活判定依据：仅当 task 仍被 containerd 跟踪且为活跃状态时保留
RootPV 状态；否则在守护进程周期性回收中卸载 overlay、删除 runtime state 并释放 lease。对 PVC 已被删除、
lease 元数据已不存在的陈旧状态，完成本地清理后直接删除 state，避免节点代理启动失败。

专项验证还发现两个需要后续处理的限制：containerd 运行中状态不能通过“直接替换二进制”从 2.1 降级到
1.6；跨大版本回退应采用排空节点并重建运行时状态的受控迁移流程。Helm 卸载会停止节点代理并删除 socket，
但当前 chart 没有节点级卸载 hook，因此不会删除 containerd 中已写入的 RootPV runtime handler。

## 后续验证

- 跨 containerd 大版本的排空、状态重建和回退迁移；
- 增加节点级卸载 hook，并验证卸载后自动移除 RootPV runtime handler；
- 其他 Kubernetes、containerd、设备 runtime、架构和存储组合。
