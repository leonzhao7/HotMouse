#!/usr/bin/env bash

# 同时启动 HotMouse 后端和前端开发服务器。
#
#   ./start.sh            # 后端 :8865，前端 :8864
#   ./start.sh 9000       # 后端 :9000，前端 :8864
#   ./start.sh 9000 3000  # 后端 :9000，前端 :3000
#
# 也支持环境变量: BACKEND_PORT / FRONTEND_PORT / HOTMOUSE_API_URL。
#
# 端口被占用时直接启动失败，不重试也不自动换端口。

set -euo pipefail

root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

backend_port="${1:-${BACKEND_PORT:-8865}}"
frontend_port="${2:-${FRONTEND_PORT:-8864}}"

strip_colon() {
	printf '%s' "${1#:}"
}

backend_port="$(strip_colon "$backend_port")"
frontend_port="$(strip_colon "$frontend_port")"

check_port() {
	local label="$1"
	local value="$2"
	if ! [[ "$value" =~ ^[0-9]+$ ]] || (( value < 1 || value > 65535 )); then
		echo "usage: $0 [backend-port] [frontend-port]" >&2
		echo "$label port must be an integer between 1 and 65535" >&2
		exit 2
	fi
}

check_port "backend" "$backend_port"
check_port "frontend" "$frontend_port"

if [[ "$backend_port" == "$frontend_port" ]]; then
	echo "backend and frontend ports must differ (both are $backend_port)" >&2
	exit 2
fi

if ! command -v node >/dev/null 2>&1; then
	echo "node is required to run the frontend" >&2
	exit 1
fi

if [[ ! -d "$root_dir/frontend/node_modules" ]]; then
	echo "installing frontend dependencies..."
	(cd "$root_dir/frontend" && npm install)
fi

export HTTP_ADDR=":$backend_port"
export FRONTEND_PORT="$frontend_port"
export HOTMOUSE_API_URL="${HOTMOUSE_API_URL:-http://127.0.0.1:$backend_port}"

backend_pid=""
frontend_pid=""

# 递归杀掉某个进程及其所有后代。
#
# go run 和 npm run 都会再 fork 出真正的服务进程，只杀包装进程会留下孤儿。
# 这里按父子关系向下清理，而不是按进程组 —— 进程组可能和别的服务重叠，
# 误杀的风险太大。
kill_tree() {
	local pid="$1"
	[[ -n "$pid" ]] || return 0
	local child
	for child in $(pgrep -P "$pid" 2>/dev/null); do
		kill_tree "$child"
	done
	kill -TERM "$pid" 2>/dev/null || true
}

cleanup() {
	local status=$?
	trap - EXIT INT TERM
	kill_tree "$frontend_pid"
	kill_tree "$backend_pid"
	wait "$frontend_pid" 2>/dev/null || true
	wait "$backend_pid" 2>/dev/null || true
	exit "$status"
}

trap cleanup EXIT INT TERM

echo "backend:  http://127.0.0.1:$backend_port"
echo "frontend: http://127.0.0.1:$frontend_port"

(cd "$root_dir/frontend" && npm run dev) &
frontend_pid=$!

(cd "$root_dir/backend" && go run ./cmd/hotmouse) &
backend_pid=$!

# 任意一个服务退出（含启动失败）都结束整个脚本。
wait -n "$backend_pid" "$frontend_pid"
