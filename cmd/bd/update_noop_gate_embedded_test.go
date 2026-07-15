//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// These tests pin the no-op-commit gate end-to-end (va-v1i9 / ADR-0023 L-A):
// a `bd update` that changes no tracked field must mint ZERO new Dolt
// commits, and a real field change must mint exactly one. Before the gate,
// every no-op update dirtied the working set (updated_at bump + event row)
// and each auto-commit minted a real commit — the "no-op commit storm"
// (observed live: ~13 identical-content_hash commits per bead).

func countDoltCommits(t *testing.T, beadsDir string) int {
	t.Helper()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&n); err != nil {
		t.Fatalf("query dolt_log: %v", err)
	}
	return n
}

func TestEmbeddedUpdateNoOpMintsNoDoltCommit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt update tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ng")
	issue := bdCreate(t, bd, dir, "No-op gate test", "--type", "task")

	// Real change first so the repeated flags below are guaranteed no-ops.
	bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--priority", "1")
	before := countDoltCommits(t, beadsDir)

	// Identical values: must not advance dolt_log at all.
	bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice", "--priority", "1")
	if after := countDoltCommits(t, beadsDir); after != before {
		t.Fatalf("no-op update minted %d new dolt commit(s); want 0", after-before)
	}

	// Values must be intact after the suppressed write.
	got := bdShow(t, bd, dir, issue.ID)
	if got.Assignee != "alice" || got.Priority != 1 {
		t.Fatalf("no-op update corrupted values: assignee=%q priority=%d", got.Assignee, got.Priority)
	}
}

func TestEmbeddedUpdateNoOpSetMetadataMintsNoDoltCommit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt update tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "nm")
	issue := bdCreate(t, bd, dir, "No-op metadata gate test", "--type", "task")

	// The storm's dominant shape: a router re-stamping gc.routed_to with the
	// value it already holds.
	bdUpdate(t, bd, dir, issue.ID, "--set-metadata", "gc.routed_to=voxist-platform/voxist.executor")
	before := countDoltCommits(t, beadsDir)

	bdUpdate(t, bd, dir, issue.ID, "--set-metadata", "gc.routed_to=voxist-platform/voxist.executor")
	if after := countDoltCommits(t, beadsDir); after != before {
		t.Fatalf("no-op --set-metadata minted %d new dolt commit(s); want 0", after-before)
	}

	got := bdShow(t, bd, dir, issue.ID)
	if want := `"gc.routed_to"`; !strings.Contains(string(got.Metadata), want) {
		t.Fatalf("metadata lost after no-op update: %s", got.Metadata)
	}
}

func TestEmbeddedUpdateNoOpStatusMintsNoDoltCommit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt update tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ns")
	issue := bdCreate(t, bd, dir, "No-op status gate test", "--type", "task")

	// First transition is real (status change + started_at stamp).
	bdUpdate(t, bd, dir, issue.ID, "--status", "in_progress")
	before := countDoltCommits(t, beadsDir)

	// Same status again: started_at is already set, nothing changes.
	bdUpdate(t, bd, dir, issue.ID, "--status", "in_progress")
	if after := countDoltCommits(t, beadsDir); after != before {
		t.Fatalf("no-op --status minted %d new dolt commit(s); want 0", after-before)
	}
}

func TestEmbeddedUpdateRealChangeMintsExactlyOneDoltCommit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt update tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "nr")
	issue := bdCreate(t, bd, dir, "Real change commit test", "--type", "task")
	before := countDoltCommits(t, beadsDir)

	bdUpdate(t, bd, dir, issue.ID, "--title", "Renamed exactly once")

	if after := countDoltCommits(t, beadsDir); after != before+1 {
		t.Fatalf("real update minted %d new dolt commit(s); want exactly 1", after-before)
	}
	got := bdShow(t, bd, dir, issue.ID)
	if got.Title != "Renamed exactly once" {
		t.Fatalf("title = %q, want the updated title", got.Title)
	}
}
