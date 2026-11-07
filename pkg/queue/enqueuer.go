package queue

import (
	"context"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
)

// JobArgs identifies a durable River job payload and its enqueue options.
type JobArgs interface {
	Kind() string
	InsertOpts() river.InsertOpts
}

// Enqueuer is the service-facing port for durable background work.
type Enqueuer interface {
	EnqueueTx(ctx context.Context, tx postgres.DBTX, req EnqueueRequest) (QueuedJob, error)
}

// EnqueueRequest describes a job that should become visible when tx commits.
type EnqueueRequest struct {
	Args  JobArgs
	Queue string
	// InitiatingAccount serializes account-bound admission with account removal.
	// Leave empty for organization/system work that must survive its initiator.
	InitiatingAccount slug.Slug
}

const (
	queuedJobStateQueued           = "queued"
	queuedJobStateSkippedDuplicate = "skipped_duplicate"
)

// QueuedJob reports the enqueue outcome, not job completion.
type QueuedJob struct {
	ID    int64 `json:"-"`
	Kind  string
	Queue string
	State string
}

type enqueuer struct {
	backend enqueueBackend
}

type enqueueBackend interface {
	enqueueTx(ctx context.Context, tx postgres.DBTX, req EnqueueRequest) (int64, bool, error)
}

func NewEnqueuer(client *Client) Enqueuer {
	if client == nil {
		return &enqueuer{}
	}

	return &enqueuer{backend: client}
}

func (e *enqueuer) EnqueueTx(
	ctx context.Context,
	tx postgres.DBTX,
	req EnqueueRequest,
) (QueuedJob, error) {
	if req.Args == nil {
		return QueuedJob{}, xerror.Unexpected().Detail("queue job args are required")
	}
	if e.backend == nil {
		return QueuedJob{}, xerror.Unexpected().Detail("queue client is required")
	}

	id, skippedDuplicate, err := e.backend.enqueueTx(ctx, tx, req)
	if err != nil {
		return QueuedJob{}, xerror.Wrapf("enqueue job: %w", err)
	}

	state := queuedJobStateQueued
	if skippedDuplicate {
		state = queuedJobStateSkippedDuplicate
	}

	return QueuedJob{
		ID:    id,
		Kind:  req.Args.Kind(),
		Queue: req.Queue,
		State: state,
	}, nil
}
