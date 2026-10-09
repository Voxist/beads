// Tests for verifyProjectIdentity/verifyGlobalProjectIdentity's handling of a
// metadata READ ERROR, as opposed to a legitimate legacy database with no
// _project_id at all. The two used to be conflated (err != nil || dbID == ""
// both soft-skipped), which silently granted the connection on a failed read
// — exactly what happens in the warming window right after a Dolt server
// restart, the same window in which the server's freed port can be reused by
// a different project (GH#2372-class cross-project collision).
//
// These tests use a *DoltStore wired directly to an unused TCP port (nothing
// listening) rather than skipIfNoDolt's real test server: GetMetadata's
// underlying BeginTx fails with "connection refused" before any MySQL
// handshake, which isRetryableError already classifies as transient — so the
// call genuinely exercises withReadTx's bounded retry (serverRetryMaxElapsed)
// without needing a live Dolt server at all.
//
// The companion soft-skip case (a successful read that finds no
// _project_id at all, i.e. the actual legacy-database path this fix must
// leave alone) is already covered against a real server by
// TestCrossProject_IdentityCheck_SoftSkip_EmptyDBProjectID in
// cross_project_test.go.
package dolt

import (
	"context"
	"database/sql"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// unusedTCPPort returns a port nothing is listening on. The brief listen+close
// leaves a vanishingly small window for reuse by something else on the
// machine; the same technique is already used by circuit_test.go.
func unusedTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find an unused port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// deadStoreOnUnusedPort builds a *DoltStore whose every query fails with a
// connection-refused error — no real Dolt server involved.
func deadStoreOnUnusedPort(t *testing.T) *DoltStore {
	t.Helper()
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: unusedTCPPort(t), Database: "unreachable"}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &DoltStore{db: db}
}

func TestVerifyProjectIdentity_MetadataReadErrorFailsClosed(t *testing.T) {
	f := newIdentityTestFixture(t, "read-error")
	f.saveLocalProjectID(t, "99999999-9999-9999-9999-999999999999")
	store := deadStoreOnUnusedPort(t)

	// Bounded well under serverRetryMaxElapsed (30s): the assertion only
	// needs proof that MORE than one attempt happened, not the full budget.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err := store.verifyProjectIdentity(ctx, f.beadsDir, false)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected verifyProjectIdentity to fail closed on a metadata read error, got nil")
	}
	if strings.Contains(err.Error(), "PROJECT IDENTITY MISMATCH") {
		t.Fatalf("a read failure must not be reported as a mismatch: %v", err)
	}
	if !strings.Contains(err.Error(), "read database project id") {
		t.Fatalf("expected the read-failure wrapper, got: %v", err)
	}
	// A single attempt against a closed port fails in well under a
	// millisecond (connection refused needs no handshake). The observed
	// delay is only explained by withReadTx's bounded retry (isRetryableError
	// classifies "connection refused" as transient) having backed off and
	// re-attempted at least once before this test's own context cut it off —
	// i.e. the retry actually retries.
	if elapsed < 200*time.Millisecond {
		t.Fatalf("expected at least one retry backoff between attempts, only took %v", elapsed)
	}
}

func TestVerifyGlobalProjectIdentity_MetadataReadErrorFailsClosed(t *testing.T) {
	f := newIdentityTestFixture(t, "global-read-error")
	if err := (&configfile.Config{GlobalProjectID: "99999999-9999-9999-9999-999999999999"}).Save(f.beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
	store := deadStoreOnUnusedPort(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err := store.verifyGlobalProjectIdentity(ctx, f.beadsDir)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected verifyGlobalProjectIdentity to fail closed on a metadata read error, got nil")
	}
	if strings.Contains(err.Error(), "GLOBAL PROJECT IDENTITY MISMATCH") {
		t.Fatalf("a read failure must not be reported as a mismatch: %v", err)
	}
	if !strings.Contains(err.Error(), "read database project id") {
		t.Fatalf("expected the read-failure wrapper, got: %v", err)
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("expected at least one retry backoff between attempts, only took %v", elapsed)
	}
}
