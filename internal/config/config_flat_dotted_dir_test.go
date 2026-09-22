package config

import (
	"os"
	"path/filepath"
	"testing"
)

// GetStringFromDir must read the FLAT dotted form out of config.yaml, not only
// the nested form. bd writes the flat form itself, and every Gas City
// workspace carries `dolt.auto-start: false` that way alongside a separate
// nested `dolt:` map that does NOT contain the key — the shape that made the
// old nested-only walk return "unset" and let bd spawn a server against a
// workspace that had disabled exactly that (ga-rpgvw).
func TestGetStringFromDirReadsFlatDottedKeyInConfigYaml(t *testing.T) {
	for _, tc := range []struct {
		name, body, key, want string
	}{
		{
			name: "flat dotted alongside a nested map of the same prefix",
			body: "issue_prefix: vc\ndolt.auto-start: false\ndolt:\n  disable-event-flush: true\ndolt.mode: server\n",
			key:  "dolt.auto-start", want: "false",
		},
		{
			name: "nested form still works",
			body: "dolt:\n  auto-start: false\n",
			key:  "dolt.auto-start", want: "false",
		},
		{
			name: "sibling key in the nested map is untouched",
			body: "dolt.auto-start: false\ndolt:\n  disable-event-flush: true\n",
			key:  "dolt.disable-event-flush", want: "true",
		},
		{
			// Precedence when a file carries both: the flat form wins, which is
			// what viper itself resolves for the same file, so a cmd-level read
			// and a dir-level read cannot disagree.
			name: "flat wins over a disagreeing nested value",
			body: "dolt.auto-start: false\ndolt:\n  auto-start: true\n",
			key:  "dolt.auto-start", want: "false",
		},
		{
			name: "absent key stays empty",
			body: "issue_prefix: vc\n",
			key:  "dolt.auto-start", want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := GetStringFromDir(dir, tc.key); got != tc.want {
				t.Errorf("GetStringFromDir(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

// The sidecar keeps precedence over config.yaml.
func TestGetStringFromDirPrefersLocalSidecar(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("dolt.auto-start: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, LocalConfigFileName), []byte("dolt.auto-start: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := GetStringFromDir(dir, "dolt.auto-start"); got != "false" {
		t.Errorf("GetStringFromDir = %q, want the sidecar's \"false\"", got)
	}
}
