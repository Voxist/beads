//go:build cgo

package beads

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// OpenBestAvailable opens a beads database using the best available backend
// for the given .beads directory. It reads metadata.json to determine the
// configured mode:
//
//   - Embedded mode (default): Opens via the CGo embedded Dolt engine.
//   - Server mode: Connects to an external dolt sql-server via NewFromConfig.
//   - Proxied-server mode: Connects to the local db-proxy via NewFromConfig.
//     Proxied-server is server-backed (the proxy speaks the MySQL wire
//     protocol), so it must NOT fall through to the embedded engine — doing so
//     creates a fresh, typeless database and yields "invalid issue type".
//
// Registered extension backends dispatch before any Dolt path, mirroring the
// CLI store factories so SDK callers get the backend they registered instead
// of a silently-opened embedded Dolt store.
//
// The server-backed path asserts project identity on open (see
// dolt.New / verifyProjectIdentity); a server serving a different project's
// database fails with ErrStoreIdentityMismatch rather than being silently
// served.
//
// The returned Storage must be closed when no longer needed.
//
// beadsDir is the path to the .beads directory.
func OpenBestAvailable(ctx context.Context, beadsDir string) (Storage, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("loading storage metadata: %w", err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	if !configfile.IsSupportedBackend(cfg.Backend) {
		return nil, configuredBackendUnavailable(cfg.Backend)
	}

	if backend, ok := backends.Lookup(cfg.GetBackend()); ok {
		return backend.Open(ctx, beadsDir)
	}

	if resolveOpenBackend(cfg) == openBackendServer {
		store, err := dolt.NewFromConfig(ctx, beadsDir)
		if err != nil {
			return nil, err
		}
		return store, nil
	}

	database := configfile.DefaultDoltDatabase
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	store, err := embeddeddolt.Open(ctx, beadsDir, database, "main")
	if err != nil {
		return nil, err
	}
	return store, nil
}
