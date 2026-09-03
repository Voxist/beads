//go:build cgo

package main

import (
	"os"
	"testing"
)

// TestEmbeddedCountInfraTypeAgreesWithList pins the user-visible half of the
// count/list plane divergence, end to end on a real backend.
//
// An infra type is ALWAYS written to the wisps table (dolt's useWispsTable
// includes IsInfraType). BuildListFilter admits that plane when an infra type
// is named; BuildCountFilter's arm was a bare `else`, so it did not — and
// `bd count --type agent` answered 0 while `bd list --type agent` returned the
// rows. Nothing said a whole table had gone unread.
//
// TestCountAndListPlaneAgreement pins the same thing at the filter level. This
// exists because a filter-level assertion cannot show the number a user
// actually sees, and the number is the complaint.
func TestEmbeddedCountInfraTypeAgreesWithList(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ci")

	// `agent` is an INFRA type by default (domain.DefaultInfraTypes) but not a
	// built-in ISSUE type, so it has to be registered before a bead can be
	// created with it. That split is the point: infra-ness decides the plane
	// on write, registration decides whether create accepts the word.
	bdRunOK(t, bd, dir, "config", "set", "types.custom", "agent")

	// Two infra beads and three ordinary durable tasks. The tasks are here so
	// a fix that simply admitted the plane for EVERY named type would be
	// caught by the last subtest rather than passing quietly.
	bdCreate(t, bd, dir, "agent one", "--type", "agent")
	bdCreate(t, bd, dir, "agent two", "--type", "agent")
	bdCreate(t, bd, dir, "durable task one", "--type", "task")
	bdCreate(t, bd, dir, "durable task two", "--type", "task")
	bdCreate(t, bd, dir, "durable task three", "--type", "task")

	countOf := func(args ...string) int {
		t.Helper()
		m := bdCountJSON(t, bd, dir, args...)
		return int(m["count"].(float64))
	}

	t.Run("count matches list for an infra type", func(t *testing.T) {
		listed := len(bdListJSON(t, bd, dir, "--type", "agent", "--all", "--limit", "0"))
		counted := countOf("--type", "agent")
		if listed != 2 {
			t.Fatalf("fixture: bd list --type agent returned %d, want 2", listed)
		}
		if counted != listed {
			t.Errorf("bd count --type agent = %d, but bd list --type agent returns %d rows; "+
				"the two must read the same plane", counted, listed)
		}
	})

	t.Run("a non-infra type is untouched", func(t *testing.T) {
		// The durable default must not move. An infra type routes to the wisp
		// plane on write; `task` does not, and admitting the plane for it would
		// change upstream's pinned default.
		if got := countOf("--type", "task"); got != 3 {
			t.Errorf("bd count --type task = %d, want 3 (the durable default, unchanged)", got)
		}
	})

	t.Run("the bare default is untouched", func(t *testing.T) {
		// No type named: still durable-only, so the two infra beads stay out.
		if got := countOf(); got != 3 {
			t.Errorf("bd count = %d, want 3 (durable rows only; infra beads live on the wisp plane)", got)
		}
	})
}
