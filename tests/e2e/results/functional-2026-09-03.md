# RootPV 功能测试结果

- 日期：2026-09-03
- 环境：Kubernetes v1.29；6 节点 x86_64 Linux 集群；目标节点使用 containerd 2.1
- 存储：动态块存储 PVC，最小容量 10Gi
- 镜像：预加载到目标节点的 RootPV amd64 测试镜像

## 结果

- RootPV Controller 和节点代理均处于就绪状态。
- 未启用 RootPV 的 Pod 保持原始 RuntimeClass 和 Pod 定义。
- 目标容器不存在的 RootPV 配置被准入校验拒绝。
- 准入注入的 RuntimeClass 为 `persistent-rootfs-node-default`，适配器为 `runc-v2`。
- 首次 Pod 成功写入根目录标记 `first-generation`。
- 使用相同 PVC 路径重建 Pod 后，成功验证并更新标记为 `second-generation`。
- 已完成测试命名空间及 PVC 清理。
