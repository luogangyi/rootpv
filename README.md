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
RootPV runtime adapter -- OCI wrapper (runc-v2) or task shim proxy (Kata + QEMU)
          |
          v
PVC: upperdir + workdir + metadata
```

RootPV consists of:

- `persistent-rootfs-controller`: validates opted-in Pods and mutates them for RootPV.
- `persistent-rootfsd`: a node DaemonSet that resolves PVC mounts, manages metadata, and holds a single-writer lease.
- `rootfs-runtime-wrapper`: prepares the OverlayFS root filesystem before delegating to the real OCI runtime.
- `containerd-shim-rootpv-kata-v2`: proxies Kata + QEMU task creation and deletion for persistent root filesystems.
- Helm chart and Kubernetes manifests for installation.

RootPV uses public Kubernetes, containerd, OCI runtime-spec, and Linux VFS interfaces. It does not patch, replace, or fork containerd, and it does not manage containerd image or snapshot garbage collection.

## Current scope and limitations

- Linux nodes using stock containerd and its `overlayfs` snapshotter.
- PVC-backed, single-writer persistent root filesystems.
- Strict base-image identity checking to prevent accidental reuse with an incompatible image.
- Runtime adapters for `io.containerd.runc.v2`-based runtimes, including device-specific runtimes when the node runtime is already installed and discovered.
- Kata + QEMU task-shim adapters with explicit `runtime_path` and QEMU `ConfigPath`; see the [Kata guide](docs/user-guide/kata-qemu.md).

The following are out of scope: non-containerd runtimes, non-`overlayfs` snapshotters, rootless or unprivileged node installation, shared read/write use of one root filesystem, automatic rebase/reset across image chains, and converting a writable layer into an OCI image.

The full end-to-end baseline currently covers a Linux x86_64 Kubernetes v1.29-series cluster with
containerd 2.1.1 (vendor build), an `io.containerd.runc.v2` runtime, and dynamically provisioned ReadWriteOnce
block PVCs. On the same platform, the dynamic-PVC persistence-and-recreate path was also verified with
official containerd 1.6.39, 1.7.35, 2.0.12, and static 2.1.9 builds. These version checks do not yet include the
full recovery, upgrade, concurrency, or performance suite. The baseline also completed three rounds each of
rootfsd, containerd-service, and node restart; ten-instance concurrent isolation; same-path single-writer
exclusion; and a RootPV Helm upgrade/rollback.

Kata + QEMU has separate functional evidence on Kubernetes 1.34.3, containerd 2.1.1, Kata 4.2.0 Rust,
QEMU 11.0.1 and ext4 local RWO PVCs. That record covers persistence, sidecars, single-writer exclusion,
rootfsd restart and normal deletion. It does not establish dynamic CSI, node/VM failure recovery or performance
support for Kata; see the [verification record](tests/e2e/results/kata-qemu-2026-10-14.md).

Directly downgrading a running containerd 2.1 state to 1.6 is not supported: the 1.6.28 candidate starts but
cannot reuse the 2.1 runtime state. Cross-major rollback needs a controlled, drained-node migration that
rebuilds runtime state. Helm uninstall currently stops the RootPV node agent but does not automatically remove
the RootPV runtime handler written to the containerd configuration; node-level uninstall cleanup remains to be added.
Other container runtimes, snapshotters, architectures, storage backends, node reboot/recovery cases, and
device-specific runtimes have not yet completed the same end-to-end validation. Build support for an
architecture does not by itself mean that it is in the validated compatibility set.

## Compatibility validation matrix

This table records evidence, not theoretical compatibility. A combination is added to **Validated** only after
its verification record is published. Entries in **Not yet validated** are not support commitments.

| Dimension | Combination or scope | Validated | Not yet validated |
| --- | --- | --- | --- |
| Baseline platform | linux/amd64; Kubernetes v1.29 series; containerd 2.1.1 (vendor build); `overlayfs`; stock `io.containerd.runc.v2`; RWO block PVC with ext4 | Functional and performance E2E; three rounds each of rootfsd, containerd-service, and node restart; ten-instance concurrent isolation; same-path single-writer exclusion; RootPV Helm upgrade/rollback | Controlled cross-major containerd migration; automatic runtime-handler cleanup after uninstall |
| Kubernetes and containerd | Kubernetes v1.27, v1.28, v1.30 and later supported series; containerd 1.6, 1.7, 2.0, and additional 2.1 combinations | Kubernetes v1.29 + official containerd 1.6.39, 1.7.35, 2.0.12, and static 2.1.9: dynamic-PVC persistence and Pod recreation | All other listed pairings; recovery, upgrade, and performance on official builds |
| CPU architecture and kernel | linux/arm64 and additional supported Linux kernel/distribution combinations | — | Baseline suite and OverlayFS recovery coverage |
| Storage | RWO block PVC with XFS | — | OverlayFS mount, persistence, and cleanup coverage |
| runc-v2 adapters | Custom `io.containerd.runc.v2` runtimes using a non-wrapper `BinaryName` delegate | — | Adapter discovery plus lifecycle, signal, exit-code, and recovery coverage |
| GPU runtimes | NVIDIA (including MIG/vGPU where available), AMD ROCm, Intel, and MThreads | — | Device workload, persistence, cleanup, and each device-partitioning mode |
| NPU runtimes | Ascend and other runc-v2-compatible accelerators; whole-device and virtual/partitioned modes tracked separately | — | Vendor workload, persistence, cleanup, and isolation coverage |
| Kata + QEMU | Linux AMD64; Kubernetes 1.34.3; containerd 2.1.1; Kata 4.2.0 Rust; QEMU 11.0.1; ext4 local RWO PVC | [Persistence, sidecar isolation, single-writer exclusion, rootfsd restart, normal Kata and cleanup](tests/e2e/results/kata-qemu-2026-10-14.md) | Dynamic CSI PVC, node/VM failure recovery, performance and devices |
| Other independent shims | Kata CLH, runsc, and shims outside the QEMU adapter | gVisor: RootPV opt-in is rejected; a normal gVisor Pod runs without RootPV mutation | Kata CLH, gVisor RootPV workloads, and other independent shims; Kata QEMU support is described in the [Kata guide](docs/user-guide/kata-qemu.md) |

See the [compatibility validation plan and detailed matrix](docs/design/04-compatibility-validation-plan.md) for
test cases, acceptance criteria, and planned environment coverage.

## Installation and deployment

RootPV keys and API groups use openfuyao.cn. For an existing installation, follow the
[domain migration guide](docs/user-guide/domain-migration.md) before updating components or workloads.

### Prerequisites

- A Kubernetes cluster with Linux nodes and containerd.
- A filesystem-mode ReadWriteOnce StorageClass suitable for OverlayFS upper/work directories.
- Helm 3 and `kubectl`.
- Cluster-administrator access. The node agent is privileged and installs the runtime wrapper and RootPV runtime handler on selected nodes.
- Explicitly selected RootPV nodes. Start with a small canary node set.

Both Helm installation paths update the containerd runtime configuration only on labeled nodes and reload or
restart containerd when required. Review node paths, the containerd socket, and restart settings in
`charts/rootpv/values.yaml`; plan the rollout according to your cluster's maintenance policy.

Kata requires an image containing the new proxy shim and the current Chart installer. Build from this branch
using Section 2, or verify that the selected CI artifact includes this implementation; a latest tag alone is insufficient.

The storage-policy values are rendered into a ConfigMap; the current Controller does not load that file to
enforce a StorageClass allowlist. Verify the actual PVC filesystem and OverlayFS capabilities.

### 1. Quick start: use CI artifacts

Use this path to evaluate RootPV directly from the image and OCI Chart published by openFuyao CI. Replace
`<storage-class>` with the StorageClass selected above.

```bash
export ROOTPV_IMAGE_REPOSITORY=openfuyao-0qfqnk.swr-pro.myhuaweicloud.com/openfuyao/rootpv
export ROOTPV_IMAGE_TAG=latest
export ROOTPV_CHART=oci://openfuyao-0qfqnk.swr-pro.myhuaweicloud.com/charts/rootpv
export ROOTPV_CHART_VERSION=0.0.0-latest

kubectl label node <node-name> openfuyao.cn/persistent-rootfs=true --overwrite

helm upgrade --install rootpv "${ROOTPV_CHART}" \
  --version "${ROOTPV_CHART_VERSION}" \
  --namespace rootpv-system \
  --create-namespace \
  --set "image.repository=${ROOTPV_IMAGE_REPOSITORY}" \
  --set "image.tag=${ROOTPV_IMAGE_TAG}" \
  --set "image.pullPolicy=Always" \
  --set-string 'config.storagePolicy.allowStorageClasses[0]=<storage-class>' \
  --wait --timeout 10m
```

`latest` and `0.0.0-latest` are CI artifacts and can change over time. Pin an image digest and a released Chart
version for repeatable or production deployments. Continue with [Verify the deployment](#4-verify-the-deployment)
after installation.

### 2. Build and deploy custom artifacts

Build an image for the target architecture and push it to an image registry reachable by the selected nodes:

```bash
make image IMAGE=<image-repository>:<image-tag>
make push IMAGE=<image-repository>:<image-tag>
```

For multi-architecture publishing, use `make push-multiarch IMAGE=<image-repository>:<image-tag>` after configuring registry authentication.

After the image is available, choose one deployment mechanism:

- Use the Helm command below for normal installations. Helm manages values, release history, upgrades, and
  uninstallation.
- Use the native-manifest path in Section 3 only when Helm is unavailable or when GitOps/IaC tooling must own
  the rendered Kubernetes resources. It replaces the Helm command below; do not use both paths for the same
  RootPV installation.

For the recommended Helm path, deploy the locally checked-out Chart with the image you built. Label only the
nodes that should run the RootPV node agent:

```bash
kubectl label node <node-name> openfuyao.cn/persistent-rootfs=true --overwrite

helm upgrade --install rootpv charts/rootpv \
  --namespace rootpv-system \
  --create-namespace \
  --set image.repository=<image-repository> \
  --set image.tag=<image-tag> \
  --set-string 'config.storagePolicy.allowStorageClasses[0]=<storage-class>' \
  --wait --timeout 10m
```

### 3. Alternative: apply rendered Kubernetes manifests

For GitOps or kubectl-managed installation, render the Chart from this checkout using the image built in
Section 2. Helm is needed at rendering time; the target deployment does not need a Helm release.
Keep one owner for these resources.

```bash
helm template rootpv charts/rootpv \
  --namespace rootpv-system --include-crds \
  --set image.repository=<image-repository> \
  --set image.tag=<image-tag> \
  --set-string 'config.storagePolicy.allowStorageClasses[0]=<storage-class>' \
  > rootpv-rendered.yaml

kubectl create namespace rootpv-system --dry-run=client -o yaml | kubectl apply -f -
kubectl label node <node-name> openfuyao.cn/persistent-rootfs=true --overwrite
kubectl apply -f rootpv-rendered.yaml
```

The legacy static installer in deploy/manifests uses fixed runc/NVIDIA handlers and does not install the Kata
proxy. Use the rendered current Chart for automatic runtime discovery and Kata support. Offline re-rendering
generates fresh TLS certificates; when applying a new Secret and caBundle, restart the Controller to load the
matching certificate. See the [deployment guide](docs/user-guide/deployment.md).

### 4. Verify the deployment

Run these checks after any deployment path:

```bash
kubectl -n rootpv-system rollout status deploy/persistent-rootfs-controller --timeout=180s
kubectl -n rootpv-system rollout status ds/persistent-rootfsd --timeout=180s
kubectl get runtimeclass -l openfuyao.cn/managed-by=rootpv
kubectl get nodes -l openfuyao.cn/persistent-rootfs-ready=true
```

### 5. Verify a persistent-rootfs workload

After creating a PVC with the selected StorageClass, opt a Pod in with the target container, PVC volume,
PVC-relative path, and strict image policy:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: rootpv-demo
  annotations:
    openfuyao.cn/persistent-rootfs: "true"
    openfuyao.cn/rootfs-container: app
    openfuyao.cn/rootfs-volume: rootfs-data
    openfuyao.cn/rootfs-path: containers/app
    openfuyao.cn/rootfs-base-image-policy: Strict
spec:
  containers:
    - name: app
      image: <your-image>
      command: ["/bin/sh", "-ec", "echo ready; sleep 3600"]
  volumes:
    - name: rootfs-data
      persistentVolumeClaim:
        claimName: rootpv-data
```

This is the YAML to submit when the RootPV mutating webhook is installed and enabled. Keep the
`volumes` declaration; no user-defined `volumeMounts` is needed for rootfs persistence. Before the
Pod is stored, the webhook automatically adds the following fields to the `app` container
(an excerpt of the resulting Pod, not an additional manifest to apply):

```yaml
env:
  - name: ROOTPV_POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
volumeMounts:
  - name: rootfs-data
    mountPath: /var/run/rootpv/rootfs-anchor/rootfs-data
    readOnly: true
    subPathExpr: .rootpv-anchor/$(ROOTPV_POD_NAME)/app
```

The final Pod needs this anchor mount: it references `rootfs-data`, causing kubelet to mount the
PVC on the node and providing a mount that RootPV can use to identify the PVC. `subPathExpr` uses
the Pod name and container name to expose only `<PVC>/.rootpv-anchor/rootpv-demo/app` at the
container's anchor path. The mount is read-only and does not expose the entire PVC or its
`upper`, `work`, and `meta` directories. You may omit these fields from the submitted YAML because
the webhook injects them; do not remove the injected anchor from the final Pod. Declaring only
`volumes` without a container mount does not replace the anchor.

`openfuyao.cn/rootfs-path: containers/app` specifies a directory **inside the PVC**, where RootPV
stores `upper`, `work`, and `meta`. The OCI wrapper or Kata proxy combines the image layers with the
persistent `upper` to provide the container's root filesystem `/`; the annotation does not set a
container `mountPath`. Mounting the entire `rootfs-data` volume at `/data` would also expose
`/data/containers/app/{upper,work,meta}` to the application. It is unnecessary for rootfs persistence
and allows the application to modify RootPV's backing data directly, so do not add that mount or
mount those internal directories. If the application needs a separate data-volume mount, declare
it separately from the rootfs backing PVC.

Inspect the created Pod to confirm that the webhook added `ROOTPV_POD_NAME` and the read-only anchor:

```bash
kubectl get pod rootpv-demo -o yaml
```

See the [documentation index](docs/README.md), [deployment guide](docs/user-guide/deployment.md), and [Helm chart](charts/rootpv/README.md) for configuration and operational details.

### 6. Kata + QEMU workload

Use the original RuntimeClass in the submitted Pod:

```yaml
spec:
  runtimeClassName: kata-qemu
```

The QEMU adapter maps it to persistent-rootfs-kata-qemu and preserves the original scheduling and overhead.
The backing PVC uses the same injected read-only anchor; untargeted sidecars keep ordinary root filesystems.
See the [Kata guide](docs/user-guide/kata-qemu.md) for source handler prerequisites, node paths, and the
[complete Namespace/PV/PVC/Pod test YAML](tests/e2e/manifests/kata-qemu.yaml). Source handler names other
than kata-qemu have catalog-derived adapter IDs; inspect the generated catalog instead of hardcoding them.

## Validation results

The 2026-09-03 runc baseline covered the following functional cases; see the
[functional record](tests/e2e/results/functional-2026-09-03.md). The separate
[2026-10-14 Kata record](tests/e2e/results/kata-qemu-2026-10-14.md) covers the QEMU adapter.

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
The figures below are the [2026-09-03 runc measurements](tests/e2e/results/performance-2026-09-03.md);
Kata performance has not been measured.

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
