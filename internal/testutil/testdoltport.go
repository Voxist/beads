package testutil

import (
	"os"
	"strconv"
	"sync"
	"testing"
)

// Test-server declaration (vp-hlfzn, vp-167w part b).
//
// A test server is something the test process DECLARES, by provisioning it
// (the shared container, an isolated container) or by naming the port of a
// server it spawned itself (DeclareTestServerPort). It is never something the
// process INFERS from an environment variable it happened to inherit.
//
// Why: bd resolves the server port env > port file > config
// (applyConfigDefaults), and every gc-managed agent session exports
// BEADS_DOLT_SERVER_PORT pointing at the LIVE multi-DB city server. The
// BEADS_TEST_MODE guard only force-fails on production-IDENTIFIED ports
// (DefaultSQLPort 3307, BEADS_PRODUCTION_PORT, a resolved dolt-server.port
// file), and the live city server's port is production HERE but not by port
// number, so a store-creating test run from an agent shell walks straight
// through every existing guard onto the live root. That is exactly how six
// fixdepkeys_* scratch databases landed there (vp-kmgu).
//
// The declaration is a port in an in-process set, not an environment
// variable. BEADS_TEST_SERVER=1 does not count: TestMains set it before they
// provision anything (it is the dolt.New database-name firewall's opt-in,
// AD-01), and a shell that once exported it would declare every inherited
// port. An in-process set cannot be inherited.

// EnvSharedDoltServer names the one Dolt port that a test harness started a
// server on for this process tree, as opposed to a port inherited from the
// surrounding environment. Its value is that port number: scripts/test.sh sets
// it to the same value as the BEADS_DOLT_PORT it exports for the dockerless
// `dolt sql-server` of its BEADS_TEST_SHARED_SERVER=1 flow. It vouches for that
// value only, never for the process: a port variable holding anything else is
// still ambient, however it got there.
//
// It is deliberately a distinct name from BEADS_TEST_SHARED_SERVER. That flag
// is also read by cmd/bd's shared-server integration test for an unrelated
// purpose, and it only says a shared server was asked for, not that one was
// started: the flow starts none when a port variable is already set, so the
// flag beside a port cannot tell the script's own port from an inherited one.
const EnvSharedDoltServer = "BEADS_TEST_SHARED_DOLT_SERVER"

// serverPortEnvs are the variables bd reads a Dolt server port from. The
// resolvers disagree on how they combine (applyConfigDefaults ignores
// BEADS_DOLT_PORT once BEADS_DOLT_SERVER_PORT is non-empty; configfile falls
// back to it when BEADS_DOLT_SERVER_PORT is not a valid port), so the guard
// checks each one on its own.
var serverPortEnvs = []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"}

// serverSocketEnv names a unix socket bd dials in preference to any TCP port
// whenever it is connectable (ResolveSocketTransport), so a declared port
// beside it does not decide where the store lands. No testutil helper serves
// a socket, so a non-empty value is never a declared server.
const serverSocketEnv = "BEADS_DOLT_SERVER_SOCKET"

// declaredPorts counts the live declarations of each port: a port the shared
// container holds can also be declared by a test that dials it.
var declaredPorts = struct {
	sync.Mutex
	n map[int]int
}{n: map[int]int{}}

// declareTestServerPort records a port this process provisioned a test server
// on. Ports that do not parse are ignored: nothing could dial them.
func declareTestServerPort(port string) {
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 {
		return
	}
	declaredPorts.Lock()
	defer declaredPorts.Unlock()
	declaredPorts.n[p]++
}

// undeclareTestServerPort drops one declaration of port.
func undeclareTestServerPort(port string) {
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 {
		return
	}
	declaredPorts.Lock()
	defer declaredPorts.Unlock()
	if declaredPorts.n[p] <= 1 {
		delete(declaredPorts.n, p)
		return
	}
	declaredPorts.n[p]--
}

func testServerPortDeclared(p int) bool {
	declaredPorts.Lock()
	defer declaredPorts.Unlock()
	return declaredPorts.n[p] > 0
}

// DeclareTestServerPort declares port, the port of a Dolt server the test
// started itself (a `dolt sql-server` under t.TempDir(), say), for the rest
// of the test. The testutil provisioning helpers declare their own ports; a
// test needs this only for a server it spawned some other way. Never pass a
// port read from the environment: that declares the inherited server the
// guard exists to refuse.
func DeclareTestServerPort(t testing.TB, port int) {
	t.Helper()
	if port <= 0 {
		t.Fatalf("DeclareTestServerPort(%d): not a port a server listens on", port)
	}
	s := strconv.Itoa(port)
	declareTestServerPort(s)
	t.Cleanup(func() { undeclareTestServerPort(s) })
}

// undeclaredServerEnv returns the first variable that names a Dolt server
// this process did not declare, or "" when there is none: a non-empty
// serverSocketEnv, or a variable in serverPortEnvs holding a port bd would
// dial (a positive integer) which this process neither declared nor had
// vouched for by EnvSharedDoltServer. A port value bd ignores ("", "0",
// non-numeric) leads nowhere and is not refused.
func undeclaredServerEnv() string {
	if os.Getenv(serverSocketEnv) != "" {
		return serverSocketEnv
	}
	for _, name := range serverPortEnvs {
		p, err := strconv.Atoi(os.Getenv(name))
		if err != nil || p <= 0 {
			continue
		}
		if testServerPortDeclared(p) || portVouchedByHarness(name) {
			continue
		}
		return name
	}
	return ""
}

// RequireDeclaredTestServer fails fast when a store-creating test could
// resolve its Dolt server from an environment variable naming a server this
// process did not declare. Call it BEFORE any store creation, next to
// RequireDoltBinary.
//
// It never fires when the environment carries no socket and no usable server
// port, nor for a port the testutil provisioning helpers or
// DeclareTestServerPort declared, nor for the harness port
// EnvSharedDoltServer vouches for.
func RequireDeclaredTestServer(t testing.TB) {
	t.Helper()
	if name := undeclaredServerEnv(); name != "" {
		t.Fatal(refusalMessage(name))
	}
}

// portVouchedByHarness reports whether the server port named by `name` is the
// harness-provisioned port vouched for by EnvSharedDoltServer. It honors
// exactly the per-variable value equality neutralizeUnvouchedDoltPorts
// honors, so outside Windows the guard and the neutralizer cannot disagree
// about which ports are ambient (the Docker-less scripts/test.sh flow). The
// Windows neutralizer honors no vouch and clears both variables, so after its
// TestMain the guard sees no port at all.
func portVouchedByHarness(name string) bool {
	vouched := os.Getenv(EnvSharedDoltServer)
	return vouched != "" && os.Getenv(name) == vouched
}

// refusalMessage builds the refusal for a variable naming an undeclared server.
// Split from the t.Fatal so tests can assert the operator guidance cannot
// silently lose a component.
func refusalMessage(name string) string {
	return "refusing store-creating test: " + name + "=\"" + os.Getenv(name) +
		"\" names a Dolt server this test process did not provision. Under a " +
		"gc-managed city that variable points at the LIVE multi-DB server, " +
		"and bd resolves env > port file > config, so proceeding would create " +
		"test databases on the live root (the vp-kmgu incident). Either point " +
		"the test at a server the testutil helpers provisioned (they declare " +
		"their own ports), call testutil.DeclareTestServerPort for a TCP " +
		"server the test started itself, or clear the variable with " +
		"t.Setenv(\"" + name + "\", \"\") if the test does not need it. A " +
		"socket is never declared. BEADS_TEST_SERVER=1 " +
		"does not declare a port. A harness-provisioned port vouched for by " +
		EnvSharedDoltServer + " is honored automatically."
}
