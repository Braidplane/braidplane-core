package runtriagesvc

import (
	"fmt"
	"strings"
	"time"
)

// Text renders the report as a short plain-text diagnosis. now fixes the
// reference time for relative ages so output is reproducible.
func (r *Report) Text(now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Inspected the %d most recent runs across %d pipeline(s)", r.RunsInspected, r.PipelinesInspected)
	if r.MoreRunsExist {
		b.WriteString(" (more runs exist beyond this window)")
	}
	fmt.Fprintf(&b, ".\n%d failing now, %d recovered after a failure, %d with no failures in the window.\n",
		len(r.Failing), len(r.Recovered), r.Healthy)
	if len(r.Failing) == 0 && len(r.Recovered) == 0 {
		b.WriteString("Nothing to triage: no failed runs in the window.\n")
	}
	for _, f := range r.Failing {
		fmt.Fprintf(&b, "\nFAILING  %s\n  Latest run   %s failed %s after %s", f.Pipeline, f.LatestRun, age(now, f.StartedAt), seconds(f.DurationMs))
		if f.Failure != nil {
			fmt.Fprintf(&b, " at step %q", f.Failure.Step)
		}
		fmt.Fprintf(&b, "\n  Streak       %d consecutive failed run(s); ", f.FailedStreak)
		if f.LastSuccess != nil {
			fmt.Fprintf(&b, "last success %s %s\n", f.LastSuccess.RunSlug, age(now, f.LastSuccess.At))
		} else {
			b.WriteString("no success in the inspected window\n")
		}
		switch {
		case f.Failure != nil:
			fmt.Fprintf(&b, "  Cause        %s\n  Error        %s\n", orDefault(f.Failure.ReasonCode, "unknown"), f.Failure.Error)
			if f.Failure.ActionHint != "" {
				fmt.Fprintf(&b, "  Suggested    %s\n", f.Failure.ActionHint)
			}
			var cautions []string
			if f.Failure.OutcomeUncertain {
				cautions = append(cautions, "outcome uncertain: inspect the destination before re-running")
			}
			if f.Failure.AutomaticRetryProhibited {
				cautions = append(cautions, "automatic retry prohibited")
			}
			if len(cautions) > 0 {
				fmt.Fprintf(&b, "  Caution      %s\n", strings.Join(cautions, "; "))
			}
		default:
			fmt.Fprintf(&b, "  Error        %s", orDefault(f.ErrorSummary, "n/a"))
			if f.DetailsUnavailable {
				b.WriteString(" (details unavailable)")
			}
			b.WriteString("\n")
		}
		if f.Retry != nil {
			state := "no retry is waiting; re-run manually after fixing the cause"
			if f.Retry.WaitingForRetry {
				state = "an automatic retry is queued or running"
			} else if f.Retry.Closed {
				state = "closed, no retry pending"
			}
			fmt.Fprintf(&b, "  Retry state  %s (%d attempt(s) in this run chain)\n", state, f.Retry.Attempts)
		}
		if f.ActiveRuns > 0 {
			fmt.Fprintf(&b, "  Note         %d newer run(s) queued or running\n", f.ActiveRuns)
		}
	}
	if len(r.OmittedFailing) > 0 {
		fmt.Fprintf(&b, "\n%d more failing pipeline(s) listed without details: %s\n", len(r.OmittedFailing), strings.Join(r.OmittedFailing, ", "))
	}
	for _, p := range r.Recovered {
		fmt.Fprintf(&b, "\nRECOVERED  %s: %d failed run(s) in the window, latest run %s %s %s\n",
			p.Pipeline, p.FailedInWindow, p.LatestRun, p.LatestStatus, age(now, p.StartedAt))
	}
	return b.String()
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func seconds(ms int64) string { return fmt.Sprintf("%.1fs", float64(ms)/1000) }

func age(now, at time.Time) string {
	d := max(now.Sub(at), 0)
	switch {
	case d < 90*time.Second:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < 90*time.Minute:
		return fmt.Sprintf("%dm ago", int((d + 30*time.Second).Minutes()))
	case d < 36*time.Hour:
		return fmt.Sprintf("%dh ago", int((d + 30*time.Minute).Hours()))
	default:
		return fmt.Sprintf("%.1fd ago", d.Hours()/24)
	}
}
