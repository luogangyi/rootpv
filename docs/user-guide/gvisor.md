# gVisor 持久化根文件系统

RootPV 支持 containerd 的 `io.containerd.runsc.v1`，保留原生 gVisor shim，
通过该 shim 的 `binary_name` 接入 OCI wrapper。使用自动发现和 catalog v2 安装路径；
旧静态 runc/NVIDIA 配置不提供该适配器。

## 源运行时与安装

先安装并独立验证 runsc、containerd-shim-runsc-v1 及配套 gvisor-bin 文件。
RootPV 镜像不安装 gVisor。以下假设 RuntimeClass 名为 gvisor、handler 名为 runsc：

先在原生 shim 启动阶段读取的全局配置中启用分组，例如 /etc/containerd/runsc/config.toml：

```toml
grouping = true
```

原生 shim 优先读取 /run/containerd/runsc/config.toml，其次读取 /etc/containerd/runsc/config.toml。
第一份存在的文件必须显式启用 grouping；仅在 handler 的 ConfigPath 设置它不够。
在实测 containerd 2.1.1 / runsc 20261012.0 中，后者导致双容器 Pod 创建三个 shim，删除后
两个闲置 shim 残留；补齐全局配置后，每个 Pod 只有一个 shim，删除后自动退出。
RootPV 会检查全局配置并把路径和内容纳入节点摘要；不会自动修改该共享配置。

源 handler 配置：

```toml
[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.runsc]
  runtime_type = "io.containerd.runsc.v1"
  runtime_path = "/opt/gvisor/containerd-shim-runsc-v1"
[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.runsc.options]
  TypeUrl = "io.containerd.runsc.v1.options"
  ConfigPath = "/etc/containerd/runsc.toml"
```

containerd 2.x 的原生插件配置使用 io.containerd.cri.v1.runtime；沿用已有节点的插件名。
ConfigPath 必须为绝对路径，文件中的 binary_name 也必须显式提供绝对路径：

```toml
binary_name = "/opt/gvisor/runsc"
grouping = true
[runsc_config]
  platform = "systrap"
```

runsc_config 中的值必须为字符串。按 [部署指南](deployment.md)安装同版本 Controller、node-agent
和 OCI wrapper。无需新增 shim 二进制。系统生成 persistent-rootfs-gvisor RuntimeClass、
persistent-rootfs-runsc-v1 handler 和独立的 /etc/rootpv/runsc/runsc-v1-<配置摘要>.toml。
生成文件权限为 0600，以内容摘要区分配置版本，避免原地覆盖仍被运行中 sandbox 引用的配置。
真实 runsc 路径固定在节点 registry；原 handler 和配置文件保持原状。停止使用 adapter 后，
先排空相关 Pod 并确认没有 sandbox、task 或恢复状态引用，再由管理员清理旧生成文件和别名。
handler 名称不同于 runsc 时，adapter ID 随之变化，以 catalog 为准。

生成配置将 binary_name 指向 wrapper 别名，并设置 overlay2=none；必须关闭 gVisor 内部临时写层，
应用写入才能经 gofer 落到 RootPV 的 PVC upperdir。其他运行时选项、annotations、原 RuntimeClass
的 scheduling、tolerations 和 overhead 保留。普通 gVisor Pod 使用原 handler 及原 overlay 配置。
原生 shim 会把 `[runsc_config]` 转成 runsc CLI flags，因此 wrapper 继续核验实际 argv 中的
`--overlay2=none`；缺少参数不能视为已经禁用临时写层，参见 [官方 shim 配置说明](https://gvisor.dev/docs/user_guide/containerd/configuration/)。

## 使用与重建

```yaml
metadata:
  annotations:
    openfuyao.cn/persistent-rootfs: "true"
    openfuyao.cn/rootfs-container: app
    openfuyao.cn/rootfs-volume: rootfs-data
    openfuyao.cn/rootfs-path: applications/example
    openfuyao.cn/rootfs-base-image-policy: Strict
spec:
  runtimeClassName: gvisor
```

PVC volume 与 runc/Kata 相同，Webhook 为目标容器注入只读 subPath anchor。
未选中的 sidecar 使用普通 containerd 快照，写入不会跨 Pod 重建保留。

[完整测试 YAML](../../tests/e2e/manifests/gvisor.yaml)包含 Namespace、静态 local PV、PVC 和双容器 Pod。
替换模板节点与镜像，在目标节点创建 /var/lib/rootpv/gvisor-test-data，确认 ext4/XFS 支持 OverlayFS upper/work。
模板假设 adapter ID 为 runsc-v1，其他 handler 名称需同步修改 ROOTPV_ADAPTER 的预期值。

```bash
kubectl get runtimeclass gvisor persistent-rootfs-gvisor
kubectl apply -f tests/e2e/manifests/gvisor.yaml
kubectl -n rootpv-test-gvisor wait --for=condition=Ready pod/gvisor-rootpv-test --timeout=180s
kubectl -n rootpv-test-gvisor logs gvisor-rootpv-test -c app
kubectl -n rootpv-test-gvisor logs gvisor-rootpv-test -c sidecar
```

首次显示 persistent-start-count=1。保留 PVC，只删除 Pod 后重建：

```bash
kubectl -n rootpv-test-gvisor delete pod gvisor-rootpv-test --wait=true
kubectl apply -f tests/e2e/manifests/gvisor.yaml
kubectl -n rootpv-test-gvisor wait --for=condition=Ready pod/gvisor-rootpv-test --timeout=180s
kubectl -n rootpv-test-gvisor logs gvisor-rootpv-test -c app
```

第二轮计数为 2，current-pod-uid 改变，first-pod-uid 保留。
/rootpv-gvisor.payload 的 0640 权限与符号链接应保留，sidecar 每轮从新根目录启动。
结束后删除 namespace 和 PV；Retain local PV 的节点目录需另行清理，不能删除其他测试或业务数据。

## 失败关闭与支持边界

| 现象或原因 | 检查项 |
| --- | --- |
| runsc_options_type_required | options.TypeUrl 必须为 io.containerd.runsc.v1.options |
| runsc_config_path_required / runsc_config_unreadable | 显式绝对 ConfigPath、文件存在和读取权限 |
| runsc_binary_path_required / delegate_missing | binary_name 必须为真实可执行的绝对路径 |
| runsc_flags_invalid | runsc_config 必须为字符串键值表 |
| runsc_shim_grouping_required | 启动阶段生效的全局 shim 配置需显式 grouping=true，仅配置 handler 的 ConfigPath 不够 |
| runsc_shim_config_unreadable / runsc_shim_config_invalid | 检查全局 shim TOML 的读取权限、语法及 grouping 布尔类型 |
| runtime_digest_unavailable | handler options 编码及配置文件读取必须成功 |
| runsc RootPV requires --overlay2=none | 检查生成的 shim 配置和实际 runsc 参数，不能使用内部临时写层 |
| runsc shim did not forward --overlay2=none | 检查 shadow handler 的 ConfigPath 是否指向生成配置，以及原生 shim 是否将 runsc_config 转为 CLI 参数 |
| rootfs annotation override is unsupported | 移除目标容器 dev.gvisor.spec.rootfs.* 根目录覆盖注解 |
| persistent_upper_in_use | 等待同 PVC/path 的旧容器删除、gofer 退出及 merged 卸载 |

要求 flat-lowerdirs 和显式的绝对 `--root`。创建及删除参数写入恢复状态，删除时拒绝不同的 runtime root。
删除和恢复先清理 runsc/gofer，再正常卸载、确认挂载消失并释放租约；
清理无法确认时保留状态和租约。恢复时重新查找 namespace 持有者，并检查宿主机 PID 1 的同名挂载。
原 namespace 在开始恢复前已消失时，不尝试 PID 1 回退删除，需要人工核验 runtime 状态与挂载，
完成清理后再处理租约；本版本不提供历史状态迁移命令。
旧状态若未保存绝对 `--root`，同样需要人工核验；不能使用当前配置或默认路径猜测历史 runtime root。
wait、exec、stats、ps、signals 等继续委托给创建时固定的 runsc。

节点配置发生变更前先撤回 adapter 就绪标签并持久化 `.pending-reload`，配置重载成功后才恢复就绪。
catalog 读取或解析失败时尝试撤回 adapter 就绪标签，恢复读取并成功协调后再发布。
重载失败或进程中断后保留该标记，下轮继续重试；必须配置 `containerdRestartCommand`。
重载成功但标记删除失败时，本次 node-agent 进程只重试删除标记，继续发布已生效配置的就绪状态；
后续配置变化仍触发重载。若 node-agent 在清理完成前重启，会保守地重新重载一次。
自动配置要求合并 imports 后只有一个 CRI runtime 配置表；新旧 CRI runtime 插件表并存时，
先由管理员确认生效插件并清理过时配置，再启用自动适配。
runsc 的配置或安装失败只撤回该 adapter 的新建授权，不阻断健康的 runc/Kata adapter。
失效 adapter 不保留旧 registry 授权条目；已有任务的查询和删除使用保存的 delegation 路由，
其 handler 和历史 shim 配置须保留到排空并核验引用后再清理。
`--publish-node-capabilities=false` 同时关闭基础能力及 adapter 就绪标签的发布和撤回；
该开关不停止配置协调，隔离手工测试若也需停用自动发现和安装，应同时使用 `--runtime-adapter-discovery=false`。

限定功能证据见 [在线 Webhook/catalog 验收](../../tests/e2e/results/gvisor-online-2026-10-20.md)：
真实自动发现、节点 reconcile、准入调用、三代重建、sidecar、单写者、rootfsd 重启及 shim 自动退出。
初次采用离线准入的 [隔离记录](../../tests/e2e/results/gvisor-2026-10-20.md)保留其历史测试边界。
动态 CSI、性能、设备、checkpoint、Webhook/配置中断与节点/Sentry/gofer 故障恢复需独立验证。
初次测试出现的闲置 shim 已定位为全局 grouping 配置缺失；修正后的原生对照见
[shim 生命周期记录](../../tests/e2e/results/gvisor-shim-lifecycle-2026-10-20.md)。

参考：[官方 shim 配置](https://gvisor.dev/docs/user_guide/containerd/configuration/)、
[官方文件系统说明](https://gvisor.dev/docs/user_guide/filesystem/)。
