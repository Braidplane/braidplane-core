package consumer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type noopPipelineWebhookExecutor struct{}

func (noopPipelineWebhookExecutor) ExecuteDelivery(context.Context, jobs.PipelineWebhookDeliveryArgs) error {
	return nil
}

func TestNewPipelineWebhookDeliveryWorkerRejectsNilLogger(t *testing.T) {
	require.PanicsWithValue(t, "nil logger passed to NewPipelineWebhookDeliveryWorker", func() {
		NewPipelineWebhookDeliveryWorker(noopPipelineWebhookExecutor{}, nil)
	})
}
