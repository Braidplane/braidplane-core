package pipelinesvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type recordingPipelineGitTxManager struct {
	tx          postgres.DBTX
	calls       int
	rollbackErr error
}

func (m *recordingPipelineGitTxManager) RunInTx(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	m.calls++
	tx := m.tx
	if tx == nil {
		tx = &fakeDBTX{}
	}
	err := fn(ctx, tx)
	if err != nil {
		m.rollbackErr = err
	}
	return err
}

func (m *recordingPipelineGitTxManager) RunRead(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	tx := m.tx
	if tx == nil {
		tx = &fakeDBTX{}
	}
	return fn(ctx, tx)
}

type recordingPipelineGitProducer struct {
	err error

	writeOrganizationSlug slug.Slug
	writeDataSourceSlug   slug.Slug
	writePipelineSlug     slug.Slug
	writeBranch           *string
	writePath             string
	writeContent          []byte
	writeCommitMessage    string
	writeFailIfExists     bool

	publishArgs jobs.PipelineGitPublishProposalArgs
	refreshArgs jobs.PipelineGitRefreshProposalArgs

	deleteTx               postgres.DBTX
	deleteOrganizationSlug slug.Slug
	deleteDataSourceSlug   slug.Slug
	deletePipelineSlug     slug.Slug
	deleteBranch           *string
	deletePath             string
	deleteCommitMessage    string
}

func (p *recordingPipelineGitProducer) EnqueueWriteFile(
	_ context.Context,
	_ postgres.DBTX,
	orgSlug slug.Slug,
	dataSourceSlug slug.Slug,
	pipelineSlug slug.Slug,
	branch *string,
	path string,
	content []byte,
	commitMessage string,
	failIfExists bool,
) (queue.QueuedJob, error) {
	p.writeOrganizationSlug = orgSlug
	p.writeDataSourceSlug = dataSourceSlug
	p.writePipelineSlug = pipelineSlug
	p.writeBranch = branch
	p.writePath = path
	p.writeContent = content
	p.writeCommitMessage = commitMessage
	p.writeFailIfExists = failIfExists
	if p.err != nil {
		return queue.QueuedJob{}, p.err
	}
	return queue.QueuedJob{State: "queued"}, nil
}

func (p *recordingPipelineGitProducer) EnqueueDeleteFile(
	_ context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	dataSourceSlug slug.Slug,
	pipelineSlug slug.Slug,
	branch *string,
	path string,
	commitMessage string,
) (queue.QueuedJob, error) {
	p.deleteTx = tx
	p.deleteOrganizationSlug = orgSlug
	p.deleteDataSourceSlug = dataSourceSlug
	p.deletePipelineSlug = pipelineSlug
	p.deleteBranch = branch
	p.deletePath = path
	p.deleteCommitMessage = commitMessage
	if p.err != nil {
		return queue.QueuedJob{}, p.err
	}
	return queue.QueuedJob{State: "queued"}, nil
}

func (p *recordingPipelineGitProducer) EnqueuePublishProposal(
	_ context.Context,
	_ postgres.DBTX,
	args jobs.PipelineGitPublishProposalArgs,
) (queue.QueuedJob, error) {
	p.publishArgs = args
	if p.err != nil {
		return queue.QueuedJob{}, p.err
	}
	return queue.QueuedJob{State: "queued"}, nil
}

func (p *recordingPipelineGitProducer) EnqueueRefreshProposal(
	_ context.Context, _ postgres.DBTX, args jobs.PipelineGitRefreshProposalArgs,
) (queue.QueuedJob, error) {
	p.refreshArgs = args
	if p.err != nil {
		return queue.QueuedJob{}, p.err
	}
	return queue.QueuedJob{State: "queued", Kind: args.Kind()}, nil
}

type recordingPipelineRunProducer struct {
	err  error
	args jobs.PipelineRunExecutionArgs
}

func (p *recordingPipelineRunProducer) EnqueuePipelineRunExecution(
	_ context.Context,
	_ postgres.DBTX,
	args jobs.PipelineRunExecutionArgs,
) (queue.QueuedJob, error) {
	p.args = args
	if p.err != nil {
		return queue.QueuedJob{}, p.err
	}
	return queue.QueuedJob{State: "queued"}, nil
}
