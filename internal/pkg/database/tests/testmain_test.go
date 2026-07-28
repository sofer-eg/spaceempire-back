package database_test

import (
	"testing"

	"spaceempire/back/internal/pkg/database/testdb"
)

// TestMain terminates the package's shared Postgres testcontainer after the
// last test. testdb.Setup starts it lazily on first use.
func TestMain(m *testing.M) { testdb.Main(m) }
