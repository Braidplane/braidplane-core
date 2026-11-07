package runrecencysvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/runrecency"
)

type (
	Scope       = runrecency.Scope
	Expectation = runrecency.Expectation
)

type Inspection struct {
	Expectation       runrecency.Expectation
	Episodes          []runrecency.EpisodeEvidence
	HasMoreEpisodes   bool
	EvaluatedAt       time.Time
	Deadline          *time.Time
	Outcome           model.RunRecencyOutcome
	UnavailableReason *string
}

// Inspect calculates current state without producing notifications.
func (s *Service) Inspect(ctx context.Context, scope runrecency.Scope, now time.Time) (*Inspection, error) {
	result := &Inspection{EvaluatedAt: now.UTC()}
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		// Serialize with rule updates and the observer so the rule and its
		// episode history cannot straddle a concurrent revision change.
		if err := s.repo.LockScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		result.Expectation, err = s.repo.Inspect(ctx, tx, scope)
		if err != nil {
			return err
		}
		result.Episodes, err = s.repo.InspectEpisodes(ctx, tx, scope)
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(result.Episodes) > 20 {
		result.HasMoreEpisodes = true
		result.Episodes = result.Episodes[:20]
	}
	result.calculate()
	return result, nil
}

func (i *Inspection) calculate() {
	e := i.Expectation
	reason := ""
	switch {
	case e.Revision == 0:
		reason = "not_configured"
	case !e.Enabled:
		reason = "expectation_disabled"
	case e.ScopeDisabled:
		reason = "environment_unavailable"
	case e.DeploymentID == 0:
		reason = "deployment_unavailable"
	}
	if reason != "" {
		i.Outcome, i.UnavailableReason = model.RunRecencyOutcomeUnavailable, &reason
		return
	}
	window := Window{EnabledAt: e.EnabledAt, DeployedAt: e.DeployedAt, MaximumAge: e.MaximumAge}
	deadline := window.Deadline(e.SucceededAt)
	i.Deadline = &deadline
	switch {
	case !i.EvaluatedAt.Before(deadline):
		i.Outcome = model.RunRecencyOutcomeBreached
	case e.SucceededAt == nil || e.SucceededAt.Before(e.EnabledAt) || e.SucceededAt.Before(e.DeployedAt):
		i.Outcome = model.RunRecencyOutcomeGrace
	default:
		i.Outcome = model.RunRecencyOutcomeHealthy
	}
}
