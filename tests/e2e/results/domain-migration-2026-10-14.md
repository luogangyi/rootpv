# openfuyao.cn 域名迁移本地冒烟

后续维护结束后已完成 [集群冒烟](domain-smoke-2026-10-14.md)。本文保留维护期间的本地执行记录。
PR 发布前另检查了 JSONPath 的一层/两层点转义，补齐遗漏引用并通过实际 client-go 解析器验证。

- 日期：2026-10-14。
- 分支：lgy/kata-qemu；基于 2ce9513 的域名迁移改动。
- 执行平台：macOS ARM64，Go 1.24.0，Python 3.13.12，Helm 4.1.4。
- 范围：代码、Chart、静态 manifests、E2E 示例和文档中的 RootPV 域名统一迁移。
- 集群状态：目标环境正在维护；仅执行本地检查，未部署或修改集群。

## 已通过

| 检查 | 结果 |
| --- | --- |
| 统一替换 | 30 个已跟踪文件中的 171 处引用改为 openfuyao.cn |
| 遗漏检查 | replace-domain.py --check 返回成功；除脚本自身的迁移源参数外无旧域名引用 |
| 脚本行为 | 预览不写入、检查返回码、apply、幂等、空域名拒绝均通过；未跟踪/忽略文件、二进制、非 UTF-8 文件和符号链接保留 |
| 全量 Go 测试 | go test -count=1 ./... 全部通过 |
| Race 检查 | admission、node、runtimecatalog、katashim、rootfsd 全部通过 |
| 本机构建 | make build 通过；Controller、rootfsd 与 Kata shim 的帮助入口可执行 |
| Linux AMD64 构建 | Controller、rootfsd、OCI wrapper 与 Kata shim 均生成静态 ELF |
| Helm | lint 通过；默认配置渲染 15 个 Kubernetes 资源 |
| 协议一致性 | CRD 名称/group、RBAC API group、webhook 名称、nodeSelector 和测试 Pod 注解均使用新域名；Chart/静态 CRD 一致 |
| 示例与文档 | 静态 manifests、完整 Kata YAML、YAML 文档片段、shell 语法、本地链接与 Markdown fence 检查通过 |
| Diff | git diff --check 通过 |

初次沙箱内测试因 Unix socket 绑定限制未通过；允许本机 socket 后全量重新执行成功。
该失败不属于 RootPV 功能失败，也不计作集群验证。

主要复现命令：

```bash
python3 scripts/replace-domain.py --check
go test -count=1 ./...
go test -race ./internal/admission ./internal/node ./internal/runtimecatalog \
  ./internal/katashim ./internal/rootfsd
make build
helm lint charts/rootpv
helm template rootpv charts/rootpv --namespace rootpv-system --include-crds
bash -n scripts/*.sh tests/e2e/*.sh tests/e2e/lib/*.sh
git diff --check
```

## 当时的集群冒烟计划

维护结束后，用当前版本组件和新域名部署到隔离 namespace/节点，验证：

1. 新域名安装标签、inventory、catalog、Shadow RuntimeClass 与 adapter 就绪标签贯通。
2. runc 和 Kata QEMU 的新域名 opt-in Pod 启动、写入、删除重建及持久化读取。
3. 非目标 sidecar 使用独立普通根目录；不 opt-in 的 Kata Pod 保持原 RuntimeClass。
4. 非法目标容器被拒绝；删除后 merged 挂载、状态和活跃租约清理。
5. 恢复测试安装前的节点配置与元数据，确认原有负载不受影响。

本次本地结果不代表新域名已经完成集群 E2E；旧
[Kata 功能记录](kata-qemu-2026-10-14.md)对应实现 33c9c36，不能替代此次迁移后的冒烟。
升级步骤见 [域名迁移指南](../../../docs/user-guide/domain-migration.md)。
