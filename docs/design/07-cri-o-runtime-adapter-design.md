# CRI-O + runc 适配设计

日期：2026-11-16。开发分支：`crio`。

## 1. 目标与范围

在不修改上游 CRI-O/runc 的前提下，让既有 RootPV 注解、Webhook/catalog、PVC 单写租约、
Flat Overlay 和恢复流程用于 CRI-O + conmon + runc。保留默认 containerd 的行为和 containerd 模块版本。
验收以官方二进制上的完整流程和 containerd 回归为准，不以 VM 实验结果替代。

| 项目 | 本分支范围 |
| --- | --- |
| 基线 | Linux AMD64；Kubernetes 1.37；官方 CRI-O 1.37.3；runc 1.5.2 |
| OCI | rootful、`runtime_type="oci"`、显式 runc 路径、conmon |
| 存储 | 内核 OverlayFS、单一 graphroot/runroot、文件系统 PVC；local PV 与串行 EBS |
| 根目录 | 选定容器持久化，sandbox/未选 sidecar 保持原生根目录；Strict 镜像身份 |
| 生命周期 | 创建、启动、exec/logs、同 Pod 重启、删除重建、服务重启、失败清理 |
| 不支持 | CRI-O 的 crun、VM handler、设备 runtime、conmon-rs、rootless、userns/idmapped、SELinux mount label、额外 image store、FUSE/composefs、checkpoint |
| 迁移 | 不声明 containerd 与 CRI-O 之间复用旧 upper 的兼容性 |

CRI-O + Kata/gVisor 的原实现和诊断补丁保存在独立
[`cri-o-with-kata-and-gvisor` 分支](https://gitcode.com/luogangyi/rootpv/tree/cri-o-with-kata-and-gvisor)。
本分支不包含这些补丁，不构建或安装任何 CRI-O/runc fork。
`internal/katashim` 保留主线 containerd 用途，不包含 CRI-O VM 生命周期逻辑。

## 2. 为什么需要独立 CRI 后端

CRI-O 由 conmon 调用 OCI runtime，并通过 containers/storage 管理镜像；它不提供
containerd 的 Container/Snapshot API。仅替换 socket 或添加 containerd handler 无法完成接入。

| 接入点 | CRI-O 实现 | 继续复用的能力 |
| --- | --- | --- |
| 配置 | 读取运行中 `/config`，写独立 drop-in 并验证激活 | inventory/catalog、Shadow RuntimeClass、节点 readiness |
| 调用 | conmon → OCI wrapper → 原 runc | 目标容器识别、原参数委托、create/delete 事务 |
| 镜像 | 独立 storage helper 读取 containers/storage 并建立引用 | SnapshotSource 接口、Strict ChainID、Flat Overlay |
| 原生初始化 | 将工作目录等内容冻结为只读 bootstrap lower | PVC 只保存应用增量、挂载身份检查 |
| 恢复 | 正式 CRI 状态、boot ID、source journal | 租约、进程身份、普通卸载、失败保留重试 |

## 3. 配置发现与激活

读取 CRI-O 本地 `/config` 返回的生效 TOML，不使用默认配置或仅凭磁盘文件宣称已启用。
发现器要求 OCI 类型、显式路径的文件名为 `runc`、无 `platform_runtime_paths`，
并沿用可执行文件和防 wrapper 递归检查。管理员负责确认 runc 的真实来源；
文件名约束只限制入口范围，不是签名或版本认证。

目录使用现有 OCI runtime 接口家族，节点 inventory 额外记录 `runtimeProvider=cri-o`；
这允许混合节点保留既有 RuntimeClass 身份。节点摘要纳入 provider、委托路径和生效配置，
不会误用 containerd 配置生成器。未支持的 runtime 保持 Unavailable，不能静默降级成临时根目录。

只写 RootPV 专属 `99-rootpv.conf` 和 registry。shadow 克隆完整源 handler，保留 monitor、
cgroup、安全和注解设置，关闭默认继承，并将入口指向独立 wrapper 别名。
不改原 handler、默认 runtime、CNI、CSI 或 kubelet endpoint。

激活顺序：撤销 readiness → 保存 daemon 的 boot ID/PID/start time → 原子写 registry/drop-in →
受控重启 CRI-O → 验证进程身份变化和生效 handler → 发布 readiness。
registry 先于 drop-in 可见，因为 CRI-O 初始化会执行 wrapper 的 `--version`/`features`。

基线版本的 SIGHUP 在在线验证中出现配置接口已更新、实际创建仍使用旧 handler 表的情况，
因此采用服务重启，不修改 CRI-O reload 源码。pending 文件允许 agent 中断后续做激活；
只有 catalog generation 变化不触发重复重启。失败时不发布 readiness。

源 handler 消失或不可用时，将旧 shadow/registry 标记 retired：拒绝新 create，
保留旧容器清理入口。排空并确认无残留后由管理员移除，不凭单次 CRI 不存在结果删配置。
恢复有效源配置后可重新启用。

## 4. 镜像来源与存储引用

`tools/crio-storage` 是独立 Go 1.25 模块，使用 containers/storage v1.63.0，
不升级主模块的 containerd 模块。主模块为正式 CRI 查询引入 `k8s.io/cri-api v0.33.1`，
其依赖将 gRPC 提升至 v1.68.1，因此仍需回归 containerd 的实际 RPC 路径。
helper 与 CRI-O 共用存储库锁，按有效 graphroot/runroot/options
打开 store，解析镜像层和 OCI DiffID ChainID；不使用文件锁旁路或手工改元数据。

每次 create 先持久化 source intent，再建立非运行中的存储引用，保护正在使用的镜像层。
state 保存 provider、socket、helper、source root 和 boot ID。释放顺序为清理委托/挂载、释放引用、
删除 journal；失败保留记录重试。helper 调用最多 30 秒，更短的调用方 deadline 优先。

CRI-O 初始化的工作目录、符号链接等不能写进公共镜像层，也不能丢掉。wrapper 校验原生 overlay
来源后，复制原生 upper 的初始化内容到独立 bootstrap 目录，再作为只读 lower 使用；
应用的 upper/work 仍由 PVC 承载。CRI-O 的相对 `l/<短名>` 镜像链接在允许目录内解析，
最终路径与内核 mountinfo 交叉核验。

Strict 使用后端标识 `cri-o-overlay` 和镜像 DiffID ChainID。不同镜像链或跨后端旧状态均不自动 rebase。

必须由管理员显式设置：

```toml
[crio]
internal_wipe = false
internal_repair = false
```

引用只能保护正常镜像删除，不能阻止 store wipe。任一自动清空选项开启时拒绝新 RootPV create；
RootPV 不代替管理员修改全局存储安全配置。额外 image store、非 overlay 等未支持布局也拒绝。

## 5. 生命周期与恢复

- sandbox 和未选中的容器直接委托原 runc；从 CRI-O OCI 注解获取 Pod/容器身份并交叉验证。
- create 沿用 PVC anchor、租约、overlay、state 和失败回滚事务，原参数交给 runc。
- 正式 CRI 状态区分 Running 与已退出容器。CRI-O 为日志保留 Exited 记录时，可以回收持久根目录挂载，
  但保留后续 Remove 所需路由。
- 同一 boot 内结合 PID/start time 判断进程；跨 reboot 不复用旧 PID/mount namespace。
- source intent 存在但还未产生 runtime state 的窗口由独立 reap 处理；CRI 查询失败时保留引用。
- 服务重启后继续恢复清理，并验证应用再次退出/重启，不能只观察服务 Ready。
- 不使用 lazy unmount、强制删锁、storage prune/wipe 来让测试通过。

## 6. 部署与分支切换

Chart 新增 `runtimeProvider=cri-o`、CRI-O socket/config/storage/sourceRoot 和 restartCommand。
CRI-O agent 安装 OCI wrapper/storage helper，containerd agent 保留 Kata proxy 安装。
默认 values 和直接 YAML 仍使用 containerd。Chart 不安装 CRI daemon，也不切换 kubelet。
一个节点只由一个 RootPV agent 管理；混合集群的 agent 选择器必须互斥。

从旧 VM 实验分支切换前，排空 RootPV Pod、核对 state/journal/pin/挂载清理，
备份并移走旧 RootPV drop-in/registry，再安装本分支。已有 VM 不能直接滚动迁移到缩减后的实现。
部署参数及回退步骤见 [使用指南](../user-guide/cri-o.md)。

## 7. 验收与证据

复用现有测试和在线驱动，仅保留必要的新增后端单测；不为每条静态检查建议扩展测试框架。

1. 官方 runc 原生双容器：启动、exec/logs、DNS/网络、重启与删除。
2. 真实 Webhook/catalog/default routing、opt-out、非法参数和伪造更新拒绝。
3. 持久化重建、权限/链接/whiteout、工作目录、sidecar/多容器/路径隔离、非 root、Strict、单写保护。
4. rootfsd/CRI-O 重启后持久化及再次退出、创建失败后同路径复用、存储引用与退出残留清理。
5. local PV 和按创建→重建→删除顺序串行执行的 EBS。
6. containerd 的 runc/Kata/gVisor 回归；无 GPU/NPU 时将硬件测试标为未执行，不能由单测代替。

实际版本、完成项和限制以 [本轮记录](../../tests/e2e/results/cri-o-runc-2026-11-16.md) 为准。
