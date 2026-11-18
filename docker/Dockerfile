# syntax=docker/dockerfile:1

ARG GO_IMAGE=golang:1.25-bookworm
ARG NODE_IMAGE=node:24-bookworm-slim
ARG PYTHON_IMAGE=python:3.13-slim-bookworm

# Use the Go image's existing C compiler for source-only packages such as mutf8.
# The interpreter comes from the official Python image on the same Debian base.
FROM ${PYTHON_IMAGE} AS python-base
FROM ${GO_IMAGE} AS python-tools
COPY --from=python-base /usr/local/ /usr/local/
COPY docker/toolkit-requirements.txt /opt/hotmouse/toolkit-requirements.txt
RUN --mount=type=cache,id=hotmouse-pip,target=/root/.cache/pip \
    python -m venv /opt/hotmouse/venv \
    && /opt/hotmouse/venv/bin/python -m pip install -r /opt/hotmouse/toolkit-requirements.txt \
    && /opt/hotmouse/venv/bin/python -m pip check

FROM ${GO_IMAGE} AS backend-build
ARG GOPROXY=https://proxy.golang.org,direct
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,id=hotmouse-go-mod,target=/go/pkg/mod go mod download
COPY backend/cmd ./cmd
COPY backend/internal ./internal
COPY backend/skills ./skills
RUN --mount=type=cache,id=hotmouse-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=hotmouse-go-build,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/hotmouse ./cmd/hotmouse
RUN --mount=type=cache,id=hotmouse-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=hotmouse-go-build,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/ctfy-agent ./cmd/ctfy-agent

FROM ${NODE_IMAGE} AS frontend-build
ARG NPM_REGISTRY=https://registry.npmjs.org
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,id=hotmouse-npm,target=/root/.npm npm ci --registry="$NPM_REGISTRY" --include=dev --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

FROM ${NODE_IMAGE} AS runtime
LABEL org.opencontainers.image.title="HotMouse" \
      org.opencontainers.image.description="HotMouse workbench, Dispatcher and Agent Workers" \
      org.opencontainers.image.licenses="GPL-3.0-only"

COPY --from=backend-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN --mount=type=cache,id=hotmouse-apt-lists,target=/var/lib/apt/lists,sharing=locked \
    --mount=type=cache,id=hotmouse-apt-cache,target=/var/cache/apt,sharing=locked \
    sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list.d/debian.sources \
    && rm -f /etc/apt/apt.conf.d/docker-clean \
    && apt-get -o Acquire::Retries=3 -o Acquire::http::Pipeline-Depth=0 update \
    && apt-get -o Acquire::Retries=3 -o Acquire::http::Pipeline-Depth=0 install -y --no-install-recommends \
       bash binutils ca-certificates curl ffuf file gdb git jq netcat-openbsd \
       nmap openssh-client openssl p7zip-full python3 python3-venv ripgrep \
       socat sqlmap sqlite3 squashfs-tools sshpass tini unzip util-linux xz-utils

# Keep the large base tool layer cached when adding native Python libraries.
RUN --mount=type=cache,id=hotmouse-apt-lists,target=/var/lib/apt/lists,sharing=locked \
    --mount=type=cache,id=hotmouse-apt-cache,target=/var/cache/apt,sharing=locked \
    apt-get -o Acquire::Retries=3 update \
    && apt-get -o Acquire::Retries=3 install -y --no-install-recommends \
       libfplll8 libgmp10 libmpfr6 libxml2 libxslt1.1

# Commands declared by backend/skills, including binwalk's basic extractors.
RUN --mount=type=cache,id=hotmouse-apt-lists,target=/var/lib/apt/lists,sharing=locked \
    --mount=type=cache,id=hotmouse-apt-cache,target=/var/cache/apt,sharing=locked \
    apt-get -o Acquire::Retries=3 update \
    && apt-get -o Acquire::Retries=3 install -y --no-install-recommends \
       binwalk bzip2 coreutils cpio etcd-client grep gzip ltrace strace tar

# Bookworm has no radare2 package. Use a verified upstream release rather than
# mixing Debian distributions; the package supplies both radare2 and r2.
ARG RADARE2_DOWNLOAD_BASE_URL=https://github.com/radareorg/radare2/releases/download
RUN --mount=type=cache,id=hotmouse-apt-lists,target=/var/lib/apt/lists,sharing=locked \
    --mount=type=cache,id=hotmouse-apt-cache,target=/var/cache/apt,sharing=locked \
    radare2_version=6.2.6 \
    && radare2_arch="$(dpkg --print-architecture)" \
    && case "$radare2_arch" in \
         amd64) radare2_sha256=0dddf90e217e38a6a658881b9ec7e217198487c75b7f82a332b3cbd30834636f ;; \
         arm64) radare2_sha256=527a8c8a970d5d5013d46ba20d50ef884e33650285c9b3ced64f225b87b7573d ;; \
         i386) radare2_sha256=13a2b86c74716d142c4e8c35ef21bd8b8271a21fe738818677d84a5c0892725e ;; \
         *) printf 'No pinned radare2 package for %s\n' "$radare2_arch" >&2; exit 1 ;; \
       esac \
    && curl --fail --silent --show-error --location --retry 3 --connect-timeout 30 \
       --max-time 900 --speed-limit 1024 --speed-time 30 \
       "${RADARE2_DOWNLOAD_BASE_URL}/${radare2_version}/radare2_${radare2_version}_${radare2_arch}.deb" \
       -o /tmp/hotmouse-radare2.deb \
    && printf '%s  %s\n' "$radare2_sha256" /tmp/hotmouse-radare2.deb | sha256sum --check - \
    && apt-get -o Acquire::Retries=3 install -y --no-install-recommends /tmp/hotmouse-radare2.deb \
    && rm /tmp/hotmouse-radare2.deb

# Prepare Python tools before a task starts; agents reuse this interpreter.
COPY --from=python-base /usr/local/ /usr/local/
COPY --from=python-tools /opt/hotmouse/venv /opt/hotmouse/venv
COPY docker/toolkit-requirements.txt /opt/hotmouse/toolkit-requirements.txt
RUN /opt/hotmouse/venv/bin/python -m pip check

# Additional CLIs remain available for the standalone CTFy target.
# Workbench workers always use the mandatory Pi installation below.
ARG AGENT_PACKAGES="@openai/codex@0.167.0 @anthropic-ai/claude-code@2.1.318"
ARG PI_PACKAGE="@earendil-works/pi-coding-agent@1.5.0"
ARG PI_OPTCHAT_PACKAGE="pi-optchat@0.11.12"
ARG NPM_REGISTRY=https://registry.npmjs.org
# Package names are intentionally split into separate npm arguments.
RUN --mount=type=cache,id=hotmouse-npm,target=/root/.npm \
    if [ -n "$AGENT_PACKAGES" ]; then \
      npm install --global --registry="$NPM_REGISTRY" --no-audit --no-fund $AGENT_PACKAGES; \
    fi

# Install together so pi-optchat's peer dependencies share Pi's SDK. Keep the
# package outside /home/node: existing home volumes must not hide image updates.
RUN --mount=type=cache,id=hotmouse-npm,target=/root/.npm \
    npm install --prefix /opt/hotmouse/pi --registry="$NPM_REGISTRY" --no-audit --no-fund "$PI_PACKAGE" "$PI_OPTCHAT_PACKAGE" \
    && ln -sf /opt/hotmouse/pi/node_modules/.bin/pi /usr/local/bin/pi \
    && test -r /opt/hotmouse/pi/node_modules/pi-optchat/src/index.ts

# npm may silently discard Codex's optional native package after a download
# failure. Install it as a required package when the launcher cannot run.
RUN --mount=type=cache,id=hotmouse-npm,target=/root/.npm \
    if command -v codex >/dev/null 2>&1 && ! codex --version >/dev/null 2>&1; then \
      codex_platform="$(node -e 'const pkg = require(process.argv[1]); const name = "@openai/codex-linux-" + process.arch; const spec = pkg.optionalDependencies?.[name]; if (!spec) throw new Error("Unsupported Codex platform: " + process.arch); console.log(name + "@" + spec);' "$(npm root --global)/@openai/codex/package.json")" \
      && npm install --global --registry="$NPM_REGISTRY" --no-audit --no-fund "$codex_platform" \
      || exit 1; \
    fi \
    && for cli in codex claude pi; do \
      if command -v "$cli" >/dev/null 2>&1; then "$cli" --version || exit 1; fi; \
    done

WORKDIR /app
COPY --from=backend-build /out/hotmouse /usr/local/bin/hotmouse
COPY --from=frontend-build /src/frontend/dist ./frontend/dist
COPY frontend/server.mjs ./frontend/server.mjs
COPY --chmod=755 docker/entrypoint.sh /usr/local/bin/hotmouse-entrypoint
COPY LICENSE /usr/share/doc/hotmouse/LICENSE
COPY backend/internal/knowledge/LICENSE.sapper-v2 backend/internal/knowledge/DISCLAIMER.sapper-v2.md backend/internal/knowledge/NOTICE.md /usr/share/doc/hotmouse/sapper-v2/
COPY backend/skills /usr/local/share/hotmouse/skills
COPY backend/internal/knowledge/skills /usr/local/share/hotmouse/skills
COPY backend/pentest_toolkit /usr/local/share/hotmouse/pentest_toolkit

ENV NODE_ENV=production \
    HOTMOUSE_LOG_LEVEL=debug \
    HOTMOUSE_LOG_ENV=1 \
    HOTMOUSE_SKILLS_DIR=/usr/local/share/hotmouse/skills \
    HOTMOUSE_PENTEST_TOOLKIT_DIR=/usr/local/share/hotmouse/pentest_toolkit \
    HOTMOUSE_PYTHON=/opt/hotmouse/venv/bin/python \
    PATH=/opt/hotmouse/venv/bin:$PATH \
    HOST=0.0.0.0 \
    PORT=9981 \
    HOTMOUSE_ADDR=127.0.0.1:9982 \
    HOTMOUSE_BACKEND_URL=http://127.0.0.1:9982 \
    HOTMOUSE_SERVER=http://127.0.0.1:9982 \
    HOTMOUSE_DB=/data/hotmouse.db \
    HOTMOUSE_PROJECT_CONTEXT_ROOT=/data/context \
    HOTMOUSE_AUTORUN=1 \
    HOTMOUSE_WORKER_DRIVER=pi \
    PI_OPTCHAT_EXTENSION=/opt/hotmouse/pi/node_modules/pi-optchat/src/index.ts \
    HOME=/home/node

RUN mkdir -p /data/context && chown -R node:node /data
COPY docker/skill-tools.json /opt/hotmouse/skill-tools.json
COPY docker/verify-toolkit.py /opt/hotmouse/verify-toolkit.py
USER node
# Verify the final PATH, embedded skill files and tools as the runtime user.
RUN pi --version \
    && test -r "$PI_OPTCHAT_EXTENSION" \
    && python /opt/hotmouse/verify-toolkit.py
EXPOSE 9981
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD curl --noproxy '*' --fail --silent --show-error --max-time 3 "http://127.0.0.1:${PORT}/healthz" >/dev/null || exit 1
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/usr/local/bin/hotmouse-entrypoint"]

# TSec runs a whole benchmark autonomously through the workbench's Dispatcher,
# Worker pool and flag ledger. Its host network already provides the VPN.
FROM runtime AS tsec
LABEL org.opencontainers.image.title="HotMouse TSec" \
      org.opencontainers.image.description="Autonomous TSec Benchmark participant with HotMouse Dispatcher and Agent Workers"
COPY --chmod=755 docker/tsec-entrypoint.sh /usr/local/bin/tsec-entrypoint
ENV HOTMOUSE_LLM_BASE_URL=http://127.0.0.1:8000/v1 \
    HOTMOUSE_LLM_API=openai \
    TSEC_VPN_MODE=external \
    TSEC_NETWORK_CHECK_URL="" \
    TSEC_TARGET_PROBE_ENABLED=0 \
    HOTMOUSE_COMPETITION_CONCURRENCY=3 \
    HOTMOUSE_COMPETITION_MAX_WORKERS=3 \
    HOTMOUSE_PROJECT_CONCURRENCY=3 \
    HOTMOUSE_PROJECT_MAX_WORKERS=3 \
    HOTMOUSE_WORKER_COUNT=16 \
    HOTMOUSE_PROJECT_BUDGET_SECONDS=0 \
    HOTMOUSE_COMPETITION_CHALLENGE_TIMEOUT=30m \
    TSEC_RETRY_INTERVAL=8s \
    TSEC_SHUTDOWN_TIMEOUT=30s \
    TSEC_RESULT_FILE=/data/tsec-result.json \
    HOTMOUSE_LOG_LEVEL=info \
    HOTMOUSE_LOG_ENV=0
HEALTHCHECK NONE
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/usr/local/bin/tsec-entrypoint"]
VOLUME ["/data", "/home/node"]

# CTFy starts the container in /ctfy/work and gives it a briefing there. This
# target keeps HotMouse's offline CTF knowledge and native CLI tools, but runs
# one bounded platform task instead of starting the workbench service.
FROM runtime AS ctfy-hosted
COPY --from=backend-build /out/ctfy-agent /usr/local/bin/ctfy-agent
HEALTHCHECK NONE
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/usr/local/bin/ctfy-agent"]

# TPCTF participant mode owns the WireGuard tunnel on the team's VPS.
FROM ctfy-hosted AS ctfy-agent
USER root
RUN --mount=type=cache,id=hotmouse-apt-lists,target=/var/lib/apt/lists,sharing=locked \
    --mount=type=cache,id=hotmouse-apt-cache,target=/var/cache/apt,sharing=locked \
    apt-get -o Acquire::Retries=3 update \
    && apt-get -o Acquire::Retries=3 install -y --no-install-recommends \
       iproute2 iptables ncat openresolv wireguard-tools wireguard-go
COPY --chmod=755 docker/ctfy-entrypoint.sh /usr/local/bin/ctfy-entrypoint
ENV HOTMOUSE_LOG_LEVEL=info HOTMOUSE_LOG_ENV=0
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/usr/local/bin/ctfy-entrypoint"]

# Keep the historical workbench image as the default docker build target.
FROM runtime AS default
# CTFy forbids image volumes because they escape the platform's disk budget.
VOLUME ["/data", "/home/node"]
