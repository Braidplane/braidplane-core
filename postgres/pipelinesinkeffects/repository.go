// Package pipelinesinkeffects persists durable pipeline sink side-effect decisions.
package pipelinesinkeffects

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	internalpg "github.com/Braidplane/braidplane-core/postgres/dbgen"
)

// Repository persists pre-effect claims and terminal success for one sink execution.
// Each method receives the caller's transaction explicitly so runtime orchestration
// can make the ledger durable before invoking an external provider.
type Repository interface {
	BeginOrGet(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		attempt pipelinerun.SinkEffectAttempt,
	) (*pipelinerun.SinkEffect, error)
	MarkSucceeded(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		attempt pipelinerun.SinkEffectAttempt,
	) error
}

type repository struct{}

// New creates a SQLC-backed durable sink-effect repository.
func New() Repository {
	return &repository{}
}

// BeginOrGet atomically inserts the pre-dispatch effect record or returns its
// existing state, preserving an in-progress row after a crash for safe replay decisions.
func (r *repository) BeginOrGet(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	attempt pipelinerun.SinkEffectAttempt,
) (*pipelinerun.SinkEffect, error) {
	row, err := internalpg.New(tx).BeginPipelineSinkEffect(ctx, internalpg.BeginPipelineSinkEffectParams{
		OrgSlug:           orgSlug,
		ProjectSlug:       projectSlug,
		EnvironmentSlug:   environmentSlug,
		ReplayIdentity:    attempt.ReplayIdentity,
		SinkStepSlug:      attempt.SinkStepSlug.String(),
		IdempotencyKey:    attempt.IdempotencyKey,
		IdempotencyClass:  attempt.IdempotencyClass,
		ReplayMethod:      attempt.ReplayMethod,
		ReplayDecision:    attempt.ReplayDecision,
		DiagnosticCode:    optionalText(attempt.DiagnosticCode),
		DiagnosticSummary: optionalText(attempt.DiagnosticSummary),
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrProjectEnvironmentNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	state, err := sinkEffectState(row.State)
	if err != nil {
		return nil, err
	}
	return &pipelinerun.SinkEffect{Claimed: row.Claimed, State: state, StartedAt: row.StartedAt.Time}, nil
}

// MarkSucceeded records the provider-confirmed effect without persisting payload,
// credentials, or the idempotency key in operator-facing diagnostics.
func (r *repository) MarkSucceeded(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	attempt pipelinerun.SinkEffectAttempt,
) error {
	row, err := internalpg.New(tx).MarkPipelineSinkEffectSucceeded(ctx, internalpg.MarkPipelineSinkEffectSucceededParams{
		ReplayDecision:    attempt.ReplayDecision,
		DiagnosticCode:    optionalText(attempt.DiagnosticCode),
		DiagnosticSummary: optionalText(attempt.DiagnosticSummary),
		OrgSlug:           orgSlug,
		ProjectSlug:       projectSlug,
		EnvironmentSlug:   environmentSlug,
		ReplayIdentity:    attempt.ReplayIdentity,
		SinkStepSlug:      attempt.SinkStepSlug.String(),
		IdempotencyKey:    attempt.IdempotencyKey,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return ErrPipelineSinkEffectNotFound.With(err)
		}
		return xerror.Unexpected().With(err)
	}

	if _, err := sinkEffectState(row.State); err != nil {
		return err
	}
	return nil
}

// optionalText converts an absent safe diagnostic into a SQL NULL.
func optionalText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

// sinkEffectState validates the durable lifecycle state before exposing it to runtime code.
func sinkEffectState(value string) (pipelinerun.SinkEffectState, error) {
	state, err := pipelinerun.SinkEffectStateString(value)
	if err != nil || state == pipelinerun.SinkEffectStateUnknown {
		return pipelinerun.SinkEffectStateUnknown, xerror.Unexpected().With(err).Detail(fmt.Sprintf("unsupported pipeline sink effect state %q", value))
	}
	return state, nil
}
