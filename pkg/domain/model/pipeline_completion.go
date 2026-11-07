package model

import "github.com/Braidplane/braidplane-core/pkg/slug"

// PipelineRunCompletion contains safe historical evidence, without execution authority or materialization.
type PipelineRunCompletion struct {
	PipelineSlug        slug.Slug
	Truncated           bool
	ContextUnavailable  bool
	RootRunSlug         slug.Slug
	Closed              bool
	WaitingForRetry     bool
	Outcome             *string
	FinalAttemptRunSlug *slug.Slug
	DecisionReason      *string
	Attempts            []PipelineCompletionAttempt
	Parent              *PipelineCompletionDecision
	Children            []PipelineCompletionDecision
}

type PipelineCompletionAttempt struct {
	RunSlug            slug.Slug
	AttemptNumber      int
	Status             string
	VersionHash        string
	DeploymentRevision *int64
}

type PipelineCompletionDecision struct {
	IsParent                   bool
	TriggerSlug                slug.Slug
	TriggerRevision            int64
	UpstreamPipelineSlug       slug.Slug
	RootRunSlug                slug.Slug
	FinalAttemptRunSlug        slug.Slug
	DownstreamPipelineSlug     slug.Slug
	Condition                  *PipelineCompletionCondition
	State                      string
	DecisionReason             *string
	ChildRunSlug               *slug.Slug
	ChildStatus                *string
	ChildOutcome               *string
	SelectedVersionHash        *string
	SelectedDeploymentRevision *int64
	SelectedResolutionDigest   *string
}

// PipelineRunConnected is a read-only projection, never a second execution status.
type PipelineRunConnected struct {
	SelectedRunSlug    slug.Slug
	Nodes              []*PipelineRunCompletion
	Truncated          bool
	ContextUnavailable bool
}
