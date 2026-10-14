# RootPV 部署指南

## 架构

RootPV 使用 runtime wrapper 和自动发现的 runtime adapter，在原生 containerd
`overlayfs` snapshotter 上把容器可写层持久化到 PVC。RootPV 不提供 containerd
二进制、不会安装 containerd patch，也不注册 proxy snapshotter。

节点本地文件均使用 RootPV 命名：运行状态在 `/run/rootpv/rootfs`，wrapper 和 adapter
配置在 `/etc/rootpv`，PVC anchor 为 `.rootpv-anchor/<pod>/<container>`，Webhook 注入
`ROOTPV_POD_NAME`。

## 安装

```bash
kubectl label node <node-name> ecloud.com/persistent-rootfs=true --overwrite

helm upgrade --install rootpv charts/rootpv \
  --namespace rootpv-system \
  --create-namespace \
  --set image.repository=<image-repository> \
  --set image.tag=<image-tag>

kubectl -n rootpv-system rollout status deploy/persistent-rootfs-controller --timeout=180s
kubectl -n rootpv-system rollout status ds/persistent-rootfsd --timeout=180s
```

## 启用持久 rootfs

Pod 需明确声明目标容器、PVC volume、PVC 内路径和严格基础镜像策略：

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: rootpv-demo
  annotations:
    ecloud.com/persistent-rootfs: "true"
    ecloud.com/rootfs-container: app
    ecloud.com/rootfs-volume: data
    ecloud.com/rootfs-path: containers/app
    ecloud.com/rootfs-base-image-policy: Strict
spec:
  containers:
  - name: app
    image: <your-image>
    command: ["/bin/sh", "-ec", "echo ready; sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: rootpv-data
```

RootPV admission 注入有效 RuntimeClass、adapter 证据、anchor mount 与 `ROOTPV_POD_NAME`。
验证结果应显示 native `overlayfs`、对应 adapter 和正确 delegate；GPU workload 使用原始
`runtimeClassName: nvidia` 并请求 `nvidia.com/gpu`。

## 卸载

先删除 RootPV workload 并确认 `/run/rootpv/rootfs` 已回收，再执行：

```bash
helm uninstall rootpv -n rootpv-system
kubectl delete namespace rootpv-system --ignore-not-found
```


Kata + QEMU 的安装条件和 RuntimeClass 用法见 [Kata 使用指南](kata-qemu.md)。
