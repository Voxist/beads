package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

func GetReadyWorkWithCountsInTx(ctx context.Context, tx *sql.Tx, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	wispDepsExist, err := optionalTableExistsInTx(ctx, tx, "wisp_dependencies")
	if err != nil {
		return nil, fmt.Errorf("get ready work with counts: wisp dependency probe: %w", err)
	}
	issues, err := GetReadyWorkInTx(ctx, tx, filter)
	if err != nil {
		return nil, err
	}
	out, err := hydrateMixedIssueCountsInTx(ctx, tx, issues, wispDepsExist)
	if err != nil {
		return nil, err
	}
	sortIssuesWithCountsByPolicy(out, filter.SortPolicy)
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func hydrateMixedIssueCountsInTx(ctx context.Context, tx *sql.Tx, issues []*types.Issue, includeWispReverseDeps bool) ([]*types.IssueWithCounts, error) {
	if len(issues) == 0 {
		return nil, nil
	}
	perm := make([]*types.Issue, 0, len(issues))
	wisps := make([]*types.Issue, 0)
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if issueLooksWispBacked(issue) {
			wisps = append(wisps, issue)
		} else {
			perm = append(perm, issue)
		}
	}
	if err := hydrateIssues(ctx, tx, perm, IssuesFilterTables, true, false); err != nil {
		return nil, fmt.Errorf("hydrate ready issue details: %w", err)
	}
	if err := hydrateIssues(ctx, tx, wisps, WispsFilterTables, true, false); err != nil {
		return nil, fmt.Errorf("hydrate ready wisp details: %w", err)
	}

	outByID := make(map[string]*types.IssueWithCounts, len(issues))
	permCounts, err := hydrateIssueCountsInTx(ctx, tx, perm, IssuesFilterTables, includeWispReverseDeps)
	if err != nil {
		return nil, err
	}
	for _, item := range permCounts {
		if item != nil && item.Issue != nil {
			outByID[item.Issue.ID] = item
		}
	}
	wispCounts, err := hydrateIssueCountsInTx(ctx, tx, wisps, WispsFilterTables, true)
	if err != nil {
		return nil, err
	}
	for _, item := range wispCounts {
		if item != nil && item.Issue != nil {
			outByID[item.Issue.ID] = item
		}
	}

	out := make([]*types.IssueWithCounts, 0, len(issues))
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if item, ok := outByID[issue.ID]; ok {
			out = append(out, item)
		}
	}
	return out, nil
}

func issueLooksWispBacked(issue *types.Issue) bool {
	return issue.Ephemeral || issue.NoHistory || issue.WispType != ""
}

func sortIssuesWithCountsByPolicy(items []*types.IssueWithCounts, policy types.SortPolicy) {
	if len(items) <= 1 {
		return
	}
	issues := make([]*types.Issue, 0, len(items))
	for _, item := range items {
		if item == nil || item.Issue == nil {
			continue
		}
		issues = append(issues, item.Issue)
	}
	if len(issues) != len(items) {
		return
	}
	sortReadyIssues(issues, policy)
	byID := make(map[string]int, len(issues))
	for i, iss := range issues {
		byID[iss.ID] = i
	}
	sorted := make([]*types.IssueWithCounts, len(items))
	for _, item := range items {
		sorted[byID[item.Issue.ID]] = item
	}
	copy(items, sorted)
}
