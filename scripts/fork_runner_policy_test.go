package scripts_test

// FORK (Voxist/beads): the Voxist org has no Blacksmith runners. A job whose
// runs-on (or matrix runner) can name a blacksmith-* label gets no runner and
// sits queued until GitHub cancels it at 24 h, which is how every push to
// main and every nightly hung from 2026-10-06 until the fork swapped the
// labels. Upstream's Blacksmith secret-read guards find no Blacksmith job in
// the fork, so this is the fork's runner guard: a new upstream job on a
// Blacksmith label fails here at the next resync instead of hanging in CI.

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// blacksmithRunnerLine matches a runs-on or matrix runner line that names a
// blacksmith-* label, literally or inside an expression. Comments and job
// names (blacksmith-macos-go-build-cache:) do not match.
var blacksmithRunnerLine = regexp.MustCompile(`^\s*(-\s+)?(runs-on|runner):.*blacksmith-`)

func blacksmithRunnerLines(src string) []int {
	var lines []int
	for i, line := range strings.Split(src, "\n") {
		if blacksmithRunnerLine.MatchString(line) {
			lines = append(lines, i+1)
		}
	}
	return lines
}

func TestForkRunnerScanCatchesEveryShape(t *testing.T) {
	src := strings.Join([]string{
		"jobs:",
		"  a:",
		"    runs-on: blacksmith-4vcpu-ubuntu-2404",
		"  b:",
		"    runs-on: ${{ github.event_name == 'push' && 'blacksmith-2vcpu-ubuntu-2404' || 'ubuntu-latest' }}",
		"  c:",
		"    strategy:",
		"      matrix:",
		"        include:",
		"          - runner: blacksmith-6vcpu-macos-26",
		"  blacksmith-macos-go-build-cache:",
		"    # runs on blacksmith-4vcpu-windows-2025 upstream",
		"    runs-on: ubuntu-latest",
	}, "\n")
	if got, want := blacksmithRunnerLines(src), []int{3, 5, 10}; !slices.Equal(got, want) {
		t.Fatalf("scan found lines %v, want %v (bare label, expression, matrix runner; not a comment or a job name)", got, want)
	}
}

func TestForkWorkflowsUseNoBlacksmithRunner(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(sourceRepoRoot(t), ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 20 {
		t.Fatalf("found only %d workflow files; the listing is broken", len(files))
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range blacksmithRunnerLines(string(data)) {
			t.Errorf("%s:%d names a blacksmith-* runner; the Voxist org has none, so the job would queue until GitHub cancels it (use the GitHub-hosted label; see engdocs/FORK_DIVERGENCE.md)", filepath.Base(f), n)
		}
	}
}
