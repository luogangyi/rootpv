# Runtime 生命周期清理回归

日期：2026-11-05。基线：已合并的 `8a2ae6c`；被测修复为本报告所在提交。
环境：Linux amd64 / 内核 5.10、Kubernetes 1.37.1、containerd 2.1.1、gVisor release-20261012.0。
使用独立部署、限定命名空间的 Webhook 和静态 local PV；生命周期用例串行执行，单写者拒绝用例除外。本轮未创建 EBS 卷。

## 已确认的问题与修复

- Flat create 在取得租约前写入纯委托状态；Strict 镜像检查拒绝后可能遗留无法被 recovery 接管的目录。改为取得租约后随完整生命周期状态一起持久化路由。
- 已停止的 containerd task 仍需 shim 完成正常删除。提前回收其状态会造成 `delegation_state_missing` 和 Pod 删除卡住；现在按 task 是否仍存在判断归属。
- delegate 的 create 进程退出后，wrapper 仍需提交创建结果。现在只要原 wrapper 的 PID 和启动时间仍匹配，就保留状态，保护创建交接窗口。
- 扫描时状态文件可能被正常删除，task 查询期间租约也可能已释放。分别检查实际读取的 ENOENT 和查询后的租约释放记录，避免重复清理。

## 验证

为放大竞态，诊断构建仅将生命周期扫描周期改为 100 ms；此设置未进入产品代码。
基线第 2 轮复现停止任务被提前清理；中间版本第 9 轮暴露创建交接遗漏。保存异常证据并定向清理后，最终修复从干净状态重新测试。

| 检查 | 结果 |
| --- | --- |
| `go test ./...`、`go vet ./...`、`git diff --check` | 通过 |
| Linux rootfsd / runtimewrapper 现有测试及新增回归 | 139 个顶层用例通过，无跳过；私有挂载命名空间运行 |
| 最终修复：100 ms 扫描下连续创建/删除 | 20/20 通过；每轮状态清空、shim/Sentry/gofer 自动退出；node-agent 重启数始终为 0 |
| runc、gVisor Strict 镜像切换 | 更换镜像被拒绝；无持久化 rootfs 状态残留；切回原镜像读取原标记成功；删除后状态为空、租约释放 |
| 默认 30 s 周期的在线 Webhook/catalog 与三代重建 | 通过：注入与非法请求拒绝、inventory/Shadow RuntimeClass、文件和 sidecar 隔离、单写者拒绝、agent 主动重建、stats/ps、DNS/HTTP，以及 Webhook/catalog 故障恢复 |
| 隔离测试清理与原有工作负载核验 | 配置、节点标签和 RuntimeClass 恢复；既有 Webhook 不变；原有 11 个业务 Pod、2 个 RootPV Pod 的 UID、状态、就绪、重启次数及容器 ID 不变；节点 Ready |

仅新增三个针对性单元测试：resolve 失败无残留、查询期间租约释放、创建交接期间 wrapper 存活。复用已有回收、挂载和路由测试，未新增提交到仓库的压力测试框架。

上轮偶发 node-agent 退出缺少退出前日志，无法追溯其唯一原因；本轮修复的是代码和实验确认的生命周期缺陷。短时无非计划重启不代表覆盖所有退出原因。
原始日志、容器标识、节点信息和诊断构建仅存于私有归档。
