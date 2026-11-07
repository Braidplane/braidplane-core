package pipelineruntime

import (
	"context"
	"encoding/json"
	"time"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
)

// runStorage persists pipeline run records.
type runStorage interface {
	CreateRun(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		params *createRunParams,
	) error
	CompleteRun(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		params *createRunParams,
	) error
}

type createRunParams struct {
	DeploymentID               *int64
	DeploymentRevision         *int64
	DeploymentResolutionDigest *string
	PipelineVersionID          int64
	PipelineSlug               string
	RunSlug                    string
	Source                     pipelinerun.RunSourceSummary
	RequestID                  *string
	Status                     string
	DurationMs                 int64
	StepCount                  int32
	StepSlug                   string
	ErrorSummary               *string
	FailureKind                *string
	FailureSide                *string
	StartedAt                  time.Time
	EndedAt                    *time.Time
	ReplayOfRunSlug            *string
	ReplayFromStep             *string
	Incremental                pipelinerun.IncrementalRunSummary
	IncrementalDiagnostics     json.RawMessage
	Timeline                   json.RawMessage
}

type persistedRunStorage struct {
	repo pipelineruns.Repository
}

func newRunStorageService(repo pipelineruns.Repository) runStorage {
	if repo == nil {
		panic("pipelineruntime: run storage repository is required")
	}

	return &persistedRunStorage{repo: repo}
}

// CreateRun persists a runtime result, completing a preaccepted async row when one exists.
func (s *persistedRunStorage) CreateRun(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params *createRunParams,
) error {
	if _, err := s.repo.CompleteRun(
		ctx,
		tx,
		orgSlug,
		projectSlug,
		environmentSlug,
		params.RunSlug,
		completeRunParamsFromCreateRunParams(params),
	); err == nil {
		return nil
	} else if !xerror.Is(err, pipelineruns.ErrRunNotFound) {
		return xerror.Wrapf("complete accepted pipeline run: %w", err)
	}

	// No accepted async row exists, so persist the runtime result directly.
	_, err := s.repo.CreateRun(ctx, tx, orgSlug, projectSlug, environmentSlug, &pipelineruns.CreateRunParams{
		PipelineVersionID:          params.PipelineVersionID,
		DeploymentID:               params.DeploymentID,
		DeploymentRevision:         params.DeploymentRevision,
		DeploymentResolutionDigest: params.DeploymentResolutionDigest,
		PipelineSlug:               params.PipelineSlug,
		RunSlug:                    params.RunSlug,
		Source:                     params.Source,
		RequestID:                  params.RequestID,
		Status:                     params.Status,
		DurationMs:                 params.DurationMs,
		StepCount:                  params.StepCount,
		StepSlug:                   params.StepSlug,
		ErrorSummary:               params.ErrorSummary,
		FailureKind:                params.FailureKind,
		FailureSide:                params.FailureSide,
		StartedAt:                  params.StartedAt,
		EndedAt:                    params.EndedAt,
		ReplayOfRunSlug:            params.ReplayOfRunSlug,
		ReplayFromStep:             params.ReplayFromStep,
		Incremental:                params.Incremental,
		IncrementalDiagnostics:     params.IncrementalDiagnostics,
		Timeline:                   params.Timeline,
	})
	if err != nil {
		if xerror.Is(err, pipelineruns.ErrRunAlreadyExists) {
			// A concurrent accepter inserted the row; complete it instead of failing the run.
			return s.CompleteRun(ctx, tx, orgSlug, projectSlug, environmentSlug, params)
		}
		return xerror.Wrapf("create pipeline run: %w", err)
	}

	return nil
}

// CompleteRun writes terminal execution diagnostics onto an accepted queued run row.
func (s *persistedRunStorage) CompleteRun(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params *createRunParams,
) error {
	_, err := s.repo.CompleteRun(
		ctx,
		tx,
		orgSlug,
		projectSlug,
		environmentSlug,
		params.RunSlug,
		completeRunParamsFromCreateRunParams(params),
	)
	if err != nil {
		return xerror.Wrapf("complete pipeline run: %w", err)
	}
	return nil
}

// completeRunParamsFromCreateRunParams maps runtime persistence fields onto accepted-run completion fields.
func completeRunParamsFromCreateRunParams(params *createRunParams) *pipelineruns.CompleteRunParams {
	return &pipelineruns.CompleteRunParams{
		PipelineVersionID:          params.PipelineVersionID,
		DeploymentID:               params.DeploymentID,
		DeploymentRevision:         params.DeploymentRevision,
		DeploymentResolutionDigest: params.DeploymentResolutionDigest,
		PipelineSlug:               params.PipelineSlug,
		Status:                     params.Status,
		DurationMs:                 params.DurationMs,
		StepCount:                  params.StepCount,
		StepSlug:                   params.StepSlug,
		ErrorSummary:               params.ErrorSummary,
		FailureKind:                params.FailureKind,
		FailureSide:                params.FailureSide,
		StartedAt:                  params.StartedAt,
		EndedAt:                    params.EndedAt,
		ReplayOfRunSlug:            params.ReplayOfRunSlug,
		ReplayFromStep:             params.ReplayFromStep,
		Incremental:                params.Incremental,
		IncrementalDiagnostics:     params.IncrementalDiagnostics,
		Timeline:                   params.Timeline,
	}
}
