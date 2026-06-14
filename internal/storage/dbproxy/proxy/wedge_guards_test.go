package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// TestDialBeginTracksConcurrentPeak verifies the S3f gauge records the
// high-water mark of simultaneous in-flight dials.
func TestDialBeginTracksConcurrentPeak(t *testing.T) {
	s := &Stats{}

	// Open three dials concurrently, then close them.
	d1 := s.DialBegin()
	d2 := s.DialBegin()
	d3 := s.DialBegin()
	if got := s.Snapshot().BackendDialConcurrentPeak; got != 3 {
		t.Fatalf("peak = %d, want 3", got)
	}
	d1()
	d2()
	d3()

	// Peak is a high-water mark: it does not decay when dials complete.
	if got := s.Snapshot().BackendDialConcurrentPeak; got != 3 {
		t.Fatalf("peak after close = %d, want 3 (high-water mark must not decay)", got)
	}

	// A later, smaller burst does not lower the peak.
	d := s.DialBegin()
	d()
	if got := s.Snapshot().BackendDialConcurrentPeak; got != 3 {
		t.Fatalf("peak after smaller burst = %d, want 3", got)
	}
}

// TestDialBeginNilStatsSafe ensures the gauge is safe on a nil *Stats (the
// proxy tolerates a nil stats sink).
func TestDialBeginNilStatsSafe(t *testing.T) {
	var s *Stats
	done := s.DialBegin()
	done() // must not panic
}

// TestIsExpectedClientDisconnect covers the S3d benign/real classification.
func TestIsExpectedClientDisconnect(t *testing.T) {
	deadlineErr := func() error {
		// A timeout net.Error, as produced by a read/write deadline.
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		_ = c1.SetReadDeadline(time.Now().Add(-time.Second))
		_, err := c1.Read(make([]byte, 1))
		return err
	}()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"eof", io.EOF, true},
		{"wrapped handshake eof", fmt.Errorf("mysqlwire: read handshake response: %w", io.EOF), true},
		{"closed conn", net.ErrClosed, true},
		{"context canceled", context.Canceled, true},
		{"deadline timeout", deadlineErr, true},
		{"real fault", fmt.Errorf("backend refused: protocol error"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isExpectedClientDisconnect(tc.err); got != tc.want {
				t.Fatalf("isExpectedClientDisconnect(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

type fakeAddr struct{ s string }

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return a.s }

// TestLogSessionErrorRateLimitsBenignDisconnects verifies that benign
// disconnects are rate-limited (S3d) while genuine faults always log, and that
// suppressed lines are counted and surfaced — never silently dropped.
func TestLogSessionErrorRateLimitsBenignDisconnects(t *testing.T) {
	var buf bytes.Buffer
	p := &proxyServer{
		logger: log.New(&buf, "", 0),
		// Burst 1, but refill effectively never during the test window so only
		// the first benign line is emitted.
		disconnectLogLimiter: rate.NewLimiter(rate.Every(time.Hour), 1),
	}
	addr := fakeAddr{s: "127.0.0.1:54321"}

	// 100 benign disconnects: only the first should log.
	for i := 0; i < 100; i++ {
		p.logSessionError(addr, io.EOF)
	}
	got := buf.String()
	if n := strings.Count(got, "client disconnected"); n != 1 {
		t.Fatalf("benign disconnect logged %d times, want 1 (rate-limited)", n)
	}

	// A genuine fault is never rate-limited.
	buf.Reset()
	p.logSessionError(addr, fmt.Errorf("backend protocol error"))
	if !strings.Contains(buf.String(), "session error") {
		t.Fatalf("genuine fault was not logged: %q", buf.String())
	}

	// The suppressed count is surfaced on the next allowed benign line.
	buf.Reset()
	p.disconnectLogLimiter.SetLimit(rate.Inf) // allow the next benign line
	p.logSessionError(addr, io.EOF)
	if !strings.Contains(buf.String(), "suppressed") {
		t.Fatalf("suppressed-count not surfaced: %q", buf.String())
	}
}

// TestLogSessionErrorConcurrent is a race-detector smoke test for concurrent
// benign disconnects (the real proxy logs from many handler goroutines).
func TestLogSessionErrorConcurrent(t *testing.T) {
	p := &proxyServer{
		logger:               log.New(io.Discard, "", 0),
		disconnectLogLimiter: rate.NewLimiter(rate.Every(time.Millisecond), 1),
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.logSessionError(fakeAddr{s: "127.0.0.1:1"}, io.EOF)
		}()
	}
	wg.Wait()
}

// TestProxyLogWriterBounded guards the production rotation policy for
// proxy.log (incident 8: an unbounded proxy.log grew to 1.38 GB). The writer
// must cap the live file at proxyLogMaxSizeMB, keep at most
// proxyLogMaxBackups backups, and compress them.
func TestProxyLogWriterBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), LogFileName)
	w := newProxyLogWriter(path)
	defer func() { _ = w.Close() }()

	if w.Filename != path {
		t.Fatalf("Filename = %q, want %q", w.Filename, path)
	}
	if w.MaxSize != 50 {
		t.Fatalf("MaxSize = %d MB, want 50 (size cap is the incident-8 guard)", w.MaxSize)
	}
	if w.MaxBackups != 3 {
		t.Fatalf("MaxBackups = %d, want 3", w.MaxBackups)
	}
	if !w.Compress {
		t.Fatal("Compress = false, want true (backups must be gzipped)")
	}
}

// TestProxyLogWriterRotates exercises rotation end-to-end through the
// production writer: once the live file exceeds the size cap it is rotated
// to a backup and the live file starts over, so proxy.log can never grow
// without bound. The cap is lowered to 1 MB (lumberjack's minimum unit) to
// keep the test fast; the policy values themselves are guarded by
// TestProxyLogWriterBounded.
func TestProxyLogWriterRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LogFileName)
	w := newProxyLogWriter(path)
	w.MaxSize = 1 // MB; test seam — lumberjack exposes the field
	defer func() { _ = w.Close() }()

	line := bytes.Repeat([]byte("x"), 64*1024-1)
	line = append(line, '\n')
	// 3 MB of writes against a 1 MB cap forces at least one rotation.
	for i := 0; i < 48; i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat live log: %v", err)
	}
	if fi.Size() > 2<<20 {
		t.Fatalf("live log = %d bytes, want bounded near the 1 MB cap (rotation did not happen)", fi.Size())
	}

	// Backups land beside the live file as proxy-<timestamp>.log, then are
	// compressed to .gz in the background; accept either form.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	backups := 0
	for _, e := range entries {
		if e.Name() != LogFileName && strings.HasPrefix(e.Name(), "proxy-") {
			backups++
		}
	}
	if backups == 0 {
		t.Fatalf("no backup files after writing 3 MB against a 1 MB cap; dir entries: %v", entries)
	}
}

// TestDebugfGatedOffByDefault guards the trace throttle (vp-rnq0): the
// per-connection debugf lines are the dominant proxy.log write source under
// fleet churn and must stay silent unless Debug is explicitly enabled, while
// tracef (lifecycle/error lines) always writes.
func TestDebugfGatedOffByDefault(t *testing.T) {
	var buf bytes.Buffer
	p := &proxyServer{logger: log.New(&buf, "", 0)}

	p.debugf("chatty per-conn line")
	if buf.Len() != 0 {
		t.Fatalf("debugf wrote with Debug=false: %q", buf.String())
	}
	p.tracef("lifecycle line")
	if !strings.Contains(buf.String(), "lifecycle line") {
		t.Fatalf("tracef must always write, got %q", buf.String())
	}

	buf.Reset()
	p.debug = true
	p.debugf("chatty per-conn line")
	if !strings.Contains(buf.String(), "chatty per-conn line") {
		t.Fatalf("debugf must write with Debug=true, got %q", buf.String())
	}
}
