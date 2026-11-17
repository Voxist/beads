//go:build cgo

package embeddeddolt_test

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestCountsIncludeWispDependencies is the regression guard for the PR #4010
// review (be-xbl83). The Count* methods that feed `bd show --json`
// (DependentCount/DependencyCount) must include `wisp_dependencies` edges,
// matching the slice-path relationship APIs (GetDependentsWithMetadata) that
// the human-readable `bd show` already uses.
//
// Before the fix, CountDependents/CountDependencies/CountDependentsByStatus
// queried only the `dependencies` table, so a permanent issue with a wisp
// dependent reported a too-small dependent_count — contradicting the
// dependents the same tool returns elsewhere.
func TestCountsIncludeWispDependencies(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "ec")
	ctx := t.Context()

	// ec-perm-dep --blocks--> ec-target   (edge in `dependencies`)
	// ec-wisp-dep --blocks--> ec-target   (edge in `wisp_dependencies`)
	for _, issue := range []*types.Issue{
		{ID: "ec-target", Title: "target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "ec-perm-dep", Title: "perm dependent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "ec-wisp-dep", Title: "wisp dependent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "ec-perm-dep", DependsOnID: "ec-target", Type: types.DepBlocks},
		{IssueID: "ec-wisp-dep", DependsOnID: "ec-target", Type: types.DepBlocks},
	} {
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	// Precondition: the slice path bd show's human output uses counts both.
	deps, err := te.store.GetDependentsWithMetadata(ctx, "ec-target")
	if err != nil {
		t.Fatalf("GetDependentsWithMetadata: %v", err)
	}
	if len(deps) != 2 {
		t.Fatalf("precondition: GetDependentsWithMetadata(ec-target) = %d, want 2", len(deps))
	}

	if n, err := te.store.CountDependents(ctx, "ec-target"); err != nil {
		t.Fatalf("CountDependents: %v", err)
	} else if n != 2 {
		t.Errorf("CountDependents(ec-target) = %d, want 2 (1 perm + 1 wisp); wisp_dependencies edge dropped", n)
	}

	if n, err := te.store.CountDependentsByStatus(ctx, "ec-target", types.StatusOpen); err != nil {
		t.Fatalf("CountDependentsByStatus: %v", err)
	} else if n != 2 {
		t.Errorf("CountDependentsByStatus(ec-target, open) = %d, want 2", n)
	}

	if n, err := te.store.CountDependencies(ctx, "ec-wisp-dep"); err != nil {
		t.Fatalf("CountDependencies: %v", err)
	} else if n != 1 {
		t.Errorf("CountDependencies(ec-wisp-dep) = %d, want 1 (edge lives in wisp_dependencies)", n)
	}
}

// TestSearchIssuesWithCountsFilteredLookupEmbedded runs the vp-6y90n two-phase
// counts search (ids first, then the by-IDs hydration) on the embedded engine,
// which shares issueops.SearchIssuesWithCountsInTx with the server store, and
// checks the session_name lookup it was built for against a hand-written
// oracle: the matching wisp's labels, outgoing and incoming blocks edges
// (including a wisp_dependencies edge onto it) and comment count.
func TestSearchIssuesWithCountsFilteredLookupEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "tp")
	ctx := t.Context()

	for _, issue := range []*types.Issue{
		{ID: "tp-mayor", Title: "mayor", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true, Metadata: []byte(`{"session_name":"mayor"}`)},
		{ID: "tp-deacon", Title: "deacon", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true, Metadata: []byte(`{"session_name":"deacon"}`)},
		{ID: "tp-target", Title: "target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	for _, lb := range []struct{ id, label string }{{"tp-mayor", "b"}, {"tp-mayor", "a"}, {"tp-deacon", "c"}} {
		if err := te.store.AddLabel(ctx, lb.id, lb.label, "tester"); err != nil {
			t.Fatalf("AddLabel %s/%s: %v", lb.id, lb.label, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "tp-mayor", DependsOnID: "tp-target", Type: types.DepBlocks},
		{IssueID: "tp-deacon", DependsOnID: "tp-mayor", Type: types.DepBlocks},
	} {
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}
	// AddComment records a comment event; ImportIssueComment writes the
	// comment row that comment_count counts.
	if _, err := te.store.ImportIssueComment(ctx, "tp-mayor", "tester", "hello", time.Now().UTC()); err != nil {
		t.Fatalf("ImportIssueComment: %v", err)
	}

	got, err := te.store.SearchIssuesWithCounts(ctx, "", types.IssueFilter{MetadataFields: map[string]string{"session_name": "mayor"}})
	if err != nil {
		t.Fatalf("SearchIssuesWithCounts: %v", err)
	}
	if len(got) != 1 || got[0].Issue.ID != "tp-mayor" {
		t.Fatalf("session_name lookup returned %d rows, want exactly tp-mayor", len(got))
	}
	iwc := got[0]
	if labels := strings.Join(iwc.Issue.Labels, ","); labels != "a,b" {
		t.Errorf("labels = %q, want a,b", labels)
	}
	if iwc.DependencyCount != 1 || iwc.DependentCount != 1 || iwc.CommentCount != 1 {
		t.Errorf("counts dep=%d rdep=%d comments=%d, want 1/1/1", iwc.DependencyCount, iwc.DependentCount, iwc.CommentCount)
	}

	none, err := te.store.SearchIssuesWithCounts(ctx, "", types.IssueFilter{MetadataFields: map[string]string{"session_name": "nobody"}})
	if err != nil {
		t.Fatalf("SearchIssuesWithCounts (no match): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("no-match lookup returned %d rows", len(none))
	}
}
