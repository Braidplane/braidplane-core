package queue

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
)

func TestEnqueuer_EnqueueTxReturnsQueuedJobMetadata(t *testing.T) {
	t.Parallel()

	tx := &fakeEnqueueDBTX{}
	backend := &recordingEnqueueBackend{}
	enqueuer := &enqueuer{backend: backend}

	queued, err := enqueuer.EnqueueTx(context.Background(), tx, EnqueueRequest{
		Args:              enqueueTestJobArgs{pipelineSlug: "daily-sync"},
		Queue:             "git-side-effects",
		InitiatingAccount: "alice",
	})

	require.NoError(t, err)
	require.Equal(t, QueuedJob{
		ID: 42, Kind: "pipeline_git_sync",
		Queue: "git-side-effects",
		State: "queued",
	}, queued)
	require.Equal(t, 1, backend.calls)
	require.Same(t, tx, backend.tx)
	require.Equal(t, "pipeline_git_sync", backend.args.Kind())
	require.Equal(t, "git-side-effects", backend.queue)
	require.Equal(t, slug.Slug("alice"), backend.account)
}

func TestEnqueuer_EnqueueTxReturnsSkippedDuplicateWhenBackendDeduplicates(t *testing.T) {
	t.Parallel()

	tx := &fakeEnqueueDBTX{}
	backend := &recordingEnqueueBackend{skippedDuplicate: true}
	enqueuer := &enqueuer{backend: backend}

	queued, err := enqueuer.EnqueueTx(context.Background(), tx, EnqueueRequest{
		Args:  enqueueTestJobArgs{pipelineSlug: "daily-sync"},
		Queue: "git-side-effects",
	})

	require.NoError(t, err)
	require.Equal(t, QueuedJob{
		ID: 42, Kind: "pipeline_git_sync",
		Queue: "git-side-effects",
		State: "skipped_duplicate",
	}, queued)
	require.Equal(t, 1, backend.calls)
	require.Same(t, tx, backend.tx)
	require.Equal(t, "pipeline_git_sync", backend.args.Kind())
	require.Equal(t, "git-side-effects", backend.queue)
}

func TestEnqueuer_EnqueueTxRequiresJobArgs(t *testing.T) {
	t.Parallel()

	backend := &recordingEnqueueBackend{}
	enqueuer := &enqueuer{backend: backend}

	queued, err := enqueuer.EnqueueTx(context.Background(), &fakeEnqueueDBTX{}, EnqueueRequest{
		Queue: "git-side-effects",
	})

	require.Equal(t, QueuedJob{}, queued)
	require.ErrorContains(t, err, "queue job args are required")
	require.Equal(t, 0, backend.calls)
}

func TestEnqueuer_EnqueueTxWrapsBackendError(t *testing.T) {
	t.Parallel()

	backendErr := xerror.Unexpected().Detail("river insert failed")
	backend := &recordingEnqueueBackend{err: backendErr}
	enqueuer := &enqueuer{backend: backend}

	queued, err := enqueuer.EnqueueTx(context.Background(), &fakeEnqueueDBTX{}, EnqueueRequest{
		Args:  enqueueTestJobArgs{pipelineSlug: "daily-sync"},
		Queue: "git-side-effects",
	})

	require.Equal(t, QueuedJob{}, queued)
	require.ErrorIs(t, err, backendErr)
	require.ErrorContains(t, err, "enqueue job")
	require.Equal(t, 1, backend.calls)
}

type recordingEnqueueBackend struct {
	account          slug.Slug
	calls            int
	tx               postgres.DBTX
	args             JobArgs
	queue            string
	skippedDuplicate bool
	err              error
}

func (b *recordingEnqueueBackend) enqueueTx(
	_ context.Context,
	tx postgres.DBTX,
	req EnqueueRequest,
) (int64, bool, error) {
	b.calls++
	b.tx = tx
	b.args = req.Args
	b.queue = req.Queue
	b.account = req.InitiatingAccount

	return 42, b.skippedDuplicate, b.err
}

type enqueueTestJobArgs struct {
	pipelineSlug string
}

func (a enqueueTestJobArgs) Kind() string {
	return "pipeline_git_sync"
}

func (a enqueueTestJobArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "pipeline_git"}
}

type fakeEnqueueDBTX struct{}

func (f *fakeEnqueueDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, xerror.Unexpected().Detail("fake enqueue tx exec should not be called")
}

func (f *fakeEnqueueDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, xerror.Unexpected().Detail("fake enqueue tx query should not be called")
}

func (f *fakeEnqueueDBTX) QueryRow(context.Context, string, ...any) pgx.Row {
	return fakeEnqueueRow{}
}

type fakeEnqueueRow struct{}

func (fakeEnqueueRow) Scan(...any) error {
	return xerror.Unexpected().Detail("fake enqueue tx query row should not be called")
}
