#!/usr/bin/env bash
set -Eeuo pipefail
set +m

PROJECT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
mode=dev
usage() {
  cat <<'HELP'
用法：./start.sh [--production] [--help]

默认启动后端及 Vite 开发界面；--production 构建并启动正式界面。
前端默认监听 http://127.0.0.1:9981，后端默认监听 http://127.0.0.1:9982。
前端同源代理后端 API。
按 Ctrl+C 停止前后端。需要 Bash 4.3+、Go/CGO、Node.js、npm、curl 和 setsid。

环境变量：
  HOTMOUSE_BACKEND_PORT   后端监听端口，默认 9982
  HOTMOUSE_FRONTEND_PORT  前端监听端口，默认 9981
  HOTMOUSE_DB             数据库路径，默认 backend/data/hotmouse.db
  HOTMOUSE_AUTORUN        自动调度，默认 1；设为 0 关闭
  HOTMOUSE_AUTH_REQUIRED  默认启用认证；设为 0 关闭
  HOTMOUSE_LOG_DIR        日志及运行文件目录，默认 .hotmouse/run

相对的数据库和日志路径以项目根目录为基准。
HELP
}
for argument in "$@"; do
  case "$argument" in
    --production) mode=production ;;
    --help|-h) usage; exit 0 ;;
    *) printf '错误：未知参数 %s\n' "$argument" >&2; usage >&2; exit 2 ;;
  esac
done

fail() { printf '错误：%s\n' "$*" >&2; exit 1; }
if (( BASH_VERSINFO[0] < 4 || (BASH_VERSINFO[0] == 4 && BASH_VERSINFO[1] < 3) )); then
  fail '需要 Bash 4.3 或更新版本。'
fi
for dependency in go node npm curl setsid; do
  command -v "$dependency" >/dev/null 2>&1 || fail "缺少 $dependency，请先安装。"
done
node -e '
  const [major, minor] = process.versions.node.split(".").map(Number);
  if (!((major === 20 && minor >= 19) || (major === 22 && minor >= 12) || major >= 24)) {
    console.error("需要 Node.js 20.19+、22.12+ 或 24+ 的受支持版本。");
    process.exit(1);
  }
' || exit 1

backend_port="${HOTMOUSE_BACKEND_PORT:-9982}"
frontend_port="${HOTMOUSE_FRONTEND_PORT:-9981}"
for port in "$backend_port" "$frontend_port"; do
  [[ "$port" =~ ^[1-9][0-9]{0,4}$ ]] && (( port <= 65535 )) || fail "无效端口：$port"
done
[[ "$backend_port" != "$frontend_port" ]] || fail '前端入口与后端内部监听不能使用同一端口；后端 API 已通过前端入口代理。'
autorun=true
case "${HOTMOUSE_AUTORUN:-1}" in
  1) autorun=true ;;
  0) autorun=false ;;
  *) fail 'HOTMOUSE_AUTORUN 必须是 0 或 1。' ;;
esac
database="${HOTMOUSE_DB:-$PROJECT_DIR/backend/data/hotmouse.db}"
log_dir="${HOTMOUSE_LOG_DIR:-$PROJECT_DIR/.hotmouse/run}"
[[ "$database" == /* ]] || database="$PROJECT_DIR/$database"
[[ "$log_dir" == /* ]] || log_dir="$PROJECT_DIR/$log_dir"
backend_url="http://127.0.0.1:$backend_port"
frontend_url="http://127.0.0.1:$frontend_port"

# Binding checks work without ss/lsof and include wildcard-listener conflicts.
node --input-type=module - "$backend_port" "$frontend_port" <<'JS'
import net from 'node:net';
for (const port of process.argv.slice(2).map(Number)) {
  await new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once('error', () => reject(new Error(`端口 ${port} 已被占用或无法监听，请停止现有服务或修改端口。`)));
    server.listen(port, '127.0.0.1', () => server.close(resolve));
  }).catch(error => { process.stderr.write(`错误：${error.message}\n`); process.exit(1); });
}
JS

umask 077
mkdir -p -- "$log_dir"
backend_log="$log_dir/backend.log"
frontend_log="$log_dir/frontend.log"
backend_pid=''
frontend_pid=''
setup_pid=''
cleanup() {
  local status=$?
  trap - EXIT INT TERM
  set +e
  local pid alive
  local pids=("$setup_pid" "$frontend_pid" "$backend_pid")
  for pid in "${pids[@]}"; do
    if [[ -n "$pid" ]]; then kill -TERM -- "-$pid" 2>/dev/null; fi
  done
  # The backend has a 15-second graceful shutdown limit.
  for ((attempt=0; attempt<200; attempt++)); do
    alive=false
    for pid in "${pids[@]}"; do
      if [[ -n "$pid" ]] && kill -0 -- "-$pid" 2>/dev/null; then alive=true; fi
    done
    if [[ "$alive" == false ]]; then break; fi
    sleep 0.1
  done
  for pid in "${pids[@]}"; do
    if [[ -n "$pid" ]]; then
      kill -KILL -- "-$pid" 2>/dev/null
      wait "$pid" 2>/dev/null
    fi
  done
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Each job owns a process group, including npm/build subprocesses. Cleanup
# signals only groups created by this invocation, never an existing service.
run_setup() {
  local directory=$1 status=0
  shift
  setsid bash -c 'cd -- "$1" || exit; shift; exec "$@"' hotmouse-setup "$directory" "$@" &
  setup_pid=$!
  wait "$setup_pid" || status=$?
  if (( status == 0 )); then setup_pid=''; fi
  return "$status"
}

if [[ ! -f "$PROJECT_DIR/frontend/node_modules/vite/bin/vite.js" ||
      ! -f "$PROJECT_DIR/frontend/node_modules/.package-lock.json" ||
      "$PROJECT_DIR/frontend/package-lock.json" -nt "$PROJECT_DIR/frontend/node_modules/.package-lock.json" ||
      "$PROJECT_DIR/frontend/package.json" -nt "$PROJECT_DIR/frontend/node_modules/.package-lock.json" ]]; then
  printf '安装前端依赖…\n'
  run_setup "$PROJECT_DIR/frontend" npm ci --include=dev --no-audit --no-fund
fi
printf '编译后端…\n'
run_setup "$PROJECT_DIR/backend" go build -o "$log_dir/hotmouse" ./cmd/hotmouse
if [[ "$mode" == production ]]; then
  printf '构建前端…\n'
  run_setup "$PROJECT_DIR/frontend" npm run build
fi

wait_ready() {
  local pid=$1 url=$2 log=$3 service=$4 deadline=$((SECONDS + 30))
  while (( SECONDS < deadline )); do
    if ! kill -0 "$pid" 2>/dev/null; then
      tail -n 30 -- "$log" >&2
      fail "$service 启动失败，日志：$log"
    fi
    if [[ -n "$backend_pid" ]] && ! kill -0 "$backend_pid" 2>/dev/null; then
      tail -n 30 -- "$backend_log" >&2
      fail "后端已退出，日志：$backend_log"
    fi
    if curl --noproxy '*' --silent --fail --max-time 1 "$url" >/dev/null; then return 0; fi
    sleep 0.2
  done
  tail -n 30 -- "$log" >&2
  fail "$service 启动超时，日志：$log"
}

printf '启动后端（自动调度：%s）…\n' "$autorun"
setsid bash -c 'cd -- "$1" || exit; shift; exec "$@"' hotmouse-backend "$PROJECT_DIR/backend" \
  "$log_dir/hotmouse" serve --addr "127.0.0.1:$backend_port" --db "$database" --autorun="$autorun" \
  >"$backend_log" 2>&1 &
backend_pid=$!
wait_ready "$backend_pid" "$backend_url/healthz" "$backend_log" 后端

printf '启动前端（%s）…\n' "$mode"
if [[ "$mode" == production ]]; then
  HOST=127.0.0.1 PORT="$frontend_port" HOTMOUSE_BACKEND_URL="$backend_url" \
    setsid node "$PROJECT_DIR/frontend/server.mjs" >"$frontend_log" 2>&1 &
else
  HOTMOUSE_BACKEND_URL="$backend_url" setsid bash -c 'cd -- "$1" || exit; shift; exec "$@"' \
    hotmouse-frontend "$PROJECT_DIR/frontend" node ./node_modules/vite/bin/vite.js \
    --host 127.0.0.1 --port "$frontend_port" --strictPort >"$frontend_log" 2>&1 &
fi
frontend_pid=$!
wait_ready "$frontend_pid" "$frontend_url/" "$frontend_log" 前端
wait_ready "$frontend_pid" "$frontend_url/healthz" "$frontend_log" 前端代理

printf '\nHotMouse 已启动\n  前端：%s\n  后端：%s\n  数据库：%s\n  日志：%s\n\n前端已配置后端 API 同源代理。按 Ctrl+C 停止前后端。\n' \
  "$frontend_url" "$backend_url" "$database" "$log_dir"
status=0
wait -n "$backend_pid" "$frontend_pid" || status=$?
printf '服务意外退出（状态 %s），正在停止另一服务。请查看 %s\n' "$status" "$log_dir" >&2
exit 1
