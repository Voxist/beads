package testutil

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Unit tests for the vp-hlfzn test-server declaration guard: an environment
// port bd would dial must refuse a store-creating test unless this process
// declared that port or the harness vouched for it. BEADS_TEST_SERVER=1 is
// not a declaration.

// clearGuardEnv empties every variable the guard reads, so a test states the
// whole environment it runs under.
func clearGuardEnv(t *testing.T) {
	t.Helper()
	for _, name := range append([]string{"BEADS_TEST_SERVER", EnvSharedDoltServer}, serverPortEnvs...) {
		t.Setenv(name, "")
	}
}

func TestUndeclaredServerPortEnv(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		declare int
		want    string
	}{
		{name: "no env port", want: ""},
		{name: "inherited BEADS_DOLT_SERVER_PORT", env: map[string]string{"BEADS_DOLT_SERVER_PORT": "48770"}, want: "BEADS_DOLT_SERVER_PORT"},
		{name: "inherited legacy BEADS_DOLT_PORT", env: map[string]string{"BEADS_DOLT_PORT": "48770"}, want: "BEADS_DOLT_PORT"},
		{name: "BEADS_TEST_SERVER=1 declares nothing", env: map[string]string{"BEADS_DOLT_SERVER_PORT": "48770", "BEADS_TEST_SERVER": "1"}, want: "BEADS_DOLT_SERVER_PORT"},
		{name: "declared port", env: map[string]string{"BEADS_DOLT_SERVER_PORT": "48770"}, declare: 48770, want: ""},
		{name: "a declaration covers its port only", env: map[string]string{"BEADS_DOLT_SERVER_PORT": "48770"}, declare: 45111, want: "BEADS_DOLT_SERVER_PORT"},
		{
			// configfile falls back to BEADS_DOLT_PORT when
			// BEADS_DOLT_SERVER_PORT is not a port, so each is checked.
			name: "legacy port behind an unusable server port",
			env:  map[string]string{"BEADS_DOLT_SERVER_PORT": "nope", "BEADS_DOLT_PORT": "48770"},
			want: "BEADS_DOLT_PORT",
		},
		{
			name:    "undeclared legacy port beside a declared one",
			env:     map[string]string{"BEADS_DOLT_SERVER_PORT": "45111", "BEADS_DOLT_PORT": "48770"},
			declare: 45111,
			want:    "BEADS_DOLT_PORT",
		},
		{name: "vouched harness port", env: map[string]string{"BEADS_DOLT_SERVER_PORT": "45111", EnvSharedDoltServer: "45111"}, want: ""},
		{name: "zero is not a port", env: map[string]string{"BEADS_DOLT_SERVER_PORT": "0"}, want: ""},
		{name: "non-numeric is not a port", env: map[string]string{"BEADS_DOLT_SERVER_PORT": "abc"}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearGuardEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if tc.declare != 0 {
				DeclareTestServerPort(t, tc.declare)
			}
			if got := undeclaredServerPortEnv(); got != tc.want {
				t.Errorf("undeclaredServerPortEnv() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeclareTestServerPortEndsWithTheTest(t *testing.T) {
	t.Run("declare", func(t *testing.T) {
		DeclareTestServerPort(t, 48771)
		if !testServerPortDeclared(48771) {
			t.Fatal("port not declared inside the declaring test")
		}
	})
	if testServerPortDeclared(48771) {
		t.Fatal("port still declared after the declaring test ended")
	}
}

func TestUndeclareKeepsOtherDeclarations(t *testing.T) {
	// The shared container's port can also be declared by a test that dials
	// it: that test ending must not undeclare the container.
	declareTestServerPort("48772")
	t.Cleanup(func() { undeclareTestServerPort("48772") })
	t.Run("second declaration", func(t *testing.T) {
		DeclareTestServerPort(t, 48772)
	})
	if !testServerPortDeclared(48772) {
		t.Fatal("ending one declaration removed the other")
	}
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
	// mode, and every remediation: an operator hitting this in an agent
	// shell needs the next step, not just a wall.
	clearGuardEnv(t)
	t.Setenv("BEADS_DOLT_SERVER_PORT", "48770")
	full := refusalMessage("BEADS_DOLT_SERVER_PORT")
	for _, want := range []string{
		"refusing store-creating test",
		`BEADS_DOLT_SERVER_PORT="48770"`,
		"vp-kmgu",
		"DeclareTestServerPort",
		`t.Setenv("BEADS_DOLT_SERVER_PORT", "")`,
		"BEADS_TEST_SERVER=1 does not declare a port",
		EnvSharedDoltServer,
	} {
		if !strings.Contains(full, want) {
			t.Errorf("refusal message missing %q:\n%s", want, full)
		}
	}
}

// guardChildEnv marks the re-executed test binary as the guard's child.
const guardChildEnv = "BEADS_TESTUTIL_GUARD_CHILD"

// guardReturnedMarker is what the child prints when the guard let it through.
const guardReturnedMarker = "GUARD-RETURNED"

// TestRequireDeclaredTestServerChild is the child half of
// TestRequireDeclaredTestServerRefuses: it runs only when re-executed with
// guardChildEnv set, calls the guard in its own fresh process, and prints
// guardReturnedMarker if the guard returned.
func TestRequireDeclaredTestServerChild(t *testing.T) {
	if os.Getenv(guardChildEnv) != "1" {
		t.Skip("child half of TestRequireDeclaredTestServerRefuses")
	}
	RequireDeclaredTestServer(t)
	os.Stdout.WriteString(guardReturnedMarker + "\n")
}

// TestRequireDeclaredTestServerRefuses runs the guard in a re-executed test
// binary with a crafted environment, since a t.Fatal cannot be observed from
// inside the test it ends. Each refusing case must exit 1 with the refusal
// and must not reach the line after the guard; the passing cases show the
// harness itself does not fail everything.
func TestRequireDeclaredTestServerRefuses(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cases := []struct {
		name   string
		env    []string
		refuse bool
	}{
		{name: "inherited port", env: []string{"BEADS_DOLT_SERVER_PORT=48770"}, refuse: true},
		{name: "inherited port under BEADS_TEST_SERVER=1", env: []string{"BEADS_DOLT_SERVER_PORT=48770", "BEADS_TEST_SERVER=1"}, refuse: true},
		{name: "inherited legacy port", env: []string{"BEADS_DOLT_PORT=48770"}, refuse: true},
		{name: "no port", refuse: false},
		{name: "vouched harness port", env: []string{"BEADS_DOLT_SERVER_PORT=45111", EnvSharedDoltServer + "=45111"}, refuse: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(exe, "-test.run=^TestRequireDeclaredTestServerChild$", "-test.v")
			// Not os.Environ(): the child sees exactly this environment.
			cmd.Env = append([]string{guardChildEnv + "=1", "PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, tc.env...)
			out, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			code := 0
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatalf("run child: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "--- ") || strings.Contains(string(out), "--- SKIP") {
				t.Fatalf("child test did not run:\n%s", out)
			}
			returned := strings.Contains(string(out), guardReturnedMarker)
			refused := strings.Contains(string(out), "refusing store-creating test")
			if tc.refuse && (code != 1 || returned || !refused) {
				t.Fatalf("want a refusal (exit 1, no %s); got exit %d:\n%s", guardReturnedMarker, code, out)
			}
			if !tc.refuse && (code != 0 || !returned || refused) {
				t.Fatalf("want the guard to return (exit 0, %s); got exit %d:\n%s", guardReturnedMarker, code, out)
			}
		})
	}
}
