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

// machineLocalMigrationMarker records that the one-time migration of
// machine-local keys out of the tracked config.yaml has already run for this
// workspace.
//
// It is a YAML COMMENT rather than a config key on purpose: viper merges this
// file into the live settings, so a real key would show up in `bd config list`
// and in every consumer that ranges over settings.
const machineLocalMigrationMarker = "# bd: machine-local keys migrated out of config.yaml (do not remove)"

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
	if err := migrateMachineLocalKeys(configPath, localPath); err != nil {
		return err
	}
	// Written after the migration so the value being set wins over any older
	// value the migration lifted out of config.yaml.
	return setYamlConfigAtPath(localPath, normalizeYamlKey(key), value)
}

// unsetMachineLocalYamlConfig comments a machine-local key out of the sidecar.
// The tracked config.yaml is left alone: a value there is a shared default that
// only an explicit edit should remove.
func unsetMachineLocalYamlConfig(configPath, key string) error {
	localPath := LocalConfigPathFor(configPath)
	// Unset has to migrate first. Before the migration has run, the live value
	// is still the one in config.yaml; clearing only the sidecar would report
	// success while `bd config get` kept returning the old value.
	if err := ensureLocalConfigFile(localPath); err != nil {
		return err
	}
	if err := migrateMachineLocalKeys(configPath, localPath); err != nil {
		return err
	}
	content, err := os.ReadFile(localPath) //nolint:gosec // localPath is derived from a resolved config.yaml path
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing set on this machine
		}
		return fmt.Errorf("failed to read %s: %w", LocalConfigFileName, err)
	}
	updated := commentOutYamlKeyAnyForm(string(content), normalizeYamlKey(key))
	if err := os.WriteFile(localPath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", LocalConfigFileName, err)
	}
	return nil
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

// migrateMachineLocalKeys performs the ONE-TIME move of machine-local keys out
// of the tracked config.yaml and into the sidecar.
//
// It runs at most once per workspace, gated on a marker comment in the sidecar.
// Running it on every write would re-take a value an operator had deliberately
// re-added to config.yaml as a shared default, which is the same self-dirtying
// churn this change exists to end — just with the sign flipped.
//
// config.yaml is rewritten line-by-line (keys are commented out, matching
// UnsetYamlConfig's convention) rather than re-marshaled, so comments,
// ordering, and formatting of the tracked file survive: the operator gets one
// small reviewable diff to commit, not a reflow of the whole file.
func migrateMachineLocalKeys(configPath, localPath string) error {
	localContent, err := os.ReadFile(localPath) //nolint:gosec // localPath is derived from a resolved config.yaml path
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", LocalConfigFileName, err)
	}
	if strings.Contains(string(localContent), machineLocalMigrationMarker) {
		return nil // already migrated
	}

	trackedRaw, err := os.ReadFile(configPath) //nolint:gosec // configPath is a resolved config.yaml path
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to read config.yaml: %w", err)
		}
		trackedRaw = nil
	}
	tracked := string(trackedRaw)

	migrated := make(map[string]string)
	for key := range MachineLocalKeys {
		value, found := yamlValueInContent(tracked, key)
		if !found {
			continue
		}
		// A value already on this machine wins; the tracked one is only a
		// default and must not overwrite it.
		if _, alreadyLocal := yamlValueInContent(string(localContent), key); !alreadyLocal {
			migrated[key] = value
		}
		tracked = commentOutYamlKeyAnyForm(tracked, key)
	}

	newLocal := string(localContent)
	for key, value := range migrated {
		newLocal, err = updateYamlKey(newLocal, key, value)
		if err != nil {
			return fmt.Errorf("migrating %s into %s: %w", key, LocalConfigFileName, err)
		}
	}
	// Values first, marker last. If the config.yaml rewrite below fails (a
	// read-only checkout, a full disk), a marker already on disk would make
	// this one-time migration skip forever, stranding the keys in the tracked
	// file. Writing the sidecar twice is cheap; it is untracked.
	if err := os.WriteFile(localPath, []byte(newLocal), 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", LocalConfigFileName, err)
	}

	// Only touch the tracked file when something actually moved.
	if trackedRaw != nil && tracked != string(trackedRaw) {
		if err := os.WriteFile(configPath, []byte(tracked), 0o600); err != nil {
			return fmt.Errorf("failed to write config.yaml: %w", err)
		}
	}

	if err := os.WriteFile(localPath, []byte(withMigrationMarker(newLocal)), 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", LocalConfigFileName, err)
	}
	return nil
}

// withMigrationMarker records the marker as the FIRST line of the sidecar.
//
// Position matters: subsequent writes to this file go through
// updateNestedYamlKey, which round-trips the document through yaml.Node and
// preserves comments by their attachment to nodes. A head comment at the top
// of the document is the position that survives that round-trip most reliably;
// a trailing comment has no node to attach to. Losing the marker would let the
// one-time migration run a second time and re-take a value the operator had
// deliberately restored to config.yaml as a shared default.
func withMigrationMarker(content string) string {
	if strings.Contains(content, machineLocalMigrationMarker) {
		return content
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return machineLocalMigrationMarker + "\n" + content
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
