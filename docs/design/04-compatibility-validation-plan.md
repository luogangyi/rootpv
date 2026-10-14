# RootPV 兼容性验证方案与矩阵

**文档状态：** 持续验证与维护（2026-10-14 更新）

**维护方式：** 每次验证完成后更新本表和对应的可复现测试记录；未记录证据的组合不得标记为支持。

**适用版本：** 当前源码；Kata + QEMU 实现见 lgy/kata-qemu 的 33c9c36

openfuyao.cn 域名迁移已完成 [本地冒烟](../../tests/e2e/results/domain-migration-2026-10-14.md)。
目标集群正在维护，新域名的集群冒烟尚未执行；既有记录对应各自标明的实现版本。

## 1. 目的与原则

RootPV 的数据路径依赖 Kubernetes 准入、containerd runtime handler、`overlayfs` 挂载语义、PVC
存储，以及实际 OCI runtime 或 Kata task shim/VM。仅根据 Kubernetes 或 containerd 的版本号不能推断兼容性。因此，兼容性
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
| 已验证（限定用例） | 指定完整组合的部分用例已有明确证据，尚未完成全部发布要求 | 只说明这些用例通过，不扩大到完整兼容性承诺 |
| 不支持（架构边界） | 当前没有对应的 OCI wrapper 或 task shim adapter；例如 Kata CLH、runsc | 不可以；应验证 fail-closed 行为 |
| 不适用 | 与该行目标无关，例如设备 runtime 行不适用于无设备节点 | 不适用 |

“已验证”并不替代上游厂商的 Kubernetes、驱动或设备插件支持声明。发布说明必须同时给出 RootPV
版本、Chart 版本、镜像 digest、操作系统/内核、Kubernetes、containerd、OCI runtime、设备驱动和
device plugin 的精确版本。

## 3. 前置条件与记录模板

所有测试环境应满足以下前提：

- Linux 节点，containerd 使用 `overlayfs` snapshotter；
- 使用 Filesystem + ReadWriteOnce PVC；动态块 PVC 与静态 local PVC 的证据分开记录，文件系统需满足 OverlayFS 要求；
- RootPV 仅部署到明确打标的 canary 节点；
- 测试镜像、Chart、配置和脚本均使用固定版本或 digest；
- 设备场景由对应厂商驱动、device plugin、RuntimeClass/handler 先独立验收；Kata 场景先独立验收 Kata/QEMU 与原 RuntimeClass。

每次运行在 `tests/e2e/results/` 下新增不含凭据的记录，至少包含以下字段：

| 字段 | 记录内容 |
| --- | --- |
| 测试标识 | 日期、RootPV Git SHA、镜像 digest、Chart 版本/包 digest |
| 平台 | Kubernetes、containerd、操作系统、内核、CPU 架构、CNI 和 CSI/StorageClass 版本 |
| 节点与存储 | 节点角色和规格摘要、snapshotter、文件系统、PVC access mode 与 volume mode |
| runtime | 原 RuntimeClass/handler、runtime type、delegate 版本和 Shadow RuntimeClass；Kata 额外记录 runtime 实现、hypervisor、guest kernel 与共享文件系统 |
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
| Kubernetes | v1.29 系列 | 基线组合；覆盖当前 E2E 场景 | stock `io.containerd.runc.v2` | 验证中（2.1.1（厂商构建）：基础功能、性能、R01/R02/R03 各连续 3 轮、C07 并发与互斥、U01 Helm 升级/回滚；官方 1.6.39、1.7.35、2.0.12 和静态 2.1.9 已完成 C04） |
| Kubernetes | v1.30 系列 | 选择一个受支持的 containerd 1.7/2.0 组合 | stock `io.containerd.runc.v2` | 待验证 |
| Kubernetes / Kata 功能组合 | v1.34.3（厂商构建） + containerd 2.1.1 + amd64 + ext4 local PVC | QEMU task adapter 的独立功能路径 | Kata 4.2.0 Rust + QEMU 11.0.1 | 已验证（限定用例）：见 [2026-10-14 记录](../../tests/e2e/results/kata-qemu-2026-10-14.md)，不替代完整 C01–C10 |
| Kubernetes | v1.31 及后续受支持系列 | 每新增一个项目支持的 Kubernetes minor 至少选择一个组合 | stock `io.containerd.runc.v2` | 待验证 |
| containerd | 1.6.x | 在一个受支持 Kubernetes 版本上完整验证 | stock `io.containerd.runc.v2` | 验证中（官方 1.6.39 在 Kubernetes v1.29 完成 C04；切换后先重建节点 CSI 插件 Pod） |
| containerd | 1.7.x | 在两个不同 Kubernetes minor 上完整验证 | stock `io.containerd.runc.v2` | 验证中（官方 1.7.35 在 Kubernetes v1.29 完成 C04） |
| containerd | 2.0.x | 在一个受支持 Kubernetes 版本上完整验证 | stock `io.containerd.runc.v2` | 验证中（官方 2.0.12 在 Kubernetes v1.29 完成 C04） |
| containerd | 2.1.x | 作为当前基线持续回归 | stock `io.containerd.runc.v2` | 验证中（2.1.1（厂商构建）：基础功能、性能、R01/R02/R03 各连续 3 轮、C07 和 U01；官方静态 2.1.9：PVC 持久化与 Pod 重建。直接降级到 1.6.28 不可复用运行中 2.1 状态） |
| CPU 架构 | linux/amd64 | 每个 containerd 大版本至少一个基础组合 | stock `io.containerd.runc.v2` | 验证中（Kubernetes v1.29、containerd 2.1.1（厂商构建） 基础功能、性能、R01/R02/R03 各连续 3 轮、C07 和 U01） |
| CPU 架构 | linux/arm64 | 至少覆盖一个 Kubernetes/containerd 基础组合及一个设备组合 | stock `io.containerd.runc.v2` | 待验证 |
| 操作系统/内核 | 主流发行版的 LTS 内核 | 每个新内核系列执行 OverlayFS 和恢复用例 | stock `io.containerd.runc.v2` | 待验证 |
| OverlayFS 存储 | RWO block PVC + ext4 | 作为所有组合的首选基线 | stock `io.containerd.runc.v2` | 验证中（Kubernetes v1.29、containerd 2.1.1（厂商构建） 基础功能、性能、R01/R02/R03 各连续 3 轮、C07 和 U01） |
| OverlayFS 存储 | RWO block PVC + XFS | 单独确认 XFS/OverlayFS 所需 mount 与内核能力 | stock `io.containerd.runc.v2` | 待验证 |

不把网络文件系统、RWX 多节点共享、非 `overlayfs` snapshotter 或 rootless 节点纳入本矩阵；它们超出
当前范围，需要独立设计和安全评审后才可立项。

### 4.2 Runtime 与设备矩阵

设备类别不是只按硬件品牌划分：同一厂商的整卡、MIG/vGPU、虚拟化切分、不同驱动或不同
device plugin 版本均是独立组合。每个“待验证”行都应先确认 source RuntimeClass 在未启用 RootPV
时可正常运行，再验证 RootPV Shadow RuntimeClass 路径。

| 类别 | 首批目标形态 | RootPV 验证方式 | 计划级别 | 当前状态 |
| --- | --- | --- | --- | --- |
| 标准 OCI runtime | runc / `io.containerd.runc.v2` | 完整正向 E2E 与恢复测试 | 发布阻塞 | 验证中（Kubernetes v1.29、containerd 2.1.1（厂商构建）、amd64 基础功能、性能与 rootfsd 重启） |
| runc-v2 自定义 runtime | `BinaryName` 指向非 wrapper 的 delegate | 验证 adapter 发现、参数/信号/退出码、create/delete/state 和重启恢复 | 高 | 待验证 |
| NVIDIA GPU | 整卡；MIG；有 vGPU 环境时单列 | GPU 分配、`nvidia-smi`/工作负载、持久化写入、重建与清理 | 高 | 待验证 |
| AMD GPU/ROCm | 整卡；有切分能力时单列 | GPU 工作负载、设备节点/环境保留、持久化与清理 | 高 | 待验证 |
| Intel GPU | 整卡；有虚拟化切分时单列 | GPU 工作负载、RuntimeClass/handler 路径和持久化 | 中 | 待验证 |
| MThreads GPU | 整卡；有切分能力时单列 | 厂商工作负载、adapter 路径、持久化与清理 | 中 | 待验证 |
| Ascend NPU | 训练/推理整卡；910/310 等可获得型号分别记录 | NPU 工作负载、设备节点/环境、持久化、重建和清理 | 高 | 待验证 |
| Ascend NPU 切分 | 虚拟 NPU、资源切分或调度插件形态 | 每种切分策略独立完成整套 NPU E2E，不从整卡结果推导 | 高 | 待验证 |
| 其他 NPU/加速器 | 厂商 runtime 属于 runc-v2 BinaryName 模型 | 按“runc-v2 自定义 runtime + 设备 E2E”执行 | 中 | 待验证 |
| Kata + QEMU（Rust） | io.containerd.kata-qemu.v2；显式 runtime_path/ConfigPath；flat-lowerdirs | task proxy 正向持久化、多容器、互斥、rootfsd 重启和清理 | 已接入，继续补全发布矩阵 | 已验证（限定用例）：Kata 4.2.0 / QEMU 11.0.1 / ext4 local PVC，见结果记录 |
| Kata 通用 runtime type / Go runtime | 明确 QEMU 配置的 io.containerd.kata.v2；其他 Kata 实现/版本 | 分别完成配置发现、shim 协议和正向 E2E | 高 | 已有配置/单元测试；真实集群组合待验证 |
| Kata Cloud Hypervisor | kata-clh 等独立类型或非 QEMU 配置 | opt-in 拒绝或无就绪 adapter；普通 Pod 回归 | 负向用例 | 不支持；完整 workload 负向 E2E 待验证 |
| gVisor | runsc 独立 shim | RootPV opt-in 必须明确拒绝；未 opt-in gVisor Pod 必须不受影响 | 发布阻塞的负向用例 | 不支持（架构边界；M01/M02 已验证） |
| 其他 microVM/独立 shim | 非 `io.containerd.runc.v2` task shim | 作为不支持类型完成 fail-closed 回归；若要支持需独立 RFC | 观察 | 不支持（架构边界） |

Kata + QEMU 已通过独立 task adapter 的限定正向功能验证；它不使用 runc 的 BinaryName hook。
CLH、gVisor 和其他未实现 task shim 的“通过”仅指负向用例。QEMU 的结果不能推导其他 hypervisor、
Kata 实现或版本通过；新增后端仍需独立设计和 E2E。

### 4.3 组合优先级

| 批次 | 目标 | 建议环境 | 完成条件 |
| --- | --- | --- | --- |
| P0：回归基线 | 防止已有能力退化 | Kubernetes v1.29 + containerd 2.1 + amd64 + runc + RWO ext4 | 每次发布候选运行完整 E2E、恢复与性能基线 |
| P1：版本兼容 | 验证 Kubernetes/containerd 主版本差异 | v1.27/1.6、v1.28/1.7、v1.30/2.0 等受支持配对 | 每个组合完成全部基础用例 |
| P2：架构与存储 | 验证 arm64、内核、XFS | arm64 基础组合；不同内核和 XFS 环境 | 每个高风险维度在至少一个基础组合完成全部基础用例 |
| P3：设备 runtime | 验证厂商 adapter 和设备工作负载 | 各厂商真实设备节点及匹配 driver/device plugin | 设备用例 + 基础用例全部通过，且记录厂商栈版本 |
| P4：Kata QEMU 正向兼容 | 完成已接入 task adapter 的发布覆盖 | QEMU Rust、通用 Kata type、Go runtime、动态 CSI | 专项 KQ 用例与适用 C/R/U 用例，分别记录证据 |
| P5：未支持 shim 负向兼容 | 固化安全边界，避免静默错误路径 | Kata CLH、gVisor 和其他未实现 shim | opt-in 明确拒绝或不就绪、普通工作负载不受影响 |

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

### 5.2 运行时变更、并发和卸载专项方案

本节将 C06 至 C09 拆成可执行的演练。所有演练仅允许在已 cordon 的 canary 节点上执行；该节点不得承载
业务 Pod、控制面组件或不可中断的存储工作负载。一次演练只改变一个变量，结束后必须恢复到开始时记录的
版本、配置、节点标签和调度状态。

#### 5.2.1 统一前置、证据和停止条件

1. 固定 RootPV 镜像 digest、Chart 版本、测试镜像 digest、StorageClass、目标 RuntimeClass 和目标节点；
   记录 Kubernetes、containerd、runc、内核、CSI 插件、CNI 插件和 kubelet 的精确版本。
2. 为每轮创建独立命名空间和独立 RWO ext4 PVC。首次 Pod 写入唯一标记、文件校验和及时间戳；重建 Pod
   必须读回该标记后再写入第二个标记。每轮结束删除命名空间并等待 PVC、PV、VolumeAttachment 和对应
   mount 全部清理。
3. 在变更节点前备份 containerd 二进制、service unit、drop-in、配置及其校验和，并记录
   `mountinfo`、RootPV runtime state/lease、节点标签和 cordon 状态。备份和恢复文件不进入代码仓库。
4. 采集 Pod events、controller/rootfsd 日志、`crictl ps`、containerd journal、CSI node plugin 日志和
   变更前后配置 diff。记录中不得包含集群地址、账户、令牌或 kubeconfig。
5. 若节点在预设超时内未 Ready、CSI 未 Ready、普通非 RootPV PVC 无法挂载，或出现无法清理的 RootPV
   mount/lease，立即停止后续用例，删除测试资源并按备份恢复；该轮结果为失败，不以跳过检查换取通过。

直接替换 containerd 二进制或重启 containerd 后，必须先运行普通 `emptyDir` Pod 和普通 PVC Pod 作为
运行时/存储对照。在当前测试环境中，长时间运行的 CSI node plugin 在裸切换 containerd 后需要重建以刷新
其 socket 和 mount namespace；只有该插件 Ready 且普通 PVC 成功挂载后，才可执行 RootPV 用例。该恢复步骤
属于测试环境切换流程，不得把它误记为 RootPV 修复或兼容性结论。

#### 5.2.2 演练矩阵

| 编号 | 场景与变更 | 关键步骤 | 通过标准 | 清理与恢复 |
| --- | --- | --- | --- | --- |
| R01 | rootfsd 重启 | 运行 RootPV Pod 并写入标记；仅重启目标节点 rootfsd Pod；确认原 Pod 状态，再删除并重建 Pod | 原 Pod 不出现非预期重启；重建 Pod 读回并更新标记；无遗留 lease/mount | 删除命名空间，等待资源清理；无需改变 containerd |
| R02 | containerd 服务重启 | 建立 RootPV Pod 和普通 PVC 对照 Pod；执行受控 `systemctl restart containerd`；等待 node、kubelet、CSI 和 rootfsd 恢复 | 普通 PVC 对照与 RootPV Pod 均可恢复或按预期被 kubelet 重建；RootPV 重建后读回标记；运行时 handler/config 未重复写入 | 删除命名空间；若 CSI 未恢复，重建其 node plugin 后再判定，并将该动作记录为环境恢复 |
| R03 | 节点重启 | 建立同 R02 的两类 Pod；安全重启已 cordon 的节点；等待 SSH、node Ready、CSI、rootfsd 和 adapter-ready 标签恢复 | 节点恢复后普通 PVC 可挂载；RootPV Pod 读回并更新标记；无失联 VolumeAttachment、僵尸 task、mount 或 lease | 删除命名空间；核对节点仍处于原 cordon 状态 |
| U01 | RootPV 升级/回滚 | 从固定稳定 Chart 升级至候选 Chart；等待 controller 与节点代理就绪；执行 C02/C04；再回滚至稳定 Chart 并重复 | 两次变更均不破坏已有持久根目录；handler 与 wrapper 配置保持单一、可用；普通 Pod 不被修改 | 删除命名空间；使用 `helm history`、values 与镜像 digest 归档证据 |
| U02 | containerd 升级/回滚 | 按备份替换为候选的官方校验二进制和匹配配置；完成普通对照、CSI 检查和 C04；恢复原二进制/config 后重复对照 | 候选和恢复后的原版本均通过普通 PVC 与 RootPV C04；恢复后的版本、配置 checksum、drop-in 和 cordon 与开始一致 | 删除命名空间；还原备份；重建 CSI node plugin 并确认 Ready |
| C07-A | 独立根文件系统并发 | 并发创建至少 10 个 Pod，每个 Pod 使用独立 PVC 和独立 `rootfs-path`；同时写入不同标记并各自重建 | 所有 Pod 只读取自己的标记；无串写、错误 lease、泄漏 mount 或未释放 VolumeAttachment | 删除命名空间并等待所有存储资源回收；保存并发窗口内的 rootfsd 日志 |
| C07-B | 同一根文件系统互斥 | 针对同一 PVC 和同一 `rootfs-path` 并发提交两个 opt-in Pod | 两个 Pod 不得同时 Ready 并写入同一 persistent rootfs；第二个请求必须被拒绝、保持未就绪或在规定超时内失败，并有可诊断原因 | 删除两个 Pod 和 PVC；确认失败请求没有遗留状态 |
| U03 | RootPV 卸载 | 先确认集群中不存在 opt-in Pod、RootPV mount 或活跃 lease；卸载 Chart；等待 controller/agent 资源消失；创建普通 Pod 和普通 PVC 对照 | 普通工作负载正常；containerd 配置不再含 RootPV handler；不存在 RootPV RuntimeClass、socket、状态目录或遗留 mount；不能在仍有 opt-in Pod 时静默卸载 | 若卸载失败，恢复原 Chart/values；完成清理后才允许结束演练 |

R02、R03 和 U02 不要求原容器进程保持 PID 不变：containerd 或节点重启后由 kubelet 重建容器是允许的。
它们的核心断言是普通工作负载恢复、持久根目录内容正确、RootPV 状态可清理，以及宿主机不残留资源。

#### 5.2.3 执行顺序与自动化边界

先执行 R01，再依次执行 R02、R03、C07-A、C07-B、U01、U02、U03。R02/R03/U02 每次均先执行普通
PVC 对照；U03 必须最后执行，并使用专用短生命周期测试安装，不能在共享或生产安装上执行。每项连续成功
三轮才可将对应组合标记为“已验证”；首次失败即停止该组合并创建脱敏故障记录。

后续测试脚本应以 `.env` 中的 `TARGET_NODE_SELECTOR`、`ROOTPV_RELEASE`、`ROOTPV_CHART`、
`ROOTPV_IMAGE`、`STABLE_*`、`CANDIDATE_*`、`STORAGE_CLASS`、`PARALLEL_PODS`、超时和恢复命令为
输入。脚本必须提供 `--dry-run`、唯一测试前缀、trap 清理、资源清单和 `--keep-artifacts-on-failure`；不得
硬编码 SSH 地址、节点名、镜像仓库或集群专有路径。涉及 SSH/reboot/containerd 二进制替换/Helm uninstall
的步骤默认要求显式开关，避免 CI 或误操作触发节点级变更。

### 5.3 设备 runtime 的附加用例

| 编号 | 用例 | 通过标准 |
| --- | --- | --- |
| D01 | 厂商基线 | 不启用 RootPV 时，设备 plugin、RuntimeClass 与最小厂商工作负载正常 |
| D02 | 资源调度 | 启用 RootPV 后 Pod 仍获得请求的设备资源，Shadow RuntimeClass 保留原 RuntimeClass 的调度与开销语义 |
| D03 | 设备可用性 | 容器中执行厂商诊断或最小计算任务成功，不仅检查设备文件存在 |
| D04 | 持久化重建 | 在持久根目录写入标记后重建 Pod，设备工作负载和标记均正常 |
| D05 | 故障清理 | 删除/强制删除后设备资源可重新分配，且 RootPV 无残留 mount 或 lease |
| D06 | 切分隔离 | 对 MIG、vGPU、虚拟 NPU 等形态，分别验证资源边界、重建和其他切分实例不受影响 |

### 5.4 未支持 microVM 与独立 shim 的负向用例

本节适用于 Kata CLH、gVisor 和其他未实现类型，不用于拒绝已接入的 QEMU adapter。

| 编号 | 用例 | 通过标准 |
| --- | --- | --- |
| M01 | RootPV opt-in | 使用 Kata CLH/gVisor/其他未实现独立 shim 的 RootPV opt-in Pod 被明确拒绝，错误信息说明不支持的 runtime type |
| M02 | 非 opt-in 回归 | 相同 runtime 的普通 Pod 能按其原有路径运行，RootPV 不修改其 handler、配置或运行结果 |
| M03 | 配置完整性 | 节点代理不为不支持 runtime 创建 Shadow handler、wrapper delegate 或误导性的 ready 标签 |

### 5.5 Kata + QEMU 专项用例

| 编号 | 用例 | 当前证据与待补项 |
| --- | --- | --- |
| KQ01 | 源 runtime 与发现 | 原 QEMU RuntimeClass 可运行；imports 中的 QEMU handler 自动进入 inventory/catalog/registry；已通过 |
| KQ02 | 调度与多容器 | Shadow 保留原 scheduling/overhead；app 和 sidecar 在同一 QEMU sandbox 中运行；已通过 |
| KQ03 | rootfs 重建与 sidecar 隔离 | app 内容、0640 权限与符号链接在两次复用后保留，sidecar 标记不保留；已通过 |
| KQ04 | 单写者互斥 | 同 PVC/path 的第二个 Pod 被 persistent_upper_in_use 拒绝，原数据完整；已通过单轮 |
| KQ05 | rootfsd 重启 | 活跃写层锁恢复，已有 app 可读，随后可正常释放；已通过单轮；不等同于完整 C06 |
| KQ06 | 正常删除 | Kata Delete 后 merged 消失、state 删除、lease released；已通过；强制删除与 OOM 待验证 |
| KQ07 | 普通 Kata 回归 | 不 opt-in 的 Pod 保持原 RuntimeClass、没有 adapter annotation；已通过 |
| KQ08 | 发布矩阵补全 | 动态 CSI、containerd/节点重启、QEMU/代理崩溃、升级回滚、性能、设备、checkpoint 恢复待验证 |

证据组合：Linux AMD64、Kubernetes v1.34.3（厂商构建）、containerd 2.1.1、
Kata 4.2.0 Rust、QEMU 11.0.1、guest kernel 6.18.35、ext4 local RWO PVC。
[详细记录](../../tests/e2e/results/kata-qemu-2026-10-14.md)中的功能结果不补齐 C04 的动态 CSI、
C06 的所有重启类型或 C10 的性能基线。新可复用清单见
[tests/e2e/manifests/kata-qemu.yaml](../../tests/e2e/manifests/kata-qemu.yaml)，
运行与清理步骤见 [Kata 使用指南](../user-guide/kata-qemu.md)。

## 6. 自动化、环境接入与结果发布

现有 `tests/e2e/run-functional.sh` 和 `tests/e2e/run-performance.sh` 是 P0 的基础。后续按以下方式
扩展，所有环境相关参数继续仅放在 Git 忽略的 `.env` 配置中：

1. 为每个矩阵组合建立不含凭据的配置样例，例如 `configs/example-nvidia.env`、`configs/example-ascend.env`；
2. 使用现有 `ORIGINAL_RUNTIME_CLASS` 参数，并增加目标 node selector、设备资源名/数量、厂商诊断命令和预期拒绝原因等参数，禁止在
   shell 脚本中按厂商名称硬编码分支；
3. 增加设备 E2E、恢复 E2E、Kata QEMU 正向 E2E 和未支持 shim 负向 E2E，复用功能测试的命名空间前缀、超时和清理保护；
4. CI 至少运行 P0；需要特定硬件或许可证的 P3/P4/P5 测试由专用环境定期执行，并将结果归档到
   `tests/e2e/results/`；
5. 每次新增“已验证”组合时，同步更新本表、根 README 的当前范围说明和相应测试记录。

## 7. 后续环境招募清单

优先寻找以下环境，不要求一次具备所有条件：

1. 分别运行 containerd 1.6、1.7、2.0 和 2.1 的 Kubernetes 集群；
2. amd64 和 arm64 Linux 节点，覆盖至少两类内核/发行版；
3. ext4 与 XFS 的 RWO block PVC；
4. NVIDIA（含 MIG/vGPU）、AMD ROCm、Intel、MThreads GPU，以及 Ascend 整卡和虚拟 NPU 节点；
5. 配置 QEMU 的 Kata 集群，用于补全动态 CSI、Go runtime、故障恢复和性能验证；
6. 配置 Kata CLH、gVisor/runsc 或其他未实现 shim 的集群，用于负向兼容回归。

环境接入前应确认可以创建隔离 namespace、使用专用 RWO PVC、在 canary 节点部署特权 RootPV 组件，并能够
收集必要的节点诊断信息。任何包含生产负载、共享凭据或无法清理测试 PVC 的环境都不应直接用于本方案。
