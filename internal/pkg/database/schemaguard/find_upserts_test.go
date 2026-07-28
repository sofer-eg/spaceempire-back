package schemaguard_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/pkg/database/schemaguard"
)

func writeGoFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

func TestUnit_FindUpserts_RawLiteral(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeGoFile(t, dir, "repo.go", `package repo

const addSQL = `+"`"+`
INSERT INTO cargo (owner_kind, owner_id, goods_type_id, quantity)
VALUES ($1, $2, $3, $4)
ON CONFLICT (owner_kind, owner_id, goods_type_id)
DO UPDATE SET quantity = cargo.quantity + EXCLUDED.quantity
`+"`"+`
`)

	got, err := schemaguard.FindUpserts(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "cargo", got[0].Table)
	assert.Equal(t, "repo.go", got[0].File)
	assert.Equal(t, 3, got[0].Line, "line of the literal's opening backtick")
	assert.Contains(t, got[0].SQL, "ON CONFLICT (owner_kind, owner_id, goods_type_id)")
}

// TestUnit_FindUpserts_IgnoresComments pins the reason the extractor walks the
// AST instead of grepping text: internal/persistence/containers/repo.go carries
// prose about the ON CONFLICT target next to an INSERT literal, and a text scan
// would splice the two into a phantom upsert.
func TestUnit_FindUpserts_IgnoresComments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeGoFile(t, dir, "repo.go", `package repo

// The ON CONFLICT target must name all four columns of
// cargo_owner_goods_uniq (migration 0050).
const insertSQL = `+"`"+`
INSERT INTO cargo (owner_kind, owner_id) VALUES ($1, $2)
`+"`"+`

/* ON CONFLICT in a block comment is not a statement either. */
`)

	got, err := schemaguard.FindUpserts(dir)
	require.NoError(t, err)
	assert.Empty(t, got, "comments mentioning ON CONFLICT must not produce upserts")
}

// TestUnit_FindUpserts_TableNameForms covers the ways a real INSERT can spell
// its target. Both non-bare forms used to be handled wrongly: a quoted name did
// not match at all, so the literal dropped out of the guard's coverage without
// changing any count, and a schema-qualified name yielded the *schema* as the
// table, which makes the drift report list the keys of a relation that does not
// exist ("the table has no UNIQUE or PRIMARY KEY at all") and sends the reader
// hunting for a problem that is not there.
func TestUnit_FindUpserts_TableNameForms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		table string
		want  string
	}{
		{name: "bare", table: `cargo`, want: `cargo`},
		{name: "quoted reserved word", table: `"user"`, want: `"user"`},
		{name: "schema qualified", table: `public.cargo`, want: `public.cargo`},
		{name: "schema qualified and quoted", table: `public."user"`, want: `public."user"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			writeGoFile(t, dir, "repo.go", `package repo

const q = `+"`"+`INSERT INTO `+tc.table+` (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`+"`"+`
`)

			got, err := schemaguard.FindUpserts(dir)
			require.NoError(t, err, "table %s", tc.table)
			require.Len(t, got, 1, "literal must be found for table %s", tc.table)
			// The captured name goes straight into to_regclass(), which accepts
			// quoted and schema-qualified names as written.
			assert.Equal(t, tc.want, got[0].Table)
		})
	}
}

func TestUnit_FindUpserts_SkipsTestFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeGoFile(t, dir, "repo_test.go", `package repo_test

const seedSQL = `+"`"+`
INSERT INTO cargo (owner_kind) VALUES ($1) ON CONFLICT (owner_kind) DO NOTHING
`+"`"+`
`)

	got, err := schemaguard.FindUpserts(dir)
	require.NoError(t, err)
	assert.Empty(t, got, "_test.go files hold fixtures, not production statements")
}

func TestUnit_FindUpserts_CaseAndNewlineInsensitive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeGoFile(t, dir, "repo.go", `package repo

const q = "insert\n   into  player_quests (player_id) values ($1) on conflict (player_id) do nothing"
`)

	got, err := schemaguard.FindUpserts(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "player_quests", got[0].Table)
}

func TestUnit_FindUpserts_MultipleLiteralsInOneFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeGoFile(t, dir, "nested/repo.go", `package repo

const (
	aSQL = `+"`"+`INSERT INTO rents (payer_id) VALUES ($1) ON CONFLICT (station_id) DO NOTHING`+"`"+`
	bSQL = `+"`"+`INSERT INTO ai_state (ship_id) VALUES ($1) ON CONFLICT (ship_id) DO UPDATE SET ship_id = 1`+"`"+`
)

func q() string {
	return `+"`"+`INSERT INTO relations (from_id) VALUES ($1) ON CONFLICT (from_id) DO NOTHING`+"`"+`
}
`)

	got, err := schemaguard.FindUpserts(dir)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"rents", "ai_state", "relations"},
		[]string{got[0].Table, got[1].Table, got[2].Table}, "sorted by line")
	for _, up := range got {
		assert.Equal(t, "nested/repo.go", up.File, "path is repo-relative with forward slashes")
	}
}

func TestUnit_FindUpserts_IgnoresNonUpsertStatements(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeGoFile(t, dir, "repo.go", `package repo

const (
	selectSQL = `+"`"+`SELECT id FROM cargo WHERE owner_id = $1`+"`"+`
	updateSQL = `+"`"+`UPDATE cargo SET quantity = $1 WHERE id = $2`+"`"+`
	plainSQL  = `+"`"+`INSERT INTO cargo (owner_id) VALUES ($1)`+"`"+`
	name      = "on conflict"
)
`)

	got, err := schemaguard.FindUpserts(dir)
	require.NoError(t, err)
	assert.Empty(t, got, "only literals carrying both INSERT INTO and ON CONFLICT count")
}

// TestUnit_FindUpserts_ProductionUpserts runs the extractor over the real
// repository: it is the fast, docker-free half of the guard, and it pins the
// nine production upserts across seven tables so a broken extractor (or a
// relocated upsert) shows up without waiting for a container.
func TestUnit_FindUpserts_ProductionUpserts(t *testing.T) {
	t.Parallel()

	root, err := schemaguard.RepoRoot()
	require.NoError(t, err)

	got, err := schemaguard.FindUpserts(root)
	require.NoError(t, err)

	// file -> tables, one entry per upsert literal, in line order.
	want := map[string][]string{
		"internal/persistence/aistate/repo.go":       {"ai_state"},
		"internal/persistence/cargo/repo.go":         {"cargo"},
		"internal/persistence/containers/repo.go":    {"cargo"},
		"internal/persistence/quests/counters.go":    {"player_quest_counters"},
		"internal/persistence/quests/repo.go":        {"player_quests", "player_quests"},
		"internal/persistence/rents/repo.go":         {"rents"},
		"internal/social/racestanding/repository.go": {"player_race_standing"},
		"internal/social/relations/repository.go":    {"relations"},
	}

	byFile := map[string][]string{}
	for _, up := range got {
		byFile[up.File] = append(byFile[up.File], up.Table)
		assert.NotEmpty(t, up.SQL)
		assert.Positive(t, up.Line)
		// The generators under cmd/starwind-tools are scanned like everything else
		// (nothing is excluded by path), and they must stay out of the result on
		// their own merit: they build ON CONFLICT clauses with strings.Builder, so
		// no single literal there carries both halves of the pattern.
		assert.NotContains(t, up.File, "cmd/starwind-tools",
			"SQL generators assemble ON CONFLICT as text fragments, not statements")
	}

	for file, tables := range want {
		assert.Equal(t, tables, byFile[file],
			"upserts in %s no longer match this map. If you deliberately added, moved or "+
				"removed an INSERT ... ON CONFLICT there, update the map -- it is the "+
				"diff-visible record of what the guard covers. If you did not touch that "+
				"file, the extractor regressed: it stopped seeing a literal it used to see.",
			file)
	}
	assert.GreaterOrEqual(t, len(got), minUpserts, "production upserts found: %v", byFile)
}

func TestUnit_RepoRoot_FindsModuleRoot(t *testing.T) {
	t.Parallel()

	root, err := schemaguard.RepoRoot()
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(root, "go.mod"))
	assert.DirExists(t, filepath.Join(root, "migrations"))
}
