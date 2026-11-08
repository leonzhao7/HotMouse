#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

log() {
  local level=$1 event=$2
  shift 2
  case "${HOTMOUSE_LOG_LEVEL:-info}" in
    error) [[ "$level" == ERROR ]] || return 0 ;;
    warn|warning) [[ "$level" == ERROR || "$level" == WARN ]] || return 0 ;;
  esac
  printf 'time=%(%Y-%m-%dT%H:%M:%S%z)T level=%s component=container event=%s' -1 "$level" "$event" >&2
  if (( $# > 0 )); then printf ' %s' "$@" >&2; fi
  printf '\n' >&2
}

log INFO container.start "pid=$$" "uid=$EUID"

# Supplied commands support CLI administration and custom container commands.
if (( $# > 0 )); then
  log INFO container.command "argc=$#"
  exec "$@"
fi

mkdir -p -- "$(dirname -- "$HOTMOUSE_DB")" "$HOTMOUSE_PROJECT_CONTEXT_ROOT"
backend_pid=''
frontend_pid=''
cleanup() {
  local status=$? pid alive
  log INFO container.shutdown "exit_code=$status" "backend_pid=${backend_pid:-none}" "frontend_pid=${frontend_pid:-none}"
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
  log INFO container.exit "exit_code=$status"
  exit "$status"
}
trap cleanup EXIT
trap 'log INFO container.signal signal=SIGINT; exit 0' INT
trap 'log INFO container.signal signal=SIGTERM; exit 0' TERM

log INFO backend.start
setsid hotmouse serve &
backend_pid=$!
log INFO backend.started "pid=$backend_pid"
ready=false
for ((attempt=0; attempt<150; attempt++)); do
  if ! kill -0 "$backend_pid" 2>/dev/null; then
    log ERROR backend.start_failed "pid=$backend_pid"
    exit 1
  fi
  if curl --noproxy '*' --fail --silent --max-time 1 "$HOTMOUSE_BACKEND_URL/healthz" >/dev/null; then
    ready=true
    break
  fi
  sleep 0.2
done
if [[ "$ready" != true ]]; then
  log ERROR backend.health_timeout "pid=$backend_pid"
  exit 1
fi
log INFO backend.ready "pid=$backend_pid"

log INFO frontend.start
setsid node /app/frontend/server.mjs &
frontend_pid=$!
log INFO frontend.started "pid=$frontend_pid"
status=0
exited_pid=''
wait -n -p exited_pid "$backend_pid" "$frontend_pid" || status=$?
log ERROR container.service_exit "pid=${exited_pid:-unknown}" "exit_code=$status"
if (( status == 0 )); then status=1; fi
exit "$status"
