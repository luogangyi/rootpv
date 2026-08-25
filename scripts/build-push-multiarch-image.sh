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

IMAGE="${IMAGE:-rootpv:dev}"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
PUSH_SLEEP_SECONDS="${PUSH_SLEEP_SECONDS:-10}"
DOCKERFILE="${DOCKERFILE:-Dockerfile}"
BUILD_CONTEXT="${BUILD_CONTEXT:-.}"
BUILDER="${BUILDER:-}"
PROVENANCE="${PROVENANCE:-false}"

usage() {
  cat <<'EOF'
Build and push RootPV multi-arch images.

Environment:
  IMAGE                Final multi-arch image reference, including tag.
                       Default: rootpv:dev
  PLATFORMS           Comma-separated platforms. Default: linux/amd64,linux/arm64
  PUSH_SLEEP_SECONDS  Sleep after every registry push. Must be >= 10. Default: 10
  DOCKERFILE          Dockerfile path. Default: Dockerfile
  BUILD_CONTEXT       Docker build context. Default: .
  BUILDER             Optional docker buildx builder name.
  PROVENANCE          docker buildx provenance setting. Default: false

The script pushes per-architecture tags first:
  ${IMAGE}-amd64
  ${IMAGE}-arm64

Then it creates and pushes the final manifest list:
  ${IMAGE}
EOF
}

die() {
  echo "error: $*" >&2
  exit 1
}

sleep_after_push() {
  local ref="$1"
  echo "Pushed ${ref}; sleeping ${PUSH_SLEEP_SECONDS}s to avoid registry throttling"
  sleep "${PUSH_SLEEP_SECONDS}"
}

platform_suffix() {
  local platform="$1"
  case "${platform}" in
    linux/amd64) printf 'amd64' ;;
    linux/arm64) printf 'arm64' ;;
    *)
      printf '%s' "${platform}" | tr '/+' '--' | tr -cd '[:alnum:]_.-'
      ;;
  esac
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

command -v docker >/dev/null 2>&1 || die "docker is required"
docker buildx version >/dev/null 2>&1 || die "docker buildx is required"

case "${PUSH_SLEEP_SECONDS}" in
  ''|*[!0-9]*) die "PUSH_SLEEP_SECONDS must be an integer >= 10" ;;
esac
[ "${PUSH_SLEEP_SECONDS}" -ge 10 ] || die "PUSH_SLEEP_SECONDS must be >= 10"

last_component="${IMAGE##*/}"
case "${last_component}" in
  *:*) ;;
  *) die "IMAGE must include an explicit tag, got ${IMAGE}" ;;
esac

image_repo="${IMAGE%:*}"
image_tag="${IMAGE##*:}"
IFS=',' read -r -a platforms <<<"${PLATFORMS}"
[ "${#platforms[@]}" -gt 0 ] || die "PLATFORMS must not be empty"

buildx_common=(docker buildx build)
if [ -n "${BUILDER}" ]; then
  buildx_common+=(--builder "${BUILDER}")
fi

arch_images=()
for platform in "${platforms[@]}"; do
  platform="$(printf '%s' "${platform}" | xargs)"
  [ -n "${platform}" ] || continue
  suffix="$(platform_suffix "${platform}")"
  [ -n "${suffix}" ] || die "could not derive tag suffix from platform ${platform}"
  arch_image="${image_repo}:${image_tag}-${suffix}"
  echo "Building and pushing ${platform} image: ${arch_image}"
  "${buildx_common[@]}" \
    --platform "${platform}" \
    --file "${DOCKERFILE}" \
    --tag "${arch_image}" \
    --provenance="${PROVENANCE}" \
    --push \
    "${BUILD_CONTEXT}"
  arch_images+=("${arch_image}")
  sleep_after_push "${arch_image}"
done

[ "${#arch_images[@]}" -gt 0 ] || die "no platform images were built"

echo "Creating multi-arch manifest: ${IMAGE}"
imagetools_common=(docker buildx imagetools create)
if [ -n "${BUILDER}" ]; then
  imagetools_common+=(--builder "${BUILDER}")
fi
"${imagetools_common[@]}" --tag "${IMAGE}" "${arch_images[@]}"
sleep_after_push "${IMAGE}"

docker buildx imagetools inspect "${IMAGE}"
