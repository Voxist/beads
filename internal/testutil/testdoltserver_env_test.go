//go:build !windows

package testutil

import (
	"os"
	"strconv"
	"testing"
)

// TestDoltContainerStartSites_SetBeadsServerPortEnv guards against dolt test
// suites silently opening stores on an ambient shared Dolt server instead of
// their own testcontainer. applyConfigDefaults (internal/storage/dolt/store.go)
// resolves the connection port from BEADS_DOLT_SERVER_PORT first, falling
// back to the legacy BEADS_DOLT_PORT only when BEADS_DOLT_SERVER_PORT is
// unset. Both container-start sites must therefore set BEADS_DOLT_SERVER_PORT
// (not just BEADS_DOLT_PORT), or an ambiently-set BEADS_DOLT_SERVER_PORT
// (e.g. a rig's shared dev server) silently wins over the fresh
// testcontainer's port.
//
// The vp-hlfzn declaration guard adds a second invariant to the same two
// sites: a successful start must also declare the port it exported, so
// RequireDeclaredTestServer accepts the tests these helpers power.
func TestDoltContainerStartSites_SetBeadsServerPortEnv(t *testing.T) {
	if state := checkDolt(); state != doltReady {
		t.Skipf("skipping test: %s", state)
	}

	var isolatedPort string
	t.Run("StartIsolatedDoltContainer", func(t *testing.T) {
		portStr := StartIsolatedDoltContainer(t)
		isolatedPort = portStr

		got := os.Getenv("BEADS_DOLT_SERVER_PORT")
		if got != portStr {
			t.Errorf("BEADS_DOLT_SERVER_PORT = %q, want %q (the isolated container's port) — testdoltserver.go:212 must also set BEADS_DOLT_SERVER_PORT, not just BEADS_DOLT_PORT", got, portStr)
		}
		if p, _ := strconv.Atoi(portStr); !testServerPortDeclared(p) {
			t.Errorf("port %s not declared: a successful provisioned start must declare its port (vp-hlfzn), or RequireDeclaredTestServer refuses the tests this helper powers", portStr)
		}
	})

	// The declaration ends with the test that owned the server, or a dead
	// server's port stays declared.
	if p, _ := strconv.Atoi(isolatedPort); p != 0 && testServerPortDeclared(p) {
		t.Errorf("isolated port %s still declared after its test ended", isolatedPort)
	}

	t.Run("EnsureDoltContainerForTestMain", func(t *testing.T) {
		if err := EnsureDoltContainerForTestMain(); err != nil {
			t.Fatalf("EnsureDoltContainerForTestMain: %v", err)
		}
		t.Cleanup(TerminateDoltContainer)

		want := strconv.Itoa(DoltContainerPortInt())
		got := os.Getenv("BEADS_DOLT_SERVER_PORT")
		if got != want {
			t.Errorf("BEADS_DOLT_SERVER_PORT = %q, want %q (the shared container's port) — testdoltserver.go:221 must also set BEADS_DOLT_SERVER_PORT, not just BEADS_DOLT_PORT", got, want)
		}
		if !testServerPortDeclared(DoltContainerPortInt()) {
			t.Errorf("port %s not declared: a successful provisioned start must declare its port (vp-hlfzn)", want)
		}

		// A restart moves the declaration to the replacement's port, and
		// terminating the shared server ends it.
		old := DoltContainerPortInt()
		port, err := RestartSharedDoltContainer()
		if err != nil {
			t.Fatalf("RestartSharedDoltContainer: %v", err)
		}
		if !testServerPortDeclared(port) {
			t.Errorf("restarted port %d not declared", port)
		}
		if port != old && testServerPortDeclared(old) {
			t.Errorf("replaced port %d still declared after the restart", old)
		}
		TerminateDoltContainer()
		if testServerPortDeclared(port) {
			t.Errorf("port %d still declared after TerminateDoltContainer", port)
		}
	})
}
