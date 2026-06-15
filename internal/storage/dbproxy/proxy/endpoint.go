package proxy

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
)

type ErrUpstreamMismatch struct {
	RootDir string
	Want    string
	Have    string
}

func (e *ErrUpstreamMismatch) Error() string {
	return fmt.Sprintf("proxy at %s fronts upstream %s, not %s", e.RootDir, e.Have, e.Want)
}

func IsUpstreamMismatch(err error) bool {
	var m *ErrUpstreamMismatch
	return errors.As(err, &m)
}

func intendedUpstreamID(rootDir string, opts OpenOpts) string {
	switch opts.Backend {
	case BackendExternal:
		return server.ExternalDoltServerID(opts.External)
	case BackendLocalServer:
		return server.LocalDoltServerID(rootDir)
	}
	return ""
}

func checkUpstream(rootDir, want string, pf *pidfile.PidFile) error {
	if want != "" && pf.UpstreamID != "" && pf.UpstreamID != want {
		return &ErrUpstreamMismatch{
			RootDir: rootDir,
			Want:    want,
			Have:    pf.UpstreamID,
		}
	}
	return nil
}

type Endpoint struct {
	Host string
	Port int
}

func (e Endpoint) Address() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

type OpenOpts struct {
	IdleTimeout    time.Duration
	Backend        Backend
	ConfigFilePath string
	LogFilePath    string
	DoltBinPath    string
	External       configfile.ExternalDoltConfig
	// PoolSize, when > 0, makes the spawned proxy pool backend connections
	// (see ProxyOpts.PoolSize). BackendUser is the user the proxy uses to
	// authenticate those pooled connections; the password, when needed, is
	// inherited by the child via the environment (it is never passed on the
	// command line). 0 preserves the transparent, non-pooling proxy.
	PoolSize    int
	BackendUser string
	// PoolConnMaxLifetime, when > 0, retires pooled backend connections after
	// this duration (see ProxyOpts.PoolConnMaxLifetime). 0 (default) keeps them
	// indefinitely, which is the right choice for steady-state pooling; the knob
	// exists as an operator escape hatch (e.g. to recycle connections around a
	// backend that leaks server-side state). Only consulted when PoolSize > 0.
	PoolConnMaxLifetime time.Duration
	// Debug enables per-connection trace logging in the proxy child process.
	// Leave false in production; set true only for diagnostic runs.
	Debug bool
}

const (
	openDeadline          = 15 * time.Second
	spawnReadyHardTimeout = 2 * time.Minute
	openPollInterval      = 100 * time.Millisecond
)

var ResolveExecutable = os.Executable

// PoolSizeEnvVar is the opt-in switch for backend connection pooling. When set
// to a positive integer, a proxy spawned by this process pools up to that many
// warm backend connections instead of dialing one per client. Unset or 0
// disables pooling (transparent forwarding, the historical behavior).
const PoolSizeEnvVar = "BEADS_PROXY_POOL_SIZE"

// PoolSizeFromEnv reads PoolSizeEnvVar, returning 0 (pooling disabled) when
// unset, empty, non-numeric, or negative.
func PoolSizeFromEnv() int {
	v := strings.TrimSpace(os.Getenv(PoolSizeEnvVar))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// PoolConnMaxLifetimeEnvVar optionally retires pooled backend connections after
// the given Go duration (e.g. "30m"). Unset, empty, unparseable, or non-positive
// keeps pooled connections indefinitely — the steady-state pooling default.
// Only has effect when pooling is enabled (PoolSizeEnvVar > 0).
const PoolConnMaxLifetimeEnvVar = "BEADS_PROXY_CONN_MAX_LIFETIME"

// PoolConnMaxLifetimeFromEnv reads PoolConnMaxLifetimeEnvVar, returning 0
// (connections kept indefinitely) when unset, empty, unparseable, or non-positive.
func PoolConnMaxLifetimeFromEnv() time.Duration {
	v := strings.TrimSpace(os.Getenv(PoolConnMaxLifetimeEnvVar))
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// IdleTimeoutEnvVar overrides how long a pooling proxy stays alive with no
// active client connections before it shuts down. The default (30s) is tuned
// for a single busy workspace; an orchestrator that touches many scopes
// sparsely (e.g. an orchestrator probing dozens of scopes once per patrol) starves each
// proxy below that window, so it spawns, serves one op, idle-dies, and respawns
// on the next touch — pure churn that never reaches the warm-pool steady state
// pooling exists to provide. Raising the timeout (e.g. "10m") keeps proxies warm
// across sparse bursts. Accepts a Go duration string.
const IdleTimeoutEnvVar = "BEADS_PROXY_IDLE_TIMEOUT"

// IdleTimeoutFromEnv reads IdleTimeoutEnvVar, returning fallback when unset,
// empty, or unparseable. A parsed non-positive value (e.g. "0") is returned
// verbatim, which disables the idle timeout (proxy stays up until stopped).
func IdleTimeoutFromEnv(fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(IdleTimeoutEnvVar))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

// DebugEnvVar re-enables the proxy's per-connection trace lines (accepted,
// handleConn start/end, dial ok, copy done) for diagnostic runs. They default
// OFF: under fleet churn those lines are the dominant proxy.log write source
// (the 1.38 GB proxy.log incident) and feed the macOS FSEvents/Spotlight
// storm. Accepts strconv.ParseBool values ("1", "true", "0", "false", ...).
const DebugEnvVar = "BEADS_PROXY_DEBUG"

// DebugFromEnv reads DebugEnvVar, returning fallback when unset, empty, or
// unparseable. A parseable value wins over the fallback, so an explicit "0"
// turns debug off even when the caller defaults it on.
func DebugFromEnv(fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(DebugEnvVar))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func PickFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

func GetCreateDatabaseProxyServerEndpoint(rootDir string, opts OpenOpts) (Endpoint, error) {
	if err := opts.Backend.Validate(); err != nil {
		return Endpoint{}, fmt.Errorf("OpenOpts.Backend: %w", err)
	}
	switch opts.Backend {
	case BackendLocalServer:
		if opts.ConfigFilePath == "" {
			return Endpoint{}, fmt.Errorf("OpenOpts.ConfigFilePath is required for backend %q", opts.Backend)
		}
		if opts.LogFilePath == "" {
			return Endpoint{}, fmt.Errorf("OpenOpts.LogFilePath is required for backend %q", opts.Backend)
		}
		if opts.DoltBinPath == "" {
			return Endpoint{}, fmt.Errorf("OpenOpts.DoltBinPath is required for backend %q", opts.Backend)
		}
	case BackendExternal:
		if opts.LogFilePath == "" {
			return Endpoint{}, fmt.Errorf("OpenOpts.LogFilePath is required for backend %q", opts.Backend)
		}
		if err := opts.External.Validate(); err != nil {
			return Endpoint{}, fmt.Errorf("OpenOpts.External: %w", err)
		}
	}
	deadline := time.Now().Add(openDeadline)

	timeout := time.NewTimer(openDeadline)
	defer timeout.Stop()
	poll := time.NewTicker(openPollInterval)
	defer poll.Stop()

	want := intendedUpstreamID(rootDir, opts)

	var lastSpawnErr error
	for {
		if ep, pf, ok := readAndDial(rootDir); ok {
			if err := checkUpstream(rootDir, want, pf); err != nil {
				return Endpoint{}, err
			}
			return ep, nil
		}

		lock, err := util.TryLock(filepath.Join(rootDir, LockFileName))
		switch {
		case err == nil:
			var ep Endpoint
			if ep, lastSpawnErr = spawnAndHandoff(rootDir, opts, deadline, lock); lastSpawnErr == nil {
				return ep, nil
			}
		case !lockfile.IsLocked(err):
			return Endpoint{}, fmt.Errorf("probe proxy lock: %w", err)
		}

		select {
		case <-timeout.C:
			if lastSpawnErr != nil {
				return Endpoint{}, lastSpawnErr
			}
			return Endpoint{}, fmt.Errorf("timeout waiting for proxy on %s", rootDir)
		case <-poll.C:
		}
	}
}

func spawnAndHandoff(rootDir string, opts OpenOpts, deadline time.Time, lock *util.Lock) (Endpoint, error) {
	handedOff := false
	defer func() {
		if !handedOff {
			lock.Unlock()
		}
	}()

	// Stale pidfile from a previous (now-dead) proxy must not mislead racing
	// readers into dialing a port that nobody is listening on.
	_ = pidfile.Remove(rootDir, PIDFileName)

	// Probe the proxy-child flock. Held: a previous proxy-child is still
	// alive and has an orphaned dolt sql-server we must kill before
	// respawning. Acquired: no proxy-child survives, but a SIGKILLed one
	// leaves its dolt sql-server orphaned (the flock dies with its holder;
	// the grandchild process does not) — still holding the dolt data-dir
	// lock, which would wedge every respawn. Either way, kill whatever live
	// process the child pidfile names, then release the flock so the child
	// we are about to spawn can take it.
	if l, err := util.TryLock(filepath.Join(rootDir, server.LockFileName)); err == nil {
		reapPidfileProcess(rootDir, server.PIDFileName)
		l.Unlock()
	} else if lockfile.IsLocked(err) {
		reapPidfileProcess(rootDir, server.PIDFileName)
	}

	port, err := PickFreePort()
	if err != nil {
		return Endpoint{}, fmt.Errorf("pick port: %w", err)
	}

	handedOff = true
	cmd, done, err := forkExecChild(rootDir, opts, port, lock)
	if err != nil {
		return Endpoint{}, fmt.Errorf("fork child: %w", err)
	}

	hard := time.NewTimer(spawnReadyHardTimeout)
	defer hard.Stop()
	poll := time.NewTicker(openPollInterval)
	defer poll.Stop()

	want := intendedUpstreamID(rootDir, opts)
	for {
		if ep, pf, ok := readAndDial(rootDir); ok {
			// Our child can lose the spawn race to a proxy fronting a
			// different upstream; the winner's endpoint must fail the same
			// check the steady-state discovery path applies.
			if err := checkUpstream(rootDir, want, pf); err != nil {
				return Endpoint{}, err
			}
			return ep, nil
		}
		select {
		case <-done:
			return Endpoint{}, fmt.Errorf("proxy child on port %d exited before becoming ready (likely lost lock race)", port)
		case <-hard.C:
			_ = cmd.Process.Kill()
			return Endpoint{}, fmt.Errorf("hard timeout (%s) waiting for proxy on port %d", spawnReadyHardTimeout, port)
		case <-poll.C:
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			return Endpoint{}, fmt.Errorf("timeout waiting for proxy to become ready on port %d", port)
		}
	}
}

func forkExecChild(rootDir string, opts OpenOpts, port int, lock *util.Lock) (*exec.Cmd, <-chan struct{}, error) {
	released := false
	defer func() {
		if !released {
			lock.Unlock()
		}
	}()

	self, err := ResolveExecutable()
	if err != nil {
		return nil, nil, fmt.Errorf("locate bd executable: %w", err)
	}

	idleTimeout := opts.IdleTimeout
	if idleTimeout < 0 {
		idleTimeout = 0
	}

	args := []string{
		"db-proxy-child",
		"--root", rootDir,
		"--port", strconv.Itoa(port),
		"--idle-timeout", idleTimeout.String(),
		"--backend", string(opts.Backend),
	}
	if opts.ConfigFilePath != "" {
		args = append(args, "--config", opts.ConfigFilePath)
	}
	if opts.LogFilePath != "" {
		args = append(args, "--logpath", opts.LogFilePath)
	}
	if opts.DoltBinPath != "" {
		args = append(args, "--dolt-bin", opts.DoltBinPath)
	}
	if opts.PoolSize > 0 {
		args = append(args, "--pool-size", strconv.Itoa(opts.PoolSize))
		if opts.BackendUser != "" {
			args = append(args, "--backend-user", opts.BackendUser)
		}
		if opts.PoolConnMaxLifetime > 0 {
			args = append(args, "--pool-conn-max-lifetime", opts.PoolConnMaxLifetime.String())
		}
	}
	if opts.Debug {
		args = append(args, "--debug")
	}
	if opts.Backend == BackendExternal {
		ext := opts.External
		if ext.Host != "" {
			args = append(args, "--external-host", ext.Host)
		}
		if ext.Port != 0 {
			args = append(args, "--external-port", strconv.Itoa(ext.Port))
		}
		if ext.Socket != "" {
			args = append(args, "--external-socket-path", ext.Socket)
		}
		if ext.TLSRequired {
			args = append(args, "--external-tls")
		}
		if ext.TLSCert != "" {
			args = append(args, "--external-tls-cert-path", ext.TLSCert)
		}
		if ext.TLSKey != "" {
			args = append(args, "--external-tls-key-path", ext.TLSKey)
		}
		if ext.KeepAlivePeriod != 0 {
			args = append(args, "--external-keep-alive", ext.KeepAlivePeriod.String())
		}
	}

	logFile, err := os.OpenFile(opts.LogFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: logFilePath is caller-derived (workspace path), not user-request input
	if err != nil {
		return nil, nil, fmt.Errorf("open log file %q: %w", opts.LogFilePath, err)
	}

	cmd := exec.Command(self, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = procAttrDetached()

	released = true
	lock.Unlock()

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, nil, fmt.Errorf("start proxy child: %w", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = cmd.Wait()
		_ = logFile.Close()
	}()

	return cmd, done, nil
}

// reapConfirmDeadline bounds how long reapPidfileProcess waits for the killed
// process to disappear. A SIGKILLed dolt that is still a child of a live
// proxy-child stays a zombie until that parent reaps it, so death is awaited
// best-effort, not to certainty.
const reapConfirmDeadline = 5 * time.Second

// reapPidfileProcess kills the process the pidfile names, waits (bounded) for
// it to exit so the respawned dolt sql-server doesn't race the dying one for
// the data-dir lock, and removes the pidfile. A pidfile whose pid is already
// dead is simply stale and is removed without a kill.
func reapPidfileProcess(rootDir, pidName string) {
	pf, err := pidfile.Read(rootDir, pidName)
	if err != nil || pf == nil {
		return
	}
	if pf.Pid > 0 && pidAlive(pf.Pid) {
		if proc, ferr := os.FindProcess(pf.Pid); ferr == nil {
			_ = proc.Kill()
		}
		deadline := time.Now().Add(reapConfirmDeadline)
		for pidAlive(pf.Pid) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
	}
	_ = pidfile.Remove(rootDir, pidName)
}

func readAndDial(rootDir string) (Endpoint, *pidfile.PidFile, bool) {
	pf, err := pidfile.Read(rootDir, PIDFileName)
	if err != nil || pf == nil {
		return Endpoint{}, nil, false
	}
	// A dead writer means a stale pidfile: after port reuse an arbitrary
	// process could be listening on the recorded port, so a bare TCP probe
	// must never be trusted on the word of a dead proxy. (Stale files are
	// removed under proxy.lock in spawnAndHandoff, not here, so a racing
	// starter's freshly written pidfile can't be deleted out from under it.)
	if pf.Pid <= 0 || !pidAlive(pf.Pid) {
		return Endpoint{}, nil, false
	}
	ep := Endpoint{Host: "127.0.0.1", Port: pf.Port}
	if !probePort(ep, 500*time.Millisecond) {
		return Endpoint{}, nil, false
	}
	return ep, pf, true
}

func probePort(ep Endpoint, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", ep.Address(), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
