package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

// TestClassifySharedServerStartErrorSkipsOnAutoStartDisabled pins the
// ga-dpbbw fix to bd init's shared-global-database block: before this fix,
// the block skipped only the doltserver.Start call when auto-start was
// disabled, then went on to call EnsureGlobalDatabase against the server it
// had just declined to start -- producing a misleading
// "failed to create global database" warning instead of a clean skip.
//
// classifySharedServerStartError is the decision point that closes that gap:
// ErrAutoStartDisabled (however wrapped) must classify as "skip the whole
// block," any other error must stay fatal (matching prior behavior), and nil
// must proceed as before.
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
