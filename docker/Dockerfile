# syntax=docker/dockerfile:1

ARG GO_IMAGE=golang:1.25-bookworm
ARG NODE_IMAGE=node:24-bookworm-slim

FROM ${GO_IMAGE} AS backend-build
ARG GOPROXY=https://proxy.golang.org,direct
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,id=hotmouse-go-mod,target=/go/pkg/mod go mod download
COPY backend/cmd ./cmd
COPY backend/internal ./internal
RUN --mount=type=cache,id=hotmouse-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=hotmouse-go-build,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/hotmouse ./cmd/hotmouse

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
       bash ca-certificates curl git openssl python3 python3-venv \
       ripgrep sqlite3 tini unzip util-linux

ARG AGENT_PACKAGES="@openai/codex@0.167.0 @anthropic-ai/claude-code@2.1.318 @mariozechner/pi-coding-agent@0.73.1"
ARG NPM_REGISTRY=https://registry.npmjs.org
# Package names are intentionally split into separate npm arguments.
RUN --mount=type=cache,id=hotmouse-npm,target=/root/.npm \
    if [ -n "$AGENT_PACKAGES" ]; then \
      npm install --global --registry="$NPM_REGISTRY" --no-audit --no-fund $AGENT_PACKAGES; \
    fi

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

ENV NODE_ENV=production \
    HOST=0.0.0.0 \
    PORT=9981 \
    HOTMOUSE_ADDR=127.0.0.1:9982 \
    HOTMOUSE_BACKEND_URL=http://127.0.0.1:9982 \
    HOTMOUSE_SERVER=http://127.0.0.1:9982 \
    HOTMOUSE_DB=/data/hotmouse.db \
    HOTMOUSE_PROJECT_CONTEXT_ROOT=/data/context \
    HOTMOUSE_AUTORUN=1 \
    HOME=/home/node

RUN mkdir -p /data/context && chown -R node:node /data
USER node
EXPOSE 9981
VOLUME ["/data", "/home/node"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD curl --noproxy '*' --fail --silent --show-error --max-time 3 "http://127.0.0.1:${PORT}/healthz" >/dev/null || exit 1
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/usr/local/bin/hotmouse-entrypoint"]
