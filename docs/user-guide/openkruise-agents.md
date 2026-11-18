# OpenKruise Agents 使用 RootPV 系统盘

[OpenKruise Agents](https://github.com/openkruise/agents) 的 Sandbox 可以通过
`volumeClaimTemplates` 为每个实例创建独立 PVC，再由 RootPV 将指定容器的可写根文件系统保存到该 PVC。
本文介绍配置、正常暂停/恢复、E2B SDK 接入，以及 2026-11-18 完成的功能和故障验证。

在本文验证的部署中，独立系统盘、同 Pod 容器重启后的数据保留、Stop 暂停后的原盘恢复，均通过配置实现，
没有为测试修改 RootPV、Agents 或 E2B SDK 源码。**根盘持久化不代表 Sandbox 能在任意故障后自动恢复**：
Pod 意外丢失或被 kubelet 驱逐后，所测 Agents 部署会进入 Failed，未自动创建替代 Pod。

这里的“系统盘”指选中业务容器的 `/` 根文件系统。它不包含进程内存、其他容器的普通可写层、额外挂载的
emptyDir 或数据卷；在 Kata 中也不是 VM 的引导盘。本文不涉及 E2B 独立 Volume API。

## 1. 前提与配置关系

先按 [RootPV 部署指南](deployment.md) 安装 controller、node-agent 和对应 runtime adapter，
再按所使用 Agents 版本的说明安装 CRD、Sandbox controller；SDK 场景还需要 sandbox-manager、gateway
以及可用的 agent-runtime/envd 模板。

本指南的集群验证使用 Linux AMD64、containerd `overlayfs`、ext4 文件系统 PVC。其他 CRI 后端的 RootPV
支持范围见各自指南，不能据本文推断已经完成 Agents + CRI-O 集成验证。

准备时确认：

- StorageClass 能提供适合 OverlayFS upper/work 的 `Filesystem` PVC；本次使用 RWO。
  local PV 需要预先制备并配置正确的 nodeAffinity，动态云盘由 CSI 制备。
- RootPV webhook 的 namespace/object selector 覆盖业务命名空间，节点和 adapter 已就绪。
- 主容器镜像固定到 digest，并在正常暂停/恢复期间保持不变；RootPV 使用 `Strict` 检查镜像链。
- 节点上已安装源运行时。Kata 见 [Kata + QEMU 指南](kata-qemu.md)，gVisor 见 [gVisor 指南](gvisor.md)。
  gVisor 的 grouping、独立 shim 配置和 `overlay2=none` 要求同样适用。

以下检查命令假设 RootPV 安装在 `rootpv-system`，资源名沿用本仓库部署清单；自定义安装应替换名称：

```bash
kubectl get crd sandboxes.agents.kruise.io sandboxsets.agents.kruise.io
kubectl -n rootpv-system rollout status deployment/persistent-rootfs-controller
kubectl -n rootpv-system rollout status daemonset/persistent-rootfsd
kubectl get nodes -l openfuyao.cn/persistent-rootfs-ready=true
kubectl get runtimeclass -l openfuyao.cn/managed-by=rootpv
kubectl -n rootpv-system get configmap rootpv-runtime-adapters -o yaml
```

### 关键字段

| 位置 | 配置 | 作用 |
| --- | --- | --- |
| Sandbox / SandboxSet `spec` | `volumeClaimTemplates` | 为每个 Sandbox 创建独立系统 PVC |
| `spec` | `pauseStrategy.type: Stop` | 暂停时停止并删除 Pod，保留 Sandbox 与 PVC |
| `spec` | `persistentContents: [filesystem]` | 使用文件系统持久化语义，不承诺内存恢复 |
| `spec.template.metadata.annotations` | `openfuyao.cn/persistent-rootfs: "true"` | 在生成的 Pod 上启用 RootPV |
| 同上 | `rootfs-container: sandbox` | 选择名为 `sandbox` 的主容器 |
| 同上 | `rootfs-volume: rootfs-data` | 对应 PVC 模板生成的 Pod volume |
| 同上 | `rootfs-path: containers/sandbox` | PVC 内稳定的根盘目录，重建后保持不变 |
| 同上 | `rootfs-base-image-policy: Strict` | 拒绝与原持久写层不兼容的镜像链 |
| 主容器 `volumeMounts` | 显式只读 anchor | 满足 Agents PVC 模板挂载校验，并由 RootPV 识别 |

表中的短 annotation 名均使用 `openfuyao.cn/` 前缀，完整写法见下文。
annotations 必须放在 **Pod 模板** 上，仅放在外层 Sandbox metadata 不能替代它们。

名为 `rootpv-demo` 的 Sandbox 使用名为 `rootfs-data` 的 PVC 模板时，生成的 PVC 为
`rootfs-data-rootpv-demo`。PVC 的 controller owner 是 **Sandbox UID，不是 Pod UID**。
同一 Sandbox 正常重建 Pod 时继续引用这个 PVC。

`rootfs-path` 不应包含会变化的 Pod UID。不同 Sandbox 已有独立 PVC，可以使用相同相对路径。
不要将 backing PVC 整体挂到 `/` 或可写的业务目录，也不要让两个运行中的容器共享同一个 upper/work。

## 2. 独立 Sandbox 示例

下面是从实测清单整理的根盘冒烟示例。它用于验证 Kubernetes Sandbox 生命周期，不包含 envd，不能直接作为
E2B SDK 模板。保存为 `sandbox-rootpv.yaml`，先替换：

- `storageClassName`：已验证的 ext4 Filesystem StorageClass。
- `image`：节点可拉取、固定 digest、默认以 root 运行的 Linux 测试镜像，包含 `/bin/sh`、`mkdir`、`cat`、`sleep`、`sync`。

示例未指定 RuntimeClass，适用于默认运行时为已接入 RootPV 的 runc 的节点。

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: rootpv-agents-demo
---
apiVersion: agents.kruise.io/v1alpha1
kind: Sandbox
metadata:
  name: rootpv-demo
  namespace: rootpv-agents-demo
spec:
  paused: false
  pauseStrategy:
    type: Stop
  persistentContents:
    - filesystem
  volumeClaimTemplates:
    - metadata:
        name: rootfs-data
      spec:
        accessModes: [ReadWriteOnce]
        volumeMode: Filesystem
        storageClassName: <ext4-storage-class>
        resources:
          requests:
            storage: 10Gi
  template:
    metadata:
      annotations:
        openfuyao.cn/persistent-rootfs: "true"
        openfuyao.cn/rootfs-container: sandbox
        openfuyao.cn/rootfs-volume: rootfs-data
        openfuyao.cn/rootfs-path: containers/sandbox
        openfuyao.cn/rootfs-base-image-policy: Strict
    spec:
      restartPolicy: Always
      terminationGracePeriodSeconds: 10
      containers:
        - name: sandbox
          image: <image-repository>@sha256:<image-digest>
          command: [/bin/sh, -ec]
          args:
            - |
              mkdir -p /rootpv-check
              if [ ! -f /rootpv-check/first-pod-uid ]; then
                printf '%s\n' "$POD_UID" > /rootpv-check/first-pod-uid
                printf '%s\n' 'persist-me' > /rootpv-check/content
              fi
              sync
              trap 'exit 0' TERM INT
              while :; do sleep 5 & wait $! || true; done
          env:
            - name: POD_UID
              valueFrom:
                fieldRef:
                  fieldPath: metadata.uid
            - name: ROOTPV_POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              cpu: "1"
              memory: 512Mi
          volumeMounts:
            - name: rootfs-data
              mountPath: /var/run/rootpv/rootfs-anchor/rootfs-data
              readOnly: true
              subPathExpr: .rootpv-anchor/$(ROOTPV_POD_NAME)/sandbox
          readinessProbe:
            exec:
              command: [/bin/sh, -ec, 'test -s /rootpv-check/content']
            periodSeconds: 2
```

不要在 `template.spec.volumes` 再重复声明 `rootfs-data`：Agents 会根据 PVC 模板生成实际 volume 和 claimName。
模板中的只读 mount 仅暴露 `.rootpv-anchor/...` 子目录，用于 PVC 挂载与定位，不是应用写入根盘的位置。
业务文件直接写到 `/rootpv-check` 等容器根目录。

### 为什么要预先声明 anchor

Agents 在验证 Sandbox/SandboxSet 模板时，要求每个 `volumeClaimTemplates` 项被至少一个容器挂载。
RootPV 的 Pod mutation 发生在实际 Pod 创建时，仅依赖它自动补 mount 无法满足更早的模板校验。
因此，在模板中显式声明上述只读 anchor，并设置 `ROOTPV_POD_NAME`。RootPV 会识别现有 mount，
补齐/收敛只读和 subPathExpr，而不是重复挂载；不要同时再设置 `subPath`。

本轮实测中，保留 anchor 的模板通过 server dry-run；移除 anchor 的模板被 Agents webhook 拒绝。

### 选择 Kata 或 gVisor

在 `spec.template.spec` 中添加源 RuntimeClass，例如：

```yaml
runtimeClassName: kata-qemu
```

或者使用集群已配置的 gVisor RuntimeClass：

```yaml
runtimeClassName: gvisor
```

名称以本集群安装为准。RootPV 会选择对应 Shadow RuntimeClass，并保留源运行时的调度约束和 overhead；
不要复制测试环境生成的 handler 名、节点摘要或内部 mutation annotation。
原 RuntimeClass 若限定了某个节点，启用 RootPV 不会取消该限制。

## 3. 创建、暂停、恢复和删除

### 创建与检查

替换示例中的占位值后执行：

```bash
kubectl apply -f sandbox-rootpv.yaml
kubectl -n rootpv-agents-demo wait sandbox/rootpv-demo \
  --for=jsonpath='{.status.phase}'=Running --timeout=180s
kubectl -n rootpv-agents-demo wait pod/rootpv-demo \
  --for=condition=Ready --timeout=180s
kubectl -n rootpv-agents-demo get sandbox,pod,pvc
kubectl -n rootpv-agents-demo get pod rootpv-demo \
  -o jsonpath='{.spec.runtimeClassName}{"\n"}'
kubectl -n rootpv-agents-demo get pvc rootfs-data-rootpv-demo \
  -o jsonpath='{.metadata.ownerReferences}{"\n"}'

ROOTPV_OLD_SANDBOX_UID=$(kubectl -n rootpv-agents-demo get sandbox rootpv-demo -o jsonpath='{.metadata.uid}')
ROOTPV_OLD_POD_UID=$(kubectl -n rootpv-agents-demo get pod rootpv-demo -o jsonpath='{.metadata.uid}')
ROOTPV_OLD_PVC_UID=$(kubectl -n rootpv-agents-demo get pvc rootfs-data-rootpv-demo -o jsonpath='{.metadata.uid}')
ROOTPV_PV=$(kubectl -n rootpv-agents-demo get pvc rootfs-data-rootpv-demo -o jsonpath='{.spec.volumeName}')
kubectl -n rootpv-agents-demo exec rootpv-demo -c sandbox -- cat /rootpv-check/content
```

PVC 应为 Bound；Pod 应使用 RootPV 对应 RuntimeClass；PVC owner 应指向当前 Sandbox。
`WaitForFirstConsumer` 存储在消费者调度前可能保持 Pending，需结合 Pod 调度事件判断，不应仅凭这一状态认定失败。

### Stop 暂停与原盘恢复

在同一 shell 会话执行，保留前面的 UID 变量：

```bash
kubectl -n rootpv-agents-demo patch sandbox rootpv-demo --type=merge \
  -p '{"spec":{"paused":true}}'
kubectl -n rootpv-agents-demo wait sandbox/rootpv-demo \
  --for=condition=SandboxPaused --timeout=180s
kubectl -n rootpv-agents-demo wait pod/rootpv-demo --for=delete --timeout=180s
test "$(kubectl -n rootpv-agents-demo get pvc rootfs-data-rootpv-demo -o jsonpath='{.metadata.uid}')" = "$ROOTPV_OLD_PVC_UID"

kubectl -n rootpv-agents-demo patch sandbox rootpv-demo --type=merge \
  -p '{"spec":{"paused":false}}'
kubectl -n rootpv-agents-demo wait sandbox/rootpv-demo \
  --for=jsonpath='{.status.phase}'=Running --timeout=180s
kubectl -n rootpv-agents-demo wait pod/rootpv-demo --for=condition=Ready --timeout=180s

test "$(kubectl -n rootpv-agents-demo get sandbox rootpv-demo -o jsonpath='{.metadata.uid}')" = "$ROOTPV_OLD_SANDBOX_UID"
test "$(kubectl -n rootpv-agents-demo get pod rootpv-demo -o jsonpath='{.metadata.uid}')" != "$ROOTPV_OLD_POD_UID"
test "$(kubectl -n rootpv-agents-demo get pvc rootfs-data-rootpv-demo -o jsonpath='{.metadata.uid}')" = "$ROOTPV_OLD_PVC_UID"
test "$(kubectl -n rootpv-agents-demo get pvc rootfs-data-rootpv-demo -o jsonpath='{.spec.volumeName}')" = "$ROOTPV_PV"
test "$(kubectl -n rootpv-agents-demo exec rootpv-demo -c sandbox -- cat /rootpv-check/content)" = persist-me
test "$(kubectl -n rootpv-agents-demo exec rootpv-demo -c sandbox -- cat /rootpv-check/first-pod-uid)" = "$ROOTPV_OLD_POD_UID"
```

预期是新 Pod UID、原 Sandbox/PVC/PV 和原文件内容。暂停会结束进程，恢复是新容器冷启动。
不要用直接删除业务 Pod 代替正常暂停：所测 Agents 部署启用了 Pod 删除保护；即使 Pod 实际丢失，
也可能导致 Sandbox Failed，而不是触发支持的恢复路径。

### 删除与回收

“系统 PVC 随 Sandbox 删除回收”指 **Sandbox CR 被删除**。Stop 删除 Pod、同 Pod 容器重启、Pod 驱逐，
都不等于删除 Sandbox，也不应据此回收系统 PVC。

```bash
ROOTPV_RECLAIM=$(kubectl get pv "$ROOTPV_PV" -o jsonpath='{.spec.persistentVolumeReclaimPolicy}')
kubectl -n rootpv-agents-demo delete sandbox rootpv-demo --wait=true
kubectl -n rootpv-agents-demo wait pod/rootpv-demo --for=delete --timeout=180s
kubectl -n rootpv-agents-demo wait pvc/rootfs-data-rootpv-demo --for=delete --timeout=180s
if [ "$ROOTPV_RECLAIM" = Delete ]; then
  kubectl wait "pv/$ROOTPV_PV" --for=delete --timeout=300s
else
  kubectl get pv "$ROOTPV_PV"
fi
```

PV 为 Delete 时，由存储系统执行回收；Retain 时通常进入 Released，底层数据仍保留，需要另行处理。
PVC/PV/VolumeAttachment 消失是本次回收验证的对象级证据，不是对后端介质安全擦除的保证。
回收策略说明见 [Kubernetes Persistent Volumes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#reclaiming)。

## 4. SandboxSet 与 E2B SDK

### 将系统盘配置合入现有 SDK 模板

SandboxSet 使用相同的 `spec.volumeClaimTemplates`、暂停策略和 Pod 模板字段。
每个生成的 Sandbox 会获得自己的 PVC；不要在多个实例中写死同一个 claimName。

先准备一个已经能通过 SDK 创建并使用 envd 的 SandboxSet，再按第 2 节的字段添加 RootPV。
保留原有主镜像/command、agent-runtime 注入或显式 sidecar、helper、postStart、探针、鉴权和网络配置。
新增 env/volumeMount 时按容器名称合并，不能用只包含 anchor 的数组覆盖整个 containers 或 volumeMounts。

改动位置如下；这是合并说明，不是一份独立可应用的资源：

```text
SandboxSet.spec
├── pauseStrategy.type = Stop
├── persistentContents = [filesystem]
├── volumeClaimTemplates += rootfs-data（第 2 节的 PVC 模板）
└── template
    ├── metadata.annotations += 第 2 节的五个 RootPV annotation
    └── spec.containers[name=sandbox]
        ├── env += ROOTPV_POD_NAME
        └── volumeMounts += rootfs-data 的只读 anchor
```

实测 SDK 模板使用 `replicas: 0`，创建时通过 Agents 扩展 metadata
`e2b.agents.kruise.io/create-on-no-stock: "true"` 按需分配，避免预热池提前占用额外 PVC。
该 metadata 是 Agents 扩展，不是所有 E2B 服务通用的参数；所选 Agents 版本必须支持它。
需要预热池时可设置副本数，但池内每个实例也会占用独立系统 PVC，已领取实例不等同于可替换的池副本。

本轮真实 SDK 模板使用 code-interpreter 主镜像和 agent-runtime helper 模式：

- 主容器名为 `sandbox`；agent-runtime 作为 `restartPolicy: Always` 的 init sidecar。
- `/mnt/envd`、`/var/run/agent-runtime`、`/var/run/agent-helper` 使用共享 emptyDir。
- 主容器 postStart 启动 `bash /mnt/envd/envd-run.sh`；envd 提供文件与进程 RPC。
- 启动探针检查 envd 49983，就绪探针检查镜像内 Jupyter 49999 `/health`。

这些是本轮镜像/模板的组合，不是 RootPV 对所有 agent 镜像的固定要求。不要给不包含相应 helper 脚本的镜像
直接添加该 postStart。RootPV 仅选中主容器，辅助容器和 emptyDir 在 Pod 重建时重新初始化。

### SDK 调用示例

以下调用与本轮官方 Python `e2b==2.10.2` 的使用方式一致，没有修改 SDK 或使用 monkey patch。
准备环境变量：

| 变量 | 含义 |
| --- | --- |
| `E2B_API_KEY` | 有权访问该模板的团队 API key |
| `E2B_API_URL` | 客户端可访问的 sandbox-manager E2B API 地址 |
| `ROOTPV_E2B_GATEWAY` | 已配置路径路由的 gateway 基础地址，不带末尾 `/` |
| `E2B_TEMPLATE` | 已配置真实 envd 和 RootPV 的 SandboxSet 名称 |

本例使用所测部署的 `/kruise/<sandbox_id>/49983` 路由。若部署使用域名路由或其他前缀，按其网关规则设置
SDK 连接参数；不要直接沿用 Pod IP，恢复后 Pod IP 可能改变。API 和 gateway 都需要从执行 SDK 的位置可达。

```bash
python3 -m venv .venv
.venv/bin/pip install 'e2b==2.10.2'
```

```python
import os
from e2b import Sandbox

opts = dict(
    api_key=os.environ["E2B_API_KEY"],
    api_url=os.environ["E2B_API_URL"],
    request_timeout=300,
)
created = Sandbox.create(
    template=os.environ["E2B_TEMPLATE"],
    timeout=1800,
    envs={"ROOTPV_CLAIM_MARKER": "created-with-sdk"},
    metadata={"e2b.agents.kruise.io/create-on-no-stock": "true"},
    **opts,
)
sid = created.sandbox_id
gateway = os.environ["ROOTPV_E2B_GATEWAY"].rstrip("/")
connection = dict(opts, sandbox_url=f"{gateway}/kruise/{sid}/49983")
sb = Sandbox.connect(sid, timeout=1800, **connection)
sb.files.write("/root/rootpv-marker", "persist-me", user="root")
sb.commands.run("sync", user="root")
sb.beta_pause(**opts)
sb = Sandbox.connect(sid, timeout=1800, **connection)
assert sb.files.read("/root/rootpv-marker", user="root") == "persist-me"
assert sb.commands.run(
    'printf %s "$ROOTPV_CLAIM_MARKER"', user="root"
).stdout == "created-with-sdk"
sb.kill(**opts)
```

完整验收还应在 Kubernetes 中检查暂停时旧 Pod 真正消失、恢复时 Pod UID 改变而 PVC UID 不变，
以及删除时 Sandbox/PVC/PV 实际完成清理。不能只以 `kill()` 返回成功判断底层资源已回收。

该 SDK 版本使用 `beta_pause()`，文件系统语义由模板的 Stop 策略提供；没有验证新版 `memory:false`
或 `pause(mode="filesystem")` 的协议兼容，也没有在本轮调用 code-interpreter 的 `run_code()`。
Agents 的 SDK 安装与接入说明见 [E2B Python 客户端文档](https://openkruise.io/kruiseagents/user-manuals/e2b-client)。

## 5. 生命周期语义

| 操作 | Pod | 系统 PVC / 数据 | 所测部署的服务行为 |
| --- | --- | --- | --- |
| 同 Pod 主容器异常退出，Always | Pod UID 不变，容器重启 | 原 PVC，文件保留 | SDK 文件/命令访问可恢复；动态注入环境变量可能丢失 |
| Stop 暂停 | Pod 删除 | Sandbox 和 PVC 保留 | 进程结束，不保存内存 |
| 正常 resume/connect | 创建新 Pod UID | 原 PVC、原路径 | 重新初始化 agent/envd 后恢复访问 |
| Pod 意外丢失 | 原 Pod 不存在 | 原 PVC 保留 | Sandbox Failed，未自动补建，connect 404 |
| kubelet 压力驱逐 | 原 Pod 可保留为 Failed/Evicted | 原 PVC 保留 | Sandbox Failed，未自动恢复 |
| 删除 Sandbox CR | 所属 Pod 清理 | PVC 随 owner 生命周期清理 | 后端数据取决于 PV 回收策略 |

RootPV 支持“新 Pod 使用原盘”，但不负责决定“何时创建新 Pod”。所分析 Agents 状态机将 Pod Failed
映射为 Sandbox Failed，后续终态调谐直接返回；`restartPolicy: Always` 管理同 Pod 内容器重启，不能补齐该逻辑。
压力驱逐与经 API 提交的 eviction 不是同一路径，Pod 删除 webhook 和 PDB 不能替代压力后的恢复机制。
参见 [Kubernetes 节点压力驱逐](https://kubernetes.io/docs/concepts/scheduling-eviction/node-pressure-eviction/)。

如果要求驱逐后同一 SDK ID 自动恢复，需要在 Agents 增加恢复策略，保留 Sandbox/PVC 身份，核验原卷，
串行清理旧 Pod、创建新 Pod、重做 envd 初始化并更新路由，同时处理重试上限、暂停、删除和 TTL 竞争。
这不是当前版本的配置开关；本次未实现该功能。现有 RootPV 原盘复用能力不要求为此修改代码。

## 6. 2026-11-18 验证记录

以下为固定环境的结果摘要，不是所有版本组合的支持承诺。节点、地址、私有镜像仓库和部署标识已匿名化；
原始日志与集群专用清单未纳入本仓库。本文可独立阅读，配置示例不依赖这些环境专用文件。

### 环境与版本

| 项目 | 本轮范围 |
| --- | --- |
| 平台 | Linux AMD64；Kubernetes v1.37.1，测试集群部署 |
| runc / Kata 节点 | containerd 2.3.7；宿主 Linux 5.10 系列，发行版内核 |
| gVisor 节点 | containerd 2.1.1；runsc release-20261012.0，systrap |
| Kata | Kata Containers 4.2.0 Rust runtime、QEMU 11.0.1、guest kernel 6.18.35 |
| Agents | 既有 0.6 系列定制部署镜像；不是上游相同版本标签的直接认证 |
| 源码分析基线 | Agents `18096feb1d1089499b1227a3e60dc25476ae072d`；RootPV `c6023bdefc736a0da582c7d43bd0c0ceb7d7e101` |
| 构建边界 | 测试复用已安装候选二进制和部署镜像，未为实验修改/重新编译代码；部署产物与源码分析提交分别记录，不能视为完全等同 |
| CR 冒烟应用 | 固定镜像的 shell/文件检查主容器及普通 sidecar |
| SDK 应用 | code-interpreter 主镜像、agent-runtime 0.3.0 系列 helper、真实 envd/Jupyter |
| SDK | Python 3.9.9、官方 e2b 2.10.2；另安装 code-interpreter 2.4.1，但未调用 run_code |
| local 存储 | ext4、Filesystem/RWO、静态 local PV、Retain；声明容量 2Gi 不等于目录配额 |
| 动态存储 | 云盘 CSI、ext4、Filesystem/RWO、10Gi、WaitForFirstConsumer、Delete |

### 正常生命周期

| 用例 | 范围 | 结果 |
| --- | --- | --- |
| 独立 PVC 与 Sandbox owner | 直接 Sandbox、SandboxSet 生成实例 | 自动创建系统 PVC；owner 为 Sandbox，非 Pod |
| 主容器重启与两轮 Stop/resume | runc、Kata、gVisor 的 local 根盘 | 原 PVC/内容保留，恢复后为新 Pod；普通 sidecar 的写层不跨 Pod 保留 |
| 动态 CSI 根盘 | Kata、gVisor 的 CR 生命周期；三种运行时的真实 SDK 生命周期 | PVC Bound，重启/恢复后复用原盘 |
| 并行独立实例 | Kata、gVisor 的 local 场景 | PVC、内容及身份标记独立，一个实例重启不改变另一个实例的根盘 |
| SDK 创建、两轮暂停/恢复、删除 | runc、Kata、gVisor，真实 agent/envd | 文件与命令 RPC 通过；正常恢复重新下发 SDK envVars |
| 主容器 / emptyDir 对照 | 三种 SDK 运行时 | 根盘文件保留；Pod 重建后 envd 共享 emptyDir 标记消失 |
| 删除 Running 或 Paused Sandbox | SDK 正常生命周期 | 测试 Sandbox/Pod/PVC 消失，Delete 云盘 PV 与对应 VolumeAttachment 清理 |
| Retain local PVC 删除 | CR 生命周期 | PV Released，目录仍有数据；检查后另行清理测试目录 |

文件检查覆盖内容与 SHA-256、0640 权限、硬链接同 inode、相对符号链接、首次 Pod UID。
CR 冒烟还验证了删除基础镜像文件形成的 whiteout 在重建后保持。
这些校验路径没有额外挂载业务数据卷，验证的是 RootPV 根文件系统。

### 故障场景

| 故障 | 服务恢复结果 | RootPV 数据结果 |
| --- | --- | --- |
| 主容器 SIGKILL，三种运行时 | 约 6–8 秒后 SDK 文件/命令重新可用；同 Pod；SDK 注入环境变量丢失 | 原 PVC/文件保留 |
| runc/Kata 所在节点真实重启 | 约 136/145 秒后 SDK 访问自动恢复；Pod UID 保持，容器 ID/IP 可变；SDK 环境变量丢失 | 原 PVC/文件保留 |
| gVisor 所在节点真实重启 | 宿主 IPv6 启动问题阻断 API/CSI；人工恢复原地址后服务恢复，不能计为全自动通过 | 原 Pod/PVC 身份及文件保留 |
| 人工构造 Pod 意外丢失，三种运行时 | 使用 controller 身份仅删除测试 Pod；约 141 秒未补建，Sandbox Failed，connect 404 | 原 PVC Bound；只读救援确认文件完整 |
| gVisor kubelet 内存压力驱逐 | 真实 Failed/Evicted；解除压力后约 65 秒未恢复，connect 404 | 原 PVC Bound；只读救援确认文件完整 |

故障观察中的时长包含轮询开销，只是单轮观测，不是性能 SLA。
节点重启仍保留同一 Pod 对象；它与 Pod 对象被删除后能否补建是两个不同测试。
gVisor 节点的 IPv6 故障不能据此归因于 RootPV 数据损坏，也没有证明网络问题已得到永久修复。

人工 Pod 丢失测试先确认普通 DELETE 被保护 webhook 拒绝，再对限定的测试对象注入删除；
没有关闭全局 webhook。它不是推荐业务操作，也不是 kubelet 驱逐的替代证据。

### 压力测试的边界与影响

内存测试临时提高 kubelet 的软驱逐阈值，并让目标 Sandbox 实际分配约 1.5GiB 内存。
观察到 MemoryPressure=True、TerminationByKubelet、Pod Failed/Evicted 和 kubelet 驱逐成功日志；
约 39 秒完成驱逐。它验证真实驱逐路径，没有耗尽整机内存，没有覆盖默认硬阈值、内核 OOM、磁盘或 inode 压力。

在 runc/Kata 节点的前置尝试中，一个原本镜像拉取失败、缺少统计信息的 kruise-daemon 被优先选择并替换；
两个目标 Sandbox 未被驱逐，不能计为它们的压力驱逐验证通过。最终成功的目标驱逐发生在 gVisor 节点。
该轮两个节点原有 25 个 Pod 中，24 个的 UID、状态、容器 ID、重启次数与基线一致，唯一已记录变化是上述 daemon。
原镜像拉取问题仍在；低优先级测试 Pod 不能保证其他 Pod 完全不受影响。

测试后恢复了 kubelet 原配置和节点调度状态，清理测试 Sandbox、PVC/PV、临时 key 和命名空间，
检查没有测试 RootPV state、merged mount 或相关运行时进程残留。

另一个清理限制：Failed 的 gVisor 实例调用 SDK kill 返回成功，但约 20 秒观察内 CR 仍存在，
最终通过显式删除该 Sandbox CR 完成回收。这与正常 Running/Paused 实例删除通过的结果应分别记录。

### 未覆盖范围

- E2B 独立 Volume API、新版内存暂停参数、进程/VM 内存恢复、具体 LLM Agent 框架。
- 公网 TLS、泛域名路由和公网租户鉴权；SDK 验证使用可达的集群内 API 与 gateway。
- 跨节点自动迁移、失联节点 fencing、跨运行时或跨镜像链复用，以及其他 CSI 驱动组合。
- 磁盘/inode 驱逐、并发大量驱逐、持续压力自动恢复和性能测试。

## 7. 排障与使用边界

| 现象 | 检查或处理 |
| --- | --- |
| SandboxSet/Sandbox 提示 PVC 模板必须被挂载 | 在 Pod 模板中添加同名只读 anchor，不能等到 Pod webhook 才补 |
| Pod Running，但重建后根盘标记丢失 | 检查最终 Pod annotations、RuntimeClass、目标容器、PVC UID 和 rootfs-path；确认标记不在 emptyDir 或其他 mount 下 |
| `persistent_upper_in_use` | 检查旧容器是否停止、旧 lease/gofer/Kata task 和 merged mount 是否释放；不要删锁文件绕过互斥 |
| `base_image_mismatch` | 核对镜像 digest、snapshotter 与原盘的基础镜像链；不要清空 upper 或把换盘当作恢复 |
| Pod Failed/Evicted，connect 404 | 检查 Sandbox phase；当前部署没有驱逐恢复开关，保留原 PVC 后按运维恢复流程处理 |
| Pod 重启后文件在、SDK 环境变量不在 | 区分文件持久化与 envd 初始化；检查 Agents 的重建/初始化路径，不能靠根盘恢复进程环境 |
| SDK kill 成功但 PVC 仍在 | 先确认 Sandbox CR 是否实际删除、finalizer/ownerReference、PV 回收策略与 CSI 事件 |
| 节点 Ready 但 Sandbox 挂卷失败 | 检查节点到 API、CSI 注册、存储网络与运行时 adapter，Node Ready 不是业务恢复的充分条件 |

同一 PVC 的 RWO 不禁止同一节点上的多个 Pod 使用它，RootPV 的单写者约束仍须保留。
本地锁也不是跨节点 fencing；未知旧写入者状态下不能通过强制删除 Pod 或 VolumeAttachment 来宣称安全恢复。
单独读取 upper 只能核验写入该层的测试文件，不能将它视为包含镜像 lower 层的完整根文件系统备份。

部署到其他 Agents 版本时，应先重复正常生命周期检查，再验证所需故障语义。本文记录的 Failed 行为和 SDK 限制
适用于所测部署及对应分析基线，后续版本是否增加恢复能力需要按实际版本重新确认。
