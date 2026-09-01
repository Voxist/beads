package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LocalConfigFileName is the untracked sidecar that sits beside a project's
// .beads/config.yaml. Initialize() already merges it LAST, so a value here
// wins over the tracked config.yaml for the same key.
const LocalConfigFileName = "config.local.yaml"

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
	return MachineLocalKeys[normalizeYamlKey(key)]
}

// LocalConfigPathFor returns the sidecar path beside the given config.yaml.
func LocalConfigPathFor(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), LocalConfigFileName)
}

// setMachineLocalYamlConfig writes a machine-local key to the sidecar beside
// configPath, first migrating any machine-local keys already sitting in the
// tracked config.yaml.
func setMachineLocalYamlConfig(configPath, key, value string) error {
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
	return setYamlConfigAtPath(localPath, normalizeYamlKey(key), value)
}

// unsetMachineLocalYamlConfig comments a machine-local key out of the sidecar.
// The tracked config.yaml is left alone: a value there is a shared default that
// only an explicit edit should remove.
func unsetMachineLocalYamlConfig(configPath, key string) error {
	localPath := LocalConfigPathFor(configPath)
	// Sidecar only, and no migration. Unsetting a machine-local key clears THIS
	// machine's override; a value left in the tracked config.yaml is a shared
	// default that only an explicit edit should remove — which is what this
	// function's contract has always said.
	//
	// The previous version called migrateMachineLocalKeys first, which
	// contradicted that contract and made the command mean two different
	// things: before the one-time marker existed it removed the key from
	// config.yaml as well, and after the marker it did not. Same command,
	// opposite outcome, decided by invisible state.
	//
	// It also no longer creates the sidecar just to comment out a key that was
	// never set. An unset in a clean workspace now leaves no file behind.
	content, err := os.ReadFile(localPath) //nolint:gosec // localPath is derived from a resolved config.yaml path
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing set on this machine
		}
		return fmt.Errorf("failed to read %s: %w", LocalConfigFileName, err)
	}
	updated := commentOutYamlKeyAnyForm(string(content), normalizeYamlKey(key))
	if updated == string(content) {
		return nil // key was not set on this machine; nothing to write
	}
	if err := os.WriteFile(localPath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", LocalConfigFileName, err)
	}
	return nil
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
	return yamlValueInContent(string(raw), normalizeYamlKey(key))
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
	return readYamlValueAtPath(LocalConfigPathFor(configPath), normalizeYamlKey(key))
}
