#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

export HOTMOUSE_WORKER_DRIVER=pi

if (( $# > 0 )); then
  exec "$@"
fi

exec hotmouse competition run tsec
