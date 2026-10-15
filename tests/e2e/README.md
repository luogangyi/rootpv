# RootPV 集群 E2E 测试

本目录包含已部署 RootPV 集群的功能与性能脚本，以及 Kata + QEMU 的完整手工测试清单。
脚本通过 SSH 在控制面执行 kubectl，开发机无需保存 kubeconfig。
测试记录按运行时、存储和版本组合分别解释，不能由一个组合推导全部 runtime 支持。

## 前提和配置

- Controller、node-agent 与具体 adapter 的就绪标签均可用。
- 原 runtime 能独立运行普通 Pod；Kata 已安装 QEMU、可用的 guest kernel 和共享文件系统。
- 测试节点能拉取镜像，或 containerd 已缓存同一测试镜像。
- 自动化脚本使用可动态供给的 Filesystem + ReadWriteOnce 块 PVC；手工 Kata 清单另用静态 local PV。
- 文件系统适合 OverlayFS upper/work；存储策略 ConfigMap 的类名配置不等于已实现的强制准入或后端验证。

普通 runc 配置：

```bash
cp tests/e2e/configs/example.env tests/e2e/configs/.ebs-cluster.env
```

Kata + QEMU 配置：

```bash
cp tests/e2e/configs/example-kata-qemu.env tests/e2e/configs/.kata-qemu.env
```

编辑连接信息、节点、镜像、StorageClass 和超时。点开头的本地 env 配置被 Git 忽略；
不得写入 SSH 密码、令牌、镜像仓库密码或 kubeconfig。

| 参数 | runc/default 示例 | Kata QEMU 示例 |
| --- | --- | --- |
| ORIGINAL_RUNTIME_CLASS | 空：使用节点默认 route | kata-qemu |
| EXPECTED_RUNTIME_CLASS | persistent-rootfs-node-default | persistent-rootfs-kata-qemu |
| EXPECTED_RUNTIME_ADAPTER | runc-v2 | kata-qemu-v2 |
| STORAGE_CLASS | 可动态供给的块存储类 | 同样需要动态块存储类 |
| TEST_IMAGE | 可执行 /bin/bash 的固定测试镜像 | 保持两轮镜像一致，符合 Strict |
| RESULTS_DIR | tests/e2e/results | artifacts/e2e/kata-qemu，避免覆盖同日其他组合 |

上表的 Kata ID 假设原 RuntimeClass 和 handler 均名为 kata-qemu；其他名称以生成 catalog 为准。

## 自动化功能测试

```bash
make e2e-functional E2E_CONFIG=tests/e2e/configs/.ebs-cluster.env
make e2e-functional E2E_CONFIG=tests/e2e/configs/.kata-qemu.env
```

每次选择一个配置运行。脚本覆盖：

- Controller、node-agent 和节点就绪；
- 未 opt-in 的默认 runtime 对照；
- 不存在的目标容器被准入拒绝；
- 动态 PVC、RuntimeClass 与 adapter 注入；
- 根目录标记在 Pod 删除重建后保留；
- 测试 Namespace/PVC 清理。

脚本中的普通 Pod 对照未填写 RuntimeClass，因此它验证节点默认 runtime。
Kata 的普通 Pod、多容器、互斥和 rootfsd 重启需要追加专项用例，不能从此脚本结果推导。

脚本只允许 rootpv-e2e- 或 rootpv-test- 前缀的 Namespace。CLEANUP=false 可保留现场，
默认会清理测试资源；不要对保留的写层启动并发写入 Pod。

## Kata 完整 YAML 手工测试

[manifests/kata-qemu.yaml](manifests/kata-qemu.yaml) 包含 Namespace、local PV、PVC、
app 和 sidecar。把模板节点与镜像替换为实际值，在目标节点创建专用目录，并先部署
包含 Kata proxy shim 的 RootPV。完整命令、预期输出和故障检查见
[Kata 使用指南](../../docs/user-guide/kata-qemu.md#完整测试-yaml-与重建验证)。

运行前确认 Kata handler 已 resolved；runtime_digest_unavailable 表示未能生成完整配置摘要，
需按 [Kata 安装要求](../../docs/user-guide/kata-qemu.md#前提与安装)排查。配置错误、socket 凭据和创建回滚
另由 `go test ./internal/node ./internal/katashim` 覆盖；socket 凭据用例仅在 Linux 上执行。

新目录首次运行的 persistent-start-count 为 1；保留 PVC，仅删除 Pod 后重新应用，
计数应变为 2，first-pod-uid 保持旧值，current-pod-uid 改变。sidecar 每轮使用新的普通根文件系统。
清单仅声明 backing PVC volume，目标容器的只读 subPath anchor 由 Webhook 注入。

local PV 为 Retain，删除 Namespace/PV 不会删除节点上的测试数据。
这份可复用清单已检查 YAML 和容器脚本语法；历史集群验证对应的精确组合和用例见
[2026-10-14 Kata 结果](results/kata-qemu-2026-10-14.md)。

## 性能测试与比较范围

```bash
make e2e-performance E2E_CONFIG=tests/e2e/configs/.ebs-cluster.env
```

脚本测量创建到 Running、根文件系统同步写入、删除清理的时延与吞吐量，
并先执行配置的预热，再执行正式测量。

当前性能脚本的未启用 RootPV 路径使用节点默认 runtime。设置 ORIGINAL_RUNTIME_CLASS=kata-qemu
只会影响 opt-in 路径，不能把默认 runc 与 Kata 的差异称为 Kata 的 RootPV 开销。
Kata 性能基线仍待专门验证：两条路径都须使用同一原 QEMU RuntimeClass、镜像、节点、
写入量和删除流程，并分别记录 VM/runtime 与持久存储成本。

## 结果索引

域名迁移后的检查见 [2026-10-14 本地冒烟](results/domain-migration-2026-10-14.md)。
维护结束后已在 Kubernetes v1.37.1 完成 [新域名集群冒烟](results/domain-smoke-2026-10-14.md)，
覆盖 runc 与 Kata 的启动、持久化重建、sidecar、普通 Kata、非法注解及清理。
此前 Kata 记录对应迁移前的实现。

| 组合 | 证据与范围 |
| --- | --- |
| Kubernetes v1.29 / containerd 2.1 / runc / 动态块 PVC | [功能](results/functional-2026-09-14.md)、[性能](results/performance-2026-09-14.md) |
| 官方 containerd 多版本 / runc | [兼容性](results/compatibility-2026-09-14.md)；仅覆盖记录中的用例 |
| Kubernetes v1.37.1 / 新域名 / runc + Kata QEMU / ext4 local PVC | [迁移后冒烟](results/domain-smoke-2026-10-14.md)：启动、重建、sidecar、准入与清理；不含完整兼容性矩阵 |
| runc 恢复、互斥和升级 | [专项验证](results/special-validation-2026-09-15.md) |
| Kubernetes v1.34.3 / containerd 2.1.1 / Kata 4.2.0 Rust / QEMU 11.0.1 / ext4 local PVC | [Kata 功能](results/kata-qemu-2026-10-14.md)：重建、sidecar、互斥、rootfsd 重启、普通 Kata 与清理 |

Kata 的动态 CSI、节点/VM 故障恢复、性能、设备和其他 runtime 实现仍待验证。
发布覆盖和状态定义以 [兼容性方案](../../docs/design/04-compatibility-validation-plan.md) 为准。

2026-09-03 的历史性能样本为 1 轮预热、每路径 3 轮、每轮 64MiB 同步写入，
详情见 [原始记录](results/performance-2026-09-03.md)。这些 runc 样本不能用于说明 Kata 性能。
