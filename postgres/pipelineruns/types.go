package pipelineruns

import (
	"encoding/json"
	"time"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// PipelineRun represents a persisted pipeline execution record.
type PipelineRun struct {
	WorkerJobID                *int64
	DeploymentID               *int64
	DeploymentRevision         *int64
	DeploymentResolutionDigest *string
	PipelineVersionID          int64
	PipelineVersionSpecHash    string
	EnvironmentSlug            string
	APIVersion                 string
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
	Timeline                   json.RawMessage
	Incremental                pipelinerun.IncrementalRunSummary
	IncrementalDiagnostics     pipelinerun.IncrementalRunDiagnostics
	CreatedAt                  time.Time
}

// CreateRunParams contains required fields to persist a pipeline run.
type CreateRunParams struct {
	WorkerJobID                *int64
	DeploymentID               *int64
	DeploymentRevision         *int64
	DeploymentResolutionDigest *string
	PipelineVersionID          int64
	APIVersion                 string
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
	Timeline                   json.RawMessage
	Incremental                pipelinerun.IncrementalRunSummary
	IncrementalDiagnostics     json.RawMessage
}

// CompleteRunParams contains terminal execution fields for an accepted run.
type CompleteRunParams struct {
	PipelineVersionID                int64
	DeploymentID, DeploymentRevision *int64
	DeploymentResolutionDigest       *string
	PipelineSlug                     string
	Status                           string
	DurationMs                       int64
	StepCount                        int32
	StepSlug                         string
	ErrorSummary                     *string
	FailureKind                      *string
	FailureSide                      *string
	StartedAt                        time.Time
	EndedAt                          *time.Time
	ReplayOfRunSlug                  *string
	ReplayFromStep                   *string
	Timeline                         json.RawMessage
	Incremental                      pipelinerun.IncrementalRunSummary
	IncrementalDiagnostics           json.RawMessage
}

// RunListFilters narrows ListRuns results.
type RunListFilters struct {
	Status             *string
	PipelineSlug       *string
	Mode               *string
	BackfillSeriesSlug *string
}

// InterruptedRun is a bounded maintenance reference; it carries no queue payload.
type InterruptedRun struct {
	OrgSlug, ProjectSlug, EnvironmentSlug slug.Slug
	RunSlug, JobState                     string
}
