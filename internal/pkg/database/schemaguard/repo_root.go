package schemaguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// RepoRoot returns the absolute path of the back/ module root. It is resolved
// from this file's compile-time location upwards to the directory holding
// go.mod, so callers do not depend on the working directory a test runs in.
func RepoRoot() (string, error) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("schemaguard: cannot resolve caller location")
	}

	dir := filepath.Dir(self)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("schemaguard: no go.mod above %s", filepath.Dir(self))
		}
		dir = parent
	}
}
