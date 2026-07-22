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

## Commit and review expectations

- Use clear, imperative commit subjects.
- Keep generated artifacts out of commits unless they are intentionally versioned release assets.
- Preserve fail-closed behavior for opted-in workloads; ordinary workloads must remain outside the RootPV data path.
- Document user-visible configuration, behavior, and compatibility changes.

By contributing, you agree that your contributions are licensed under the Mulan Permissive Software License, Version 2, as described in [LICENSE](LICENSE).
