# RootPV Helm Chart

该 Chart 只部署 RootPV runtime-wrapper/runtime-adapter 架构：保留原生 containerd
`overlayfs`，在已标记节点上生成 RootPV RuntimeClass handler。镜像不包含、Chart 也不安装或修改 containerd 二进制。

## 安装

```bash
kubectl label node <node-name> ecloud.com/persistent-rootfs=true --overwrite

helm upgrade --install rootpv charts/rootpv \
  --namespace rootpv-system \
  --create-namespace \
  --set image.repository=<image-repository> \
  --set image.tag=<image-tag>
```

节点 agent 在 `/run/rootpv/rootfs` 保存运行状态，在 `/etc/rootpv` 保存 runtime-adapter registry 和 wrapper 配置。

## 验证

```bash
kubectl -n rootpv-system rollout status deploy/persistent-rootfs-controller --timeout=180s
kubectl -n rootpv-system rollout status ds/persistent-rootfsd --timeout=180s
kubectl get runtimeclass -l ecloud.com/managed-by=rootpv
helm -n rootpv-system status rootpv
```

静态检查：

```bash
helm lint charts/rootpv
helm template rootpv charts/rootpv --namespace rootpv-system --include-crds
```
