#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# Supplied commands support CLI administration and custom container commands.
if (( $# > 0 )); then
  exec "$@"
fi

mkdir -p -- "$(dirname -- "$HOTMOUSE_DB")" "$HOTMOUSE_PROJECT_CONTEXT_ROOT"
backend_pid=''
frontend_pid=''
cleanup() {
  local status=$? pid alive
  trap - EXIT INT TERM
  set +e
  local pids=("$frontend_pid" "$backend_pid")
  for pid in "${pids[@]}"; do
    if [[ -n "$pid" ]]; then kill -TERM -- "-$pid" 2>/dev/null; fi
  done
  # Allow the backend's 15-second graceful shutdown before forcing descendants.
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
trap 'exit 0' INT TERM

setsid hotmouse serve &
backend_pid=$!
ready=false
for ((attempt=0; attempt<150; attempt++)); do
  if ! kill -0 "$backend_pid" 2>/dev/null; then
    printf 'HotMouse 后端启动失败。\n' >&2
    exit 1
  fi
  if curl --noproxy '*' --fail --silent --max-time 1 "$HOTMOUSE_BACKEND_URL/healthz" >/dev/null; then
    ready=true
    break
  fi
  sleep 0.2
done
if [[ "$ready" != true ]]; then
  printf 'HotMouse 后端健康检查超时。\n' >&2
  exit 1
fi

setsid node /app/frontend/server.mjs &
frontend_pid=$!
status=0
wait -n "$backend_pid" "$frontend_pid" || status=$?
printf 'HotMouse 子服务退出，正在停止容器。\n' >&2
if (( status == 0 )); then status=1; fi
exit "$status"
