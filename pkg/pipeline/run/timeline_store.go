package run

import (
	"context"
	"time"

	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"

	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	WorkerObservationInterval   = 5 * time.Second
	WorkerObservationStaleAfter = 15 * time.Second
)

var (
	ErrTimelineNotFound              = xerror.NotFound(xerror.PipelineRun)
	ErrReplayCheckpointNotFound      = xerror.NotFound(xerror.PipelineCheckpoint)
	ErrIncrementalCheckpointNotFound = xerror.NotFound(xerror.PipelineCheckpoint)
)

// RunPersister stores pipeline run metadata and timeline externally.
type RunPersister interface {
	PersistRun(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		pipeline *pipelinespec.Pipeline,
		runResult *RunResult,
	) error
}

// TimelineStore retrieves persisted run timelines and replay checkpoints.
type TimelineStore interface {
	ObserveWorker(context.Context, slug.Slug, slug.Slug, slug.Slug, string, time.Time) error
	Get(ctx context.Context, orgSlug, projectSlug, environmentSlug slug.Slug, runSlug string) (*RunTimeline, error)
	SaveCheckpoint(
		ctx context.Context, orgSlug, projectSlug, environmentSlug slug.Slug, runSlug string, checkpoint ReplayCheckpoint,
	) error
	GetCheckpoint(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		runSlug string,
	) (*ReplayCheckpoint, error)
}

type IncrementalCheckpointReader interface {
	GetCurrentIncrementalCheckpoint(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		lookup IncrementalCheckpointLookup,
	) (*IncrementalCheckpoint, error)
}

// RunStore persists completed runs and provides replay state access.
type RunStore interface {
	PreviousSuccess(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, int64, time.Time) (*pipelineruntime.SuccessfulRun, error)
	RunPersister
	TimelineStore
	IncrementalCheckpointReader
}
