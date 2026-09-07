# RootPV 集群 E2E 测试

本目录包含面向已部署 RootPV 集群的功能与性能测试。脚本通过 SSH 在控制面节点执行
kubectl，因此开发机不需要保存 kubeconfig。

## 如何使用

### 1. 准备测试环境

测试前请确认：

- RootPV Controller 与节点代理已部署并就绪；
- 至少一个工作节点已配置 RootPV runtime handler，并带有
  ecloud.com/persistent-rootfs-ready=true 标签；
- 目标节点可以拉取测试镜像，或已将测试镜像预加载到该节点的 containerd；
- 集群已有可动态供给、支持 ReadWriteOnce 的块存储 StorageClass。

复制示例配置，编辑本地配置中的连接信息、目标节点、镜像、StorageClass 和超时参数：

```bash
cp tests/e2e/configs/example.env tests/e2e/configs/.ebs-cluster.env
```

.ebs-cluster.env 已被 Git 忽略。配置文件不得保存 SSH 密码、镜像仓库密码、访问令牌或
kubeconfig。

### 2. 执行功能测试

```bash
make e2e-functional E2E_CONFIG=tests/e2e/configs/.ebs-cluster.env
```

功能测试覆盖以下场景：

- RootPV Controller、节点代理和节点就绪标签；
- 未声明 RootPV 注解的 Pod 保持原始 RuntimeClass 与 Pod 定义；
- 目标容器不存在的 RootPV 配置被 Webhook 拒绝；
- 动态 PVC、RuntimeClass 与 runtime adapter 的准入注入；
- 根目录写入跨 Pod 删除和重建后仍可保留；
- 测试命名空间和 PVC 回收。

### 3. 执行性能测试

```bash
make e2e-performance E2E_CONFIG=tests/e2e/configs/.ebs-cluster.env
```

性能测试以同一镜像、节点、写入量和删除流程分别测量未启用与启用 RootPV 的路径；每条路径均先执行可配置的预热轮次，再进行正式测量：

- Pod 创建到 Running 的端到端时延；
- 指定大小根文件系统顺序写入的时延和吞吐量；
- Pod 删除与 RootPV 运行时清理时延。

测试脚本只允许操作名称以 rootpv-e2e- 或 rootpv-test- 开头的命名空间。设置
CLEANUP=false 可保留现场进行故障分析；默认会清理测试资源。

## 测试结果总结

2026-09-03 的验证在 Kubernetes v1.29 系列、6 节点 x86_64 Linux 集群中完成，目标工作节点
使用 containerd 2.1，动态块存储 PVC 最小容量为 10Gi。

| 功能测试用例 | 验证内容 | 结果 |
| --- | --- | --- |
| 组件与节点就绪 | Controller、节点代理及 RootPV 节点标签均处于就绪状态 | 通过 |
| 非启用路径 | 未声明 RootPV 注解的 Pod 保持原始 Pod 定义和 RuntimeClass | 通过 |
| 非法配置拒绝 | RootPV 指向不存在的目标容器时，Webhook 在服务端校验阶段拒绝请求 | 通过 |
| 准入注入 | 动态 PVC 场景正确注入预期 RuntimeClass 与 runtime adapter | 通过 |
| 持久根目录 | 首个 Pod 写入标记后删除并重建，后续 Pod 可读取该标记 | 通过 |
| 资源回收 | 测试结束后命名空间与 PVC 能够清理 | 通过 |

性能测试使用 1 轮预热、每个路径 3 轮正式测量和每轮 64MiB 同步写入。逐轮数据和详细结论见
results 目录。未启用 RootPV 时写入容器可写层，启用
RootPV 时写入持久根目录，因此“性能损耗占比”表示持久根目录带来的端到端差异，不等同于单独
的 runtime wrapper 开销。性能结果受节点规格、块存储后端、镜像缓存和集群负载影响，应仅与
相同环境的历史结果比较。

| 指标 | 未启用 RootPV | 启用 RootPV | 性能损耗占比 |
| --- | ---: | ---: | ---: |
| Pod 创建至 Running（平均 / P95） | 5500 / 5700 ms | 14560 / 15553 ms | 平均 +164.73%；P95 +172.86% |
| 根文件系统顺序写入（平均 / P95） | 1910 / 1972 ms | 1995 / 2066 ms | 平均 +4.45%；P95 +4.77% |
| Pod 删除与运行时清理（平均 / P95） | 32336 / 32440 ms | 32247 / 32519 ms | 平均 -0.28%；P95 +0.24% |
| 根文件系统顺序写入吞吐量 | 33.50 MiB/s | 32.08 MiB/s | 4.24% |
