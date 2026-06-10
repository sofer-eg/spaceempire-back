// Command gen-trade-markets generates the universal trade-station / pirbase
// market seed (migration 0044, phase 10.19 follow-up).
//
// In the original StarWind a trade station (and a pirbase) ran a UNIVERSAL
// market: its grid listed every commodity good in the universe, the player
// could sell any good to it, and buy any good it currently had in stock. This
// tool reproduces that: every built trade_station (owner_kind 4) and pirbase
// (owner_kind 5) gets a station_goods row for every commodity good
// (object_type_id 10 with a real avg_price), with BOTH a buy and a sell price
// so the player can always sell and can buy whatever is in stock.
//
// Prices are FLAT — they do not vary with the on-hand quantity (parity with
// the original `Trade` SP; the dynamic avg↔max pricing from phase 10.18 stays
// exclusive to production factories). Per the original formulas the player
// buys at ~0.6·avg and sells at ~0.5·avg for ordinary goods, and at ~1.21·avg
// / ~1.1·avg for the premium high-tech list. Pirbases additionally trade
// slaves (good 323) at a fixed price.
//
// It reads one source — configs/balance.yaml (the goods catalog: id +
// avg_price + object_type_id) — and emits a migration that CROSS JOINs the
// per-good price table with the trade_stations / pirbases tables, so station
// ids never need enumerating here. One-shot tool — rerun when balance changes.
//
// Usage:
//
//	go run ./cmd/starwind-tools/gen-trade-markets \
//	    -balance configs/balance.yaml \
//	    -out migrations/0044_trade_station_universal_market.sql
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"spaceempire/back/internal/balance"
)

const (
	commodityObjectType = 10        // object_type_id of tradeable wares
	initialStock        = 500       // seeded stock so every good is buyable at once
	maxStock            = 1_000_000 // effectively uncapped so selling never overflows
	slavesGood          = 323       // pirbase-only special good
	slavesBuyPrice      = 800       // pirbase buys slaves from the player
	slavesSellPrice     = 1000      // pirbase sells slaves to the player
	slavesMaxStock      = 1_000_000_000
)

// premium is the original Trade SP's high-tech ware list: these trade above
// average (the player buys at ~1.21·avg, sells at ~1.1·avg) instead of below.
var premium = map[int]bool{
	5: true, 10: true, 11: true, 12: true, 13: true, 14: true,
	20: true, 21: true, 22: true, 23: true, 24: true, 25: true, 26: true, 27: true,
}

// ceilDiv is integer ceil(a/b), matching the original SP's CEILING(avg/n).
func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

// sellToPlayer is what the station charges the player (station_goods.sell_price,
// the original tbuy price_sale): premium avg+10%+10%, else avg/2+10%.
func sellToPlayer(avg int64, prem bool) int64 {
	if prem {
		base := avg + ceilDiv(avg, 10)
		return base + ceilDiv(base, 10)
	}
	return ceilDiv(avg, 2) + ceilDiv(avg, 10)
}

// buyFromPlayer is what the station pays the player (station_goods.buy_price,
// the original tsell price_buy): premium avg+10%, else avg/2. Always below
// sellToPlayer, so there is no risk-free arbitrage at one station.
func buyFromPlayer(avg int64, prem bool) int64 {
	if prem {
		return avg + ceilDiv(avg, 10)
	}
	return ceilDiv(avg, 2)
}

func main() {
	balanceFile := flag.String("balance", "configs/balance.yaml", "path to goods catalog (balance.yaml)")
	out := flag.String("out", "migrations/0044_trade_station_universal_market.sql", "output migration path")
	flag.Parse()
	if err := run(*balanceFile, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// priceRow is one good's flat buy/sell price for the universal market.
type priceRow struct {
	id   int64
	buy  int64
	sell int64
}

func run(balanceFile, outPath string) error {
	bal, err := balance.LoadFromFile(balanceFile)
	if err != nil {
		return fmt.Errorf("load balance: %w", err)
	}

	var rows []priceRow
	for _, g := range bal.AllGoods() {
		if g.ObjectTypeID != commodityObjectType || g.AvgPrice <= 0 || int(g.ID) == slavesGood {
			continue
		}
		prem := premium[int(g.ID)]
		rows = append(rows, priceRow{
			id:   int64(g.ID),
			buy:  buyFromPlayer(g.AvgPrice, prem),
			sell: sellToPlayer(g.AvgPrice, prem),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	if len(rows) == 0 {
		return fmt.Errorf("no commodity goods (object_type_id=%d, avg_price>0) found in %s", commodityObjectType, balanceFile)
	}

	sql := render(rows)
	if err := os.WriteFile(outPath, []byte(sql), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	fmt.Fprintf(os.Stderr, "wrote universal market for %d commodity goods (× every built trade station + pirbase) to %s\n", len(rows), outPath)
	return nil
}

func render(rows []priceRow) string {
	var b strings.Builder
	b.WriteString("-- +goose Up\n-- +goose StatementBegin\n")
	b.WriteString("-- Universal trade-station / pirbase market (phase 10.19 follow-up),\n")
	b.WriteString("-- generated by cmd/starwind-tools/gen-trade-markets. Every built trade\n")
	b.WriteString("-- station (owner_kind 4) and pirbase (owner_kind 5) trades every commodity\n")
	b.WriteString("-- good: it buys any good from the player (buy_price) and sells any good it\n")
	b.WriteString("-- has in stock (sell_price). Prices are FLAT — parity with the original\n")
	b.WriteString("-- StarWind Trade SP (the dynamic avg<->max pricing of phase 10.18 stays\n")
	b.WriteString("-- exclusive to production factories). Replaces the narrow 0036/0043 trade-\n")
	b.WriteString("-- station seed and the 0027/0004 pirbase seed. Do not edit — rerun the tool.\n\n")

	b.WriteString("DELETE FROM station_goods WHERE owner_kind IN (4, 5);\n\n")

	b.WriteString("INSERT INTO station_goods (owner_kind, owner_id, goods_type_id, buy_price, sell_price, stock, max_stock)\n")
	fmt.Fprintf(&b, "SELECT o.owner_kind, o.owner_id, g.gid, g.buy_price, g.sell_price, %d, %d\n", initialStock, maxStock)
	b.WriteString("FROM (\n")
	b.WriteString("    SELECT 4 AS owner_kind, id AS owner_id FROM trade_stations WHERE built\n")
	b.WriteString("    UNION ALL\n")
	b.WriteString("    SELECT 5 AS owner_kind, id AS owner_id FROM pirbases WHERE built\n")
	b.WriteString(") o\n")
	b.WriteString("CROSS JOIN (VALUES\n")
	for i, r := range rows {
		sep := ","
		if i == len(rows)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "    (%d, %d, %d)%s\n", r.id, r.buy, r.sell, sep)
	}
	b.WriteString(") AS g(gid, buy_price, sell_price);\n\n")

	b.WriteString("-- Pirbases also trade slaves (323): buy from the player, sell back at a\n")
	b.WriteString("-- markup. Start with none in stock — buyable only once some are sold here.\n")
	b.WriteString("INSERT INTO station_goods (owner_kind, owner_id, goods_type_id, buy_price, sell_price, stock, max_stock)\n")
	fmt.Fprintf(&b, "SELECT 5, pb.id, %d, %d, %d, 0, %d\n", slavesGood, slavesBuyPrice, slavesSellPrice, slavesMaxStock)
	b.WriteString("FROM pirbases pb WHERE pb.built;\n")

	b.WriteString("-- +goose StatementEnd\n\n")

	b.WriteString("-- +goose Down\n-- +goose StatementBegin\n")
	b.WriteString("-- Irreversible pre-release reseed: drops every trade-station / pirbase\n")
	b.WriteString("-- market (the narrow 0036/0043/0027 seeds are not restored).\n")
	b.WriteString("DELETE FROM station_goods WHERE owner_kind IN (4, 5);\n")
	b.WriteString("-- +goose StatementEnd\n")
	return b.String()
}
