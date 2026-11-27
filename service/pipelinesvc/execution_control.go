package pipelinesvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
)

type (
	ExecutionScope   = pipelineruns.ExecutionScope
	ExecutionControl = pipelineruns.ExecutionControl
)

var (
	ErrExecutionPaused          = pipelineruns.ErrExecutionPaused
	ErrExecutionControlRevision = pipelineruns.ErrExecutionControlRevision
)

func (s *service) GetExecutionControl(ctx context.Context, scope ExecutionScope) (*ExecutionControl, error) {
	return postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*ExecutionControl, error) {
		return pipelineruns.InspectExecutionControl(ctx, db, scope)
	})
}

func (s *service) SetExecutionControl(ctx context.Context, scope ExecutionScope, expectedRevision int64, paused bool, actor model.EventActor) (*ExecutionControl, error) {
	if expectedRevision < 0 {
		return nil, xerror.InvalidInput(xerror.Pipeline).Detail("expected revision must be nonnegative")
	}
	if err := s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		if err := pipelineruns.EnsureExecutionControl(ctx, db, scope); err != nil {
			return err
		}
		changed, err := pipelineruns.SetExecutionControl(ctx, db, scope, expectedRevision, paused, actor)
		if err != nil {
			return err
		}
		if !changed {
			state, err := pipelineruns.InspectExecutionControl(ctx, db, scope)
			if err != nil {
				return err
			}
			if state.Paused != paused || expectedRevision > state.Revision {
				return ErrExecutionControlRevision
			}
			return nil
		}
		operation := "execution_resumed"
		if paused {
			operation = "execution_paused"
		}
		_, err = s.eventProducer.EnqueueRequired(ctx, db, scope.Org, actor, pipelineEvent{
			projectSlug: scope.Project, pipelineSlug: scope.Pipeline, action: model.EventActionUpdate, securityAudit: true,
			metadata: map[string]any{"operation": operation, "environment_slug": scope.Environment.String(), "revision": expectedRevision + 1},
		})
		return err
	}); err != nil {
		return nil, err
	}
	// The authorization snapshot can predate a claim that committed before the pause acquired its lock.
	return s.GetExecutionControl(ctx, scope)
}
