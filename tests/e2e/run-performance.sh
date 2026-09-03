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

summary_ms() {
  local samples_file="$1"
  awk '{sum += $1} END {if (NR == 0) exit 1; printf "%.0f", sum / NR}' "${samples_file}"
}

percentile_ms() {
  local samples_file="$1"
  local percentile="$2"
  local count index
  count="$(wc -l <"${samples_file}" | tr -d ' ')"
  index=$(( (count * percentile + 99) / 100 ))
  sort -n "${samples_file}" | sed -n "${index}p"
}

sum_ms() {
  awk '{sum += $1} END {if (NR == 0) exit 1; print sum}' "$1"
}

throughput_mib_per_second() {
  local iterations="$1"
  local elapsed_ms="$2"
  awk -v total_mib="$((iterations * PERF_WRITE_MIB))" -v elapsed_ms="${elapsed_ms}" \
    'BEGIN {printf "%.2f", total_mib * 1000 / elapsed_ms}'
}

latency_loss_percent() {
  local baseline="$1"
  local rootpv="$2"
  awk -v baseline="${baseline}" -v rootpv="${rootpv}" \
    'BEGIN {printf "%+.2f%%", (rootpv - baseline) * 100 / baseline}'
}

throughput_loss_percent() {
  local baseline="$1"
  local rootpv="$2"
  awk -v baseline="${baseline}" -v rootpv="${rootpv}" \
    'BEGIN {printf "%.2f%%", (baseline - rootpv) * 100 / baseline}'
}

run_standard_iteration() {
  local pod_name="$1"
  local sample_prefix="$2"
  local iteration="$3"
  local record_csv="${4:-true}"
  local start_ms ready_ms write_start_ms write_end_ms delete_start_ms delete_end_ms
  local startup_ms write_ms cleanup_ms

  start_ms="$(remote_now_ms)"
  apply_standard_pod "${pod_name}" "sleep ${POD_SLEEP_SECONDS}"
  wait_for_pod_running "${pod_name}"
  verify_standard_pod "${pod_name}"
  ready_ms="$(remote_now_ms)"
  write_start_ms="$(remote_now_ms)"
  remote_kubectl -n "${TEST_NAMESPACE}" exec "${pod_name}" -c "${TEST_CONTAINER_NAME}" -- \
    dd if=/dev/zero of="${PERF_FILE_PATH}" bs=1M count="${PERF_WRITE_MIB}" conv=fdatasync status=none
  write_end_ms="$(remote_now_ms)"
  delete_start_ms="$(remote_now_ms)"
  delete_test_pod "${pod_name}" >/dev/null
  delete_end_ms="$(remote_now_ms)"
  startup_ms=$((ready_ms - start_ms))
  write_ms=$((write_end_ms - write_start_ms))
  cleanup_ms=$((delete_end_ms - delete_start_ms))
  printf '%s\n' "${startup_ms}" >>"${sample_prefix}-startup-ms"
  printf '%s\n' "${write_ms}" >>"${sample_prefix}-write-ms"
  printf '%s\n' "${cleanup_ms}" >>"${sample_prefix}-cleanup-ms"
  if [[ "${record_csv}" == "true" ]]; then
    printf '未启用 RootPV,%s,%s,%s,%s\n' "${iteration}" "${startup_ms}" "${write_ms}" "${cleanup_ms}" >>"${csv_file}"
  fi
}

run_rootpv_iteration() {
  local pod_name="$1"
  local sample_prefix="$2"
  local iteration="$3"
  local record_csv="${4:-true}"
  local start_ms ready_ms write_start_ms write_end_ms delete_start_ms delete_end_ms
  local startup_ms write_ms cleanup_ms

  start_ms="$(remote_now_ms)"
  apply_rootpv_pod "${pod_name}" "${PERF_ROOTFS_PATH}" "sleep ${POD_SLEEP_SECONDS}"
  wait_for_pod_running "${pod_name}"
  verify_mutation "${pod_name}"
  ready_ms="$(remote_now_ms)"
  write_start_ms="$(remote_now_ms)"
  remote_kubectl -n "${TEST_NAMESPACE}" exec "${pod_name}" -c "${TEST_CONTAINER_NAME}" -- \
    dd if=/dev/zero of="${PERF_FILE_PATH}" bs=1M count="${PERF_WRITE_MIB}" conv=fdatasync status=none
  write_end_ms="$(remote_now_ms)"
  delete_start_ms="$(remote_now_ms)"
  delete_test_pod "${pod_name}" >/dev/null
  delete_end_ms="$(remote_now_ms)"
  startup_ms=$((ready_ms - start_ms))
  write_ms=$((write_end_ms - write_start_ms))
  cleanup_ms=$((delete_end_ms - delete_start_ms))
  printf '%s\n' "${startup_ms}" >>"${sample_prefix}-startup-ms"
  printf '%s\n' "${write_ms}" >>"${sample_prefix}-write-ms"
  printf '%s\n' "${cleanup_ms}" >>"${sample_prefix}-cleanup-ms"
  if [[ "${record_csv}" == "true" ]]; then
    printf '启用 RootPV,%s,%s,%s,%s\n' "${iteration}" "${startup_ms}" "${write_ms}" "${cleanup_ms}" >>"${csv_file}"
  fi
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
[[ "${PERF_ITERATIONS}" -gt 0 ]] || {
  echo "PERF_ITERATIONS must be greater than zero" >&2
  exit 2
}
[[ "${PERF_WARMUP_ITERATIONS}" -ge 0 ]] || {
  echo "PERF_WARMUP_ITERATIONS must not be negative" >&2
  exit 2
}
[[ "${PERF_BASELINE_ITERATIONS}" -gt 0 ]] || {
  echo "PERF_BASELINE_ITERATIONS must be greater than zero" >&2
  exit 2
}

result_file="${RESULTS_DIR}/performance-$(date +%F).md"
csv_file="${RESULTS_DIR}/performance-$(date +%F).csv"
samples_dir="$(mktemp -d)"
cleanup_performance_test() {
  delete_test_namespace
  # samples_dir 由 mktemp 创建，仅删除本次测试生成的临时样本文件。
  if [[ -d "${samples_dir}" ]]; then
    rm -rf -- "${samples_dir}"
  fi
}
trap cleanup_performance_test EXIT
baseline_samples="${samples_dir}/baseline"
rootpv_samples="${samples_dir}/rootpv"
printf '模式,迭代次数,启动时延ms,写入时延ms,清理时延ms\n' >"${csv_file}"

check_rootpv_components
delete_test_namespace

# 基线与 RootPV 使用同一镜像、节点、写入量和删除流程，先消除冷启动干扰再进行正式测量。
remote_kubectl create namespace "${TEST_NAMESPACE}"
for ((warmup = 1; warmup <= PERF_WARMUP_ITERATIONS; warmup++)); do
  run_standard_iteration "rootpv-performance-baseline-warmup-${warmup}" "${samples_dir}/baseline-warmup" "${warmup}" false
done
for iteration in $(seq 1 "${PERF_BASELINE_ITERATIONS}"); do
  run_standard_iteration "rootpv-performance-baseline-${iteration}" "${baseline_samples}" "${iteration}"
done

apply_test_pvc
for ((warmup = 1; warmup <= PERF_WARMUP_ITERATIONS; warmup++)); do
  run_rootpv_iteration "rootpv-performance-rootpv-warmup-${warmup}" "${samples_dir}/rootpv-warmup" "${warmup}" false
done
for iteration in $(seq 1 "${PERF_ITERATIONS}"); do
  run_rootpv_iteration "rootpv-performance-rootpv-${iteration}" "${rootpv_samples}" "${iteration}"
done

baseline_startup_avg="$(summary_ms "${baseline_samples}-startup-ms")"
baseline_write_avg="$(summary_ms "${baseline_samples}-write-ms")"
baseline_cleanup_avg="$(summary_ms "${baseline_samples}-cleanup-ms")"
baseline_startup_p95="$(percentile_ms "${baseline_samples}-startup-ms" 95)"
baseline_write_p95="$(percentile_ms "${baseline_samples}-write-ms" 95)"
baseline_cleanup_p95="$(percentile_ms "${baseline_samples}-cleanup-ms" 95)"
baseline_write_throughput="$(throughput_mib_per_second "${PERF_BASELINE_ITERATIONS}" "$(sum_ms "${baseline_samples}-write-ms")")"
rootpv_startup_avg="$(summary_ms "${rootpv_samples}-startup-ms")"
rootpv_write_avg="$(summary_ms "${rootpv_samples}-write-ms")"
rootpv_cleanup_avg="$(summary_ms "${rootpv_samples}-cleanup-ms")"
rootpv_startup_p95="$(percentile_ms "${rootpv_samples}-startup-ms" 95)"
rootpv_write_p95="$(percentile_ms "${rootpv_samples}-write-ms" 95)"
rootpv_cleanup_p95="$(percentile_ms "${rootpv_samples}-cleanup-ms" 95)"
rootpv_write_throughput="$(throughput_mib_per_second "${PERF_ITERATIONS}" "$(sum_ms "${rootpv_samples}-write-ms")")"
cleanup_summary="按配置保留测试命名空间及 PVC。"
if [[ "${CLEANUP}" == "true" ]]; then
  cleanup_test_namespace
  CLEANUP=false
  cleanup_summary="已完成测试命名空间及 PVC 清理。"
fi

cat >"${result_file}" <<EOF
# RootPV 性能测试结果

- 日期：$(date +%F)
- 环境：${TEST_ENVIRONMENT_SUMMARY}
- 存储：${STORAGE_SUMMARY}
- 镜像：${TEST_IMAGE_SUMMARY}
- 预热轮数：${PERF_WARMUP_ITERATIONS}
- 未启用 RootPV 迭代次数：${PERF_BASELINE_ITERATIONS}
- 启用 RootPV 迭代次数：${PERF_ITERATIONS}
- 每轮根文件系统写入：${PERF_WRITE_MIB} MiB

| 指标 | 未启用 RootPV | 启用 RootPV | 性能损耗占比 |
| --- | ---: | ---: | ---: |
| Pod 创建至 Running（平均 / P95） | ${baseline_startup_avg} / ${baseline_startup_p95} ms | ${rootpv_startup_avg} / ${rootpv_startup_p95} ms | 平均 $(latency_loss_percent "${baseline_startup_avg}" "${rootpv_startup_avg}")；P95 $(latency_loss_percent "${baseline_startup_p95}" "${rootpv_startup_p95}") |
| 根文件系统顺序写入（平均 / P95） | ${baseline_write_avg} / ${baseline_write_p95} ms | ${rootpv_write_avg} / ${rootpv_write_p95} ms | 平均 $(latency_loss_percent "${baseline_write_avg}" "${rootpv_write_avg}")；P95 $(latency_loss_percent "${baseline_write_p95}" "${rootpv_write_p95}") |
| Pod 删除与运行时清理（平均 / P95） | ${baseline_cleanup_avg} / ${baseline_cleanup_p95} ms | ${rootpv_cleanup_avg} / ${rootpv_cleanup_p95} ms | 平均 $(latency_loss_percent "${baseline_cleanup_avg}" "${rootpv_cleanup_avg}")；P95 $(latency_loss_percent "${baseline_cleanup_p95}" "${rootpv_cleanup_p95}") |
| 根文件系统顺序写入吞吐量 | ${baseline_write_throughput} MiB/s | ${rootpv_write_throughput} MiB/s | $(throughput_loss_percent "${baseline_write_throughput}" "${rootpv_write_throughput}") |

未启用 RootPV 时写入容器可写层；启用 RootPV 时写入持久根目录。两者差异代表启用持久根目录的端到端成本，并非仅 runtime wrapper 的开销。逐轮结果见同目录的 CSV 文件。${cleanup_summary}
EOF

echo "performance test passed: ${result_file}"
