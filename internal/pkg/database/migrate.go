package database

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"spaceempire/back/migrations"
)

const migrationsDir = "."

// gooseMu serialises calls to goose.SetBaseFS/SetDialect/UpContext.
// goose stores these on package-global vars, so concurrent MigrateUp from
// parallel integration tests races without this lock.
var gooseMu sync.Mutex

func openMigrateDB(dsn string) (*sql.DB, error) {
	pcfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse pg dsn: %w", err)
	}
	return stdlib.OpenDB(*pcfg), nil
}

// MigrateUp applies all pending migrations against the database at dsn.
func MigrateUp(ctx context.Context, dsn string) error {
	db, err := openMigrateDB(dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	gooseMu.Lock()
	defer gooseMu.Unlock()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

// MigrateDown rolls back the most recent migration. Used by the local
// dev workflow (`make migrate-down`) — not invoked at startup.
func MigrateDown(ctx context.Context, dsn string) error {
	db, err := openMigrateDB(dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	gooseMu.Lock()
	defer gooseMu.Unlock()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}
	if err := goose.DownContext(ctx, db, migrationsDir); err != nil {
		return fmt.Errorf("goose down: %w", err)
	}
	return nil
}
