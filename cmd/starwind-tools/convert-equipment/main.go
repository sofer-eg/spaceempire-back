// Command convert-equipment reads the legacy StarWind ship-equipment catalog
// from the ct_updates table in sql/db.sql and writes it out as
// configs/equipment.yaml in the schema consumed by internal/balance. One-shot
// tool — rerun whenever the upstream dump changes.
//
// The dump's data sections are UTF-8 despite the cp1251 declaration (same as
// convert-ship-classes), so we read the bytes directly.
//
// ct_updates_energy (per-mode energy coefficients) is out of MVP scope — only
// ct_updates is ported here.
//
// Usage:
//
//	go run ./cmd/starwind-tools/convert-equipment \
//	    -sql /path/to/starwind/sql/db.sql \
//	    -out configs/equipment.yaml
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlEquipment mirrors the ct_updates columns, in dump order.
type yamlEquipment struct {
	ID            int    `yaml:"id"`
	Type          string `yaml:"type"`
	Description   string `yaml:"description"`
	MaxLevel      int    `yaml:"max_level"`
	Race          int    `yaml:"race"`
	Class         int    `yaml:"class"`
	Price         int64  `yaml:"price"`
	PricePerLevel int64  `yaml:"price_per_level"`
	MinWarRate    int    `yaml:"min_war_rate"`
	MinTradeRate  int    `yaml:"min_trade_rate"`
	MinRaceRate   int    `yaml:"min_race_rate"`
	IsBase        int    `yaml:"is_base"`
	Position      int    `yaml:"position"`
	Dependance    string `yaml:"dependance"`
	EnergyUseType string `yaml:"energy_use_type"`
	EnergyUsage   int    `yaml:"energy_usage"`
}

type yamlFile struct {
	Equipment []yamlEquipment `yaml:"equipment"`
}

// xbtfGapUpgrades are the four "X-BTF gap" modules spaceempire added on top of
// the ported StarWind ct_updates catalog: up_rudder (10.3.15), up_cargobay
// (10.3.16), up_ore_scanner (10.3.19) and up_transporter (10.3.18). They have no
// row in the dump, so the converter appends them verbatim (ids 140-143) to keep
// equipment.yaml a full, single-source-of-truth catalog. Their energy fields are
// already final (hold/action), so calibrate() would leave them unchanged.
var xbtfGapUpgrades = []yamlEquipment{
	{ID: 140, Type: "up_rudder", Description: "Оптимизация рулей", MaxLevel: 3, Race: 0, Class: 0, Price: 200000, PricePerLevel: 150000, MinWarRate: 0, MinTradeRate: 0, MinRaceRate: 0, IsBase: 0, Position: 1, Dependance: "none", EnergyUseType: "hold", EnergyUsage: 0},
	{ID: 141, Type: "up_cargobay", Description: "Расширение трюма", MaxLevel: 3, Race: 0, Class: 0, Price: 200000, PricePerLevel: 150000, MinWarRate: 0, MinTradeRate: 0, MinRaceRate: 0, IsBase: 0, Position: 1, Dependance: "none", EnergyUseType: "hold", EnergyUsage: 0},
	{ID: 142, Type: "up_ore_scanner", Description: "Сканер руды", MaxLevel: 1, Race: 0, Class: 0, Price: 150000, PricePerLevel: 0, MinWarRate: 0, MinTradeRate: 0, MinRaceRate: 0, IsBase: 0, Position: 1, Dependance: "none", EnergyUseType: "hold", EnergyUsage: 0},
	{ID: 143, Type: "up_transporter", Description: "Транспортатор грузов", MaxLevel: 1, Race: 0, Class: 0, Price: 300000, PricePerLevel: 0, MinWarRate: 0, MinTradeRate: 0, MinRaceRate: 0, IsBase: 0, Position: 1, Dependance: "none", EnergyUseType: "action", EnergyUsage: 50},
}

func main() {
	sqlFile := flag.String("sql", "", "path to starwind/sql/db.sql")
	out := flag.String("out", "configs/equipment.yaml", "output YAML path")
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
	rawValues, err := sliceInsertValues(raw, "ct_updates")
	if err != nil {
		return err
	}
	tuples, err := splitTopLevelTuples(strings.TrimSpace(string(rawValues)))
	if err != nil {
		return err
	}

	items := make([]yamlEquipment, 0, len(tuples)+len(xbtfGapUpgrades))
	for i, t := range tuples {
		e, err := parseTuple(t)
		if err != nil {
			return fmt.Errorf("tuple %d: %w", i, err)
		}
		applyPostDumpOverrides(&e)
		items = append(items, e)
	}
	// The X-BTF gap upgrades (ids 140-143) have no ct_updates rows in the dump —
	// they were designed for spaceempire (phases 10.3.15/16/18/19). Emit them here
	// so the converter reproduces the whole shipped catalog rather than silently
	// dropping four modules.
	items = append(items, xbtfGapUpgrades...)

	header := "# Auto-generated from sql/db.sql (ct_updates) by cmd/starwind-tools/convert-equipment.\n" +
		"# Do not edit by hand; rerun the converter against the source dump.\n"
	body, err := yaml.Marshal(yamlFile{Equipment: items})
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(outPath, append([]byte(header), body...), 0o644); err != nil {
		return fmt.Errorf("write yaml: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %d equipment rows to %s\n", len(items), outPath)
	return nil
}

// applyPostDumpOverrides rewrites the catalog fields spaceempire re-calibrated
// after the StarWind dump was frozen, so the converter reproduces the shipped
// catalog rather than reverting prior tasks on a rerun. Each override is a
// deliberate design change with its own task + test:
//
//   - up_scanner max_level 1→2 where the class can fit it (TASK-123 radar: +50
//     units/level, up to +100). Classes with a 0 dump level (TL/TS: it is not
//     available to them) stay 0.
//   - up_torpedo_launcher gates on war_rate>=2, not race_rate — min_race_rate was
//     dead config (TASK-100.3.14/5.2, ЧТЗ doc-1 C-06).
//   - energy calibration (calibrate, TASK-100.3.25).
func applyPostDumpOverrides(e *yamlEquipment) {
	calibrate(e)
	switch e.Type {
	case "up_scanner":
		if e.MaxLevel > 0 {
			e.MaxLevel = 2
		}
	case "up_torpedo_launcher":
		e.MinWarRate = 2
		e.MinRaceRate = 0
	}
}

// calibrate rewrites the steady-state energy fields the original ct_updates
// left at the uncalibrated DEFAULT of 100 (TASK-100.3.25 energy model). Only the
// per-tick draws are touched; one-off `action` costs (launcher/torpedo/drill/
// transporter, …) and passive `hold` modules keep their dump values:
//
//   - always drain (up_shield/up_pro/up_turret_control/up_hide/… ) → 2/tick, so a
//     ship's base kit costs a few points/tick, not −100 each (which pinned energy
//     at 0 and silenced the laser).
//   - reverse feed (up_generator) → 6/tick, so one generator more than offsets the
//     base always-drain and lets a player extend fire.
//   - up_accumulator max_level → 3, so it can double the energy pool up to three
//     times (scout 40 → 80 → 160 → 320; fire duration 10 → 20 → 40 → 80).
//
// The pool/recharge that make these land on the per-class fire-duration targets
// live in convert-ship-classes (energyCalibration). See
// back/docs/specs/energy_model.md.
func calibrate(e *yamlEquipment) {
	switch e.EnergyUseType {
	case "always":
		e.EnergyUsage = 2
	case "reverse":
		e.EnergyUsage = 6
	}
	if e.Type == "up_accumulator" {
		e.MaxLevel = 3
	}
}

func parseTuple(t string) (yamlEquipment, error) {
	f := splitFields(t)
	if len(f) != 16 {
		return yamlEquipment{}, fmt.Errorf("expected 16 fields, got %d: %q", len(f), t)
	}
	var e yamlEquipment
	ints := []struct {
		dst *int
		idx int
		lbl string
	}{
		{&e.ID, 0, "id"}, {&e.MaxLevel, 3, "max_level"}, {&e.Race, 4, "race"},
		{&e.Class, 5, "class"}, {&e.MinWarRate, 8, "min_war_rate"},
		{&e.MinTradeRate, 9, "min_trade_rate"}, {&e.MinRaceRate, 10, "min_race_rate"},
		{&e.IsBase, 11, "is_base"}, {&e.Position, 12, "position"},
		{&e.EnergyUsage, 15, "energy_usage"},
	}
	for _, p := range ints {
		v, err := strconv.Atoi(f[p.idx])
		if err != nil {
			return e, fmt.Errorf("%s: %w", p.lbl, err)
		}
		*p.dst = v
	}
	var err error
	if e.Price, err = strconv.ParseInt(f[6], 10, 64); err != nil {
		return e, fmt.Errorf("price: %w", err)
	}
	if e.PricePerLevel, err = strconv.ParseInt(f[7], 10, 64); err != nil {
		return e, fmt.Errorf("price_per_level: %w", err)
	}
	e.Type = strings.TrimSpace(f[1])
	e.Description = strings.TrimSpace(f[2])
	e.Dependance = strings.TrimSpace(f[13])
	e.EnergyUseType = strings.TrimSpace(f[14])
	return e, nil
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
