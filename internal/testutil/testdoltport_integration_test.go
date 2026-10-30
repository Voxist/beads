//go:build cgo

package testutil_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// TestDeclaredOptInRunsStoreCreatingTestOnEphemeralServer is the AC2 negative
// control and AC3 demonstration for the vp-hlfzn declaration guard (vp-167w
// part b): with BEADS_TEST_SERVER=1 explicitly set, a store-creating test
// runs NORMALLY against a throwaway server — a guard that blocks everything
// is as useless as one that blocks nothing.
//
// The throwaway server is a real `dolt sql-server` spawned under t.TempDir()
// on an ephemeral port, never the live city server on :48770 — the test
// points BEADS_DOLT_SERVER_PORT at the ephemeral port, which is exactly the
// inherited-env shape the guard polices, so the pass here can only come from
// the declaration.
func TestDeclaredOptInRunsStoreCreatingTestOnEphemeralServer(t *testing.T) {
	testutil.RequireDoltBinary(t)
	testutil.RequireDoltCLIOnly(t)

	serverRoot := filepath.Join(t.TempDir(), "server-root")
	if err := os.MkdirAll(serverRoot, 0o755); err != nil {
		t.Fatalf("create server root: %v", err)
	}
	doltInit := exec.Command("dolt", "init", "--name", "test", "--email", "test@example.com")
	doltInit.Dir = serverRoot
	if out, err := doltInit.CombinedOutput(); err != nil {
		t.Fatalf("dolt init: %v\n%s", err, out)
	}

	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	server := exec.Command("dolt", "sql-server",
		"-H", "127.0.0.1",
		"-P", strconv.Itoa(port),
		"--loglevel=ERROR",
	)
	server.Dir = serverRoot
	if err := server.Start(); err != nil {
		t.Fatalf("start ephemeral dolt sql-server: %v", err)
	}
	t.Cleanup(func() {
		_ = server.Process.Kill()
		_, _ = server.Process.Wait()
	})
	if !testutil.WaitForServer(port, 60*time.Second) {
		t.Fatalf("ephemeral dolt sql-server on 127.0.0.1:%d never accepted connections", port)
	}

	// The exact shape the guard polices: an env-resolved server port. Only
	// the explicit BEADS_TEST_SERVER=1 declaration makes this a test server.
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
	t.Setenv("BEADS_TEST_SERVER", "1")
	testutil.RequireDeclaredTestServer(t)

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create .beads: %v", err)
	}

	// Unique database name: the ephemeral server may outlive this process by
	// a shutdown race; the name avoids clashes and the deferred DROP removes
	// the DB from that server's root.
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	dbName := "testdb_guard_" + hex.EncodeToString(buf)

	cfg := &configfile.Config{
		Database: dbName,
		Backend:  configfile.BackendDolt,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		Database:        dbName,
		CreateIfMissing: true,
		MaxOpenConns:    1,
	})
	if err != nil {
		t.Fatalf("dolt.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	defer func() {
		if _, err := store.UnderlyingDB().ExecContext(ctx, "DROP DATABASE `"+dbName+"`"); err != nil {
			t.Logf("drop scratch database %s: %v", dbName, err)
		}
	}()

	if err := store.SetConfig(ctx, "issue_prefix", "tst"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}

	issue := &types.Issue{
		ID:        "tst-1",
		Title:     "declaration guard negative control",
		Priority:  2,
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	got, err := store.GetIssue(ctx, "tst-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got == nil || got.Title != issue.Title {
		t.Fatalf("round-trip mismatch: got %+v, want title %q", got, issue.Title)
	}
}
