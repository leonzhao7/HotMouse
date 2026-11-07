#!/usr/bin/env bash
set -Eeuo pipefail

project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
if [[ "${1:-}" == --help || "${1:-}" == -h ]]; then
  cat <<'HELP'
用法：./docker/build.sh [docker build 参数...]

构建前后端及 Agent CLI 镜像，默认名称 hotmouse:latest。
可从任意目录执行。需要 Docker Engine 和 BuildKit。

示例：
  ./docker/build.sh
  HOTMOUSE_IMAGE=hotmouse:v1 ./docker/build.sh --no-cache
  ./docker/build.sh --build-arg AGENT_PACKAGES=
  ./docker/build.sh --build-arg NODE_IMAGE=node:24-bookworm-slim
HELP
  exit 0
fi
command -v docker >/dev/null 2>&1 || { printf '错误：需要 Docker。\n' >&2; exit 1; }
# Go expects uppercase proxies; apt uses lowercase. Forward existing proxy
# settings as Docker's predefined build args, without embedding them in layers.
export HTTP_PROXY="${HTTP_PROXY:-${http_proxy:-}}"
export HTTPS_PROXY="${HTTPS_PROXY:-${https_proxy:-}}"
export NO_PROXY="${NO_PROXY:-${no_proxy:-}}"
export http_proxy="${http_proxy:-$HTTP_PROXY}"
export https_proxy="${https_proxy:-$HTTPS_PROXY}"
export no_proxy="${no_proxy:-$NO_PROXY}"
proxy_args=()
for proxy_name in HTTP_PROXY HTTPS_PROXY NO_PROXY http_proxy https_proxy no_proxy; do
  if [[ -n "${!proxy_name}" ]]; then proxy_args+=(--build-arg "$proxy_name"); fi
done
export DOCKER_BUILDKIT=1
exec docker build \
  --file "$project_dir/docker/Dockerfile" \
  --tag "${HOTMOUSE_IMAGE:-hotmouse:latest}" \
  "${proxy_args[@]}" "$@" "$project_dir"
