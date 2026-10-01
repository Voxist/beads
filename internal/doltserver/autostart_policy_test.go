package doltserver

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// writeWorkspace lays down the Gas City shape: server mode plus a flat dotted
// `dolt.auto-start` in config.yaml.
func writeWorkspace(t *testing.T, autoStart string) string {
	t.Helper()
	// Neutralise EVERY variable that steers server-mode resolution, not just
	// BEADS_DOLT_AUTO_START. On a Gas City machine these are exported, and
	// leaving them set makes resolveServerDir return the REAL
	// ~/.beads/shared-server: EnsureRunningDetailed would then adopt the live
	// managed server and EnsurePortFile would WRITE the operator's shared-server
	// port file from a unit test. A test that can touch fleet state is not a
	// unit test.
	for _, k := range []string{
		"BEADS_DOLT_SHARED_SERVER",
		"BEADS_DOLT_SERVER_MODE",
		"BEADS_DOLT_SERVER_HOST",
		"BEADS_DOLT_SERVER_PORT",
		"BEADS_DIR",
	} {
		t.Setenv(k, "")
	}
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "issue_prefix: vc\ndolt.auto-start: " + autoStart + "\ndolt:\n  disable-event-flush: true\ndolt.mode: server\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := `{"database":"dolt","backend":"dolt","dolt_mode":"server","dolt_database":"hq","project_id":"p"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

// The regression: with no BEADS_DOLT_AUTO_START in the environment and global
// viper uninitialised — every library consumer, and any bd path that resolves a
// server before config.Initialize — the workspace's own config.yaml must still
// be able to forbid spawning a server. Before ga-rpgvw only the env var was
// honoured here, so bd started an UNMANAGED sql-server on the Gas City shared
// port and blocked the managed server's restart.
func TestIsAutoStartDisabledForHonoursWorkspaceConfigWithoutGlobalInit(t *testing.T) {
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	// Hermetic precondition: the point of this test is the path where global
	// viper was never initialised. Another test in this binary calling
	// config.Initialize would leak a bound config and flip the "enabled" case
	// below into a false pass.
	config.ResetForTesting()
	if got := config.GetString("dolt.auto-start"); got != "" {
		t.Fatalf("precondition: global config must be unbound, got %q", got)
	}

	disabled := writeWorkspace(t, "false")
	if !IsAutoStartDisabledFor(disabled) {
		t.Error("IsAutoStartDisabledFor = false for a workspace whose config.yaml says dolt.auto-start: false")
	}

	enabled := writeWorkspace(t, "true")
	if IsAutoStartDisabledFor(enabled) {
		t.Error("IsAutoStartDisabledFor = true for a workspace that enables auto-start")
	}

	// No workspace to consult: unchanged behaviour, env/global only.
	if IsAutoStartDisabledFor("") {
		t.Error("IsAutoStartDisabledFor(\"\") must not claim disabled with nothing to read")
	}
}

// The three sources are a DISJUNCTION: any one of them may disable auto-start
// and none re-enables it over another. BEADS_DOLT_AUTO_START=1 therefore does
// not resurrect auto-start for a workspace whose config says false -- it fails
// closed, which is what a shared store wants.
func TestIsAutoStartDisabledForEnvIsNotAnOverrideBackToEnabled(t *testing.T) {
	config.ResetForTesting()
	disabled := writeWorkspace(t, "false")
	t.Setenv("BEADS_DOLT_AUTO_START", "1")
	if !IsAutoStartDisabledFor(disabled) {
		t.Error("BEADS_DOLT_AUTO_START=1 must NOT re-enable auto-start against a workspace config that disables it")
	}
}

// The env var disables in the other direction, and still applies for a caller
// that passes no directory.
func TestIsAutoStartDisabledForEnvDisables(t *testing.T) {
	// Same hermetic precondition as its siblings: without it, a config.Initialize
	// elsewhere in this binary (doltserver_test.go's TestIsAutoStartDisabled_Sources
	// does exactly that, with no cleanup) leaks a bound viper and turns the
	// "stays enabled" assertion below into a pass that proves nothing. Today it
	// survives only because Go compiles test files in filename order.
	config.ResetForTesting()
	enabled := writeWorkspace(t, "true")

	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	if !IsAutoStartDisabledFor(enabled) {
		t.Error("BEADS_DOLT_AUTO_START=0 must disable auto-start even where config enables it")
	}
	if !IsAutoStartDisabled() {
		t.Error("the dirless form must keep honouring the env var")
	}

	t.Setenv("BEADS_DOLT_AUTO_START", "1")
	if IsAutoStartDisabledFor(enabled) {
		t.Error("BEADS_DOLT_AUTO_START=1 with config true must leave auto-start enabled")
	}
}

// The call-site wiring, not just the policy helper.
//
// This test CAN spawn a server -- that is precisely its failure mode, and the
// earlier comment here claiming otherwise was true only while the fix is
// present, which is the one case the test does not exist to cover. The cleanup
// below kills anything that starts, so a regression fails loudly instead of
// leaking an orphan dolt onto the host (or CI) per run.
func TestImplicitPathsRefuseToSpawnWhenWorkspaceDisablesAutoStart(t *testing.T) {
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	config.ResetForTesting()
	beadsDir := writeWorkspace(t, "false")

	// The failure mode of this test is a REAL dolt sql-server: when the gate is
	// missing, EnsureRunningDetailed reaches Start and the child reparents to
	// PID 1 and outlives the test binary, still listening. On a host running the
	// fleet that orphan IS the bug under test (ga-rpgvw). So the teardown is
	// registered BEFORE the call, and it runs whether the call refuses, returns
	// or panics.
	serverDir := resolveServerDir(beadsDir)
	t.Cleanup(func() {
		if state, err := IsRunning(serverDir); err == nil && state != nil && state.Running {
			t.Errorf("a server was started at %s (pid %d, port %d) despite auto-start being disabled; killing it", serverDir, state.PID, state.Port)
			if stopErr := StopWithForce(serverDir, true); stopErr != nil {
				t.Errorf("FAILED TO KILL the leaked server (pid %d): %v -- kill it by hand", state.PID, stopErr)
			}
		}
	})

	port, startedByUs, err := EnsureRunningDetailed(beadsDir)
	if err == nil {
		t.Fatalf("EnsureRunningDetailed started or adopted a server (port=%d startedByUs=%v); it must refuse", port, startedByUs)
	}
	if startedByUs {
		t.Error("EnsureRunningDetailed reported it started a server despite the workspace disabling auto-start")
	}
	if !strings.Contains(err.Error(), "auto-start is disabled") {
		t.Errorf("refusal must name the policy; got: %v", err)
	}

	// NOT asserted here: KillStaleServers. With this fixture it returns an
	// empty slice whether or not the gate is present -- the workspace has no
	// dolt-server.pid, so killStaleServersForDir returns early at canonicalPID
	// == 0 and the kill loop is unreachable. Asserting len(killed) == 0 would
	// pass with the gate deleted, i.e. prove nothing. Making it real needs a PID
	// file naming a live dolt process, which a unit test must not arrange on a
	// host running the fleet's server.
}

// TestStartRefusesWhenItsOwnDirectoryDisablesAutoStart pins the ga-dpbbw
// funnel: Start(beadsDir) is the ONLY function that spawns a bd-MANAGED,
// non-proxied dolt sql-server (the proxied backend spawns its own through a
// separate path -- ga-kcebr), and it must refuse on its own -- before doing
// anything else -- when beadsDir's own auto-start policy says no. Before
// this fix Start had four call sites and the check was hand-applied at only
// TWO of them (EnsureRunningDetailed, applyServer); bd init's shared-global-
// database block had no check at all (a real gap, not a design choice), and
// bd dolt start deliberately had none by design (it is the explicit
// override). A caller that forgot the check, or a new fifth call site,
// reached a fully ungated Start. Routing the check through Start itself
// means there is no route to a spawned server that does not pass through
// it.
//
// The refusal must arrive as ErrAutoStartDisabled (errors.Is) and must arrive
// before the dolt binary is even looked up, so this test needs no dolt on
// PATH. The cleanup is still registered unconditionally, matching the
// sibling tests in this file: it costs nothing when the gate holds (its own
// point), and it is the difference between a clean failure and a leaked dolt
// process the next time this test regresses.
//
// See TestStartExplicit_BypassesAutoStartGate
// (internal/doltserver/lifecycle_integration_test.go, `integration &&
// !windows`) for the other half of this pin: the explicit bypass must still
// spawn a real server. That half needs a real dolt sql-server on every green
// run, unlike this one, so it lives in the integration tier rather than here.
func TestStartRefusesWhenItsOwnDirectoryDisablesAutoStart(t *testing.T) {
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	config.ResetForTesting()
	beadsDir := writeWorkspace(t, "false")
	serverDir := resolveServerDir(beadsDir)

	t.Cleanup(func() {
		if state, err := IsRunning(serverDir); err == nil && state != nil && state.Running {
			t.Errorf("a server was started at %s (pid %d, port %d) despite auto-start being disabled; killing it", serverDir, state.PID, state.Port)
			if stopErr := StopWithForce(serverDir, true); stopErr != nil {
				t.Errorf("FAILED TO KILL the leaked server (pid %d): %v -- kill it by hand", state.PID, stopErr)
			}
		}
	})

	state, err := Start(beadsDir)
	if err == nil {
		t.Fatalf("Start succeeded (state=%+v) despite beadsDir's own config disabling auto-start", state)
	}
	if !errors.Is(err, ErrAutoStartDisabled) {
		t.Errorf("Start error does not wrap ErrAutoStartDisabled: %v", err)
	}
	if _, statErr := os.Stat(pidPath(serverDir)); !os.IsNotExist(statErr) {
		t.Errorf("Start wrote server state despite refusing to start (pid file stat err: %v)", statErr)
	}
}

// TestStartRefusesForSharedServerDirWithItsOwnConfig proves the shared-server
// half of the same gate is not just theoretical. SharedServerDir() resolves
// to ~/.beads/shared-server, and nothing in bd today WRITES a config.yaml
// there (see ErrAutoStartDisabled's doc and the CHANGELOG entry for
// ga-dpbbw) -- an operator has to place one by hand for this path to ever
// matter in practice. This test proves the mechanism works once one exists,
// with BEADS_SHARED_SERVER_DIR pointed at a throwaway directory rather than
// leaving the claim undemonstrated.
func TestStartRefusesForSharedServerDirWithItsOwnConfig(t *testing.T) {
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	config.ResetForTesting()
	sharedDir := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
	if err := os.WriteFile(filepath.Join(sharedDir, "config.yaml"), []byte("dolt.auto-start: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if state, err := IsRunning(sharedDir); err == nil && state != nil && state.Running {
			t.Errorf("a server was started at %s (pid %d, port %d) despite the shared-server config disabling auto-start; killing it", sharedDir, state.PID, state.Port)
			if stopErr := StopWithForce(sharedDir, true); stopErr != nil {
				t.Errorf("FAILED TO KILL the leaked server (pid %d): %v -- kill it by hand", state.PID, stopErr)
			}
		}
	})

	state, err := Start(sharedDir)
	if err == nil {
		t.Fatalf("Start succeeded (state=%+v) despite the shared-server directory's own config disabling auto-start", state)
	}
	if !errors.Is(err, ErrAutoStartDisabled) {
		t.Errorf("Start error does not wrap ErrAutoStartDisabled: %v", err)
	}

	// Control, so this test cannot pass vacuously: prove the refusal above
	// came from sharedDir's config.yaml specifically, not from some ambient
	// env var or leaked global viper state that would have disabled
	// auto-start regardless of that file's content. Rewriting the SAME file
	// under the SAME env to a value that does NOT disable auto-start must
	// flip the policy read back to "enabled" -- if it didn't, the refusal
	// above would have been caused by something else, and this test would
	// have "passed" without ever exercising the shared-server config.yaml
	// path at all. Checked via IsAutoStartDisabledFor directly rather than
	// a second Start call, so the control never spawns a process itself.
	if err := os.WriteFile(filepath.Join(sharedDir, "config.yaml"), []byte("dolt.auto-start: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if IsAutoStartDisabledFor(sharedDir) {
		t.Fatal("control failed: IsAutoStartDisabledFor(sharedDir) is still true after rewriting config.yaml to " +
			"dolt.auto-start: true -- the earlier refusal was not actually caused by this file's content " +
			"(ambient env or global config may be disabling auto-start on its own), so this test proves nothing")
	}
}

// A value that is neither truthy nor falsy (`dolt.auto-start: disabled`) used
// to fail open in silence. It still fails open — refusing every command over a
// typo would be its own outage — but the operator is told once.
func TestUnparseableAutoStartValueFailsOpenLoudly(t *testing.T) {
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	config.ResetForTesting()

	for _, v := range []string{"disabled", "no-thanks", "0.5"} {
		if !unparseableAutoStart(v) {
			t.Errorf("unparseableAutoStart(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "true", "false", "off", "on", " 1 ", "0"} {
		if unparseableAutoStart(v) {
			t.Errorf("unparseableAutoStart(%q) = true; recognised values must not warn", v)
		}
	}

	// The policy still permits auto-start for the typo, and the workspace that
	// spells it correctly is still honoured.
	if IsAutoStartDisabledFor(writeWorkspace(t, "disabled")) {
		t.Error("an unparseable value must not silently disable auto-start either; it fails open")
	}
	if !IsAutoStartDisabledFor(writeWorkspace(t, "false")) {
		t.Error("a correctly spelled false must still disable auto-start")
	}
}

// unparseableAutoStart must stay the exact complement of the two recognisers
// for every token either of them accepts, so widening one cannot leave the
// warning firing on a value bd understands.
func TestUnparseableAutoStartIsTheComplementOfTheRecognisers(t *testing.T) {
	for _, v := range []string{"true", "false", "TRUE", "False", "1", "0", "t", "f", "on", "OFF", " true ", "off"} {
		recognised := isFalsyBool(v) || isTruthyBool(v)
		if !recognised {
			t.Errorf("%q is accepted by neither recogniser; the table or the parsers drifted", v)
		}
		if unparseableAutoStart(v) {
			t.Errorf("unparseableAutoStart(%q) = true for a recognised value", v)
		}
	}
}

// The write-time validator must accept exactly what the READERS honour.
//
// This assertion lives here, not in internal/config, on purpose. The validator
// duplicates the vocabulary (as config.isBoolLikeConfigValue) because
// internal/doltserver imports internal/config and the dependency cannot run
// backwards — so a parity test inside config can only compare the validator to a
// hardcoded table, and would still pass if someone later widened isFalsyBool or
// isTruthyBool here. The CHANGELOG names `no` as exactly that candidate, given
// bd doctor's isValidBoolString already calls it a valid boolean. From this
// package the real functions are in scope, so widening a reader without widening
// the writer fails the build's tests instead of silently refusing, at write
// time, a value bd honours at read time.
//
// The action goes through config.SetYamlConfigInDir, NOT the config.SetYamlConfig
// that `bd config set` itself calls: that one resolves its target through
// findProjectConfigYaml, which walks up from the working directory and would
// rewrite the beads repo's own .beads/config.yaml during a test run. All three
// writers call validateYamlConfigValue as their first statement, so this
// exercises the same gate on a directory the test owns.
func TestWriteTimeValidationAcceptsEveryValueTheReadersHonour(t *testing.T) {
	// Every spelling either reader might plausibly be widened to, plus values
	// that must stay rejected.
	candidates := []string{
		"true", "TRUE", "True", "t", "T", "1",
		"false", "FALSE", "False", "f", "F", "0",
		"on", "ON", "off", "OFF", " false ", "\ttrue\n",
		"yes", "no", "y", "n", "disabled", "nope", "2", "",
	}

	var sawHonoured, sawRejected int
	for _, v := range candidates {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue_prefix: vc\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		honoured := isFalsyBool(v) || isTruthyBool(v)
		err := config.SetYamlConfigInDir(beadsDir, "dolt.auto-start", v)

		if honoured {
			sawHonoured++
		} else {
			sawRejected++
		}

		switch {
		case honoured && err != nil:
			t.Errorf("the write-time validator (shared by bd config set) REFUSED dolt.auto-start=%q, but the readers honour it: %v", v, err)
		case !honoured && err == nil:
			t.Errorf("the write-time validator (shared by bd config set) ACCEPTED dolt.auto-start=%q, but neither reader honours it", v)
		}
	}

	// Non-vacuity asserted, not inferred. candidates is a literal today, so an
	// empty iteration cannot happen -- but this whole PR exists because an
	// assertion's teeth depended on where it lived, and a later refactor to a
	// generated list would make both branches silently unreachable.
	if sawHonoured == 0 || sawRejected == 0 {
		t.Fatalf("the candidate list must exercise BOTH verdicts: honoured=%d rejected=%d", sawHonoured, sawRejected)
	}
}

// TestKillStaleServersHonoursItsOwnDirectoryAutoStart pins the orphan-cleanup
// guard in killStaleServersForDir to the directory-aware policy check. With a
// workspace that disables auto-start in its own config.yaml, an unset env var
// and an empty global viper (the library-consumer shape), the server is
// externally managed and nothing may be reaped. upstream's IsAutoStartDisabled()
// cannot see that config, so the guard falls through and the same-repo
// non-canonical process below would be killed.
func TestKillStaleServersHonoursItsOwnDirectoryAutoStart(t *testing.T) {
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	config.ResetForTesting()
	beadsDir := writeWorkspace(t, "false")
	serverDir := resolveServerDir(beadsDir)
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalPID, sameRepoPID := 111, 222
	if err := os.WriteFile(pidPath(serverDir), []byte(strconv.Itoa(canonicalPID)), 0o600); err != nil {
		t.Fatal(err)
	}

	var killed []int
	got, err := killStaleServersForDir(
		beadsDir,
		[]int{canonicalPID, sameRepoPID},
		func(int, string) bool { return true },
		func(pid int) error {
			killed = append(killed, pid)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("killStaleServersForDir error: %v", err)
	}
	if len(got) != 0 || len(killed) != 0 {
		t.Fatalf("killed %v (callback %v) in a directory whose own config disables auto-start", got, killed)
	}
}

// TestEnsureRunningNeverSpawnsInSharedMode pins the invariant that keeps the
// shared-server directory's own policy out of EnsureRunningDetailed's reach.
// Since the upstream lifecycle-lock merge it spawns through startLocked, not
// Start, so Start's check of serverDir no longer runs there; it checks only
// beadsDir. That is sound only because serverDir differs from beadsDir solely
// in shared mode, and ResolveServerMode returns External in shared mode, which
// refuses before startLocked. The shared dir's config PERMITS auto-start here,
// so the refusal cannot be coming from that file: if shared mode ever stops
// resolving External, this fails and the serverDir gate has to come back.
func TestEnsureRunningNeverSpawnsInSharedMode(t *testing.T) {
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	config.ResetForTesting()
	beadsDir := writeWorkspace(t, "true")
	sharedDir := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	if err := os.WriteFile(filepath.Join(sharedDir, "config.yaml"), []byte("dolt.auto-start: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveServerDir(beadsDir); got != sharedDir {
		t.Fatalf("resolveServerDir(beadsDir) = %q, want the shared-server dir %q; the fixture is not in shared mode", got, sharedDir)
	}
	if IsAutoStartDisabledFor(beadsDir) || IsAutoStartDisabledFor(sharedDir) {
		t.Fatal("fixture: both directories must permit auto-start, or the refusal below could come from policy")
	}

	t.Cleanup(func() {
		if state, err := IsRunning(sharedDir); err == nil && state != nil && state.Running {
			t.Errorf("a server was started at %s (pid %d, port %d) in shared mode; killing it", sharedDir, state.PID, state.Port)
			if stopErr := StopWithForce(sharedDir, true); stopErr != nil {
				t.Errorf("FAILED TO KILL the leaked server (pid %d): %v -- kill it by hand", state.PID, stopErr)
			}
		}
	})

	port, startedByUs, err := EnsureRunningDetailed(beadsDir)
	if err == nil || startedByUs {
		t.Fatalf("EnsureRunningDetailed in shared mode returned port=%d startedByUs=%v err=%v; it must refuse to spawn", port, startedByUs, err)
	}
	if _, statErr := os.Stat(pidPath(sharedDir)); !os.IsNotExist(statErr) {
		t.Errorf("EnsureRunningDetailed wrote server state in shared mode (pid file stat err: %v)", statErr)
	}
}
