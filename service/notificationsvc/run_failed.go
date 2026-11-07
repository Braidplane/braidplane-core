package notificationsvc

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// RunFailedCandidate is called only after the source owner rules out automatic retry.
func RunFailedCandidate(org, project, environment, pipeline, run, root slug.Slug, at time.Time) model.NotificationCandidate {
	if root == "" {
		root = run
	}
	identity := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%s", org, project, environment, root)))
	record := slug.Slug(fmt.Sprintf("run-failed-%x", identity))
	return model.NotificationCandidate{
		OrgSlug: org, ProjectSlug: &project, EnvironmentSlug: &environment, RecordSlug: &record,
		NotificationType: model.NotificationTypePipelineRunFailed, Severity: model.NotificationSeverityCritical,
		DedupeFingerprint: record.String(), ObservedAt: at,
		SourceEntityType: model.EventEntityTypePipelineRun, SourceAction: model.EventActionExecute,
		SourceRefs:   map[string]string{"project_slug": project.String(), "environment_slug": environment.String(), "pipeline_slug": pipeline.String(), "run_slug": run.String(), "root_run_slug": root.String()},
		SafeMetadata: map[string]any{"summary": "Pipeline run failed. No automatic retry will run."},
	}
}
