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
	identity := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%s/%d/%s/%s", fire.OrgSlug, fire.ProjectSlug, fire.EnvironmentSlug, fire.TriggerSlug, fire.TriggerRevision, fire.ScheduledFireAt.UTC().Format(time.RFC3339Nano), kind)))
	record := slug.Slug(fmt.Sprintf("schedule-%x", identity))
	observedAt := fire.ScheduledFireAt.Add(scheduleOnTimeGrace)
	severity, summary := model.NotificationSeverityWarning, "Scheduled pipeline fire exceeded the one-minute completion grace window."
	if kind == model.NotificationTypePipelineScheduledRunMissing {
		observedAt = fire.ActualFireAt
		severity, summary = model.NotificationSeverityCritical, "Scheduled pipeline fire reached a terminal state without creating a run."
	}
	return model.NotificationCandidate{
		OrgSlug: fire.OrgSlug, ProjectSlug: &fire.ProjectSlug, EnvironmentSlug: &fire.EnvironmentSlug, RecordSlug: &record,
		NotificationType: kind, Severity: severity, DedupeFingerprint: record.String(), ObservedAt: observedAt,
		SourceEntityType: model.EventEntityTypePipelineTrigger, SourceAction: model.EventActionExecute,
		SourceRefs:   map[string]string{"project_slug": fire.ProjectSlug.String(), "environment_slug": fire.EnvironmentSlug.String(), "pipeline_slug": fire.PipelineSlug.String(), "trigger_slug": fire.TriggerSlug.String(), "source_ref": fire.TriggerSlug.String(), "trigger_revision": strconv.FormatInt(fire.TriggerRevision, 10), "scheduled_fire_at": fire.ScheduledFireAt.UTC().Format(time.RFC3339Nano)},
		SafeMetadata: map[string]any{"summary": summary, "grace_seconds": int(scheduleOnTimeGrace.Seconds())},
	}
}
