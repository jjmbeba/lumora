package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jjmbeba/lumora/backend/internal/config"
	"github.com/jjmbeba/lumora/backend/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("migration command failed: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 || (args[0] != "up" && args[0] != "status") {
		return errors.New("usage: go run ./cmd/migrate {up|status}")
	}
	dsn, err := config.RequireEnv("DATABASE_URL")
	if err != nil {
		return err
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}

	provider, err := migrations.NewProvider(db)
	if err != nil {
		return fmt.Errorf("configure Goose: %w", err)
	}

	switch args[0] {
	case "up":
		results, err := provider.Up(ctx)
		if err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
		for _, result := range results {
			log.Printf("applied migration %d (%s)", result.Source.Version, result.Source.Path)
		}
		if len(results) == 0 {
			log.Print("database is up to date")
		}
	case "status":
		statuses, err := provider.Status(ctx)
		if err != nil {
			return fmt.Errorf("read migration status: %w", err)
		}
		for _, status := range statuses {
			state := "pending"
			if status.State == goose.StateApplied {
				state = "applied"
			}
			log.Printf("%d %s %s", status.Source.Version, state, status.Source.Path)
		}
	}
	return nil
}
