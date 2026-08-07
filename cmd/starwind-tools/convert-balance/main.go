// Command convert-balance reads the legacy StarWind cargo definitions from
// includes/types_prod.php and writes them out as configs/balance.yaml in the
// schema consumed by internal/balance. One-shot tool — run it again whenever
// the upstream PHP file changes.
//
// Usage:
//
//	go run ./cmd/starwind-tools/convert-balance \
//	    -php-script /path/to/starwind/includes/types_prod.php \
//	    -out configs/balance.yaml
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"

	"gopkg.in/yaml.v3"
)

// phpEntry mirrors the inner cargo array from types_prod.php. The "id" field
// is the legacy subtype id and is intentionally not copied to YAML — the
// canonical id is the PHP array key (which is what sendToDB writes to
// goods_types.id in MySQL).
type phpEntry struct {
	Name          string `json:"name"`
	MinWarRate    int    `json:"min_war_rate"`
	MinTradeRate  int    `json:"min_trade_rate"`
	MinRaceRate   int    `json:"min_race_rate"`
	AvgPrice      int64  `json:"avg_price"`
	MaxPrice      int64  `json:"max_price"`
	ProductionStd int    `json:"production_std"`
	Space         int    `json:"space"`
	ObjectTypeID  int    `json:"object_type_id"`
}

type phpRow struct {
	Key   int
	Entry phpEntry
}

type yamlGoods struct {
	ID            int    `yaml:"id"`
	Name          string `yaml:"name"`
	MinWarRate    int    `yaml:"min_war_rate"`
	MinTradeRate  int    `yaml:"min_trade_rate"`
	MinRaceRate   int    `yaml:"min_race_rate"`
	AvgPrice      int64  `yaml:"avg_price"`
	MaxPrice      int64  `yaml:"max_price"`
	ProductionStd int    `yaml:"production_std"`
	Space         int    `yaml:"space"`
	ObjectTypeID  int    `yaml:"object_type_id"`
}

type yamlBalance struct {
	GoodsTypes []yamlGoods `yaml:"goods_types"`
}

// excludedGoods are catalog entries deliberately dropped on import, keyed by
// the PHP array key with the source name for grep-ability.
//
// Both are the only two entries of the 80-good catalog whose name is not
// Russian, and both are faithful to the original StarWind data — the label came
// in with the game, the converter did not mangle it. That makes them the same
// defect as TASK-140: a foreign-language label in a Russian interface, visible
// in the hold, on every market, in auction lots and in the trade scanner. The
// customer chose to drop them from the catalog outright rather than translate
// (TASK-177), accepting that the units players already held are gone.
//
// The exclusion belongs here and not in configs/balance.yaml because that file
// is generated: a hand-edit is undone by the next converter run. It is enough to
// do it once, here — gen-trade-markets and gen-station-markets both read the
// generated YAML, so neither can put the goods back on a market either.
var excludedGoods = map[int]string{
	104: "Поцелуй Evening",
	114: `Gold ring with diamonds "charm"`,
}

func main() {
	phpFile := flag.String("php-script", "", "path to starwind/includes/types_prod.php")
	out := flag.String("out", "configs/balance.yaml", "output YAML path")
	flag.Parse()

	if *phpFile == "" {
		fmt.Fprintln(os.Stderr, "-php-script is required")
		os.Exit(2)
	}
	if err := run(*phpFile, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(phpFile, outPath string) error {
	abs, err := filepath.Abs(phpFile)
	if err != nil {
		return fmt.Errorf("resolve php script: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("php script: %w", err)
	}

	script := fmt.Sprintf(`
chdir(dirname(%q));
require %q;
$out = [];
foreach (SC_CargoInfo::$Cargo as $key => $info) {
    $info["name"] = iconv("CP1251", "UTF-8", $info["name"]);
    $out[(string)$key] = $info;
}
echo json_encode((object)$out, JSON_UNESCAPED_UNICODE);
`, abs, abs)

	cmd := exec.Command("php", "-d", "short_open_tag=On", "-r", script)
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("php exec: %w", err)
	}

	var byKey map[string]phpEntry
	if err := json.Unmarshal(raw, &byKey); err != nil {
		return fmt.Errorf("decode php output: %w", err)
	}

	goods, err := convert(byKey)
	if err != nil {
		return err
	}
	out := yamlBalance{GoodsTypes: goods}

	// The recipes note used to be appended to the YAML by hand, which meant every
	// converter run silently dropped it (this one did). It is emitted from here so
	// it survives regeneration — the same reason the exclusion list lives in the
	// tool and not in the file.
	header := "# Auto-generated from includes/types_prod.php by cmd/starwind-tools/convert-balance.\n" +
		"# Do not edit by hand; rerun the converter against the source PHP file.\n" +
		"#\n" +
		"# Production recipes moved to configs/station_types.yaml (phase 8.15): they are\n" +
		"# generated from station_goods_types by convert-station-types, while this file\n" +
		"# is regenerated from types_prod.php by convert-balance (which writes only\n" +
		"# goods_types). Keeping them on separate files avoids one generator clobbering\n" +
		"# the other's section.\n"

	body, err := yaml.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(outPath, append([]byte(header), body...), 0o644); err != nil {
		return fmt.Errorf("write yaml: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %d goods types to %s (%d excluded)\n", len(goods), outPath, len(byKey)-len(goods))
	return nil
}

// convert maps the decoded PHP cargo table to the YAML catalog: excluded goods
// are dropped, the rest are copied through in id order.
func convert(byKey map[string]phpEntry) ([]yamlGoods, error) {
	rows := make([]phpRow, 0, len(byKey))
	for k, v := range byKey {
		id, err := strconv.Atoi(k)
		if err != nil {
			return nil, fmt.Errorf("non-integer goods key %q", k)
		}
		if _, skip := excludedGoods[id]; skip {
			continue
		}
		rows = append(rows, phpRow{Key: id, Entry: v})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })

	goods := make([]yamlGoods, 0, len(rows))
	for _, r := range rows {
		goods = append(goods, yamlGoods{
			ID:            r.Key,
			Name:          r.Entry.Name,
			MinWarRate:    r.Entry.MinWarRate,
			MinTradeRate:  r.Entry.MinTradeRate,
			MinRaceRate:   r.Entry.MinRaceRate,
			AvgPrice:      r.Entry.AvgPrice,
			MaxPrice:      r.Entry.MaxPrice,
			ProductionStd: r.Entry.ProductionStd,
			Space:         r.Entry.Space,
			ObjectTypeID:  r.Entry.ObjectTypeID,
		})
	}
	return goods, nil
}
