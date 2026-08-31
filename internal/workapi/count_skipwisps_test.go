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
// It also pins that nothing NARROWS what comes back: Ephemeral stays unset, so
// a named type admits the whole plane — no_history rows at ephemeral = 0 and
// true wisps at ephemeral = 1 alike. Pinning Ephemeral false was tried during
// review and reverted (1eac1264a): it makes count_embedded_test.go's 3 durable
// + 2 no_history + 1 ephemeral task answer 5 instead of 6. That embedded case
// is gated behind BEADS_TEST_EMBEDDED_DOLT=1, so this unit guard is what fails
// fast if someone re-narrows it.
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
			// Ephemeral stays UNSET: a named type means that type wherever it
			// lives, ephemeral rows included. Pinning it false would make
			// TestEmbeddedCountIncludeInfra's 3 durable + 2 no_history + 1
			// ephemeral task answer 5 instead of 6.
			name: "--type=task admits the plane, ephemeral rows included",
			in:   issueops.CountRequest{IssueType: "task"},
			want: ephemeralWant{skipWisps: false, ephemeral: nil},
		},
		{
			name: "--type=molecule admits the plane, ephemeral rows included",
			in:   issueops.CountRequest{IssueType: "molecule"},
			want: ephemeralWant{skipWisps: false, ephemeral: nil},
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

// TestBuildListFilter_PlaneAdmitsNamedTypeWhereverItLives is the list-side
// companion: TestBuildListFilter_SkipWisps pins WHETHER the plane is read,
// this pins that nothing narrows what comes back from it. The fork's contract
// is that a named type means that type wherever it lives — durable, no_history
// and ephemeral alike — so Ephemeral must stay unset. See
// TestEmbeddedCountIncludeInfra's type_filter case, which wants all six.
func TestBuildListFilter_PlaneAdmitsNamedTypeWhereverItLives(t *testing.T) {
	cfg := ListConfig{}

	filter, err := BuildListFilter(issueops.ListRequest{IssueType: "task"}, cfg)
	if err != nil {
		t.Fatalf("BuildListFilter: %v", err)
	}
	if filter.SkipWisps {
		t.Error("SkipWisps = true, want false (the plane must be read)")
	}
	if filter.Ephemeral != nil {
		t.Errorf("Ephemeral = %v, want unset (a named type admits ephemeral rows too)", *filter.Ephemeral)
	}
}
