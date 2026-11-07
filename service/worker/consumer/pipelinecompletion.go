package consumer

import (
	"context"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type CompletionDispatcher interface {
	Dispatch(context.Context) error
}

type completionDispatchWorker struct {
	river.WorkerDefaults[jobs.PipelineCompletionDispatchArgs]
	dispatcher CompletionDispatcher
}

func NewCompletionDispatchWorker(dispatcher CompletionDispatcher) river.Worker[jobs.PipelineCompletionDispatchArgs] {
	if dispatcher == nil {
		panic("completion dispatcher is required")
	}
	return &completionDispatchWorker{dispatcher: dispatcher}
}

func (w *completionDispatchWorker) Work(ctx context.Context, _ *river.Job[jobs.PipelineCompletionDispatchArgs]) error {
	return w.dispatcher.Dispatch(ctx)
}
