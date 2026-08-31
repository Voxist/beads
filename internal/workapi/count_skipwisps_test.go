package workapi

import (
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// ephemeralWant describes what the fork's plane rule must produce for the
// wisps table: whether the table is read at all, and whether true wisps
// (ephemeral = 1) are admitted along with the no_history rows (ephemeral = 0)
// that share it.
type ephemeralWant struct {
	skipWisps bool
	// ephemeral is the expected *filter.Ephemeral, or nil for "unset".
	ephemeral *bool
}

func ptrBool(b bool) *bool { return &b }

// TestBuildCountFilter_PlaneRule pins the COUNT half of the fork plane delta
// (vg-8db). It exists because the list half was guarded by
// TestBuildListFilter_SkipWisps and the golden corpus while this half was not:
// reverting count.go's condition to upstream's bare `else` left the whole
// package green, so the next resync would have silently taken `bd count --type
// task` back to undercounting every no_history task parked in the wisps table.
//
// It also pins the narrowing that keeps the delta honest: naming a type admits
// the PLANE, not true wisps. The table holds both (no_history at ephemeral = 0,
// wisps at ephemeral = 1), so Ephemeral must be pinned false — otherwise a bare
// `bd count --type task` starts counting ephemeral task wisps with no flag to
// turn them off.
func TestBuildCountFilter_PlaneRule(t *testing.T) {
	cfg := ListConfig{}

	cases := []struct {
		name string
		in   issueops.CountRequest
		want ephemeralWant
	}{
		{
			name: "unfiltered count stays durable-only (historical default)",
			in:   issueops.CountRequest{},
			want: ephemeralWant{skipWisps: true, ephemeral: nil},
		},
		{
			name: "--type=task admits the plane but not true wisps",
			in:   issueops.CountRequest{IssueType: "task"},
			want: ephemeralWant{skipWisps: false, ephemeral: ptrBool(false)},
		},
		{
			name: "--type=molecule admits the plane but not true wisps",
			in:   issueops.CountRequest{IssueType: "molecule"},
			want: ephemeralWant{skipWisps: false, ephemeral: ptrBool(false)},
		},
		{
			name: "infra type routes to the wisp plane alone, untouched by the delta",
			in:   issueops.CountRequest{IssueType: "agent", IncludeInfra: true},
			want: ephemeralWant{skipWisps: false, ephemeral: ptrBool(true)},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			filter, err := BuildCountFilter(tt.in, cfg)
			if err != nil {
				t.Fatalf("BuildCountFilter: %v", err)
			}

			if filter.SkipWisps != tt.want.skipWisps {
				t.Errorf("SkipWisps = %v, want %v", filter.SkipWisps, tt.want.skipWisps)
			}
			switch {
			case tt.want.ephemeral == nil && filter.Ephemeral != nil:
				t.Errorf("Ephemeral = %v, want unset", *filter.Ephemeral)
			case tt.want.ephemeral != nil && filter.Ephemeral == nil:
				t.Errorf("Ephemeral = unset, want %v", *tt.want.ephemeral)
			case tt.want.ephemeral != nil && *filter.Ephemeral != *tt.want.ephemeral:
				t.Errorf("Ephemeral = %v, want %v", *filter.Ephemeral, *tt.want.ephemeral)
			}
		})
	}
}

// TestBuildListFilter_PlaneAdmitsNoHistoryNotWisps is the list-side companion:
// TestBuildListFilter_SkipWisps pins WHETHER the plane is read, this pins WHAT
// comes back from it. Without the Ephemeral pin, `bd list --type task` would
// widen from "durable tasks" to "durable tasks + every live task wisp".
func TestBuildListFilter_PlaneAdmitsNoHistoryNotWisps(t *testing.T) {
	cfg := ListConfig{}

	t.Run("non-infra type pins Ephemeral false", func(t *testing.T) {
		filter, err := BuildListFilter(issueops.ListRequest{IssueType: "task"}, cfg)
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if filter.SkipWisps {
			t.Error("SkipWisps = true, want false (the plane must be read)")
		}
		if filter.Ephemeral == nil {
			t.Fatal("Ephemeral = unset, want false (true wisps must stay excluded)")
		}
		if *filter.Ephemeral {
			t.Error("Ephemeral = true, want false")
		}
	})

	t.Run("--include-ephemeral leaves the pin off so wisps are admitted", func(t *testing.T) {
		filter, err := BuildListFilter(
			issueops.ListRequest{IssueType: "task", IncludeEphemeral: true}, cfg)
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if filter.SkipWisps {
			t.Error("SkipWisps = true, want false")
		}
		if filter.Ephemeral != nil && !*filter.Ephemeral {
			t.Error("Ephemeral pinned false, want unset: --include-ephemeral asked for wisps")
		}
	})
}
