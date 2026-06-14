package beads

import (
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestResolveOpenBackend is the open-decision matrix. It asserts the pure
// routing decision OpenBestAvailable makes for each configured Dolt mode,
// without touching a database. The load-bearing case is proxied-server: it
// MUST route to the server-backed store (NewFromConfig), never fall through
// to a fresh embedded DB (the "invalid issue type" misroute).
func TestResolveOpenBackend(t *testing.T) {
	tests := []struct {
		name string
		cfg  *configfile.Config
		want openBackend
	}{
		{
			name: "nil config defaults to embedded",
			cfg:  nil,
			want: openBackendEmbedded,
		},
		{
			name: "explicit embedded mode",
			cfg:  &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeEmbedded},
			want: openBackendEmbedded,
		},
		{
			name: "no dolt_mode defaults to embedded",
			cfg:  &configfile.Config{Backend: configfile.BackendDolt},
			want: openBackendEmbedded,
		},
		{
			name: "server mode routes to server",
			cfg:  &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer},
			want: openBackendServer,
		},
		{
			name: "proxied-server mode routes to server (the misroute fix)",
			cfg:  &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeProxiedServer},
			want: openBackendServer,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveOpenBackend(tt.cfg)
			if got != tt.want {
				t.Errorf("resolveOpenBackend() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestResolveOpenBackend_ProxiedNeverEmbedded is the explicit regression guard
// for the verified root cause: proxied-server must never resolve to the
// embedded backend, because the embedded path creates a fresh, typeless DB
// (no custom_types) and produces the "invalid issue type" error.
func TestResolveOpenBackend_ProxiedNeverEmbedded(t *testing.T) {
	cfg := &configfile.Config{
		Backend:  configfile.BackendDolt,
		DoltMode: configfile.DoltModeProxiedServer,
	}
	if got := resolveOpenBackend(cfg); got == openBackendEmbedded {
		t.Fatalf("proxied-server resolved to embedded backend: this is the "+
			"fresh-typeless-DB misroute that yields 'invalid issue type' (got %v)", got)
	}
}
