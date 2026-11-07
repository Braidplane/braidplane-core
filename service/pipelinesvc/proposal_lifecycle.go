package pipelinesvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type proposalUpdateRecorder interface {
	CompleteProposal(context.Context, pipelinegit.ProposalCompletion) error
	FailProposal(context.Context, pipelinegit.ProposalFailure) error
	CompleteProposalRefresh(context.Context, pipelinegit.ProposalRefreshCompletion) error
	FailProposalRefresh(context.Context, pipelinegit.ProposalRefreshFailure) error
}

type proposalUpdateAuthorizer interface {
	AuthorizeProposalPublish(context.Context, jobs.PipelineGitPublishProposalArgs) error
}

// ProposalLifecycle keeps creation state separate from imported-pipeline drafts.
type ProposalLifecycle struct {
	updates    proposalUpdateRecorder
	authorizer proposalUpdateAuthorizer
	creations  *CreationService
}

func NewProposalLifecycle(updates proposalUpdateRecorder, authorizer proposalUpdateAuthorizer, creations *CreationService) (*ProposalLifecycle, error) {
	if updates == nil || authorizer == nil || creations == nil {
		return nil, xerror.Unexpected().Detail("proposal lifecycle dependencies are required")
	}
	return &ProposalLifecycle{updates, authorizer, creations}, nil
}

func (s *ProposalLifecycle) AuthorizeProposalPublish(ctx context.Context, args jobs.PipelineGitPublishProposalArgs) error {
	if args.CreationSlug != "" {
		return s.creations.AuthorizeProposalPublish(ctx, args)
	}
	return s.authorizer.AuthorizeProposalPublish(ctx, args)
}

func (s *ProposalLifecycle) CompleteProposal(ctx context.Context, completed pipelinegit.ProposalCompletion) error {
	if completed.CreationSlug != "" {
		return s.creations.CompleteProposal(ctx, completed)
	}
	return s.updates.CompleteProposal(ctx, completed)
}

func (s *ProposalLifecycle) FailProposal(ctx context.Context, failed pipelinegit.ProposalFailure) error {
	if failed.CreationSlug != "" {
		return s.creations.FailProposal(ctx, failed)
	}
	return s.updates.FailProposal(ctx, failed)
}

func (s *ProposalLifecycle) CompleteProposalRefresh(ctx context.Context, completed pipelinegit.ProposalRefreshCompletion) error {
	return s.updates.CompleteProposalRefresh(ctx, completed)
}

func (s *ProposalLifecycle) FailProposalRefresh(ctx context.Context, failed pipelinegit.ProposalRefreshFailure) error {
	return s.updates.FailProposalRefresh(ctx, failed)
}
