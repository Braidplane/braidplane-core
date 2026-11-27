package pipelineschedulesvc

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
)

func scheduleNotificationCandidate(fire pipelinetriggers.DelayedScheduleFire, kind model.NotificationType) model.NotificationCandidate {
	ref := ScheduleFireExecutionRef{
		OrgSlug: fire.OrgSlug, ProjectSlug: fire.ProjectSlug, EnvironmentSlug: fire.EnvironmentSlug,
		TriggerSlug: fire.TriggerSlug, TriggerRevision: fire.TriggerRevision, ScheduledFireAt: fire.ScheduledFireAt,
	}
	candidate := scheduleOccurrenceNotificationCandidate(ref, fire.PipelineSlug, kind)
	candidate.ObservedAt = fire.ScheduledFireAt.Add(scheduleOnTimeGrace)
	summary := "Scheduled pipeline fire exceeded the one-minute completion grace window."
	if kind == model.NotificationTypePipelineScheduledRunMissing {
		candidate.ObservedAt = fire.ActualFireAt
		candidate.Severity = model.NotificationSeverityCritical
		summary = "Scheduled pipeline fire reached a terminal state without creating a run."
	}
	candidate.SafeMetadata = map[string]any{"summary": summary, "grace_seconds": int(scheduleOnTimeGrace.Seconds())}
	return candidate
}

func preflightFailureNotificationCandidate(ref ScheduleFireExecutionRef, pipeline slug.Slug, observedAt time.Time, reasonCode string) model.NotificationCandidate {
	candidate := scheduleOccurrenceNotificationCandidate(ref, pipeline, model.NotificationTypePipelineScheduledPreflightFailed)
	candidate.ObservedAt = observedAt
	reason := "Scheduled preflight validation failed."
	switch reasonCode {
	case "SCHEMA_REFRESH_FAILED":
		reason = "Source schema refresh failed during scheduled preflight."
	case "PREFLIGHT_TIMED_OUT":
		reason = "Scheduled preflight timed out."
	}
	candidate.SafeMetadata = map[string]any{
		"summary":     fmt.Sprintf("%s Pipeline %q in %q was scheduled for %s. Review the history of schedule %q.", reason, pipeline, ref.EnvironmentSlug, candidate.SourceRefs["scheduled_fire_at"], ref.TriggerSlug),
		"reason_code": reasonCode,
	}
	return candidate
}

func scheduleOccurrenceNotificationCandidate(ref ScheduleFireExecutionRef, pipeline slug.Slug, kind model.NotificationType) model.NotificationCandidate {
	scheduledAt := ref.ScheduledFireAt.UTC().Format(time.RFC3339Nano)
	identity := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%s/%d/%s/%s", ref.OrgSlug, ref.ProjectSlug, ref.EnvironmentSlug, ref.TriggerSlug, ref.TriggerRevision, scheduledAt, kind)))
	record := slug.Slug(fmt.Sprintf("schedule-%x", identity))
	return model.NotificationCandidate{
		OrgSlug: ref.OrgSlug, ProjectSlug: &ref.ProjectSlug, EnvironmentSlug: &ref.EnvironmentSlug, RecordSlug: &record,
		NotificationType: kind, Severity: model.NotificationSeverityWarning, DedupeFingerprint: record.String(),
		SourceEntityType: model.EventEntityTypePipelineTrigger, SourceAction: model.EventActionExecute,
		SourceRefs: map[string]string{
			"project_slug": ref.ProjectSlug.String(), "environment_slug": ref.EnvironmentSlug.String(),
			"pipeline_slug": pipeline.String(), "trigger_slug": ref.TriggerSlug.String(), "source_ref": ref.TriggerSlug.String(),
			"trigger_revision": strconv.FormatInt(ref.TriggerRevision, 10), "scheduled_fire_at": scheduledAt,
		},
	}
}
