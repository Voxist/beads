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

// The call-site wiring, not just the policy helper. Neither assertion can spawn
// a server: EnsureRunningDetailed gates before Start, and KillStaleServers
// returns early, so a regression of either edit fails here instead of putting
// an unmanaged sql-server on the shared port.
func TestImplicitPathsRefuseToSpawnWhenWorkspaceDisablesAutoStart(t *testing.T) {
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	config.ResetForTesting()
	beadsDir := writeWorkspace(t, "false")

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

	killed, err := KillStaleServers(beadsDir)
	if err != nil {
		t.Fatalf("KillStaleServers: %v", err)
	}
	if len(killed) != 0 {
		t.Errorf("KillStaleServers touched %v with auto-start disabled; bd does not own that server", killed)
	}
}
