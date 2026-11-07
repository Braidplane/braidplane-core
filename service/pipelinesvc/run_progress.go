package pipelinesvc

import (
	"context"
	"encoding/json"
	"time"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
)

type RunProgress struct {
	CompiledPlanDigest, DeploymentResolutionDigest              *string
	CompletedSteps, FailedSteps, PendingSteps, InterruptedSteps int
	CurrentStep                                                 *string
	Metadata                                                    PipelineRunMetadata
	Revision, AfterEvent, NextAfterEvent, EventCount            int64
	Events                                                      []pipelinerun.TimelineEvent
	HasMore                                                     bool
	WorkerObservedAt, ObservedAt                                *time.Time
	WorkerLiveness                                              pipelinerun.WorkerLiveness
	ServerTime                                                  time.Time
}

func (s *service) GetRunProgress(ctx context.Context, org, project, environment slug.Slug, run string, after int64, limit int) (*RunProgress, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, xerror.InvalidInput(xerror.PipelineRun).Detail("after_event must be nonnegative and limit must be between 1 and 100")
	}
	record, err := s.runRepo.FindRunBySlug(ctx, s.dbtx, org, project, environment, run)
	if xerror.Is(err, pipelineruns.ErrRunNotFound) {
		return nil, ErrPipelineRunNotFound.With(err)
	}
	if err != nil {
		return nil, xerror.Wrapf("read pipeline run progress: %w", err)
	}
	var timeline pipelinerun.RunTimeline
	if err := json.Unmarshal(record.Timeline, &timeline); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return projectRunProgress(record, timeline, after, limit, s.executionClock.Now().UTC())
}

func projectRunProgress(record *pipelineruns.PipelineRun, timeline pipelinerun.RunTimeline, after int64, limit int, now time.Time) (*RunProgress, error) {
	count := int64(len(timeline.Events))
	if after > count {
		return nil, xerror.InvalidInput(xerror.PipelineRun).Detail("after_event exceeds retained history; resynchronize from zero")
	}
	next := after + min(int64(limit), count-after)
	progress := &RunProgress{
		Metadata: *toPipelineRunMetadata(record), Revision: timeline.Revision,
		DeploymentResolutionDigest: record.DeploymentResolutionDigest,
		AfterEvent:                 after, NextAfterEvent: next, EventCount: count,
		Events: append([]pipelinerun.TimelineEvent{}, timeline.Events[after:next]...), HasMore: next < count,
		WorkerObservedAt: timeline.WorkerObservedAt, WorkerLiveness: pipelinerun.WorkerLivenessUnavailable, ServerTime: now,
	}
	if timeline.CompiledPlanDigest != "" {
		progress.CompiledPlanDigest = &timeline.CompiledPlanDigest
	}
	states := map[slug.Slug]string{}
	for _, event := range timeline.Events {
		if event.Step == "" {
			continue
		}
		switch event.Type {
		case "step_started":
			states[event.Step] = "running"
			if record.Status == "running" {
				step := event.Step.String()
				progress.CurrentStep = &step
			}
		case "step_succeeded", "step_reused", "step_failed":
			states[event.Step] = event.Status
			if progress.CurrentStep != nil && *progress.CurrentStep == event.Step.String() {
				progress.CurrentStep = nil
			}
		}
	}
	for _, status := range states {
		switch status {
		case "succeeded":
			progress.CompletedSteps++
		case "failed":
			progress.FailedSteps++
		case "running":
			if record.Status != "running" && record.Status != "queued" {
				progress.InterruptedSteps++
			}
		}
	}
	progress.PendingSteps = max(0, int(record.StepCount)-len(states))
	observe := func(at *time.Time) {
		if at != nil && (progress.ObservedAt == nil || at.After(*progress.ObservedAt)) {
			observedAt := *at
			progress.ObservedAt = &observedAt
		}
	}
	observe(timeline.WorkerObservedAt)
	for _, event := range timeline.Events {
		observe(&event.At)
	}
	for _, check := range timeline.StructuralChecks {
		observe(check.StartedAt)
		observe(check.CompletedAt)
	}
	switch record.Status {
	case "succeeded", "failed", "canceled":
		progress.WorkerLiveness = pipelinerun.WorkerLivenessTerminal
	case "running":
		if timeline.WorkerObservedAt != nil {
			age := now.Sub(*timeline.WorkerObservedAt)
			if age >= 0 && age <= pipelinerun.WorkerObservationStaleAfter {
				progress.WorkerLiveness = pipelinerun.WorkerLivenessRecent
			}
			if age > pipelinerun.WorkerObservationStaleAfter {
				progress.WorkerLiveness = pipelinerun.WorkerLivenessStale
			}
		}
	}
	return progress, nil
}
