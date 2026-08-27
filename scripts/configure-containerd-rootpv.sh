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

config_path="${CONTAINERD_CONFIG:-/etc/containerd/config.toml}"
runc_wrapper="${ROOTPV_RUNC_WRAPPER:-/usr/local/bin/rootfs-runtime-wrapper-runc}"
nvidia_wrapper="${ROOTPV_NVIDIA_WRAPPER:-/usr/local/bin/rootfs-runtime-wrapper-nvidia}"
unified_wrapper="${ROOTPV_UNIFIED_WRAPPER:-}"
validate_mode="${ROOTPV_VALIDATE_MODE:-containerd}"
restart_command="${ROOTPV_RESTART_COMMAND:-systemctl restart containerd}"
backup="${config_path}.rootpv.$(date +%Y%m%d%H%M%S).bak"

if [[ "${unified_wrapper}" != "" ]]; then
  runc_wrapper="${unified_wrapper}"
  nvidia_wrapper="${unified_wrapper}"
fi

cp "$config_path" "$backup"

python3 - "$config_path" "$runc_wrapper" "$nvidia_wrapper" <<'PY'
import os
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
runc_wrapper = sys.argv[2]
nvidia_wrapper = sys.argv[3]
text = path.read_text()

def detect_systemd_cgroup(src):
    override = os.environ.get("ROOTPV_SYSTEMD_CGROUP", "").strip().lower()
    if override in ("true", "false"):
        return override
    if override:
        raise SystemExit("ROOTPV_SYSTEMD_CGROUP must be true or false")

    for runtime in ("runc", "nvidia"):
        value = find_runtime_systemd_cgroup(src, runtime)
        if value != "":
            return value
    return "false"

def find_runtime_systemd_cgroup(src, runtime):
    in_options = False
    needle = f".containerd.runtimes.{runtime}.options]"
    for line in src.splitlines():
        stripped = line.strip()
        if stripped.startswith("["):
            normalized = stripped.replace('"', "'")
            in_options = normalized.endswith(needle) or needle in normalized
            continue
        if not in_options:
            continue
        match = re.match(r"SystemdCgroup\s*=\s*(true|false)\s*$", stripped, re.IGNORECASE)
        if match:
            return match.group(1).lower()
    return ""

def detect_containerd_plugin(src):
    match = re.search(r"(?m)^\s*version\s*=\s*(\d+)\s*$", src)
    if match:
        version = int(match.group(1))
        if version >= 3:
            return "plugins.'io.containerd.cri.v1.runtime'"
        return 'plugins."io.containerd.grpc.v1.cri"'
    if "io.containerd.cri.v1.runtime" in src:
        return "plugins.'io.containerd.cri.v1.runtime'"
    if "io.containerd.grpc.v1.cri" in src:
        return 'plugins."io.containerd.grpc.v1.cri"'
    return "plugins.'io.containerd.cri.v1.runtime'"

def remove_tables(src, prefixes):
    out = []
    skip = False
    for line in src.splitlines():
        stripped = line.strip()
        if stripped.startswith("["):
            if any(stripped.startswith(prefix) for prefix in prefixes):
                skip = True
                continue
            if skip:
                skip = False
        if not skip:
            out.append(line)
    return "\n".join(out).rstrip() + "\n"

text = remove_tables(text, [
    '[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.persistent-rootfs-runc',
    '[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.persistent-rootfs-nvidia',
    "[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.persistent-rootfs-runc",
    "[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.persistent-rootfs-nvidia",
    "[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.persistent-rootfs-runc",
    "[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.persistent-rootfs-nvidia",
    '[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.persistent-rootfs-runc',
    '[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.persistent-rootfs-nvidia',
])
systemd_cgroup = detect_systemd_cgroup(text)

plugin = detect_containerd_plugin(text)

blocks = [f'''

[{plugin}.containerd.runtimes.persistent-rootfs-nvidia]
  runtime_type = "io.containerd.runc.v2"
  snapshotter = "overlayfs"
  pod_annotations = ["ecloud.com/*"]
  container_annotations = ["ecloud.com/*"]

  [{plugin}.containerd.runtimes.persistent-rootfs-nvidia.options]
    BinaryName = "{nvidia_wrapper}"
    SystemdCgroup = {systemd_cgroup}

[{plugin}.containerd.runtimes.persistent-rootfs-runc]
  runtime_type = "io.containerd.runc.v2"
  snapshotter = "overlayfs"
  pod_annotations = ["ecloud.com/*"]
  container_annotations = ["ecloud.com/*"]

  [{plugin}.containerd.runtimes.persistent-rootfs-runc.options]
    BinaryName = "{runc_wrapper}"
    SystemdCgroup = {systemd_cgroup}
''']

path.write_text(text.rstrip() + "".join(blocks) + "\n")
PY

case "${validate_mode}" in
  containerd)
    containerd config dump >/dev/null
    ;;
  toml)
    python3 - "$config_path" <<'PY'
import pathlib
import sys

try:
    import tomllib
except ModuleNotFoundError:
    raise SystemExit("ROOTPV_VALIDATE_MODE=toml requires Python 3.11+")

tomllib.loads(pathlib.Path(sys.argv[1]).read_text())
PY
    ;;
  none|"")
    ;;
  *)
    sh -ec "${validate_mode}"
    ;;
esac

if [[ "${restart_command}" != "" && "${restart_command}" != "none" ]]; then
  sh -ec "${restart_command}"
fi
echo "containerd rootpv runtime-wrapper config applied; backup: $backup"
