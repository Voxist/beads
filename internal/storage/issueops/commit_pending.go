package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SQLQuerier is the subset of *sql.Tx / *sql.DB needed by the commit helpers.
type SQLQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// HasPendingChanges checks whether there are any committable changes in the
// Dolt working set, excluding tables matched by dolt_ignore.
func HasPendingChanges(ctx context.Context, db SQLQuerier) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status s
		WHERE NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			AND s.table_name LIKE di.pattern
		)`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check status: %w", err)
	}
	return count > 0, nil
}

// HasStagedChanges reports whether the Dolt working set has any STAGED changes,
// i.e. rows that a subsequent DOLT_COMMIT('-m', …) would actually commit.
//
// This is the correct pre-commit check for selective-staging commit helpers
// (StageAndCommit, doltAddAndCommit, doltAddAndCommitInTx) that DOLT_ADD only a
// fixed/dirty-tracked set of tables. A global HasPendingChanges check is NOT
// sufficient for them: a table can be marked dirty by a write statement yet have
// no real row change (idempotent INSERT IGNORE / ON DUPLICATE KEY no-op, or an
// UPDATE matching nothing). When some OTHER table is concurrently dirty in the
// working set, HasPendingChanges is true, but staging only the (clean) target
// tables stages nothing — and DOLT_COMMIT('-m') then fails server-side with a
// "nothing to commit" warning that floods the Dolt log at reconcile cadence.
// Checking the staged set AFTER DOLT_ADD captures exactly what '-m' will commit.
func HasStagedChanges(ctx context.Context, db SQLQuerier) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE staged = 1").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check staged status: %w", err)
	}
	return count > 0, nil
}

// BuildBatchCommitMessage generates a descriptive commit message summarizing
// what changed since the last commit by querying dolt_diff against HEAD.
// It reports issue-level create/update/delete counts and lists any other
// tables (labels, comments, events, etc.) that have uncommitted changes.
func BuildBatchCommitMessage(ctx context.Context, db SQLQuerier, actor string) string {
	if actor == "" {
		actor = "bd"
	}

	var added, modified, removed int
	rows, err := db.QueryContext(ctx, `
		SELECT diff_type, COUNT(*) as cnt
		FROM dolt_diff('HEAD', 'WORKING', 'issues')
		GROUP BY diff_type
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var diffType string
			var count int
			if scanErr := rows.Scan(&diffType, &count); scanErr == nil {
				switch diffType {
				case "added":
					added = count
				case "modified":
					modified = count
				case "removed":
					removed = count
				}
			}
		}
		_ = rows.Err()
	}

	var otherTables []string
	statusRows, statusErr := db.QueryContext(ctx, `
		SELECT table_name FROM dolt_status s
		WHERE table_name != 'issues'
		AND NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			AND s.table_name LIKE di.pattern
		)`)
	if statusErr == nil {
		defer statusRows.Close()
		for statusRows.Next() {
			var table string
			if scanErr := statusRows.Scan(&table); scanErr == nil {
				otherTables = append(otherTables, table)
			}
		}
		_ = statusRows.Err()
	}

	msg := fmt.Sprintf("bd: batch commit by %s", actor)
	var parts []string
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d created", added))
	}
	if modified > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", modified))
	}
	if removed > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", removed))
	}
	if len(parts) > 0 {
		msg += " — " + strings.Join(parts, ", ")
	}
	if len(otherTables) > 0 {
		msg += fmt.Sprintf(" (+ %s)", strings.Join(otherTables, ", "))
	}
	return msg
}

// operationalOnlyColumns are issue/wisp columns that carry no semantic content:
// pure lease/liveness bookkeeping that bd heartbeat (and lease reclaim) rewrite
// on every op. A working-set change confined to these columns is a no-op that
// must not create a Dolt version commit — vp-on8s: heartbeat flooded 923
// identical-content commits (one per beat) and stalled the fleet. Keep this list
// minimal: anything not listed here is treated as semantic, so a real change is
// never silently dropped.
var operationalOnlyColumns = map[string]struct{}{
	"updated_at":       {},
	"heartbeat_at":     {},
	"lease_expires_at": {},
	"row_lock":         {},
}

// diffMetaColumns are dolt_diff() metadata pseudo-columns (named without the
// from_/to_ data prefix once stripped) that always differ between HEAD and
// WORKING and are not real table columns.
var diffMetaColumns = map[string]struct{}{
	"commit":      {},
	"commit_date": {},
}

// noOpDiffTables are the only tables a semantic no-op may touch. Heartbeat and
// lease writes route to issues or wisps and touch nothing else; a dirty row in
// any other table (events, dependencies, labels, …) is a real change.
var noOpDiffTables = map[string]struct{}{
	"issues": {},
	"wisps":  {},
}

// WorkingSetIsOperationalNoOp reports whether the Dolt working set has pending
// changes that are ENTIRELY confined to operationalOnlyColumns on issues/wisps
// rows — i.e. a lease/heartbeat write that bumped bookkeeping columns without
// changing any semantic field. It returns false (do not skip) when the working
// set is clean, when any non-issue/wisp table is dirty, when any row was added
// or removed, or when any non-operational column changed. The auto-commit path
// uses a true result to skip the Dolt version commit while keeping the SQL
// working-set write (vp-on8s).
func WorkingSetIsOperationalNoOp(ctx context.Context, db SQLQuerier) (bool, error) {
	dirty, err := dirtyTables(ctx, db)
	if err != nil {
		return false, err
	}
	// A clean working set is nothing to skip; leave it to the caller's normal
	// "nothing to commit" handling.
	if len(dirty) == 0 {
		return false, nil
	}
	for _, table := range dirty {
		if _, ok := noOpDiffTables[table]; !ok {
			// A change outside issues/wisps is always real.
			return false, nil
		}
	}
	for _, table := range dirty {
		onlyOps, err := tableDiffIsOperationalOnly(ctx, db, table)
		if err != nil {
			return false, err
		}
		if !onlyOps {
			return false, nil
		}
	}
	return true, nil
}

// dirtyTables returns the distinct set of tables with uncommitted working-set
// changes, excluding tables matched by dolt_ignore.
func dirtyTables(ctx context.Context, db SQLQuerier) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT s.table_name FROM dolt_status s
		WHERE NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			AND s.table_name LIKE di.pattern
		)`)
	if err != nil {
		return nil, fmt.Errorf("query dolt_status: %w", err)
	}
	defer rows.Close()

	var tables []string
	seen := make(map[string]struct{})
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, fmt.Errorf("scan dolt_status: %w", err)
		}
		// dolt_status can list a table twice (staged + unstaged).
		if _, dup := seen[table]; dup {
			continue
		}
		seen[table] = struct{}{}
		tables = append(tables, table)
	}
	return tables, rows.Err()
}

// tableDiffIsOperationalOnly reports whether every working-set row change in the
// given table is a modification confined to operationalOnlyColumns. An added or
// removed row, or any modified non-operational column, makes it return false.
func tableDiffIsOperationalOnly(ctx context.Context, db SQLQuerier, table string) (bool, error) {
	// table is a caller-validated constant from noOpDiffTables.
	//nolint:gosec // G201: table is a hardcoded constant, not user input.
	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		"SELECT * FROM dolt_diff('HEAD', 'WORKING', '%s')", table))
	if err != nil {
		return false, fmt.Errorf("query dolt_diff for %s: %w", table, err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return false, fmt.Errorf("diff columns for %s: %w", table, err)
	}

	for rows.Next() {
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return false, fmt.Errorf("scan diff for %s: %w", table, err)
		}

		// Track values as *string so SQL NULL (nil) is distinct from "" — a
		// content column going NULL->"" is a real change, not a no-op.
		fromVals := make(map[string]*string, len(cols))
		toVals := make(map[string]*string, len(cols))
		var diffType string
		for i, col := range cols {
			var val *string
			if vals[i] != nil {
				s := string(vals[i])
				val = &s
			}
			switch {
			case col == "diff_type":
				if val != nil {
					diffType = *val
				}
			case strings.HasPrefix(col, "from_"):
				fromVals[strings.TrimPrefix(col, "from_")] = val
			case strings.HasPrefix(col, "to_"):
				toVals[strings.TrimPrefix(col, "to_")] = val
			}
		}

		// Only in-place modifications can be operational no-ops; an added or
		// removed row is a real change.
		if diffType != "modified" {
			return false, nil
		}

		for col, toVal := range toVals {
			if _, meta := diffMetaColumns[col]; meta {
				continue
			}
			if _, op := operationalOnlyColumns[col]; op {
				continue
			}
			if !nullableStringEqual(fromVals[col], toVal) {
				return false, nil
			}
		}
	}
	return true, rows.Err()
}

// nullableStringEqual reports whether two nullable column values are equal,
// treating SQL NULL (nil) as distinct from any non-null value including "".
func nullableStringEqual(a, b *string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a == nil {
		return true
	}
	return *a == *b
}

// IsNothingToCommitError returns true if the error indicates there was nothing
// to commit (Dolt may report this even when dolt_status showed changes).
func IsNothingToCommitError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "nothing to commit") {
		return true
	}
	if strings.Contains(s, "no changes") && strings.Contains(s, "commit") {
		return true
	}
	return false
}
