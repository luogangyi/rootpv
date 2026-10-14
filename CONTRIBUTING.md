# Contributing to RootPV

Thank you for contributing to RootPV. Please keep each change focused, explain the problem it solves, and include tests or validation evidence appropriate to the change.

## Development setup

- Go 1.24 or later
- Docker or another OCI image builder
- A Kubernetes cluster with Linux/containerd nodes for end-to-end validation
- Helm 3 and `kubectl`

## Before opening a pull request

```bash
gofmt -w <changed-go-files>
make test
make build
make helm-lint
make helm-template
```

For changes to node runtime integration, include the tested Kubernetes version, containerd version, runtime handler, storage class, and validation results in the pull request description. Do not commit credentials, kubeconfigs, cluster addresses, or other sensitive operational data.

## Design and compatibility changes

RootPV is a node-level runtime integration. Changes that alter runtime adapters, containerd configuration, OverlayFS behavior, admission mutation, storage semantics, or compatibility guarantees should begin with a design proposal in `docs/design/` and include focused unit tests plus end-to-end validation where feasible.

## Runtime integration documentation

When changing a runtime adapter, update the English and Chinese READMEs, deployment guide, Chart README,
current design, compatibility matrix, and E2E instructions together. Separate historical design decisions and
dated test evidence from the current implementation. A successful functional case does not establish device,
performance, dynamic CSI, or crash-recovery support.

For Kata + QEMU, document the original RuntimeClass/handler, runtime type, explicit runtime_path/ConfigPath,
Kata implementation, hypervisor, guest kernel, storage type, and tested lifecycle cases. Keep the complete
reusable test manifest and environment example in tests/e2e; keep cluster-specific copies and credentials
outside versioned sources.

Check local Markdown links, fence/table formatting, YAML and shell syntax, and Helm rendering for changes to
documentation or examples. New full Go runs are needed when behavior changes, not for prose-only edits.

## Commit and review expectations

- Use clear, imperative commit subjects.
- Keep generated artifacts out of commits unless they are intentionally versioned release assets.
- Preserve fail-closed behavior for opted-in workloads; ordinary workloads must remain outside the RootPV data path.
- Document user-visible configuration, behavior, and compatibility changes.

By contributing, you agree that your contributions are licensed under the Mulan Permissive Software License, Version 2, as described in [LICENSE](LICENSE).
