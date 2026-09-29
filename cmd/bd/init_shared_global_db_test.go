package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

// TestClassifySharedServerStartErrorSkipsOnAutoStartDisabled pins the
// ga-dpbbw fix to bd init's shared-global-database block. On the base
// commit, that block had NO auto-start check at all: doltserver.Start(
// sharedDir) would spawn or silently adopt an existing listener regardless
// of policy, and ANY failure (for any reason) ended bd init with exit 1.
//
// classifySharedServerStartError is the decision point that makes the block
// respect the policy instead of ignoring it: ErrAutoStartDisabled (however
// wrapped) must classify as sharedGlobalDBSkip -- STOP TRYING TO START the
// server, but still fall through to EnsureGlobalDatabase afterward (that
// call only connects, never spawns, so it is safe even when Start was
// skipped; see sharedGlobalDBSkip's own doc for why this matters for an
// externally-managed server bd's PID file doesn't know about). Any other
// error must stay fatal, matching the prior "any failure exits" behavior.
// nil must proceed as before.
func TestClassifySharedServerStartErrorSkipsOnAutoStartDisabled(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want sharedGlobalDBStartOutcome
	}{
		{
			name: "nil proceeds",
			err:  nil,
			want: sharedGlobalDBProceed,
		},
		{
			name: "bare ErrAutoStartDisabled skips",
			err:  doltserver.ErrAutoStartDisabled,
			want: sharedGlobalDBSkip,
		},
		{
			name: "wrapped ErrAutoStartDisabled skips",
			err:  fmt.Errorf("starting shared dolt server: %w", doltserver.ErrAutoStartDisabled),
			want: sharedGlobalDBSkip,
		},
		{
			name: "unrelated error is fatal",
			err:  errors.New("dolt is not installed (not found in PATH)"),
			want: sharedGlobalDBFatal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifySharedServerStartError(tt.err); got != tt.want {
				t.Errorf("classifySharedServerStartError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestInitSharedGlobalDatabase pins the three cases named in ga-dpbbw's
// review that classifySharedServerStartError alone does not cover, because
// it only tests the classification, not what the CALLER does with it. Before
// this test existed, re-adding a skip or an early return anywhere in
// initSharedGlobalDatabase's switch (the exact shape of the bug this whole
// PR fixes in bd init) kept every other test in this package green -- these
// assert on ensureGlobal actually being invoked or not, and on fatalErr,
// which classifySharedServerStartError's own return value cannot show.
func TestInitSharedGlobalDatabase(t *testing.T) {
	sentinelGlobalErr := errors.New("dial tcp: connection refused")
	sentinelFatalErr := errors.New("dolt is not installed (not found in PATH)")

	t.Run("ErrAutoStartDisabled, reachable: ensureGlobal called, succeeds, no fatal error", func(t *testing.T) {
		var globalCalls int
		result := initSharedGlobalDatabase(false,
			func() (*doltserver.State, error) { return nil, doltserver.ErrAutoStartDisabled },
			func() error { globalCalls++; return nil },
		)
		if globalCalls != 1 {
			t.Errorf("ensureGlobal called %d times, want 1", globalCalls)
		}
		if !result.globalCalled {
			t.Error("result.globalCalled = false, want true")
		}
		if result.globalErr != nil {
			t.Errorf("result.globalErr = %v, want nil", result.globalErr)
		}
		if result.fatalErr != nil {
			t.Errorf("result.fatalErr = %v, want nil", result.fatalErr)
		}
		if result.startOutcome != sharedGlobalDBSkip {
			t.Errorf("result.startOutcome = %v, want sharedGlobalDBSkip", result.startOutcome)
		}
	})

	t.Run("ErrAutoStartDisabled, unreachable: ensureGlobal called, warning only, no fatal error", func(t *testing.T) {
		var globalCalls int
		result := initSharedGlobalDatabase(false,
			func() (*doltserver.State, error) { return nil, doltserver.ErrAutoStartDisabled },
			func() error { globalCalls++; return sentinelGlobalErr },
		)
		if globalCalls != 1 {
			t.Errorf("ensureGlobal called %d times, want 1", globalCalls)
		}
		if !result.globalCalled {
			t.Error("result.globalCalled = false, want true")
		}
		if !errors.Is(result.globalErr, sentinelGlobalErr) {
			t.Errorf("result.globalErr = %v, want %v", result.globalErr, sentinelGlobalErr)
		}
		if result.fatalErr != nil {
			t.Errorf("result.fatalErr = %v, want nil -- an unreachable server must warn, not fail bd init", result.fatalErr)
		}
	})

	t.Run("other start error: fatal, ensureGlobal NOT called", func(t *testing.T) {
		var globalCalls int
		result := initSharedGlobalDatabase(false,
			func() (*doltserver.State, error) { return nil, sentinelFatalErr },
			func() error { globalCalls++; return nil },
		)
		if globalCalls != 0 {
			t.Errorf("ensureGlobal called %d times, want 0 -- must not run after a genuine Start failure", globalCalls)
		}
		if result.globalCalled {
			t.Error("result.globalCalled = true, want false")
		}
		if !errors.Is(result.fatalErr, sentinelFatalErr) {
			t.Errorf("result.fatalErr = %v, want %v", result.fatalErr, sentinelFatalErr)
		}
		if result.startOutcome != sharedGlobalDBFatal {
			t.Errorf("result.startOutcome = %v, want sharedGlobalDBFatal", result.startOutcome)
		}
	})

	t.Run("nil start error: proceeds, ensureGlobal called", func(t *testing.T) {
		var startCalls, globalCalls int
		result := initSharedGlobalDatabase(false,
			func() (*doltserver.State, error) { startCalls++; return &doltserver.State{Running: true}, nil },
			func() error { globalCalls++; return nil },
		)
		if startCalls != 1 {
			t.Errorf("start called %d times, want 1", startCalls)
		}
		if globalCalls != 1 {
			t.Errorf("ensureGlobal called %d times, want 1", globalCalls)
		}
		if result.startOutcome != sharedGlobalDBProceed {
			t.Errorf("result.startOutcome = %v, want sharedGlobalDBProceed", result.startOutcome)
		}
		if result.fatalErr != nil {
			t.Errorf("result.fatalErr = %v, want nil", result.fatalErr)
		}
	})

	t.Run("alreadyRunning: start never called, ensureGlobal still called", func(t *testing.T) {
		var startCalls, globalCalls int
		result := initSharedGlobalDatabase(true,
			func() (*doltserver.State, error) { startCalls++; return nil, errors.New("must not be called") },
			func() error { globalCalls++; return nil },
		)
		if startCalls != 0 {
			t.Errorf("start called %d times, want 0 -- alreadyRunning must skip it entirely", startCalls)
		}
		if globalCalls != 1 {
			t.Errorf("ensureGlobal called %d times, want 1", globalCalls)
		}
		if !result.globalCalled {
			t.Error("result.globalCalled = false, want true")
		}
	})

	// alreadyRunning=true with a NIL start func is the exact call shape bd
	// init's caller uses when doltserver.SharedServerDir() itself fails
	// (ga-dpbbw L2): there is no sharedDir to build a Start closure from, so
	// alreadyRunning is forced to true specifically so start is never
	// dereferenced. On base, EnsureGlobalDatabase ran unconditionally even
	// when SharedServerDir() failed -- it never took sharedDir as a
	// parameter -- so this proves the fix doesn't reintroduce that silent
	// skip via a different route (a nil-start panic would be just as much a
	// regression as an early return).
	t.Run("alreadyRunning with nil start: does not panic, ensureGlobal still called", func(t *testing.T) {
		var globalCalls int
		result := initSharedGlobalDatabase(true, nil, func() error { globalCalls++; return nil })
		if globalCalls != 1 {
			t.Errorf("ensureGlobal called %d times, want 1", globalCalls)
		}
		if !result.globalCalled {
			t.Error("result.globalCalled = false, want true")
		}
		if result.fatalErr != nil {
			t.Errorf("result.fatalErr = %v, want nil", result.fatalErr)
		}
	})
}
