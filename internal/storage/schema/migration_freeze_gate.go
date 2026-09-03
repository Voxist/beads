package schema

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// migrationFrozen is the process-local record that a MIGRATION-FREEZE sentinel
// is active. Set by SetMigrationFrozen in cmd/bd's root PersistentPreRunE,
// before autoMigrateOnVersionBump and before the main store open — the same
// spot, and the same shape, as forceAllowRemoteMigrate. Like that flag it is
// deliberately not an environment variable: it cannot leak into child
// processes (git hooks, dolt subprocesses), and the storage layer never has to
// know where the sentinel lives.
var migrationFrozen bool

// SetMigrationFrozen records (or clears) an active migration freeze for this
// process. While set, MigrateUp refuses to apply pending schema migrations on
// every store-open path — embedded, server and proxied all funnel through it —
// and returns *MigrationFrozenError instead. External test packages reset it
// to false after each case.
//
// This is the half of the freeze the CLI-layer check cannot provide. That
// check refuses WRITE commands before the store opens; a READ is let through
// so diagnosis keeps working, and its store open used to run MigrateUp
// unconditionally — so `bd list` under an active freeze still migrated the
// database it was meant to leave alone.
func SetMigrationFrozen(v bool) { migrationFrozen = v }

// MigrationFrozenError is returned by MigrateUp when a migration freeze is
// active and the database has pending schema work.
type MigrationFrozenError struct {
	CurrentVersion int
	LatestVersion  int
	Pending        int
}

func (e *MigrationFrozenError) Error() string {
	unit := "migrations"
	if e.Pending == 1 {
		unit = "migration"
	}
	return fmt.Sprintf("refusing to apply %d pending schema %s while a MIGRATION-FREEZE is active (v%d -> v%d): the workspace is frozen for migration",
		e.Pending, unit, e.CurrentVersion, e.LatestVersion)
}

// IsMigrationFrozenError reports whether err wraps a *MigrationFrozenError.
func IsMigrationFrozenError(err error) bool {
	var e *MigrationFrozenError
	return errors.As(err, &e)
}

// checkMigrationFreezeGate is MigrateUp's first act. It returns nil in the
// common case — no freeze — without touching the database at all, so an
// ordinary open pays nothing for it. Under a freeze it reads whether any
// migration work is pending and, if so, refuses before ANY write: before the
// dolt_ignore seed, before pre-existing tables are unstaged, before the first
// migration step. A frozen database that is already at the binary's version
// falls through to the normal path and behaves exactly as it did.
//
// `bd migrate --force` names this process the designated migrator and is the
// override for the remote gate already; it overrides the freeze the same way,
// so one flag means one thing.
func checkMigrationFreezeGate(ctx context.Context, db DBConn) error {
	if !migrationFrozen {
		return nil
	}
	needed, err := migrationWorkNeeded(ctx, db)
	if err != nil {
		return fmt.Errorf("migration-freeze gate: checking pending work: %w", err)
	}
	if !needed {
		return nil
	}
	current, err := CurrentVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("migration-freeze gate: read current version: %w", err)
	}
	pending, err := PendingVersions(ctx, db)
	if err != nil {
		return fmt.Errorf("migration-freeze gate: read pending versions: %w", err)
	}
	if forceAllowRemoteMigrate {
		fmt.Fprintf(os.Stderr,
			"Warning: applying %d pending schema migration(s) under an active MIGRATION-FREEZE (bd migrate --force): this process is the designated migrator\n",
			len(pending))
		return nil
	}
	return &MigrationFrozenError{
		CurrentVersion: current,
		LatestVersion:  LatestVersion(),
		Pending:        len(pending),
	}
}
