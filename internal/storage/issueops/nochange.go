package issueops

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// UpdateColumn is one column/value pair destined for an UPDATE SET clause,
// carrying the exact driver argument the UPDATE would send so the no-change
// probe and the write compare/store identical values.
type UpdateColumn struct {
	Column string
	Value  interface{}
}

// AllColumnsEqualInTx reports whether every given column already holds the
// given value on the row, using the engine's own comparison semantics via
// NULL-safe equality (<=>), so a NULL argument matches a NULL cell and a
// case-only difference under the binary collation still counts as a change.
// The metadata JSON column is compared as JSON (CAST(? AS JSON)) so key
// order and whitespace differences do not defeat the check.
//
// This is the no-op-commit gate (va-v1i9 / ADR-0023 L-A): callers skip the
// UPDATE — and with it the updated_at bump and the event row — when nothing
// would change, which keeps the Dolt working set clean and prevents the
// enclosing DOLT_COMMIT from minting a zero-delta commit.
//
// Returns sql.ErrNoRows when the row does not exist. Callers must treat any
// error as "assume changed" and fall through to the legacy write path — only
// a positive "unchanged" answer may suppress a write.
func AllColumnsEqualInTx(ctx context.Context, tx DBTX, table, id string, cols []UpdateColumn) (bool, error) {
	if len(cols) == 0 {
		return false, nil
	}
	exprs := make([]string, 0, len(cols))
	args := make([]interface{}, 0, len(cols)+1)
	for _, c := range cols {
		if c.Column == "metadata" {
			exprs = append(exprs, "(`metadata` <=> CAST(? AS JSON))")
		} else {
			exprs = append(exprs, fmt.Sprintf("(`%s` <=> ?)", c.Column))
		}
		args = append(args, c.Value)
	}
	args = append(args, id)
	//nolint:gosec // G201: table names come from WispTableRouting/pickIssueTable constants; column names from the update-field allowlists
	query := fmt.Sprintf("SELECT (%s) FROM %s WHERE id = ?", strings.Join(exprs, " AND "), table)

	// Scan into bool, not int64: the server wire protocol returns the boolean
	// expression as 1/0 (or []byte("1")), but the embedded engine's driver
	// returns a native bool — database/sql's driver.Bool converter accepts all
	// three, whereas an int64 destination errors on bool and would silently
	// disable the gate in embedded mode via the fall-through below.
	var equal bool
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&equal); err != nil {
		return false, err
	}
	return equal, nil
}

// UpdateWouldSideEffect reports whether the auto-managed update clauses
// (pinned auto-clear, ManageClosedAt, ManageStartedAt) would mutate the row
// even though every explicitly requested field compares unchanged. It gates
// the no-op early return and is evaluated together with AllColumnsEqualInTx
// returning true, which implies any requested status equals the stored value
// — so the reopen branch of ManageClosedAt (old closed, new not closed) can
// never apply on a suppressed update.
func UpdateWouldSideEffect(oldIssue *types.Issue, updates map[string]interface{}) bool {
	rawStatus, hasStatus := updates["status"]
	if !hasStatus {
		return false
	}
	var newStatus string
	switch v := rawStatus.(type) {
	case string:
		newStatus = v
	case types.Status:
		newStatus = string(v)
	default:
		// Unknown status representation: assume a side effect (fail open).
		return true
	}
	if _, alreadySet := updates["pinned"]; !alreadySet &&
		oldIssue.Pinned && newStatus != string(types.StatusPinned) {
		return true // the pinned auto-clear would flip pinned to false
	}
	if _, explicit := updates["closed_at"]; !explicit &&
		newStatus == string(types.StatusClosed) {
		// ManageClosedAt stamps closed_at = now on EVERY implicit close,
		// even when the row is already closed — never suppress that.
		return true
	}
	if _, explicit := updates["started_at"]; !explicit &&
		newStatus == string(types.StatusInProgress) && oldIssue.StartedAt == nil {
		return true // ManageStartedAt would stamp the missing started_at
	}
	return false
}
