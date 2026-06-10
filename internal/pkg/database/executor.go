// Package database provides a thin pgx wrapper used by all repositories:
// an Executor interface satisfied by both *pgxpool.Pool and pgx.Tx, a Pool
// constructor with optional goose migrations, and a TxManager that hands a
// transaction-bound executor to the caller.
package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Executor is the minimal pgx surface a repository needs. It is satisfied by
// both *pgxpool.Pool and pgx.Tx, so repositories work transparently inside
// and outside of transactions via Repository.WithExecutor.
type Executor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
