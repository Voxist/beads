package uow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

func NewExternalDoltServerUOWProvider(
	ctx context.Context,
	serverRootDir string,
	database string,
	serverLogFilePath string,
	external configfile.ExternalDoltConfig,
	rootUser string,
	rootPassword string,
) (UnitOfWorkProvider, error) {
	if database == "" {
		return nil, fmt.Errorf("uow: database name must not be empty (caller should default to %q)", "beads")
	}
	if rootUser == "" {
		return nil, fmt.Errorf("uow: rootUser must not be empty")
	}
	if err := external.Validate(); err != nil {
		return nil, fmt.Errorf("uow: external: %w", err)
	}

	absServerRootDir, err := filepath.Abs(serverRootDir)
	if err != nil {
		return nil, fmt.Errorf("uow: resolving server root dir: %w", err)
	}

	if err := os.MkdirAll(absServerRootDir, config.BeadsDirPerm); err != nil {
		return nil, fmt.Errorf("uow: creating server root directory: %w", err)
	}

	// Tell Spotlight never to index this directory (same rationale as the
	// local-server provider — see doltserver_provider.go).
	noindex := filepath.Join(absServerRootDir, ".metadata_never_index")
	if _, statErr := os.Stat(noindex); os.IsNotExist(statErr) {
		_ = os.WriteFile(noindex, nil, 0o444)
	}

	ep, err := proxy.GetCreateDatabaseProxyServerEndpoint(absServerRootDir, proxy.OpenOpts{
		Backend:     proxy.BackendExternal,
		LogFilePath: serverLogFilePath,
		External:    external,
		IdleTimeout: proxy.IdleTimeoutFromEnv(defaultProxyIdleTimeout),
		PoolSize:    proxy.PoolSizeFromEnv(),
		BackendUser: rootUser,
		Debug:       proxy.DebugFromEnv(false),
	})
	if err != nil {
		return nil, fmt.Errorf("uow: get proxy endpoint: %w", err)
	}

	return openAndInitSchema(ctx, ep, database, rootUser, rootPassword)
}
