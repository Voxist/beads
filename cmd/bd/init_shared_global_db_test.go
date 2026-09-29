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
