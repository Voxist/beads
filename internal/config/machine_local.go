package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LocalConfigFileName moved to config.go when upstream adopted the same
// constant with the identical value ("config.local.yaml"). Declaring it here
// too would be a redeclaration, so this file now uses upstream's. The sidecar
// still behaves as this package documents: Initialize() merges it LAST, so a
// value there wins over the tracked config.yaml for the same key.

const localConfigHeader = `# bd machine-local configuration.
#
# Settings here describe THIS machine (which Dolt to talk to, whether this
# host takes backups) rather than the project. bd merges this file last, so a
# value here overrides the same key in the tracked config.yaml.
#
# This file must NOT be committed: .beads/.gitignore excludes it.
`

// MachineLocalKeys are config keys whose value is a statement about the
// MACHINE bd is running on, not about the project.
//
// They are written to the untracked config.local.yaml sidecar instead of the
// tracked .beads/config.yaml. Writing them to config.yaml has two costs, and
// the first one is paid by every user of the repository:
//
//  1. The checkout dirties itself. bd rewrites config.yaml as a side effect of
//     ordinary operation, so `git status` reports a modification no one made.
//     Any clean-tree guard — a release script, a pre-commit hook, CI — then
//     refuses for a reason no operator caused and none can fix by committing
//     once, because the next bd run writes the file again.
//  2. One machine's answer propagates to every clone that pulls it. That is
//     the same hazard IsUserGlobalKey exists to prevent for node_id, one axis
//     over: user-global keys are per-machine across ALL workspaces, these are
//     per-machine for ONE workspace. `dolt.mode` is the exemplar — bd's own
//     init code calls a config.yaml dolt.mode "a deliberate statement about
//     this machine" — and it cannot live in the user-global file, because a
//     host may legitimately run one workspace in server mode and another
//     embedded.
//
// Membership is EXACT, never by prefix. Prefix matching is what made
// IsYamlOnlyKey coarse enough to sweep in keys nobody classified; here an
// unrecognized key stays shared, which preserves existing behavior. A
// committed value still works as a shared DEFAULT: reads merge config.yaml
// first and the sidecar second, so a project can ship one and a machine can
// override it.
//
// Deliberately NOT included, as shared project contract:
//   - dolt.auto-start, dolt.disable-event-flush: fleet-wide policy about how
//     the project's store is driven, committed on purpose.
//   - dolt.shared-server: arguably machine-local — it selects a per-machine
//     path under ~/.beads/shared-server/ — but bd's proxied-server migrations
//     record it in config.yaml as workspace state and assert on it there, so
//     it is left shared pending a deliberate decision rather than moved as a
//     side effect of this change.
//   - dolt.max-conns, dolt.pool-read-timeout, dolt.pool-write-timeout: tuning
//     a project ships for all of its clones.
//   - backup.git-push, backup.git-repo: where backups go is arguably a
//     project decision; only whether THIS host takes them is local.
//   - secrets (github.token, *.api_key, ...): already covered by the stricter
//     control in CheckSecretKeyGitSafety, which REFUSES the write rather than
//     relocating it. Routing them here would silently downgrade that refusal.
var MachineLocalKeys = map[string]bool{
	// Which Dolt this host talks to, and how.
	"dolt.mode":     true,
	"dolt.host":     true,
	"dolt.port":     true,
	"dolt.socket":   true,
	"dolt.user":     true,
	"dolt.data-dir": true,
	"dolt.debug":    true,

	// Whether THIS host takes backups, and how often. Backups are written to
	// .beads/backup/, which .beads/.gitignore already excludes as local-only.
	"backup.enabled":  true,
	"backup.interval": true,
}

// IsMachineLocalKey reports whether key describes this machine rather than the
// project, and so must be written to the untracked sidecar. Exact match only —
// see MachineLocalKeys.
func IsMachineLocalKey(key string) bool {
	return MachineLocalKeys[key]
}

// LocalConfigPathFor returns the sidecar path beside the given config.yaml.
func LocalConfigPathFor(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), LocalConfigFileName)
}

// setMachineLocalYamlConfig writes a machine-local key to the sidecar beside
// configPath, first migrating any machine-local keys already sitting in the
// tracked config.yaml.
func setMachineLocalYamlConfig(configPath, key, value string) error {
	// The registry is the single decision point in BOTH directions:
	// SaveConfigValue refuses a machine-local key, and this refuses one that is
	// not. Without the second half a caller could re-create the split simply by
	// picking a function.
	if !IsMachineLocalKey(key) {
		return fmt.Errorf("%q is not a machine-local key; use SetYamlConfig (or SetYamlConfigInDir) to write it to the tracked config.yaml", key)
	}
	// Run the SAME validation the literal writers run. Without this, moving the
	// routing to the callers silently REMOVED a refusal: `bd config set
	// dolt.mode bogus` is rejected by SetYamlConfigInDir but was accepted here,
	// and the sidecar wins on read -- so the bad value became the live one.
	// dolt.mode and dolt.debug both have cases in validateYamlConfigValue. A
	// validator a caller escapes by choosing a destination is not a validator.
	if err := validateYamlConfigValue(key, value); err != nil {
		return err
	}
	localPath := LocalConfigPathFor(configPath)
	if err := ensureLocalConfigFile(localPath); err != nil {
		return err
	}
	// The tracked config.yaml is NOT touched, and no migration runs. Both read
	// paths already prefer the sidecar — Initialize merges config.local.yaml
	// AFTER config.yaml, and GetStringFromDir checks the sidecar first — so a
	// value written here wins without moving anything out of the tracked file.
	//
	// Rewriting config.yaml as a side effect of a write that reports
	// "(in config.local.yaml)" was worse than untidy. A project that commits
	// `dolt.mode: server` as its shared contract would have that line silently
	// commented out; committing the result sends every other clone back to
	// embedded storage — a different, empty database. Leaving the tracked file
	// alone costs nothing, because precedence already does the job.
	return setSidecarYamlKey(localPath, key, value)
}

// setSidecarYamlKey writes a key into the sidecar in the FLAT dotted form,
// always, whatever shape the file is already in.
//
// The form is load-bearing, not cosmetic. viper's key lookup tries the longest
// joined prefix first, so a flat `dolt.port:` BEATS a nested `dolt: {port:}`
// regardless of merge order — the sidecar being merged last does not save it.
// setYamlConfigAtPath picks the shape by accident of file state: a fresh
// sidecar is comment-only so updateNestedYamlKey bails and the key lands flat,
// while every later write finds a mapping and lands nested. A stock `bd init`
// config.yaml is comment-only too, so the first machine-local key old bd wrote
// there is flat as well.
//
// Combine those and the sidecar silently loses: `bd config set dolt.port 3307`
// reports success while merged viper keeps returning the tracked 9999, and
// GetStringFromDir (sidecar-first) returns 3307 — the two read paths disagree,
// so bootstrap provisions one port and the runtime dials another. Pinning the
// flat form makes the sidecar's key at least as specific as anything in the
// tracked file, so last-merge-wins holds.
func setSidecarYamlKey(localPath, key, value string) error {
	content, err := os.ReadFile(localPath) //nolint:gosec // localPath derives from a resolved config.yaml path
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", LocalConfigFileName, err)
	}
	updated, err := updateFlatYamlKey(string(content), key, value)
	if err != nil {
		return err
	}
	if err := os.WriteFile(localPath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", LocalConfigFileName, err)
	}
	return nil
}

// unsetMachineLocalYamlConfig comments a machine-local key out of the sidecar.
// The tracked config.yaml is left alone: a value there is a shared default that
// only an explicit edit should remove.
func unsetMachineLocalYamlConfig(configPath, key string) (trackedValue string, clearedTracked, clearedLocal bool, err error) {
	localPath := LocalConfigPathFor(configPath)
	normalized := key

	// COMPUTE BOTH EDITS BEFORE WRITING EITHER.
	//
	// Since the 2026-09-27 resync, commentOutYamlKeyAnyForm can REFUSE a shape
	// (#6574's unsupportedUnsetShape) rather than always succeeding. Writing
	// the sidecar first and only then discovering the tracked file is, say, a
	// flow mapping left the unset half-applied while the command exited
	// non-zero: the operator was told it failed, and their machine-local
	// override was gone anyway. Computing both first means a refusal on either
	// file leaves BOTH untouched, and the caller's purpose-built "nothing was
	// removed, edit it by hand" message stays reachable.
	var (
		localUpdated string
		writeLocal   bool
		localContent string
	)
	if content, readErr := os.ReadFile(localPath); readErr == nil { //nolint:gosec // localPath derives from a resolved config.yaml path
		localContent = string(content)
		updated, unsetErr := commentOutYamlKeyAnyForm(localContent, normalized)
		if unsetErr != nil {
			return "", false, false, unsetErr
		}
		localUpdated, writeLocal = updated, updated != localContent
	} else if !os.IsNotExist(readErr) {
		return "", false, false, fmt.Errorf("failed to read %s: %w", LocalConfigFileName, readErr)
	}

	// Then clear the tracked value, because `bd config unset` is documented as
	// "Delete a configuration value" and an operator typing it expects the
	// setting to stop applying. Leaving a tracked value in place made the verb
	// a silent no-op for every machine-local key whose value lived only in
	// config.yaml — and config_side_effects would still announce, wrongly,
	// that automatic backups had stopped.
	//
	// This is NOT the silent rewrite that the migration did. That one moved
	// keys the operator had not named, as a side effect of setting something
	// else. This removes exactly the key they asked to remove, and the caller
	// reports the tracked file it touched so the git diff is never a surprise.
	trackedRaw, readErr := os.ReadFile(configPath) //nolint:gosec // configPath is a resolved config.yaml path
	if readErr != nil {
		if os.IsNotExist(readErr) {
			clearedLocal, err = flushSidecarUnset(localPath, localUpdated, writeLocal)
			return "", false, clearedLocal, err
		}
		return "", false, false, fmt.Errorf("failed to read config.yaml: %w", readErr)
	}
	value, found := yamlValueInContent(string(trackedRaw), normalized)
	if !found {
		clearedLocal, err = flushSidecarUnset(localPath, localUpdated, writeLocal)
		return "", false, clearedLocal, err
	}
	// commentOutYamlKeyAnyForm is line-based and cannot reach a key inside a
	// FLOW mapping (`dolt: {mode: server}`). Reporting clearedTracked=false
	// there is what lets the caller say nothing was removed, instead of
	// printing success and a side-effect consequence that did not happen.
	updated, unsetErr := commentOutYamlKeyAnyForm(string(trackedRaw), normalized)
	if unsetErr != nil {
		// Nothing has been written yet, so the sidecar override survives and
		// the operator's state is exactly what it was before the command.
		return "", false, false, unsetErr
	}

	// Both edits are known good; now write.
	if clearedLocal, err = flushSidecarUnset(localPath, localUpdated, writeLocal); err != nil {
		return "", false, false, err
	}
	if updated == string(trackedRaw) {
		return "", false, clearedLocal, nil
	}
	if writeErr := os.WriteFile(configPath, []byte(updated), 0o600); writeErr != nil {
		return "", false, clearedLocal, fmt.Errorf("failed to write config.yaml: %w", writeErr)
	}
	return value, true, clearedLocal, nil
}

// flushSidecarUnset writes the sidecar edit computed by
// unsetMachineLocalYamlConfig, once both edits are known to be applicable.
func flushSidecarUnset(localPath, updated string, write bool) (bool, error) {
	if !write {
		return false, nil
	}
	if err := os.WriteFile(localPath, []byte(updated), 0o600); err != nil {
		return false, fmt.Errorf("failed to write %s: %w", LocalConfigFileName, err)
	}
	return true, nil
}

// TrackedYamlValueFor reports a machine-local key's value still present in the
// TRACKED config.yaml, so a caller can tell the operator that unsetting their
// machine-local override did not remove the shared default.
//
// Without this the command is dishonest in a way that matters: it prints
// "Unset dolt.mode" and exits 0 while `bd config get dolt.mode` keeps returning
// the tracked value, and the operator has no hint about which of the two files
// is still speaking.
func TrackedYamlValueFor(configPath, key string) (string, bool) {
	raw, err := os.ReadFile(configPath) //nolint:gosec // configPath is a resolved config.yaml path
	if err != nil {
		return "", false
	}
	return yamlValueInContent(string(raw), key)
}

// ensureSidecarIgnored adds exactly the config.local.yaml line to
// .beads/.gitignore when it is missing, and nothing else.
//
// It lives here, at the funnel every sidecar write passes through, rather than
// in one CLI branch: `bd config set-many` and `bd dolt set --update-config`
// also create this file, and a guarantee that only `bd config set` honors is
// not a guarantee — the untracked sidecar still shows up in git status for
// every other writer.
//
// It writes ONE pattern deliberately. doctor.EnsureGitignoreForBeadsDir appends
// every missing required pattern under an "# Added by bd" header — 27 lines in
// a real workspace — and .beads/.gitignore is tracked, so calling it from a
// config write turned `bd config set dolt.mode server` into a silent, unrelated
// diff on a tracked file. Repairing the whole file is bd doctor --fix's job;
// this only covers the file this package just created.
func ensureSidecarIgnored(beadsDir string) {
	gitignorePath := filepath.Join(beadsDir, ".gitignore")
	content, err := os.ReadFile(gitignorePath) //nolint:gosec // beadsDir is a resolved workspace path
	if err != nil && !os.IsNotExist(err) {
		return // best effort: a config write must not fail on .gitignore
	}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == LocalConfigFileName {
			return
		}
	}
	updated := string(content)
	if updated != "" && !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	updated += LocalConfigFileName + "\n"
	// 0644 matches doctor.ensureProjectGitignore, which has a test pinning that
	// mode for this same file; 0600 here would make the two writers disagree
	// depending on which one created it.
	_ = os.WriteFile(gitignorePath, []byte(updated), 0o644) //nolint:gosec // .gitignore is not sensitive and must match doctor's mode
}

// ensureLocalConfigFile creates the sidecar with its header if absent. The
// 0600 posture matches every other config writer in this package.
func ensureLocalConfigFile(localPath string) error {
	if _, err := os.Stat(localPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat %s: %w", LocalConfigFileName, err)
	}
	if err := os.WriteFile(localPath, []byte(localConfigHeader), 0o600); err != nil {
		return fmt.Errorf("failed to create %s: %w", LocalConfigFileName, err)
	}
	// The file is untracked; the ignore rule must exist by the time it does.
	ensureSidecarIgnored(filepath.Dir(localPath))
	return nil
}

// yamlValueInContent reads a dotted key out of YAML text in either the flat
// dotted form (`dolt.mode: server`) or the nested form (`dolt:\n  mode:
// server`). bd has written both over its lifetime, so a migration that
// understood only one would leave the other behind.
func yamlValueInContent(content, key string) (string, bool) {
	if strings.TrimSpace(content) == "" {
		return "", false
	}
	return yamlValueFromBytes([]byte(content), key)
}

// SetMachineLocalYamlConfig writes key to the machine-local sidecar beside the
// project config.yaml the caller is standing in.
//
// This is the EXPLICIT half of the machine-local split (bd-zj95 / upstream
// #6125). The library's own writers -- SetYamlConfig, SetYamlConfigInDir,
// UnsetYamlConfig -- deliberately write exactly where they are told, so a
// caller that names a file gets that file and upstream #6574's dotted-key
// round-trip guarantees hold unchanged. The ROUTING decision belongs to the
// caller that knows the user's intent: `bd config set` (single and set-many),
// `bd dolt set --update-config` and `bd init --debug` each pick between this
// and a literal writer via IsMachineLocalKey.
func SetMachineLocalYamlConfig(key, value string) error {
	configPath, err := findProjectConfigYaml()
	if err != nil {
		return err
	}
	return setMachineLocalYamlConfig(configPath, key, value)
}

// SetMachineLocalYamlConfigInDir is SetMachineLocalYamlConfig for a named
// workspace, for callers that already hold the beads dir.
func SetMachineLocalYamlConfigInDir(beadsDir, key, value string) error {
	return setMachineLocalYamlConfig(filepath.Join(beadsDir, "config.yaml"), key, value)
}

// MachineLocalYamlValue reads a key from the project's config.local.yaml ONLY,
// never the tracked config.yaml.
//
// It exists so the CLI can ATTRIBUTE a value correctly. GetValueSource reports
// SourceConfigFile for anything viper merged, which cannot tell the tracked
// file from the sidecar; labeling a sidecar value "config.yaml" would send an
// operator to edit a file that does not contain it — the same misattribution
// config_show.go already guards against for user-global keys.
func MachineLocalYamlValue(key string) (string, bool) {
	if !IsMachineLocalKey(key) {
		return "", false
	}
	configPath, err := findProjectConfigYaml()
	if err != nil {
		return "", false
	}
	return readYamlValueAtPath(LocalConfigPathFor(configPath), key)
}
