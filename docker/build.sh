#!/usr/bin/env bash
set -Eeuo pipefail

project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
image_kind=workbench
case "${1:-}" in
  workbench|tsec|ctfy) image_kind=$1; shift ;;
  ''|-*) ;;
  *) printf '错误：未知镜像类型 %s，请使用 workbench、tsec 或 ctfy。\n' "$1" >&2; exit 2 ;;
esac
if [[ "${1:-}" == --help || "${1:-}" == -h ]]; then
  cat <<'HELP'
用法：./docker/build.sh [workbench|tsec|ctfy] [docker build 参数...]

单独构建指定镜像，省略类型时构建工作台。
  workbench  工作台及 Agent CLI，默认 hotmouse:latest
  tsec       TSec 接入工作台，默认 hotmouse-tsec:latest
  ctfy       CTFy 专用 Agent，默认 hotmouse-ctfy:latest
HOTMOUSE_IMAGE 可覆盖当前镜像名称；额外参数原样传给 docker build。
可从任意目录执行。需要 Docker Engine 和 BuildKit。

示例：
  ./docker/build.sh
  ./docker/build.sh tsec
  ./docker/build.sh ctfy
  HOTMOUSE_IMAGE=hotmouse-tsec:v1 ./docker/build.sh tsec --no-cache
  HOTMOUSE_IMAGE=hotmouse-ctfy:v1 ./docker/build.sh ctfy --platform linux/amd64
  HOTMOUSE_IMAGE=hotmouse:v1 ./docker/build.sh --no-cache
  ./docker/build.sh --build-arg AGENT_PACKAGES=
  ./docker/build.sh --build-arg NODE_IMAGE=node:24-bookworm-slim
HELP
  exit 0
fi
case "$image_kind" in
  workbench) build_target=default; default_image=hotmouse:latest ;;
  tsec) build_target=tsec; default_image=hotmouse-tsec:latest ;;
  ctfy) build_target=ctfy-agent; default_image=hotmouse-ctfy:latest ;;
esac
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
  --target "$build_target" \
  --tag "${HOTMOUSE_IMAGE:-$default_image}" \
  "${proxy_args[@]}" "$@" "$project_dir"
