package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

// TestResetThroughServerHonorsTheWorkspacesOwnAutoStart pins the fork's
// change to externalServerDeclaration: reset-data asks the workspace whose
// server it would restart (IsAutoStartDisabledFor(serverDir)), not upstream's
// global IsAutoStartDisabled(), which reads only the environment and the
// process's active config and so misses a workspace's own
// `dolt.auto-start: false` (fork #56/#57). With the global check, bd would
// restart a server the workspace declared managed outside bd.
//
// The control subtest is the same workspace without the setting: bd restarts
// its server, so the decline is the config.yaml line's doing and not the
// environment the test runs in.
func TestResetThroughServerHonorsTheWorkspacesOwnAutoStart(t *testing.T) {
	clearServerOwnershipEnv(t)
	oldRestart := restartDoltServer
	t.Cleanup(func() { restartDoltServer = oldRestart })

	const want = "auto-start is off (dolt.auto-start or BEADS_DOLT_AUTO_START)"
	for _, tc := range []struct {
		name       string
		configYAML string
		declined   bool
	}{
		{name: "config.yaml sets dolt.auto-start false", configYAML: "dolt.auto-start: false\n", declined: true},
		{name: "control: config.yaml leaves auto-start alone", configYAML: "issue-prefix: fork\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tc.configYAML), 0o600); err != nil {
				t.Fatal(err)
			}
			var restarted []string
			restartDoltServer = func(serverDir string) (*doltserver.State, error) {
				restarted = append(restarted, serverDir)
				return &doltserver.State{Running: true}, nil
			}

			if got := externalServerDeclaration(dir); (got == want) != tc.declined || (!tc.declined && got != "") {
				t.Errorf("externalServerDeclaration(%s) = %q, want declined=%t (%q)", dir, got, tc.declined, want)
			}

			refresh := refreshServerAfterReset(&managedResetDataServerStore{dir: dir}, "origin")
			if tc.declined {
				if len(restarted) != 0 || refresh.restarted {
					t.Fatalf("bd restarted the server in %v although the workspace's config.yaml turns auto-start off", restarted)
				}
				if refresh.declared != want || refresh.required == "" {
					t.Errorf("refresh declared %q, required %q; want the auto-start declaration and a restart request", refresh.declared, refresh.required)
				}
				return
			}
			if len(restarted) != 1 || restarted[0] != dir || !refresh.restarted {
				t.Fatalf("bd restarted the servers in %v (restarted=%t), want one restart of the server in %s", restarted, refresh.restarted, dir)
			}
		})
	}
}
