# HotMouse

HotMouse is a Go decision-support API. Each project stores an append-only,
directed graph of facts and one goal, backed by SQLite through GORM and served
with Iris. It also stores the LLM endpoint settings in a singleton database
row.

See [backend API documentation](docs/API.md) for endpoints and graph rules.

Requires Go 1.23 or newer and a C compiler with CGO enabled for the SQLite
driver.

Run the backend and the frontend together in development:

```sh
./start.sh
```

The backend listens on `:8865` and the frontend dev server on `:8864`.
Specify different ports as the first and second arguments:

```sh
./start.sh 9000        # backend :9000, frontend :8864
./start.sh 9000 3000   # backend :9000, frontend :3000
```

`BACKEND_PORT` and `FRONTEND_PORT` work as alternatives. A port already in
use makes the script fail immediately; nothing retries or picks another port.

`start.sh` creates `hotmouse.db` in the `backend` directory and accepts ports
from `1` to `65535`. When starting the Go binary directly, configure the
listener with `HTTP_ADDR` and the SQLite DSN with `DATABASE_DSN`.

Run checks from `backend` with `go test ./...`, `go test -race ./...`,
`go vet ./...`, and `go build ./...`.

## Worker

The worker starts an OpenAI-compatible streaming chat agent using the worker
LLM profile stored in SQLite. You can optionally configure `worker_command` in
the settings page to run an external Worker command instead. Run it from
`backend/` after configuring the settings through the API or frontend:

```sh
go run ./cmd/hotmouse worker \
  --prompt "Summarize the current project graph" \
  --output ./worker-output.txt
```

The prompt can also be supplied as one positional argument. The worker sends
the prompt to the configured worker endpoint (appending `/chat/completions`
when needed), streams the agent response to the terminal, and atomically saves
the completed output to `--output`. A configured external Worker command is
started without a shell, receives the prompt on stdin, and can use
`{{prompt}}` as an argument placeholder. It uses `DATABASE_DSN` or defaults
to `hotmouse.db`.

When the normal API process is running, it also starts a background dispatcher.
Creating a project or appending a fact or step queues that project for
reasoning. The dispatcher selects a promising action with the configured LLM,
runs it through the worker, summarizes the result into new facts and steps,
and marks the project completed when the goal is supported by the resulting
graph. Only one dispatch run is active per project, and each graph update is
applied transactionally.

## Frontend

The frontend lives in `frontend/` and is a Vite + React + TypeScript app
styled after the GitHub design language. It proxies `/api/*` to the backend,
which serves no static assets and sends no CORS headers.

To run it on its own, without the backend:

```sh
cd frontend
npm install
npm run dev
```

The dev server runs on `:8864` and talks to the backend on `:8865`. Override
either with `FRONTEND_PORT` and `HOTMOUSE_API_URL`:

```sh
FRONTEND_PORT=3000 HOTMOUSE_API_URL=http://127.0.0.1:9000 npm run dev
```

Frontend conventions live in [frontend/STYLE.md](frontend/STYLE.md), and
`npm run style:check` enforces them.
