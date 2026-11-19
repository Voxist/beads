package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// TestSearchCountsFilterBeforeJoinParity proves that the filter-before-join
// shape of sqlbuild.SearchCountsSQL returns exactly the same rows (ids, order,
// dep/rdep/comment counts, parent, labels, and dependency JSON) as the prior
// filter-after-join shape, across a representative set of filters, orderings,
// and limits. The WHERE clause only references issue columns, so pushing it
// into an inner main-table subquery is result-equivalent (ORDER BY and LIMIT
// stay after the joins); this test is the contract that keeps it so.
func TestSearchCountsFilterBeforeJoinParity(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	seedSearchCountsFixture(ctx, t, store)

	cases := []struct {
		name   string
		query  string
		filter types.IssueFilter
	}{
		{name: "no filter, default order"},
		{name: "status open", filter: types.IssueFilter{Statuses: []types.Status{types.StatusOpen}}},
		{name: "priority 1", filter: types.IssueFilter{Priority: ptr(1)}},
		{name: "label alpha", filter: types.IssueFilter{Labels: []string{"alpha"}}},
		{name: "substring search", query: "counts-parity"},
		{name: "limit 2 default order", filter: types.IssueFilter{Limit: 2}},
		{name: "limit 3 order created", filter: types.IssueFilter{Limit: 3, SortBy: "created"}},
		{name: "order title", filter: types.IssueFilter{SortBy: "title"}},
		{name: "order status desc", filter: types.IssueFilter{SortBy: "status", SortDesc: true}},
		{name: "skip labels", filter: types.IssueFilter{SkipLabels: true}},
		{name: "limit 1 priority", filter: types.IssueFilter{Limit: 1, SortBy: "priority"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			whereClauses, args, err := sqlbuild.BuildIssueFilterClauses(tc.query, tc.filter, sqlbuild.IssuesFilterTables)
			if err != nil {
				t.Fatalf("BuildIssueFilterClauses: %v", err)
			}
			whereSQL := ""
			if len(whereClauses) > 0 {
				whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
			}
			orderBy := sqlbuild.OrderBy(tc.filter.SortBy, tc.filter.SortDesc, "i")
			limitSQL := ""
			if tc.filter.Limit > 0 {
				limitSQL = fmt.Sprintf("LIMIT %d", tc.filter.Limit)
			}

			// Predicate form (nil ids): the form this PR reshapes. The by-IDs
			// form keeps the plain driver and is unaffected.
			hyd := sqlbuild.CountsHydration{SkipLabels: tc.filter.SkipLabels}
			newSQL, genArgs := sqlbuild.SearchCountsSQL(sqlbuild.IssuesFilterTables, nil, whereSQL, orderBy, limitSQL, true, hyd)
			if genArgs != nil {
				t.Fatalf("predicate form must not generate args, got %d", len(genArgs))
			}
			oldSQL := oldSearchCountsSQL(sqlbuild.IssuesFilterTables, whereSQL, orderBy, limitSQL, true, tc.filter.SkipLabels)

			// Both sides scan under the hydration that built them; the old
			// builder has no lite form, so this pair is always full-shape.
			gotNew := runCountsSQL(ctx, t, store.db, newSQL, args, hyd)
			gotOld := runCountsSQL(ctx, t, store.db, oldSQL, args, hyd)

			if len(gotNew) != len(gotOld) {
				t.Fatalf("row count: new=%d old=%d", len(gotNew), len(gotOld))
			}
			for i := range gotOld {
				if gotNew[i] != gotOld[i] {
					t.Fatalf("row %d differs:\n new=%+v\n old=%+v", i, gotNew[i], gotOld[i])
				}
			}
		})
	}
}

// TestSearchCountsTwoPhaseParity is the vp-6y90n contract for the store path
// (issueops.SearchIssuesWithCountsInTx, which the server-backed DoltStore and
// the embedded store both call). A filtered counts search now selects the
// matching ids first and hydrates them through the by-IDs form; this proves it
// returns exactly the rows (ids, order, counts, parent, labels, deps JSON) the
// single-statement predicate form returned, on both table families, under
// ORDER BY + LIMIT, every hydration opt-out, an empty match and a match on
// either side of sqlbuild.SearchByIDsMaxRows — including the
// metadata.session_name lookup that motivated the change.
//
// The reference side is the predicate form rendered with the filter, exactly
// as runFilterSearchQueryInTx rendered it before this change, read in the
// same transaction as the two-phase side.
func TestSearchCountsTwoPhaseParity(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	seedSearchCountsFixture(ctx, t, store)
	seedTwoPhaseWispFixture(ctx, t, store)
	// A match larger than SearchByIDsMaxRows takes the single statement; seed
	// enough labeled wisps to reach it on purpose.
	bulk := sqlbuild.SearchByIDsMaxRows + 10
	for i := 0; i < bulk; i++ {
		id := fmt.Sprintf("tp-bulk-%03d", i)
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO wisps (id, title, status, priority, issue_type, ephemeral) VALUES (?, ?, 'open', ?, 'task', 1)",
			id, fmt.Sprintf("bulk %03d", (i*37)%bulk), i%4); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
		if _, err := store.db.ExecContext(ctx, "INSERT INTO wisp_labels (issue_id, label) VALUES (?, 'bulk')", id); err != nil {
			t.Fatalf("label %s: %v", id, err)
		}
	}

	yes := true
	issuesOnly := func(f types.IssueFilter) types.IssueFilter { f.SkipWisps = true; return f }
	wispsOnly := func(f types.IssueFilter) types.IssueFilter { f.Ephemeral = &yes; return f }
	session := func(name string) map[string]string { return map[string]string{"session_name": name} }

	cases := []struct {
		name   string
		query  string
		filter types.IssueFilter
		// wantRows guards the premise: a case meant to match must match on
		// the reference side, or old == new would hold vacuously.
		wantRows bool
	}{
		{name: "issues/status open", filter: issuesOnly(types.IssueFilter{Statuses: []types.Status{types.StatusOpen}}), wantRows: true},
		{name: "issues/label shared", filter: issuesOnly(types.IssueFilter{Labels: []string{"shared"}}), wantRows: true},
		{name: "issues/limit 2 default order", filter: issuesOnly(types.IssueFilter{Limit: 2, Statuses: []types.Status{types.StatusOpen}}), wantRows: true},
		{name: "issues/limit 3 order created", filter: issuesOnly(types.IssueFilter{Limit: 3, SortBy: "created", IDPrefix: "counts-parity"}), wantRows: true},
		{name: "issues/limit 2 order title desc", filter: issuesOnly(types.IssueFilter{Limit: 2, SortBy: "title", SortDesc: true, IDPrefix: "counts-parity"}), wantRows: true},
		{name: "issues/skip labels", filter: issuesOnly(types.IssueFilter{SkipLabels: true, IDPrefix: "counts-parity"}), wantRows: true},
		{name: "issues/skip counts", filter: issuesOnly(types.IssueFilter{SkipCounts: true, IDPrefix: "counts-parity"}), wantRows: true},
		{name: "issues/skip both", filter: issuesOnly(types.IssueFilter{SkipLabels: true, SkipCounts: true, IDPrefix: "counts-parity"}), wantRows: true},
		{name: "issues/lite", filter: issuesOnly(types.IssueFilter{Lite: true, IDPrefix: "counts-parity"}), wantRows: true},
		{name: "issues/substring", query: "counts-parity", filter: issuesOnly(types.IssueFilter{}), wantRows: true},
		{name: "issues/empty match", filter: issuesOnly(types.IssueFilter{MetadataFields: session("nobody")})},
		{name: "wisps/session_name", filter: wispsOnly(types.IssueFilter{MetadataFields: session("mayor")}), wantRows: true},
		{name: "wisps/session_name skip counts", filter: wispsOnly(types.IssueFilter{MetadataFields: session("mayor"), SkipCounts: true}), wantRows: true},
		{name: "wisps/session_name skip labels", filter: wispsOnly(types.IssueFilter{MetadataFields: session("deacon"), SkipLabels: true}), wantRows: true},
		{name: "wisps/label hot", filter: wispsOnly(types.IssueFilter{Labels: []string{"hot"}}), wantRows: true},
		{name: "wisps/limit 2 priority", filter: wispsOnly(types.IssueFilter{Limit: 2, SortBy: "priority", IDPrefix: "tp-w"}), wantRows: true},
		{name: "wisps/limit 3 order created desc", filter: wispsOnly(types.IssueFilter{Limit: 3, SortBy: "created", SortDesc: true, IDPrefix: "tp-w"}), wantRows: true},
		{name: "wisps/order title", filter: wispsOnly(types.IssueFilter{SortBy: "title", IDPrefix: "tp-w"}), wantRows: true},
		{name: "wisps/lite", filter: wispsOnly(types.IssueFilter{Lite: true, IDPrefix: "tp-w"}), wantRows: true},
		// Past SearchByIDsMaxRows: the single statement, with ORDER BY + LIMIT.
		{name: "wisps/bulk unlimited", filter: wispsOnly(types.IssueFilter{IDPrefix: "tp-bulk-"}), wantRows: true},
		{name: "wisps/bulk title limit past bound", filter: wispsOnly(types.IssueFilter{IDPrefix: "tp-bulk-", SortBy: "title", Limit: sqlbuild.SearchByIDsMaxRows + 5}), wantRows: true},
		{name: "wisps/bulk title limit at bound", filter: wispsOnly(types.IssueFilter{IDPrefix: "tp-bulk-", SortBy: "title", Limit: sqlbuild.SearchByIDsMaxRows}), wantRows: true},
		{name: "wisps/bulk label limit 7", filter: wispsOnly(types.IssueFilter{Labels: []string{"bulk"}, SortBy: "created", SortDesc: true, Limit: 7}), wantRows: true},
		// An ephemeral search whose wisp leg matches nothing falls through to
		// the issues table; both sides must agree on that path too.
		{name: "wisps/empty match", filter: wispsOnly(types.IssueFilter{MetadataFields: session("nobody")})},
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	sawCounts, sawWispLabels, sawWispRdep, sawWispComments := false, false, false, false
	for _, tc := range cases {
		reference := func(tables sqlbuild.FilterTables) []countsRow {
			whereClauses, args, err := sqlbuild.BuildIssueFilterClauses(tc.query, tc.filter, tables)
			if err != nil {
				t.Fatalf("%s: BuildIssueFilterClauses: %v", tc.name, err)
			}
			if len(whereClauses) == 0 {
				t.Fatalf("%s: case must be filtered to exercise the two-phase path", tc.name)
			}
			limitSQL := ""
			if eff := issueops.EffectiveSearchLimit(tc.filter.Limit, tc.filter.MaxRows); eff > 0 {
				limitSQL = fmt.Sprintf("LIMIT %d", eff)
			}
			hyd := sqlbuild.CountsHydration{SkipLabels: tc.filter.SkipLabels, SkipCounts: tc.filter.SkipCounts, Lite: tc.filter.Lite}
			oldSQL, _ := sqlbuild.SearchCountsSQL(tables, nil, "WHERE "+strings.Join(whereClauses, " AND "),
				sqlbuild.OrderBy(tc.filter.SortBy, tc.filter.SortDesc, "i"), limitSQL, true, hyd)
			return runCountsSQL(ctx, t, tx, oldSQL, args, hyd)
		}
		var want []countsRow
		if tc.filter.Ephemeral != nil {
			want = reference(sqlbuild.WispsFilterTables)
		}
		if len(want) == 0 {
			want = reference(sqlbuild.IssuesFilterTables)
		}

		got, err := issueops.SearchIssuesWithCountsInTx(ctx, tx, tc.query, tc.filter)
		if err != nil {
			t.Fatalf("%s: SearchIssuesWithCountsInTx: %v", tc.name, err)
		}
		if tc.wantRows && len(want) == 0 {
			t.Fatalf("%s: fixture must give the reference side rows", tc.name)
		}
		if !tc.wantRows && len(want) != 0 {
			t.Fatalf("%s: empty-match case matched %d rows on the reference side", tc.name, len(want))
		}
		if len(got) != len(want) {
			t.Fatalf("%s: row count: two-phase=%d single-statement=%d", tc.name, len(got), len(want))
		}
		for i := range want {
			g := countsRowOf(got[i])
			if g != want[i] {
				t.Fatalf("%s: row %d differs:\n two-phase=%+v\n single-statement=%+v", tc.name, i, g, want[i])
			}
			sawCounts = sawCounts || g.depCount+g.rdepCount+g.commentCount > 0
			if strings.HasPrefix(g.id, "tp-w") {
				sawWispLabels = sawWispLabels || g.labels != ""
				sawWispRdep = sawWispRdep || g.rdepCount > 0
				sawWispComments = sawWispComments || g.commentCount > 0
			}
		}
	}
	// Premise guards: equality over all-zero counts would prove nothing.
	if !sawCounts || !sawWispLabels || !sawWispRdep || !sawWispComments {
		t.Fatalf("fixture premise: counts=%v wispLabels=%v wispRdep=%v wispComments=%v, want all true",
			sawCounts, sawWispLabels, sawWispRdep, sawWispComments)
	}
}

// seedTwoPhaseWispFixture adds a wisp plane to the counts fixture: wisps with
// session_name metadata (the lookup vp-6y90n measured), wisp labels, wisp
// dependencies (blocks and parent-child, wisp-to-wisp and wisp-to-issue), and
// wisp comments.
func seedTwoPhaseWispFixture(ctx context.Context, t *testing.T, store *DoltStore) {
	t.Helper()

	wisps := []struct {
		id, title, session string
		priority           int
	}{
		{"tp-w1", "mayor session", "mayor", 1},
		{"tp-w2", "deacon session", "deacon", 2},
		{"tp-w3", "witness session", "witness", 1},
		{"tp-w4", "refinery session", "refinery", 3},
		{"tp-w5", "boot session", "boot", 2},
	}
	for _, w := range wisps {
		issue := &types.Issue{
			ID: w.id, Title: w.title, Status: types.StatusOpen, Priority: w.priority,
			IssueType: types.TypeTask, Ephemeral: true,
			Metadata: []byte(fmt.Sprintf(`{"session_name":%q}`, w.session)),
		}
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", w.id, err)
		}
	}
	for _, lb := range []struct{ id, label string }{
		{"tp-w1", "hot"}, {"tp-w1", "session"}, {"tp-w2", "session"}, {"tp-w3", "hot"},
	} {
		if err := store.AddLabel(ctx, lb.id, lb.label, "tester"); err != nil {
			t.Fatalf("AddLabel %s/%s: %v", lb.id, lb.label, err)
		}
	}
	for _, d := range []*types.Dependency{
		{IssueID: "tp-w2", DependsOnID: "tp-w1", Type: types.DepBlocks},
		{IssueID: "tp-w3", DependsOnID: "tp-w1", Type: types.DepBlocks},
		{IssueID: "tp-w4", DependsOnID: "tp-w5", Type: types.DepParentChild},
		{IssueID: "tp-w5", DependsOnID: "counts-parity-e", Type: types.DepBlocks},
	} {
		if err := store.AddDependency(ctx, d, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", d.IssueID, d.DependsOnID, err)
		}
	}
	for _, c := range []struct{ id, body string }{
		{"tp-w1", "first"}, {"tp-w1", "second"}, {"tp-w2", "only"},
	} {
		// AddComment records a comment event; ImportIssueComment writes the
		// wisp_comments row that comment_count counts.
		if _, err := store.ImportIssueComment(ctx, c.id, "tester", c.body, time.Now().UTC()); err != nil {
			t.Fatalf("ImportIssueComment %s: %v", c.id, err)
		}
	}
}

// countsRow is a flattened, comparable projection of one IssueWithCounts row.
type countsRow struct {
	id           string
	depCount     int
	rdepCount    int
	commentCount int
	parent       string
	labels       string
	deps         string
	lite         bool
	title        string
}

// countsQuerier is the read seam runCountsSQL needs; *sql.DB and *sql.Tx both
// satisfy it, so a parity test can read both sides inside one transaction.
type countsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func runCountsSQL(ctx context.Context, t *testing.T, db countsQuerier, query string, args []any, hyd sqlbuild.CountsHydration) []countsRow {
	t.Helper()
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("query: %v\nSQL:\n%s", err, query)
	}
	defer func() { _ = rows.Close() }()

	var out []countsRow
	seen := make(map[string]bool)
	for rows.Next() {
		iwc, err := issueops.ScanReadyWorkRowWithCounts(rows, hyd)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		if iwc == nil || iwc.Issue == nil || seen[iwc.Issue.ID] {
			continue
		}
		seen[iwc.Issue.ID] = true
		out = append(out, countsRowOf(iwc))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return out
}

// countsRowOf flattens one hydrated row into its comparable projection.
func countsRowOf(iwc *types.IssueWithCounts) countsRow {
	parent := ""
	if iwc.Parent != nil {
		parent = *iwc.Parent
	}
	deps := make([]string, 0, len(iwc.Issue.Dependencies))
	for _, d := range iwc.Issue.Dependencies {
		deps = append(deps, string(d.Type)+":"+d.DependsOnID)
	}
	return countsRow{
		id:           iwc.Issue.ID,
		depCount:     iwc.DependencyCount,
		rdepCount:    iwc.DependentCount,
		commentCount: iwc.CommentCount,
		parent:       parent,
		labels:       strings.Join(iwc.Issue.Labels, ","),
		deps:         strings.Join(deps, "|"),
		lite:         iwc.Issue.IsLitePartial,
		title:        iwc.Issue.Title,
	}
}

func seedSearchCountsFixture(ctx context.Context, t *testing.T, store *DoltStore) {
	t.Helper()

	issues := []*types.Issue{
		{ID: "counts-parity-a", Title: "alpha root", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "counts-parity-b", Title: "beta child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "counts-parity-c", Title: "gamma blocker", Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeBug},
		{ID: "counts-parity-d", Title: "delta closed", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask},
		{ID: "counts-parity-e", Title: "epsilon leaf", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	if err := store.CloseIssue(ctx, "counts-parity-d", "done", "tester", "s1"); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	// Labels: exercises the labels_json aggregate and the DISTINCT-driving filter.
	for _, lb := range []struct{ id, label string }{
		{"counts-parity-a", "alpha"},
		{"counts-parity-a", "shared"},
		{"counts-parity-c", "alpha"},
		{"counts-parity-e", "shared"},
	} {
		if err := store.AddLabel(ctx, lb.id, lb.label, "tester"); err != nil {
			t.Fatalf("AddLabel %s/%s: %v", lb.id, lb.label, err)
		}
	}

	// Dependencies: blocks (dep_count / rdep_count) and parent-child (parent_id).
	deps := []*types.Dependency{
		{IssueID: "counts-parity-b", DependsOnID: "counts-parity-a", Type: types.DepParentChild},
		{IssueID: "counts-parity-b", DependsOnID: "counts-parity-c", Type: types.DepBlocks},
		{IssueID: "counts-parity-e", DependsOnID: "counts-parity-c", Type: types.DepBlocks},
		{IssueID: "counts-parity-a", DependsOnID: "counts-parity-c", Type: types.DepBlocks},
	}
	for _, d := range deps {
		if err := store.AddDependency(ctx, d, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", d.IssueID, d.DependsOnID, err)
		}
	}

	// Comments: exercises the comment_count aggregate.
	for _, c := range []struct{ id, body string }{
		{"counts-parity-a", "first"},
		{"counts-parity-a", "second"},
		{"counts-parity-c", "only"},
	} {
		if err := store.AddComment(ctx, c.id, "tester", c.body); err != nil {
			t.Fatalf("AddComment %s: %v", c.id, err)
		}
	}
}

// oldSearchCountsSQL renders the pre-refactor counts mega-query shape, with the
// WHERE/ORDER BY/LIMIT applied AFTER the aggregate LEFT JOINs. It is kept here,
// in test code only, as the reference shape that the production
// filter-before-join query in sqlbuild.SearchCountsSQL must stay equivalent to.
func oldSearchCountsSQL(tables sqlbuild.FilterTables, whereSQL, orderBySQL, limitSQL string, includeWispReverseDeps, skipLabels bool) string {
	reverseBlockerSelect := `
				SELECT COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS dep_id
				FROM dependencies WHERE type = 'blocks'
	`
	if includeWispReverseDeps {
		reverseBlockerSelect += `
				UNION ALL
				SELECT COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS dep_id
				FROM wisp_dependencies WHERE type = 'blocks'
		`
	}

	labelsSelect := "l.labels_json AS labels_json"
	labelsJoin := fmt.Sprintf(`
		LEFT JOIN (
			SELECT issue_id, JSON_ARRAYAGG(label) AS labels_json
			FROM %s
			GROUP BY issue_id
		) l ON l.issue_id = i.id`, tables.Labels)
	if skipLabels {
		labelsSelect = "NULL AS labels_json"
		labelsJoin = ""
	}

	return fmt.Sprintf(`
		SELECT %s,
			%s,
			COALESCE(dc.cnt, 0) AS dep_count,
			COALESCE(rc.cnt, 0) AS rdep_count,
			COALESCE(cc.cnt, 0) AS comment_count,
			pc.parent_id     AS parent_id,
			d.deps_json      AS deps_json
		FROM %s i
		%s
		%s
		LEFT JOIN (
			SELECT issue_id, COUNT(*) AS cnt
			FROM %s
			WHERE type = 'blocks'
			GROUP BY issue_id
		) dc ON dc.issue_id = i.id
		LEFT JOIN (
			SELECT dep_id, COUNT(*) AS cnt FROM (
				%s
			) all_blockers GROUP BY dep_id
		) rc ON rc.dep_id = i.id
		LEFT JOIN (
			SELECT issue_id, COUNT(*) AS cnt
			FROM %s
			GROUP BY issue_id
		) cc ON cc.issue_id = i.id
		LEFT JOIN (
			SELECT issue_id,
			       MIN(COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)) AS parent_id
			FROM %s
			WHERE type = 'parent-child'
			GROUP BY issue_id
		) pc ON pc.issue_id = i.id
		LEFT JOIN (
			SELECT issue_id, JSON_ARRAYAGG(%s) AS deps_json
			FROM %s
			GROUP BY issue_id
		) d ON d.issue_id = i.id
		%s
		%s
		%s
	`,
		sqlbuild.ReadyWorkIssueColumns,
		labelsSelect,
		tables.Main,
		sqlbuild.LeaseJoin("i"),
		labelsJoin,
		tables.Dependencies,
		reverseBlockerSelect,
		tables.Comments,
		tables.Dependencies,
		sqlbuild.DepJSONObject,
		tables.Dependencies,
		whereSQL,
		orderBySQL,
		limitSQL,
	)
}
