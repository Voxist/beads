package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// sampleValueFor returns a value that passes validateYamlConfigValue for key.
func sampleValueFor(key string) string {
	switch key {
	case "dolt.mode":
		return "server"
	case "dolt.port":
		return "3306"
	case "dolt.host":
		return "100.64.0.1"
	case "dolt.socket":
		return "/tmp/mysql.sock"
	case "dolt.user":
		return "bd"
	case "dolt.data-dir":
		return "/var/lib/bd"
	case "dolt.shared-server", "dolt.debug", "backup.enabled":
		return "true"
	case "backup.interval":
		return "30m"
	default:
		return "x"
	}
}

// trackedConfigFixture is a config.yaml whose content is project contract:
// every key in it is shared, and none is machine-local.
const trackedConfigFixture = `# Project contract.
issue_prefix: vp
dolt.auto-start: false          # shared: fleet policy
export.auto: false
types.custom: molecule,convoy
dolt:
  disable-event-flush: true
`

func newWorkspace(t *testing.T, configContent string) (beadsDir, configPath, localPath string) {
	t.Helper()
	beadsDir = filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	configPath = filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	return beadsDir, configPath, filepath.Join(beadsDir, LocalConfigFileName)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestMachineLocalKeysNeverReachTrackedConfig is the CLASS GUARD.
//
// It asserts the property the whole change exists to establish — no key in the
// registry is ever written to the git-tracked config.yaml — over the registry
// itself rather than over a hand-listed sample, so a key added to
// MachineLocalKeys later is covered the moment it is added.
func TestMachineLocalKeysNeverReachTrackedConfig(t *testing.T) {
	// Both public writers are covered. They resolve the target workspace
	// differently — SetYamlConfig discovers it, SetYamlConfigInDir is handed
	// it — and `bd config set`, the command that produced the reported
	// defect, goes through the discovering one.
	// The ROUTING writers, not the literal ones. Since the fork adopted
	// upstream's design (b), SetYamlConfig/SetYamlConfigInDir write exactly
	// where they are told -- that literalness is what keeps upstream #6574's
	// dotted-key round-trip suite honest -- and the routing lives at the
	// callers (`bd config set`, `bd dolt set --update-config`,
	// `bd init --debug`). The invariant this test protects is unchanged:
	// a machine-local key must never dirty the tracked config.yaml. What
	// changed is which function is responsible for honouring it.
	//
	// cmd/bd's TestNoLiteralWriterWritesAMachineLocalKey is the other half:
	// it fails the build if any caller hands a machine-local key to a literal
	// writer, which is the mistake this test can no longer catch.
	writers := map[string]func(t *testing.T, beadsDir, key, value string) error{
		"SetMachineLocalYamlConfigInDir": func(_ *testing.T, beadsDir, key, value string) error {
			return SetMachineLocalYamlConfigInDir(beadsDir, key, value)
		},
		"SetMachineLocalYamlConfig": func(t *testing.T, beadsDir, key, value string) error {
			t.Setenv("BEADS_DIR", beadsDir)
			return SetMachineLocalYamlConfig(key, value)
		},
	}

	for writerName, write := range writers {
		for key := range MachineLocalKeys {
			t.Run(writerName+"/"+key, func(t *testing.T) {
				beadsDir, configPath, localPath := newWorkspace(t, trackedConfigFixture)
				before := readFile(t, configPath)

				if err := write(t, beadsDir, key, sampleValueFor(key)); err != nil {
					t.Fatalf("%s(%s): %v", writerName, key, err)
				}

				if after := readFile(t, configPath); after != before {
					t.Errorf("config.yaml was modified by writing machine-local key %q via %s\n--- before ---\n%s\n--- after ---\n%s",
						key, writerName, before, after)
				}
				if got, ok := readYamlValueAtPath(localPath, key); !ok {
					t.Errorf("%s does not contain %q after the write", LocalConfigFileName, key)
				} else if want := sampleValueFor(key); got != want {
					t.Errorf("%s has %s = %q, want %q", LocalConfigFileName, key, got, want)
				}
			})
		}
	}
}

// TestSharedKeysStillReachTrackedConfig is the SURVIVING CONTROL for the class
// guard: it fails if routing is applied too broadly. Without it, a change that
// sent every key to the sidecar would pass the guard above.
func TestSharedKeysStillReachTrackedConfig(t *testing.T) {
	shared := []struct{ key, value string }{
		{"dolt.auto-start", "false"},     // fleet policy, committed on purpose
		{"dolt.max-conns", "20"},         // project tuning
		{"export.auto", "true"},          // project behavior
		{"sync.remote", "file:///tmp/r"}, // project remote
	}
	for _, tc := range shared {
		t.Run(tc.key, func(t *testing.T) {
			beadsDir, configPath, localPath := newWorkspace(t, trackedConfigFixture)
			before := readFile(t, configPath)

			if err := SetYamlConfigInDir(beadsDir, tc.key, tc.value); err != nil {
				t.Fatalf("SetYamlConfigInDir(%s): %v", tc.key, err)
			}

			// Assert the VALUE is in the tracked file, not merely that the
			// bytes changed. The fixture already carries
			// `dolt.auto-start: false` with a trailing comment, so writing the
			// same value is a legitimate no-op byte-wise -- and this assertion
			// used to pass only because the fork's writer destroyed that
			// trailing comment. Upstream's writer preserves it (that is what
			// TestDottedSetKeepsTheTrailingCommentOnTheKeyItRewrites pins), so
			// "bytes changed" was measuring the bug, not the contract.
			if got, ok := yamlValueInContent(readFile(t, configPath), tc.key); !ok || got != tc.value {
				t.Errorf("shared key %q reads back from config.yaml as %q (present=%v), want %q\n--- before ---\n%s\n--- after ---\n%s",
					tc.key, got, ok, tc.value, before, readFile(t, configPath))
			}
			if _, err := os.Stat(localPath); err == nil {
				t.Errorf("writing shared key %q created %s; only machine-local keys belong there", tc.key, LocalConfigFileName)
			}
		})
	}
}

// TestMachineLocalSidecarWinsOnRead pins the precedence half of the contract:
// routing writes to the sidecar is only correct because reads merge it last.
func TestMachineLocalSidecarWinsOnRead(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A committed shared DEFAULT in the tracked file...
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"),
		[]byte("dolt.mode: embedded\ndolt.auto-start: false\n"), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	// ...overridden for THIS machine by the sidecar.
	if err := os.WriteFile(filepath.Join(beadsDir, LocalConfigFileName),
		[]byte("dolt.mode: server\n"), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	t.Chdir(tmpDir)
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if got := GetYamlConfig("dolt.mode"); got != "server" {
		t.Errorf("dolt.mode = %q, want \"server\" (sidecar must win over config.yaml)", got)
	}
	if got := GetBool("dolt.auto-start"); got != false {
		t.Errorf("dolt.auto-start = %v, want false (shared key still read from config.yaml)", got)
	}
	// The sidecar value must register as an explicit setting, not a default:
	// backup auto-detection and `bd config get` both branch on this.
	if src := GetValueSource("dolt.mode"); src != SourceConfigFile {
		t.Errorf("GetValueSource(dolt.mode) = %v, want %v", src, SourceConfigFile)
	}
}

func TestIsMachineLocalKeyIsExactNotPrefix(t *testing.T) {
	local := []string{"dolt.mode", "dolt.host", "backup.enabled", "backup.interval"}
	for _, key := range local {
		if !IsMachineLocalKey(key) {
			t.Errorf("IsMachineLocalKey(%q) = false, want true", key)
		}
	}
	// Unrecognized siblings under the same prefix must stay SHARED: prefix
	// matching is what made IsYamlOnlyKey sweep in keys nobody classified.
	shared := []string{
		"dolt.auto-start", "dolt.disable-event-flush", "dolt.max-conns",
		"dolt.pool-read-timeout", "backup.git-push", "backup.git-repo",
		"dolt", "backup", "dolt.mode.extra",
	}
	for _, key := range shared {
		if IsMachineLocalKey(key) {
			t.Errorf("IsMachineLocalKey(%q) = true, want false (unclassified keys stay shared)", key)
		}
	}
}

// TestMachineLocalKeysExcludeSecrets: secrets are covered by a STRICTER
// control (CheckSecretKeyGitSafety refuses the write). Routing one here would
// silently downgrade a refusal into a relocation.
func TestMachineLocalKeysExcludeSecrets(t *testing.T) {
	var offenders []string
	for key := range MachineLocalKeys {
		if IsSecretKey(key) {
			offenders = append(offenders, key)
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("secret keys must not be in MachineLocalKeys: %v", offenders)
	}
}

func TestCommentOutYamlKeyAnyForm(t *testing.T) {
	tests := []struct {
		name    string
		content string
		key     string
		want    string
	}{
		{
			name:    "flat form",
			content: "a: 1\nbackup.enabled: false\nb: 2",
			key:     "backup.enabled",
			want:    "a: 1\n# backup.enabled: false\nb: 2",
		},
		{
			name:    "nested form with surviving sibling",
			content: "dolt:\n  disable-event-flush: true\n  mode: server\nb: 2",
			key:     "dolt.mode",
			want:    "dolt:\n  disable-event-flush: true\n  # mode: server\nb: 2",
		},
		{
			name:    "nested form, last child empties the parent",
			content: "dolt:\n  mode: server\nb: 2",
			key:     "dolt.mode",
			want:    "# dolt:\n  # mode: server\nb: 2",
		},
		{
			name:    "deeper key of the same name is NOT touched",
			content: "dolt:\n  pool:\n    mode: fast\n  disable: true",
			key:     "dolt.mode",
			want:    "dolt:\n  pool:\n    mode: fast\n  disable: true",
		},
		{
			name:    "three-segment key is commented at the right depth",
			content: "dolt:\n  pool:\n    mode: fast\n    size: 4",
			key:     "dolt.pool.mode",
			want:    "dolt:\n  pool:\n    # mode: fast\n    size: 4",
		},
		{
			name:    "emptied ancestors are commented out up the chain",
			content: "dolt:\n  pool:\n    mode: fast\nother: 1",
			key:     "dolt.pool.mode",
			want:    "# dolt:\n  # pool:\n    # mode: fast\nother: 1",
		},
		{
			name:    "same-named key under a different parent is left alone",
			content: "other:\n  mode: keep\ndolt:\n  mode: server",
			key:     "dolt.mode",
			want:    "other:\n  mode: keep\n# dolt:\n  # mode: server",
		},
		{
			name:    "absent key is a no-op",
			content: "a: 1\nb: 2",
			key:     "dolt.mode",
			want:    "a: 1\nb: 2",
		},
		{
			name:    "already commented is left alone",
			content: "# dolt.mode: server\na: 1",
			key:     "dolt.mode",
			want:    "# dolt.mode: server\na: 1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := commentOutYamlKeyAnyForm(tc.content, tc.key)
			if err != nil {
				t.Fatalf("commentOutYamlKeyAnyForm(): %v", err)
			}
			if got != tc.want {
				t.Errorf("commentOutYamlKeyAnyForm()\ngot:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// TestMachineLocalKeysAreReadableByDirScopedReaders pins the reader half.
// GetStringFromDir opens the workspace's files directly rather than going
// through merged viper; `bd bootstrap` resolves dolt.port through it, so a
// sidecar value invisible there would silently fall back to a default.
func TestMachineLocalKeysAreReadableByDirScopedReaders(t *testing.T) {
	beadsDir, _, _ := newWorkspace(t, "issue_prefix: vp\ndolt.port: \"1111\"\n")

	if err := SetYamlConfigInDir(beadsDir, "dolt.port", "3306"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := GetStringFromDir(beadsDir, "dolt.port"); got != "3306" {
		t.Errorf("GetStringFromDir(dolt.port) = %q, want \"3306\" (sidecar must win)", got)
	}
	// A shared key still resolves from config.yaml.
	if got := GetStringFromDir(beadsDir, "issue_prefix"); got != "vp" {
		t.Errorf("GetStringFromDir(issue_prefix) = %q, want \"vp\"", got)
	}
}

// TestSetMachineLocalKeyNeverRewritesTrackedConfig pins the property that
// replaced the one-time migration: a machine-local write leaves the tracked
// config.yaml byte-identical, even when that file already defines the key.
//
// The migration used to comment the key out of config.yaml as a side effect of
// a write that reported "(in config.local.yaml)". For a project committing
// `dolt.mode: server` as its shared contract, committing that cleanup sends
// every other clone back to embedded storage — a different, empty database.
// Precedence already makes the sidecar win, so the rewrite bought nothing.
func TestSetMachineLocalKeyNeverRewritesTrackedConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	tracked := "dolt:\n  mode: server\n  host: shared.example\n"
	if err := os.WriteFile(configPath, []byte(tracked), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := setMachineLocalYamlConfig(configPath, "dolt.mode", "embedded"); err != nil {
		t.Fatalf("set: %v", err)
	}

	if got := readFile(t, configPath); got != tracked {
		t.Errorf("config.yaml was rewritten:\n got: %q\nwant: %q", got, tracked)
	}
	if v, ok := yamlValueInContent(readFile(t, LocalConfigPathFor(configPath)), "dolt.mode"); !ok || v != "embedded" {
		t.Errorf("sidecar dolt.mode = %q (found=%v), want embedded", v, ok)
	}
}

// TestUnsetMachineLocalKeyClearsBothFiles pins that `bd config unset` actually
// unsets.
//
// An earlier revision of this change cleared only the sidecar, on the theory
// that a tracked value is a shared default. That made the verb — documented as
// "Delete a configuration value" — a silent no-op for every machine-local key
// whose value lived only in config.yaml, while config_side_effects still
// announced that automatic backups had stopped. The tell was that it required
// rewriting a passing regression test (yaml_config_test.go's UnsetYamlConfig
// case) to a different key.
//
// Removing the key the operator NAMED is not the silent rewrite the migration
// did: that one moved keys nobody asked about, as a side effect of setting
// something else. The caller reports the tracked edit so the git diff is never
// a surprise.
func TestUnsetMachineLocalKeyClearsBothFiles(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("dolt:\n  mode: server\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setMachineLocalYamlConfig(configPath, "dolt.mode", "embedded"); err != nil {
		t.Fatal(err)
	}

	tracked, cleared, _, err := unsetMachineLocalYamlConfig(configPath, "dolt.mode")
	if err != nil {
		t.Fatalf("unset: %v", err)
	}
	if !cleared {
		t.Error("clearedTracked = false, want true: config.yaml defined the key")
	}
	if tracked != "server" {
		t.Errorf("trackedValue = %q, want %q — the caller reports this to the operator", tracked, "server")
	}
	if _, ok := yamlValueInContent(readFile(t, configPath), "dolt.mode"); ok {
		t.Error("config.yaml still defines dolt.mode after unset")
	}
	if _, ok := yamlValueInContent(readFile(t, LocalConfigPathFor(configPath)), "dolt.mode"); ok {
		t.Error("sidecar still defines dolt.mode after unset")
	}
}

// TestUnsetMachineLocalKeyNeverSetCreatesNothing pins that unsetting a key that
// was never set leaves the workspace clean. The old code called
// ensureLocalConfigFile before checking, so an unset in a fresh workspace
// created an untracked file — the self-dirtying this change exists to end.
func TestUnsetMachineLocalKeyNeverSetCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("dolt:\n  mode: server\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := unsetMachineLocalYamlConfig(configPath, "dolt.socket"); err != nil {
		t.Fatalf("unset: %v", err)
	}

	if _, err := os.Stat(LocalConfigPathFor(configPath)); !os.IsNotExist(err) {
		t.Errorf("unset of a never-set key created %s", LocalConfigFileName)
	}
}

// sortedRegistryKeys gives MachineLocalKeys a stable order so the guards below
// produce identical output from identical input.
func sortedRegistryKeys() []string {
	out := make([]string, 0, len(MachineLocalKeys))
	for k := range MachineLocalKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestSidecarWritesAreValidatedLikeTrackedWrites pins that routing a key to the
// sidecar does not cost it its validation.
//
// This is a real regression that shipped and was caught in review: when the
// routing moved from the library writers to the callers, the sidecar writer had
// no validateYamlConfigValue call, so `bd config set dolt.mode bogus` — refused
// by SetYamlConfigInDir — was accepted here and, because the sidecar is merged
// last, the bad value became the LIVE one. A validator a caller escapes by
// choosing a destination is not a validator.
func TestSidecarWritesAreValidatedLikeTrackedWrites(t *testing.T) {
	rejected := map[string]string{
		"dolt.mode":  "bogus",     // neither server nor embedded
		"dolt.debug": "sometimes", // neither true nor false
	}
	sawRejected := 0
	for key, bad := range rejected {
		t.Run(key, func(t *testing.T) {
			if !IsMachineLocalKey(key) {
				t.Fatalf("%s is not in MachineLocalKeys; this test's premise is stale", key)
			}
			beadsDir, _, localPath := newWorkspace(t, trackedConfigFixture)

			// The literal writer's verdict is the control. If it accepts the
			// value there is no refusal to match and this row proves nothing.
			trackedErr := SetYamlConfigInDir(beadsDir, key, bad)
			localErr := SetMachineLocalYamlConfigInDir(beadsDir, key, bad)
			if trackedErr == nil {
				t.Skipf("SetYamlConfigInDir accepts %s=%q, so there is no refusal to match", key, bad)
			}
			sawRejected++
			if localErr == nil {
				t.Fatalf("SetYamlConfigInDir refused %s=%q (%v) but the sidecar writer accepted it", key, bad, trackedErr)
			}
			if data, err := os.ReadFile(localPath); err == nil && strings.Contains(string(data), bad) {
				t.Errorf("rejected value reached the sidecar anyway:\n%s", data)
			}
		})
	}
	if sawRejected == 0 {
		t.Fatal("no row exercised a refusal; the test proved nothing")
	}
}

// TestSidecarWritersRefuseSharedKeys is the other half of the registry
// contract: SaveConfigValue refuses to put a machine-local key in the tracked
// file, and this refuses to put a shared key in the sidecar. Together they make
// MachineLocalKeys the single decision point in BOTH directions, so the split
// cannot be re-created by a caller picking a function.
func TestSidecarWritersRefuseSharedKeys(t *testing.T) {
	for _, key := range []string{"dolt.auto-start", "dolt.max-conns", "export.auto", "issue_prefix"} {
		t.Run(key, func(t *testing.T) {
			if IsMachineLocalKey(key) {
				t.Fatalf("%s is in MachineLocalKeys; this test's premise is stale", key)
			}
			beadsDir, configPath, localPath := newWorkspace(t, trackedConfigFixture)
			before := readFile(t, configPath)

			if err := SetMachineLocalYamlConfigInDir(beadsDir, key, sampleValueFor(key)); err == nil {
				t.Errorf("SetMachineLocalYamlConfigInDir(%s) succeeded; it must refuse a shared key", key)
			}
			if after := readFile(t, configPath); after != before {
				t.Errorf("config.yaml was modified despite the refusal:\n%s", after)
			}
			if data, err := os.ReadFile(localPath); err == nil && strings.Contains(string(data), key) {
				t.Errorf("%s reached the sidecar despite the refusal:\n%s", key, data)
			}
		})
	}
}

// TestSidecarWritersStillAcceptEveryRegistryKey is the control for both guards:
// it fails if either is applied too broadly and starts rejecting the keys the
// sidecar exists to hold.
func TestSidecarWritersStillAcceptEveryRegistryKey(t *testing.T) {
	for _, key := range sortedRegistryKeys() {
		t.Run(key, func(t *testing.T) {
			beadsDir, _, localPath := newWorkspace(t, trackedConfigFixture)
			if err := SetMachineLocalYamlConfigInDir(beadsDir, key, sampleValueFor(key)); err != nil {
				t.Fatalf("SetMachineLocalYamlConfigInDir(%s): %v", key, err)
			}
			if !strings.Contains(readFile(t, localPath), sampleValueFor(key)) {
				t.Errorf("%s was not written to the sidecar:\n%s", key, readFile(t, localPath))
			}
		})
	}
}

// TestUnsetLeavesBothFilesAloneWhenTheTrackedShapeIsRefused pins the ordering
// fix found in review.
//
// commentOutYamlKeyAnyForm can REFUSE a shape (#6574's unsupportedUnsetShape).
// The unset used to write the sidecar first and only then discover the refusal,
// so it exited non-zero having already deleted the operator's machine-local
// override — told them it failed, and did half of it anyway. A refusal must
// leave BOTH files exactly as they were.
func TestUnsetLeavesBothFilesAloneWhenTheTrackedShapeIsRefused(t *testing.T) {
	// A flow mapping is the shape the line-based editor cannot touch.
	beadsDir, configPath, localPath := newWorkspace(t, "issue_prefix: vp\ndolt: {mode: server}\n")
	if err := SetMachineLocalYamlConfigInDir(beadsDir, "dolt.mode", "embedded"); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}
	trackedBefore := readFile(t, configPath)
	localBefore := readFile(t, localPath)

	_, _, _, err := unsetMachineLocalYamlConfig(configPath, "dolt.mode")
	if err == nil {
		t.Skip("the tracked shape was not refused; this test's premise is stale")
	}

	if got := readFile(t, configPath); got != trackedBefore {
		t.Errorf("config.yaml changed despite the refusal:\n--- before ---\n%s\n--- after ---\n%s", trackedBefore, got)
	}
	if got := readFile(t, localPath); got != localBefore {
		t.Errorf("the machine-local override was cleared even though the unset failed:\n--- before ---\n%s\n--- after ---\n%s", localBefore, got)
	}
}

// TestSaveConfigValueRefusesMachineLocalKeys is the tracked-file half of the
// registry contract. Its mirror, TestSidecarWritersRefuseSharedKeys, already
// existed; this one did not, and the refusal it pins was missing too. Two
// comments claimed MachineLocalKeys was the single decision point "in BOTH
// directions" while only one direction was enforced -- and with no test on
// this side, nothing could ever report the gap. Found in review of #60.
func TestSaveConfigValueRefusesMachineLocalKeys(t *testing.T) {
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(ResetForTesting)

	checked := 0
	for _, key := range sortedRegistryKeys() {
		t.Run(key, func(t *testing.T) {
			beadsDir, configPath, _ := newWorkspace(t, trackedConfigFixture)
			before := readFile(t, configPath)

			err := SaveConfigValue(key, sampleValueFor(key), beadsDir)
			if err == nil {
				t.Fatalf("SaveConfigValue(%s) succeeded; it must refuse a machine-local key", key)
			}
			if !strings.Contains(err.Error(), LocalConfigFileName) {
				t.Errorf("error does not point the caller at the sidecar: %v", err)
			}
			if after := readFile(t, configPath); after != before {
				t.Errorf("config.yaml was modified despite the refusal:\n%s", after)
			}
		})
		checked++
	}
	if checked == 0 {
		t.Fatal("MachineLocalKeys is empty, so no refusal was exercised; the test proved nothing")
	}
}

// TestSaveConfigValueStillWritesSharedKeys is the control for the refusal
// above: it fails if the guard is applied too broadly and breaks the one real
// caller, cmd/bd/init.go writing no-git-ops.
func TestSaveConfigValueStillWritesSharedKeys(t *testing.T) {
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(ResetForTesting)

	beadsDir, configPath, _ := newWorkspace(t, trackedConfigFixture)
	if err := SaveConfigValue("no-git-ops", true, beadsDir); err != nil {
		t.Fatalf("SaveConfigValue(no-git-ops): %v", err)
	}
	if !strings.Contains(readFile(t, configPath), "no-git-ops") {
		t.Errorf("no-git-ops was not written to config.yaml:\n%s", readFile(t, configPath))
	}
}
