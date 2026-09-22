package doltserver

import (
	"os"
	"path/filepath"
	"testing"
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

// The env var keeps its override in both directions, and still wins for a
// caller that passes no directory.
func TestIsAutoStartDisabledForEnvOverride(t *testing.T) {
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
