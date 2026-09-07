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

## Current scope and limitations

- Linux nodes using stock containerd and its `overlayfs` snapshotter.
- PVC-backed, single-writer persistent root filesystems.
- Strict base-image identity checking to prevent accidental reuse with an incompatible image.
- Runtime adapters for `io.containerd.runc.v2`-based runtimes, including device-specific runtimes when the node runtime is already installed and discovered.

The following are out of scope for the initial release: non-containerd runtimes, non-`overlayfs` snapshotters, rootless or unprivileged node installation, shared read/write use of one root filesystem, automatic rebase/reset across image chains, and converting a writable layer into an OCI image.

The current end-to-end validation is limited to a Linux x86_64 Kubernetes v1.29-series cluster with stock
containerd 2.1, an `io.containerd.runc.v2` runtime, and dynamically provisioned ReadWriteOnce block PVCs.
Other container runtimes, snapshotters, architectures, storage backends, node reboot/recovery cases, and
device-specific runtimes have not yet completed the same end-to-end validation. Build support for an
architecture does not by itself mean that it is in the validated compatibility set.

## Installation and deployment

### Prerequisites

- A Kubernetes cluster with Linux nodes and containerd.
- A storage class that provisions filesystem-mode PVCs. The PVC must be suitable for OverlayFS upper/work directories.
- Helm 3 and `kubectl`.
- Cluster-administrator access. The node agent is privileged and installs the runtime wrapper and RootPV runtime handler on selected nodes.
- Nodes that are explicitly selected for RootPV. Start with a small canary node set.

### 1. Build and publish the image

Build an image for the target architecture and push it to an image registry reachable by the selected nodes:

```bash
make image IMAGE=<image-repository>:<image-tag>
make push IMAGE=<image-repository>:<image-tag>
```

For multi-architecture publishing, use `make push-multiarch IMAGE=<image-repository>:<image-tag>` after configuring registry authentication.

### 2. Deploy with Helm

Label only the nodes that should run the RootPV node agent, then install the chart:

```bash
kubectl label node <node-name> ecloud.com/persistent-rootfs=true --overwrite

helm upgrade --install rootpv charts/rootpv \
  --namespace rootpv-system \
  --create-namespace \
  --set image.repository=<image-repository> \
  --set image.tag=<image-tag>

kubectl -n rootpv-system rollout status deploy/persistent-rootfs-controller --timeout=180s
kubectl -n rootpv-system rollout status ds/persistent-rootfsd --timeout=180s
kubectl get runtimeclass -l ecloud.com/managed-by=rootpv
```

The installer validates and updates the containerd runtime configuration on labeled nodes, then reloads or
restarts containerd when required. Review `charts/rootpv/values.yaml` for node paths, containerd socket and
restart settings before installation. Plan a maintenance window or drain canary nodes according to your
cluster's operational policy.

Helm is the recommended deployment path. Native manifests are also available for environments that manage
their own image substitutions and configuration:

```bash
# Replace rootpv:latest in deploy/manifests with an accessible image first.
kubectl apply -f deploy/crds
kubectl apply -f deploy/manifests
```

### 3. Enable a workload

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

See the [documentation index](docs/README.md), [deployment guide](docs/user-guide/deployment.md), and [Helm chart](charts/rootpv/README.md) for configuration and operational details.

## Validation results

The latest cluster verification covered the following functional cases:

| Test case | Result |
| --- | --- |
| Controller, node agent, and RootPV-ready node | Passed |
| Pod without RootPV opt-in remains on the normal runtime path | Passed |
| Invalid target-container configuration is rejected by admission | Passed |
| Dynamic PVC workload receives the expected RuntimeClass and runtime adapter | Passed |
| Writable-root marker persists across Pod deletion and recreation | Passed |
| Test namespace and PVC cleanup | Passed |

Performance was measured with one warm-up iteration, three measured iterations per path, and a 64 MiB
synchronous write per iteration. The same image, node, write size, and Pod lifecycle were used for both paths.

| Metric | RootPV disabled | RootPV enabled | Performance impact |
| --- | ---: | ---: | ---: |
| Pod creation to Running (average / P95) | 5500 / 5700 ms | 14560 / 15553 ms | average +164.73%; P95 +172.86% |
| Sequential writable-root write (average / P95) | 1910 / 1972 ms | 1995 / 2066 ms | average +4.45%; P95 +4.77% |
| Pod deletion and runtime cleanup (average / P95) | 32336 / 32440 ms | 32247 / 32519 ms | average -0.28%; P95 +0.24% |
| Sequential writable-root throughput | 33.50 MiB/s | 32.08 MiB/s | -4.24% |

The disabled path writes the container writable layer; the enabled path writes the persistent root filesystem.
The difference therefore represents the end-to-end cost of persistence, not only the runtime wrapper. These
numbers are a small-sample reference, not a performance SLO; compare them only with runs on the same node,
storage backend, image cache state, and cluster load.

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
