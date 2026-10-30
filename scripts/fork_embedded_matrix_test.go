package scripts_test

// FORK (Voxist/beads): the embedded Dolt tier is a four-leg matrix in the
// fork's own CI (mode local, GitHub-hosted 4-vCPU runners), cutting the lane
// from ~133 min on one runner. Splitting a lane is only safe if no target
// falls between the legs, so this pins the partition: the "rest" leg is
// //... minus exactly the other legs' targets. A target upstream adds then
// lands in "rest" instead of in no leg, and a target cannot be excluded from
// "rest" without another leg running it.

import (
	"slices"
	"strings"
	"testing"
)

func TestForkEmbeddedMatrixCoversEveryTarget(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelEmbedJobName)
	if job.Strategy.FailFast {
		t.Errorf("%s fail-fast = true; one failing leg must not cancel the others' results", bazelEmbedJobName)
	}
	legs := job.Strategy.Matrix.Include
	var groups []string
	var rest []string
	var others []string
	for _, leg := range legs {
		groups = append(groups, leg.Group)
		if leg.Targets == "" {
			t.Errorf("leg %q names no targets", leg.Group)
		}
		if leg.Group == "rest" {
			rest = strings.Fields(leg.Targets)
			continue
		}
		for _, target := range strings.Fields(leg.Targets) {
			if strings.HasPrefix(target, "-") {
				t.Errorf("leg %q excludes %s; only the rest leg may exclude", leg.Group, target)
			}
			others = append(others, target)
		}
	}
	if want := []string{"cmd-a", "cmd-b", "storage", "rest"}; !slices.Equal(groups, want) {
		t.Errorf("embedded matrix groups = %v, want %v", groups, want)
	}
	want := []string{"//..."}
	for _, target := range others {
		want = append(want, "-"+target)
	}
	if !slices.Equal(rest, want) {
		t.Errorf("rest leg targets = %v, want %v (//... minus exactly the other legs' targets)", rest, want)
	}

	served := job.step(t, "Upload served test timings")
	if want := "${{ !cancelled() && steps.test.outcome != 'skipped' && matrix.group == 'rest' }}"; served.If != want {
		t.Errorf("served test timings upload if = %q, want %q; only the rest leg runs backend/http and may upload bazel-embedded-served-testxml", served.If, want)
	}

	// Each leg reads its own targets and suites from the matrix; a wrong
	// mapping (every leg on one leg's targets) would silently drop the rest.
	if got := job.step(t, "bazel test //... --config=embedded").Env["TARGETS"]; got != "${{ matrix.targets }}" {
		t.Errorf("embedded test step TARGETS = %q, want ${{ matrix.targets }}", got)
	}
	if got := job.step(t, "Every listed test ran in its shard").Env["COVERAGE"]; got != "${{ matrix.shard-suites }}" {
		t.Errorf("embedded coverage step COVERAGE = %q, want ${{ matrix.shard-suites }}", got)
	}
}
