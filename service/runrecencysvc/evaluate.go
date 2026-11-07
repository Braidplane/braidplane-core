package runrecencysvc

import "time"

// Window is the immutable expectation and deployment snapshot for an evaluation.
type Window struct {
	EnabledAt  time.Time
	DeployedAt time.Time
	MaximumAge time.Duration
}

// Deadline gives a new expectation or deployment a full window. Callers supply
// only durable successes qualified against this exact deployment.
func (w Window) Deadline(lastSuccess *time.Time) time.Time {
	baseline := w.EnabledAt
	if w.DeployedAt.After(baseline) {
		baseline = w.DeployedAt
	}
	if lastSuccess != nil && lastSuccess.After(baseline) {
		baseline = *lastSuccess
	}
	return baseline.Add(w.MaximumAge)
}

func (w Window) Breached(now time.Time, lastSuccess *time.Time) bool {
	return !now.Before(w.Deadline(lastSuccess))
}
