//go:build cgo

package main

import (
	"os"
	"testing"
)

// TestEmbeddedCountIncludeEphemeral covers the plane knob end to end, on the
// same fixture shape TestEmbeddedCountIncludeInfra uses.
//
// It is a SEPARATE file rather than a subtest of that function on purpose: this
// fork used to assert the wide behavior by editing upstream's test in place,
// which meant re-applying the edit on every resync and, once, forgetting the
// proxied twin — the two then disagreed on the same command until a resync
// surfaced it. Upstream's count tests are now byte-identical here, and this
// file carries what the fork actually needs.
//
// The distinction being pinned: --include-infra bundles four changes (see
// issueops.CountRequest.IncludeInfra) and its template exclusion silently drops
// template rows of the named type. --include-ephemeral admits the plane and
// nothing else.
func TestEmbeddedCountIncludeEphemeral(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ie")

	// 3 durable tasks (one closed), 2 no_history tasks, 1 ephemeral task.
	bdCreate(t, bd, dir, "durable task one", "--type", "task")
	bdCreate(t, bd, dir, "durable task two", "--type", "task")
	closed := bdCreate(t, bd, dir, "durable task closed", "--type", "task")
	bdClose(t, bd, dir, closed.ID)
	bdCreate(t, bd, dir, "nohistory task one", "--type", "task", "--no-history")
	bdCreate(t, bd, dir, "nohistory task two", "--type", "task", "--no-history")
	bdCreate(t, bd, dir, "ephemeral task", "--type", "task", "--ephemeral")

	countOf := func(args ...string) int {
		t.Helper()
		m := bdCountJSON(t, bd, dir, args...)
		return int(m["count"].(float64))
	}

	t.Run("default stays durable-only", func(t *testing.T) {
		if got := countOf("--type", "task"); got != 3 {
			t.Errorf("bd count --type task = %d, want 3 (upstream's default, unchanged)", got)
		}
	})

	t.Run("include-ephemeral reaches the wisps tier", func(t *testing.T) {
		if got := countOf("--type", "task", "--include-ephemeral"); got != 6 {
			t.Errorf("bd count --type task --include-ephemeral = %d, want 6 "+
				"(3 durable + 2 no_history + 1 ephemeral)", got)
		}
	})

	t.Run("count matches list cardinality under the same flag", func(t *testing.T) {
		listed := len(bdListJSON(t, bd, dir,
			"--type", "task", "--include-ephemeral", "--status", "all", "--limit", "0"))
		if got := countOf("--type", "task", "--include-ephemeral"); got != listed {
			t.Errorf("count = %d but list returned %d rows under the same filters", got, listed)
		}
	})
}
