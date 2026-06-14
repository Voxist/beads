package dolt

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestNewProjectIdentityMismatchError verifies that the identity-mismatch
// diagnostic wraps storage.ErrStoreIdentityMismatch so automated fallback paths
// can detect it with errors.Is, and that it never silently degrades. This is a
// pure unit test — no Dolt server required.
func TestNewProjectIdentityMismatchError(t *testing.T) {
	tests := []struct {
		name      string
		localID   string
		dbID      string
		global    bool
		wantInMsg []string
	}{
		{
			name:      "local mismatch wraps sentinel and names both IDs",
			localID:   "local-uuid-aaa",
			dbID:      "db-uuid-bbb",
			global:    false,
			wantInMsg: []string{"local-uuid-aaa", "db-uuid-bbb", "refusing to connect"},
		},
		{
			name:      "global mismatch wraps sentinel and names both IDs",
			localID:   "global-uuid-ccc",
			dbID:      "db-uuid-ddd",
			global:    true,
			wantInMsg: []string{"global-uuid-ccc", "db-uuid-ddd", "global"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := newProjectIdentityMismatchError(tt.localID, tt.dbID, tt.global)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !errors.Is(err, storage.ErrStoreIdentityMismatch) {
				t.Errorf("error does not wrap storage.ErrStoreIdentityMismatch: %v", err)
			}
			for _, want := range tt.wantInMsg {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error message missing %q: %v", want, err)
				}
			}
		})
	}
}
