package consumer

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type pipelineWebhookExecutor interface {
	ExecuteDelivery(context.Context, jobs.PipelineWebhookDeliveryArgs) error
}

func NewPipelineWebhookDeliveryWorker(service pipelineWebhookExecutor, logger *slog.Logger) river.Worker[jobs.PipelineWebhookDeliveryArgs] {
	if service == nil {
		panic("nil pipelineWebhookExecutor passed to NewPipelineWebhookDeliveryWorker")
	}
	if logger == nil {
		panic("nil logger passed to NewPipelineWebhookDeliveryWorker")
	}
	return &pipelineWebhookDeliveryWorker{service: service, logger: logger}
}

type pipelineWebhookDeliveryWorker struct {
	river.WorkerDefaults[jobs.PipelineWebhookDeliveryArgs]
	service pipelineWebhookExecutor
	logger  *slog.Logger
}

func (w *pipelineWebhookDeliveryWorker) Work(ctx context.Context, job *river.Job[jobs.PipelineWebhookDeliveryArgs]) error {
	if err := w.service.ExecuteDelivery(ctx, job.Args); err != nil {
		return err
	}
	w.logger.InfoContext(ctx, "pipeline webhook delivery dispatched", slog.String("delivery_id", job.Args.DeliveryID))
	return nil
}
