package schemaguard_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/pkg/database/schemaguard"
	"spaceempire/back/internal/pkg/database/testdb"
)

const (
	// codeNoUniqueForConflict — "there is no unique or exclusion constraint
	// matching the ON CONFLICT specification". The TASK-151 signature.
	codeNoUniqueForConflict = "42P10"
	// codeUndefinedColumn / codeUndefinedTable — the same class of code-vs-schema
	// drift, only in a column or table name.
	codeUndefinedColumn = "42703"
	codeUndefinedTable  = "42P01"

	// minVersionNum is PostgreSQL 16, the first release with
	// EXPLAIN (GENERIC_PLAN).
	minVersionNum = 160000

	// minUpserts is the lower bound of the guard's coverage: nine production
	// upserts across seven tables at the time of writing (TASK-155).
	minUpserts = 9
)

// TestIntegration_SchemaGuard_UpsertTargetsMatchSchema plans every production
// INSERT ... ON CONFLICT literal against the migrated schema. Postgres resolves
// the ON CONFLICT arbiter index at plan time, so planning alone (no values, no
// rows written) proves the target still matches a real UNIQUE/PK key.
func TestIntegration_SchemaGuard_UpsertTargetsMatchSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	requirePlannerSupport(ctx, t, pool)

	root, err := schemaguard.RepoRoot()
	require.NoError(t, err)

	upserts, err := schemaguard.FindUpserts(root)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(upserts), minUpserts,
		"found only %d upsert literals, expected at least %d: either the extractor "+
			"stopped seeing them (AST walk / skip list in schemaguard) or the upserts "+
			"moved out of the scanned tree -- do not lower this bound to make the test pass",
		len(upserts), minUpserts)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	var verified, skipped int
	for _, up := range upserts {
		t.Run(fmt.Sprintf("%s:%d", up.File, up.Line), func(t *testing.T) {
			planErr := planStatement(ctx, tx, up.SQL)

			var pgErr *pgconn.PgError
			switch {
			case planErr == nil:
				verified++
			case errors.As(planErr, &pgErr) && isSchemaDrift(pgErr.Code):
				verified++
				t.Error(driftReport(ctx, tx, up, pgErr))
			default:
				skipped++
				t.Logf("NOT VERIFIED (skipped): %s:%d could not be planned: %v -- "+
					"not a schema-drift code, so this literal's ON CONFLICT target is unchecked",
					up.File, up.Line, planErr)
			}
		})
	}

	t.Logf("schemaguard: scanned %d upsert literals, verified %d, skipped %d",
		len(upserts), verified, skipped)
}

// TestIntegration_SchemaGuard_DetectsBrokenTarget is the guard's own regression
// test: on a fixture table it plans a correct upsert, a too-narrow ON CONFLICT
// target and an undefined column, and asserts the guard classifies each and
// reports what to fix. Without it a broken classifier would leave the guard
// permanently, silently green.
func TestIntegration_SchemaGuard_DetectsBrokenTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	requirePlannerSupport(ctx, t, pool)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
CREATE TABLE guard_fixture (a bigint NOT NULL, b bigint NOT NULL, qty bigint NOT NULL);
CREATE UNIQUE INDEX guard_fixture_ab_uniq ON guard_fixture (a, b);`)
	require.NoError(t, err)

	fullTarget := `
INSERT INTO guard_fixture (a, b, qty) VALUES ($1, $2, $3)
ON CONFLICT (a, b) DO UPDATE SET qty = guard_fixture.qty + EXCLUDED.qty`
	require.NoError(t, planStatement(ctx, tx, fullTarget),
		"a target covering the whole unique key must plan")

	narrowTarget := `
INSERT INTO guard_fixture (a, b, qty) VALUES ($1, $2, $3)
ON CONFLICT (a) DO UPDATE SET qty = guard_fixture.qty + EXCLUDED.qty`
	var pgErr *pgconn.PgError
	require.ErrorAs(t, planStatement(ctx, tx, narrowTarget), &pgErr)
	assert.Equal(t, codeNoUniqueForConflict, pgErr.Code)
	assert.True(t, isSchemaDrift(pgErr.Code))

	report := driftReport(ctx, tx, schemaguard.Upsert{
		SQL: narrowTarget, Table: "guard_fixture", File: "internal/fake/repo.go", Line: 42,
	}, pgErr)
	assert.Contains(t, report, "internal/fake/repo.go:42", "report points at the literal")
	assert.Contains(t, report, codeNoUniqueForConflict, "report carries the Postgres code")
	assert.Contains(t, report, "guard_fixture_ab_uniq", "report lists the existing unique key")
	assert.Contains(t, report, "(a, b)", "report shows the key's columns in order")
	assert.Contains(t, report, "ON CONFLICT", "report says what to grep after a key change")

	undefinedColumn := `
INSERT INTO guard_fixture (a, b, nope) VALUES ($1, $2, $3) ON CONFLICT (a, b) DO NOTHING`
	require.ErrorAs(t, planStatement(ctx, tx, undefinedColumn), &pgErr)
	assert.Equal(t, codeUndefinedColumn, pgErr.Code)
	assert.True(t, isSchemaDrift(pgErr.Code), "a stale column name is the same drift class")
}

// planStatement plans sql without running it.
//
// EXPLAIN (GENERIC_PLAN) is the only check that reaches the planner without
// parameter values, and the planner is where it matters: PREPARE stops after
// parse+analyze, before arbiter-index inference, so it accepts the exact stale
// target that broke every container pickup in TASK-151 (verified on PG 16.12).
//
// The statement runs inside a savepoint so a planning error leaves the caller's
// transaction usable — the failure report still needs to query the catalog.
func planStatement(ctx context.Context, tx pgx.Tx, sql string) error {
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("savepoint: %w", err)
	}
	defer func() { _ = savepoint.Rollback(ctx) }()

	// No arguments: pgx sends this over the simple protocol, which is what lets
	// the $N placeholders stay unbound.
	_, err = savepoint.Exec(ctx, "EXPLAIN (GENERIC_PLAN, COSTS OFF) "+sql)
	return err
}

func isSchemaDrift(code string) bool {
	switch code {
	case codeNoUniqueForConflict, codeUndefinedColumn, codeUndefinedTable:
		return true
	default:
		return false
	}
}

func requirePlannerSupport(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	var version int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT current_setting('server_version_num')::int`).Scan(&version))

	if version < minVersionNum {
		t.Fatalf("schemaguard needs PostgreSQL 16+ for EXPLAIN (GENERIC_PLAN), got "+
			"server_version_num=%d; testdb pins postgres:16-alpine, so check whether the "+
			"image was downgraded. This is a failure and not a skip on purpose: on an older "+
			"server the guard would verify nothing while still reporting green.", version)
	}
}

const uniqueIndexesSQL = `
SELECT i.relname, ix.indisprimary, pg_get_indexdef(ix.indexrelid)
FROM pg_index ix
JOIN pg_class i ON i.oid = ix.indexrelid
WHERE ix.indrelid = to_regclass($1) AND ix.indisunique
ORDER BY ix.indisprimary DESC, i.relname
`

func driftReport(ctx context.Context, tx pgx.Tx, up schemaguard.Upsert, pgErr *pgconn.PgError) string {
	var out strings.Builder

	fmt.Fprintf(&out, "\n%s\n", driftHeadline(pgErr.Code, up.Table))
	fmt.Fprintf(&out, "  literal:  %s:%d\n", up.File, up.Line)
	fmt.Fprintf(&out, "  table:    %s\n", up.Table)
	fmt.Fprintf(&out, "  postgres: %s %s\n", pgErr.Code, pgErr.Message)
	out.WriteString(uniqueKeyListing(ctx, tx, up.Table))
	fmt.Fprintf(&out, "  fix:      align the ON CONFLICT target at %s:%d with one of the keys above,\n",
		up.File, up.Line)
	out.WriteString("            or fix the migration that changed the key. A migration that adds, drops or\n")
	out.WriteString("            changes a UNIQUE/PRIMARY KEY must be accompanied by a grep of 'ON CONFLICT'\n")
	out.WriteString("            for that table (see CLAUDE.md, \"Миграции и ON CONFLICT\").\n")
	fmt.Fprintf(&out, "  sql:      %s\n", strings.TrimSpace(up.SQL))

	return out.String()
}

func driftHeadline(code, table string) string {
	switch code {
	case codeNoUniqueForConflict:
		return fmt.Sprintf("ON CONFLICT target matches no UNIQUE/PK key of table %q -- Postgres resolves "+
			"the arbiter index at plan time, so EVERY execution of this statement fails, not only "+
			"the conflicting ones", table)
	case codeUndefinedColumn:
		return fmt.Sprintf("SQL literal names a column that table %q does not have", table)
	case codeUndefinedTable:
		return fmt.Sprintf("SQL literal targets table %q, which the schema does not have", table)
	default:
		return fmt.Sprintf("SQL literal does not match the schema of table %q", table)
	}
}

func uniqueKeyListing(ctx context.Context, tx pgx.Tx, table string) string {
	rows, err := tx.Query(ctx, uniqueIndexesSQL, table)
	if err != nil {
		return fmt.Sprintf("  keys:     (could not list unique keys: %v)\n", err)
	}
	defer rows.Close()

	var out strings.Builder
	fmt.Fprintf(&out, "  existing UNIQUE/PK keys on %q:\n", table)

	found := false
	for rows.Next() {
		var (
			name      string
			isPrimary bool
			def       string
		)
		if err := rows.Scan(&name, &isPrimary, &def); err != nil {
			return fmt.Sprintf("  keys:     (could not read unique keys: %v)\n", err)
		}
		found = true
		kind := "UNIQUE"
		if isPrimary {
			kind = "PRIMARY KEY"
		}
		fmt.Fprintf(&out, "    - %s (%s): %s\n", name, kind, columnsOf(def))
	}
	if err := rows.Err(); err != nil {
		return fmt.Sprintf("  keys:     (could not list unique keys: %v)\n", err)
	}
	if !found {
		out.WriteString("    (none -- the table has no UNIQUE or PRIMARY KEY at all)\n")
	}

	return out.String()
}

// columnsOf reduces a pg_get_indexdef definition to its trailing column list,
// which is the part an ON CONFLICT target has to match (a partial index keeps
// its WHERE clause, which the target must repeat too).
func columnsOf(indexDef string) string {
	if open := strings.Index(indexDef, "("); open >= 0 {
		return indexDef[open:]
	}
	return indexDef
}
