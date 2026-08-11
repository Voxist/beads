//go:build cgo

package main

import (
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// TestApplyCountIncludeInfraMirrorsListFilter pins `bd count --include-infra`
// to the exact cardinality semantics of `bd list --include-infra --all`
// (GH#4387): for any filter set, the count must equal the number of rows the
// equivalent list invocation returns. The trap dimensions are the wisps merge
// (SkipWisps), template exclusion (IsTemplate), the default gate exclusion
// (ExcludeTypes), and infra-type routing to the ephemeral tier (Ephemeral).
func TestApplyCountIncludeInfraMirrorsListFilter(t *testing.T) {
	cfg := workapi.ListConfig{}
	for _, issueType := range []string{"", "task", "gate", "message"} {
		name := issueType
		if name == "" {
			name = "none"
		}
		t.Run("type_"+name, func(t *testing.T) {
			in := issueops.ListRequest{AllFlag: true, IncludeInfra: true, IssueType: issueType}
			want, err := workapi.BuildListFilter(in, cfg)
			if err != nil {
				t.Fatalf("workapi.BuildListFilter(%q): %v", issueType, err)
			}

			got := types.IssueFilter{}
			if issueType != "" {
				it := types.IssueType(issueType)
				got.IssueType = &it
			}
			applyCountIncludeInfra(&got, issueType, cfg)

			if got.SkipWisps != want.SkipWisps {
				t.Errorf("SkipWisps = %v, list --include-infra --all uses %v", got.SkipWisps, want.SkipWisps)
			}
			if !reflect.DeepEqual(got.IsTemplate, want.IsTemplate) {
				t.Errorf("IsTemplate = %v, list --include-infra --all uses %v", ptrStr(got.IsTemplate), ptrStr(want.IsTemplate))
			}
			if !reflect.DeepEqual(got.ExcludeTypes, want.ExcludeTypes) {
				t.Errorf("ExcludeTypes = %v, list --include-infra --all uses %v", got.ExcludeTypes, want.ExcludeTypes)
			}
			if !reflect.DeepEqual(got.Ephemeral, want.Ephemeral) {
				t.Errorf("Ephemeral = %v, list --include-infra --all uses %v", ptrStr(got.Ephemeral), ptrStr(want.Ephemeral))
			}
			if !reflect.DeepEqual(got.IssueType, want.IssueType) {
				t.Errorf("IssueType = %v, list --include-infra --all uses %v", got.IssueType, want.IssueType)
			}
			// bd count defaults to all statuses and all pinned states, which is
			// exactly what list's --all flag selects: none of these dimensions
			// may carry a filter on either side.
			if !reflect.DeepEqual(got.Status, want.Status) {
				t.Errorf("Status = %v, list --include-infra --all uses %v", got.Status, want.Status)
			}
			if !reflect.DeepEqual(got.Statuses, want.Statuses) {
				t.Errorf("Statuses = %v, list --include-infra --all uses %v", got.Statuses, want.Statuses)
			}
			if !reflect.DeepEqual(got.ExcludeStatus, want.ExcludeStatus) {
				t.Errorf("ExcludeStatus = %v, list --include-infra --all uses %v", got.ExcludeStatus, want.ExcludeStatus)
			}
			if !reflect.DeepEqual(got.Pinned, want.Pinned) {
				t.Errorf("Pinned = %v, list --include-infra --all uses %v", ptrStr(got.Pinned), ptrStr(want.Pinned))
			}
		})
	}
}

// TestApplyCountIncludeInfraCustomInfraTypes verifies that the infra-type
// routing honors a store-configured infra set, exactly like bd list does.
func TestApplyCountIncludeInfraCustomInfraTypes(t *testing.T) {
	cfg := workapi.ListConfig{InfraSet: map[string]bool{"robot": true}}

	var robot types.IssueFilter
	applyCountIncludeInfra(&robot, "robot", cfg)
	if robot.Ephemeral == nil || !*robot.Ephemeral {
		t.Errorf("custom infra type %q must route to the ephemeral tier (Ephemeral=true), got %v", "robot", ptrStr(robot.Ephemeral))
	}

	// "message" is a default infra type but NOT part of the custom set, so it
	// must not route to the ephemeral tier (mirrors workapi.ListConfig.IsInfra).
	var msg types.IssueFilter
	applyCountIncludeInfra(&msg, "message", cfg)
	if msg.Ephemeral != nil {
		t.Errorf("non-infra type under custom set must keep Ephemeral=nil, got %v", ptrStr(msg.Ephemeral))
	}
}

// TestApplyCountIncludeInfraDefaultUntouched documents that the helper is only
// invoked under --include-infra: without that flag, an unfiltered count
// keeps today's durable-only semantics (SkipWisps=true, no template/gate
// exclusion). An explicit --type instead goes through
// applyCountSkipWispsDefault (vg-8db) — see TestApplyCountSkipWispsDefault.
func TestApplyCountIncludeInfraDefaultUntouched(t *testing.T) {
	// Without --include-infra, count.go delegates to
	// applyCountSkipWispsDefault rather than calling this helper. Pin the
	// flag's existence and default value so scripted callers keep
	// byte-identical behavior.
	flag := countCmd.Flags().Lookup("include-infra")
	if flag == nil {
		t.Fatal("bd count must expose an --include-infra flag (GH#4387)")
	}
	if flag.DefValue != "false" {
		t.Fatalf("--include-infra must default to false, got %q", flag.DefValue)
	}
}

// TestApplyCountSkipWispsDefault pins bd count's SkipWisps decision on the
// path that does not pass --include-infra.
//
// vg-8db (sibling of va-k0e/vg-3kn): `bd count` and especially
// `bd count --type=<T>` undercounted wisps-table (ephemeral) rows, because
// this path unconditionally set SkipWisps=true regardless of an explicit
// --type. A --type filter that can only ever match wisps-table rows
// (molecules, or any other type parked there) returned a silently-low count
// instead of the matching rows.
func TestApplyCountSkipWispsDefault(t *testing.T) {
	cases := []struct {
		name      string
		issueType string
		want      bool
	}{
		{
			name:      "default unfiltered count still skips wisps (perf default preserved)",
			issueType: "",
			want:      true,
		},
		{
			name:      "explicit --type=molecule must not skip wisps",
			issueType: "molecule",
			want:      false,
		},
		{
			name:      "explicit --type=task must not skip wisps (same root cause, any type)",
			issueType: "task",
			want:      false,
		},
		{
			name:      "explicit --type=message (infra type) still does not skip wisps",
			issueType: "message",
			want:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter := types.IssueFilter{}
			applyCountSkipWispsDefault(&filter, tc.issueType)
			if filter.SkipWisps != tc.want {
				t.Errorf("SkipWisps = %v, want %v", filter.SkipWisps, tc.want)
			}
		})
	}
}

// TestApplyCountSkipWispsDefaultMirrorsListFilter cross-checks
// applyCountSkipWispsDefault's SkipWisps decision against buildListFilter's
// for the equivalent input (no --include-infra, --all, or
// --include-ephemeral), so the two independently-implemented paths cannot
// silently diverge again (vg-8db).
func TestApplyCountSkipWispsDefaultMirrorsListFilter(t *testing.T) {
	cfg := workapi.ListConfig{}
	for _, issueType := range []string{"", "task", "molecule", "message"} {
		name := issueType
		if name == "" {
			name = "none"
		}
		t.Run("type_"+name, func(t *testing.T) {
			want, err := workapi.BuildListFilter(issueops.ListRequest{IssueType: issueType}, cfg)
			if err != nil {
				t.Fatalf("workapi.BuildListFilter(%q): %v", issueType, err)
			}

			got := types.IssueFilter{}
			applyCountSkipWispsDefault(&got, issueType)

			if got.SkipWisps != want.SkipWisps {
				t.Errorf("SkipWisps = %v, list (no --include-infra/--all/--include-ephemeral) uses %v", got.SkipWisps, want.SkipWisps)
			}
		})
	}
}

func ptrStr[T any](p *T) string {
	if p == nil {
		return "<nil>"
	}
	return "&" + reflect.ValueOf(*p).String()
}
