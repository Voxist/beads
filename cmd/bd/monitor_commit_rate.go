package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
)

var (
	alertThresholdMin     int
	alertThresholdCommits int
	alertOnly             bool
	dryRun                bool
)

func init() {
	monitorCommitRateCmd.Flags().IntVar(&alertThresholdMin, "minutes", 1, "Time window in minutes to monitor for commit rate")
	monitorCommitRateCmd.Flags().IntVar(&alertThresholdCommits, "commits", 10, "Alert threshold: number of no-op commits in the time window that triggers an alert")
	monitorCommitRateCmd.Flags().BoolVar(&alertOnly, "alert-only", true, "Only alert when thresholds are exceeded (don't auto-take action)")
	monitorCommitRateCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Perform a dry run without taking any actions")
}

var monitorCommitRateCmd = &cobra.Command{
	Use:   "monitor-commit-rate",
	Short: "Monitor Dolt commit rate and alert on excessive no-op commits (vp-5u7i deliverable 2)",
	Long: `Monitor Dolt commit rate to detect excessive no-op commits that indicate the storm pattern.

Samples dolt_diff_issues for the current database and alerts when the no-op
commit count in the time window is at or above the threshold AND those
commits are concentrated on a disproportionately small set of beads
(signature: many commits, few beads, identical content_hash — ADR-0023 L-A).
This is the local backstop that catches a recurrence independent of bd's own
write-path gate (e.g. a stray script or supervisor writing directly).

This implements deliverable 2 from bead vp-5u7i.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMonitorCommitRate()
	},
}

func runMonitorCommitRate() error {
	if dryRun {
		fmt.Printf("DRY RUN: Monitoring commit rate with threshold of %d no-op commits per %d minutes\n", alertThresholdCommits, alertThresholdMin)
		return nil
	}

	if store == nil {
		return HandleErrorRespectJSON("no database connection available (%s)", diagHint())
	}
	accessor, ok := storage.UnwrapStore(store).(storage.RawDBAccessor)
	if !ok {
		return HandleErrorRespectJSON("storage backend does not support raw DB access required for commit-rate monitoring")
	}
	db := accessor.UnderlyingDB()
	if db == nil {
		return HandleErrorRespectJSON("underlying database not available")
	}

	ctx := rootCtx
	var dbName string
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&dbName); err != nil {
		return HandleErrorRespectJSON("resolving current database name: %v", err)
	}

	result, err := analyzeCommitPatterns(ctx, db, dbName, alertThresholdMin, alertThresholdCommits)
	if err != nil {
		return HandleErrorRespectJSON("failed to analyze commit patterns: %v", err)
	}

	if result.ExcessiveNoOpsDetected {
		fmt.Fprintf(os.Stderr, "ALERT: Excessive no-op commit pattern detected!\n")
		fmt.Fprintf(os.Stderr, "  Database: %s\n", result.DatabaseName)
		fmt.Fprintf(os.Stderr, "  No-op commits: %d\n", result.CommitCount)
		fmt.Fprintf(os.Stderr, "  Distinct beads affected: %d\n", result.DistinctBeadCount)
		fmt.Fprintf(os.Stderr, "  Time window: %v\n", result.TimeWindow)
		fmt.Fprintf(os.Stderr, "  Content hash similarity: %.2f%% identical\n", result.PercentIdenticalContent)

		if !alertOnly {
			fmt.Fprintf(os.Stderr, "Taking corrective action (disabled in this implementation)...\n")
		}
		// Exit code 2 (Nagios-style: 0 clean, 1 command error, 2 alert) gives
		// a caller (the commit-rate-watchdog city-pack order, one invocation
		// per rig) a reliable machine-readable signal without parsing stderr.
		return &exitError{Code: 2}
	}

	fmt.Printf("No excessive commit patterns detected within threshold (database: %s).\n", result.DatabaseName)
	return nil
}

// CommitAnalysisResult holds the results of a commit-rate analysis pass.
// CommitCount and DistinctBeadCount describe only the no-op subset of the
// commits examined in the window — the counts the storm signature is judged
// against — not the window's total commit volume.
type CommitAnalysisResult struct {
	ExcessiveNoOpsDetected  bool
	DatabaseName            string
	CommitCount             int // no-op commits in the window
	DistinctBeadCount       int // distinct beads touched by those no-op commits
	TimeWindow              time.Duration
	PercentIdenticalContent float64 // % of all commits examined that were no-op
}

// minCommitsPerBeadRatio is the "flat distinct-bead count" leg of the storm
// signature (ADR-0023 L-A): the measured incident averaged ~13 no-op commits
// per bead (va-wzio alone took 10 in ~2s). Organic churn averages close to
// one commit per bead, so requiring at least this many no-op commits per
// affected bead separates a storm (the same small set hammered repeatedly)
// from a legitimate burst of first-time no-op touches across many beads.
const minCommitsPerBeadRatio = 2.0

// analyzeCommitPatterns samples dolt_diff_issues for the current database and
// reports whether the no-op-commit storm signature (ADR-0023 L-A) is present
// in the trailing windowMinutes: no-op commit volume at or above
// alertThreshold, concentrated on a disproportionately small set of beads,
// every one of them value-identical (from_content_hash == to_content_hash).
// A commit counts as no-op only when every row it touched compares
// content-identical; any real content change (or an added row, which has no
// "from" state) marks the whole commit as real.
// noOpDiffExcludedColumns are the issues columns whose change does NOT make a
// diff row meaningful, so they are left out of the value comparison below.
//
//   - updated_at is rewritten by every update, including one that changes
//     nothing else — it is the thing a no-op commit consists of.
//   - row_lock is rewritten by any write that can touch status/assignee, so a
//     concurrent reclaim/close collides on the row (see freshRowLock); its
//     change carries no user-visible content.
//   - content_hash is NOT MAINTAINED on the update path. It is written by the
//     upsert/import path only (issueUpsertColumns in issueops/helpers.go) and
//     never recomputed by UpdateIssue, so from_content_hash == to_content_hash
//     holds for every ordinary edit. Comparing it — which this command used to
//     do, and only that — reported every real update as a no-op.
var noOpDiffExcludedColumns = map[string]bool{
	"updated_at":   true,
	"row_lock":     true,
	"content_hash": true,
}

// noOpDiffPredicate builds the SQL that decides whether one dolt_diff_issues
// row changed anything meaningful.
//
// The column list is discovered from information_schema rather than hardcoded:
// the issues table gains columns regularly upstream, and a stale hardcoded list
// fails OPEN — an unlisted column's change would be invisible and the row would
// count as a no-op. Discovery makes a new column meaningful by default, which
// is the safe direction to be wrong in.
//
// <=> is MySQL's NULL-safe equality: NULL <=> NULL is true and NULL <=> 'x' is
// false, which is what "unchanged" means for a nullable column.
func noOpDiffPredicate(ctx context.Context, db *sql.DB, dbName string) (string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = ? AND table_name = 'issues'
		ORDER BY ordinal_position
	`, dbName)
	if err != nil {
		return "", fmt.Errorf("reading issues columns: %w", err)
	}
	defer rows.Close()

	var terms []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return "", fmt.Errorf("scanning column name: %w", err)
		}
		if noOpDiffExcludedColumns[col] {
			continue
		}
		terms = append(terms, fmt.Sprintf("`from_%s` <=> `to_%s`", col, col))
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("reading issues columns: %w", err)
	}
	if len(terms) == 0 {
		return "", fmt.Errorf("issues table reported no comparable columns")
	}
	return strings.Join(terms, " AND "), nil
}

func analyzeCommitPatterns(ctx context.Context, db *sql.DB, dbName string, windowMinutes, alertThreshold int) (*CommitAnalysisResult, error) {
	unchanged, err := noOpDiffPredicate(ctx, db, dbName)
	if err != nil {
		return nil, err
	}

	// Compute the cutoff in Go rather than relying on the server's NOW() with
	// a bound INTERVAL — go-mysql-server's parser support for a placeholder
	// inside INTERVAL is untested, whereas a literal DATETIME comparison is
	// universally supported and keeps the window's meaning explicit.
	cutoff := time.Now().UTC().Add(-time.Duration(windowMinutes) * time.Minute)
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT to_commit,
		       COALESCE(to_id, '') AS to_id,
		       from_id IS NOT NULL AS is_update,
		       (%s) AS unchanged
		FROM dolt_diff_issues
		WHERE to_commit_date > ?
		  AND to_id IS NOT NULL
	`, unchanged), cutoff)
	if err != nil {
		return nil, fmt.Errorf("querying dolt_diff_issues: %w", err)
	}
	defer rows.Close()

	type commitAgg struct {
		allNoOp bool
		beads   map[string]struct{}
	}
	commits := make(map[string]*commitAgg)
	for rows.Next() {
		var commitHash, id string
		var isUpdate, rowUnchanged bool
		if err := rows.Scan(&commitHash, &id, &isUpdate, &rowUnchanged); err != nil {
			return nil, fmt.Errorf("scanning dolt_diff_issues row: %w", err)
		}
		if id == "" {
			continue
		}
		agg, ok := commits[commitHash]
		if !ok {
			agg = &commitAgg{allNoOp: true, beads: make(map[string]struct{})}
			commits[commitHash] = agg
		}
		agg.beads[id] = struct{}{}
		// An INSERT is never a no-op: it added a bead that was not there. Only
		// a modification whose every compared column is unchanged qualifies.
		if !isUpdate || !rowUnchanged {
			agg.allNoOp = false
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading dolt_diff_issues: %w", err)
	}

	noOpCommits := 0
	noOpBeads := make(map[string]struct{})
	for _, agg := range commits {
		if agg.allNoOp {
			noOpCommits++
			for id := range agg.beads {
				noOpBeads[id] = struct{}{}
			}
		}
	}

	result := &CommitAnalysisResult{
		DatabaseName:      dbName,
		CommitCount:       noOpCommits,
		DistinctBeadCount: len(noOpBeads),
		TimeWindow:        time.Duration(windowMinutes) * time.Minute,
	}
	if len(commits) > 0 {
		result.PercentIdenticalContent = 100 * float64(noOpCommits) / float64(len(commits))
	}

	beadsPerCommit := 0.0
	if len(noOpBeads) > 0 {
		beadsPerCommit = float64(noOpCommits) / float64(len(noOpBeads))
	}
	result.ExcessiveNoOpsDetected = noOpCommits >= alertThreshold && beadsPerCommit >= minCommitsPerBeadRatio

	return result, nil
}
