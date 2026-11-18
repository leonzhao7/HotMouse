#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
if (( $# > 0 )); then exec "$@"; fi
# Hosted tasks retain the unprivileged runtime required by native Claude Code.
# Competition mode keeps NET_ADMIN to bring up/down the participant VPN.
if [[ "${CTFY_MODE:-}" == hosted || ( "${CTFY_MODE:-}" != competition && ( -f "${CTFY_WORK_DIR:-$PWD}/task.json" || "${CTFY_EVAL_DRY_RUN:-}" == 1 ) ) ]]; then
  if (( EUID == 0 )); then
    exec setpriv --reuid=node --regid=node --init-groups /usr/local/bin/ctfy-agent
  fi
fi
exec /usr/local/bin/ctfy-agent
