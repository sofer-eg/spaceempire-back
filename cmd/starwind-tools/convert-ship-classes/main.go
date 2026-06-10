// Command convert-ship-classes reads the legacy StarWind ship-class catalog
// from the ct_ship_classes table in sql/db.sql and writes it out as
// configs/ship_classes.yaml in the schema consumed by internal/balance.
// One-shot tool — rerun whenever the upstream dump changes.
//
// The dump is cp1251; we shell out to iconv (same dependency style as
// convert-balance shelling out to php) to get UTF-8 before parsing.
//
// Usage:
//
//	go run ./cmd/starwind-tools/convert-ship-classes \
//	    -sql /path/to/starwind/sql/db.sql \
//	    -out configs/ship_classes.yaml
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

// yamlShipClass mirrors the ct_ship_classes columns, in dump order.
type yamlShipClass struct {
	ID              int     `yaml:"id"`
	Race            int     `yaml:"race"`
	Type            int     `yaml:"type"`
	Class           int     `yaml:"class"`
	Name            string  `yaml:"name"`
	Speed           float64 `yaml:"speed"`
	Acceleration    float64 `yaml:"acceleration"`
	Laser           int     `yaml:"laser"`
	Shield          int     `yaml:"shield"`
	Hull            int     `yaml:"hull"`
	ShieldCharge    int     `yaml:"shield_charge"`
	Maneuverability float64 `yaml:"maneuverability"`
	CargoBay        int     `yaml:"cargobay"`
	BasePrice       int64   `yaml:"base_price"`
	HangerSmall     int     `yaml:"hanger_small"`
	HangerCapital   int     `yaml:"hanger_capital"`
	HangerShipType  int     `yaml:"hanger_ship_type"`
	HangerShipSpace int     `yaml:"hanger_ship_space"`
	PilotCabin      int     `yaml:"pilot_cabin"`
	JumpFuel        float64 `yaml:"jump_fuel"`
}

type yamlFile struct {
	ShipClasses []yamlShipClass `yaml:"ship_classes"`
}

func main() {
	sqlFile := flag.String("sql", "", "path to starwind/sql/db.sql")
	out := flag.String("out", "configs/ship_classes.yaml", "output YAML path")
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

	// Despite the `DEFAULT CHARSET=cp1251` declaration inside, this dump's
	// data sections are stored as UTF-8 (verified: 'К' = 0xD0 0x9A, and
	// `file -bi` reports charset=utf-8), so we read the slice directly.
	raw, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("read sql: %w", err)
	}
	rawValues, err := sliceInsertValues(raw, "ct_ship_classes")
	if err != nil {
		return err
	}
	values := strings.TrimSpace(string(rawValues))
	tuples, err := splitTopLevelTuples(values)
	if err != nil {
		return err
	}

	classes := make([]yamlShipClass, 0, len(tuples))
	for i, t := range tuples {
		sc, err := parseTuple(t)
		if err != nil {
			return fmt.Errorf("tuple %d: %w", i, err)
		}
		classes = append(classes, sc)
	}

	header := "# Auto-generated from sql/db.sql (ct_ship_classes) by cmd/starwind-tools/convert-ship-classes.\n" +
		"# Do not edit by hand; rerun the converter against the source dump.\n"
	body, err := yaml.Marshal(yamlFile{ShipClasses: classes})
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(outPath, append([]byte(header), body...), 0o644); err != nil {
		return fmt.Errorf("write yaml: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %d ship classes to %s\n", len(classes), outPath)
	return nil
}

// sliceInsertValues finds `INSERT INTO `table` VALUES <body>;\n` in the raw
// cp1251 bytes and returns the <body> bytes (still cp1251). The marker and
// statement structure are ASCII; only the names inside are cp1251.
func sliceInsertValues(dump []byte, table string) ([]byte, error) {
	marker := []byte("INSERT INTO `" + table + "` VALUES ")
	idx := bytes.Index(dump, marker)
	if idx < 0 {
		return nil, fmt.Errorf("marker %q not found", marker)
	}
	rest := dump[idx+len(marker):]
	// The VALUES body contains no semicolons (only numbers and quoted
	// names), so the first ';' is the statement terminator — robust to
	// CRLF vs LF line endings in the dump.
	end := bytes.IndexByte(rest, ';')
	if end < 0 {
		return nil, fmt.Errorf("no statement terminator after %q", marker)
	}
	return rest[:end], nil
}

// splitTopLevelTuples splits "(...),(...),(...)" into the inner contents of
// each top-level (...), respecting single-quoted strings (so commas and
// parens inside a name don't split).
func splitTopLevelTuples(s string) ([]string, error) {
	var out []string
	var buf bytes.Buffer
	depth, inStr := 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			if c == '\'' {
				// '' is an escaped quote inside the string.
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

// splitFields splits one tuple's body by top-level commas, respecting quotes.
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

func parseTuple(t string) (yamlShipClass, error) {
	f := splitFields(t)
	if len(f) != 20 {
		return yamlShipClass{}, fmt.Errorf("expected 20 fields, got %d: %q", len(f), t)
	}
	atoi := func(s string) (int, error) { return strconv.Atoi(s) }
	atof := func(s string) (float64, error) { return strconv.ParseFloat(s, 64) }

	var sc yamlShipClass
	var err error
	if sc.ID, err = atoi(f[0]); err != nil {
		return sc, fmt.Errorf("id: %w", err)
	}
	if sc.Race, err = atoi(f[1]); err != nil {
		return sc, fmt.Errorf("race: %w", err)
	}
	if sc.Type, err = atoi(f[2]); err != nil {
		return sc, fmt.Errorf("type: %w", err)
	}
	if sc.Class, err = atoi(f[3]); err != nil {
		return sc, fmt.Errorf("class: %w", err)
	}
	sc.Name = strings.TrimSpace(f[4])
	if sc.Speed, err = atof(f[5]); err != nil {
		return sc, fmt.Errorf("speed: %w", err)
	}
	if sc.Acceleration, err = atof(f[6]); err != nil {
		return sc, fmt.Errorf("acceleration: %w", err)
	}
	if sc.Laser, err = atoi(f[7]); err != nil {
		return sc, fmt.Errorf("laser: %w", err)
	}
	if sc.Shield, err = atoi(f[8]); err != nil {
		return sc, fmt.Errorf("shield: %w", err)
	}
	if sc.Hull, err = atoi(f[9]); err != nil {
		return sc, fmt.Errorf("hull: %w", err)
	}
	if sc.ShieldCharge, err = atoi(f[10]); err != nil {
		return sc, fmt.Errorf("shield_charge: %w", err)
	}
	if sc.Maneuverability, err = atof(f[11]); err != nil {
		return sc, fmt.Errorf("maneuverability: %w", err)
	}
	if sc.CargoBay, err = atoi(f[12]); err != nil {
		return sc, fmt.Errorf("cargobay: %w", err)
	}
	bp, err := strconv.ParseInt(f[13], 10, 64)
	if err != nil {
		return sc, fmt.Errorf("base_price: %w", err)
	}
	sc.BasePrice = bp
	if sc.HangerSmall, err = atoi(f[14]); err != nil {
		return sc, fmt.Errorf("hanger_small: %w", err)
	}
	if sc.HangerCapital, err = atoi(f[15]); err != nil {
		return sc, fmt.Errorf("hanger_capital: %w", err)
	}
	if sc.HangerShipType, err = atoi(f[16]); err != nil {
		return sc, fmt.Errorf("hanger_ship_type: %w", err)
	}
	if sc.HangerShipSpace, err = atoi(f[17]); err != nil {
		return sc, fmt.Errorf("hanger_ship_space: %w", err)
	}
	if sc.PilotCabin, err = atoi(f[18]); err != nil {
		return sc, fmt.Errorf("pilot_cabin: %w", err)
	}
	if sc.JumpFuel, err = atof(f[19]); err != nil {
		return sc, fmt.Errorf("jump_fuel: %w", err)
	}
	return sc, nil
}
