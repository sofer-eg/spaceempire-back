// Command convert-station-types reads the legacy StarWind station catalog and
// production chains from the station_types and station_goods_types tables in
// sql/db.sql and writes them out as configs/station_types.yaml (catalog +
// recipes) in the schema consumed by internal/balance. One-shot tool — rerun
// whenever the upstream dump changes.
//
// The dump's data sections are UTF-8 despite the cp1251 declaration (same as
// convert-ship-classes), so we read the bytes directly.
//
// Recipe model note: the original station_goods_types carries a per-line
// cycle_time, while balance.Recipe has a single CycleTime per recipe. We map
// production rows (cycle_type=1) to the single-cycle model by taking the
// recipe's CycleTime from the slowest output line and normalising every input
// quantity to that period (qty*recipeCycle/lineCycle) — rate-preserving, and
// the output batch (qty) stays exact, matching the production engine's
// "consume Inputs, then yield Outputs after CycleTime" contract. Secondary
// upkeep lines (type=-1, racial food/whisky) and build/rebuild cycles
// (cycle_type=0/2) are out of MVP scope and skipped.
//
// Usage:
//
//	go run ./cmd/starwind-tools/convert-station-types \
//	    -sql /path/to/starwind/sql/db.sql \
//	    -out configs/station_types.yaml
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlStationType mirrors the station_types columns we keep (id, name,
// race_id, kind=type, hull, shield, sellable). `space` is build-cost data,
// not needed for naming, so it is dropped.
type yamlStationType struct {
	ID       int    `yaml:"id"`
	Name     string `yaml:"name"`
	RaceID   int    `yaml:"race_id"`
	Kind     int    `yaml:"kind"`
	Hull     int    `yaml:"hull"`
	Shield   int    `yaml:"shield"`
	Sellable int    `yaml:"sellable"`
}

// yamlRecipeLine matches the shape internal/balance already parses (type/qty/
// max). Max is only meaningful on outputs (the storage cap, goods_max_count).
type yamlRecipeLine struct {
	Type int   `yaml:"type"`
	Qty  int64 `yaml:"qty"`
	Max  int64 `yaml:"max,omitempty"`
}

type yamlRecipe struct {
	StationType int              `yaml:"station_type"`
	CycleTime   string           `yaml:"cycle_time"`
	Inputs      []yamlRecipeLine `yaml:"inputs,omitempty"`
	Outputs     []yamlRecipeLine `yaml:"outputs"`
}

type yamlFile struct {
	StationTypes []yamlStationType `yaml:"station_types"`
	Recipes      []yamlRecipe      `yaml:"recipes"`
}

// goodsRow is one parsed station_goods_types tuple.
type goodsRow struct {
	stationType int
	goodsType   int
	cycleType   int
	cycleTime   int
	count       int64
	maxCount    int64
	kind        int // 0 input, 1 output, -1 upkeep
}

func main() {
	sqlFile := flag.String("sql", "", "path to starwind/sql/db.sql")
	out := flag.String("out", "configs/station_types.yaml", "output YAML path")
	flag.Parse()

	if *sqlFile == "" {
		fmt.Fprintln(os.Stderr, "-sql is required")
		os.Exit(2)
	}
	if err := run(*sqlFile, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(sqlFile, outPath string) error {
	abs, err := filepath.Abs(sqlFile)
	if err != nil {
		return fmt.Errorf("resolve sql: %w", err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("read sql: %w", err)
	}

	types, err := parseStationTypes(raw)
	if err != nil {
		return err
	}
	recipes, dropped, err := parseRecipes(raw)
	if err != nil {
		return err
	}

	header := "# Auto-generated from sql/db.sql (station_types + station_goods_types)\n" +
		"# by cmd/starwind-tools/convert-station-types. Do not edit by hand;\n" +
		"# rerun the converter against the source dump.\n"
	body, err := yaml.Marshal(yamlFile{StationTypes: types, Recipes: recipes})
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(outPath, append([]byte(header), body...), 0o644); err != nil {
		return fmt.Errorf("write yaml: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %d station types and %d recipes to %s (skipped %d non-producer types)\n",
		len(types), len(recipes), outPath, dropped)
	return nil
}

func parseStationTypes(raw []byte) ([]yamlStationType, error) {
	rawValues, err := sliceInsertValues(raw, "station_types")
	if err != nil {
		return nil, err
	}
	tuples, err := splitTopLevelTuples(strings.TrimSpace(string(rawValues)))
	if err != nil {
		return nil, err
	}
	types := make([]yamlStationType, 0, len(tuples))
	for i, t := range tuples {
		f := splitFields(t)
		if len(f) != 8 {
			return nil, fmt.Errorf("station_types tuple %d: expected 8 fields, got %d: %q", i, len(f), t)
		}
		st := yamlStationType{Name: strings.TrimSpace(f[1])}
		for _, p := range []struct {
			dst *int
			src string
			lbl string
		}{
			{&st.ID, f[0], "id"}, {&st.RaceID, f[2], "race_id"}, {&st.Kind, f[3], "kind"},
			{&st.Hull, f[5], "hull"}, {&st.Shield, f[6], "shield"}, {&st.Sellable, f[7], "sellable"},
		} {
			v, err := strconv.Atoi(p.src)
			if err != nil {
				return nil, fmt.Errorf("station_types %q: %s: %w", t, p.lbl, err)
			}
			*p.dst = v
		}
		types = append(types, st)
	}
	sort.Slice(types, func(i, j int) bool { return types[i].ID < types[j].ID })
	return types, nil
}

func parseRecipes(raw []byte) ([]yamlRecipe, int, error) {
	rawValues, err := sliceInsertValues(raw, "station_goods_types")
	if err != nil {
		return nil, 0, err
	}
	tuples, err := splitTopLevelTuples(strings.TrimSpace(string(rawValues)))
	if err != nil {
		return nil, 0, err
	}

	byStation := map[int][]goodsRow{}
	order := []int{}
	for i, t := range tuples {
		f := splitFields(t)
		if len(f) != 7 {
			return nil, 0, fmt.Errorf("station_goods_types tuple %d: expected 7 fields, got %d: %q", i, len(f), t)
		}
		nums := make([]int64, 7)
		for k := 0; k < 7; k++ {
			v, err := strconv.ParseInt(f[k], 10, 64)
			if err != nil {
				return nil, 0, fmt.Errorf("station_goods_types %q field %d: %w", t, k, err)
			}
			nums[k] = v
		}
		r := goodsRow{
			stationType: int(nums[0]), goodsType: int(nums[1]), cycleType: int(nums[2]),
			cycleTime: int(nums[3]), count: nums[4], maxCount: nums[5], kind: int(nums[6]),
		}
		if r.cycleType != 1 { // only production cycles (skip build=0 / rebuild=2)
			continue
		}
		if _, ok := byStation[r.stationType]; !ok {
			order = append(order, r.stationType)
		}
		byStation[r.stationType] = append(byStation[r.stationType], r)
	}
	sort.Ints(order)

	recipes := make([]yamlRecipe, 0, len(order))
	dropped := 0
	for _, st := range order {
		rec, ok := buildRecipe(st, byStation[st])
		if !ok {
			dropped++
			continue
		}
		recipes = append(recipes, rec)
	}
	return recipes, dropped, nil
}

// buildRecipe collapses one station's production rows into the single-cycle
// balance.Recipe shape. Returns ok=false when the station has no output line
// (a pure consumer / non-producer) — such types carry no recipe.
func buildRecipe(stationType int, rows []goodsRow) (yamlRecipe, bool) {
	// recipeCycle = slowest output cycle_time (the production period).
	recipeCycle := 0
	for _, r := range rows {
		if r.kind == 1 && r.cycleTime > recipeCycle {
			recipeCycle = r.cycleTime
		}
	}
	if recipeCycle <= 0 {
		return yamlRecipe{}, false
	}

	var inputs, outputs []yamlRecipeLine
	for _, r := range rows {
		switch r.kind {
		case 1: // output — qty normalised to the recipe period (factor ≥1)
			outputs = append(outputs, yamlRecipeLine{
				Type: r.goodsType,
				Qty:  normalise(r.count, recipeCycle, r.cycleTime),
				Max:  r.maxCount,
			})
		case 0: // input — consumed amount over one recipe period
			if r.cycleTime <= 0 {
				continue
			}
			inputs = append(inputs, yamlRecipeLine{
				Type: r.goodsType,
				Qty:  normalise(r.count, recipeCycle, r.cycleTime),
			})
		default: // -1: racial upkeep, out of MVP scope — skip
		}
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Type < inputs[j].Type })
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Type < outputs[j].Type })

	return yamlRecipe{
		StationType: stationType,
		CycleTime:   fmt.Sprintf("%ds", recipeCycle),
		Inputs:      inputs,
		Outputs:     outputs,
	}, true
}

// normalise scales a per-lineCycle quantity to the recipe period, rounding to
// the nearest integer. lineCycle is guaranteed positive by the caller.
func normalise(qty int64, recipeCycle, lineCycle int) int64 {
	num := qty * int64(recipeCycle)
	return (num + int64(lineCycle)/2) / int64(lineCycle)
}

// --- shared dump-parsing helpers (same as convert-ship-classes) ------------

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
