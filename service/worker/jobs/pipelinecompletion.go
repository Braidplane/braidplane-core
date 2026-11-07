package jobs

import "github.com/riverqueue/river"

type PipelineCompletionDispatchArgs struct{}

func (PipelineCompletionDispatchArgs) Kind() string { return "pipeline_completion_dispatch" }

func (PipelineCompletionDispatchArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: PipelineScheduleQueue}
}
