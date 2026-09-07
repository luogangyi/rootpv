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

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "${script_dir}/lib/common.sh"

usage() {
  echo "usage: $0 --config <test-config.env>" >&2
}

config_file=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --config)
      [[ $# -ge 2 ]] || {
        usage
        exit 2
      }
      config_file="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      exit 2
      ;;
  esac
done
[[ -n "${config_file}" ]] || {
  usage
  exit 2
}

load_e2e_config "${config_file}"
ensure_safe_namespace
prepare_results_dir
trap delete_test_namespace EXIT

result_file="${RESULTS_DIR}/functional-$(date +%F).md"
first_pod="rootpv-functional-first"
second_pod="rootpv-functional-second"

check_rootpv_components
delete_test_namespace
apply_test_pvc
apply_standard_pod "${NON_ROOTPV_POD_NAME}"
wait_for_pod_running "${NON_ROOTPV_POD_NAME}"
verify_standard_pod "${NON_ROOTPV_POD_NAME}"
delete_test_pod "${NON_ROOTPV_POD_NAME}"
assert_invalid_rootfs_config_rejected

first_command="printf '%s\\n' '${FUNCTIONAL_FIRST_MARKER}' > '${FUNCTIONAL_MARKER_PATH}'
sync
sleep ${POD_SLEEP_SECONDS}"
apply_rootpv_pod "${first_pod}" "${FUNCTIONAL_ROOTFS_PATH}" "${first_command}"
wait_for_pod_running "${first_pod}"
verify_mutation "${first_pod}"
first_marker="$(remote_kubectl -n "${TEST_NAMESPACE}" exec "${first_pod}" -c "${TEST_CONTAINER_NAME}" -- cat "${FUNCTIONAL_MARKER_PATH}")"
[[ "${first_marker}" == "${FUNCTIONAL_FIRST_MARKER}" ]] || {
  echo "first pod did not write the expected marker" >&2
  exit 1
}

delete_test_pod "${first_pod}"
second_command="test \"\$(cat '${FUNCTIONAL_MARKER_PATH}')\" = '${FUNCTIONAL_FIRST_MARKER}'
printf '%s\\n' '${FUNCTIONAL_SECOND_MARKER}' > '${FUNCTIONAL_MARKER_PATH}'
sync
sleep ${POD_SLEEP_SECONDS}"
apply_rootpv_pod "${second_pod}" "${FUNCTIONAL_ROOTFS_PATH}" "${second_command}"
wait_for_pod_running "${second_pod}"
verify_mutation "${second_pod}"
second_marker="$(remote_kubectl -n "${TEST_NAMESPACE}" exec "${second_pod}" -c "${TEST_CONTAINER_NAME}" -- cat "${FUNCTIONAL_MARKER_PATH}")"
[[ "${second_marker}" == "${FUNCTIONAL_SECOND_MARKER}" ]] || {
  echo "recreated pod did not preserve and update the marker" >&2
  exit 1
}
cleanup_summary="按配置保留测试命名空间及 PVC。"
if [[ "${CLEANUP}" == "true" ]]; then
  cleanup_test_namespace
  CLEANUP=false
  cleanup_summary="已完成测试命名空间及 PVC 清理。"
fi

cat >"${result_file}" <<EOF
# RootPV 功能测试结果

- 日期：$(date +%F)
- 环境：${TEST_ENVIRONMENT_SUMMARY}
- 存储：${STORAGE_SUMMARY}
- 镜像：${TEST_IMAGE_SUMMARY}

## 结果

- RootPV Controller 和节点代理均处于就绪状态。
- 未启用 RootPV 的 Pod 保持原始 RuntimeClass 和 Pod 定义。
- 目标容器不存在的 RootPV 配置被准入校验拒绝。
- 准入注入的 RuntimeClass 为 \`${EXPECTED_RUNTIME_CLASS}\`，适配器为 \`${EXPECTED_RUNTIME_ADAPTER}\`。
- 首次 Pod 成功写入根目录标记 \`${FUNCTIONAL_FIRST_MARKER}\`。
- 使用相同 PVC 路径重建 Pod 后，成功验证并更新标记为 \`${FUNCTIONAL_SECOND_MARKER}\`。
- ${cleanup_summary}
EOF

echo "functional test passed: ${result_file}"
