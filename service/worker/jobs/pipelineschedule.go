package jobs

import (
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

const PipelineScheduleQueue = "pipeline_schedule"

// PipelineScheduleDispatchArgs is the periodic River payload that scans due schedules.
type PipelineScheduleDispatchArgs struct{}

func (PipelineScheduleDispatchArgs) Kind() string {
	return "pipeline_schedule_dispatch"
}

func (PipelineScheduleDispatchArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: PipelineScheduleQueue}
}

// PipelineScheduleFireExecutionArgs is the durable River payload for executing one claimed scheduled fire.
type PipelineScheduleFireExecutionArgs struct {
	FinalizeOnly     bool      `json:"finalize_only,omitempty" river:"unique"`
	OrganizationSlug slug.Slug `json:"organization_slug" river:"unique"`
	ProjectSlug      slug.Slug `json:"project_slug" river:"unique"`
	EnvironmentSlug  slug.Slug `json:"environment_slug" river:"unique"`
	TriggerSlug      slug.Slug `json:"trigger_slug" river:"unique"`
	TriggerRevision  int64     `json:"trigger_revision" river:"unique"`
	ScheduledFireAt  time.Time `json:"scheduled_fire_at" river:"unique"`
}

func (PipelineScheduleFireExecutionArgs) Kind() string {
	return "pipeline_schedule_fire_execution"
}

func (PipelineScheduleFireExecutionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: PipelineScheduleQueue,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByState: rivertype.UniqueOptsByStateDefault(),
		},
	}
}
