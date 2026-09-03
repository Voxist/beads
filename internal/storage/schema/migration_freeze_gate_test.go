package schema

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestMigrationFreezeGateIsFreeWhenNotFrozen pins the cost of the gate on the
// common path: with no freeze set it must return nil without issuing a single
// query. sqlmock fails ExpectationsWereMet on any unexpected query, so a gate
// that "just checks" the cursor under no freeze would fail here.
func TestMigrationFreezeGateIsFreeWhenNotFrozen(t *testing.T) {
	SetMigrationFrozen(false)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	if err := checkMigrationFreezeGate(context.Background(), db); err != nil {
		t.Fatalf("no freeze: want nil, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no freeze must issue no queries: %v", err)
	}
}

// TestMigrationFrozenErrorShape pins the error's identity and wording: the
// store layers branch on IsMigrationFrozenError, and the message is what the
// operator reads on a refused open.
func TestMigrationFrozenErrorShape(t *testing.T) {
	e := &MigrationFrozenError{CurrentVersion: 60, LatestVersion: 66, Pending: 6}
	wrapped := fmt.Errorf("embeddeddolt: migrate: %w", e)
	if !IsMigrationFrozenError(wrapped) {
		t.Fatal("IsMigrationFrozenError must see through wrapping")
	}
	if IsMigrationFrozenError(errors.New("something else")) {
		t.Fatal("IsMigrationFrozenError must not match an unrelated error")
	}
	for _, want := range []string{"MIGRATION-FREEZE", "6 pending schema migrations", "v60 -> v66"} {
		if !strings.Contains(e.Error(), want) {
			t.Errorf("Error() must contain %q; got %q", want, e.Error())
		}
	}
	one := &MigrationFrozenError{CurrentVersion: 65, LatestVersion: 66, Pending: 1}
	if !strings.Contains(one.Error(), "1 pending schema migration ") {
		t.Errorf("singular form expected; got %q", one.Error())
	}
}
