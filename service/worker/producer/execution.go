package producer

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type executionDispatcher struct{ enqueuer queue.Enqueuer }

func NewExecutionDispatcher(enqueuer queue.Enqueuer) (*executionDispatcher, error) {
	if enqueuer == nil {
		return nil, xerror.Unexpected().Detail("execution River enqueuer is required")
	}
	return &executionDispatcher{enqueuer: enqueuer}, nil
}

func (d *executionDispatcher) Dispatch(
	ctx context.Context,
	tx postgres.DBTX,
	requestID uuid.UUID,
) error {
	_, err := d.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Args:  jobs.ExecutionRequestArgs{RequestID: requestID},
		Queue: jobs.ExecutionQueue,
	})
	return err
}
