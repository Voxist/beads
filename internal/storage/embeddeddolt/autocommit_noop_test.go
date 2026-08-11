//go:build cgo

package embeddeddolt_test

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestWorkingSetIsOperationalNoOp exercises the vp-on8s heartbeat-flood guard.
// A working set whose only pending changes are lease/liveness columns on an
// issue row is a semantic no-op that the auto-commit path must skip; any real
// change (content, another table, an added/removed row) must not be skipped.
func TestWorkingSetIsOperationalNoOp(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("heartbeat is a no-op", func(t *testing.T) {
		te := newTestEnv(t, "noop_hb")
		ctx := t.Context()

		issue := &types.Issue{
			ID: "noop-hb-1", Title: "heartbeat me",
			Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "seeder"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		claimCtx := issueops.WithLeaseTTL(ctx, time.Second)
		if err := te.store.ClaimIssue(claimCtx, "noop-hb-1", "alice"); err != nil {
			t.Fatalf("ClaimIssue: %v", err)
		}
		// Commit create+claim so HEAD is a clean baseline.
		if err := te.store.Commit(ctx, "seed claimed issue"); err != nil {
			t.Fatalf("Commit baseline: %v", err)
		}

		// Since migration 0055 (bd-lrgn1) a heartbeat writes only the
		// dolt_ignored leases table, so it leaves NO pending working set at
		// all — the schema-level resolution of the same incident this guard
		// was built for, and a stronger property than the guard's skip.
		if err := te.store.HeartbeatIssue(claimCtx, "noop-hb-1", "alice"); err != nil {
			t.Fatalf("HeartbeatIssue: %v", err)
		}

		// The classifier reports false for a clean set by contract (nothing
		// to skip); the load-bearing assertion is that no commit is minted.
		noop, err := te.store.WorkingSetIsOperationalNoOp(ctx)
		if err != nil {
			t.Fatalf("WorkingSetIsOperationalNoOp: %v", err)
		}
		if noop {
			t.Fatal("heartbeat left an operational-only working set; leases have leaked back into a versioned table")
		}
		head, err := te.store.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("GetCurrentCommit: %v", err)
		}
		committed, err := te.store.CommitPending(ctx, "hb-check")
		if err != nil {
			t.Fatalf("CommitPending: %v", err)
		}
		if committed {
			t.Fatal("heartbeat produced a pending working set; want none (heartbeats must mint no commits, bd-lrgn1/vp-on8s)")
		}
		if after, err := te.store.GetCurrentCommit(ctx); err != nil || after != head {
			t.Fatalf("HEAD moved across a heartbeat: %s -> %s (err=%v)", head, after, err)
		}
	})

	t.Run("content change is not a no-op", func(t *testing.T) {
		te := newTestEnv(t, "noop_edit")
		ctx := t.Context()

		issue := &types.Issue{
			ID: "noop-edit-1", Title: "original",
			Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "seeder"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if err := te.store.Commit(ctx, "seed"); err != nil {
			t.Fatalf("Commit baseline: %v", err)
		}

		if err := te.store.UpdateIssue(ctx, "noop-edit-1", map[string]interface{}{"title": "changed"}, "editor"); err != nil {
			t.Fatalf("UpdateIssue: %v", err)
		}

		noop, err := te.store.WorkingSetIsOperationalNoOp(ctx)
		if err != nil {
			t.Fatalf("WorkingSetIsOperationalNoOp: %v", err)
		}
		if noop {
			t.Fatal("title change: got noop=true, want false")
		}
	})

	t.Run("clean working set is not a no-op", func(t *testing.T) {
		te := newTestEnv(t, "noop_clean")
		ctx := t.Context()

		issue := &types.Issue{
			ID: "noop-clean-1", Title: "seed",
			Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "seeder"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if err := te.store.Commit(ctx, "seed"); err != nil {
			t.Fatalf("Commit baseline: %v", err)
		}

		// Nothing pending: a clean working set is not something to skip.
		noop, err := te.store.WorkingSetIsOperationalNoOp(ctx)
		if err != nil {
			t.Fatalf("WorkingSetIsOperationalNoOp: %v", err)
		}
		if noop {
			t.Fatal("clean working set: got noop=true, want false")
		}
	})
}
