// Command gen-ship-radar generates the ships.radar_range backfill (migration
// 0055, TASK-117). Migration 0046 added ships.radar_range with DEFAULT 0 and no
// backfill, so every ship that predates it — including the player's own — has
// radar_range=0 and falls back to the whole-sector radius, defeating the radar.
//
// It reads the ship-class catalog (configs/ship_classes.yaml) — the same source
// of truth the spawners use — and emits one UPDATE per distinct class radar,
// grouping the class ids that share it:
//
//	UPDATE ships SET radar_range = <radar>
//	WHERE radar_range <= 0 AND is_spacesuit = false AND ship_class_id IN (...);
//
// Only radar_range<=0, non-spacesuit rows are touched, so correctly-spawned
// ships and spacesuits are left alone. One-shot tool — rerun if the class radar
// calibration in balance/shipclass.go or ship_classes.yaml changes.
//
// Usage:
//
//	go run ./cmd/starwind-tools/gen-ship-radar \
//	    -classes configs/ship_classes.yaml \
//	    -out migrations/0055_ship_radar_backfill.sql
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"spaceempire/back/internal/balance"
)

func main() {
	classesFile := flag.String("classes", "configs/ship_classes.yaml", "path to the ship-class catalog")
	out := flag.String("out", "migrations/0055_ship_radar_backfill.sql", "output migration path")
	overwrite := flag.Bool("overwrite", false, "recalibration mode: overwrite every class ship's radar_range (drop the radar_range<=0 guard)")
	flag.Parse()
	if err := run(*classesFile, *out, *overwrite); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(classesFile, outPath string, overwrite bool) error {
	classes, err := balance.LoadShipClassesFromFile(classesFile)
	if err != nil {
		return fmt.Errorf("load ship classes: %w", err)
	}

	// Group class ids by their (already category-defaulted) radar value.
	byRadar := make(map[int][]int)
	for _, c := range classes.AllShipClasses() {
		if c.Radar <= 0 {
			continue // no radar → nothing to backfill for this class
		}
		byRadar[c.Radar] = append(byRadar[c.Radar], int(c.ID))
	}

	radars := make([]int, 0, len(byRadar))
	for r := range byRadar {
		radars = append(radars, r)
	}
	sort.Ints(radars)

	migration, updates := render(byRadar, radars, overwrite)
	if err := os.WriteFile(outPath, []byte(migration), 0o644); err != nil {
		return fmt.Errorf("write migration: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %d radar backfill UPDATE(s) to %s\n", updates, outPath)
	return nil
}

func render(byRadar map[int][]int, radars []int, overwrite bool) (string, int) {
	// The WHERE guard differs by mode: initial backfill only fills ships still
	// at radar_range<=0 (leaving correctly-spawned ships alone); recalibration
	// overwrites every class ship so the whole fleet moves to the new numbers.
	guard := "radar_range <= 0 AND is_spacesuit = false"
	if overwrite {
		guard = "is_spacesuit = false"
	}

	var b strings.Builder
	b.WriteString("-- +goose Up\n-- +goose StatementBegin\n")
	if overwrite {
		b.WriteString("-- Recalibrate ships.radar_range for every class ship (TASK-123), generated\n")
		b.WriteString("-- by cmd/starwind-tools/gen-ship-radar -overwrite. The radar defaults were\n")
		b.WriteString("-- re-anchored to the real sector geometry (~±1000), so this overwrites the\n")
		b.WriteString("-- old inflated values with each class's new radar. up_scanner is not folded\n")
		b.WriteString("-- (no ship has one installed); the outfit path recomputes it on install.\n")
		b.WriteString("-- Spacesuits (radar_range 0) are left alone.\n")
		b.WriteString("-- Do not edit by hand — rerun the generator.\n\n")
	} else {
		b.WriteString("-- Backfill ships.radar_range for existing class ships (TASK-117), generated\n")
		b.WriteString("-- by cmd/starwind-tools/gen-ship-radar. Migration 0046 added the column with\n")
		b.WriteString("-- DEFAULT 0 and no backfill, so pre-0046 ships (incl. the player's own) sat at\n")
		b.WriteString("-- radar_range=0 and fell back to the whole-sector radius. Each UPDATE assigns\n")
		b.WriteString("-- the ship class's radar (balance category default) to the ships of that class\n")
		b.WriteString("-- that still have no radar. Spacesuits and already-set ships are left alone.\n")
		b.WriteString("-- Do not edit by hand — rerun the generator.\n\n")
	}

	for _, r := range radars {
		ids := append([]int(nil), byRadar[r]...)
		sort.Ints(ids)
		strIDs := make([]string, len(ids))
		for i, id := range ids {
			strIDs[i] = strconv.Itoa(id)
		}
		fmt.Fprintf(&b,
			"UPDATE ships SET radar_range = %d\nWHERE %s AND ship_class_id IN (%s);\n",
			r, guard, strings.Join(strIDs, ", "))
	}

	b.WriteString("-- +goose StatementEnd\n\n")
	b.WriteString("-- +goose Down\n-- +goose StatementBegin\n")
	b.WriteString("-- Undo the backfill: reset class ships back to radar_range=0 (spacesuits\n")
	b.WriteString("-- untouched). The runtime fallback keeps them playable; a re-run of Up\n")
	b.WriteString("-- restores the class radars.\n")
	allIDs := make([]int, 0)
	for _, r := range radars {
		allIDs = append(allIDs, byRadar[r]...)
	}
	sort.Ints(allIDs)
	strAll := make([]string, len(allIDs))
	for i, id := range allIDs {
		strAll[i] = strconv.Itoa(id)
	}
	fmt.Fprintf(&b,
		"UPDATE ships SET radar_range = 0\nWHERE is_spacesuit = false AND ship_class_id IN (%s);\n",
		strings.Join(strAll, ", "))
	b.WriteString("-- +goose StatementEnd\n")
	return b.String(), len(radars)
}
