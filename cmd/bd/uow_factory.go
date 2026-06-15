package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// newProxiedServerRoutedStore builds a server-mode DoltStorage that connects
// through the already-running db-proxy (the same proxy newProxiedServerUOWProvider
// ensured). Proxied-server mode historically only wired the uow provider, which
// covers `bd create` but leaves the legacy store-based commands (list, ready,
// stats, update, close, ...) hitting a nil store. Routing a normal server-mode
// store at the proxy endpoint makes every store-based command work in proxied
// mode, and — crucially — those connections flow through the proxy's backend
// pool just like the uow provider's.
//
// The proxy terminates the client handshake with skip-auth, so the user and
// password sent here are irrelevant; root/empty is used. The real backend
// credentials live in the proxy child (managed) or its inherited env (external).
func newProxiedServerRoutedStore(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	if beadsDir == "" {
		return nil, fmt.Errorf("newProxiedServerRoutedStore: beadsDir must be set")
	}
	rootPath, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerRoutedStore: resolve root path: %w", err)
	}
	pf, err := pidfile.Read(rootPath, proxy.PIDFileName)
	if err != nil || pf == nil || pf.Port == 0 {
		return nil, fmt.Errorf("newProxiedServerRoutedStore: read proxy endpoint from %s: %w", rootPath, err)
	}

	persisted, _ := configfile.Load(beadsDir)
	database := configfile.DefaultDoltDatabase
	if persisted != nil {
		database = persisted.GetDoltDatabase()
	}

	cfg := &dolt.Config{
		Path:       beadsDir,
		BeadsDir:   beadsDir,
		Database:   database,
		ServerMode: true,
		ServerHost: "127.0.0.1",
		ServerPort: pf.Port,
		ServerUser: "root",
		// AutoStart stays false: the proxy owns the backend lifecycle. We must
		// never try to spawn a dolt server for the proxy's listener port.
		//
		// RoutedThroughProxy stops newServerMode from persisting pf.Port (the
		// ephemeral proxy listener) into .beads/dolt-server.port. That file is
		// the canonical-server discovery hint; a proxy port written there is
		// clobbered on the next reconcile and goes stale when the proxy respawns
		// on a new port, breaking endpoint resolution for any reader that trusts
		// it.
		RoutedThroughProxy: true,
	}
	return dolt.New(ctx, cfg)
}

func newProxiedServerUOWProvider(ctx context.Context, beadsDir string) (uow.UnitOfWorkProvider, error) {
	if beadsDir == "" {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: beadsDir must be set")
	}

	// Both loads return (nil, nil) when the file is simply absent; a non-nil
	// error means the file EXISTS but cannot be read or parsed. Swallowing
	// either silently falls back to a fresh managed local database — reads
	// return zero issues and writes land in the wrong database (split-brain,
	// bd-6dnrw.44 item 6) — so refuse to proceed instead.
	persisted, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: workspace config in %s is unreadable; fix or remove it rather than letting bd guess the database: %w", beadsDir, err)
	}
	database := configfile.DefaultDoltDatabase
	if persisted != nil {
		database = persisted.GetDoltDatabase()
	}

	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: %s in %s is unreadable; refusing to fall back to a fresh managed database (fix or remove the file): %w", configfile.ProxiedServerClientInfoFileName, beadsDir, err)
	}
	if info != nil && info.External != nil {
		return newExternalProxiedServerUOWProvider(ctx, beadsDir, database, info.External)
	}

	return newManagedProxiedServerUOWProvider(ctx, beadsDir, database)
}

func newExternalProxiedServerUOWProvider(
	ctx context.Context,
	beadsDir, database string,
	external *configfile.ExternalDoltConfig,
) (uow.UnitOfWorkProvider, error) {
	rootPath, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: resolve root path: %w", err)
	}
	if err := validateProxiedServerRootPath(rootPath); err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: proxied server root (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
	}

	logPath, isCustomLog, err := resolveProxiedServerLogPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: resolve log path: %w", err)
	}
	if isCustomLog {
		if err := validateProxiedServerLogPath(logPath); err != nil {
			return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: proxied server log (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
		}
	}

	if err := os.MkdirAll(rootPath, config.BeadsDirPerm); err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: mkdir %s: %w", rootPath, err)
	}

	return uow.NewExternalDoltServerUOWProvider(
		ctx,
		rootPath,
		database,
		logPath,
		*external,
		external.ResolvedUser(),
		os.Getenv(configfile.ExternalDoltPasswordEnvVar),
	)
}

func newManagedProxiedServerUOWProvider(
	ctx context.Context,
	beadsDir, database string,
) (uow.UnitOfWorkProvider, error) {
	doltBin, err := exec.LookPath("dolt")
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: dolt is not installed (not found in PATH); install from https://docs.dolthub.com/introduction/installation: %w", err)
	}

	rootPath, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: resolve root path: %w", err)
	}
	if err := validateProxiedServerRootPath(rootPath); err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: proxied server root (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
	}

	configPath, err := ensureProxiedServerConfig(beadsDir)
	if err != nil {
		return nil, err
	}

	logPath, isCustomLog, err := resolveProxiedServerLogPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: resolve log path: %w", err)
	}
	if isCustomLog {
		if err := validateProxiedServerLogPath(logPath); err != nil {
			return nil, fmt.Errorf("newProxiedServerUOWProvider: proxied server log (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
		}
	}

	provider, err := uow.NewDoltServerUOWProvider(
		ctx,
		rootPath,
		database,
		logPath,
		configPath,
		proxy.BackendLocalServer,
		"root",
		"", // proxy is loopback-only, no auth
		doltBin,
	)
	if err != nil {
		return nil, err
	}

	// Provider warmup means the managed dolt is up, so its `dolt init` (run by
	// the proxy child in rootPath) has already happened. Seed the .bd-dolt-ok
	// compatibility marker so future bd versions don't mistake the database
	// for a pre-0.56 embedded-mode leftover. No-op when .dolt/ is absent.
	if err := doltserver.MarkDoltDirCompatible(rootPath); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to mark %s dolt-compatible: %v\n", rootPath, err)
	}

	return provider, nil
}
