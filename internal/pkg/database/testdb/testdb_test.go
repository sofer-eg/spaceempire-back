package testdb

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUnit_TestDB_DSNForDB covers the shapes container.ConnectionString can
// return, plus the one input that parses but must not be accepted.
func TestUnit_TestDB_DSNForDB(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		dsn  string
		want string
		err  bool
	}{
		{
			name: "host port and query preserved",
			dsn:  "postgres://test:test@localhost:33704/spaceempire_tmpl?sslmode=disable",
			want: "postgres://test:test@localhost:33704/testdb_1?sslmode=disable",
		},
		{
			name: "ipv6 host and escaped password preserved",
			dsn:  "postgres://test:p%40ss@[::1]:5432/spaceempire_tmpl?sslmode=disable",
			want: "postgres://test:p%40ss@[::1]:5432/testdb_1?sslmode=disable",
		},
		{
			// url.Parse accepts this and leaves everything in Path, so without
			// the scheme/host check the result would be a plausible-looking
			// "/testdb_1" that connects nowhere.
			name: "keyword value dsn rejected",
			dsn:  "host=localhost user=test dbname=spaceempire_tmpl",
			err:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := dsnForDB(tc.dsn, "testdb_1")
			if tc.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestUnit_TestDB_MakefileFiltersMatchLabels keeps the Makefile's cleanup
// filters and the labels stamped here from drifting apart. Drift does not fail
// anything: the filters simply stop matching, and leaked containers return
// silently — the very failure mode this package exists to prevent.
func TestUnit_TestDB_MakefileFiltersMatchLabels(t *testing.T) {
	t.Parallel()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller")
	// internal/pkg/database/testdb -> back
	makefile := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "Makefile")

	body, err := os.ReadFile(makefile)
	require.NoError(t, err, "read Makefile")
	text := string(body)

	assert.True(t, strings.Contains(text, LabelKey+"="+LabelValue),
		"Makefile must filter on the project label %s=%s", LabelKey, LabelValue)
	assert.True(t, strings.Contains(text, RunLabelKey),
		"Makefile must filter on the run label %s", RunLabelKey)
	assert.True(t, strings.Contains(text, RunIDEnv+"="),
		"Makefile must export the run id as %s", RunIDEnv)
}
