package proxy

import (
	"testing"
	"time"
)

func TestIdleTimeoutFromEnv(t *testing.T) {
	const fallback = 30 * time.Second
	cases := []struct {
		name string
		set  bool
		val  string
		want time.Duration
	}{
		{"unset returns fallback", false, "", fallback},
		{"blank returns fallback", true, "   ", fallback},
		{"unparseable returns fallback", true, "soon", fallback},
		{"valid minutes", true, "10m", 10 * time.Minute},
		{"valid seconds", true, "90s", 90 * time.Second},
		{"zero disables (verbatim)", true, "0", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(IdleTimeoutEnvVar, tc.val)
			} else {
				t.Setenv(IdleTimeoutEnvVar, "")
			}
			if got := IdleTimeoutFromEnv(fallback); got != tc.want {
				t.Fatalf("IdleTimeoutFromEnv(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

// TestDebugFromEnv covers the operator escape hatch for the trace throttle
// (vp-rnq0): per-connection proxy traces default OFF and are re-enabled for
// diagnostic runs via BEADS_PROXY_DEBUG, without code changes.
func TestDebugFromEnv(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want bool
	}{
		{"unset returns fallback", "", false},
		{"blank returns fallback", "   ", false},
		{"unparseable returns fallback", "yes please", false},
		{"one enables", "1", true},
		{"true enables", "true", true},
		{"mixed-case true enables", "True", true},
		{"zero disables", "0", false},
		{"false disables", "false", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(DebugEnvVar, tc.val)
			if got := DebugFromEnv(false); got != tc.want {
				t.Fatalf("DebugFromEnv(false) with %s=%q = %v, want %v", DebugEnvVar, tc.val, got, tc.want)
			}
		})
	}

	// A true fallback survives unset/unparseable values (mirrors
	// IdleTimeoutFromEnv's fallback semantics).
	t.Run("fallback true preserved", func(t *testing.T) {
		t.Setenv(DebugEnvVar, "")
		if !DebugFromEnv(true) {
			t.Fatal("DebugFromEnv(true) with unset env = false, want true")
		}
		t.Setenv(DebugEnvVar, "0")
		if DebugFromEnv(true) {
			t.Fatal("DebugFromEnv(true) with env=0 = true, want false (explicit off wins)")
		}
	})
}
