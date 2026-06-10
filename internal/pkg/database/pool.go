package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config controls Pool construction.
type Config struct {
	DSN         string
	MaxConns    int32
	ConnTimeout time.Duration
	// AutoMigrate runs the embedded goose migrations during NewPool. Disabled
	// for tests that prepare the schema with testdb.Setup.
	AutoMigrate bool
}

// NewPool opens a pgx connection pool, verifies the connection, and (if
// AutoMigrate is set) brings the schema up to the latest embedded version.
func NewPool(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse pg dsn: %w", err)
	}
	if cfg.MaxConns > 0 {
		pcfg.MaxConns = cfg.MaxConns
	}
	if cfg.ConnTimeout > 0 {
		pcfg.ConnConfig.ConnectTimeout = cfg.ConnTimeout
	}

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	if cfg.AutoMigrate {
		if err := MigrateUp(ctx, cfg.DSN); err != nil {
			pool.Close()
			return nil, fmt.Errorf("run migrations: %w", err)
		}
	}

	return pool, nil
}
