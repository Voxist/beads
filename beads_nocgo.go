//go:build !cgo

package beads

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// OpenBestAvailable opens a beads database using the best available backend
// for the given .beads directory. In non-CGO builds, only server-backed modes
// are supported; embedded mode returns an error directing the user to server
// mode.
//
// Both server mode and proxied-server mode are server-backed and route through
// NewFromConfig (the proxy speaks the MySQL wire protocol). Proxied-server must
// not be treated as embedded — in a CGo build that would create a fresh,
// typeless database and yield "invalid issue type". The server-backed path
// asserts project identity on open (see dolt.New / verifyProjectIdentity),
// returning ErrStoreIdentityMismatch when the server is serving a different
// project's database.
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

	// Dispatch to a registered extension backend before any Dolt path, mirroring
	// the CLI store factories so SDK callers get the backend they registered
	// instead of the embedded-Dolt-requires-CGO error.
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
	return nil, fmt.Errorf("embedded Dolt requires CGO; use server mode (bd init --server)")
}
