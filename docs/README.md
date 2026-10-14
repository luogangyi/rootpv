# RootPV 文档

## 部署与使用

- [部署指南](user-guide/deployment.md)：构建镜像、Helm/渲染清单安装、路径配置、就绪和卸载。
- [Kata + QEMU 使用指南](user-guide/kata-qemu.md)：运行时前提、完整测试 YAML、持久化重建与排障。
- [Helm Chart 说明](../charts/rootpv/README.md)：安装参数、OCI/Kata 两条接入路径与验证命令。
- [完整 Kata 测试清单](../tests/e2e/manifests/kata-qemu.yaml)：Namespace、local PV、PVC 和双容器 Pod。

## 设计与演进

- [项目立项评审](design/01-k8s-persistent-rootfs-community-project-review.md)：原始问题定义和 runc 路径评审；后续变化见文首更新说明。
- [OCI wrapper 与 adapter 设计](design/02-unified-runtime-wrapper-runtime-adapter-design.md)：runc-v2 的委托和状态边界。
- [自动发现与 catalog 设计](design/03-configmap-driven-runtime-adapter-extension-design.md)：inventory、ID、路由、Shadow handler 和 readiness。
- [Kata task shim 设计](design/05-kata-qemu-task-shim-adapter-design.md)：QEMU 接入、共享 sandbox 和写层生命周期。

## 兼容性与测试证据

- [兼容性方案与矩阵](design/04-compatibility-validation-plan.md)：区分已验证用例、待验证组合和不支持类型。
- [E2E 使用说明](../tests/e2e/README.md)：脚本配置、Kata 手工测试和性能比较边界。
- [Kata 功能验证记录](../tests/e2e/results/kata-qemu-2026-10-14.md)：2026-10-14 的固定版本组合和已通过用例。

设计中的实施目标不等于已验证能力；对外说明以兼容性矩阵和对应结果记录为准。
