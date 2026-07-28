// Package schemaguard extracts the upsert SQL literals from the repository's
// own Go sources so a test can hand each one to Postgres for planning.
//
// Why this exists: Postgres resolves an ON CONFLICT target to a concrete
// unique index at *plan* time, so a target that no longer matches any
// UNIQUE/PK key breaks every execution of the statement, not just the ones
// that actually conflict. TASK-151 was exactly that — migration 0050 replaced
// the cargo UNIQUE key while containers.Pickup kept targeting the dropped
// three-column key, so every container pickup failed with 42P10, and the
// compiler could not see the drift because migration and query live in
// different files.
//
// The extraction walks the AST rather than grepping text on purpose:
// internal/persistence/containers/repo.go carries prose about its ON CONFLICT
// target right next to the SQL constants, and a line-based scan would splice
// the comment onto a neighbouring INSERT INTO. With parser mode 0 comments are
// not part of the AST at all, so prose can never be mistaken for a statement.
//
// Known limitation: only whole statements written as a single string literal are
// found. A statement spliced together from parts ("INSERT INTO t ..." +
// onConflictClause) carries half the pattern in each literal and is invisible to
// the guard. There is no such upsert in the tree today; keep upsert SQL in one
// literal so it stays covered.
package schemaguard

// Upsert is one INSERT ... ON CONFLICT string literal found in the sources.
type Upsert struct {
	// SQL is the literal's unquoted content, ready to be planned.
	SQL string
	// Table is the target of the INSERT INTO.
	Table string
	// File is the source path relative to the module root, slash-separated.
	File string
	// Line is where the literal starts in File.
	Line int
}
