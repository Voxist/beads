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
// The default before this change must stay byte-identical: durable plane only.
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
			name:          "a named type alone stays durable-only (the default before this change)",
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

// TestCountAndListPlaneAgreement pins how count and list decide the PLANE for
// the same request. They now agree on every case, including an infra type.
//
// They did not always. The count arm was a bare `else { SkipWisps =
// true }`, so `bd count --type agent` answered 0 where `bd list --type agent`
// returned rows; this file used to pin that divergence rather than fix it. It
// is fixed now, in both builders, and the row below is the case that flipped.
//
// WHAT THIS TEST CANNOT SEE. It hands both builders a POPULATED ListConfig.
// The real count path does not: storecounter.filter and uow.countFilter load
// the workspace config, and loading it under IncludeInfra alone left a zero
// ListConfig — which silently falls back to the DEFAULT infra set — on every
// other request. A workspace that configured types.infra therefore still got 0
// from count while list returned rows, and no case here could show it because
// cfg is an argument. That gap is covered by
// TestEmbeddedCountInfraTypeAgreesWithList's configured-infra-type subtest,
// which goes through the real caller.
//
// It asserts the ABSOLUTE expected value as well as the agreement. Agreement
// alone is satisfied by a regression that turns the flag into a no-op on BOTH
// builders, which is exactly the drift this exists to catch.
//
// It deliberately says nothing about the two answers being equal in ROWS: a
// count includes templates and a listing does not, with or without this flag.
// See issueops.CountRequest.IncludeEphemeral.
func TestCountAndListPlaneAgreement(t *testing.T) {
	// CustomTypes admits "agent" into the workspace vocabulary (BuildListFilter
	// rejects a type it does not know), and the default InfraSet already treats
	// it as infra. Without the vocabulary the builders return an error and the
	// infra cases below prove nothing.
	cfg := ListConfig{CustomTypes: []string{"agent", "role", "message"}}

	for _, tt := range []struct {
		name             string
		issueType        string
		includeEphemeral bool
		wantCountSkip    bool
		wantAgree        bool
	}{
		{"default", "", false, true, true},
		{"named type", "task", false, true, true},
		{"include-ephemeral", "", true, false, true},
		{"named type + include-ephemeral", "task", true, false, true},
		// Formerly the known divergence: list read the plane for an infra
		// type and count did not, so `bd count --type agent` answered 0 while
		// `bd list --type agent` returned rows. count's arm now carries
		// list's clause, so the two agree with no flag at all.
		{"infra type agrees", "agent", false, false, true},
		{"infra type + include-ephemeral", "agent", true, false, true},
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
			if countFilter.SkipWisps != tt.wantCountSkip {
				t.Errorf("count SkipWisps = %v, want %v", countFilter.SkipWisps, tt.wantCountSkip)
			}
			agree := listFilter.SkipWisps == countFilter.SkipWisps
			if agree != tt.wantAgree {
				t.Errorf("list SkipWisps=%v count SkipWisps=%v (agree=%v), want agree=%v — "+
					"a change to either builder alone lands here",
					listFilter.SkipWisps, countFilter.SkipWisps, agree, tt.wantAgree)
			}
		})
	}
}
