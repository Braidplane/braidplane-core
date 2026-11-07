package pipelinesvc

import (
	"context"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinesinkeffects"
)

// sinkEffectLedgerService keeps durable pre-effect writes in a dedicated
// database transaction so a crash cannot roll the claim back with terminal run
// persistence that has not happened yet.
type sinkEffectLedgerService struct {
	txm  postgres.TransactionManager
	repo pipelinesinkeffects.Repository
}

// NewSinkEffectLedger creates the durable ledger that guards production sink dispatch.
func NewSinkEffectLedger(
	txm postgres.TransactionManager,
	repo pipelinesinkeffects.Repository,
) (pipelinerun.SinkEffectLedger, error) {
	if txm == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("sink effect ledger transaction manager is required"),
		)
	}
	if repo == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("sink effect ledger repository is required"),
		)
	}
	return sinkEffectLedgerService{txm: txm, repo: repo}, nil
}

// BeginOrGet claims a side effect before connector dispatch or returns its
// prior durable state for the runtime's replay decision.
func (s sinkEffectLedgerService) BeginOrGet(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	attempt pipelinerun.SinkEffectAttempt,
) (*pipelinerun.SinkEffect, error) {
	var effect *pipelinerun.SinkEffect
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		effect, err = s.repo.BeginOrGet(ctx, tx, orgSlug, projectSlug, environmentSlug, attempt)
		return err
	})
	return effect, err
}

// MarkSucceeded records a provider-confirmed effect separately from the later
// terminal-run write, preserving the conservative unknown state on a crash.
func (s sinkEffectLedgerService) MarkSucceeded(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	attempt pipelinerun.SinkEffectAttempt,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return s.repo.MarkSucceeded(ctx, tx, orgSlug, projectSlug, environmentSlug, attempt)
	})
}
