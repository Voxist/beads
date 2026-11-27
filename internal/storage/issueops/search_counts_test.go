package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

func TestSearchIssuesWithCountsAppliesLimitToEachSourceQuery(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)FROM issues i.*ORDER BY i\.priority ASC, i\.created_at DESC, i\.id ASC\s+LIMIT 3`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)FROM wisps i.*ORDER BY i\.priority ASC, i\.created_at DESC, i\.id ASC\s+LIMIT 3`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	got, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", types.IssueFilter{Limit: 3})
	if err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("SearchIssuesWithCountsInTx returned %d rows, want none", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestSearchIssuesWithCountsHydratesFilteredSearchByID pins the two-phase shape
// of a filtered counts search (vp-6y90n), on the lookup that motivated it: a
// session bead found by metadata.session_name. Phase one selects only ids,
// under the filter, ORDER BY and LIMIT; phase two is exactly the by-IDs form
// for those ids, so every aggregate subquery is id-constrained instead of
// aggregating whole side tables. A phase one that matches nothing must not
// issue a second statement: sqlmock fails any query without an expectation.
func TestSearchIssuesWithCountsHydratesFilteredSearchByID(t *testing.T) {
	t.Parallel()

	yes := true
	filter := types.IssueFilter{
		Ephemeral:      &yes,
		MetadataFields: map[string]string{"session_name": "mayor"},
		Limit:          2,
	}
	hyd := hydrationFor(filter)
	values := func(args []any) []driver.Value {
		out := make([]driver.Value, len(args))
		for i, a := range args {
			out[i] = a
		}
		return out
	}
	phaseOne := func(tables FilterTables) (string, []any) {
		where, args, err := BuildIssueFilterClauses("", filter, tables)
		if err != nil {
			t.Fatalf("BuildIssueFilterClauses: %v", err)
		}
		return sqlbuild.SearchIDsSQL(tables, "WHERE "+joinAnd(where), sqlbuild.OrderBy("", false, "i"), "LIMIT 2"), args
	}

	mock, tx := beginExactMockTx(t)
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	wispIDSQL, wispArgs := phaseOne(WispsFilterTables)
	if !strings.Contains(wispIDSQL, "JSON_EXTRACT(metadata, ?)") {
		t.Fatalf("phase one lost the metadata predicate:\n%s", wispIDSQL)
	}
	mock.ExpectQuery(wispIDSQL).WithArgs(values(wispArgs)...).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("w-2").AddRow("w-1"))

	hydrateSQL, hydrateArgs := sqlbuild.SearchCountsSQL(WispsFilterTables, []string{"w-2", "w-1"}, "", "", "", true, hyd)
	if strings.Contains(hydrateSQL, "SELECT i.*") || !strings.Contains(hydrateSQL, "FROM wisp_labels\n\t\t\tWHERE issue_id IN (?,?)") {
		t.Fatalf("phase two must be the by-IDs form with id-constrained aggregates:\n%s", hydrateSQL)
	}
	// Both rows vanished between the phases: the wisp leg yields nothing, so
	// the ephemeral search falls through to the issues table.
	mock.ExpectQuery(hydrateSQL).WithArgs(values(hydrateArgs)...).
		WillReturnRows(emptyCountsRows())

	issueIDSQL, issueArgs := phaseOne(IssuesFilterTables)
	mock.ExpectQuery(issueIDSQL).WithArgs(values(issueArgs)...).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	got, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("SearchIssuesWithCountsInTx returned %d rows, want none", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestSearchCountsByIDsInTxMatchSizeChoosesTheForm pins the size rule of the
// two-phase search: a match of up to sqlbuild.SearchByIDsMaxRows ids is
// hydrated by id in one statement, and a larger one runs exactly the old
// single-statement predicate form, with the caller's ORDER BY and LIMIT — not
// a by-IDs hydration of hundreds of ids, which costs more than aggregating the
// side tables once. Both honor includeWispReverseDeps=false.
func TestSearchCountsByIDsInTxMatchSizeChoosesTheForm(t *testing.T) {
	t.Parallel()

	hyd := sqlbuild.CountsHydration{SkipLabels: true}
	where := "WHERE i.status = ?"
	order := sqlbuild.OrderBy("created", false, "i")
	idRows := func(n int) (*sqlmock.Rows, []string) {
		rows := sqlmock.NewRows([]string{"id"})
		ids := make([]string, n)
		for i := range ids {
			ids[i] = fmt.Sprintf("bd-%03d", i)
			rows.AddRow(ids[i])
		}
		return rows, ids
	}

	for _, tc := range []struct {
		name     string
		matched  int
		limitSQL string
		byIDs    bool
	}{
		{name: "at the bound, unlimited", matched: sqlbuild.SearchByIDsMaxRows, byIDs: true},
		{name: "past the bound, unlimited", matched: sqlbuild.SearchByIDsMaxRows + 1},
		{name: "past the bound, caller limit", matched: sqlbuild.SearchByIDsMaxRows + 20, limitSQL: fmt.Sprintf("LIMIT %d", sqlbuild.SearchByIDsMaxRows+20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mock, tx := beginExactMockTx(t)
			rows, ids := idRows(tc.matched)
			mock.ExpectQuery(sqlbuild.SearchIDsSQL(IssuesFilterTables, where, order, tc.limitSQL)).
				WithArgs("open").WillReturnRows(rows)
			var second string
			if tc.byIDs {
				second, _ = sqlbuild.SearchCountsSQL(IssuesFilterTables, ids, "", "", "", false, hyd)
				mock.ExpectQuery(second).WillReturnRows(emptyCountsRows())
			} else {
				second, _ = sqlbuild.SearchCountsSQL(IssuesFilterTables, nil, where, order, tc.limitSQL, false, hyd)
				mock.ExpectQuery(second).WithArgs("open").WillReturnRows(emptyCountsRows())
			}
			if strings.Contains(second, "wisp_dependencies") {
				t.Fatalf("includeWispReverseDeps=false must not read wisp_dependencies:\n%s", second)
			}

			if _, err := searchCountsByIDsInTx(context.Background(), tx, IssuesFilterTables, where, order, tc.limitSQL, []any{"open"}, false, hyd); err != nil {
				t.Fatalf("searchCountsByIDsInTx: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// beginExactMockTx is beginMockTx with sqlmock's equality matcher, which
// compares whitespace-collapsed SQL text exactly: the two-phase tests assert
// the precise statements a search issues, not a pattern they contain.
func beginExactMockTx(t *testing.T) (sqlmock.Sqlmock, *sql.Tx) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return mock, tx
}

// Regression for bd-6dnrw.42: SkipWisps must suppress the wisps probe and
// merge, matching SearchIssuesInTx and the domain/db counts path. sqlmock
// rejects any query without a matching expectation, so only the
// wisp_dependencies probe and the issues query may run.
func TestSearchIssuesWithCountsHonorsSkipWisps(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)FROM issues i.*ORDER BY i\.priority ASC, i\.created_at DESC, i\.id ASC`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	got, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", types.IssueFilter{SkipWisps: true})
	if err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("SearchIssuesWithCountsInTx returned %d rows, want none", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Regression for bd-6dnrw.43: SortBy/SortDesc must reach the per-table SQL
// ORDER BY; otherwise LIMIT is applied under priority order and the wrong
// row SET survives (not just the wrong order).
func TestSearchIssuesWithCountsPushesSortIntoSQL(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)FROM issues i.*ORDER BY i\.created_at DESC, i\.id ASC\s+LIMIT 2`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)FROM wisps i.*ORDER BY i\.created_at DESC, i\.id ASC\s+LIMIT 2`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	filter := types.IssueFilter{SortBy: "created", Limit: 2}
	if _, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", filter); err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Regression for bd-6dnrw.43 (merge half): when issues and wisps results are
// merged before the limit cut, the in-memory sort must follow the requested
// SortBy, or the surviving row set reverts to priority order.
func TestFinishSearchIssuesWithCountsTruncatesByRequestedSort(t *testing.T) {
	t.Parallel()

	at := func(day int) time.Time {
		return time.Date(2026, 6, day, 0, 0, 0, 0, time.UTC)
	}
	iwc := func(id string, priority int, created time.Time) *types.IssueWithCounts {
		return &types.IssueWithCounts{Issue: &types.Issue{ID: id, Priority: priority, CreatedAt: created}}
	}
	// P0 but oldest; the two newest are low priority.
	items := []*types.IssueWithCounts{
		iwc("bd-old-p0", 0, at(1)),
		iwc("bd-new-p3", 3, at(9)),
		iwc("bd-mid-p2", 2, at(5)),
		iwc("bd-newest-p4", 4, at(10)),
	}

	got, err := finishSearchIssuesWithCounts(items, types.IssueFilter{SortBy: "created", Limit: 2})
	if err != nil {
		t.Fatalf("sort=created limit=2: unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Issue.ID != "bd-newest-p4" || got[1].Issue.ID != "bd-new-p3" {
		ids := make([]string, len(got))
		for i, g := range got {
			ids[i] = g.Issue.ID
		}
		t.Fatalf("sort=created limit=2 kept %v, want [bd-newest-p4 bd-new-p3]", ids)
	}

	// SortDesc flips the per-key default direction: created becomes ASC.
	got, err = finishSearchIssuesWithCounts(items, types.IssueFilter{SortBy: "created", SortDesc: true, Limit: 2})
	if err != nil {
		t.Fatalf("sort=created desc limit=2: unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Issue.ID != "bd-old-p0" || got[1].Issue.ID != "bd-mid-p2" {
		ids := make([]string, len(got))
		for i, g := range got {
			ids[i] = g.Issue.ID
		}
		t.Fatalf("sort=created desc limit=2 kept %v, want [bd-old-p0 bd-mid-p2]", ids)
	}

	// Default (no SortBy) keeps the historical priority/created/id order.
	got, err = finishSearchIssuesWithCounts(items, types.IssueFilter{Limit: 2})
	if err != nil {
		t.Fatalf("default sort limit=2: unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Issue.ID != "bd-old-p0" || got[1].Issue.ID != "bd-mid-p2" {
		ids := make([]string, len(got))
		for i, g := range got {
			ids[i] = g.Issue.ID
		}
		t.Fatalf("default sort limit=2 kept %v, want [bd-old-p0 bd-mid-p2]", ids)
	}
}
