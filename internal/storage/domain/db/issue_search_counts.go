package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/types"
)

func (r *issueSQLRepositoryImpl) searchAcrossIssuesAndWispsWithCounts(ctx context.Context, query string, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	limit := filter.Limit

	wispDepsExist, err := r.optionalTableExists(ctx, "wisp_dependencies")
	if err != nil {
		return nil, fmt.Errorf("search issues with counts: wisp dependency probe: %w", err)
	}

	if filter.Ephemeral != nil && *filter.Ephemeral {
		empty, probeErr := r.wispsTableEmptyOrMissing(ctx)
		if probeErr != nil {
			return nil, fmt.Errorf("search issues with counts: ephemeral wisp probe: %w", probeErr)
		}
		if empty || !wispDepsExist {
			return nil, nil
		}
		wisps, err := r.runFilterSearchQuery(ctx, query, filter, wispsFilterTables, true)
		if err != nil {
			return nil, err
		}
		return finishSearchIssuesWithCounts(wisps, limit), nil
	}

	out, err := r.runFilterSearchQuery(ctx, query, filter, issuesFilterTables, wispDepsExist)
	if err != nil {
		return nil, err
	}

	if filter.SkipWisps {
		return finishSearchIssuesWithCounts(out, limit), nil
	}

	empty, probeErr := r.wispsTableEmptyOrMissing(ctx)
	if probeErr != nil {
		return nil, fmt.Errorf("search issues with counts: wisp probe: %w", probeErr)
	}
	if empty || !wispDepsExist {
		return finishSearchIssuesWithCounts(out, limit), nil
	}

	wisps, err := r.runFilterSearchQuery(ctx, query, filter, wispsFilterTables, true)
	if err != nil {
		if dberrors.IsTableNotExist(err) {
			return finishSearchIssuesWithCounts(out, limit), nil
		}
		return nil, err
	}
	if len(wisps) == 0 {
		return finishSearchIssuesWithCounts(out, limit), nil
	}

	seen := make(map[string]struct{}, len(out))
	for _, iwc := range out {
		if iwc != nil && iwc.Issue != nil {
			seen[iwc.Issue.ID] = struct{}{}
		}
	}
	for _, w := range wisps {
		if w == nil || w.Issue == nil {
			continue
		}
		if _, dup := seen[w.Issue.ID]; dup {
			return nil, fmt.Errorf("search issues with counts: id %q exists in both issues and wisps", w.Issue.ID)
		}
		out = append(out, w)
	}
	return finishSearchIssuesWithCounts(out, limit), nil
}

func (r *issueSQLRepositoryImpl) runFilterSearchQuery(ctx context.Context, query string, filter types.IssueFilter, tables filterTables, includeWispReverseDeps bool) ([]*types.IssueWithCounts, error) {
	searchFilter := filter
	searchFilter.IncludeDependencies = true
	issues, err := r.searchTable(ctx, query, searchFilter, tables)
	if err != nil {
		return nil, err
	}
	return r.hydrateIssueCounts(ctx, issues, tables, includeWispReverseDeps)
}

func (r *issueSQLRepositoryImpl) hydrateIssueCounts(ctx context.Context, issues []*types.Issue, tables filterTables, includeWispReverseDeps bool) ([]*types.IssueWithCounts, error) {
	if len(issues) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(issues))
	out := make([]*types.IssueWithCounts, 0, len(issues))
	byID := make(map[string]*types.IssueWithCounts, len(issues))
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		item := &types.IssueWithCounts{Issue: issue}
		out = append(out, item)
		ids = append(ids, issue.ID)
		byID[issue.ID] = item
	}
	if len(ids) == 0 {
		return out, nil
	}

	depCounts, err := r.countOutgoingBlocks(ctx, tables.Dependencies, ids)
	if err != nil {
		return nil, err
	}
	for id, count := range depCounts {
		if item, ok := byID[id]; ok {
			item.DependencyCount = count
		}
	}

	for _, depTable := range reverseDependencyTables(includeWispReverseDeps) {
		dependentCounts, err := r.countIncomingBlocks(ctx, depTable, ids)
		if err != nil {
			return nil, err
		}
		for id, count := range dependentCounts {
			if item, ok := byID[id]; ok {
				item.DependentCount += count
			}
		}
	}

	commentCounts, err := r.countComments(ctx, tables.Comments, ids)
	if err != nil {
		return nil, err
	}
	for id, count := range commentCounts {
		if item, ok := byID[id]; ok {
			item.CommentCount = count
		}
	}

	parents, err := r.parentIDs(ctx, tables.Dependencies, ids)
	if err != nil {
		return nil, err
	}
	for id, parent := range parents {
		if item, ok := byID[id]; ok {
			p := parent
			item.Parent = &p
		}
	}

	return out, nil
}

func reverseDependencyTables(includeWispReverseDeps bool) []string {
	if includeWispReverseDeps {
		return []string{"dependencies", "wisp_dependencies"}
	}
	return []string{"dependencies"}
}

func (r *issueSQLRepositoryImpl) countOutgoingBlocks(ctx context.Context, depTable string, ids []string) (map[string]int, error) {
	out := make(map[string]int)
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildInPlaceholders(ids[start:end])
		//nolint:gosec // G201: depTable is a hardcoded table name selected by caller.
		rows, err := r.runner.QueryContext(ctx, fmt.Sprintf(`
			SELECT issue_id, COUNT(*) FROM %s
			WHERE issue_id IN (%s) AND type = 'blocks'
			GROUP BY issue_id
		`, depTable, placeholders), args...)
		if err != nil {
			return nil, fmt.Errorf("count outgoing blocks from %s: %w", depTable, err)
		}
		if err := scanIntCounts(rows, out); err != nil {
			return nil, fmt.Errorf("count outgoing blocks from %s: %w", depTable, err)
		}
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) countIncomingBlocks(ctx context.Context, depTable string, ids []string) (map[string]int, error) {
	out := make(map[string]int)
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildInPlaceholders(ids[start:end])
		targetClause, targetArgs := depTargetIn(placeholders, args)
		//nolint:gosec // G201: depTable and targetClause are hardcoded fragments.
		rows, err := r.runner.QueryContext(ctx, fmt.Sprintf(`
			SELECT %s AS depends_on_id, COUNT(*) FROM %s
			WHERE %s AND type = 'blocks'
			GROUP BY %s
		`, depTargetExpr, depTable, targetClause, depTargetExpr), targetArgs...)
		if err != nil {
			return nil, fmt.Errorf("count incoming blocks from %s: %w", depTable, err)
		}
		if err := scanIntCounts(rows, out); err != nil {
			return nil, fmt.Errorf("count incoming blocks from %s: %w", depTable, err)
		}
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) countComments(ctx context.Context, commentTable string, ids []string) (map[string]int, error) {
	out := make(map[string]int)
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildInPlaceholders(ids[start:end])
		//nolint:gosec // G201: commentTable is a hardcoded table name selected by caller.
		rows, err := r.runner.QueryContext(ctx, fmt.Sprintf(`
			SELECT issue_id, COUNT(*) FROM %s
			WHERE issue_id IN (%s)
			GROUP BY issue_id
		`, commentTable, placeholders), args...)
		if err != nil {
			return nil, fmt.Errorf("count comments from %s: %w", commentTable, err)
		}
		if err := scanIntCounts(rows, out); err != nil {
			return nil, fmt.Errorf("count comments from %s: %w", commentTable, err)
		}
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) parentIDs(ctx context.Context, depTable string, ids []string) (map[string]string, error) {
	out := make(map[string]string)
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildInPlaceholders(ids[start:end])
		//nolint:gosec // G201: depTable is a hardcoded table name selected by caller.
		rows, err := r.runner.QueryContext(ctx, fmt.Sprintf(`
			SELECT issue_id, MIN(%s) FROM %s
			WHERE issue_id IN (%s) AND type = 'parent-child'
			GROUP BY issue_id
		`, depTargetExpr, depTable, placeholders), args...)
		if err != nil {
			return nil, fmt.Errorf("load parent ids from %s: %w", depTable, err)
		}
		for rows.Next() {
			var issueID string
			var parent sql.NullString
			if err := rows.Scan(&issueID, &parent); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan parent id: %w", err)
			}
			if parent.Valid {
				out[issueID] = parent.String
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("parent id rows: %w", err)
		}
	}
	return out, nil
}

func scanIntCounts(rows *sql.Rows, out map[string]int) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return fmt.Errorf("scan count: %w", err)
		}
		out[id] += count
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("count rows: %w", err)
	}
	return nil
}

func (r *issueSQLRepositoryImpl) optionalTableExists(ctx context.Context, table string) (bool, error) {
	var probe int
	//nolint:gosec // G201: table is a hardcoded constant from caller (issues, wisps, dependencies, wisp_dependencies, ...).
	err := r.runner.QueryRowContext(ctx, fmt.Sprintf("SELECT 1 FROM %s LIMIT 1", table)).Scan(&probe)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return true, nil
	case dberrors.IsTableNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

func finishSearchIssuesWithCounts(items []*types.IssueWithCounts, limit int) []*types.IssueWithCounts {
	sortSearchIssuesWithCounts(items)
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}

func sortSearchIssuesWithCounts(items []*types.IssueWithCounts) {
	if len(items) <= 1 {
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a == nil || a.Issue == nil {
			return false
		}
		if b == nil || b.Issue == nil {
			return true
		}
		if a.Issue.Priority != b.Issue.Priority {
			return a.Issue.Priority < b.Issue.Priority
		}
		if !a.Issue.CreatedAt.Equal(b.Issue.CreatedAt) {
			return a.Issue.CreatedAt.After(b.Issue.CreatedAt)
		}
		return a.Issue.ID < b.Issue.ID
	})
}
