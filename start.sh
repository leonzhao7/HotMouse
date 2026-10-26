#!/usr/bin/env bash

set -euo pipefail

root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
port="${1:-8865}"

if [[ "$port" == :* ]]; then
	port="${port#:}"
fi

if ! [[ "$port" =~ ^[0-9]+$ ]] || (( port < 1 || port > 65535 )); then
	echo "usage: $0 [port]" >&2
	echo "port must be an integer between 1 and 65535" >&2
	exit 2
fi

export HTTP_ADDR=":$port"

cd "$root_dir/backend"
exec go run ./cmd/hotmouse
