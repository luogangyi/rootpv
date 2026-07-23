# RootPV

[中文文档](README_ZH.md) · [Contributing](CONTRIBUTING.md) · [License](LICENSE)

RootPV preserves a Kubernetes container's writable root filesystem on a PersistentVolumeClaim (PVC). It keeps the container image layers immutable and managed by the stock containerd `overlayfs` snapshotter, while storing only the writable OverlayFS delta on the PVC.

It is intended for development workspaces, AI/ML environments, interactive notebooks, and long-running initialization workloads whose in-container changes must survive Pod recreation.

> RootPV is not an image builder, backup system, or substitute for application-data persistence. Build and publish production images through your normal image pipeline.

## How it works

```text
Pod annotations + PVC
          |
          v
Admission controller -- selects a RootPV RuntimeClass and mounts the PVC anchor
          |
          v
stock containerd overlayfs snapshotter -- provides immutable image lower layers
          |
          v
RootPV OCI runtime wrapper -- mounts a single OverlayFS and delegates to runc/vendor runtime
          |
          v
PVC: upperdir + workdir + metadata
```

RootPV consists of:

- `persistent-rootfs-controller`: validates opted-in Pods and mutates them for RootPV.
- `persistent-rootfsd`: a node DaemonSet that resolves PVC mounts, manages metadata, and holds a single-writer lease.
- `rootfs-runtime-wrapper`: prepares the OverlayFS root filesystem before delegating to the real OCI runtime.
- Helm chart and Kubernetes manifests for installation.

RootPV uses public Kubernetes, containerd, OCI runtime-spec, and Linux VFS interfaces. It does not patch, replace, or fork containerd, and it does not manage containerd image or snapshot garbage collection.

## Current scope

- Linux nodes using stock containerd and its `overlayfs` snapshotter.
- PVC-backed, single-writer persistent root filesystems.
- Strict base-image identity checking to prevent accidental reuse with an incompatible image.
- Runtime adapters for `io.containerd.runc.v2`-based runtimes, including device-specific runtimes when the node runtime is already installed and discovered.

The following are out of scope for the initial release: non-containerd runtimes, non-`overlayfs` snapshotters, rootless or unprivileged node installation, shared read/write use of one root filesystem, automatic rebase/reset across image chains, and converting a writable layer into an OCI image.

## Quick start

### Prerequisites

- A Kubernetes cluster with Linux nodes and containerd.
- A storage class that provisions filesystem-mode PVCs. The PVC must be suitable for OverlayFS upper/work directories.
- Helm 3 and `kubectl`.
- Nodes that are explicitly selected for RootPV.

Build and publish a RootPV image, then label every node that should run the node agent:

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

Create a PVC and opt a Pod in with the target container, PVC volume, PVC-relative path, and strict image policy:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: rootpv-demo
  annotations:
    ecloud.com/persistent-rootfs: "true"
    ecloud.com/rootfs-container: app
    ecloud.com/rootfs-volume: rootfs-data
    ecloud.com/rootfs-path: containers/app
    ecloud.com/rootfs-base-image-policy: Strict
spec:
  containers:
    - name: app
      image: <your-image>
      command: ["/bin/sh", "-ec", "echo ready; sleep 3600"]
      volumeMounts:
        - name: rootfs-data
          mountPath: /data
  volumes:
    - name: rootfs-data
      persistentVolumeClaim:
        claimName: rootpv-data
```

See [the deployment guide](docs/user-guide/deployment.md) and [the Helm chart](charts/rootpv/README.md) for configuration and operational details.

## Development

This project uses Go 1.24 or later.

```bash
make test
make build
make helm-lint
make helm-template
```

Before submitting a change, format Go source with `gofmt` and run the checks above. See [CONTRIBUTING.md](CONTRIBUTING.md) for the contribution workflow.

## Security model

RootPV is a privileged, node-level component. The node agent accesses host mounts, containerd state, and the runtime configuration. Limit installation to trusted clusters and nodes. RootPV rejects opted-in workloads when it cannot establish a safe identity, mount, or lifecycle state; workloads that do not opt in stay on the normal runtime path.

## License

Copyright (c) 2026 OpenFuyao Contributors.

RootPV is licensed under the [Mulan Permissive Software License, Version 2](LICENSE) (Mulan PSL v2).
