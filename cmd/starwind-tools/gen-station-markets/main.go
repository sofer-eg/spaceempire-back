// Command gen-station-markets generates the full station market seed
// (migration 0042) so every built production station (kind 2) trades,
// not just the five race capitals seeded in 0036 (phase 10.18).
//
// It reads three sources:
//   - configs/balance.yaml      — the goods catalog (id/name/space) used to
//     widen goods_types past the partial 0006 seed, plus avg_price as the
//     stored price column (the live price is computed dynamically at trade
//     time from stock fill, so the column is a tradeable-direction flag).
//   - starwind/sql/db.sql       — station_goods_types: which goods each
//     station TYPE consumes (raw, type-flag 0/-1) or produces (product,
//     type-flag 1) per production cycle (cycle_type 1) and the per-good cap.
//   - migrations/0036_*.sql     — the authoritative list of placed stations
//     (id, type, built) so we only seed markets for stations that exist.
//
// For each built kind-2 station we emit one station_goods row per cycle_type=1
// good of its type: inputs become buy rows, the output becomes a sell row,
// stock starts at half the cap (so both buying and selling work immediately),
// and max_stock is the legacy goods_max_count. One-shot tool — rerun when the
// dump, balance, or station placement changes.
//
// Usage:
//
//	go run ./cmd/starwind-tools/gen-station-markets \
//	    -sql /path/to/starwind/sql/db.sql \
//	    -balance configs/balance.yaml \
//	    -stations migrations/0036_starwind_stations.sql \
//	    -out migrations/0042_station_markets.sql
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

// marketGood is one cycle_type=1 entry of a station type: a good the type
// trades, whether it is bought (input) or sold (output), and its cap.
type marketGood struct {
	good   int
	maxCap int
	output bool // type-flag 1 = finished product (sell); else raw/consumed (buy)
}

// placedStation is one row of the 0036 stations seed.
type placedStation struct {
	id    int
	typ   int
	built bool
}

func main() {
	sqlFile := flag.String("sql", "", "path to starwind/sql/db.sql")
	balanceFile := flag.String("balance", "configs/balance.yaml", "path to goods catalog (balance.yaml)")
	stationsFile := flag.String("stations", "migrations/0036_starwind_stations.sql", "path to the stations placement migration")
	out := flag.String("out", "migrations/0042_station_markets.sql", "output market migration path")
	flag.Parse()
	if *sqlFile == "" {
		fmt.Fprintln(os.Stderr, "-sql is required")
		os.Exit(2)
	}
	if err := run(*sqlFile, *balanceFile, *stationsFile, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(sqlFile, balanceFile, stationsFile, outPath string) error {
	bal, err := balance.LoadFromFile(balanceFile)
	if err != nil {
		return fmt.Errorf("load balance: %w", err)
	}

	dump, err := os.ReadFile(sqlFile)
	if err != nil {
		return fmt.Errorf("read sql: %w", err)
	}
	byType, err := parseStationGoodsTypes(dump)
	if err != nil {
		return fmt.Errorf("parse station_goods_types: %w", err)
	}

	stationsDump, err := os.ReadFile(stationsFile)
	if err != nil {
		return fmt.Errorf("read stations migration: %w", err)
	}
	stations, err := parsePlacedStations(stationsDump)
	if err != nil {
		return fmt.Errorf("parse stations migration: %w", err)
	}

	migration, rows := render(bal, byType, stations)
	if err := os.WriteFile(outPath, []byte(migration), 0o644); err != nil {
		return fmt.Errorf("write migration: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %d station_goods rows for production stations to %s\n", rows, outPath)
	return nil
}

// parseStationGoodsTypes returns the cycle_type=1 market goods keyed by
// station type. Columns: station_type_id, goods_type_id, cycle_type,
// cycle_time, goods_count_cycle, goods_max_count, type.
func parseStationGoodsTypes(dump []byte) (map[int][]marketGood, error) {
	rows, err := tuples(dump, "station_goods_types")
	if err != nil {
		return nil, err
	}
	out := make(map[int][]marketGood)
	for _, t := range rows {
		f := splitFields(t)
		if len(f) != 7 {
			return nil, fmt.Errorf("station_goods_types: expected 7 fields, got %d in %q", len(f), t)
		}
		cycleType, err := strconv.Atoi(f[2])
		if err != nil {
			return nil, fmt.Errorf("station_goods_types cycle_type %q: %w", f[2], err)
		}
		if cycleType != 1 { // 0=construction, 2=rebuild — not the production market
			continue
		}
		typ, err := strconv.Atoi(f[0])
		if err != nil {
			return nil, fmt.Errorf("station_goods_types type_id %q: %w", f[0], err)
		}
		good, err := strconv.Atoi(f[1])
		if err != nil {
			return nil, fmt.Errorf("station_goods_types good %q: %w", f[1], err)
		}
		maxCap, err := strconv.Atoi(f[5])
		if err != nil {
			return nil, fmt.Errorf("station_goods_types max_count %q: %w", f[5], err)
		}
		flag, err := strconv.Atoi(f[6])
		if err != nil {
			return nil, fmt.Errorf("station_goods_types type-flag %q: %w", f[6], err)
		}
		out[typ] = append(out[typ], marketGood{good: good, maxCap: maxCap, output: flag == 1})
	}
	return out, nil
}

// parsePlacedStations extracts (id, type, built) from the Postgres
// "INSERT INTO stations (id, type, sector_id, pos_x, pos_y, race, built)
// VALUES ..." block of the placement migration.
func parsePlacedStations(dump []byte) ([]placedStation, error) {
	marker := []byte("INSERT INTO stations (id, type, sector_id, pos_x, pos_y, race, built) VALUES")
	idx := bytes.Index(dump, marker)
	if idx < 0 {
		return nil, fmt.Errorf("marker %q not found", marker)
	}
	rest := dump[idx+len(marker):]
	end := bytes.IndexByte(rest, ';')
	if end < 0 {
		return nil, fmt.Errorf("no statement terminator after stations insert")
	}
	rows, err := splitTopLevelTuples(strings.TrimSpace(string(rest[:end])))
	if err != nil {
		return nil, err
	}
	var out []placedStation
	for _, t := range rows {
		f := splitFields(t)
		if len(f) != 7 {
			return nil, fmt.Errorf("stations: expected 7 fields, got %d in %q", len(f), t)
		}
		id, err := strconv.Atoi(f[0])
		if err != nil {
			return nil, fmt.Errorf("stations id %q: %w", f[0], err)
		}
		typ, err := strconv.Atoi(f[1])
		if err != nil {
			return nil, fmt.Errorf("stations type %q: %w", f[1], err)
		}
		out = append(out, placedStation{id: id, typ: typ, built: f[6] == "true"})
	}
	return out, nil
}

func render(bal *balance.Balance, byType map[int][]marketGood, stations []placedStation) (string, int) {
	var b strings.Builder
	b.WriteString("-- +goose Up\n-- +goose StatementBegin\n")
	b.WriteString("-- Full production-station market (phase 10.18), generated by\n")
	b.WriteString("-- cmd/starwind-tools/gen-station-markets. Widens goods_types to the full\n")
	b.WriteString("-- StarWind catalog (the 0006 seed is a subset) and seeds station_goods for\n")
	b.WriteString("-- every built production station (owner_kind 2) from its type's legacy\n")
	b.WriteString("-- station_goods_types cycle: inputs are buy rows, the output is a sell row.\n")
	b.WriteString("-- The stored price is avg_price; the live trade price is computed from stock\n")
	b.WriteString("-- fill at runtime. Trade stations (owner_kind 4) and pirbases (5) are left\n")
	b.WriteString("-- to 0036/0027. Do not edit by hand — rerun the generator.\n\n")

	writeGoodsCatalog(&b, bal)

	b.WriteString("DELETE FROM station_goods WHERE owner_kind = 2;\n\n")

	rows := writeMarket(&b, bal, byType, stations)

	b.WriteString("-- +goose StatementEnd\n\n")
	b.WriteString("-- +goose Down\n-- +goose StatementBegin\n")
	b.WriteString("-- Drop the production-station market and restore 0036's capital\n")
	b.WriteString("-- power-plant battery rows. The widened goods_types rows are left in\n")
	b.WriteString("-- place (referenced by cargo/production) — they are harmless reference data.\n")
	b.WriteString("DELETE FROM station_goods WHERE owner_kind = 2;\n")
	b.WriteString("INSERT INTO station_goods (owner_kind, owner_id, goods_type_id, buy_price, sell_price, stock, max_stock) VALUES\n")
	b.WriteString("    (2, 3, 1, NULL, 50, 250, 600),\n")
	b.WriteString("    (2, 84, 1, NULL, 50, 250, 600),\n")
	b.WriteString("    (2, 163, 1, NULL, 50, 250, 600),\n")
	b.WriteString("    (2, 243, 1, NULL, 50, 250, 600),\n")
	b.WriteString("    (2, 323, 1, NULL, 50, 250, 600);\n")
	b.WriteString("-- +goose StatementEnd\n")
	return b.String(), rows
}

// writeGoodsCatalog widens goods_types to the full balance catalog so the
// station_goods/cargo FK holds for every recipe good (food 60-69, products
// 301-322, …) that the partial 0006 seed omitted.
func writeGoodsCatalog(b *strings.Builder, bal *balance.Balance) {
	goods := bal.AllGoods()
	sort.Slice(goods, func(i, j int) bool { return goods[i].ID < goods[j].ID })
	b.WriteString("-- Widen the goods catalog to every StarWind good (the 0006 seed only\n")
	b.WriteString("-- carried the core trade goods). ON CONFLICT keeps existing rows.\n")
	b.WriteString("INSERT INTO goods_types (id, name, space) VALUES\n")
	lines := make([]string, 0, len(goods))
	for _, g := range goods {
		lines = append(lines, fmt.Sprintf("    (%d, %s, %d)", g.ID, sqlString(g.Name), g.Space))
	}
	b.WriteString(strings.Join(lines, ",\n"))
	b.WriteString("\nON CONFLICT (id) DO NOTHING;\n\n")
}

// writeMarket emits the station_goods INSERT for every built kind-2 station
// whose type has a production cycle. Returns the number of rows written.
func writeMarket(b *strings.Builder, bal *balance.Balance, byType map[int][]marketGood, stations []placedStation) int {
	sorted := append([]placedStation(nil), stations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].id < sorted[j].id })

	var lines []string
	for _, st := range sorted {
		if !st.built {
			continue
		}
		goods := byType[st.typ]
		if len(goods) == 0 {
			continue
		}
		sortedGoods := append([]marketGood(nil), goods...)
		sort.Slice(sortedGoods, func(i, j int) bool { return sortedGoods[i].good < sortedGoods[j].good })
		for _, mg := range sortedGoods {
			if mg.maxCap <= 0 { // max_stock must be > 0 (schema CHECK)
				continue
			}
			g, ok := bal.Get(domain.GoodsTypeID(mg.good))
			if !ok { // not in the catalog → would violate the goods_types FK
				fmt.Fprintf(os.Stderr, "skip station %d good %d: absent from balance catalog\n", st.id, mg.good)
				continue
			}
			price := g.AvgPrice
			if price <= 0 { // satisfy CHECK (price > 0); avg is the dynamic floor anyway
				price = 1
			}
			buy, sell := "NULL", strconv.FormatInt(price, 10)
			if !mg.output {
				buy, sell = strconv.FormatInt(price, 10), "NULL"
			}
			lines = append(lines, fmt.Sprintf("    (2, %d, %d, %s, %s, %d, %d)",
				st.id, mg.good, buy, sell, mg.maxCap/2, mg.maxCap))
		}
	}

	b.WriteString("INSERT INTO station_goods (owner_kind, owner_id, goods_type_id, buy_price, sell_price, stock, max_stock) VALUES\n")
	b.WriteString(strings.Join(lines, ",\n"))
	b.WriteString(";\n\n")
	return len(lines)
}

func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// --- shared dump-parsing helpers (same as the other converters) ------------

func tuples(raw []byte, table string) ([]string, error) {
	v, err := sliceInsertValues(raw, table)
	if err != nil {
		return nil, err
	}
	return splitTopLevelTuples(strings.TrimSpace(string(v)))
}

func sliceInsertValues(dump []byte, table string) ([]byte, error) {
	marker := []byte("INSERT INTO `" + table + "` VALUES ")
	idx := bytes.Index(dump, marker)
	if idx < 0 {
		return nil, fmt.Errorf("marker %q not found", marker)
	}
	rest := dump[idx+len(marker):]
	end := bytes.IndexByte(rest, ';')
	if end < 0 {
		return nil, fmt.Errorf("no statement terminator after %q", marker)
	}
	return rest[:end], nil
}

func splitTopLevelTuples(s string) ([]string, error) {
	var out []string
	var buf bytes.Buffer
	depth, inStr := 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					buf.WriteByte(c)
					buf.WriteByte(s[i+1])
					i++
					continue
				}
				inStr = false
			}
			buf.WriteByte(c)
		case c == '\'':
			inStr = true
			buf.WriteByte(c)
		case c == '(':
			depth++
			if depth == 1 {
				buf.Reset()
				continue
			}
			buf.WriteByte(c)
		case c == ')':
			depth--
			if depth == 0 {
				out = append(out, buf.String())
				continue
			}
			buf.WriteByte(c)
		default:
			buf.WriteByte(c)
		}
	}
	if depth != 0 || inStr {
		return nil, fmt.Errorf("unbalanced tuple list")
	}
	return out, nil
}

func splitFields(s string) []string {
	var out []string
	var buf bytes.Buffer
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					buf.WriteByte('\'')
					i++
					continue
				}
				inStr = false
				continue
			}
			buf.WriteByte(c)
		case c == '\'':
			inStr = true
		case c == ',':
			out = append(out, strings.TrimSpace(buf.String()))
			buf.Reset()
		default:
			buf.WriteByte(c)
		}
	}
	out = append(out, strings.TrimSpace(buf.String()))
	return out
}
