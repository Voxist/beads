//go:build cgo

package embeddeddolt_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedMigrationFreezeGate covers the store-open half of a
// MIGRATION-FREEZE on a real embedded database. It is the layer the CLI check
// cannot reach: a read command is let past that check by design, and its open
// used to run schema.MigrateUp unconditionally.
//
// One database, one regressed cursor, four opens — each pinning one clause of
// the contract:
//
//   - a strict Open under a freeze is REFUSED with *MigrationFrozenError;
//   - a non-strict open under a freeze SUCCEEDS and the cursor does not move;
//   - `bd migrate --force` (SetForceAllowRemoteMigrate) migrates through a
//     freeze, because --force names the designated migrator;
//   - with the freeze cleared, a strict Open migrates as it always did.
//
// The cursor assertion after the non-strict open is the one that mattered:
// before the gate it read LatestVersion() there, not LatestVersion()-1.
func TestEmbeddedMigrationFreezeGate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")
	schema.SetMigrationFrozen(false)
	schema.SetForceAllowRemoteMigrate(false)
	t.Cleanup(func() {
		schema.SetMigrationFrozen(false)
		schema.SetForceAllowRemoteMigrate(false)
	})

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	latest := schema.LatestVersion()
	behind := latest - 1

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "testdb"); err != nil {
		store.Close()
		t.Fatalf("SetConfig: %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		store.Close()
		t.Fatalf("Commit: %v", err)
	}
	store.Close()

	regress := func() {
		t.Helper()
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		if _, err := db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = ?", latest); err != nil {
			t.Fatalf("regress cursor: %v", err)
		}
		if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-am', 'test: regress schema cursor')"); err != nil {
			t.Fatalf("commit regressed cursor: %v", err)
		}
		_ = cleanup()
	}
	version := func() int {
		t.Helper()
		v, err := schemaVersion(ctx, dataDir, "testdb")
		if err != nil {
			t.Fatalf("schemaVersion: %v", err)
		}
		return v
	}

	regress()
	if got := version(); got != behind {
		t.Fatalf("fixture: cursor = %d, want %d", got, behind)
	}
	schema.SetMigrationFrozen(true)

	t.Run("strict open is refused", func(t *testing.T) {
		s, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
		if err == nil {
			s.Close()
			t.Fatal("Open under a freeze with pending work must fail")
		}
		if !schema.IsMigrationFrozenError(err) {
			t.Fatalf("error = %T (%v), want *schema.MigrationFrozenError", err, err)
		}
		if got := version(); got != behind {
			t.Errorf("a refused open must not migrate: cursor = %d, want %d", got, behind)
		}
	})

	t.Run("non-strict open continues on the current schema", func(t *testing.T) {
		s, err := embeddeddolt.OpenForWorkingSetReconcile(ctx, beadsDir, "testdb", "main")
		if err != nil {
			t.Fatalf("OpenForWorkingSetReconcile under a freeze must succeed: %v", err)
		}
		s.Close()
		if got := version(); got != behind {
			t.Errorf("the open must not migrate under a freeze: cursor = %d, want %d", got, behind)
		}
	})

	t.Run("--force migrates through the freeze", func(t *testing.T) {
		schema.SetForceAllowRemoteMigrate(true)
		defer schema.SetForceAllowRemoteMigrate(false)
		s, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
		if err != nil {
			t.Fatalf("Open with the designated-migrator override must succeed: %v", err)
		}
		s.Close()
		if got := version(); got != latest {
			t.Errorf("--force must migrate: cursor = %d, want %d", got, latest)
		}
	})

	t.Run("thawed, a strict open migrates as before", func(t *testing.T) {
		regress()
		schema.SetMigrationFrozen(false)
		s, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
		if err != nil {
			t.Fatalf("Open with no freeze: %v", err)
		}
		s.Close()
		if got := version(); got != latest {
			t.Errorf("no freeze must migrate: cursor = %d, want %d", got, latest)
		}
	})
}
