package jobs

import (
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

const PipelineRetryQueue = "pipeline_retry"

// PipelineTriggerRetryExecutionArgs is the durable River payload for one trigger retry attempt.
type PipelineTriggerRetryExecutionArgs struct {
	FinalizeOnly     bool      `json:"finalize_only,omitempty" river:"unique"`
	OrganizationSlug slug.Slug `json:"organization_slug" river:"unique"`
	ProjectSlug      slug.Slug `json:"project_slug" river:"unique"`
	EnvironmentSlug  slug.Slug `json:"environment_slug" river:"unique"`
	RetryAttemptSlug slug.Slug `json:"retry_attempt_slug" river:"unique"`
	ScheduledAt      time.Time `json:"scheduled_at"`
}

// Kind returns the stable River job kind for trigger retry execution.
func (PipelineTriggerRetryExecutionArgs) Kind() string {
	return "pipeline_trigger_retry_execution"
}

// InsertOpts schedules retry work on the retry queue with argument-level uniqueness.
func (a PipelineTriggerRetryExecutionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       PipelineRetryQueue,
		ScheduledAt: a.ScheduledAt,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByState: rivertype.UniqueOptsByStateDefault(),
		},
	}
}
