# RootPV 兼容性验证方案与矩阵

**文档状态：** 计划中  
**维护方式：** 每次验证完成后更新本表和对应的可复现测试记录；未记录证据的组合不得标记为支持。  
**适用版本：** RootPV 当前主干

## 1. 目的与原则

RootPV 的数据路径依赖 Kubernetes 准入、containerd runtime handler、`overlayfs` 挂载语义、PVC
存储和实际 OCI runtime。仅根据 Kubernetes 或 containerd 的版本号不能推断兼容性。因此，兼容性
验证以“可复现的完整组合”为单位，而不是以单个组件版本为单位。

本方案有两个目标：

1. 建立可逐步填充的验证表，便于按可获得的测试环境安排工作；
2. 清楚区分已验证、待验证和当前架构明确不支持的组合，避免将构建成功或控制面配置存在误称为运行时兼容。

矩阵采用风险优先和配对覆盖，而非所有维度的笛卡尔积。每个 Kubernetes、containerd 和运行时版本
至少要有一个“基础路径”组合；设备运行时、存储、内核和架构等高风险维度则在代表性基础组合上叠加验证。
某一行通过，不自动推导相邻版本、不同设备型号或不同厂商 runtime 通过。

## 2. 状态定义和支持声明

| 状态 | 含义 | 是否可对外声明支持 |
| --- | --- | --- |
| 已验证 | 已按第 5 节完成必测项，结果、制品 digest、版本信息和日志位置均已记录 | 可以，仅限该完整组合 |
| 验证中 | 已有环境或执行记录，但未完成全部必测项 | 不可以 |
| 待验证 | 已规划但尚未执行 | 不可以 |
| 不支持（架构边界） | 当前实现无法经过 `io.containerd.runc.v2` 的 `BinaryName` wrapper 路径介入 | 不可以；应验证 fail-closed 行为 |
| 不适用 | 与该行目标无关，例如设备 runtime 行不适用于无设备节点 | 不适用 |

“已验证”并不替代上游厂商的 Kubernetes、驱动或设备插件支持声明。发布说明必须同时给出 RootPV
版本、Chart 版本、镜像 digest、操作系统/内核、Kubernetes、containerd、OCI runtime、设备驱动和
device plugin 的精确版本。

## 3. 前置条件与记录模板

所有测试环境应满足以下前提：

- Linux 节点，containerd 使用 `overlayfs` snapshotter；
- 可动态供给的 filesystem 类型 ReadWriteOnce PVC；首轮使用 ext4 或已确认满足 OverlayFS 要求的 XFS；
- RootPV 仅部署到明确打标的 canary 节点；
- 测试镜像、Chart、配置和脚本均使用固定版本或 digest；
- 设备场景由对应厂商驱动、device plugin、RuntimeClass/handler 先独立验收，RootPV 不负责安装或修复它们。

每次运行在 `tests/e2e/results/` 下新增不含凭据的记录，至少包含以下字段：

| 字段 | 记录内容 |
| --- | --- |
| 测试标识 | 日期、RootPV Git SHA、镜像 digest、Chart 版本/包 digest |
| 平台 | Kubernetes、containerd、操作系统、内核、CPU 架构、CNI 和 CSI/StorageClass 版本 |
| 节点与存储 | 节点角色和规格摘要、snapshotter、文件系统、PVC access mode 与 volume mode |
| runtime | 原 RuntimeClass/handler、runtime type、delegate binary 版本和 Shadow RuntimeClass |
| 设备 | 厂商、型号/切分形态、驱动、device plugin、容器运行时集成版本；无设备时标记“无” |
| 执行 | 配置文件模板版本、测试脚本命令、开始/结束时间、轮次和失败重试次数 |
| 结果 | 第 5 节各用例结果、关键日志/事件位置、性能原始数据和已知限制 |

不得在测试记录、README 或配置样例中写入集群名称、IP 地址、账户、令牌、镜像仓库密码或 kubeconfig。

## 4. 兼容性验证矩阵

### 4.1 基础平台矩阵

下表列出首批需要寻找环境的组合。版本号使用“系列”而非某个补丁号；实际执行时必须记录精确版本。
`K8s` 与 containerd 的配对应同时满足目标 Kubernetes 发行版的上游支持范围。

| 维度 | 目标组合 | 覆盖策略 | 必测运行时 | 当前状态 |
| --- | --- | --- | --- | --- |
| Kubernetes | v1.27 系列 | 选择一个受支持的 containerd 1.6/1.7 组合 | stock `io.containerd.runc.v2` | 待验证 |
| Kubernetes | v1.28 系列 | 选择一个受支持的 containerd 1.7 组合 | stock `io.containerd.runc.v2` | 待验证 |
| Kubernetes | v1.29 系列 | 基线组合；覆盖当前 E2E 场景 | stock `io.containerd.runc.v2` | 验证中（containerd 2.1 基础功能与性能） |
| Kubernetes | v1.30 系列 | 选择一个受支持的 containerd 1.7/2.0 组合 | stock `io.containerd.runc.v2` | 待验证 |
| Kubernetes | v1.31 及后续受支持系列 | 每新增一个项目支持的 Kubernetes minor 至少选择一个组合 | stock `io.containerd.runc.v2` | 待验证 |
| containerd | 1.6.x | 在一个受支持 Kubernetes 版本上完整验证 | stock `io.containerd.runc.v2` | 待验证 |
| containerd | 1.7.x | 在两个不同 Kubernetes minor 上完整验证 | stock `io.containerd.runc.v2` | 待验证 |
| containerd | 2.0.x | 在一个受支持 Kubernetes 版本上完整验证 | stock `io.containerd.runc.v2` | 待验证 |
| containerd | 2.1.x | 作为当前基线持续回归 | stock `io.containerd.runc.v2` | 验证中（Kubernetes v1.29 基础功能与性能） |
| CPU 架构 | linux/amd64 | 每个 containerd 大版本至少一个基础组合 | stock `io.containerd.runc.v2` | 验证中（Kubernetes v1.29、containerd 2.1 基础功能与性能） |
| CPU 架构 | linux/arm64 | 至少覆盖一个 Kubernetes/containerd 基础组合及一个设备组合 | stock `io.containerd.runc.v2` | 待验证 |
| 操作系统/内核 | 主流发行版的 LTS 内核 | 每个新内核系列执行 OverlayFS 和恢复用例 | stock `io.containerd.runc.v2` | 待验证 |
| OverlayFS 存储 | RWO block PVC + ext4 | 作为所有组合的首选基线 | stock `io.containerd.runc.v2` | 验证中（Kubernetes v1.29、containerd 2.1 基础功能与性能） |
| OverlayFS 存储 | RWO block PVC + XFS | 单独确认 XFS/OverlayFS 所需 mount 与内核能力 | stock `io.containerd.runc.v2` | 待验证 |

不把网络文件系统、RWX 多节点共享、非 `overlayfs` snapshotter 或 rootless 节点纳入本矩阵；它们超出
当前范围，需要独立设计和安全评审后才可立项。

### 4.2 OCI runtime 与设备矩阵

设备类别不是只按硬件品牌划分：同一厂商的整卡、MIG/vGPU、虚拟化切分、不同驱动或不同
device plugin 版本均是独立组合。每个“待验证”行都应先确认 source RuntimeClass 在未启用 RootPV
时可正常运行，再验证 RootPV Shadow RuntimeClass 路径。

| 类别 | 首批目标形态 | RootPV 验证方式 | 计划级别 | 当前状态 |
| --- | --- | --- | --- | --- |
| 标准 OCI runtime | runc / `io.containerd.runc.v2` | 完整正向 E2E 与恢复测试 | 发布阻塞 | 验证中（Kubernetes v1.29、containerd 2.1、amd64 基础功能与性能） |
| runc-v2 自定义 runtime | `BinaryName` 指向非 wrapper 的 delegate | 验证 adapter 发现、参数/信号/退出码、create/delete/state 和重启恢复 | 高 | 待验证 |
| NVIDIA GPU | 整卡；MIG；有 vGPU 环境时单列 | GPU 分配、`nvidia-smi`/工作负载、持久化写入、重建与清理 | 高 | 待验证 |
| AMD GPU/ROCm | 整卡；有切分能力时单列 | GPU 工作负载、设备节点/环境保留、持久化与清理 | 高 | 待验证 |
| Intel GPU | 整卡；有虚拟化切分时单列 | GPU 工作负载、RuntimeClass/handler 路径和持久化 | 中 | 待验证 |
| MThreads GPU | 整卡；有切分能力时单列 | 厂商工作负载、adapter 路径、持久化与清理 | 中 | 待验证 |
| Ascend NPU | 训练/推理整卡；910/310 等可获得型号分别记录 | NPU 工作负载、设备节点/环境、持久化、重建和清理 | 高 | 待验证 |
| Ascend NPU 切分 | 虚拟 NPU、资源切分或调度插件形态 | 每种切分策略独立完成整套 NPU E2E，不从整卡结果推导 | 高 | 待验证 |
| 其他 NPU/加速器 | 厂商 runtime 属于 runc-v2 BinaryName 模型 | 按“runc-v2 自定义 runtime + 设备 E2E”执行 | 中 | 待验证 |
| Kata Containers | `io.containerd.kata.v2`，含 QEMU/Cloud Hypervisor 等可用后端 | RootPV opt-in 必须在准入/就绪阶段明确拒绝；未 opt-in Kata Pod 必须不受影响 | 发布阻塞的负向用例 | 不支持（架构边界） |
| gVisor | runsc 独立 shim | RootPV opt-in 必须明确拒绝；未 opt-in gVisor Pod 必须不受影响 | 发布阻塞的负向用例 | 不支持（架构边界） |
| 其他 microVM/独立 shim | 非 `io.containerd.runc.v2` task shim | 作为不支持类型完成 fail-closed 回归；若要支持需独立 RFC | 观察 | 不支持（架构边界） |

Kata、gVisor 和其他独立 task shim 的“通过”仅表示拒绝行为符合预期，不表示 RootPV 持久根目录能够在
其中运行。若社区计划支持任一 microVM runtime，必须先完成 shim proxy/adapter 的架构 RFC、威胁建模和
逐后端 E2E；不得通过将其 RuntimeClass 强行改为 runc 来规避验证。

### 4.3 组合优先级

| 批次 | 目标 | 建议环境 | 完成条件 |
| --- | --- | --- | --- |
| P0：回归基线 | 防止已有能力退化 | Kubernetes v1.29 + containerd 2.1 + amd64 + runc + RWO ext4 | 每次发布候选运行完整 E2E、恢复与性能基线 |
| P1：版本兼容 | 验证 Kubernetes/containerd 主版本差异 | v1.27/1.6、v1.28/1.7、v1.30/2.0 等受支持配对 | 每个组合完成全部基础用例 |
| P2：架构与存储 | 验证 arm64、内核、XFS | arm64 基础组合；不同内核和 XFS 环境 | 每个高风险维度在至少一个基础组合完成全部基础用例 |
| P3：设备 runtime | 验证厂商 adapter 和设备工作负载 | 各厂商真实设备节点及匹配 driver/device plugin | 设备用例 + 基础用例全部通过，且记录厂商栈版本 |
| P4：microVM 负向兼容 | 固化安全边界，避免静默错误路径 | Kata、gVisor 和一个其他独立 shim（可获得时） | opt-in 明确拒绝、普通工作负载不受影响 |

## 5. 单个组合的测试用例与通过标准

### 5.1 所有可支持组合的必测项

| 编号 | 用例 | 通过标准 |
| --- | --- | --- |
| C01 | 安装与 preflight | Controller、节点代理、目标 adapter 和 readiness 全部就绪；非目标节点未被修改 |
| C02 | 普通 Pod 回归 | 未启用 RootPV 的 Pod 定义、RuntimeClass 与运行结果不变 |
| C03 | 准入负向 | 缺失 PVC、错误目标容器、未登记 RuntimeClass、缺失 handler/delegate 均被明确拒绝或不就绪 |
| C04 | 动态 PVC 持久化 | 创建 Pod、写入标记、删除并重建 Pod 后可读回标记；基础镜像不匹配时 fail closed |
| C05 | 生命周期 | create/delete/state、优雅删除与强制删除均不遗留 RootPV mount、lease 或 runtime state |
| C06 | 故障恢复 | 分别重启 rootfsd、containerd 和节点；按支持边界验证已有工作负载、状态恢复或安全拒绝 |
| C07 | 并发与隔离 | 并发创建多个独立 PVC 工作负载；禁止同一 RWO rootfs 的并发写入 |
| C08 | 升级/回滚 | 在受支持升级路径上升级 RootPV；失败时不破坏既有状态，回滚步骤可执行 |
| C09 | 卸载 | 删除 RootPV 工作负载后卸载，普通 runtime 恢复且无遗留 handler/wrapper 配置 |
| C10 | 性能基线 | 按现有性能脚本记录创建、写入、删除时延和吞吐量；先记录趋势，不在本方案中设硬性性能门槛 |

每项至少连续执行三次；任一次失败必须保留脱敏日志、事件、mountinfo、containerd 配置摘要与 RootPV 状态
以供分析。暂时无法解释的失败使该组合保持“验证中”或“待验证”，不能以重试成功替代根因结论。

### 5.2 设备 runtime 的附加用例

| 编号 | 用例 | 通过标准 |
| --- | --- | --- |
| D01 | 厂商基线 | 不启用 RootPV 时，设备 plugin、RuntimeClass 与最小厂商工作负载正常 |
| D02 | 资源调度 | 启用 RootPV 后 Pod 仍获得请求的设备资源，Shadow RuntimeClass 保留原 RuntimeClass 的调度与开销语义 |
| D03 | 设备可用性 | 容器中执行厂商诊断或最小计算任务成功，不仅检查设备文件存在 |
| D04 | 持久化重建 | 在持久根目录写入标记后重建 Pod，设备工作负载和标记均正常 |
| D05 | 故障清理 | 删除/强制删除后设备资源可重新分配，且 RootPV 无残留 mount 或 lease |
| D06 | 切分隔离 | 对 MIG、vGPU、虚拟 NPU 等形态，分别验证资源边界、重建和其他切分实例不受影响 |

### 5.3 microVM 与独立 shim 的负向用例

| 编号 | 用例 | 通过标准 |
| --- | --- | --- |
| M01 | RootPV opt-in | 使用 Kata/gVisor/其他独立 shim 的 RootPV opt-in Pod 被明确拒绝，错误信息说明不支持的 runtime type |
| M02 | 非 opt-in 回归 | 相同 runtime 的普通 Pod 能按其原有路径运行，RootPV 不修改其 handler、配置或运行结果 |
| M03 | 配置完整性 | 节点代理不为不支持 runtime 创建 Shadow handler、wrapper delegate 或误导性的 ready 标签 |

## 6. 自动化、环境接入与结果发布

现有 `tests/e2e/run-functional.sh` 和 `tests/e2e/run-performance.sh` 是 P0 的基础。后续按以下方式
扩展，所有环境相关参数继续仅放在 Git 忽略的 `.env` 配置中：

1. 为每个矩阵组合建立不含凭据的配置样例，例如 `configs/example-nvidia.env`、`configs/example-ascend.env`；
2. 增加 `RUNTIME_CLASS`、目标 node selector、设备资源名/数量、厂商诊断命令和预期拒绝原因等参数，禁止在
   shell 脚本中按厂商名称硬编码分支；
3. 增加设备 E2E、恢复 E2E 和 microVM 负向 E2E，复用功能测试的命名空间前缀、超时和清理保护；
4. CI 至少运行 P0；需要特定硬件或许可证的 P3/P4 测试由专用环境定期执行，并将结果归档到
   `tests/e2e/results/`；
5. 每次新增“已验证”组合时，同步更新本表、根 README 的当前范围说明和相应测试记录。

## 7. 后续环境招募清单

优先寻找以下环境，不要求一次具备所有条件：

1. 分别运行 containerd 1.6、1.7、2.0 和 2.1 的 Kubernetes 集群；
2. amd64 和 arm64 Linux 节点，覆盖至少两类内核/发行版；
3. ext4 与 XFS 的 RWO block PVC；
4. NVIDIA（含 MIG/vGPU）、AMD ROCm、Intel、MThreads GPU，以及 Ascend 整卡和虚拟 NPU 节点；
5. 安装 Kata Containers、gVisor/runsc 或其他独立 shim 的集群，用于负向兼容回归。

环境接入前应确认可以创建隔离 namespace、使用专用 RWO PVC、在 canary 节点部署特权 RootPV 组件，并能够
收集必要的节点诊断信息。任何包含生产负载、共享凭据或无法清理测试 PVC 的环境都不应直接用于本方案。
