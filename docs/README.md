# RootPV 文档

## 部署与使用

- [部署指南](user-guide/deployment.md)：构建镜像、Helm/渲染清单安装、路径配置、就绪和卸载。
- [域名迁移指南](user-guide/domain-migration.md)：openfuyao.cn 协议键、替换脚本与已有安装升级。
- [Kata + QEMU 使用指南](user-guide/kata-qemu.md)：运行时前提、完整测试 YAML、持久化重建与排障。
- [Helm Chart 说明](../charts/rootpv/README.md)：安装参数、OCI/Kata 两条接入路径与验证命令。
- [完整 Kata 测试清单](../tests/e2e/manifests/kata-qemu.yaml)：Namespace、local PV、PVC 和双容器 Pod。
- [gVisor 使用指南](user-guide/gvisor.md)：原生 shim 配置、持久化与完整测试清单。
- [Ascend 910C / 950DT 使用指南](user-guide/ascend.md)：原厂 runtime、containerd v3、芯片资源单位、950DT 宿主参数和完整 local PV 清单。
- [NVIDIA 使用指南](user-guide/nvidia.md)：CDI/legacy 接入、local PV 清单、驱动升级和 MIG 边界。

## 设计与演进

- [项目立项评审](design/01-k8s-persistent-rootfs-community-project-review.md)：原始问题定义和 runc 路径评审；后续变化见文首更新说明。
- [OCI wrapper 与 adapter 设计](design/02-unified-runtime-wrapper-runtime-adapter-design.md)：runc-v2 的委托和状态边界。
- [自动发现与 catalog 设计](design/03-configmap-driven-runtime-adapter-extension-design.md)：inventory、ID、路由、Shadow handler 和 readiness。
- [Kata task shim 设计](design/05-kata-qemu-task-shim-adapter-design.md)：QEMU 接入、共享 sandbox 和写层生命周期。
- [gVisor runsc adapter 设计](design/06-gvisor-runsc-runtime-adapter-design.md)：shim 配置克隆、写入透传与 gofer 清理。

## 兼容性与测试证据

- [兼容性方案与矩阵](design/04-compatibility-validation-plan.md)：区分已验证用例、待验证组合和不支持类型。
- [E2E 使用说明](../tests/e2e/README.md)：脚本配置、Kata 手工测试和性能比较边界。
- [Kata 功能验证记录](../tests/e2e/results/kata-qemu-2026-10-14.md)：2026-10-14 的固定版本组合和已通过用例。
- [域名迁移本地冒烟](../tests/e2e/results/domain-migration-2026-10-14.md)：维护期间完成的新协议键本地检查。
- [新域名集群冒烟](../tests/e2e/results/domain-smoke-2026-10-14.md)：v1.37.1 下 runc/Kata 的启动、持久化重建和清理。
- [gVisor 功能验证](../tests/e2e/results/gvisor-2026-10-20.md)：隔离节点测试与在线准入验证边界。
- [gVisor shim 生命周期](../tests/e2e/results/gvisor-shim-lifecycle-2026-10-20.md)：启动分组原因与原生自动退出对照。
- [gVisor 在线功能验收](../tests/e2e/results/gvisor-online-2026-10-20.md)：真实 Webhook/catalog、三代重建、自动退出与环境恢复。
- [NVIDIA 功能验收](../tests/e2e/results/nvidia-2026-11-08.md)：CDI 实算、legacy hook、三代重建、隔离和清理；标明驱动/MIG 未覆盖范围。

- [Ascend 910C 功能验收](../tests/e2e/results/ascend-910c-2026-11-11.md)：ARM64、NPU 实算、持久化、隔离和清理。
- [Ascend 950DT 功能验收](../tests/e2e/results/ascend-950-2026-11-12.md)：厂商 udma 配置、8 个 NPU 实算、三代持久化、服务重启后计算及 Webhook/catalog。

设计中的实施目标不等于已验证能力；对外说明以兼容性矩阵和对应结果记录为准。
