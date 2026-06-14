package beads

import "github.com/steveyegge/beads/internal/configfile"

// openBackend identifies which storage backend OpenBestAvailable should open
// for a given configuration. Keeping the routing decision in a small, pure
// function makes the open-decision matrix unit-testable without a live
// database and keeps the cgo/nocgo entry points in lockstep.
type openBackend int

const (
	// openBackendEmbedded selects the in-process embedded Dolt engine
	// (CGo-only). It is the default when no explicit server-backed mode is
	// configured.
	openBackendEmbedded openBackend = iota
	// openBackendServer selects a server-backed store reached over the MySQL
	// wire protocol via dolt.NewFromConfig. Both plain server mode and
	// proxied-server mode are server-backed: a proxied-server config points at
	// a local db-proxy that speaks the same protocol, so it must be routed
	// here rather than falling through to a fresh embedded database.
	openBackendServer
)

// resolveOpenBackend returns the backend OpenBestAvailable should use for cfg.
//
// A nil cfg (no metadata.json) defaults to embedded. Server mode and
// proxied-server mode both resolve to the server-backed store. Routing
// proxied-server to the server store is the fix for the misroute in which a
// proxied-server config fell through to the embedded engine, which created a
// fresh, typeless database (lacking custom_types) and produced the
// "invalid issue type" failure. The store opened via the server path performs
// the project-identity assertion (see dolt.New / verifyProjectIdentity), so a
// server that is serving a different project's database fails loudly with
// ErrStoreIdentityMismatch rather than being silently accepted.
func resolveOpenBackend(cfg *configfile.Config) openBackend {
	if cfg == nil {
		return openBackendEmbedded
	}
	if cfg.IsDoltServerMode() || cfg.IsDoltProxiedServerMode() {
		return openBackendServer
	}
	return openBackendEmbedded
}
