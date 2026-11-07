package producer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestPipelineGitProducerEnqueuesWriteFile(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{
		queued: queue.QueuedJob{
			Kind:  "pipeline_git_write_file",
			Queue: jobs.PipelineGitQueue,
			State: "queued",
		},
	}
	producer, err := NewPipelineGitProducer(enqueuer)
	require.NoError(t, err)
	branch := "main"

	queued, err := producer.EnqueueWriteFile(
		executiondelivery.WithInitiatingUser(context.Background(), "alice"),
		nil,
		slug.Slug("acme"),
		slug.Slug("pipelines-git"),
		slug.Slug("daily-sync"),
		&branch,
		"pipelines/daily-sync.pipeline.yaml",
		[]byte("metadata:\n  slug: daily-sync\n"),
		"export pipeline daily-sync",
		true,
	)

	require.NoError(t, err)
	require.Equal(t, enqueuer.queued, queued)
	require.Equal(t, 1, enqueuer.calls)
	require.Nil(t, enqueuer.tx)
	require.Equal(t, jobs.PipelineGitQueue, enqueuer.req.Queue)
	require.Equal(t, slug.Slug("alice"), enqueuer.req.InitiatingAccount)
	require.Equal(t, jobs.PipelineGitWriteFileArgs{
		InitiatingUserSlug: "alice",
		OrganizationSlug:   slug.Slug("acme"),
		DataSourceSlug:     slug.Slug("pipelines-git"),
		PipelineSlug:       slug.Slug("daily-sync"),
		Branch:             &branch,
		Path:               "pipelines/daily-sync.pipeline.yaml",
		Content:            []byte("metadata:\n  slug: daily-sync\n"),
		CommitMessage:      "export pipeline daily-sync",
		FailIfExists:       true,
	}, enqueuer.req.Args)
}

func TestPipelineGitProducerEnqueuesDeleteFile(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{
		queued: queue.QueuedJob{
			Kind:  "pipeline_git_delete_file",
			Queue: jobs.PipelineGitQueue,
			State: "skipped_duplicate",
		},
	}
	producer, err := NewPipelineGitProducer(enqueuer)
	require.NoError(t, err)

	queued, err := producer.EnqueueDeleteFile(
		context.Background(),
		nil,
		slug.Slug("acme"),
		slug.Slug("pipelines-git"),
		slug.Slug("daily-sync"),
		nil,
		"pipelines/daily-sync.pipeline.yaml",
		"remove pipeline daily-sync",
	)

	require.NoError(t, err)
	require.Equal(t, enqueuer.queued, queued)
	require.Equal(t, 1, enqueuer.calls)
	require.Equal(t, jobs.PipelineGitQueue, enqueuer.req.Queue)
	require.Equal(t, jobs.PipelineGitDeleteFileArgs{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Path:             "pipelines/daily-sync.pipeline.yaml",
		CommitMessage:    "remove pipeline daily-sync",
	}, enqueuer.req.Args)
}

func TestPipelineGitProducerEnqueuesPublishProposal(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{
		queued: queue.QueuedJob{
			Kind:  "pipeline_git_publish_proposal",
			Queue: jobs.PipelineGitQueue,
			State: "queued",
		},
	}
	producer, err := NewPipelineGitProducer(enqueuer)
	require.NoError(t, err)

	baseRef := "refs/heads/main"
	baseCommitSHA := "abc123"
	queued, err := producer.EnqueuePublishProposal(
		context.Background(),
		nil,
		jobs.PipelineGitPublishProposalArgs{
			OrganizationSlug: slug.Slug("acme"),
			ProjectSlug:      slug.Slug("default"),
			DataSourceSlug:   slug.Slug("pipelines-git"),
			PipelineSlug:     slug.Slug("daily-sync"),
			MemberSlug:       slug.Slug("member-a"),
			BaseRef:          &baseRef,
			BaseCommitSHA:    &baseCommitSHA,
			BranchName:       "braidplane/default/daily-sync/member-a",
			Path:             "pipelines/daily-sync.pipeline.yaml",
			Content:          []byte("metadata:\n  slug: daily-sync\n"),
			CommitMessage:    "propose pipeline daily-sync",
			ProposalTitle:    "Propose pipeline daily-sync",
			ProposalBody:     "Pipeline draft proposed by Braidplane.",
		},
	)

	require.NoError(t, err)
	require.Equal(t, enqueuer.queued, queued)
	require.Equal(t, 1, enqueuer.calls)
	require.Equal(t, jobs.PipelineGitQueue, enqueuer.req.Queue)
	require.Equal(t, jobs.PipelineGitPublishProposalArgs{
		OrganizationSlug: slug.Slug("acme"),
		ProjectSlug:      slug.Slug("default"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		MemberSlug:       slug.Slug("member-a"),
		BaseRef:          &baseRef,
		BaseCommitSHA:    &baseCommitSHA,
		BranchName:       "braidplane/default/daily-sync/member-a",
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("metadata:\n  slug: daily-sync\n"),
		CommitMessage:    "propose pipeline daily-sync",
		ProposalTitle:    "Propose pipeline daily-sync",
		ProposalBody:     "Pipeline draft proposed by Braidplane.",
	}, enqueuer.req.Args)
}

func TestPipelineGitProducerReturnsEnqueueError(t *testing.T) {
	t.Parallel()

	enqueueErr := xerror.Unexpected().Detail("queue unavailable")
	producer, err := NewPipelineGitProducer(&recordingEnqueuer{err: enqueueErr})
	require.NoError(t, err)

	queued, err := producer.EnqueueWriteFile(
		context.Background(),
		nil,
		slug.Slug("acme"),
		slug.Slug("pipelines-git"),
		slug.Slug("daily-sync"),
		nil,
		"pipelines/daily-sync.pipeline.yaml",
		[]byte("metadata:\n  slug: daily-sync\n"),
		"export pipeline daily-sync",
		false,
	)

	require.Empty(t, queued)
	require.ErrorIs(t, err, enqueueErr)
}

func TestNewPipelineGitProducerRequiresEnqueuer(t *testing.T) {
	t.Parallel()

	producer, err := NewPipelineGitProducer(nil)

	require.Nil(t, producer)
	require.ErrorContains(t, err, "queue enqueuer is required")
}

type recordingEnqueuer struct {
	calls  int
	tx     postgres.DBTX
	req    queue.EnqueueRequest
	queued queue.QueuedJob
	err    error
}

func (e *recordingEnqueuer) EnqueueTx(
	_ context.Context,
	tx postgres.DBTX,
	req queue.EnqueueRequest,
) (queue.QueuedJob, error) {
	e.calls++
	e.tx = tx
	e.req = req
	return e.queued, e.err
}
