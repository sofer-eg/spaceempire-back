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
	// codeUndefinedObject — "constraint %q for table %q does not exist": what the
	// other legal target form, ON CONFLICT ON CONSTRAINT <name>, raises once the
	// constraint is renamed or dropped. Postgres resolves it in the planner just
	// like a column-list target, so it breaks every execution too — with a
	// different SQLSTATE, which is why it needs naming explicitly.
	codeUndefinedObject = "42704"

	// minVersionNum is PostgreSQL 16, the first release with
	// EXPLAIN (GENERIC_PLAN).
	minVersionNum = 160000

	// minUpserts is the lower bound of the guard's coverage: nine production
	// upserts across seven tables at the time of writing (TASK-155).
	minUpserts = 9
)

// planOutcome is what came back when a literal was handed to the planner.
type planOutcome int

const (
	// outcomePlanned — Postgres planned the statement, so its ON CONFLICT target
	// resolves to a real arbiter index.
	outcomePlanned planOutcome = iota
	// outcomeDrift — the planner rejected the statement because the code and the
	// schema disagree. This is the bug the guard exists to catch.
	outcomeDrift
	// outcomeUnplannable — the literal never reached the planner, so nothing about
	// its target was checked. It fails the test: a literal that quietly stops
	// being planned leaves exactly the hole TASK-151 slipped through.
	outcomeUnplannable
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

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	var planned, unchecked int
	for _, up := range upserts {
		outcome, report := checkUpsert(ctx, tx, up)
		if outcome == outcomeUnplannable {
			unchecked++
		} else {
			planned++
		}

		// Classification happens outside the subtest so that -run on a single
		// literal (see subtestName) still leaves the coverage counters below
		// complete; the subtest only attributes the failure to its literal.
		t.Run(subtestName(up), func(t *testing.T) {
			// Anything but a clean plan carries a report, and a report is a
			// failure: t.Logf would be invisible in `make test-integration`,
			// which runs without -v.
			if report != "" {
				t.Error(report)
			}
		})
	}

	assert.Zero(t, unchecked,
		"%d of %d literals never reached the planner, so their ON CONFLICT targets are "+
			"unverified — see the UNCHECKED failures above. An unverified target is exactly "+
			"the hole TASK-151 slipped through, so it fails instead of being logged.",
		unchecked, len(upserts))

	require.GreaterOrEqual(t, planned, minUpserts,
		"only %d of the %d literals found were actually planned, expected at least %d "+
			"verified: either the extractor stopped seeing literals (AST walk in schemaguard, "+
			"or the upserts moved out of the scanned tree) or they stopped reaching the "+
			"planner. The bound counts *verified* literals on purpose — do not lower it, and "+
			"do not widen the classification just to make an unchecked literal look green.",
		planned, len(upserts), minUpserts)

	t.Logf("schemaguard: scanned %d upsert literals, planned %d, unchecked %d",
		len(upserts), planned, unchecked)
}

// TestIntegration_SchemaGuard_DetectsBrokenTarget is the guard's own regression
// test: on a fixture table it runs every classification branch through the same
// checkUpsert the real test uses — a correct upsert, both broken target forms
// (column list and ON CONSTRAINT), stale column and table names, and two
// literals that cannot be planned at all. Without it a broken classifier would
// leave the guard permanently, silently green.
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

	cases := []struct {
		name         string
		table        string
		sql          string
		want         planOutcome
		wantInReport []string
	}{
		{
			name:  "target covers the whole unique key",
			table: "guard_fixture",
			sql: `INSERT INTO guard_fixture (a, b, qty) VALUES ($1, $2, $3)
ON CONFLICT (a, b) DO UPDATE SET qty = guard_fixture.qty + EXCLUDED.qty`,
			want: outcomePlanned,
		},
		{
			name:  "column target too narrow for the key",
			table: "guard_fixture",
			sql: `INSERT INTO guard_fixture (a, b, qty) VALUES ($1, $2, $3)
ON CONFLICT (a) DO UPDATE SET qty = guard_fixture.qty + EXCLUDED.qty`,
			want: outcomeDrift,
			wantInReport: []string{
				codeNoUniqueForConflict,
				"guard_fixture_ab_uniq", // the report lists the existing key
				"(a, b)",                // with its columns, in order
				"EVERY execution",       // and why this is not a rare merge case
				"ON CONFLICT",           // what to grep after a key change
			},
		},
		{
			name:         "column name the table does not have",
			table:        "guard_fixture",
			sql:          `INSERT INTO guard_fixture (a, b, nope) VALUES ($1, $2, $3) ON CONFLICT (a, b) DO NOTHING`,
			want:         outcomeDrift,
			wantInReport: []string{codeUndefinedColumn, "does not have"},
		},
		{
			// The other legal target form. It breaks the same way and used to be
			// swallowed: 42704 fell through to the "not a schema-drift code"
			// branch, which only logged, so a renamed constraint left the test
			// green while every execution of the statement failed.
			name:  "ON CONSTRAINT naming a constraint that is gone",
			table: "guard_fixture",
			sql: `INSERT INTO guard_fixture (a, b, qty) VALUES ($1, $2, $3)
ON CONFLICT ON CONSTRAINT guard_fixture_ab_uniq_renamed_by_a_later_migration DO UPDATE SET qty = 1`,
			want:         outcomeDrift,
			wantInReport: []string{codeUndefinedObject, "renamed or dropped", "unique index"},
		},
		{
			// guard_fixture_ab_uniq is a unique *index*, and ON CONSTRAINT does
			// not accept index names — same 42704, and the reason the report
			// mentions it.
			name:  "ON CONSTRAINT naming a unique index",
			table: "guard_fixture",
			sql: `INSERT INTO guard_fixture (a, b, qty) VALUES ($1, $2, $3)
ON CONFLICT ON CONSTRAINT guard_fixture_ab_uniq DO UPDATE SET qty = 1`,
			want:         outcomeDrift,
			wantInReport: []string{codeUndefinedObject, "unique index"},
		},
		{
			// A name that does not resolve must say so instead of listing zero
			// keys: "no UNIQUE or PRIMARY KEY at all" would send the reader
			// hunting for a missing key when the real cause can be a mis-parsed
			// INSERT target.
			name:         "table the schema does not have",
			table:        "guard_absent",
			sql:          `INSERT INTO guard_absent (a) VALUES ($1) ON CONFLICT (a) DO NOTHING`,
			want:         outcomeDrift,
			wantInReport: []string{codeUndefinedTable, "does not resolve to a table"},
		},
		{
			// Scenario (a) of a silent skip: an upsert assembled with fmt.Sprintf
			// keeps its verbs in the literal, so the planner never sees a valid
			// statement.
			name:         "literal still holding a Sprintf verb",
			table:        "guard_fixture",
			sql:          `INSERT INTO guard_fixture (a, b, qty) VALUES ($1, $2, $3) ON CONFLICT (%s) DO NOTHING`,
			want:         outcomeUnplannable,
			wantInReport: []string{"UNCHECKED", "SQLSTATE 42601", "one single statement"},
		},
		{
			name:  "literal holding more than one statement",
			table: "guard_fixture",
			sql: `INSERT INTO guard_fixture (a, b, qty) VALUES ($1, $2, $3) ON CONFLICT (a, b) DO NOTHING;
INSERT INTO guard_fixture (a, b, qty) VALUES (7, 7, 7)`,
			want:         outcomeUnplannable,
			wantInReport: []string{"UNCHECKED", "more than one statement"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := schemaguard.Upsert{
				SQL: tc.sql, Table: tc.table, File: "internal/fake/repo.go", Line: 42,
			}

			outcome, report := checkUpsert(ctx, tx, up)
			assert.Equal(t, int(tc.want), int(outcome), "classification")

			if tc.want == outcomePlanned {
				assert.Empty(t, report, "a clean plan says nothing")
				return
			}

			// The loop only fails on a non-empty report, so every outcome other
			// than a clean plan has to produce one.
			require.NotEmpty(t, report, "a non-planned outcome must explain itself")
			assert.Contains(t, report, "internal/fake/repo.go:42", "report points at the literal")
			for _, want := range tc.wantInReport {
				assert.Contains(t, report, want)
			}
		})
	}

	// The multi-statement case ends in an INSERT that Postgres executes when the
	// literal is sent as one simple query (verified on PG16: the tail wrote a
	// row). checkUpsert refuses to send it, and planStatement's savepoint is the
	// backstop behind that; either way the fixture table must be untouched.
	var rows int
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM guard_fixture`).Scan(&rows))
	assert.Zero(t, rows, "the guard must not execute what it plans or rejects")
}

// checkUpsert classifies one literal against the live schema. The report is
// empty only for a clean plan: the caller fails the test with whatever it gets,
// so every other outcome has to explain what to fix.
func checkUpsert(ctx context.Context, tx pgx.Tx, up schemaguard.Upsert) (planOutcome, string) {
	if holdsSeveralStatements(up.SQL) {
		return outcomeUnplannable, uncheckedReport(up,
			"the literal holds more than one statement (an inner ';'). EXPLAIN covers only the "+
				"first one, and an argument-less query goes to Postgres as a simple query, so "+
				"the tail would be EXECUTED rather than planned (verified on PG16: the trailing "+
				"INSERT wrote a row). The guard refuses to send it.")
	}

	planErr := planStatement(ctx, tx, up.SQL)

	var pgErr *pgconn.PgError
	switch {
	case planErr == nil:
		return outcomePlanned, ""
	case errors.As(planErr, &pgErr) && isSchemaDrift(pgErr.Code):
		return outcomeDrift, driftReport(ctx, tx, up, pgErr)
	default:
		return outcomeUnplannable, uncheckedReport(up, planErr.Error())
	}
}

// holdsSeveralStatements reports whether sql carries a statement separator other
// than a single trailing one.
func holdsSeveralStatements(sql string) bool {
	return strings.Contains(strings.TrimSuffix(strings.TrimSpace(sql), ";"), ";")
}

// subtestName keeps '/' out of the name: go test reads it as a subtest
// separator, so a failing literal could not be re-run with -run.
func subtestName(up schemaguard.Upsert) string {
	return fmt.Sprintf("%s:%d", strings.ReplaceAll(up.File, "/", "_"), up.Line)
}

// planStatement plans sql without running it.
//
// EXPLAIN (GENERIC_PLAN) is the only check that reaches the planner without
// parameter values, and the planner is where it matters: PREPARE stops after
// parse+analyze, before arbiter-index inference, so it accepts the exact stale
// target that broke every container pickup in TASK-151 (verified on PG 16.12).
//
// Planning writes nothing only for a single statement: EXPLAIN applies to the
// first statement of the string, and pgx sends an argument-less query over the
// simple protocol, so Postgres would execute whatever follows a ';'. checkUpsert
// rejects such literals before they reach here — the savepoint below and the
// caller's rolled-back transaction are the second line of defence, not the first.
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
	case codeNoUniqueForConflict, codeUndefinedColumn, codeUndefinedTable, codeUndefinedObject:
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

// uncheckedReport explains a literal that never reached the planner. It is a
// failure and not a note: the target stays unverified, which is the state that
// let TASK-151 live in production.
func uncheckedReport(up schemaguard.Upsert, reason string) string {
	var out strings.Builder

	out.WriteString("\nUNCHECKED ON CONFLICT target: the literal never reached the planner, so " +
		"nothing about its target was verified\n")
	fmt.Fprintf(&out, "  literal:  %s:%d\n", up.File, up.Line)
	fmt.Fprintf(&out, "  table:    %s\n", up.Table)
	fmt.Fprintf(&out, "  reason:   %s\n", reason)
	out.WriteString("  fix:      make the literal plannable: one single statement of static text with\n")
	out.WriteString("            $N placeholders (keep the SQL a plain const -- a query assembled with\n")
	out.WriteString("            fmt.Sprintf leaves its verbs in the literal and cannot be planned).\n")
	out.WriteString("            If this really is a new, legitimate statement shape, widen the\n")
	out.WriteString("            classification in this test deliberately -- but do not leave the\n")
	out.WriteString("            target unchecked, and do not lower minUpserts to get past the bound.\n")
	fmt.Fprintf(&out, "  sql:      %s\n", strings.TrimSpace(up.SQL))

	return out.String()
}

func driftHeadline(code, table string) string {
	switch code {
	case codeNoUniqueForConflict:
		return fmt.Sprintf("ON CONFLICT target matches no UNIQUE/PK key of table %q -- Postgres resolves "+
			"the arbiter index at plan time, so EVERY execution of this statement fails, not only "+
			"the conflicting ones", table)
	case codeUndefinedObject:
		return fmt.Sprintf("ON CONFLICT ON CONSTRAINT names a constraint that table %q does not have -- "+
			"the constraint was renamed or dropped by a migration, or the key is a unique index "+
			"rather than a constraint (ON CONSTRAINT does not accept index names -- target the "+
			"columns instead). Resolved at plan time like 42P10, so EVERY execution of this "+
			"statement fails", table)
	case codeUndefinedColumn:
		return fmt.Sprintf("SQL literal names a column that table %q does not have", table)
	case codeUndefinedTable:
		return fmt.Sprintf("SQL literal targets table %q, which the schema does not have", table)
	default:
		return fmt.Sprintf("SQL literal does not match the schema of table %q", table)
	}
}

func uniqueKeyListing(ctx context.Context, tx pgx.Tx, table string) string {
	// A name that does not resolve is reported as such: listing zero keys would
	// read as "this table has no unique key", when the cause can just as well be
	// a table name the extractor parsed wrongly.
	var resolved *string
	if err := tx.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&resolved); err != nil {
		return fmt.Sprintf("  keys:     (could not resolve %q: %v)\n", table, err)
	}
	if resolved == nil {
		return fmt.Sprintf("  keys:     (%q does not resolve to a table -- either the schema has no "+
			"such table, or schemaguard mis-parsed the INSERT INTO target and this name is wrong)\n",
			table)
	}

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
