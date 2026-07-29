package balance_test

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database/testdb"
)

// balancePath is the catalog the application actually loads (cfg.Balance.Path
// defaults to it), relative to this package — the same way stationtype_test.go
// reaches it.
const balancePath = "../../configs/balance.yaml"

// TestIntegration_BalanceCatalog_MatchesGoodsTypesTable pins the goods catalog
// to ONE shape across the two places it is read from (TASK-167).
//
// The client labels every hold row, market row, auction lot and trade-scanner
// entry from GET /api/goods, which serves configs/balance.yaml. The server sizes
// the same goods against goods_types in Postgres. Nothing connected the two, so
// they drifted: migrations 0017/0018 invented goods 50 'Missile' / 51 'Combat
// Drone' in the table alone, the launch handlers spent them, and the client — which
// had never heard of those ids — fell back to «Товар #50». space drifted with the
// name: the table said 2 for good 51 while the catalog said nothing at all, so the
// client sized a drone as free.
//
// Hence all three columns, not just the id set: an id-only check would have passed
// happily while a hold row was labelled and sized wrong.
func TestIntegration_BalanceCatalog_MatchesGoodsTypesTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)

	cat, err := balance.LoadFromFile(balancePath)
	require.NoError(t, err, "load %s", balancePath)

	type row struct {
		name  string
		space float64
	}

	table := make(map[domain.GoodsTypeID]row)
	rows, err := pool.Query(ctx, "SELECT id, name, space FROM goods_types")
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var (
			id domain.GoodsTypeID
			r  row
		)
		require.NoError(t, rows.Scan(&id, &r.name, &r.space))
		table[id] = r
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, table, "goods_types is seeded by the migrations")

	file := make(map[domain.GoodsTypeID]row, len(cat.AllGoods()))
	for _, g := range cat.AllGoods() {
		file[g.ID] = row{name: g.Name, space: float64(g.Space)}
	}

	// Collected rather than fail-fast: a drift is usually a whole batch of goods
	// (a migration that seeded a set, a regenerated YAML), and seeing one id at a
	// time turns one fix into ten runs.
	var missingFromFile, missingFromTable []string
	var mismatched []string
	for _, id := range sortedIDs(table) {
		f, ok := file[id]
		if !ok {
			missingFromFile = append(missingFromFile, fmt.Sprintf("%d %q", id, table[id].name))
			continue
		}
		tbl := table[id]
		if f.name != tbl.name {
			mismatched = append(mismatched, fmt.Sprintf("%d name: table %q != balance.yaml %q", id, tbl.name, f.name))
		}
		if f.space != tbl.space {
			mismatched = append(mismatched, fmt.Sprintf("%d space: table %v != balance.yaml %v", id, tbl.space, f.space))
		}
	}
	for _, id := range sortedIDs(file) {
		if _, ok := table[id]; !ok {
			missingFromTable = append(missingFromTable, fmt.Sprintf("%d %q", id, file[id].name))
		}
	}

	assert.Empty(t, missingFromFile,
		"goods_types rows absent from configs/balance.yaml: the client cannot label or size them "+
			"(GET /api/goods serves the YAML). Add them to the catalog, or drop the rows.")
	assert.Empty(t, missingFromTable,
		"configs/balance.yaml goods absent from goods_types: cargo.goods_type_id has an FK to that "+
			"table, so nothing can ever be held or traded. Seed them in a migration.")
	assert.Empty(t, mismatched,
		"same id, different name/space in goods_types and configs/balance.yaml: the client labels and "+
			"sizes from the YAML while the server sizes from the table, so the two disagree in front of the player.")
}

func sortedIDs[V any](m map[domain.GoodsTypeID]V) []domain.GoodsTypeID {
	ids := make([]domain.GoodsTypeID, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
