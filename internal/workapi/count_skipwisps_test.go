package workapi

import (
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestBuildCountFilter_IncludeEphemeral pins the plane knob on the count side.
//
// The wisps TABLE holds no_history beads (ephemeral = 0) alongside true wisps
// (ephemeral = 1), and the write path routes on STORAGE CLASS, not type — so a
// no_history task lives there while remaining ordinary durable work. A count
// that names a type therefore has to be able to reach that table, and until
// IncludeEphemeral existed the only way was IncludeInfra, which ALSO drops
// template rows of the named type: one silent undercount traded for another.
//
// The default is upstream's and must stay byte-identical: durable plane only.
func TestBuildCountFilter_IncludeEphemeral(t *testing.T) {
	cfg := ListConfig{}

	cases := []struct {
		name          string
		in            issueops.CountRequest
		wantSkipWisps bool
	}{
		{
			name:          "unfiltered count stays durable-only",
			in:            issueops.CountRequest{},
			wantSkipWisps: true,
		},
		{
			name:          "a named type alone stays durable-only (upstream's default)",
			in:            issueops.CountRequest{IssueType: "task"},
			wantSkipWisps: true,
		},
		{
			name:          "--include-ephemeral admits the plane",
			in:            issueops.CountRequest{IncludeEphemeral: true},
			wantSkipWisps: false,
		},
		{
			name:          "--include-ephemeral with a named type admits the plane",
			in:            issueops.CountRequest{IssueType: "task", IncludeEphemeral: true},
			wantSkipWisps: false,
		},
		{
			name:          "--include-infra still admits the plane on its own",
			in:            issueops.CountRequest{IssueType: "task", IncludeInfra: true},
			wantSkipWisps: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			filter, err := BuildCountFilter(tt.in, cfg)
			if err != nil {
				t.Fatalf("BuildCountFilter: %v", err)
			}
			if filter.SkipWisps != tt.wantSkipWisps {
				t.Errorf("SkipWisps = %v, want %v", filter.SkipWisps, tt.wantSkipWisps)
			}
		})
	}
}

// TestCountAndListAgreeOnThePlane is the property applyCountIncludeInfra exists
// to hold, extended to the new knob: for the same request, count and list must
// read the same planes. It is the guard that would fail if a later change wired
// the flag into one of the two builders and not the other.
func TestCountAndListAgreeOnThePlane(t *testing.T) {
	cfg := ListConfig{}

	for _, tt := range []struct {
		name             string
		issueType        string
		includeEphemeral bool
	}{
		{"default", "", false},
		{"named type", "task", false},
		{"include-ephemeral", "", true},
		{"named type + include-ephemeral", "task", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			listFilter, err := BuildListFilter(issueops.ListRequest{
				IssueType: tt.issueType, IncludeEphemeral: tt.includeEphemeral,
			}, cfg)
			if err != nil {
				t.Fatalf("BuildListFilter: %v", err)
			}
			countFilter, err := BuildCountFilter(issueops.CountRequest{
				IssueType: tt.issueType, IncludeEphemeral: tt.includeEphemeral,
			}, cfg)
			if err != nil {
				t.Fatalf("BuildCountFilter: %v", err)
			}
			if listFilter.SkipWisps != countFilter.SkipWisps {
				t.Errorf("plane disagreement: list SkipWisps=%v, count SkipWisps=%v",
					listFilter.SkipWisps, countFilter.SkipWisps)
			}
		})
	}
}
