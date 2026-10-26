package main

import (
	"fmt"
	"log"
	"os"

	"github.com/kataras/iris/v12"

	"hotmouse/backend/internal/api"
	"hotmouse/backend/internal/storage"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	dsn := envOrDefault("DATABASE_DSN", "hotmouse.db")
	address := envOrDefault("HTTP_ADDR", ":8080")

	db, err := storage.Open(dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get database connection: %w", err)
	}
	defer sqlDB.Close()

	app := api.NewApp(api.NewService(db))
	log.Printf("HotMouse API listening on %s", address)
	if err := app.Listen(address, iris.WithoutServerError(iris.ErrServerClosed)); err != nil {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
