package main

import (
	"context"
	"log"
	"os"

	"github.com/kataras/iris/v12"
	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/httpapi"
	"hotmouse/backend/internal/worker"
)

func main() {
	path := os.Getenv("HOTMOUSE_DB")
	db, err := core.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer db.SQL.Close()
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	if os.Getenv("HOTMOUSE_AUTORUN") == "1" {
		worker.New(db).Start(workerCtx)
	}
	app := iris.New()
	httpapi.Register(app, httpapi.New(db))
	addr := os.Getenv("HOTMOUSE_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("HotMouse backend listening on %s", addr)
	if err := app.Listen(addr, iris.WithoutServerError(iris.ErrServerClosed)); err != nil {
		log.Fatal(err)
	}
}
