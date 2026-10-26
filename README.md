# HotMouse

HotMouse is a Go decision-support API. Each project stores an append-only,
directed graph of facts and one goal, backed by SQLite through GORM and served
with Iris.

See [backend API documentation](docs/API.md) for endpoints and graph rules.

Run the backend with the default port `8865`:

```sh
./start.sh
```

Specify a different port as the first argument:

```sh
./start.sh 9000
```

Requires Go 1.23 or newer and a C compiler with CGO enabled for the SQLite
driver.

`start.sh` listens on `:8865` by default and creates `hotmouse.db` in the
`backend` directory. The script accepts ports from `1` to `65535`. When
starting the Go binary directly, configure the listener with `HTTP_ADDR` and
the SQLite DSN with `DATABASE_DSN`.

Run checks from `backend` with `go test ./...`, `go test -race ./...`,
`go vet ./...`, and `go build ./...`.

## Frontend

The frontend lives in `frontend/` and is a Vite + React + TypeScript app
styled after the GitHub design language. It proxies `/api/*` to the backend,
which serves no static assets and sends no CORS headers.

```sh
cd frontend
npm install
npm run dev
```

The dev server runs on `:5173` and talks to the backend on `:8865`. Point it
elsewhere with `HOTMOUSE_API_URL=http://127.0.0.1:9000 npm run dev`.

Frontend conventions live in [frontend/STYLE.md](frontend/STYLE.md), and
`npm run style:check` enforces them.
