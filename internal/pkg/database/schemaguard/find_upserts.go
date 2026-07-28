package schemaguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// skippedDirNames are never scanned wherever they appear.
var skippedDirNames = map[string]struct{}{
	".git":         {},
	"vendor":       {},
	"node_modules": {},
	"bin":          {},
}

// skippedRelPaths are module-relative directories excluded from the scan.
// cmd/starwind-tools generates *migration text*: it assembles ON CONFLICT
// clauses through strings.Builder, so whatever literals it holds are fragments
// that never run as statements. internal/webui/dist is the embedded SPA bundle.
var skippedRelPaths = map[string]struct{}{
	"cmd/starwind-tools":  {},
	"internal/webui/dist": {},
}

var (
	insertIntoRe = regexp.MustCompile(`(?is)INSERT\s+INTO\s+([a-zA-Z_][a-zA-Z0-9_]*)`)
	onConflictRe = regexp.MustCompile(`(?is)ON\s+CONFLICT`)
)

// FindUpserts walks the Go sources under root (a module root) and returns every
// string literal that carries both an INSERT INTO and an ON CONFLICT clause.
// Test files are skipped: their literals are fixtures, not production
// statements. The result is sorted by (File, Line) so callers are deterministic.
func FindUpserts(root string) ([]Upsert, error) {
	var out []Upsert

	walk := func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		if entry.IsDir() {
			if rel == "." {
				return nil
			}
			if _, ok := skippedDirNames[entry.Name()]; ok {
				return fs.SkipDir
			}
			if _, ok := skippedRelPaths[rel]; ok {
				return fs.SkipDir
			}
			return nil
		}

		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}

		found, err := upsertsInFile(path, rel)
		if err != nil {
			return err
		}
		out = append(out, found...)
		return nil
	}

	if err := filepath.WalkDir(root, walk); err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})

	return out, nil
}

func upsertsInFile(path, rel string) ([]Upsert, error) {
	fset := token.NewFileSet()
	// Mode 0 keeps comments out of the AST entirely — see the package doc.
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rel, err)
	}

	var out []Upsert
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}

		// Unquote handles both interpreted and raw (backtick) literals.
		sql, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}

		match := insertIntoRe.FindStringSubmatch(sql)
		if match == nil || !onConflictRe.MatchString(sql) {
			return true
		}

		out = append(out, Upsert{
			SQL:   sql,
			Table: match[1],
			File:  rel,
			Line:  fset.Position(lit.Pos()).Line,
		})
		return true
	})

	return out, nil
}
