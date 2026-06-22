package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
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
