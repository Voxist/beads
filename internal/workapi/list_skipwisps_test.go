package workapi

import (
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestBuildListFilter_SkipWisps pins BuildListFilter's decision of whether to
// search the wisps (ephemeral) table alongside the durable issues table.
//
// va-k0e/vg-3kn: "bd list" silently omitted ephemeral molecule ("wisp") beads
// under every filter combination, including an explicit "--type=molecule"
// filter that can only ever match wisps-table rows for ephemeral molecules,
// and "--all" (documented as "Show all issues including closed (overrides
// default filter)"). The pre-fix condition only cleared the default
// SkipWisps=true for infra types (agent/role/message) and never checked --all
// at all; any other explicit --type (molecule, task, bug, ...) or --all still
// hid the wisps table with no way to opt back in short of --include-infra.
func TestBuildListFilter_SkipWisps(t *testing.T) {
	cfg := ListConfig{}

	cases := []struct {
		name string
		in   issueops.ListRequest
		want bool
	}{
		{
			name: "default unfiltered list still skips wisps (perf default preserved)",
			in:   issueops.ListRequest{},
			want: true,
		},
		{
			name: "explicit --type=molecule must not skip wisps",
			in:   issueops.ListRequest{IssueType: "molecule"},
			want: false,
		},
		{
			name: "explicit --type=task must not skip wisps (same root cause, any type)",
			in:   issueops.ListRequest{IssueType: "task"},
			want: false,
		},
		{
			name: "explicit --type=message (infra type) still does not skip wisps",
			in:   issueops.ListRequest{IssueType: "message"},
			want: false,
		},
		{
			name: "--include-infra still does not skip wisps",
			in:   issueops.ListRequest{IncludeInfra: true},
			want: false,
		},
		{
			name: "--include-ephemeral does not skip wisps",
			in:   issueops.ListRequest{IncludeEphemeral: true},
			want: false,
		},
		{
			name: "--all must not skip wisps (--all is documented as overriding default filters)",
			in:   issueops.ListRequest{AllFlag: true},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter, err := BuildListFilter(tc.in, cfg)
			if err != nil {
				t.Fatalf("BuildListFilter: %v", err)
			}
			if filter.SkipWisps != tc.want {
				t.Errorf("SkipWisps = %v, want %v", filter.SkipWisps, tc.want)
			}
		})
	}
}
