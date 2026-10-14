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

readonly E2E_LIBRARY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly E2E_ROOT_DIR="$(cd "${E2E_LIBRARY_DIR}/../../.." && pwd)"

load_e2e_config() {
  local config_file="$1"

  [[ -f "${config_file}" ]] || {
    echo "test configuration not found: ${config_file}" >&2
    return 2
  }
  # 配置文件仅保存连接信息和测试参数；不保存仓库密码或 kubeconfig 凭据。
  # shellcheck disable=SC1090
  source "${config_file}"
  : "${SSH_HOST:?SSH_HOST is required}"
  : "${SSH_PORT:?SSH_PORT is required}"
  : "${TEST_ENVIRONMENT_SUMMARY:?TEST_ENVIRONMENT_SUMMARY is required}"
  : "${STORAGE_SUMMARY:?STORAGE_SUMMARY is required}"
  : "${TEST_IMAGE_SUMMARY:?TEST_IMAGE_SUMMARY is required}"
  : "${ROOTPV_NAMESPACE:?ROOTPV_NAMESPACE is required}"
  : "${TEST_NAMESPACE:?TEST_NAMESPACE is required}"
  : "${TEST_NODE:?TEST_NODE is required}"
  : "${STORAGE_CLASS:?STORAGE_CLASS is required}"
  : "${PVC_SIZE:?PVC_SIZE is required}"
  : "${TEST_IMAGE:?TEST_IMAGE is required}"
  : "${EXPECTED_RUNTIME_CLASS:?EXPECTED_RUNTIME_CLASS is required}"
  : "${EXPECTED_RUNTIME_ADAPTER:?EXPECTED_RUNTIME_ADAPTER is required}"
  : "${INVALID_ROOTFS_CONTAINER:?INVALID_ROOTFS_CONTAINER is required}"
  : "${POD_START_TIMEOUT:?POD_START_TIMEOUT is required}"
  : "${POD_DELETE_TIMEOUT:?POD_DELETE_TIMEOUT is required}"
  : "${POD_SLEEP_SECONDS:?POD_SLEEP_SECONDS is required}"
  : "${CLEANUP:?CLEANUP is required}"
  : "${PERF_WARMUP_ITERATIONS:?PERF_WARMUP_ITERATIONS is required}"
  : "${PERF_BASELINE_ITERATIONS:?PERF_BASELINE_ITERATIONS is required}"
  SSH_USER="${SSH_USER:-root}"
  TEST_IMAGE_PULL_POLICY="${TEST_IMAGE_PULL_POLICY:-IfNotPresent}"
  NON_ROOTPV_POD_NAME="${NON_ROOTPV_POD_NAME:-rootpv-functional-standard}"
  INVALID_POD_NAME="${INVALID_POD_NAME:-rootpv-functional-invalid}"
  RESULTS_DIR="${RESULTS_DIR:-tests/e2e/results}"
}

remote_kubectl() {
  local argument quoted_argument remote_command="kubectl"
  # SSH 会把后续参数拼接为远端 shell 命令；逐项转义可保留 JSONPath 等特殊字符。
  for argument in "$@"; do
    printf -v quoted_argument '%q' "${argument}"
    remote_command+=" ${quoted_argument}"
  done
  ssh -o BatchMode=yes -o ConnectTimeout=15 -p "${SSH_PORT}" "${SSH_USER}@${SSH_HOST}" "${remote_command}"
}

remote_shell() {
  ssh -o BatchMode=yes -o ConnectTimeout=15 -p "${SSH_PORT}" "${SSH_USER}@${SSH_HOST}" bash -s
}

remote_now_ms() {
  remote_shell <<'EOF'
date +%s%3N
EOF
}

ensure_safe_namespace() {
  case "${TEST_NAMESPACE}" in
    rootpv-e2e-*|rootpv-test-*) ;;
    *)
      echo "refusing to operate on non-test namespace: ${TEST_NAMESPACE}" >&2
      return 2
      ;;
  esac
}

prepare_results_dir() {
  if [[ "${RESULTS_DIR}" != /* ]]; then
    RESULTS_DIR="${E2E_ROOT_DIR}/${RESULTS_DIR}"
  fi
  mkdir -p "${RESULTS_DIR}"
}

wait_for_pod_running() {
  local pod_name="$1"
  remote_kubectl -n "${TEST_NAMESPACE}" wait \
    --for=jsonpath='{.status.phase}'=Running "pod/${pod_name}" \
    "--timeout=${POD_START_TIMEOUT}"
}

delete_test_namespace() {
  if [[ "${CLEANUP}" != "true" ]]; then
    return 0
  fi
  remote_kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found --wait=false >/dev/null || true
  remote_kubectl wait --for=delete "namespace/${TEST_NAMESPACE}" \
    "--timeout=${POD_DELETE_TIMEOUT}" >/dev/null 2>&1 || true
}

cleanup_test_namespace() {
  if [[ "${CLEANUP}" != "true" ]]; then
    return 0
  fi
  remote_kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found --wait=false >/dev/null
  remote_kubectl wait --for=delete "namespace/${TEST_NAMESPACE}" \
    "--timeout=${POD_DELETE_TIMEOUT}" >/dev/null
}

check_rootpv_components() {
  remote_kubectl -n "${ROOTPV_NAMESPACE}" rollout status \
    "deployment/${ROOTPV_CONTROLLER_DEPLOYMENT}" "--timeout=${POD_START_TIMEOUT}"
  remote_kubectl -n "${ROOTPV_NAMESPACE}" rollout status \
    "daemonset/${ROOTPV_DAEMONSET}" "--timeout=${POD_START_TIMEOUT}"
  local ready_label
  ready_label="$(remote_kubectl get node "${TEST_NODE}" \
    -o jsonpath='{.metadata.labels.ecloud\.com/persistent-rootfs-ready}')"
  [[ "${ready_label}" == "true" ]] || {
    echo "RootPV node is not ready: ${TEST_NODE}" >&2
    return 1
  }
}

apply_test_pvc() {
  remote_kubectl apply -f - <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: ${TEST_NAMESPACE}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${TEST_PVC_NAME}
  namespace: ${TEST_NAMESPACE}
spec:
  accessModes:
  - ReadWriteOnce
  storageClassName: ${STORAGE_CLASS}
  resources:
    requests:
      storage: ${PVC_SIZE}
EOF
}

apply_rootpv_pod() {
  local pod_name="$1"
  local rootfs_path="$2"
  local command="$3"

  {
    printf '%s\n' 'apiVersion: v1' 'kind: Pod' 'metadata:' "  name: ${pod_name}" "  namespace: ${TEST_NAMESPACE}" '  annotations:' \
      '    ecloud.com/persistent-rootfs: "true"' \
      "    ecloud.com/rootfs-container: ${TEST_CONTAINER_NAME}" \
      "    ecloud.com/rootfs-volume: ${TEST_VOLUME_NAME}" \
      "    ecloud.com/rootfs-path: ${rootfs_path}" \
      '    ecloud.com/rootfs-base-image-policy: Strict' \
      'spec:' '  restartPolicy: Never'
    if [[ -n "${ORIGINAL_RUNTIME_CLASS:-}" ]]; then
      printf '%s\n' "  runtimeClassName: ${ORIGINAL_RUNTIME_CLASS}"
    fi
    printf '%s\n' '  containers:' \
      "  - name: ${TEST_CONTAINER_NAME}" \
      "    image: ${TEST_IMAGE}" \
      "    imagePullPolicy: ${TEST_IMAGE_PULL_POLICY}" \
      '    command:' '    - /bin/bash' '    - -ec' '    - |'
    printf '%s\n' "${command}" | sed 's/^/      /'
    printf '%s\n' '    volumeMounts:' \
      "    - name: ${TEST_VOLUME_NAME}" \
      '      mountPath: /data' \
      '  volumes:' \
      "  - name: ${TEST_VOLUME_NAME}" \
      '    persistentVolumeClaim:' \
      "      claimName: ${TEST_PVC_NAME}"
  } | remote_kubectl apply -f -
}

apply_standard_pod() {
  local pod_name="$1"
  local command="${2:-sleep ${POD_SLEEP_SECONDS}}"

  {
    printf '%s\n' 'apiVersion: v1' 'kind: Pod' 'metadata:' "  name: ${pod_name}" "  namespace: ${TEST_NAMESPACE}" \
      'spec:' '  restartPolicy: Never' '  nodeSelector:' "    kubernetes.io/hostname: ${TEST_NODE}" '  containers:' \
      "  - name: ${TEST_CONTAINER_NAME}" "    image: ${TEST_IMAGE}" "    imagePullPolicy: ${TEST_IMAGE_PULL_POLICY}" \
      '    command:' '    - /bin/bash' '    - -ec' '    - |'
    printf '%s\n' "${command}" | sed 's/^/      /'
  } | remote_kubectl apply -f -
}

verify_standard_pod() {
  local pod_name="$1"
  local runtime_class mutation_marker node_name
  runtime_class="$(remote_kubectl -n "${TEST_NAMESPACE}" get pod "${pod_name}" -o jsonpath='{.spec.runtimeClassName}')"
  mutation_marker="$(remote_kubectl -n "${TEST_NAMESPACE}" get pod "${pod_name}" -o jsonpath='{.metadata.annotations.ecloud\.com/persistent-rootfs-mutated}')"
  node_name="$(remote_kubectl -n "${TEST_NAMESPACE}" get pod "${pod_name}" -o jsonpath='{.spec.nodeName}')"
  [[ -z "${runtime_class}" && -z "${mutation_marker}" ]] || {
    echo "non-opt-in pod was unexpectedly mutated" >&2
    return 1
  }
  [[ "${node_name}" == "${TEST_NODE}" ]] || {
    echo "standard pod was scheduled to an unexpected node: ${node_name}" >&2
    return 1
  }
}

assert_invalid_rootfs_config_rejected() {
  if {
    printf '%s\n' 'apiVersion: v1' 'kind: Pod' 'metadata:' \
      "  name: ${INVALID_POD_NAME}" "  namespace: ${TEST_NAMESPACE}" '  annotations:' \
      '    ecloud.com/persistent-rootfs: "true"' \
      "    ecloud.com/rootfs-container: ${INVALID_ROOTFS_CONTAINER}" \
      "    ecloud.com/rootfs-volume: ${TEST_VOLUME_NAME}" \
      '    ecloud.com/rootfs-path: e2e/invalid' \
      '    ecloud.com/rootfs-base-image-policy: Strict' \
      'spec:' '  containers:' \
      "  - name: ${TEST_CONTAINER_NAME}" \
      "    image: ${TEST_IMAGE}" \
      '    command: ["/bin/bash", "-ec", "sleep 1"]' \
      '    volumeMounts:' \
      "    - name: ${TEST_VOLUME_NAME}" \
      '      mountPath: /data' \
      '  volumes:' \
      "  - name: ${TEST_VOLUME_NAME}" \
      '    persistentVolumeClaim:' \
      "      claimName: ${TEST_PVC_NAME}"
  } | remote_kubectl apply --dry-run=server -f - >/dev/null 2>&1; then
    echo "invalid RootPV configuration was unexpectedly accepted" >&2
    return 1
  fi
}

verify_mutation() {
  local pod_name="$1"
  local runtime_class adapter node_name
  runtime_class="$(remote_kubectl -n "${TEST_NAMESPACE}" get pod "${pod_name}" -o jsonpath='{.spec.runtimeClassName}')"
  adapter="$(remote_kubectl -n "${TEST_NAMESPACE}" get pod "${pod_name}" -o jsonpath='{.metadata.annotations.ecloud\.com/runtime-adapter-id}')"
  node_name="$(remote_kubectl -n "${TEST_NAMESPACE}" get pod "${pod_name}" -o jsonpath='{.spec.nodeName}')"
  [[ "${runtime_class}" == "${EXPECTED_RUNTIME_CLASS}" ]] || {
    echo "unexpected RuntimeClass: ${runtime_class}" >&2
    return 1
  }
  [[ "${adapter}" == "${EXPECTED_RUNTIME_ADAPTER}" ]] || {
    echo "unexpected runtime adapter: ${adapter}" >&2
    return 1
  }
  [[ "${node_name}" == "${TEST_NODE}" ]] || {
    echo "unexpected test node: ${node_name}" >&2
    return 1
  }
}

delete_test_pod() {
  local pod_name="$1"
  remote_kubectl -n "${TEST_NAMESPACE}" delete "pod/${pod_name}" --wait=true \
    "--timeout=${POD_DELETE_TIMEOUT}"
}
