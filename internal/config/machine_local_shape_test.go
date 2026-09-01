package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

// TestSidecarWinsWhateverShapeTheTrackedFileUses pins the property the sidecar
// split depends on: a machine-local value must win, for every combination of
// key shapes the two files can be in.
//
// viper's lookup tries the longest joined prefix first, so a FLAT `dolt.port:`
// beats a nested `dolt: {port:}` no matter which file is merged last. Before
// the sidecar was pinned to the flat form, its shape was decided by accident of
// file state — flat on the first write into a comment-only file, nested on
// every write after — so `bd config set dolt.port 3307` could report success
// while merged viper kept returning the tracked value, and GetStringFromDir
// (sidecar-first) returned the new one. Two read paths, two answers.
func TestSidecarWinsWhateverShapeTheTrackedFileUses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tracked string
	}{
		{"tracked flat", "dolt.port: 9999\n"},
		{"tracked nested", "dolt:\n  port: 9999\n"},
		{"tracked comment-only", "# Beads config\n"},
		{"tracked absent key", "backup:\n  enabled: true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(tc.tracked), 0o600); err != nil {
				t.Fatal(err)
			}

			// Two writes: the second is where the shape used to flip to nested.
			if err := setMachineLocalYamlConfig(configPath, "dolt.port", "3307"); err != nil {
				t.Fatal(err)
			}
			if err := setMachineLocalYamlConfig(configPath, "dolt.mode", "server"); err != nil {
				t.Fatal(err)
			}

			vp := viper.New()
			vp.SetConfigType("yaml")
			vp.SetConfigFile(configPath)
			if err := vp.ReadInConfig(); err != nil {
				t.Fatal(err)
			}
			vp.SetConfigFile(LocalConfigPathFor(configPath))
			if err := vp.MergeInConfig(); err != nil {
				t.Fatal(err)
			}

			if got := vp.GetString("dolt.port"); got != "3307" {
				t.Errorf("merged viper dolt.port = %q, want 3307 (the sidecar value)", got)
			}
			if got := GetStringFromDir(dir, "dolt.port"); got != "3307" {
				t.Errorf("GetStringFromDir dolt.port = %q, want 3307", got)
			}
			// The two read paths must agree; disagreement is the bootstrap/runtime split.
			if vp.GetString("dolt.port") != GetStringFromDir(dir, "dolt.port") {
				t.Error("merged viper and GetStringFromDir disagree")
			}
		})
	}
}
