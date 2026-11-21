package runtriagesvc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReportTextNothingToTriage(t *testing.T) {
	report := &Report{RunsInspected: 3, PipelinesInspected: 2, Healthy: 2}

	require.Equal(t, "Inspected the 3 most recent runs across 2 pipeline(s).\n"+
		"0 failing now, 0 recovered after a failure, 2 with no failures in the window.\n"+
		"Nothing to triage: no failed runs in the window.\n", report.Text(testNow))
}

func TestReportTextFailingPipeline(t *testing.T) {
	report := &Report{
		RunsInspected: 7, PipelinesInspected: 3, MoreRunsExist: true, Healthy: 1,
		Failing: []FailingPipeline{{
			Pipeline: "customer-enrichment", LatestRun: "ce-r3", StartedAt: testNow.Add(-3 * time.Hour), DurationMs: 1800,
			FailedStreak: 2, LastSuccess: &RunRef{RunSlug: "ce-r1", At: testNow.Add(-50 * time.Hour)}, ActiveRuns: 1,
			Failure: &Failure{
				Step: "fetch-customers", Error: "relation does not exist", ReasonCode: "source_query_failed",
				ActionHint: "Check the source table", OutcomeUncertain: true, AutomaticRetryProhibited: true,
			},
			Retry: &Retry{Attempts: 1},
		}},
		Recovered:      []RecoveredPipeline{{Pipeline: "inventory", FailedInWindow: 1, LatestRun: "inv-r2", LatestStatus: "succeeded", StartedAt: testNow.Add(-time.Hour)}},
		OmittedFailing: []string{"x", "y"},
	}

	require.Equal(t, `Inspected the 7 most recent runs across 3 pipeline(s) (more runs exist beyond this window).
1 failing now, 1 recovered after a failure, 1 with no failures in the window.

FAILING  customer-enrichment
  Latest run   ce-r3 failed 3h ago after 1.8s at step "fetch-customers"
  Streak       2 consecutive failed run(s); last success ce-r1 2.1d ago
  Cause        source_query_failed
  Error        relation does not exist
  Suggested    Check the source table
  Caution      outcome uncertain: inspect the destination before re-running; automatic retry prohibited
  Retry state  no retry is waiting; re-run manually after fixing the cause (1 attempt(s) in this run chain)
  Note         1 newer run(s) queued or running

2 more failing pipeline(s) listed without details: x, y

RECOVERED  inventory: 1 failed run(s) in the window, latest run inv-r2 succeeded 60m ago
`, report.Text(testNow))
}

func TestAgeFormatsRelativeTime(t *testing.T) {
	for _, tc := range []struct {
		age  time.Duration
		want string
	}{
		{30 * time.Second, "30s ago"},
		{10 * time.Minute, "10m ago"},
		{5 * time.Hour, "5h ago"},
		{48 * time.Hour, "2.0d ago"},
		{-time.Minute, "0s ago"},
	} {
		require.Equal(t, tc.want, age(testNow, testNow.Add(-tc.age)))
	}
}
