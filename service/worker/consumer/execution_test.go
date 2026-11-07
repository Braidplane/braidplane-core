package consumer

import (
	"context"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type fixedExecutionProcessor struct {
	result executiondelivery.ProcessResult
	err    error
}

func (p fixedExecutionProcessor) Process(
	context.Context,
	uuid.UUID,
) (executiondelivery.ProcessResult, error) {
	return p.result, p.err
}

func TestExecutionRequestWorkerHandlesProcessDisposition(t *testing.T) {
	t.Parallel()

	processErr := xerror.Unexpected().Detail("processor unavailable")
	tests := []struct {
		name        string
		processor   fixedExecutionProcessor
		wantErr     error
		wantMessage string
	}{
		{
			name: "done",
			processor: fixedExecutionProcessor{result: executiondelivery.ProcessResult{
				Disposition: executiondelivery.ProcessDone,
			}},
		},
		{
			name: "retry",
			processor: fixedExecutionProcessor{result: executiondelivery.ProcessResult{
				Disposition: executiondelivery.ProcessRetryAt,
				RetryAt:     time.Now(),
			}},
			wantMessage: "snooze execution request",
		},
		{
			name:      "processor error",
			processor: fixedExecutionProcessor{err: processErr},
			wantErr:   processErr,
		},
		{
			name: "invalid disposition",
			processor: fixedExecutionProcessor{result: executiondelivery.ProcessResult{
				Disposition: executiondelivery.ProcessDisposition(255),
			}},
			wantMessage: "invalid execution process disposition",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			worker := NewExecutionRequestWorker(test.processor)
			err := worker.Work(t.Context(), &river.Job[jobs.ExecutionRequestArgs]{
				Args: jobs.ExecutionRequestArgs{RequestID: uuid.New()},
			})
			switch {
			case test.wantErr != nil:
				require.ErrorIs(t, err, test.wantErr)
			case test.wantMessage != "":
				require.ErrorContains(t, err, test.wantMessage)
			default:
				require.NoError(t, err)
			}
		})
	}
}
