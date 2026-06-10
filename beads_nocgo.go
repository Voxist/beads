//go:build !cgo

package beads

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
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
// beadsDir is the path to the .beads directory.
func OpenBestAvailable(ctx context.Context, beadsDir string) (Storage, error) {
	cfg, _ := configfile.Load(beadsDir)
	if resolveOpenBackend(cfg) == openBackendServer {
		store, err := dolt.NewFromConfig(ctx, beadsDir)
		if err != nil {
			return nil, err
		}
		return store, nil
	}
	return nil, fmt.Errorf("embedded Dolt requires CGO; use server mode (bd init --server)")
}
