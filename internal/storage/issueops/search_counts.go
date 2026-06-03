package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/types"
)

func SearchIssuesWithCountsInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	limit := filter.Limit

	wispDepsExist, err := optionalTableExistsInTx(ctx, tx, "wisp_dependencies")
	if err != nil {
		return nil, fmt.Errorf("search issues with counts: wisp dependency probe: %w", err)
	}

	if filter.Ephemeral != nil && *filter.Ephemeral {
		empty, probeErr := wispsTableEmptyOrMissingInTx(ctx, tx)
		if probeErr != nil {
			return nil, fmt.Errorf("search issues with counts: ephemeral wisp probe: %w", probeErr)
		}
		if empty || !wispDepsExist {
			return nil, nil
		}
		wisps, err := runFilterSearchQueryInTx(ctx, tx, query, filter, WispsFilterTables, true)
		if err != nil {
			return nil, err
		}
		return finishSearchIssuesWithCounts(wisps, limit), nil
	}

	out, err := runFilterSearchQueryInTx(ctx, tx, query, filter, IssuesFilterTables, wispDepsExist)
	if err != nil {
		return nil, err
	}

	empty, probeErr := wispsTableEmptyOrMissingInTx(ctx, tx)
	if probeErr != nil {
		return nil, fmt.Errorf("search issues with counts: wisp probe: %w", probeErr)
	}
	if empty {
		return finishSearchIssuesWithCounts(out, limit), nil
	}
	if !wispDepsExist {
		return finishSearchIssuesWithCounts(out, limit), nil
	}

	wisps, err := runFilterSearchQueryInTx(ctx, tx, query, filter, WispsFilterTables, true)
	if err != nil {
		if isTableNotExistError(err) {
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

func runFilterSearchQueryInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter, tables FilterTables, includeWispReverseDeps bool) ([]*types.IssueWithCounts, error) {
	searchFilter := filter
	searchFilter.IncludeDependencies = true
	issues, err := searchTableInTx(ctx, tx, query, searchFilter, tables)
	if err != nil {
		return nil, err
	}
	return hydrateIssueCountsInTx(ctx, tx, issues, tables, includeWispReverseDeps)
}

func hydrateIssueCountsInTx(ctx context.Context, tx *sql.Tx, issues []*types.Issue, tables FilterTables, includeWispReverseDeps bool) ([]*types.IssueWithCounts, error) {
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

	depCounts, err := countOutgoingBlocksInTx(ctx, tx, tables.Dependencies, ids)
	if err != nil {
		return nil, err
	}
	for id, count := range depCounts {
		if item, ok := byID[id]; ok {
			item.DependencyCount = count
		}
	}

	for _, depTable := range reverseDependencyTablesInTx(includeWispReverseDeps) {
		dependentCounts, err := countIncomingBlocksInTx(ctx, tx, depTable, ids)
		if err != nil {
			return nil, err
		}
		for id, count := range dependentCounts {
			if item, ok := byID[id]; ok {
				item.DependentCount += count
			}
		}
	}

	commentCounts, err := countCommentsInTx(ctx, tx, tables.Comments, ids)
	if err != nil {
		return nil, err
	}
	for id, count := range commentCounts {
		if item, ok := byID[id]; ok {
			item.CommentCount = count
		}
	}

	parents, err := parentIDsInTx(ctx, tx, tables.Dependencies, ids)
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

func reverseDependencyTablesInTx(includeWispReverseDeps bool) []string {
	if includeWispReverseDeps {
		return []string{"dependencies", "wisp_dependencies"}
	}
	return []string{"dependencies"}
}

func countOutgoingBlocksInTx(ctx context.Context, tx *sql.Tx, depTable string, ids []string) (map[string]int, error) {
	out := make(map[string]int)
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildSQLInClause(ids[start:end])
		//nolint:gosec // G201: depTable is a hardcoded table name selected by caller.
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT issue_id, COUNT(*) FROM %s
			WHERE issue_id IN (%s) AND type = 'blocks'
			GROUP BY issue_id
		`, depTable, placeholders), args...)
		if err != nil {
			return nil, fmt.Errorf("count outgoing blocks from %s: %w", depTable, err)
		}
		if err := scanIntCountsInTx(rows, out); err != nil {
			return nil, fmt.Errorf("count outgoing blocks from %s: %w", depTable, err)
		}
	}
	return out, nil
}

func countIncomingBlocksInTx(ctx context.Context, tx *sql.Tx, depTable string, ids []string) (map[string]int, error) {
	out := make(map[string]int)
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildSQLInClause(ids[start:end])
		targetClause, targetArgs := depTargetIn("", placeholders, args)
		//nolint:gosec // G201: depTable and targetClause are hardcoded fragments.
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT %s AS depends_on_id, COUNT(*) FROM %s
			WHERE %s AND type = 'blocks'
			GROUP BY %s
		`, DepTargetExpr, depTable, targetClause, DepTargetExpr), targetArgs...)
		if err != nil {
			return nil, fmt.Errorf("count incoming blocks from %s: %w", depTable, err)
		}
		if err := scanIntCountsInTx(rows, out); err != nil {
			return nil, fmt.Errorf("count incoming blocks from %s: %w", depTable, err)
		}
	}
	return out, nil
}

func countCommentsInTx(ctx context.Context, tx *sql.Tx, commentTable string, ids []string) (map[string]int, error) {
	out := make(map[string]int)
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildSQLInClause(ids[start:end])
		//nolint:gosec // G201: commentTable is a hardcoded table name selected by caller.
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT issue_id, COUNT(*) FROM %s
			WHERE issue_id IN (%s)
			GROUP BY issue_id
		`, commentTable, placeholders), args...)
		if err != nil {
			return nil, fmt.Errorf("count comments from %s: %w", commentTable, err)
		}
		if err := scanIntCountsInTx(rows, out); err != nil {
			return nil, fmt.Errorf("count comments from %s: %w", commentTable, err)
		}
	}
	return out, nil
}

func parentIDsInTx(ctx context.Context, tx *sql.Tx, depTable string, ids []string) (map[string]string, error) {
	out := make(map[string]string)
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildSQLInClause(ids[start:end])
		//nolint:gosec // G201: depTable is a hardcoded table name selected by caller.
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT issue_id, MIN(%s) FROM %s
			WHERE issue_id IN (%s) AND type = 'parent-child'
			GROUP BY issue_id
		`, DepTargetExpr, depTable, placeholders), args...)
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

func scanIntCountsInTx(rows *sql.Rows, out map[string]int) error {
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

func joinAnd(clauses []string) string {
	switch len(clauses) {
	case 0:
		return ""
	case 1:
		return clauses[0]
	}
	total := 0
	for _, c := range clauses {
		total += len(c)
	}
	total += 5 * (len(clauses) - 1)
	buf := make([]byte, 0, total)
	for i, c := range clauses {
		if i > 0 {
			buf = append(buf, " AND "...)
		}
		buf = append(buf, c...)
	}
	return string(buf)
}
