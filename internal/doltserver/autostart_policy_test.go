package doltserver

import (
	"os"
	"path/filepath"
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
func TestWriteTimeValidationAcceptsEveryValueTheReadersHonour(t *testing.T) {
	// Every spelling either reader might plausibly be widened to, plus values
	// that must stay rejected.
	candidates := []string{
		"true", "TRUE", "True", "t", "T", "1",
		"false", "FALSE", "False", "f", "F", "0",
		"on", "ON", "off", "OFF", " false ", "\ttrue\n",
		"yes", "no", "y", "n", "disabled", "nope", "2", "",
	}

	for _, v := range candidates {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue_prefix: vc\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		honoured := isFalsyBool(v) || isTruthyBool(v)
		err := config.SetYamlConfigInDir(beadsDir, "dolt.auto-start", v)

		switch {
		case honoured && err != nil:
			t.Errorf("bd config set dolt.auto-start %q was REFUSED, but the readers honour it: %v", v, err)
		case !honoured && err == nil:
			t.Errorf("bd config set dolt.auto-start %q was ACCEPTED, but neither reader honours it", v)
		}
	}
}
