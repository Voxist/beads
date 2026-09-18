package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/migration"
)

// The two fork-only pieces of the MIGRATION-FREEZE refusal, pinned at the unit
// level so they do not depend on the embedded e2e tests (which skip unless
// BEADS_TEST_EMBEDDED_DOLT=1).

func exitCodeOf(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return -1
}

func TestMigrationFreezeRefusalForceOverride(t *testing.T) {
	marker := filepath.Join(t.TempDir(), migration.FileName)
	if err := os.WriteFile(marker, []byte("kb\t2026-09-18T10:00:00Z\tresync\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readable := migration.Result{Path: marker}
	unreadable := migration.Result{Err: errors.New("permission denied")}

	reset := func() { migrateForceOverridesFreeze, freezeOverrideWarned = false, false }
	t.Cleanup(reset)

	t.Run("no override refuses a readable freeze", func(t *testing.T) {
		reset()
		if got := exitCodeOf(migrationFreezeRefusal("create", readable)); got != ExitMigrationFrozen {
			t.Fatalf("exit = %d, want %d", got, ExitMigrationFrozen)
		}
	})

	t.Run("--force passes a readable freeze and warns once", func(t *testing.T) {
		reset()
		migrateForceOverridesFreeze = true
		for i := 0; i < 2; i++ { // pre-run and CheckReadonly both consult it
			if err := migrationFreezeRefusal("migrate", readable); err != nil {
				t.Fatalf("call %d: --force must pass a readable freeze, got %v", i, err)
			}
		}
		if !freezeOverrideWarned {
			t.Fatal("the override must record that it warned, so the second call stays silent")
		}
	})

	t.Run("--force does not pass an unreadable marker", func(t *testing.T) {
		reset()
		migrateForceOverridesFreeze = true
		if got := exitCodeOf(migrationFreezeRefusal("migrate", unreadable)); got != ExitMigrationFrozen {
			t.Fatalf("an undeterminable freeze must still refuse under --force: exit = %d, want %d", got, ExitMigrationFrozen)
		}
	})

	t.Run("no freeze is not an error either way", func(t *testing.T) {
		reset()
		migrateForceOverridesFreeze = true
		if err := migrationFreezeRefusal("migrate", migration.Result{}); err != nil {
			t.Fatalf("got %v", err)
		}
	})
}

// Not parallel: t.Setenv. TestMain pins migration.EnvFreezeFile, which is
// authoritative and would hide the walk; clearing it re-enables the walk.
func TestTownFreezeRootsFindsATownMarkerFromOutsideTheTree(t *testing.T) {
	t.Setenv(migration.EnvFreezeFile, "")
	t.Setenv("GT_ROOT", "")

	town := t.TempDir()
	outside := t.TempDir() // cwd and workspace both outside the town
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(town, "mayor", "town.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(town, migration.FileName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(outside)

	t.Setenv("GT_TOWN_ROOT", town)
	if res := migration.Find(append([]string{filepath.Join(outside, ".beads")}, townFreezeRoots()...)...); !res.Frozen() {
		t.Fatal("a town freeze must be found through GT_TOWN_ROOT when cwd and workspace are outside the town")
	}
	// Same through the production root list every write gate uses
	// (freezeSearchRoots -> freezeRootsWith -> CheckReadonly, init, bootstrap,
	// import), so dropping townFreezeRoots from it fails here too.
	t.Setenv("BEADS_DIR", filepath.Join(outside, ".beads"))
	if res := migration.Find(freezeSearchRoots()...); !res.Frozen() {
		t.Fatal("freezeSearchRoots must carry the GT_TOWN_ROOT town root")
	}

	// A directory that is not a town (no mayor/town.json) is not consulted:
	// the variable alone must not let an arbitrary path freeze bd.
	notTown := t.TempDir()
	if err := os.WriteFile(filepath.Join(notTown, migration.FileName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_TOWN_ROOT", notTown)
	if roots := townFreezeRoots(); len(roots) != 0 {
		t.Fatalf("townFreezeRoots() = %v, want none for a directory without mayor/town.json", roots)
	}
}
