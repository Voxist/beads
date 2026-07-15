package db

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/steveyegge/beads/internal/storage/domain"
)

// These tests pin the no-op-commit gate (va-v1i9 / ADR-0023 L-A): an Update
// whose field values all equal the stored row must not touch the working set
// at all — no UPDATE (no updated_at bump), no event row — so the caller's
// DOLT_COMMIT finds nothing to commit and the Dolt history stays flat.
// A real field change must keep writing exactly as before; a false "clean"
// read here would silently drop writes, which is why every suppression case
// has a matching still-writes control.
func (s *testSuite) TestIssueSQLRepositoryUpdateNoOpGate() {
	s.Run("NoOpUpdateLeavesWorkingSetClean", s.noopUpdateLeavesWorkingSetClean)
	s.Run("NoOpMetadataKeyOrderInsensitive", s.noopMetadataKeyOrderInsensitive)
	s.Run("RealChangeStillWritesAndRecordsEvent", s.realChangeStillWritesAndRecordsEvent)
	s.Run("MixedNoOpAndRealChangeStillWrites", s.mixedNoOpAndRealChangeStillWrites)
	s.Run("CaseOnlyChangeStillWrites", s.caseOnlyChangeStillWrites)
	s.Run("NullableClearToNullIsNoOpWhenAlreadyNull", s.nullableClearToNullIsNoOpWhenAlreadyNull)
	s.Run("MissingIDStillErrNoRows", s.updateMissingIDStillErrNoRows)
	s.Run("NoOpUpdateOnWispSkipsEvent", s.noopUpdateOnWispSkipsEvent)
}

// checkpointDolt commits the current working set so later dirty-checks are
// attributable to the code under test alone.
func (s *testSuite) checkpointDolt() {
	_, err := s.Runner().ExecContext(s.Ctx(), "CALL DOLT_COMMIT('-Am', 'test checkpoint')")
	s.Require().NoError(err)
}

func (s *testSuite) workingSetDirtyRows() int {
	var n int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM dolt_status").Scan(&n))
	return n
}

// issueUpdatedAtRaw reads updated_at as CHAR so the assertion is byte-exact
// regardless of the DSN's parseTime setting.
func (s *testSuite) issueUpdatedAtRaw(table, id string) string {
	var ts string
	//nolint:gosec // G201: table is a test-controlled constant
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT CAST(updated_at AS CHAR) FROM "+table+" WHERE id = ?", id).Scan(&ts))
	return ts
}

func (s *testSuite) eventRows(table, id string) int {
	var n int
	//nolint:gosec // G201: table is a test-controlled constant
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM "+table+" WHERE issue_id = ?", id).Scan(&n))
	return n
}

func (s *testSuite) noopUpdateLeavesWorkingSetClean() {
	s.seedIssueRow("bd-noop-clean")
	r := NewIssueSQLRepository(s.Runner())

	fields := map[string]any{
		"status":   "in_progress",
		"assignee": "agent-a",
		"metadata": json.RawMessage(`{"gc.routed_to":"voxist-platform/voxist.executor"}`),
	}
	s.Require().NoError(r.Update(s.Ctx(), "bd-noop-clean", fields, "tester", domain.IssueTableOpts{}))
	s.checkpointDolt()
	evtBefore := s.eventRows("events", "bd-noop-clean")
	tsBefore := s.issueUpdatedAtRaw("issues", "bd-noop-clean")

	// The same values again — the shape of the storm's `bd update` no-ops.
	s.Require().NoError(r.Update(s.Ctx(), "bd-noop-clean", map[string]any{
		"status":   "in_progress",
		"assignee": "agent-a",
		"metadata": json.RawMessage(`{"gc.routed_to":"voxist-platform/voxist.executor"}`),
	}, "tester", domain.IssueTableOpts{}))

	s.Equal(0, s.workingSetDirtyRows(), "no-op update must leave the Dolt working set clean")
	s.Equal(evtBefore, s.eventRows("events", "bd-noop-clean"), "no-op update must not record an event")
	s.Equal(tsBefore, s.issueUpdatedAtRaw("issues", "bd-noop-clean"), "no-op update must not bump updated_at")
}

func (s *testSuite) noopMetadataKeyOrderInsensitive() {
	s.seedIssueRow("bd-noop-meta")
	r := NewIssueSQLRepository(s.Runner())

	s.Require().NoError(r.Update(s.Ctx(), "bd-noop-meta", map[string]any{
		"metadata": json.RawMessage(`{"a":1,"b":"two"}`),
	}, "tester", domain.IssueTableOpts{}))
	s.checkpointDolt()

	// Same JSON value, different key order and whitespace — still a no-op.
	s.Require().NoError(r.Update(s.Ctx(), "bd-noop-meta", map[string]any{
		"metadata": json.RawMessage(`{ "b": "two", "a": 1 }`),
	}, "tester", domain.IssueTableOpts{}))

	s.Equal(0, s.workingSetDirtyRows(), "semantically-equal metadata must compare as unchanged")
}

func (s *testSuite) realChangeStillWritesAndRecordsEvent() {
	s.seedIssueRow("bd-real-change")
	r := NewIssueSQLRepository(s.Runner())
	s.checkpointDolt()
	evtBefore := s.eventRows("events", "bd-real-change")

	s.Require().NoError(r.Update(s.Ctx(), "bd-real-change", map[string]any{
		"assignee": "agent-b",
	}, "tester", domain.IssueTableOpts{}))

	var assignee string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT assignee FROM issues WHERE id = ?", "bd-real-change").Scan(&assignee))
	s.Equal("agent-b", assignee)
	s.Equal(evtBefore+1, s.eventRows("events", "bd-real-change"), "real change must record an event")
	s.Positive(s.workingSetDirtyRows(), "real change must dirty the working set")
}

func (s *testSuite) mixedNoOpAndRealChangeStillWrites() {
	s.seedIssueRow("bd-mixed")
	r := NewIssueSQLRepository(s.Runner())
	s.Require().NoError(r.Update(s.Ctx(), "bd-mixed", map[string]any{
		"assignee": "agent-a", "priority": 1,
	}, "tester", domain.IssueTableOpts{}))
	s.checkpointDolt()

	// One field unchanged, one changed: must write.
	s.Require().NoError(r.Update(s.Ctx(), "bd-mixed", map[string]any{
		"assignee": "agent-a", "priority": 3,
	}, "tester", domain.IssueTableOpts{}))

	var priority int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT priority FROM issues WHERE id = ?", "bd-mixed").Scan(&priority))
	s.Equal(3, priority)
	s.Positive(s.workingSetDirtyRows())
}

// Pins that the comparison is byte/case-exact (Dolt's default binary
// collation). If this ever fails, the no-change probe is comparing under a
// case-insensitive collation and can silently drop case-only edits — that
// would be a data-loss regression, do not relax this test.
func (s *testSuite) caseOnlyChangeStillWrites() {
	s.seedIssueRow("bd-case-only")
	r := NewIssueSQLRepository(s.Runner())
	s.Require().NoError(r.Update(s.Ctx(), "bd-case-only", map[string]any{
		"assignee": "agent-a",
	}, "tester", domain.IssueTableOpts{}))
	s.checkpointDolt()

	s.Require().NoError(r.Update(s.Ctx(), "bd-case-only", map[string]any{
		"assignee": "Agent-A",
	}, "tester", domain.IssueTableOpts{}))

	var assignee string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT assignee FROM issues WHERE id = ?", "bd-case-only").Scan(&assignee))
	s.Equal("Agent-A", assignee, "case-only change must still be written")
	s.Positive(s.workingSetDirtyRows())
}

func (s *testSuite) nullableClearToNullIsNoOpWhenAlreadyNull() {
	s.seedIssueRow("bd-null-clear")
	s.checkpointDolt()
	r := NewIssueSQLRepository(s.Runner())

	// due_at is NULL on the seeded row; clearing it again is a no-op.
	s.Require().NoError(r.Update(s.Ctx(), "bd-null-clear", map[string]any{
		"due_at": nil,
	}, "tester", domain.IssueTableOpts{}))

	s.Equal(0, s.workingSetDirtyRows(), "NULL-to-NULL must compare as unchanged (NULL-safe <=>)")
}

func (s *testSuite) updateMissingIDStillErrNoRows() {
	r := NewIssueSQLRepository(s.Runner())
	err := r.Update(s.Ctx(), "bd-ghost-999", map[string]any{
		"assignee": "agent-a",
	}, "tester", domain.IssueTableOpts{})
	s.Require().Error(err)
	s.True(errors.Is(err, sql.ErrNoRows), "missing id must still surface sql.ErrNoRows, got: %v", err)
}

// Wisp tables may be excluded from Dolt versioning, so the wisp variant pins
// the row/event effects rather than working-set cleanliness.
func (s *testSuite) noopUpdateOnWispSkipsEvent() {
	s.seedWispRow("bd-noop-wisp")
	r := NewIssueSQLRepository(s.Runner())
	s.Require().NoError(r.Update(s.Ctx(), "bd-noop-wisp", map[string]any{
		"assignee": "agent-w",
	}, "tester", domain.IssueTableOpts{UseWispsTable: true}))
	evtBefore := s.eventRows("wisp_events", "bd-noop-wisp")
	tsBefore := s.issueUpdatedAtRaw("wisps", "bd-noop-wisp")

	s.Require().NoError(r.Update(s.Ctx(), "bd-noop-wisp", map[string]any{
		"assignee": "agent-w",
	}, "tester", domain.IssueTableOpts{UseWispsTable: true}))

	s.Equal(evtBefore, s.eventRows("wisp_events", "bd-noop-wisp"), "no-op wisp update must not record an event")
	s.Equal(tsBefore, s.issueUpdatedAtRaw("wisps", "bd-noop-wisp"), "no-op wisp update must not bump updated_at")
}
