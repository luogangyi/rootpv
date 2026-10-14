# openfuyao.cn 域名迁移集群冒烟

- 日期：2026-10-14；实现提交：9a5cec8，分支 lgy/kata-qemu。
- 平台：Linux AMD64 裸金属 worker，宿主机运行 5.10 系列厂商内核（构建标识已匿名）。
- Kubernetes API 与测试节点：v1.37.1；所有控制面在测试前后均通过 /readyz。
- containerd：2.1.1，原生 overlayfs snapshotter；节点使用自定义 kubelet/containerd 数据目录。
- Kata：4.2.0 Rust runtime，io.containerd.kata-qemu.v2；QEMU 11.0.1 (kata-static)，guest kernel 6.18.35。
- 存储：专用静态 local PV、Filesystem + ReadWriteOnce PVC，ext4 独立块设备，Retain。
- 镜像与安装：使用已缓存的 nginx:1.21.4 和当前提交交叉编译的四个 Linux 二进制。
  当前 Chart 渲染后增加测试二进制 hostPath，并调整 Controller、rootfsd 与 installer 的执行入口。
  未构建或推送完整 RootPV 容器镜像。
- 隔离：独立系统/测试 namespace，仅 BM 节点运行临时组件；Webhook 只匹配测试 namespace 标签。

## 已通过

| 检查 | 实际结果 |
| --- | --- |
| 原 runtime 前置检查 | 未安装 RootPV 时普通 Kata Pod Ready，可 exec，guest 内核 6.18.35 |
| 新域名发现链路 | openfuyao.cn 下的安装/就绪标签、inventory annotation、catalog 标签及生成标记贯通 |
| Shadow RuntimeClass | runc-v2 与 kata-qemu-v2 分别生成；Kata 保留原节点选择、CPU 250m/内存 160Mi overhead |
| CRD/RBAC/Webhook | 新 API group 与 webhook 名称生效；Controller 可读取新 CRD 资源，node service account 可 patch status |
| 注解进入运行时 | Shadow handler 的新域名白名单加载；新注解正确选择目标 app、只读 subPath anchor 和具体 adapter |
| runc 与 Kata 首次启动 | 两个 app/sidecar Pod 均 2/2 Ready；计数为 1，首次 UID 与当前 Pod UID 相同 |
| PVC 写层真实性 | 容器根目录标记与 payload 在宿主机对应 PVC upper 中可读 |
| 删除重建 | 两条路径各完成一次复用；Pod UID 改变，计数为 2，首次 UID 保持，payload 可读 |
| 文件属性 | 0640 权限与符号链接在重建后保留 |
| Sidecar 隔离 | sidecar 看不到 app 标记；上一轮专门写入 sidecar 的标记在重建后不存在 |
| 普通 Kata 回归 | RootPV Webhook 运行期间新建的普通 Kata Pod 保持原 RuntimeClass，没有 adapter annotation |
| 非法配置拒绝 | 不存在的目标容器被 mpod.persistent-rootfs.openfuyao.cn 以 missing container 错误拒绝 |
| 正常删除 | 两条路径均无残留 state.json 或 merged 挂载，lease phase 为 released |
| 安装清理 | 本次 namespace、PV/PVC、Shadow 类、CRD/RBAC/Webhook、节点 registry、别名与二进制均清理；本次 sandbox/delegate 不再存在 |
| 配置与元数据还原 | containerd 主配置按字节恢复；仅还原本次 RootPV 节点键；原 Kata RuntimeClass、handler 与安装保留 |
| 原有负载 | 测试前的所有 Running Pod 的 UID、phase、各容器 ready/restartCount 在重启及最终清理后均未改变；节点 Ready |

containerd 加载和还原配置前均检查 KillMode=process、无 ExecStop，现有 shim 独立运行。
没有排空节点、删除原有 Pod 或改动原 Kata/CLH 安装。

专用测试数据保留在原 ext4 文件系统的备份目录；配置、二进制、就绪快照和结果摘要也保留在节点/控制面专用备份中。
复用示例见 [完整 Kata YAML](../manifests/kata-qemu.yaml) 和 [操作指南](../../../docs/user-guide/kata-qemu.md)。

## 范围与限制

本轮验证新域名贯穿 admission、discovery、containerd、runc/Kata 与持久写层生命周期。
每条持久化路径为两代 Pod、一次复用；不能代替完整兼容性或故障恢复矩阵。
未覆盖动态 CSI、节点/VM 崩溃、RootPV 镜像发布安装、性能、设备、网络/DNS或其他 Kata 后端。
API 与节点健康检查仅作为本轮前置条件，网络/DNS 应另行验收。

维护期间完成的本地测试保留在 [本地记录](domain-migration-2026-10-14.md)；
升级步骤见 [域名迁移指南](../../../docs/user-guide/domain-migration.md)。
