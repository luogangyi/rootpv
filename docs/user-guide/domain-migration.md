# RootPV 域名迁移

RootPV 的 annotation、label、containerd 注解白名单、webhook 名称及 CRD/RBAC API group
统一使用 openfuyao.cn。当前版本不读取旧域名的 Pod opt-in 注解，也不使用旧域名的节点安装或就绪标签。
仅修改业务 Pod 注解、保留旧 Controller 或节点 shim，会使协议两端不一致。

## 已有安装升级

1. 备份原 Helm values、业务 YAML、节点 containerd 配置、RootPV registry 和已有自定义资源。
2. 删除受影响的 RootPV Pod，确认写层已卸载、租约已释放；保留 PVC 及其数据。
3. 同步更新 Controller、rootfsd、OCI wrapper、Kata shim 与 Chart，替换安装标签、Pod 注解、
   nodeSelector、webhook selector 和 containerd 的 pod_annotations/container_annotations 白名单。
   按新 Chart 为目标节点设置 openfuyao.cn/persistent-rootfs=true。
4. CRD 名称改为 persistentrootfses.openfuyao.cn，API group 改为 openfuyao.cn。
   Helm upgrade 不会自动更新或迁移 crds/ 下的资源；已有自定义资源应先导出，改写 apiVersion 后
   在新 CRD 下重建，并核对其 spec/status 和外部引用。确认旧资源已妥善迁移后再退役旧 CRD，
   不要直接删除仍承载资源的旧 CRD。
5. 检查新域名下的 inventory、catalog、Shadow RuntimeClass 和 adapter 就绪标签，
   再用更新后的 Pod YAML 重建业务；同一镜像与 PVC/rootfs-path 可继续复用原持久化数据。
6. 验证新 workload 后，按备份范围清理旧域名的 RootPV 标签和注解。
   不要批量删除集群中其他组件的域名键或修改厂商 runtime/CSI 的配置。

本次变更不会自动迁移运行中 workload，也不自动修改测试环境中的历史安装。
安装和清理命令见 [部署指南](deployment.md)，功能验证见 [Kata 使用指南](kata-qemu.md)。

## 仓库统一替换

```bash
python3 scripts/replace-domain.py
python3 scripts/replace-domain.py --apply
python3 scripts/replace-domain.py --check
```

默认仅预览，--apply 写入，--check 用于检查遗漏。脚本只处理 Git 跟踪的 UTF-8 文本，
排除自身、二进制、符号链接、忽略文件和未跟踪的本地配置。自定义迁移可传入
--from-domain 和 --to-domain。集群专用 values、测试 YAML 与节点配置副本需另行同步。
