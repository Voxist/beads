package proxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

var errStopAfterLogCheck = errors.New("stop after the proxy.log check")

// logCheckingServer records whether proxy.log exists when the backend
// starts. Nothing logs before backend Start (the accept loop begins after
// it), so without the eager create the file is never there yet.
type logCheckingServer struct {
	*server.TestDatabaseServerImpl
	rootDir string
	statErr error
}

func (s *logCheckingServer) Start(context.Context) error {
	_, s.statErr = os.Stat(filepath.Join(s.rootDir, LogFileName))
	return errStopAfterLogCheck
}

// TestListenAndServe_CreatesProxyLogBeforeBackendStart asserts that
// proxy.log exists from startup rather than from the first log write, so a
// snapshot of the directory after startup does not depend on timing.
func TestListenAndServe_CreatesProxyLogBeforeBackendStart(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	baseline, err := readStopEpoch(root)
	require.NoError(t, err)

	backend := &logCheckingServer{TestDatabaseServerImpl: server.New(), rootDir: root}
	p := NewProxyServer(ProxyOpts{
		RootDir:   root,
		Port:      0,
		Server:    backend,
		StopEpoch: baseline,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = p.ListenAndServe(ctx)
	require.ErrorIs(t, err, errStopAfterLogCheck)
	require.NoError(t, backend.statErr, "proxy.log must exist before the backend starts")
}
