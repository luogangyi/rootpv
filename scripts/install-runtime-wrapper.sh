#!/usr/bin/env bash
# Copyright (c) 2026 OpenFuyao Contributors.
# RootPV is licensed under Mulan PSL v2.
# You can use this software according to the terms and conditions of the Mulan PSL v2.
# You may obtain a copy of Mulan PSL v2 at:
# http://license.coscl.org.cn/MulanPSL2
# THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND,
# EITHER EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT,
# MERCHANTABILITY OR FIT FOR A PARTICULAR PURPOSE.
# See the Mulan PSL v2 for more details.

set -euo pipefail

binary="${1:-dist/rootfs-runtime-wrapper-linux-amd64}"
target_dir="${TARGET_DIR:-/usr/local/bin}"
config_dir="${CONFIG_DIR:-/etc/rootpv}"
rootfsd_socket="${ROOTPV_ROOTFSD_SOCKET:-/run/persistent-rootfsd/rootfsd.sock}"
runtime_root="${ROOTPV_RUNTIME_ROOT:-/run/rootpv/rootfs}"
overlay_mode="${ROOTPV_OVERLAY_MODE:-flat-lowerdirs}"
containerd_address="${ROOTPV_CONTAINERD_ADDRESS:-/run/containerd/containerd.sock}"
containerd_namespace="${ROOTPV_CONTAINERD_NAMESPACE:-k8s.io}"
snapshot_root="${ROOTPV_OVERLAY_SNAPSHOT_ROOT:-/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots}"
runc_runtime="${ROOTPV_RUNC_RUNTIME:-}"
nvidia_runtime="${ROOTPV_NVIDIA_RUNTIME:-/usr/bin/nvidia-container-runtime}"

if [[ ! -f "${binary}" ]]; then
  echo "usage: $0 /path/to/rootfs-runtime-wrapper" >&2
  exit 2
fi

install -d -m 0755 "${target_dir}" "${config_dir}"
install -m 0755 "${binary}" "${target_dir}/rootfs-runtime-wrapper"
ln -sfn rootfs-runtime-wrapper "${target_dir}/rootfs-runtime-wrapper-runc"
ln -sfn rootfs-runtime-wrapper "${target_dir}/rootfs-runtime-wrapper-nvidia"

if [[ -z "${runc_runtime}" ]]; then
  if [[ -x /usr/local/sbin/runc ]]; then
    runc_runtime=/usr/local/sbin/runc
  else
    runc_runtime=/usr/bin/runc
  fi
fi

tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT
cat >"${tmp}" <<EOF
defaultDelegateRuntime: ${runc_runtime}
rootfsdSocket: ${rootfsd_socket}
runtimeRoot: ${runtime_root}
overlayMode: ${overlay_mode}
containerdAddress: ${containerd_address}
containerdNamespace: ${containerd_namespace}
allowedOverlaySnapshotRoots:
  - ${snapshot_root}
delegateRuntimeByWrapper:
  rootfs-runtime-wrapper: ${runc_runtime}
  rootfs-runtime-wrapper-runc: ${runc_runtime}
  rootfs-runtime-wrapper-nvidia: ${nvidia_runtime}
EOF
install -m 0644 "${tmp}" "${config_dir}/rootfs-runtime-wrapper.yaml"

echo "rootfs runtime wrapper installed to ${target_dir}; config: ${config_dir}/rootfs-runtime-wrapper.yaml"
