package testutil

import (
	"strings"
	"testing"
)

// Unit tests for the vp-hlfzn test-server declaration guard: an
// environment-inherited BEADS_DOLT_SERVER_PORT (or legacy BEADS_DOLT_PORT)
// must refuse a store-creating test unless BEADS_TEST_SERVER=1 declares the
// server a test server; no inherited port must never refuse.

func TestInheritedServerPortEnv(t *testing.T) {
	t.Run("no env port passes", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")
		t.Setenv("BEADS_DOLT_PORT", "")
		t.Setenv("BEADS_TEST_SERVER", "")
		if got := inheritedServerPortEnv(); got != "" {
			t.Errorf("inheritedServerPortEnv() = %q, want \"\"", got)
		}
	})

	t.Run("inherited BEADS_DOLT_SERVER_PORT is named", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "48770")
		t.Setenv("BEADS_DOLT_PORT", "")
		t.Setenv("BEADS_TEST_SERVER", "")
		if got := inheritedServerPortEnv(); got != "BEADS_DOLT_SERVER_PORT" {
			t.Errorf("inheritedServerPortEnv() = %q, want BEADS_DOLT_SERVER_PORT", got)
		}
	})

	t.Run("legacy BEADS_DOLT_PORT is named", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")
		t.Setenv("BEADS_DOLT_PORT", "48770")
		t.Setenv("BEADS_TEST_SERVER", "")
		if got := inheritedServerPortEnv(); got != "BEADS_DOLT_PORT" {
			t.Errorf("inheritedServerPortEnv() = %q, want BEADS_DOLT_PORT", got)
		}
	})

	t.Run("declaration suppresses the refusal", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "48770")
		t.Setenv("BEADS_TEST_SERVER", "1")
		if got := inheritedServerPortEnv(); got != "BEADS_DOLT_SERVER_PORT" {
			t.Fatalf("inheritedServerPortEnv() = %q, want BEADS_DOLT_SERVER_PORT (env is still inherited)", got)
		}
		if !testServerDeclared() {
			t.Fatal("testServerDeclared() = false, want true with BEADS_TEST_SERVER=1")
		}
	})
}

func TestRequireDeclaredTestServerPassesWhenDeclared(t *testing.T) {
	// The wrapper is a thin t.Fatalf over the decision helpers; the fatal
	// path cannot be observed from inside the test goroutine, so the refusal
	// matrix lives in TestInheritedServerPortEnv and the vouch matrix in
	// TestPortVouchedByHarness. This pins the wrapper's pass-through behavior
	// in every accepting configuration.
	t.Run("no inherited port", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")
		t.Setenv("BEADS_DOLT_PORT", "")
		t.Setenv("BEADS_TEST_SERVER", "")
		t.Setenv(EnvSharedDoltServer, "")
		RequireDeclaredTestServer(t)
	})

	t.Run("declared with inherited port", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "48770")
		t.Setenv("BEADS_TEST_SERVER", "1")
		t.Setenv(EnvSharedDoltServer, "")
		RequireDeclaredTestServer(t)
	})

	t.Run("vouched harness port", func(t *testing.T) {
		// The Docker-less scripts/test.sh flow: no BEADS_TEST_SERVER
		// declaration, but the port is the one the harness provisioned and
		// vouched for — the same exception neutralizeUnvouchedDoltPorts
		// honors. Refusing here would break that flow.
		t.Setenv("BEADS_DOLT_SERVER_PORT", "45111")
		t.Setenv("BEADS_TEST_SERVER", "")
		t.Setenv(EnvSharedDoltServer, "45111")
		RequireDeclaredTestServer(t)
	})
}

func TestPortVouchedByHarness(t *testing.T) {
	t.Run("exact value match vouches", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "45111")
		t.Setenv(EnvSharedDoltServer, "45111")
		if !portVouchedByHarness("BEADS_DOLT_SERVER_PORT") {
			t.Fatal("portVouchedByHarness = false, want true for the exact vouched value")
		}
	})

	t.Run("different value does not vouch", func(t *testing.T) {
		// The marker vouches for ITS value only, never for the process — a
		// different port beside it is still ambient (mirrors
		// neutralizeUnvouchedDoltPorts' per-variable decision, gm-2g3g5r).
		t.Setenv("BEADS_DOLT_SERVER_PORT", "48770")
		t.Setenv("BEADS_DOLT_PORT", "45111")
		t.Setenv(EnvSharedDoltServer, "45111")
		if portVouchedByHarness("BEADS_DOLT_SERVER_PORT") {
			t.Fatal("48770 must not be vouched by a marker naming 45111")
		}
		if !portVouchedByHarness("BEADS_DOLT_PORT") {
			t.Fatal("45111 must be vouched")
		}
	})

	t.Run("empty marker vouches nothing", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "45111")
		t.Setenv(EnvSharedDoltServer, "")
		if portVouchedByHarness("BEADS_DOLT_SERVER_PORT") {
			t.Fatal("no marker, no vouch")
		}
	})
}

func TestRefusalMessageCarriesTheDoctrine(t *testing.T) {
	// The refusal must name the offending variable, the live-city failure
	// mode, and both remediations — an operator hitting this in an agent
	// shell needs the next step, not just a wall. Assert the actual Fatalf
	// payload shape by re-deriving it from the same inputs the wrapper uses,
	// so the message cannot silently lose a component.
	t.Setenv("BEADS_DOLT_SERVER_PORT", "48770")
	t.Setenv("BEADS_TEST_SERVER", "")
	name := inheritedServerPortEnv()
	if name == "" {
		t.Fatal("expected the inherited port to be detected")
	}
	full := refusalMessage(name)
	for _, want := range []string{
		"refusing store-creating test",
		name,
		"48770",
		"BEADS_TEST_SERVER=1",
		EnvSharedDoltServer,
		"vp-kmgu",
		`t.Setenv("` + name + `", "")`,
	} {
		if !strings.Contains(full, want) {
			t.Errorf("refusal message missing %q:\n%s", want, full)
		}
	}
}
