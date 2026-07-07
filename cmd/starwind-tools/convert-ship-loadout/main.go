// Command convert-ship-loadout reads the legacy StarWind base-loadout table
// ct_npc_ship_modules from sql/db.sql and writes it out as
// configs/ship_base_loadout.yaml in the schema consumed by internal/balance
// (TASK-100.3.25).
//
// The original SP CreateStandartPilotShip fitted these modules on a freshly
// spawned ship directly (raw INSERT ... SELECT from ct_npc_ship_modules),
// bypassing the dependance/rank gates the interactive shipyard enforces. Each
// (race, type, module_type, module_level) row is resolved against the ship-class
// catalog (to find the ship's class number) and the equipment catalog (to pin
// the exact EquipmentID for that module_type + class), and the level is clamped
// to the catalog row's max_level (a handful of original rows overshoot the cap).
//
// The output covers every model in configs/ship_classes.yaml: the 72 standard
// race×type models carry their ct_npc_ship_modules set; the 14 special models
// (race 100, plus race 3/8 type 10) have no rows upstream and spawn bare.
//
// One-shot tool — rerun whenever the upstream dump, ship_classes.yaml or
// equipment.yaml changes.
//
// Usage:
//
//	go run ./cmd/starwind-tools/convert-ship-loadout \
//	    -sql /path/to/starwind/sql/db.sql \
//	    -classes configs/ship_classes.yaml \
//	    -equipment configs/equipment.yaml \
//	    -out configs/ship_base_loadout.yaml
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

	"spaceempire/back/internal/balance"
)

// moduleOrder is the canonical display order the emitted loadout uses, so the
// output is deterministic and independent of the dump's per-ship ordering
// (which lists up_pro before up_launcher). Types outside this map sort last.
var moduleOrder = map[string]int{
	"up_engine":         0,
	"up_shield":         1,
	"up_weapon_control": 2,
	"up_turret_control": 3,
	"up_launcher":       4,
	"up_pro":            5,
}

// ctModule is one ct_npc_ship_modules row (id column dropped).
type ctModule struct {
	Race       int
	Type       int
	ModuleType string
	Level      int
}

// yamlModule / yamlLoadout / yamlFile mirror the on-disk shape the balance
// loader (internal/balance/ship_loadout_loader.go) reads back.
type yamlModule struct {
	EquipmentID int    `yaml:"equipment_id"`
	Type        string `yaml:"type"`
	Level       int    `yaml:"level"`
}

type yamlLoadout struct {
	Race    int          `yaml:"race"`
	Type    int          `yaml:"type"`
	Modules []yamlModule `yaml:"modules"`
}

type yamlFile struct {
	Loadouts []yamlLoadout `yaml:"loadouts"`
}

func main() {
	sqlFile := flag.String("sql", "", "path to starwind/sql/db.sql")
	classesFile := flag.String("classes", "configs/ship_classes.yaml", "path to the ship-class catalog")
	equipFile := flag.String("equipment", "configs/equipment.yaml", "path to the equipment catalog")
	out := flag.String("out", "configs/ship_base_loadout.yaml", "output YAML path")
	flag.Parse()

	if *sqlFile == "" {
		fmt.Fprintln(os.Stderr, "-sql is required")
		os.Exit(2)
	}
	if err := run(*sqlFile, *classesFile, *equipFile, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(sqlFile, classesFile, equipFile, outPath string) error {
	classes, err := balance.LoadShipClassesFromFile(classesFile)
	if err != nil {
		return fmt.Errorf("load ship classes: %w", err)
	}
	equipment, err := balance.LoadEquipmentFromFile(equipFile)
	if err != nil {
		return fmt.Errorf("load equipment: %w", err)
	}
	modules, err := readCtModules(sqlFile)
	if err != nil {
		return err
	}

	// Index the ct rows by (race, type).
	byRaceType := make(map[[2]int][]ctModule)
	for _, m := range modules {
		key := [2]int{m.Race, m.Type}
		byRaceType[key] = append(byRaceType[key], m)
	}

	var loadouts []yamlLoadout
	clamps := 0
	for _, cls := range classes.AllShipClasses() {
		rows := byRaceType[[2]int{cls.Race, cls.Type}]
		mods := make([]yamlModule, 0, len(rows))
		for _, m := range rows {
			e, err := resolveEquip(equipment, m.ModuleType, cls.Class, cls.Race)
			if err != nil {
				return fmt.Errorf("race=%d type=%d module=%s: %w", m.Race, m.Type, m.ModuleType, err)
			}
			level, clamped := clampLevel(m.Level, e.MaxLevel)
			if clamped {
				clamps++
				fmt.Fprintf(os.Stderr, "clamp: race=%d type=%d %s level %d -> %d (eqid %d max_level %d)\n",
					m.Race, m.Type, m.ModuleType, m.Level, level, e.ID, e.MaxLevel)
			}
			mods = append(mods, yamlModule{EquipmentID: int(e.ID), Type: m.ModuleType, Level: level})
		}
		sort.SliceStable(mods, func(i, j int) bool {
			return moduleOrder[mods[i].Type] < moduleOrder[mods[j].Type]
		})
		loadouts = append(loadouts, yamlLoadout{Race: cls.Race, Type: cls.Type, Modules: mods})
	}

	header := "# Auto-generated from sql/db.sql (ct_npc_ship_modules) by cmd/starwind-tools/convert-ship-loadout.\n" +
		"# Do not edit by hand; rerun the converter against the source dump.\n"
	body, err := yaml.Marshal(yamlFile{Loadouts: loadouts})
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(outPath, append([]byte(header), body...), 0o644); err != nil {
		return fmt.Errorf("write yaml: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %d loadouts (%d level clamps) to %s\n", len(loadouts), clamps, outPath)
	return nil
}

// resolveEquip finds the single equipment catalog row for a module type on a
// ship of the given class and race. It matches type + exact class, preferring a
// race-specific row over the universal (race 0) one when both exist. It fails on
// zero matches (unresolved) or an ambiguous multi-match — so a config drift that
// breaks the 1:1 mapping is caught at generation time, not silently.
func resolveEquip(c *balance.Equipments, moduleType string, classNum, race int) (balance.Equipment, error) {
	var universal, raceSpecific []balance.Equipment
	for _, e := range c.EquipmentByType(moduleType) {
		if e.ShipClass != classNum {
			continue
		}
		switch e.Race {
		case race:
			raceSpecific = append(raceSpecific, e)
		case 0:
			universal = append(universal, e)
		}
	}
	if len(raceSpecific) == 1 {
		return raceSpecific[0], nil
	}
	if len(raceSpecific) > 1 {
		return balance.Equipment{}, fmt.Errorf("ambiguous: %d race-specific rows for class %d race %d", len(raceSpecific), classNum, race)
	}
	if len(universal) == 1 {
		return universal[0], nil
	}
	if len(universal) > 1 {
		return balance.Equipment{}, fmt.Errorf("ambiguous: %d universal rows for class %d", len(universal), classNum)
	}
	return balance.Equipment{}, fmt.Errorf("unresolved: no equipment row for class %d race %d", classNum, race)
}

// clampLevel caps level to the catalog row's max_level (treating a <1 max as 1),
// returning the clamped level and whether it was reduced.
func clampLevel(level, maxLevel int) (int, bool) {
	if maxLevel < 1 {
		maxLevel = 1
	}
	if level > maxLevel {
		return maxLevel, true
	}
	if level < 1 {
		return 1, false
	}
	return level, false
}

// readCtModules extracts the ct_npc_ship_modules rows from the dump.
func readCtModules(sqlFile string) ([]ctModule, error) {
	abs, err := filepath.Abs(sqlFile)
	if err != nil {
		return nil, fmt.Errorf("resolve sql: %w", err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read sql: %w", err)
	}
	rawValues, err := sliceInsertValues(raw, "ct_npc_ship_modules")
	if err != nil {
		return nil, err
	}
	tuples, err := splitTopLevelTuples(strings.TrimSpace(string(rawValues)))
	if err != nil {
		return nil, err
	}
	out := make([]ctModule, 0, len(tuples))
	for i, t := range tuples {
		m, err := parseTuple(t)
		if err != nil {
			return nil, fmt.Errorf("tuple %d: %w", i, err)
		}
		out = append(out, m)
	}
	return out, nil
}

// parseTuple parses one ct_npc_ship_modules tuple:
// (ID, race, type, module_type, module_level). The ID column is discarded.
func parseTuple(t string) (ctModule, error) {
	f := splitFields(t)
	if len(f) != 5 {
		return ctModule{}, fmt.Errorf("expected 5 fields, got %d: %q", len(f), t)
	}
	var m ctModule
	var err error
	if m.Race, err = strconv.Atoi(f[1]); err != nil {
		return m, fmt.Errorf("race: %w", err)
	}
	if m.Type, err = strconv.Atoi(f[2]); err != nil {
		return m, fmt.Errorf("type: %w", err)
	}
	m.ModuleType = strings.TrimSpace(f[3])
	if m.Level, err = strconv.Atoi(f[4]); err != nil {
		return m, fmt.Errorf("module_level: %w", err)
	}
	return m, nil
}

// --- shared dump-parsing helpers (same as convert-equipment) ---------------

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
