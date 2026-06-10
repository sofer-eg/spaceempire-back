// Command migrate is the thin CLI we use from the Makefile to apply or
// roll back goose migrations against the local development database.
// It reuses the same embedded migration set as the server runtime, so
// `make migrate-up` and an app startup with AutoMigrate=true converge on
// the same schema version.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"spaceempire/back/internal/pkg/database"
	"spaceempire/back/migrations"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("PG_DSN"), "Postgres DSN (overrides PG_DSN)")
	flag.Parse()

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "PG_DSN env or -dsn flag is required")
		os.Exit(2)
	}

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: migrate [up|down|status|version]")
		os.Exit(2)
	}

	ctx := context.Background()
	if err := run(ctx, *dsn, args[0]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dsn, cmd string) error {
	switch cmd {
	case "up":
		return database.MigrateUp(ctx, dsn)
	case "down":
		return database.MigrateDown(ctx, dsn)
	case "status", "version":
		db, err := openDB(dsn)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()

		goose.SetBaseFS(migrations.FS)
		if err := goose.SetDialect("postgres"); err != nil {
			return fmt.Errorf("goose dialect: %w", err)
		}
		if cmd == "status" {
			return goose.StatusContext(ctx, db, ".")
		}
		return goose.VersionContext(ctx, db, ".")
	default:
		return fmt.Errorf("unknown subcommand %q", cmd)
	}
}

func openDB(dsn string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	return stdlib.OpenDB(*cfg), nil
}
