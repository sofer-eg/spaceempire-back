package testdb

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// repoRoot is back/, four levels up from this package. Derived from the working
// directory (go test runs a package's tests in its own directory) rather than
// runtime.Caller, whose path is baked in at compile time and is relative under
// -trimpath.
const repoRoot = "../../../.."

// dryRun expands a make target without running it. `make -n` prints even
// @-prefixed recipe lines, so the expansion is fully visible.
//
// It does, however, execute any recipe line containing $(MAKE) — including the
// `go test ./...` sharing that line. The recipes here inline their cleanup so
// that cannot happen, but a future edit could bring the sub-make back, and this
// guard must not become a way to launch the whole integration suite. A stub
// `go` first on PATH keeps that harmless (measured: 156s -> instant).
func dryRun(t *testing.T, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not installed")
	}

	stub := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(stub, "go"), []byte("#!/bin/sh\nexit 0\n"), 0o755))

	cmd := exec.Command("make", append([]string{"-C", repoRoot, "-n"}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "make -n %v: %s", args, out)
	return string(out)
}

// TestUnit_TestDB_MakefileReapsWhatItStamps pins the wiring between the labels
// stamped here and the filters the Makefile cleans up with. Both bugs this
// guards against are silent: the filter stops matching, the reaper reports
// success having found nothing, and leaked containers come back.
//
// It asserts on the expanded recipe rather than on the file's text, because the
// defect that actually happened — the run id being re-evaluated in a sub-make,
// so that containers were stamped with one id and reaped by another — leaves
// every expected substring present in the file.
func TestUnit_TestDB_MakefileReapsWhatItStamps(t *testing.T) {
	t.Parallel()

	// Both targets run TestIntegration_ tests and both must clean up after
	// themselves; the wiring is duplicated in the Makefile, so check both.
	for _, target := range []string{"test", "test-integration"} {
		t.Run(target+" stamps and reaps the same id", func(t *testing.T) {
			t.Parallel()
			// Deliberately no TEST_RUN_ID on the command line: command-line
			// variables propagate to sub-makes through MAKEFLAGS, so supplying
			// one would paper over exactly the defect this asserts against — the
			// id being re-evaluated wherever the cleanup runs. The id has to come
			// from the Makefile's own `date`, and both places must show that one.
			//
			// That also makes this subtest a Linux guard: it can only tell two
			// evaluations apart while they differ, and `date +%N` is a GNU
			// extension. Where %N is unsupported, two evaluations inside the same
			// second are identical and the subtest goes blind.
			out := dryRun(t, target)

			stamped := regexp.MustCompile(RunIDEnv + `=(\S+)`).FindStringSubmatch(out)
			require.Len(t, stamped, 2, "the test run must receive an id as %s:\n%s", RunIDEnv, out)

			assert.Contains(t, out, RunLabelKey+"="+stamped[1],
				"the cleanup must filter on the id the containers were stamped with (%s)", stamped[1])

			// The cleanup must also be told whether the run failed. Without it
			// the script takes the single-pass branch, and containers a killed
			// binary was still creating survive the sweep — silently, with every
			// other assertion here still green.
			assert.Regexp(t, `reap-test-containers\.sh "`+regexp.QuoteMeta(RunLabelKey+"="+stamped[1])+`" \$status`, out,
				"the cleanup must receive the test run's exit status")
		})
	}

	t.Run("manual sweep filters on the project label", func(t *testing.T) {
		t.Parallel()
		out := dryRun(t, "test-clean")
		assert.Contains(t, out, LabelKey+"="+LabelValue)
	})

	t.Run("project label cannot be overridden from the command line", func(t *testing.T) {
		t.Parallel()
		// Without `override` in the Makefile this sweeps every project's
		// testcontainers off the host. := does not prevent it.
		out := dryRun(t, "test-clean", "TEST_LABEL=org.testcontainers=true")
		assert.Contains(t, out, LabelKey+"="+LabelValue)
		assert.NotContains(t, out, "org.testcontainers=true")
	})
}
