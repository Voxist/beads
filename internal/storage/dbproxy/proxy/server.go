package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v4"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
)

type ProxyOpts struct {
	RootDir     string
	Port        int
	IdleTimeout time.Duration
	Server      server.DatabaseServer
	// Stats is optional. When non-nil, the proxy records per-event counters
	// against it; tests use Snapshot() to assert. Production code should
	// leave this nil.
	Stats *Stats

	// PoolSize enables connection pooling when > 0: instead of dialing a fresh
	// backend per client and closing it on disconnect (the transparent path),
	// the proxy terminates each client handshake itself and lends a backend
	// from a pool of up to PoolSize warm, already-authenticated connections,
	// resetting and returning them on client disconnect. This collapses the
	// per-bd-invocation connection churn that dolt pays for as setup CPU.
	// 0 (default) preserves the original transparent forwarding behavior.
	PoolSize int
	// BackendUser / BackendPassword are the credentials the proxy uses to
	// authenticate pooled backend connections to dolt. Only consulted when
	// PoolSize > 0. For the managed loopback server this is root / empty.
	BackendUser     string
	BackendPassword string
	// PoolConnMaxLifetime optionally retires pooled connections after this
	// duration. 0 (default) keeps them indefinitely — a short lifetime would
	// re-create the very connection churn pooling exists to eliminate.
	PoolConnMaxLifetime time.Duration
	// Debug enables per-connection trace lines (accepted, handleConn start/end,
	// dial ok, copy done). Default false — these lines are the dominant write
	// source under fleet churn and drive the macOS FSEvents/Spotlight storm.
	Debug bool
}

type proxyServer struct {
	rootDir         string
	port            int
	idleTimeout     time.Duration
	server          server.DatabaseServer
	stats           *Stats
	poolSize        int
	backendUser     string
	backendPassword string
	poolLifetime    time.Duration
	debug           bool

	logger      *log.Logger
	listener    net.Listener
	pool        *backendPool
	connID      atomic.Uint32
	activeConns atomic.Int64
	conns       errgroup.Group

	// S3d: rate-limit logging of benign client disconnects. bd clients connect,
	// run one command, and disconnect; logging each teardown unconditionally
	// produced the proxy.log flood (1 GB in the incident). disconnectLogLimiter
	// caps those log lines; droppedDisconnectLogs counts suppressed lines so the
	// next emitted line can report the gap (no silent loss).
	disconnectLogLimiter  *rate.Limiter
	droppedDisconnectLogs atomic.Int64
}

const (
	PIDFileName  = "proxy.pid"
	LogFileName  = "proxy.log"
	LockFileName = "proxy.lock"
)

// LockHeldExitCode is the exit code a child proxy should use when
// ListenAndServe returns ErrLockHeld. The spawning parent treats this
// (EX_TEMPFAIL) as "lost the spawn race" and retries via readAndDial.
const LockHeldExitCode = 75

// ErrLockHeld is returned from ListenAndServe when another proxy already
// holds proxy.lock for the same rootDir. It is a normal "lost the race"
// outcome, not a failure: callers spawned as children should map it to
// LockHeldExitCode and exit cleanly.
var ErrLockHeld = errors.New("proxy lock held by another proxy on this rootDir")

const (
	serverReadyTimeout     = 30 * time.Second
	readyDialTimeout       = 2 * time.Second
	readyInitialBackoff    = 50 * time.Millisecond
	readyMaxBackoff        = 1 * time.Second
	idleWatcherMinInterval = 1 * time.Second
	backendStopTimeout     = 10 * time.Second
	tcpKeepAlivePeriod     = 30 * time.Second
)

var errIdleTimeout = errors.New("idle timeout reached")

func NewProxyServer(opts ProxyOpts) *proxyServer {
	return &proxyServer{
		rootDir:         opts.RootDir,
		port:            opts.Port,
		idleTimeout:     opts.IdleTimeout,
		server:          opts.Server,
		stats:           opts.Stats,
		poolSize:        opts.PoolSize,
		backendUser:     opts.BackendUser,
		backendPassword: opts.BackendPassword,
		poolLifetime:    opts.PoolConnMaxLifetime,
		debug:           opts.Debug,
		// At most one benign-disconnect log line per second, burst 1.
		disconnectLogLimiter: rate.NewLimiter(rate.Every(time.Second), 1),
	}
}

// isExpectedClientDisconnect reports whether err is a benign client teardown
// (clean EOF — including the wrapped "read handshake response: EOF" — a closed
// connection, a canceled context, or a read/write timeout) rather than a real
// proxy fault. These are normal for short-lived bd clients and must not be
// logged at full volume.
func isExpectedClientDisconnect(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return false
}

// logSessionError logs a handlePooledConn error, rate-limiting benign client
// disconnects (S3d) while always logging genuine faults. Suppressed benign
// lines are counted and surfaced on the next emitted benign line.
func (p *proxyServer) logSessionError(remote net.Addr, err error) {
	if !isExpectedClientDisconnect(err) {
		p.tracef("handlePooledConn(%s) session error: %v", remote, err)
		return
	}
	if p.disconnectLogLimiter == nil || p.disconnectLogLimiter.Allow() {
		if dropped := p.droppedDisconnectLogs.Swap(0); dropped > 0 {
			p.tracef("handlePooledConn(%s) client disconnected: %v (+%d similar suppressed)", remote, err, dropped)
		} else {
			p.tracef("handlePooledConn(%s) client disconnected: %v", remote, err)
		}
		return
	}
	p.droppedDisconnectLogs.Add(1)
}

func (p *proxyServer) tracef(format string, args ...any) {
	p.logger.Printf(format, args...)
}

// debugf logs only when Debug is enabled. Use for per-connection events that
// are chatty under fleet churn (accepted, handleConn, copy done).
func (p *proxyServer) debugf(format string, args ...any) {
	if p.debug {
		p.logger.Printf(format, args...)
	}
}

func (p *proxyServer) ListenAndServe(parentCtx context.Context) error {
	lock, err := util.TryLock(filepath.Join(p.rootDir, LockFileName))
	if err != nil {
		if lockfile.IsLocked(err) {
			return ErrLockHeld
		}
		return fmt.Errorf("acquire %s: %w", LockFileName, err)
	}
	defer lock.Unlock()

	logPath := filepath.Join(p.rootDir, LogFileName)
	lj := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    50, // MB per file before rotation
		MaxBackups: 3,
		LocalTime:  true,
	}
	p.logger = log.New(lj, "[proxy] ", log.LstdFlags|log.Lmicroseconds)
	defer func() { _ = lj.Close() }()

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	// Install signal handlers BEFORE Listen. Without this, Go's default
	// SIGTERM action terminates the process during the startup window
	// (Listen, pidfile write, backend Start, readiness wait), bypassing all
	// deferred cleanup including RemoveDatabaseProxyPidFile.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	var sigReceived atomic.Bool
	go func() {
		select {
		case <-ctx.Done():
		case <-sigCh:
			sigReceived.Store(true)
			p.stats.IncSignalReceived()
			cancel()
		}
	}()

	addr := fmt.Sprintf("127.0.0.1:%d", p.port)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	p.listener = ln
	defer func() { _ = ln.Close() }()
	p.stats.IncListenAndServe()

	p.stats.IncBackendStart()
	if err := p.server.Start(ctx); err != nil {
		return fmt.Errorf("start database server: %w", err)
	}

	if err := waitForServerReady(ctx, p.server, serverReadyTimeout); err != nil {
		p.stats.IncBackendStop()
		_ = stopBackendBounded(p.server)
		return fmt.Errorf("database server not ready: %w", err)
	}

	if err := pidfile.Write(p.rootDir, PIDFileName, pidfile.PidFile{
		Pid:        os.Getpid(),
		Port:       p.port,
		UpstreamID: p.server.ID(ctx),
	}); err != nil {
		p.stats.IncBackendStop()
		_ = stopBackendBounded(p.server)
		return fmt.Errorf("write pid file: %w", err)
	}
	defer func() { _ = pidfile.Remove(p.rootDir, PIDFileName) }()

	if p.poolSize > 0 {
		user := p.backendUser
		if user == "" {
			user = "root"
		}
		p.pool = newBackendPool(
			func(dctx context.Context) (net.Conn, error) { return p.server.Dial(dctx) },
			user, p.backendPassword, p.poolSize, p.poolLifetime, p.stats,
		)
		p.tracef("connection pooling enabled (maxIdle=%d, user=%q, lifetime=%s)", p.poolSize, user, p.poolLifetime)
		defer p.pool.drain()

		// S3a (v2, primary anti-wedge): cap the number of concurrently-handled
		// client connections to poolSize. errgroup.SetLimit makes acceptLoop's
		// p.conns.Go block (kernel backlog absorbs the queue) rather than
		// spawning an unbounded set of handlers, each of which would dial or
		// borrow a backend connection. This is what bounds live backend
		// connections per proxy; with poolSize=2 an N-scope city peaks at
		// N×(poolSize+1) backend connections. Only set when pooling is enabled:
		// SetLimit(0) would block every handler and wedge the non-pooled path.
		p.conns.SetLimit(p.poolSize)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		<-gctx.Done()
		_ = p.listener.Close()
		return nil
	})
	g.Go(func() error { return p.idleWatcher(gctx) })
	g.Go(func() error { return p.acceptLoop(gctx) })

	runErr := g.Wait()
	_ = p.conns.Wait()
	p.stats.IncBackendStop()
	if stopErr := stopBackendBounded(p.server); stopErr != nil && runErr == nil {
		runErr = fmt.Errorf("stop database server: %w", stopErr)
	}
	if errors.Is(runErr, errIdleTimeout) || sigReceived.Load() {
		return nil
	}
	return runErr
}

func stopBackendBounded(s server.DatabaseServer) error {
	ctx, cancel := context.WithTimeout(context.Background(), backendStopTimeout)
	defer cancel()
	return s.Stop(ctx)
}

func (p *proxyServer) idleWatcher(ctx context.Context) error {
	if p.idleTimeout <= 0 {
		<-ctx.Done()
		return nil
	}
	interval := p.idleTimeout / 4
	if interval < idleWatcherMinInterval {
		interval = idleWatcherMinInterval
	}
	p.tracef("idleWatcher start (timeout=%s, tick=%s)", p.idleTimeout, interval)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	var idleSince time.Time
	for {
		select {
		case <-ctx.Done():
			p.tracef("idleWatcher exit (ctx done)")
			return nil
		case <-tick.C:
			if n := p.activeConns.Load(); n > 0 {
				if !idleSince.IsZero() {
					p.tracef("idleWatcher cleared (active=%d)", n)
					idleSince = time.Time{}
				}
				continue
			}
			if idleSince.IsZero() {
				p.tracef("idleWatcher armed")
				idleSince = time.Now()
				continue
			}
			if time.Since(idleSince) >= p.idleTimeout {
				p.tracef("idleWatcher expired after %s, shutting down", p.idleTimeout)
				p.stats.IncIdleTimeout()
				return errIdleTimeout
			}
		}
	}
}

func (p *proxyServer) acceptLoop(ctx context.Context) error {
	p.tracef("acceptLoop start (addr=%s)", p.listener.Addr())
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				p.tracef("acceptLoop exit (ctx=%v)", ctx.Err())
				return nil
			}
			// Surface non-shutdown accept errors to the errgroup so the
			// proxy fails fast instead of busy-looping. Specific errors that
			// warrant retry (e.g. transient EMFILE under load) can be added
			// here as the need arises.
			p.tracef("acceptLoop error: %v", err)
			p.stats.IncAcceptError()
			return fmt.Errorf("accept: %w", err)
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetKeepAlive(true)
			_ = tc.SetKeepAlivePeriod(tcpKeepAlivePeriod)
		}
		p.debugf("acceptLoop accepted (remote=%s)", conn.RemoteAddr())
		p.stats.IncAccept()
		p.conns.Go(func() error {
			return p.handleConn(ctx, conn)
		})
	}
}

func (p *proxyServer) handleConn(ctx context.Context, client net.Conn) error {
	addr := client.RemoteAddr()
	p.debugf("handleConn(%s) start", addr)
	p.activeConns.Add(1)
	defer func() {
		p.activeConns.Add(-1)
		p.debugf("handleConn(%s) end (active=%d)", addr, p.activeConns.Load())
	}()

	if p.pool != nil {
		return p.handlePooledConn(ctx, client)
	}

	p.stats.IncBackendDialAttempt()
	dialDone := p.stats.DialBegin() // S3f: concurrent-dial peak gauge
	backend, err := p.server.Dial(ctx)
	dialDone()
	if err != nil {
		p.tracef("handleConn(%s) backend dial error: %v", addr, err)
		p.stats.IncBackendDialError()
		_ = client.Close()
		return err
	}
	p.debugf("handleConn(%s) backend dial ok", addr)
	p.stats.IncBackendDialSuccess()
	p.stats.IncHandledConn()

	done := make(chan struct{})
	var doneOnce sync.Once
	finish := func() { doneOnce.Do(func() { close(done) }) }

	var g errgroup.Group
	g.Go(func() error {
		select {
		case <-ctx.Done():
			p.tracef("handleConn(%s) ctx canceled, force-closing", addr)
			_ = client.Close()
			_ = backend.Close()
		case <-done:
		}
		return nil
	})
	g.Go(func() error {
		defer finish()
		defer func() { _ = backend.Close() }()
		defer func() { _ = client.Close() }()
		n, err := io.Copy(backend, client)
		p.stats.AddBytesClientToBackend(n)
		p.debugf("handleConn(%s) client→backend done (n=%d, err=%v)", addr, n, err)
		return err
	})
	g.Go(func() error {
		defer finish()
		defer func() { _ = backend.Close() }()
		defer func() { _ = client.Close() }()
		n, err := io.Copy(client, backend)
		p.stats.AddBytesBackendToClient(n)
		p.debugf("handleConn(%s) backend→client done (n=%d, err=%v)", addr, n, err)
		return err
	})
	return g.Wait()
}

// handlePooledConn serves one client connection in pooling mode: terminate the
// client handshake, borrow a wire-compatible backend from the pool, forward the
// command phase byte-transparently, and reset+return the backend on disconnect.
func (p *proxyServer) handlePooledConn(ctx context.Context, client net.Conn) error {
	defer func() { _ = client.Close() }()
	p.stats.IncHandledConn()
	res, err := runPooledSession(ctx, p.stats, p.pool, client, p.connID.Add(1))
	if err != nil {
		p.logSessionError(client.RemoteAddr(), err)
		if res.backend != nil {
			_ = res.backend.conn.Close()
		}
		return err
	}
	if res.backend == nil {
		return nil
	}
	if res.reusable {
		p.pool.put(res.backend)
	} else {
		_ = res.backend.conn.Close()
		p.stats.IncPoolRetire()
	}
	return nil
}

func waitForServerReady(ctx context.Context, s server.DatabaseServer, timeout time.Duration) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = readyInitialBackoff
	bo.MaxInterval = readyMaxBackoff
	bo.MaxElapsedTime = timeout

	return backoff.Retry(func() error {
		if !s.Running(ctx) {
			return errors.New("database server not running")
		}
		dialCtx, cancel := context.WithTimeout(ctx, readyDialTimeout)
		defer cancel()
		conn, err := s.Dial(dialCtx)
		if err != nil {
			return err
		}
		_ = conn.Close()
		return nil
	}, backoff.WithContext(bo, ctx))
}
