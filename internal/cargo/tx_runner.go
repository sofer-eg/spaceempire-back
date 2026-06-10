package cargo

import (
	"context"

	"github.com/jackc/pgx/v5"

	cargorepo "spaceempire/back/internal/persistence/cargo"
	"spaceempire/back/internal/pkg/database"
)

// RepoTxRunner is the production TxRunner. It opens a pgx transaction
// via database.TxManager and binds a fresh persistence Repository to it
// for the duration of fn.
type RepoTxRunner struct {
	tm   *database.TxManager
	base *cargorepo.Repository
}

// NewRepoTxRunner wires a RepoTxRunner. base is used only as a template
// for WithExecutor; its own executor is never invoked by Do.
func NewRepoTxRunner(tm *database.TxManager, base *cargorepo.Repository) *RepoTxRunner {
	return &RepoTxRunner{tm: tm, base: base}
}

// Do implements TxRunner.
func (r *RepoTxRunner) Do(ctx context.Context, fn func(ctx context.Context, txRepo Repo) error) error {
	return r.tm.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, r.base.WithExecutor(tx))
	})
}
