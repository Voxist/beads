package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestUnsupportedInProxiedModeErrorIsTyped verifies the error is errors.As-
// checkable and carries the exact capability — the contract S5 commands and
// the dead-drop guard test rely on, so an unsupported op can never be mistaken
// for a swallowed nil error.
func TestUnsupportedInProxiedModeErrorIsTyped(t *testing.T) {
	err := ErrUnsupportedInProxiedMode(CapabilityDoltPush)

	var typed *UnsupportedInProxiedModeError
	if !errors.As(err, &typed) {
		t.Fatalf("ErrUnsupportedInProxiedMode is not errors.As-checkable as *UnsupportedInProxiedModeError")
	}
	if typed.Capability != CapabilityDoltPush {
		t.Fatalf("Capability = %q, want %q", typed.Capability, CapabilityDoltPush)
	}

	got, ok := AsUnsupportedInProxiedMode(fmt.Errorf("wrap: %w", err))
	if !ok {
		t.Fatalf("AsUnsupportedInProxiedMode failed through a wrap")
	}
	if got.Capability != CapabilityDoltPush {
		t.Fatalf("wrapped Capability = %q, want %q", got.Capability, CapabilityDoltPush)
	}
}

// TestUnsupportedInProxiedModeMessageIsActionable asserts every capability
// renders a non-empty message that names the capability and points to the
// proxied=false escape hatch — never an empty string that could read as success.
func TestUnsupportedInProxiedModeMessageIsActionable(t *testing.T) {
	for _, c := range []Capability{
		CapabilityDoltPush,
		CapabilityDoltPull,
		CapabilityDoltCommit,
		CapabilityCompaction,
		CapabilityFederation,
		CapabilityRemoteSync,
	} {
		msg := ErrUnsupportedInProxiedMode(c).Error()
		if strings.TrimSpace(msg) == "" {
			t.Fatalf("capability %q produced an empty message", c)
		}
		if !strings.Contains(msg, string(c)) {
			t.Fatalf("message %q does not name capability %q", msg, c)
		}
		if !strings.Contains(msg, "proxied=false") {
			t.Fatalf("message %q does not mention the proxied=false escape hatch", msg)
		}
	}
}

// TestCapabilityValuesAreStable pins the wire/string values so a rename can't
// silently break tests or operator-facing messages.
func TestCapabilityValuesAreStable(t *testing.T) {
	want := map[Capability]string{
		CapabilityDoltPush:   "dolt-push",
		CapabilityDoltPull:   "dolt-pull",
		CapabilityDoltCommit: "dolt-commit",
		CapabilityCompaction: "compaction",
		CapabilityFederation: "federation",
		CapabilityRemoteSync: "remote-sync",
	}
	for c, s := range want {
		if string(c) != s {
			t.Fatalf("capability value = %q, want %q", string(c), s)
		}
	}
}
