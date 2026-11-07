package producer

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

// DataSourceSchemaImportProducer enqueues data-source schema import work.
type DataSourceSchemaImportProducer interface {
	EnqueueDataSourceSchemaImport(
		ctx context.Context,
		tx postgres.DBTX,
		args jobs.DataSourceSchemaImportArgs,
	) (queue.QueuedJob, error)
}

type dataSourceSchemaImportProducer struct {
	enqueuer queue.Enqueuer
	queue    string
}

// NewDataSourceSchemaImportProducer creates a producer for the default schema import queue.
func NewDataSourceSchemaImportProducer(enqueuer queue.Enqueuer) (DataSourceSchemaImportProducer, error) {
	return NewDataSourceSchemaImportProducerForQueue(enqueuer, jobs.DataSourceSchemaImportQueue)
}

// NewDataSourceSchemaImportProducerForQueue creates a producer for tests and custom queues.
func NewDataSourceSchemaImportProducerForQueue(
	enqueuer queue.Enqueuer,
	queueName string,
) (DataSourceSchemaImportProducer, error) {
	if enqueuer == nil {
		return nil, xerror.Unexpected().Detail("queue enqueuer is required")
	}
	queueName = strings.TrimSpace(queueName)
	if queueName == "" {
		return nil, xerror.Unexpected().Detail("data source schema import queue is required")
	}

	return &dataSourceSchemaImportProducer{enqueuer: enqueuer, queue: queueName}, nil
}

// EnqueueDataSourceSchemaImport inserts one schema import job in tx.
func (p *dataSourceSchemaImportProducer) EnqueueDataSourceSchemaImport(
	ctx context.Context,
	tx postgres.DBTX,
	args jobs.DataSourceSchemaImportArgs,
) (queue.QueuedJob, error) {
	return p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Args:  args,
		Queue: p.queue,
	})
}
