package main

import (
	"context"
	"log"
	"os"
	"streamtool-relay/internal/persistence"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal("migration job failed: ", err)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	s, err := persistence.Open(ctx, os.Getenv("SQLITE_PATH"))
	if err != nil {
		return err
	}
	defer s.Close()
	dir := os.Getenv("SQLITE_MIGRATIONS_DIR")
	if dir == "" {
		dir = "sqlite-migrations"
	}
	if err = s.Migrate(ctx, dir); err != nil {
		return err
	}
	return nil
}
