package uow

import (
	"errors"
	"strings"

	mysql "github.com/go-sql-driver/mysql"
)

// isSerializationError returns true if the error is a Dolt/MySQL serialization
// failure that guarantees the transaction was rolled back. Safe to retry.
//   - 1213 (ER_LOCK_DEADLOCK): concurrent transactions conflict at commit time
//   - 1205 (ER_LOCK_WAIT_TIMEOUT): lock wait exceeded, transaction rolled back
func isSerializationError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	return mysqlErr.Number == 1213 || mysqlErr.Number == 1205
}

// isInvalidConnectionError returns true if the error is a transient MySQL
// driver connection failure (the dolt sql-server reaped an idle pooled
// connection, the server restarted, the pipe broke, etc.).
//
// Whether retrying is SAFE depends on WHEN this fires, not just that it did:
//   - Before commit (pinning a connection, starting the tx, or a write inside
//     fn): nothing was committed, so replaying the whole sequence is safe.
//   - At/after commit: the result is AMBIGUOUS — the commit may have landed on
//     the server before the connection dropped. Callers must NOT blindly replay
//     a commit-phase failure; see RunInTx for the phase-aware handling.
func isInvalidConnectionError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	// These substrings must stay in lock-step with the direct DoltStore-layer
	// classifier (internal/storage/dolt.isRetryableError): the two retry layers
	// have to agree on what "transient connection failure" means, or the same
	// dolt-server/db-proxy restart retries on one path and FatalErrors on the
	// other. "connection reset"/"connection refused" cover the server-restart
	// window the direct path already treats as transient.
	return strings.Contains(s, "invalid connection") ||
		strings.Contains(s, "driver: bad connection") ||
		strings.Contains(s, "lost connection") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "connection refused")
}
