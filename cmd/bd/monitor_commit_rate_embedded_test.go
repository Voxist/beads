//go:build cgo

package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// These tests pin the commit-rate watchdog (vp-5u7i deliverable 2, ADR-0023
// L-A local backstop): analyzeCommitPatterns must fire on a synthetic no-op
// commit burst regardless of its source — the write-path gate (see
// update_noop_gate_embedded_test.go) covers bd's own no-op updates, so this
// backstop is deliberately exercised via raw SQL that bypasses bd entirely,
// the same way an unrelated writer (a stray script, a supervisor runaway)
// would produce the storm. It must also stay quiet on ordinary activity
// spread across many beads.

func openEmbeddedRawDB(t *testing.T, beadsDir string) *sql.DB {
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
	t.Cleanup(func() { _ = cleanup() })
	return db
}

// commitNoOpChange bumps updated_at only — content_hash untouched — then
// commits it, reproducing the pre-gate storm shape (real row diff, identical
// content_hash) directly at the SQL layer.
func commitNoOpChange(t *testing.T, db *sql.DB, id string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "UPDATE issues SET updated_at = ? WHERE id = ?", at, id); err != nil {
		t.Fatalf("synthetic no-op update: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)", fmt.Sprintf("bd: update %s", id)); err != nil {
		t.Fatalf("synthetic no-op commit: %v", err)
	}
}

func currentDatabase(t *testing.T, db *sql.DB) string {
	t.Helper()
	var name string
	if err := db.QueryRowContext(t.Context(), "SELECT DATABASE()").Scan(&name); err != nil {
		t.Fatalf("SELECT DATABASE(): %v", err)
	}
	return name
}

func TestAnalyzeCommitPatternsDetectsNoOpStorm(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt monitor tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mc")
	issue := bdCreate(t, bd, dir, "Storm target", "--type", "task")

	db := openEmbeddedRawDB(t, beadsDir)
	base := time.Now().UTC()
	for i := 0; i < 12; i++ {
		commitNoOpChange(t, db, issue.ID, base.Add(time.Duration(i)*time.Second))
	}

	result, err := analyzeCommitPatterns(t.Context(), db, currentDatabase(t, db), 5, 10)
	if err != nil {
		t.Fatalf("analyzeCommitPatterns: %v", err)
	}
	if !result.ExcessiveNoOpsDetected {
		t.Fatalf("want storm detected, got %+v", result)
	}
	if result.DistinctBeadCount != 1 {
		t.Fatalf("DistinctBeadCount = %d, want 1 (all 12 no-op commits hit the same bead)", result.DistinctBeadCount)
	}
	if result.CommitCount < 10 {
		t.Fatalf("CommitCount = %d, want >= 10", result.CommitCount)
	}
}

func TestAnalyzeCommitPatternsIgnoresDistinctContentAcrossManyBeads(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt monitor tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mn")

	for i := 0; i < 5; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("Normal bead %d", i), "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--title", fmt.Sprintf("Normal bead %d renamed", i))
	}

	db := openEmbeddedRawDB(t, beadsDir)
	result, err := analyzeCommitPatterns(t.Context(), db, currentDatabase(t, db), 5, 10)
	if err != nil {
		t.Fatalf("analyzeCommitPatterns: %v", err)
	}
	if result.ExcessiveNoOpsDetected {
		t.Fatalf("want no storm on distinct-content activity across many beads, got %+v", result)
	}
	if result.CommitCount != 0 {
		t.Fatalf("CommitCount = %d, want 0 (every commit changed real content)", result.CommitCount)
	}
}

func TestAnalyzeCommitPatternsBelowThresholdNoAlert(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt monitor tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mb")
	issue := bdCreate(t, bd, dir, "Small burst", "--type", "task")

	db := openEmbeddedRawDB(t, beadsDir)
	base := time.Now().UTC()
	for i := 0; i < 3; i++ {
		commitNoOpChange(t, db, issue.ID, base.Add(time.Duration(i)*time.Second))
	}

	result, err := analyzeCommitPatterns(t.Context(), db, currentDatabase(t, db), 5, 10)
	if err != nil {
		t.Fatalf("analyzeCommitPatterns: %v", err)
	}
	if result.ExcessiveNoOpsDetected {
		t.Fatalf("want no alert below the commit-count threshold, got %+v", result)
	}
	if result.CommitCount != 3 {
		t.Fatalf("CommitCount = %d, want 3", result.CommitCount)
	}
}
