package dolt

import (
	"context"
	"errors"
	"testing"
)

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "driver bad connection",
			err:      errors.New("driver: bad connection"),
			expected: true,
		},
		{
			name:     "Driver Bad Connection (case insensitive)",
			err:      errors.New("Driver: Bad Connection"),
			expected: true,
		},
		{
			name:     "invalid connection",
			err:      errors.New("invalid connection"),
			expected: true,
		},
		{
			name:     "broken pipe",
			err:      errors.New("write: broken pipe"),
			expected: true,
		},
		{
			name:     "connection reset",
			err:      errors.New("read: connection reset by peer"),
			expected: true,
		},
		{
			name:     "connection refused - retryable (server restart)",
			err:      errors.New("dial tcp: connection refused"),
			expected: true,
		},
		{
			name:     "database is read only - retryable",
			err:      errors.New("cannot update manifest: database is read only"),
			expected: true,
		},
		{
			name:     "Database Is Read Only (case insensitive)",
			err:      errors.New("Database Is Read Only"),
			expected: true,
		},
		{
			name:     "lost connection - retryable (MySQL error 2013)",
			err:      errors.New("Error 2013: Lost connection to MySQL server during query"),
			expected: true,
		},
		{
			name:     "server gone away - retryable (MySQL error 2006)",
			err:      errors.New("Error 2006: MySQL server has gone away"),
			expected: true,
		},
		{
			name:     "i/o timeout - retryable",
			err:      errors.New("read tcp 127.0.0.1:3307: i/o timeout"),
			expected: true,
		},
		{
			name:     "unknown database - retryable (catalog race GH-1851)",
			err:      errors.New("Error 1049 (42000): Unknown database 'beads_test'"),
			expected: true,
		},
		{
			name:     "Unknown Database (case insensitive)",
			err:      errors.New("Unknown Database 'beads_test'"),
			expected: true,
		},
		{
			name:     "no root value found in session",
			err:      errors.New("Error 1105 (HY000): no root value found in session"),
			expected: true,
		},
		{
			name:     "syntax error - not retryable",
			err:      errors.New("Error 1064: You have an error in your SQL syntax"),
			expected: false,
		},
		{
			name:     "table not found - not retryable",
			err:      errors.New("Error 1146: Table 'beads.foo' doesn't exist"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableError(tt.err)
			if got != tt.expected {
				t.Errorf("isRetryableError(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

func TestWithRetry_Success(t *testing.T) {
	store := &DoltStore{}

	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		return nil
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 call on success, got %d", callCount)
	}
}

func TestWithRetry_RetryOnBadConnection(t *testing.T) {
	store := &DoltStore{}

	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		if callCount < 3 {
			return errors.New("driver: bad connection")
		}
		return nil // Success on 3rd attempt
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls (2 retries + success), got %d", callCount)
	}
}

func TestWithRetry_RetryOnUnknownDatabase(t *testing.T) {
	// Simulates the GH-1851 race: "Unknown database" is transient after CREATE DATABASE
	store := &DoltStore{}

	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		if callCount < 3 {
			return errors.New("Error 1049 (42000): Unknown database 'beads_test'")
		}
		return nil // Catalog caught up on 3rd attempt
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls (2 retries + success), got %d", callCount)
	}
}

func TestWithRetry_NonRetryableError(t *testing.T) {
	store := &DoltStore{}

	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		return errors.New("syntax error in SQL")
	})

	if err == nil {
		t.Error("expected error, got nil")
	}
	if callCount != 1 {
		t.Errorf("expected 1 call for non-retryable error, got %d", callCount)
	}
}

func TestCommitPhaseError(t *testing.T) {
	domain := errors.New("syntax error in SQL")
	tests := []struct {
		name        string
		in          error
		wantNil     bool
		wantWrapped bool // wrapped as a non-retryable ErrCommitIndeterminate
	}{
		{name: "nil passes through", in: nil, wantNil: true},
		{name: "invalid connection -> indeterminate", in: errors.New("invalid connection"), wantWrapped: true},
		{name: "bad connection -> indeterminate", in: errors.New("driver: bad connection"), wantWrapped: true},
		{name: "broken pipe -> indeterminate", in: errors.New("write: broken pipe"), wantWrapped: true},
		{name: "domain error passes through", in: domain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := commitPhaseError(tt.in)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("commitPhaseError(nil) = %v, want nil", got)
				}
				return
			}
			if tt.wantWrapped {
				if !errors.Is(got, ErrCommitIndeterminate) {
					t.Fatalf("commitPhaseError(%v) not wrapped as ErrCommitIndeterminate: %v", tt.in, got)
				}
				// The whole point: a wrapped commit-phase error must NOT be retried.
				if isRetryableError(got) {
					t.Fatalf("isRetryableError(commitPhaseError(%v)) = true, want false (would replay a commit -> double-mint)", tt.in)
				}
				return
			}
			if !errors.Is(got, tt.in) {
				t.Fatalf("commitPhaseError(%v) = %v, want pass-through", tt.in, got)
			}
		})
	}
}

// TestWithRetry_DoesNotReplayCommitIndeterminate proves the double-mint fix: a
// connection loss surfaced at/after COMMIT (wrapped via commitPhaseError) must
// stop withRetry from re-running the operation, which would re-apply the write.
func TestWithRetry_DoesNotReplayCommitIndeterminate(t *testing.T) {
	store := &DoltStore{}
	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		// A write whose COMMIT may have landed before the connection dropped.
		return commitPhaseError(errors.New("invalid connection"))
	})
	if callCount != 1 {
		t.Errorf("expected exactly 1 call (no replay) for commit-indeterminate, got %d", callCount)
	}
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Errorf("expected ErrCommitIndeterminate, got %v", err)
	}
}

// TestWithRetry_StillRetriesPreCommitConnLoss guards the converse: a transient
// connection error that is NOT a commit-phase failure stays retryable — only the
// commit phase is protected, so pre-commit blips still recover automatically.
func TestWithRetry_StillRetriesPreCommitConnLoss(t *testing.T) {
	store := &DoltStore{}
	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		if callCount < 2 {
			return errors.New("invalid connection") // pre-commit: safe to replay
		}
		return nil
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls (1 retry + success) for pre-commit conn loss, got %d", callCount)
	}
}
