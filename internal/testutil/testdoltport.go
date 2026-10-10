package testutil

import (
	"os"
	"testing"
)

// Test-server declaration (vp-hlfzn, vp-167w part b).
//
// A test server is something the test process DECLARES — by provisioning one
// (the shared container, an isolated container, a spawned `dolt sql-server`)
// or by setting BEADS_TEST_SERVER=1 — never something the process INFERS from
// an environment variable it happened to inherit.
//
// Why: bd resolves the server port env > port file > config
// (applyConfigDefaults), and every gc-managed agent session exports
// BEADS_DOLT_SERVER_PORT pointing at the LIVE multi-DB city server. The
// BEADS_TEST_MODE guard only force-fails on production-IDENTIFIED ports
// (DefaultSQLPort 3307, BEADS_PRODUCTION_PORT, a resolved dolt-server.port
// file), and the live city server's port is production HERE but not by port
// number — so a store-creating test run from an agent shell walks straight
// through every existing guard onto the live root. That is exactly how six
// fixdepkeys_* scratch databases landed there (vp-kmgu).
//
// RequireDeclaredTestServer refuses to run a store-creating test while a
// Dolt server port is present in the process environment WITHOUT the
// BEADS_TEST_SERVER=1 declaration — the same opt-in the dolt.New
// database-name firewall (AD-01, be-c5p) and the production-port heuristics
// already honor. The refusal names the offending variable and both ways out:
// declare a real test server, or clear the variable (t.Setenv(name, "")) if
// the test does not need a server at all.

// inheritedServerPortEnv returns the name of the environment variable that
// would resolve a Dolt server port from the ambient environment
// (BEADS_DOLT_SERVER_PORT, or its legacy fallback BEADS_DOLT_PORT), or ""
// when neither is set. Pure: reads only the process environment.
func inheritedServerPortEnv() string {
	for _, name := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"} {
		if os.Getenv(name) != "" {
			return name
		}
	}
	return ""
}

// testServerDeclared reports whether the process has declared that the
// Dolt server it may reach is a test server (BEADS_TEST_SERVER=1).
func testServerDeclared() bool {
	return os.Getenv("BEADS_TEST_SERVER") == "1"
}

// RequireDeclaredTestServer fails fast when a store-creating test would
// resolve its Dolt server port from an environment-inherited
// BEADS_DOLT_SERVER_PORT / BEADS_DOLT_PORT without a BEADS_TEST_SERVER=1
// declaration. Call it BEFORE any store creation, next to RequireDoltBinary.
//
// It never fires when the environment carries no server port, and never
// fires when the port was provisioned by this test process — the testutil
// provisioning helpers (the shared container, StartIsolatedDoltContainer)
// declare BEADS_TEST_SERVER themselves on success, and suites whose TestMain
// provisions a server set the declaration too.
func RequireDeclaredTestServer(t *testing.T) {
	t.Helper()
	name := inheritedServerPortEnv()
	if name == "" {
		return
	}
	if testServerDeclared() || portVouchedByHarness(name) {
		return
	}
	t.Fatal(refusalMessage(name))
}

// portVouchedByHarness reports whether the server port named by `name` is the
// harness-provisioned port vouched for by EnvSharedDoltServer. It honors
// exactly the per-variable value equality neutralizeUnvouchedDoltPorts
// honors, so the guard and the neutralizer can never disagree about which
// ports are ambient (the Docker-less scripts/test.sh flow).
func portVouchedByHarness(name string) bool {
	vouched := os.Getenv(EnvSharedDoltServer)
	return vouched != "" && os.Getenv(name) == vouched
}

// refusalMessage builds the refusal for an inherited server-port variable.
// Split from the t.Fatalf so tests can assert the operator guidance cannot
// silently lose a component.
func refusalMessage(name string) string {
	return "refusing store-creating test: " + name + "=\"" + os.Getenv(name) +
		"\" was inherited from the environment and no test server is declared " +
		"for it. Under a gc-managed city that variable points at the LIVE " +
		"multi-DB server, and bd resolves env > port file > config — " +
		"proceeding would create test databases on the live root (the " +
		"vp-kmgu incident). Either point the test at a server this process " +
		"provisioned (the testutil helpers declare BEADS_TEST_SERVER=1 " +
		"themselves), or set BEADS_TEST_SERVER=1 explicitly, or clear the " +
		"variable with t.Setenv(\"" + name + "\", \"\") if the test needs no " +
		"server. A harness-provisioned port vouched for by " +
		EnvSharedDoltServer + " is honored automatically."
}
