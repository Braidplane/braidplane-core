package producer

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

// PipelineGitProducer enqueues pipeline git side effects for background execution.
type PipelineGitProducer interface {
	EnqueueWriteFile(
		ctx context.Context,
		tx postgres.DBTX,
		organizationSlug slug.Slug,
		dataSourceSlug slug.Slug,
		pipelineSlug slug.Slug,
		branch *string,
		path string,
		content []byte,
		commitMessage string,
		failIfExists bool,
	) (queue.QueuedJob, error)
	EnqueueDeleteFile(
		ctx context.Context,
		tx postgres.DBTX,
		organizationSlug slug.Slug,
		dataSourceSlug slug.Slug,
		pipelineSlug slug.Slug,
		branch *string,
		path string,
		commitMessage string,
	) (queue.QueuedJob, error)
	EnqueuePublishProposal(
		ctx context.Context,
		tx postgres.DBTX,
		args jobs.PipelineGitPublishProposalArgs,
	) (queue.QueuedJob, error)
	EnqueueRefreshProposal(
		ctx context.Context,
		tx postgres.DBTX,
		args jobs.PipelineGitRefreshProposalArgs,
	) (queue.QueuedJob, error)
}

func (p *pipelineGitProducer) EnqueueRefreshProposal(
	ctx context.Context,
	tx postgres.DBTX,
	args jobs.PipelineGitRefreshProposalArgs,
) (queue.QueuedJob, error) {
	return p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{Args: args, Queue: p.queue, InitiatingAccount: args.MemberSlug})
}

type pipelineGitProducer struct {
	enqueuer queue.Enqueuer
	queue    string
}

func NewPipelineGitProducer(enqueuer queue.Enqueuer) (PipelineGitProducer, error) {
	return NewPipelineGitProducerForQueue(enqueuer, jobs.PipelineGitQueue)
}

func NewPipelineGitProducerForQueue(enqueuer queue.Enqueuer, queueName string) (PipelineGitProducer, error) {
	if enqueuer == nil {
		return nil, xerror.Unexpected().Detail("queue enqueuer is required")
	}
	if strings.TrimSpace(queueName) == "" {
		return nil, xerror.Unexpected().Detail("pipeline git queue is required")
	}

	return &pipelineGitProducer{enqueuer: enqueuer, queue: queueName}, nil
}

func (p *pipelineGitProducer) EnqueueWriteFile(
	ctx context.Context,
	tx postgres.DBTX,
	organizationSlug slug.Slug,
	dataSourceSlug slug.Slug,
	pipelineSlug slug.Slug,
	branch *string,
	path string,
	content []byte,
	commitMessage string,
	failIfExists bool,
) (queue.QueuedJob, error) {
	return p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Args: jobs.PipelineGitWriteFileArgs{
			InitiatingUserSlug: executiondelivery.InitiatingUser(ctx),
			OrganizationSlug:   organizationSlug,
			DataSourceSlug:     dataSourceSlug,
			PipelineSlug:       pipelineSlug,
			Branch:             branch,
			Path:               path,
			Content:            content,
			CommitMessage:      commitMessage,
			FailIfExists:       failIfExists,
		},
		Queue:             p.queue,
		InitiatingAccount: executiondelivery.InitiatingUser(ctx),
	})
}

func (p *pipelineGitProducer) EnqueueDeleteFile(
	ctx context.Context,
	tx postgres.DBTX,
	organizationSlug slug.Slug,
	dataSourceSlug slug.Slug,
	pipelineSlug slug.Slug,
	branch *string,
	path string,
	commitMessage string,
) (queue.QueuedJob, error) {
	return p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Args: jobs.PipelineGitDeleteFileArgs{
			InitiatingUserSlug: executiondelivery.InitiatingUser(ctx),
			OrganizationSlug:   organizationSlug,
			DataSourceSlug:     dataSourceSlug,
			PipelineSlug:       pipelineSlug,
			Branch:             branch,
			Path:               path,
			CommitMessage:      commitMessage,
		},
		Queue:             p.queue,
		InitiatingAccount: executiondelivery.InitiatingUser(ctx),
	})
}

func (p *pipelineGitProducer) EnqueuePublishProposal(
	ctx context.Context,
	tx postgres.DBTX,
	args jobs.PipelineGitPublishProposalArgs,
) (queue.QueuedJob, error) {
	return p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Args:              args,
		Queue:             p.queue,
		InitiatingAccount: args.MemberSlug,
	})
}
