package main

import "testing"

// TestBuildListFilter_SkipWisps pins buildListFilter's decision of whether to
// search the wisps (ephemeral) table alongside the durable issues table.
//
// va-k0e: "bd list" silently omitted ephemeral molecule ("wisp") beads under
// every filter combination, including an explicit "--type=molecule" filter
// that can only ever match wisps-table rows for ephemeral molecules. The
// pre-fix condition only cleared the default SkipWisps=true for infra types
// (agent/role/message); any other explicit --type (molecule, task, bug, ...)
// still hid the wisps table with no way to opt back in short of --include-infra.
func TestBuildListFilter_SkipWisps(t *testing.T) {
	cfg := listFilterConfig{}

	cases := []struct {
		name string
		in   listInput
		want bool
	}{
		{
			name: "default unfiltered list still skips wisps (perf default preserved)",
			in:   listInput{},
			want: true,
		},
		{
			name: "explicit --type=molecule must not skip wisps",
			in:   listInput{issueType: "molecule"},
			want: false,
		},
		{
			name: "explicit --type=task must not skip wisps (same root cause, any type)",
			in:   listInput{issueType: "task"},
			want: false,
		},
		{
			name: "explicit --type=message (infra type) still does not skip wisps",
			in:   listInput{issueType: "message"},
			want: false,
		},
		{
			name: "--include-infra still does not skip wisps",
			in:   listInput{includeInfra: true},
			want: false,
		},
		{
			name: "--include-ephemeral does not skip wisps",
			in:   listInput{includeEphemeral: true},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter, err := buildListFilter(tc.in, cfg)
			if err != nil {
				t.Fatalf("buildListFilter: %v", err)
			}
			if filter.SkipWisps != tc.want {
				t.Errorf("SkipWisps = %v, want %v", filter.SkipWisps, tc.want)
			}
		})
	}
}
