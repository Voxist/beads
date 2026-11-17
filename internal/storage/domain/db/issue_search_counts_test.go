package db

import (
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueSearchAcrossIssuesAndWispsWithCounts() {
	s.Run("DependencyAndDependentCounts", s.searchCountsDepAndRDep)
	s.Run("CommentCount", s.searchCountsComment)
	s.Run("ParentPopulated", s.searchCountsParent)
	s.Run("MergesIssuesAndWisps", s.searchCountsMergesTables)
	s.Run("SkipWispsExcludesWisps", s.searchCountsSkipWisps)
	s.Run("EphemeralTrueOnlyWisps", s.searchCountsEphemeralOnly)
	s.Run("LabelHydration", s.searchCountsLabelHydration)
	s.Run("SkipLabelsLeavesEmpty", s.searchCountsSkipLabels)
	s.Run("SortByPriorityThenCreatedAt", s.searchCountsSortOrder)
	s.Run("LimitRespected", s.searchCountsLimit)
	s.Run("CollisionAcrossTablesKeepsTheWispCopy", s.searchCountsCollision)
	s.Run("PredicateFormIDPrefixMatchesIDList", s.searchCountsPredicateIDFilterParity)
	s.Run("PredicateFormIDPrefixMatchesIDListOnWispPlane", s.searchCountsWispPredicateIDFilterParity)
	s.Run("ByIDsFormMatchesPredicateForm", s.searchCountsByIDsFormMatchesPredicateForm)
	s.Run("TwoPhaseMatchesSingleStatement", s.searchCountsTwoPhaseMatchesSingleStatement)
}

func (s *testSuite) searchCountsDepAndRDep() {
	r := s.issueRepo()
	dep := s.depRepo()

	mid := newTestIssue("bd-srxc-dr-mid", "mid")
	s.Require().NoError(r.Insert(s.Ctx(), mid, "tester", domain.InsertIssueOpts{}))
	a := newTestIssue("bd-srxc-dr-a", "a")
	s.Require().NoError(r.Insert(s.Ctx(), a, "tester", domain.InsertIssueOpts{}))
	b := newTestIssue("bd-srxc-dr-b", "b")
	s.Require().NoError(r.Insert(s.Ctx(), b, "tester", domain.InsertIssueOpts{}))
	c := newTestIssue("bd-srxc-dr-c", "c")
	s.Require().NoError(r.Insert(s.Ctx(), c, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-dr-mid", "bd-srxc-dr-a", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-dr-mid", "bd-srxc-dr-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-dr-c", "bd-srxc-dr-mid", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDs: []string{"bd-srxc-dr-mid"}, SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 1)
	s.Equal(2, out.Items[0].DependencyCount, "outgoing blocks count")
	s.Equal(1, out.Items[0].DependentCount, "incoming blocks count")
}

func (s *testSuite) searchCountsComment() {
	r := s.issueRepo()
	issue := newTestIssue("bd-srxc-cmt-1", "with comments")
	s.Require().NoError(r.Insert(s.Ctx(), issue, "tester", domain.InsertIssueOpts{}))

	for i := 0; i < 3; i++ {
		_, err := s.Runner().ExecContext(s.Ctx(),
			"INSERT INTO comments (id, issue_id, author, text) VALUES (UUID(), ?, ?, ?)",
			"bd-srxc-cmt-1", "tester", "comment")
		s.Require().NoError(err)
	}

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDs: []string{"bd-srxc-cmt-1"}, SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 1)
	s.Equal(3, out.Items[0].CommentCount)
}

func (s *testSuite) searchCountsParent() {
	r := s.issueRepo()
	dep := s.depRepo()
	parent := newTestIssue("bd-srxc-par-parent", "parent")
	s.Require().NoError(r.Insert(s.Ctx(), parent, "tester", domain.InsertIssueOpts{}))
	child := newTestIssue("bd-srxc-par-child", "child")
	s.Require().NoError(r.Insert(s.Ctx(), child, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(),
		newDep("bd-srxc-par-child", "bd-srxc-par-parent", types.DepParentChild), "tester", domain.DepInsertOpts{}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDs: []string{"bd-srxc-par-child"}, SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 1)
	s.Require().NotNil(out.Items[0].Parent)
	s.Equal("bd-srxc-par-parent", *out.Items[0].Parent)
}

func (s *testSuite) searchCountsMergesTables() {
	r := s.issueRepo()
	perm := newTestIssue("bd-srxc-mrg-perm", "perm")
	s.Require().NoError(r.Insert(s.Ctx(), perm, "tester", domain.InsertIssueOpts{}))

	w := newTestIssue("bd-srxc-mrg-wisp", "wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-mrg-"})
	s.Require().NoError(err)
	got := iwcIDs(out)
	s.Contains(got, "bd-srxc-mrg-perm")
	s.Contains(got, "bd-srxc-mrg-wisp")
}

func (s *testSuite) searchCountsSkipWisps() {
	r := s.issueRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue("bd-srxc-sk-perm", "perm"), "tester", domain.InsertIssueOpts{}))
	w := newTestIssue("bd-srxc-sk-wisp", "wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-sk-", SkipWisps: true})
	s.Require().NoError(err)
	got := iwcIDs(out)
	s.Contains(got, "bd-srxc-sk-perm")
	s.NotContains(got, "bd-srxc-sk-wisp")
}

func (s *testSuite) searchCountsEphemeralOnly() {
	r := s.issueRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue("bd-srxc-eo-perm", "perm"), "tester", domain.InsertIssueOpts{}))
	w := newTestIssue("bd-srxc-eo-wisp", "wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	yes := true
	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-eo-", Ephemeral: &yes})
	s.Require().NoError(err)
	got := iwcIDs(out)
	s.Contains(got, "bd-srxc-eo-wisp")
	s.NotContains(got, "bd-srxc-eo-perm")
}

func (s *testSuite) searchCountsLabelHydration() {
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())
	issue := newTestIssue("bd-srxc-lbl-1", "labeled")
	s.Require().NoError(r.Insert(s.Ctx(), issue, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srxc-lbl-1", "alpha", "tester", domain.LabelOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srxc-lbl-1", "beta", "tester", domain.LabelOpts{}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDs: []string{"bd-srxc-lbl-1"}, SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 1)
	s.ElementsMatch([]string{"alpha", "beta"}, out.Items[0].Issue.Labels)
}

func (s *testSuite) searchCountsSkipLabels() {
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())
	issue := newTestIssue("bd-srxc-nolbl-1", "labeled")
	s.Require().NoError(r.Insert(s.Ctx(), issue, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srxc-nolbl-1", "gamma", "tester", domain.LabelOpts{}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDs: []string{"bd-srxc-nolbl-1"}, SkipWisps: true, SkipLabels: true})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 1)
	s.Empty(out.Items[0].Issue.Labels)
}

func (s *testSuite) searchCountsSortOrder() {
	r := s.issueRepo()
	hi := newTestIssue("bd-srxc-srt-hi", "hi")
	hi.Priority = 1
	s.Require().NoError(r.Insert(s.Ctx(), hi, "tester", domain.InsertIssueOpts{}))
	mid := newTestIssue("bd-srxc-srt-mid", "mid")
	mid.Priority = 2
	s.Require().NoError(r.Insert(s.Ctx(), mid, "tester", domain.InsertIssueOpts{}))
	lo := newTestIssue("bd-srxc-srt-lo", "lo")
	lo.Priority = 3
	s.Require().NoError(r.Insert(s.Ctx(), lo, "tester", domain.InsertIssueOpts{}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-srt-", SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 3)
	s.Equal("bd-srxc-srt-hi", out.Items[0].Issue.ID)
	s.Equal("bd-srxc-srt-mid", out.Items[1].Issue.ID)
	s.Equal("bd-srxc-srt-lo", out.Items[2].Issue.ID)
}

func (s *testSuite) searchCountsLimit() {
	r := s.issueRepo()
	for i := 0; i < 5; i++ {
		s.Require().NoError(r.Insert(s.Ctx(),
			newTestIssue("bd-srxc-lim-"+string(rune('a'+i)), "x"),
			"tester", domain.InsertIssueOpts{}))
	}
	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-lim-", Limit: 3, SkipWisps: true})
	s.Require().NoError(err)
	s.Len(out.Items, 3)
}

// searchCountsCollision pins what a CROSS-PLANE DUPLICATE answers with. One id resident in both
// tables is corruption — no local write path can produce it, only replication —
// and this read used to fail the whole query over it, which left a store with
// one bad id unable to answer any question about the others.
//
// The canonical copy is the WISPS one, and the read answers with it. That is
// the verdict the per-table seam has always reached (issueops, be-iabdi) and
// the one `bd doctor --check=validate --fix` acts on: it deletes the stale
// ISSUES copy, the same row this drops. scanIDSrcPage carries the full
// argument.
func (s *testSuite) searchCountsCollision() {
	r := s.issueRepo()
	const id = "bd-srxc-coll-1"
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, "perm"), "tester", domain.InsertIssueOpts{}))
	w := newTestIssue(id, "wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-coll-"})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 1, "the id must come back once, not once per plane")
	s.Require().NotNil(out.Items[0].Issue)
	s.Equal("wisp", out.Items[0].Issue.Title, "the wisps copy is canonical; the issues copy is the stale one")
}

// searchCountsPredicateIDFilterParity pins that the predicate form projects the
// same counts and JSON whether the driver's row set is bounded by an id prefix
// or by an explicit id list.
//
// Both reads below take the SAME query form, and that is worth stating because
// the names in this area invite the opposite reading. SkipWisps routes past the
// union in searchAcrossIssuesAndWispsWithCounts, so each read lands on the
// single-plane predicate form, and types.IssueFilter.IDs renders as one more
// clause inside that form's whereSQL rather than selecting the by-IDs form —
// which is reached only through the union's fetchCountsByIDs. So this is
// coverage of the predicate form under two different bindings, NOT a comparison
// of the two forms; searchCountsByIDsFormMatchesPredicateForm is that one.
//
// Every count/JSON field the mega-query projects is exercised (dep/rdep/comment
// counts, parent, labels, deps_json), so a change that dropped rows from one
// subquery's bound surfaces here rather than passing as a coincidental 0 == 0.
func (s *testSuite) searchCountsPredicateIDFilterParity() {
	r := s.issueRepo()
	dep := s.depRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())

	parent := newTestIssue("bd-srxc-par2-parent", "parent")
	s.Require().NoError(r.Insert(s.Ctx(), parent, "tester", domain.InsertIssueOpts{}))
	mid := newTestIssue("bd-srxc-par2-mid", "mid")
	s.Require().NoError(r.Insert(s.Ctx(), mid, "tester", domain.InsertIssueOpts{}))
	a := newTestIssue("bd-srxc-par2-a", "a")
	s.Require().NoError(r.Insert(s.Ctx(), a, "tester", domain.InsertIssueOpts{}))
	b := newTestIssue("bd-srxc-par2-b", "b")
	s.Require().NoError(r.Insert(s.Ctx(), b, "tester", domain.InsertIssueOpts{}))

	// mid depends on a (outgoing/DependencyCount); b depends on mid (incoming/
	// DependentCount); mid is a child of parent (Parent); mid has a label and
	// a comment. Every projected count/JSON field is nonzero so a subquery
	// that silently drops mid's rows would show up as a mismatch, not a
	// coincidental 0 == 0.
	//
	// The incoming edge comes from a distinct issue b, not from a: mid -> a
	// plus a -> mid is a 2-cycle, and Insert rejects scheduling deps that
	// close one (domain.ErrDependencyCycle). parent cannot play that role
	// either — a blocker that is the issue's ancestor trips
	// ValidateBlockingHierarchy — so the incoming blocker needs its own issue.
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-par2-mid", "bd-srxc-par2-a", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-par2-b", "bd-srxc-par2-mid", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-par2-mid", "bd-srxc-par2-parent", types.DepParentChild), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srxc-par2-mid", "alpha", "tester", domain.LabelOpts{}))
	_, err := s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO comments (id, issue_id, author, text) VALUES (UUID(), ?, ?, ?)",
		"bd-srxc-par2-mid", "tester", "comment")
	s.Require().NoError(err)

	// IDPrefix bounds the driver with a LIKE; IDs bounds it with an IN. Same
	// issue, same filter intent, same query form.
	byPrefix, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-par2-mid", SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(byPrefix.Items, 1)

	byIDList, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDs: []string{"bd-srxc-par2-mid"}, SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(byIDList.Items, 1)

	p, i := byPrefix.Items[0], byIDList.Items[0]
	// Guard the premise before comparing: every parity assertion below is
	// vacuous if the fixture left the reference side at zero, so pin the
	// reference side's counts as nonzero first. Without this a fixture that
	// stopped producing edges would keep passing as 0 == 0.
	s.Require().NotZero(i.DependencyCount, "fixture must give the reference side an outgoing dep")
	s.Require().NotZero(i.DependentCount, "fixture must give the reference side an incoming dep")
	s.Require().NotZero(i.CommentCount, "fixture must give the reference side a comment")
	s.Require().NotEmpty(i.Issue.Labels, "fixture must give the reference side a label")
	s.Require().NotEmpty(i.Issue.Dependencies, "fixture must give the reference side deps_json rows")
	s.Equal(i.DependencyCount, p.DependencyCount, "dependency_count parity")
	s.Equal(i.DependentCount, p.DependentCount, "dependent_count parity")
	s.Equal(i.CommentCount, p.CommentCount, "comment_count parity")
	s.Require().NotNil(i.Parent)
	s.Require().NotNil(p.Parent)
	s.Equal(*i.Parent, *p.Parent, "parent parity")
	s.ElementsMatch(i.Issue.Labels, p.Issue.Labels, "labels parity")
	s.Require().Len(p.Issue.Dependencies, len(i.Issue.Dependencies), "deps_json length parity")
}

// searchCountsWispPredicateIDFilterParity is the wisp-plane twin of
// searchCountsPredicateIDFilterParity, and carries the same caveat: Ephemeral
// pins both reads to the single wisp plane, so both take the predicate form.
//
// It earns its place anyway, because the two planes resolve labels through
// different tables — the wisp plane reads wisp_labels — and that join had no
// coverage at this seam at all. Note which assertion carries the weight: a
// label bound that dropped the wisp would empty BOTH sides, so the ElementsMatch
// below would still pass. The NotEmpty premise guard is what fails.
func (s *testSuite) searchCountsWispPredicateIDFilterParity() {
	r := s.issueRepo()
	uc := s.labelUseCase()

	w := newTestIssue("bd-srxc-wpar-1", "wisp parity")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))
	s.Require().NoError(uc.AddWispLabel(s.Ctx(), "bd-srxc-wpar-1", "alpha", "tester"))
	s.Require().NoError(uc.AddWispLabel(s.Ctx(), "bd-srxc-wpar-1", "beta", "tester"))

	// Ephemeral pins both reads to the wisp plane; IDPrefix bounds the driver
	// with a LIKE and IDs with an IN. Same wisp, same filter intent.
	yes := true
	byPrefix, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-wpar-1", Ephemeral: &yes})
	s.Require().NoError(err)
	s.Require().Len(byPrefix.Items, 1)

	byIDList, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDs: []string{"bd-srxc-wpar-1"}, Ephemeral: &yes})
	s.Require().NoError(err)
	s.Require().Len(byIDList.Items, 1)

	p, i := byPrefix.Items[0], byIDList.Items[0]
	// Guard the premise first: labels parity is vacuous if the reference side
	// came back empty, which is exactly what a wisp_labels bound that dropped
	// the row would look like.
	s.Require().NotEmpty(i.Issue.Labels, "fixture must give the reference side wisp labels")
	s.ElementsMatch(i.Issue.Labels, p.Issue.Labels, "wisp labels parity")
	s.Equal(i.DependencyCount, p.DependencyCount, "dependency_count parity")
	s.Equal(i.DependentCount, p.DependentCount, "dependent_count parity")
	s.Equal(i.CommentCount, p.CommentCount, "comment_count parity")
}

// searchCountsByIDsFormMatchesPredicateForm is the cross-form parity test the
// two subtests above are not. SearchCountsSQL renders two shapes, and which one
// a read gets is decided by the dispatch in
// searchAcrossIssuesAndWispsWithCounts — never by the filter's id fields:
//
//   - SkipWisps, Ephemeral-only, or an empty/missing wisps plane each take one
//     plane through runFilterSearchQuery, which renders the PREDICATE form
//     (ids nil, whereSQL bounding a derived driver subquery).
//   - anything else unions the two planes and hydrates the resulting page
//     through fetchCountsByIDs, which renders the BY-IDS form (whereSQL empty,
//     an id predicate at the outer level).
//
// So reaching the by-IDs form takes a search that spans both planes against a
// populated wisps plane — no id filter selects it. This drives one issue both
// ways and asserts the two shapes project identical counts and JSON, which is
// the equivalence a change to either shape has to preserve.
func (s *testSuite) searchCountsByIDsFormMatchesPredicateForm() {
	r := s.issueRepo()
	dep := s.depRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())

	parent := newTestIssue("bd-srxc-xform-parent", "parent")
	s.Require().NoError(r.Insert(s.Ctx(), parent, "tester", domain.InsertIssueOpts{}))
	mid := newTestIssue("bd-srxc-xform-mid", "mid")
	s.Require().NoError(r.Insert(s.Ctx(), mid, "tester", domain.InsertIssueOpts{}))
	a := newTestIssue("bd-srxc-xform-a", "a")
	s.Require().NoError(r.Insert(s.Ctx(), a, "tester", domain.InsertIssueOpts{}))
	b := newTestIssue("bd-srxc-xform-b", "b")
	s.Require().NoError(r.Insert(s.Ctx(), b, "tester", domain.InsertIssueOpts{}))

	// wispsTableEmptyOrMissing short-circuits an empty wisp plane back to the
	// single-plane predicate form, which would quietly turn this into a third
	// predicate-vs-predicate test. Seed a wisp so the union leg is really
	// taken. It must NOT match the search prefix: the point is to compare one
	// row across two forms, not to merge two rows.
	w := newTestIssue("bd-srxc-xformwisp-1", "wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	// Same edge shape, and same reason, as searchCountsPredicateIDFilterParity:
	// mid -> a plus a -> mid is a 2-cycle Insert rejects, and a blocker that is
	// the issue's ancestor trips ValidateBlockingHierarchy, so the incoming
	// edge needs its own issue b.
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-xform-mid", "bd-srxc-xform-a", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-xform-b", "bd-srxc-xform-mid", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-xform-mid", "bd-srxc-xform-parent", types.DepParentChild), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srxc-xform-mid", "alpha", "tester", domain.LabelOpts{}))
	_, err := s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO comments (id, issue_id, author, text) VALUES (UUID(), ?, ?, ?)",
		"bd-srxc-xform-mid", "tester", "comment")
	s.Require().NoError(err)

	// Neither SkipWisps nor Ephemeral: the read spans both planes, so the page
	// is hydrated through fetchCountsByIDs — the by-IDs form.
	byIDsForm, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-xform-mid"})
	s.Require().NoError(err)
	s.Require().Len(byIDsForm.Items, 1)

	// SkipWisps on the otherwise-identical filter drops to the predicate form.
	byPredicateForm, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-xform-mid", SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(byPredicateForm.Items, 1)

	i, p := byIDsForm.Items[0], byPredicateForm.Items[0]
	// The predicate form is the reference side, so guard ITS premise: two forms
	// that both return nothing agree perfectly. Guarding the reference and then
	// comparing the by-IDs form against it is what makes a bound that is wrong
	// in only one form fail on the parity line rather than on a guard.
	s.Require().NotZero(p.DependencyCount, "fixture must give the reference side an outgoing dep")
	s.Require().NotZero(p.DependentCount, "fixture must give the reference side an incoming dep")
	s.Require().NotZero(p.CommentCount, "fixture must give the reference side a comment")
	s.Require().NotEmpty(p.Issue.Labels, "fixture must give the reference side a label")
	s.Require().NotEmpty(p.Issue.Dependencies, "fixture must give the reference side deps_json rows")
	s.Equal(p.DependencyCount, i.DependencyCount, "dependency_count parity across forms")
	s.Equal(p.DependentCount, i.DependentCount, "dependent_count parity across forms")
	s.Equal(p.CommentCount, i.CommentCount, "comment_count parity across forms")
	s.Require().NotNil(p.Parent)
	s.Require().NotNil(i.Parent)
	s.Equal(*p.Parent, *i.Parent, "parent parity across forms")
	s.ElementsMatch(p.Issue.Labels, i.Issue.Labels, "labels parity across forms")
	s.Require().Len(i.Issue.Dependencies, len(p.Issue.Dependencies), "deps_json length parity across forms")
}

func iwcIDs(page domain.SearchCountsPage) []string {
	out := make([]string, 0, len(page.Items))
	for _, iwc := range page.Items {
		if iwc == nil || iwc.Issue == nil {
			continue
		}
		out = append(out, iwc.Issue.ID)
	}
	return out
}

// searchCountsTwoPhaseMatchesSingleStatement is the vp-6y90n contract for the
// domain/db stack: runSearchQuery, which now selects the matching ids first
// and hydrates them through the by-IDs form, returns exactly the rows (ids,
// order, counts, parent, labels, deps JSON) the single-statement predicate
// form returned. It drives runSearchQuery directly so it can cover what the
// public dispatch fixes for it: both table families, includeWispReverseDeps on
// and off, every hydration opt-out, ORDER BY + LIMIT, an empty match and a
// match on either side of sqlbuild.SearchByIDsMaxRows. Every statement the
// two-phase side runs for a by-IDs-sized match is recorded, and none may be a
// counts query with an aggregate that is not id-constrained.
func (s *testSuite) searchCountsTwoPhaseMatchesSingleStatement() {
	r := s.issueRepo()
	dep := s.depRepo()
	labels := NewLabelSQLRepository(s.Runner())
	uc := s.labelUseCase()

	for i, p := range []int{2, 0, 1, 3} {
		in := newTestIssue(fmt.Sprintf("bd-tp-i%d", i), fmt.Sprintf("issue %c", 'd'-rune(i)))
		in.Priority = p
		if i == 1 {
			in.Metadata = []byte(`{"session_name":"mayor"}`)
		}
		s.Require().NoError(r.Insert(s.Ctx(), in, "tester", domain.InsertIssueOpts{}))
	}
	for i, sess := range []string{"mayor", "deacon", "mayor", "witness"} {
		w := newTestIssue(fmt.Sprintf("bd-tp-w%d", i), fmt.Sprintf("wisp %c", 'a'+rune(i)))
		w.Ephemeral = true
		w.Priority = i % 3
		w.Metadata = []byte(fmt.Sprintf(`{"session_name":%q}`, sess))
		s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))
	}
	s.Require().NoError(labels.Insert(s.Ctx(), "bd-tp-i0", "alpha", "tester", domain.LabelOpts{}))
	s.Require().NoError(labels.Insert(s.Ctx(), "bd-tp-i1", "alpha", "tester", domain.LabelOpts{}))
	s.Require().NoError(uc.AddWispLabel(s.Ctx(), "bd-tp-w0", "hot", "tester"))
	s.Require().NoError(uc.AddWispLabel(s.Ctx(), "bd-tp-w2", "hot", "tester"))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-tp-i1", "bd-tp-i0", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-tp-i2", "bd-tp-i3", types.DepParentChild), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-tp-w1", "bd-tp-w0", types.DepBlocks), "tester", domain.DepInsertOpts{UseWispsTable: true}))
	// A wisp blocking on a durable issue: counted in that issue's rdep_count
	// only when includeWispReverseDeps unions wisp_dependencies in.
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-tp-w2", "bd-tp-i0", types.DepBlocks), "tester", domain.DepInsertOpts{UseWispsTable: true}))
	_, err := s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO comments (id, issue_id, author, text) VALUES (UUID(), ?, ?, ?)", "bd-tp-i0", "tester", "c")
	s.Require().NoError(err)
	_, err = s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO wisp_comments (id, issue_id, author, text) VALUES (UUID(), ?, ?, ?)", "bd-tp-w0", "tester", "c")
	s.Require().NoError(err)

	// Enough labeled rows per plane to pass SearchByIDsMaxRows, so the
	// single-statement side of the size rule is compared too.
	for i := 0; i < sqlbuild.SearchByIDsMaxRows+5; i++ {
		_, err := s.Runner().ExecContext(s.Ctx(),
			"INSERT INTO issues (id, title, status, priority, issue_type, description, design, acceptance_criteria, notes) VALUES (?, ?, 'open', ?, 'task', '', '', '', '')",
			fmt.Sprintf("bd-tpbulk-i%03d", i), fmt.Sprintf("bulk %03d", (i*37)%101), i%4)
		s.Require().NoError(err)
		_, err = s.Runner().ExecContext(s.Ctx(),
			"INSERT INTO wisps (id, title, status, priority, issue_type, ephemeral) VALUES (?, ?, 'open', ?, 'task', 1)",
			fmt.Sprintf("bd-tpbulk-w%03d", i), fmt.Sprintf("bulk %03d", (i*37)%101), i%4)
		s.Require().NoError(err)
	}

	type plane struct {
		name   string
		tables filterTables
	}
	planes := []plane{{"issues", issuesFilterTables}, {"wisps", wispsFilterTables}}
	cases := []struct {
		name   string
		filter types.IssueFilter
		limit  string
		empty  bool
	}{
		{name: "prefix", filter: types.IssueFilter{IDPrefix: "bd-tp-"}},
		{name: "prefix order created limit 2", filter: types.IssueFilter{IDPrefix: "bd-tp-", SortBy: "created"}, limit: "LIMIT 2"},
		{name: "prefix order title desc limit 3", filter: types.IssueFilter{IDPrefix: "bd-tp-", SortBy: "title", SortDesc: true}, limit: "LIMIT 3"},
		{name: "prefix default order limit 1", filter: types.IssueFilter{IDPrefix: "bd-tp-"}, limit: "LIMIT 1"},
		{name: "skip labels", filter: types.IssueFilter{IDPrefix: "bd-tp-", SkipLabels: true}},
		{name: "skip counts", filter: types.IssueFilter{IDPrefix: "bd-tp-", SkipCounts: true}},
		{name: "lite", filter: types.IssueFilter{IDPrefix: "bd-tp-", Lite: true}},
		{name: "session_name", filter: types.IssueFilter{MetadataFields: map[string]string{"session_name": "mayor"}}},
		{name: "empty match", filter: types.IssueFilter{MetadataFields: map[string]string{"session_name": "nobody"}}, empty: true},
		{name: "past the bound", filter: types.IssueFilter{IDPrefix: "bd-tpbulk-"}},
		{name: "past the bound order title limit", filter: types.IssueFilter{IDPrefix: "bd-tpbulk-", SortBy: "title"}, limit: fmt.Sprintf("LIMIT %d", sqlbuild.SearchByIDsMaxRows+3)},
		{name: "at the bound order title limit", filter: types.IssueFilter{IDPrefix: "bd-tpbulk-", SortBy: "title"}, limit: fmt.Sprintf("LIMIT %d", sqlbuild.SearchByIDsMaxRows)},
	}

	flatten := func(items []*types.IssueWithCounts) []string {
		out := make([]string, 0, len(items))
		for _, iwc := range items {
			parent := ""
			if iwc.Parent != nil {
				parent = *iwc.Parent
			}
			deps := make([]string, 0, len(iwc.Issue.Dependencies))
			for _, d := range iwc.Issue.Dependencies {
				deps = append(deps, string(d.Type)+":"+d.DependsOnID)
			}
			out = append(out, fmt.Sprintf("%s dep=%d rdep=%d cmt=%d parent=%s labels=%s deps=%s lite=%v title=%q",
				iwc.Issue.ID, iwc.DependencyCount, iwc.DependentCount, iwc.CommentCount, parent,
				strings.Join(iwc.Issue.Labels, ","), strings.Join(deps, "|"), iwc.Issue.IsLitePartial, iwc.Issue.Title))
		}
		return out
	}

	rec := &recordingRunner{Runner: s.Runner()}
	twoPhase := &issueSQLRepositoryImpl{runner: rec}
	single := r.(*issueSQLRepositoryImpl)
	sawRows, sawWispRdep := false, map[bool]bool{}
	for _, pl := range planes {
		for _, wispDeps := range []bool{true, false} {
			for _, tc := range cases {
				name := fmt.Sprintf("%s/wispDeps=%v/%s", pl.name, wispDeps, tc.name)
				where, args, err := buildIssueFilterClauses("", tc.filter, pl.tables)
				s.Require().NoError(err, name)
				s.Require().NotEmpty(where, name)
				whereSQL := "WHERE " + strings.Join(where, " AND ")
				order := orderBySQL(tc.filter.SortBy, tc.filter.SortDesc, "i")
				hyd := hydrationFor(tc.filter)

				oldSQL, _ := sqlbuild.SearchCountsSQL(pl.tables, nil, whereSQL, order, tc.limit, wispDeps, hyd)
				want, err := single.scanCountsQuery(s.Ctx(), pl.tables, oldSQL, args, hyd)
				s.Require().NoError(err, name)

				rec.reset()
				got, err := twoPhase.runSearchQuery(s.Ctx(), pl.tables, whereSQL, order, tc.limit, args, wispDeps, hyd)
				s.Require().NoError(err, name)

				calls := rec.reset()
				if tc.empty {
					s.Require().Empty(want, "%s: empty-match premise", name)
					s.Require().Len(calls, 1, "%s: an empty match must not issue a hydration statement", name)
				} else {
					s.Require().NotEmpty(want, "%s: fixture must give the reference side rows", name)
					sawRows = true
				}
				s.Require().Equal(flatten(want), flatten(got), name)
				for _, iwc := range got {
					if iwc.Issue.ID == "bd-tp-i0" && iwc.DependentCount > 1 {
						sawWispRdep[wispDeps] = true
					}
				}
				for _, c := range calls {
					if len(want) <= sqlbuild.SearchByIDsMaxRows && strings.Contains(c.query, "AS comment_count") {
						s.Require().Empty(unconstrainedCountsAggregates(c.query), "%s: aggregate subquery without id constraint", name)
					}
				}
			}
		}
	}
	s.Require().True(sawRows, "fixture premise: some case must match rows")
	s.Require().True(sawWispRdep[true], "fixture premise: a wisp blocker must reach a durable rdep_count with wispDeps on")
	s.Require().False(sawWispRdep[false], "wispDeps off must leave wisp blockers out of rdep_count")
}

// unconstrainedCountsAggregates returns each labels/dependencies/comments
// subquery of a rendered counts query that carries no IN (...) constraint (the
// FROM clause up to the end of its WHERE). Each one aggregates a whole table.
func unconstrainedCountsAggregates(sqlText string) []string {
	side := map[string]bool{}
	for _, ft := range []filterTables{issuesFilterTables, wispsFilterTables} {
		side[ft.Labels], side[ft.Dependencies], side[ft.Comments] = true, true, true
	}
	var bad []string
	for _, part := range strings.Split(sqlText, "FROM ")[1:] {
		fields := strings.Fields(part)
		if len(fields) == 0 || !side[fields[0]] {
			continue
		}
		end := len(part)
		for _, stop := range []string{"GROUP BY", "UNION ALL", ") all_blockers"} {
			if i := strings.Index(part, stop); i >= 0 && i < end {
				end = i
			}
		}
		if !strings.Contains(part[:end], " IN (") {
			bad = append(bad, "FROM "+strings.TrimSpace(part[:end]))
		}
	}
	return bad
}

// TestRunSearchQueryHydratesFilteredSearchByID pins the statements the
// domain/db counts search issues for a filter (vp-6y90n): phase one selects
// only the matching ids, phase two is exactly the by-IDs form for them, and
// an empty match issues no second statement (sqlmock fails any query without
// an expectation), and a match past sqlbuild.SearchByIDsMaxRows runs the
// single-statement predicate form instead.
func TestRunSearchQueryHydratesFilteredSearchByID(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := &issueSQLRepositoryImpl{runner: db}

	where := "WHERE JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?"
	order := orderBySQL("", false, "i")
	hyd := sqlbuild.CountsHydration{}

	mock.ExpectQuery(sqlbuild.SearchIDsSQL(wispsFilterTables, where, order, "LIMIT 2")).
		WithArgs("$.session_name", "mayor").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("w-2").AddRow("w-1"))
	hydrateSQL, hydrateArgs := sqlbuild.SearchCountsSQL(wispsFilterTables, []string{"w-2", "w-1"}, "", "", "", true, hyd)
	if bad := unconstrainedCountsAggregates(hydrateSQL); len(bad) > 0 {
		t.Fatalf("phase two has unconstrained aggregates: %v", bad)
	}
	argValues := make([]driver.Value, len(hydrateArgs))
	for i, a := range hydrateArgs {
		argValues[i] = a
	}
	mock.ExpectQuery(hydrateSQL).WithArgs(argValues...).WillReturnRows(sqlmock.NewRows([]string{"id"}))

	got, err := repo.runSearchQuery(t.Context(), wispsFilterTables, where, order, "LIMIT 2", []any{"$.session_name", "mayor"}, true, hyd)
	if err != nil {
		t.Fatalf("runSearchQuery: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d rows, want none (both rows vanished between phases)", len(got))
	}

	mock.ExpectQuery(sqlbuild.SearchIDsSQL(issuesFilterTables, where, order, "")).
		WithArgs("$.session_name", "nobody").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if _, err := repo.runSearchQuery(t.Context(), issuesFilterTables, where, order, "", []any{"$.session_name", "nobody"}, false, hyd); err != nil {
		t.Fatalf("runSearchQuery (empty match): %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}

	// Past SearchByIDsMaxRows the match runs as the single-statement predicate
	// form with the caller's ORDER BY and LIMIT, not a by-IDs hydration.
	many := sqlmock.NewRows([]string{"id"})
	for i := 0; i <= sqlbuild.SearchByIDsMaxRows; i++ {
		many.AddRow(fmt.Sprintf("bd-%03d", i))
	}
	mock.ExpectQuery(sqlbuild.SearchIDsSQL(issuesFilterTables, where, order, "")).
		WithArgs("$.session_name", "everyone").WillReturnRows(many)
	single, _ := sqlbuild.SearchCountsSQL(issuesFilterTables, nil, where, order, "", true, hyd)
	mock.ExpectQuery(single).WithArgs("$.session_name", "everyone").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if _, err := repo.runSearchQuery(t.Context(), issuesFilterTables, where, order, "", []any{"$.session_name", "everyone"}, true, hyd); err != nil {
		t.Fatalf("runSearchQuery (past the bound): %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (past the bound): %v", err)
	}

	// The checker must flag the predicate form, or the assertions above and
	// in the suite test would hold vacuously.
	pred, _ := sqlbuild.SearchCountsSQL(wispsFilterTables, nil, where, order, "", true, hyd)
	if len(unconstrainedCountsAggregates(pred)) == 0 {
		t.Fatal("unconstrainedCountsAggregates found nothing in the predicate form; the checker is vacuous")
	}
}
