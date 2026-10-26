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
driver. Only the backend is implemented; no frontend is included.

`start.sh` listens on `:8865` by default and creates `hotmouse.db` in the
`backend` directory. The script accepts ports from `1` to `65535`. When
starting the Go binary directly, configure the listener with `HTTP_ADDR` and
the SQLite DSN with `DATABASE_DSN`.

Run checks from `backend` with `go test ./...`, `go test -race ./...`,
`go vet ./...`, and `go build ./...`.
