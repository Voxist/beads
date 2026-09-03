//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/migration"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// embeddedSchemaCursor reads the recorded schema version of an embedded
// database from outside bd, so an assertion about "did bd migrate?" does not
// depend on bd's own reporting.
func embeddedSchemaCursor(t *testing.T, beadsDir, database string) int {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	var v int
	if err := db.QueryRowContext(t.Context(), "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&v); err != nil {
		t.Fatalf("read schema cursor: %v", err)
	}
	return v
}

// TestEmbeddedMigrationFreezeStopsAReadFromMigrating is the scenario that was
// measured before this change, verbatim: a database behind the binary, a
// MIGRATION-FREEZE in .beads, and `bd list`. The cursor went 60 -> 66. It is
// the exact hole that made BD_NO_AUTO_MIGRATE a placebo — every read's store
// open ran schema.MigrateUp, and no gate consulted the freeze.
//
// The three steps are one story: the read must not migrate; the designated
// migrator (--force) must be able to; and --force must NOT thaw the workspace
// as a side effect, or the next agent's write would race in behind it.
func TestEmbeddedMigrationFreezeStopsAReadFromMigrating(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "frz")
	latest := schema.LatestVersion()

	regressEmbeddedSchemaCursor(t, beadsDir, "frz")
	if got := embeddedSchemaCursor(t, beadsDir, "frz"); got != latest-1 {
		t.Fatalf("fixture: cursor = %d, want %d", got, latest-1)
	}

	sentinel := filepath.Join(beadsDir, migration.FileName)
	if err := os.WriteFile(sentinel, []byte("kb\t2026-09-03T10:00:00Z\tschema bump"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	t.Run("a read under a freeze does not migrate", func(t *testing.T) {
		out := bdRunOK(t, bd, dir, "list")
		if got := embeddedSchemaCursor(t, beadsDir, "frz"); got != latest-1 {
			t.Fatalf("bd list under a freeze migrated the database: cursor = %d, want %d\n%s", got, latest-1, out)
		}
		if !strings.Contains(out, "frozen") {
			t.Errorf("the read should say why it is running on an old schema; got:\n%s", out)
		}
	})

	t.Run("the designated migrator migrates through it", func(t *testing.T) {
		out := bdRunOK(t, bd, dir, "migrate", "--force")
		if got := embeddedSchemaCursor(t, beadsDir, "frz"); got != latest {
			t.Fatalf("bd migrate --force under a freeze must migrate: cursor = %d, want %d\n%s", got, latest, out)
		}
		if !strings.Contains(out, "designated migrator") {
			t.Errorf("--force through a freeze must say so; got:\n%s", out)
		}
	})

	t.Run("--force does not thaw the workspace", func(t *testing.T) {
		if _, err := os.Stat(sentinel); err != nil {
			t.Fatalf("the sentinel must survive a forced migrate: %v", err)
		}
		out, code := bdRunFailCode(t, bd, dir, "create", "still frozen", "--type", "task")
		if code != 1 || !strings.Contains(out, "frozen for migration") {
			t.Errorf("a write after a forced migrate must still be refused while the sentinel exists; exit=%d\n%s", code, out)
		}
	})
}
