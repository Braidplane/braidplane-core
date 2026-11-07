package jobs

import (
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

const PipelineRunQueue = "pipeline_run"

// PipelineRunExecutionArgs is the durable River payload for one manually started run.
type PipelineRunExecutionArgs struct {
	CompletionSnapshotID       int                               `json:"completion_snapshot_id,omitempty"`
	ScheduledAt                time.Time                         `json:"scheduled_at,omitempty"`
	DeploymentResolutionDigest *string                           `json:"deployment_resolution_digest,omitempty"`
	OrganizationSlug           slug.Slug                         `json:"organization_slug" river:"unique"`
	ProjectSlug                slug.Slug                         `json:"project_slug" river:"unique"`
	EnvironmentSlug            slug.Slug                         `json:"environment_slug" river:"unique"`
	PipelineSlug               slug.Slug                         `json:"pipeline_slug" river:"unique"`
	PipelineVersionHash        string                            `json:"pipeline_version_hash" river:"unique"`
	RunSlug                    slug.Slug                         `json:"run_slug" river:"unique"`
	AcceptedAt                 time.Time                         `json:"accepted_at"`
	Actor                      model.EventActor                  `json:"actor"`
	Source                     pipelinerun.RunSourceSummary      `json:"source"`
	Incremental                pipelinerun.IncrementalRunOptions `json:"incremental,omitempty"`
	RuntimeInput               map[string]any                    `json:"runtime_input,omitempty"`
	ProtectedEnvironment       bool                              `json:"protected_environment"`
}

// Kind returns the stable River job kind for manual pipeline run execution.
func (PipelineRunExecutionArgs) Kind() string {
	return "pipeline_run_execution"
}

// InsertOpts schedules manual/API run work on the pipeline run queue.
func (a PipelineRunExecutionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       PipelineRunQueue,
		ScheduledAt: a.ScheduledAt,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByState: rivertype.UniqueOptsByStateDefault(),
		},
	}
}
