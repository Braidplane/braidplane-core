package consumer

import (
	"context"
	"log/slog"

	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

// PipelineGitExecutor performs git side effects for pipeline jobs.
type PipelineGitExecutor interface {
	WriteFile(ctx context.Context, args jobs.PipelineGitWriteFileArgs) error
	DeleteFile(ctx context.Context, args jobs.PipelineGitDeleteFileArgs) error
	PublishProposal(ctx context.Context, args jobs.PipelineGitPublishProposalArgs) (*pipelinegit.ProposalResult, error)
	RefreshProposal(ctx context.Context, args jobs.PipelineGitRefreshProposalArgs) (*pipelinegit.ProposalResult, error)
}

// PipelineGitProposalRecorder persists safe proposal metadata after provider execution.
type PipelineGitProposalRecorder interface {
	CompleteProposal(ctx context.Context, completed pipelinegit.ProposalCompletion) error
	FailProposal(ctx context.Context, failed pipelinegit.ProposalFailure) error
	CompleteProposalRefresh(ctx context.Context, completed pipelinegit.ProposalRefreshCompletion) error
	FailProposalRefresh(ctx context.Context, failed pipelinegit.ProposalRefreshFailure) error
}

type PipelineGitProposalAuthorizer interface {
	AuthorizeProposalPublish(ctx context.Context, args jobs.PipelineGitPublishProposalArgs) error
}

type pipelineGitExecutorAdapter struct {
	fileExecutor     pipelinegit.FileExecutor
	proposalExecutor pipelinegit.ProposalExecutor
}

func NewPipelineGitExecutor(
	fileExecutor pipelinegit.FileExecutor,
	proposalExecutor pipelinegit.ProposalExecutor,
) (PipelineGitExecutor, error) {
	if fileExecutor == nil {
		return nil, xerror.Unexpected().Detail("pipeline git file executor is required")
	}
	if proposalExecutor == nil {
		return nil, xerror.Unexpected().Detail("pipeline git proposal executor is required")
	}

	return pipelineGitExecutorAdapter{fileExecutor: fileExecutor, proposalExecutor: proposalExecutor}, nil
}

func (a pipelineGitExecutorAdapter) WriteFile(
	ctx context.Context,
	args jobs.PipelineGitWriteFileArgs,
) error {
	ctx = executiondelivery.WithInitiatingUser(ctx, args.InitiatingUserSlug)
	return a.fileExecutor.WriteFile(ctx, pipelinegit.WriteFileOperation{
		OrganizationSlug: args.OrganizationSlug,
		DataSourceSlug:   args.DataSourceSlug,
		PipelineSlug:     args.PipelineSlug,
		Branch:           args.Branch,
		Path:             args.Path,
		Content:          args.Content,
		CommitMessage:    args.CommitMessage,
		FailIfExists:     args.FailIfExists,
	})
}

func (a pipelineGitExecutorAdapter) DeleteFile(
	ctx context.Context,
	args jobs.PipelineGitDeleteFileArgs,
) error {
	ctx = executiondelivery.WithInitiatingUser(ctx, args.InitiatingUserSlug)
	return a.fileExecutor.DeleteFile(ctx, pipelinegit.DeleteFileOperation{
		OrganizationSlug: args.OrganizationSlug,
		DataSourceSlug:   args.DataSourceSlug,
		PipelineSlug:     args.PipelineSlug,
		Branch:           args.Branch,
		Path:             args.Path,
		CommitMessage:    args.CommitMessage,
	})
}

func (a pipelineGitExecutorAdapter) PublishProposal(
	ctx context.Context,
	args jobs.PipelineGitPublishProposalArgs,
) (*pipelinegit.ProposalResult, error) {
	ctx = executiondelivery.WithInitiatingUser(ctx, args.MemberSlug)
	if a.proposalExecutor == nil {
		return nil, xerror.ExecutionUnavailable().Detail("pipeline git proposal executor is required")
	}
	return a.proposalExecutor.PublishProposal(ctx, pipelinegit.ProposalOperation{
		CreateOnly:       args.CreationSlug != "",
		OrganizationSlug: args.OrganizationSlug,
		DataSourceSlug:   args.DataSourceSlug,
		PipelineSlug:     args.PipelineSlug,
		BaseRef:          args.BaseRef,
		BaseCommitSHA:    args.BaseCommitSHA,
		ExpectedHeadSHA:  args.ExpectedHeadSHA,
		BranchName:       args.BranchName,
		Path:             args.Path,
		Content:          args.Content,
		CommitMessage:    args.CommitMessage,
		ProposalTitle:    args.ProposalTitle,
		ProposalBody:     args.ProposalBody,
		Authority:        args.Authority,
	})
}

func (a pipelineGitExecutorAdapter) RefreshProposal(
	ctx context.Context,
	args jobs.PipelineGitRefreshProposalArgs,
) (*pipelinegit.ProposalResult, error) {
	ctx = executiondelivery.WithInitiatingUser(ctx, args.MemberSlug)
	return a.proposalExecutor.RefreshProposal(ctx, pipelinegit.ProposalRefreshOperation{
		OrganizationSlug: args.OrganizationSlug,
		DataSourceSlug:   args.DataSourceSlug,
		ProposalID:       args.ProposalID,
	})
}

func NewPipelineGitWriteFileWorker(
	pipelineGit PipelineGitExecutor,
	logger *slog.Logger,
) river.Worker[jobs.PipelineGitWriteFileArgs] {
	return &pipelineGitWriteFileWorker{
		pipelineGit: pipelineGit,
		logger:      logger,
	}
}

func NewPipelineGitDeleteFileWorker(
	pipelineGit PipelineGitExecutor,
	logger *slog.Logger,
) river.Worker[jobs.PipelineGitDeleteFileArgs] {
	return &pipelineGitDeleteFileWorker{
		pipelineGit: pipelineGit,
		logger:      logger,
	}
}

func NewPipelineGitPublishProposalWorker(
	pipelineGit PipelineGitExecutor,
	authorizer PipelineGitProposalAuthorizer,
	recorder PipelineGitProposalRecorder,
	logger *slog.Logger,
) river.Worker[jobs.PipelineGitPublishProposalArgs] {
	return &pipelineGitPublishProposalWorker{
		pipelineGit: pipelineGit,
		authorizer:  authorizer,
		recorder:    recorder,
		logger:      logger,
	}
}

func NewPipelineGitRefreshProposalWorker(
	pipelineGit PipelineGitExecutor,
	recorder PipelineGitProposalRecorder,
	logger *slog.Logger,
) river.Worker[jobs.PipelineGitRefreshProposalArgs] {
	return &pipelineGitRefreshProposalWorker{pipelineGit: pipelineGit, recorder: recorder, logger: logger}
}

type pipelineGitWriteFileWorker struct {
	river.WorkerDefaults[jobs.PipelineGitWriteFileArgs]

	pipelineGit PipelineGitExecutor
	logger      *slog.Logger
}

func (w *pipelineGitWriteFileWorker) Work(
	ctx context.Context,
	job *river.Job[jobs.PipelineGitWriteFileArgs],
) error {
	args := job.Args
	w.logger.InfoContext(
		ctx,
		"pipeline git write job started",
		slog.String("organization_slug", args.OrganizationSlug.String()),
		slog.String("data_source_slug", args.DataSourceSlug.String()),
		slog.String("pipeline_slug", args.PipelineSlug.String()),
		slog.String("path", args.Path),
	)

	err := w.pipelineGit.WriteFile(ctx, args)
	if err != nil {
		w.logger.ErrorContext(ctx, "pipeline git write job failed", slog.Any("error", err))
		return pipelineGitJobError(err)
	}

	w.logger.InfoContext(ctx, "pipeline git write job succeeded")
	return nil
}

type pipelineGitDeleteFileWorker struct {
	river.WorkerDefaults[jobs.PipelineGitDeleteFileArgs]

	pipelineGit PipelineGitExecutor
	logger      *slog.Logger
}

type pipelineGitPublishProposalWorker struct {
	river.WorkerDefaults[jobs.PipelineGitPublishProposalArgs]

	pipelineGit PipelineGitExecutor
	authorizer  PipelineGitProposalAuthorizer
	recorder    PipelineGitProposalRecorder
	logger      *slog.Logger
}

type pipelineGitRefreshProposalWorker struct {
	river.WorkerDefaults[jobs.PipelineGitRefreshProposalArgs]
	pipelineGit PipelineGitExecutor
	recorder    PipelineGitProposalRecorder
	logger      *slog.Logger
}

func (w *pipelineGitDeleteFileWorker) Work(
	ctx context.Context,
	job *river.Job[jobs.PipelineGitDeleteFileArgs],
) error {
	args := job.Args
	w.logger.InfoContext(
		ctx,
		"pipeline git delete job started",
		slog.String("organization_slug", args.OrganizationSlug.String()),
		slog.String("data_source_slug", args.DataSourceSlug.String()),
		slog.String("pipeline_slug", args.PipelineSlug.String()),
		slog.String("path", args.Path),
	)

	err := w.pipelineGit.DeleteFile(ctx, args)
	if err != nil {
		w.logger.ErrorContext(ctx, "pipeline git delete job failed", slog.Any("error", err))
		return pipelineGitJobError(err)
	}

	w.logger.InfoContext(ctx, "pipeline git delete job succeeded")
	return nil
}

func (w *pipelineGitPublishProposalWorker) Work(
	ctx context.Context,
	job *river.Job[jobs.PipelineGitPublishProposalArgs],
) error {
	args := job.Args
	if w.authorizer == nil {
		return xerror.Forbidden(xerror.Pipeline).Detail("pipeline proposal authority recheck is unavailable")
	}
	if err := w.authorizer.AuthorizeProposalPublish(ctx, args); err != nil {
		if xerror.Is(err, jobs.ErrPipelineProposalAuthorityDenied) {
			w.recordProposalFailure(ctx, args, err)
			return xerror.Wrapf("cancel terminal pipeline proposal job: %w", river.JobCancel(err))
		}
		return err
	}
	w.logger.InfoContext(
		ctx,
		"pipeline git proposal job started",
		slog.String("organization_slug", args.OrganizationSlug.String()),
		slog.String("project_slug", args.ProjectSlug.String()),
		slog.String("data_source_slug", args.DataSourceSlug.String()),
		slog.String("pipeline_slug", args.PipelineSlug.String()),
		slog.String("branch_name", args.BranchName),
		slog.String("path", args.Path),
	)

	result, err := w.pipelineGit.PublishProposal(ctx, args)
	if err != nil {
		w.recordProposalFailure(ctx, args, err)
		w.logger.ErrorContext(ctx, "pipeline git proposal job failed", slog.Any("error", err))
		return pipelineGitJobError(err)
	}
	if err := result.ValidatePublication(args.Authority, args.BranchName); err != nil {
		w.recordProposalFailure(ctx, args, err)
		return xerror.Wrapf("cancel invalid pipeline proposal result: %w", river.JobCancel(err))
	}

	if args.MemberSlug != "" {
		if err := w.recorder.CompleteProposal(ctx, pipelinegit.ProposalCompletion{
			CreationSlug:     args.CreationSlug,
			OrganizationSlug: args.OrganizationSlug,
			ProjectSlug:      args.ProjectSlug,
			PipelineSlug:     args.PipelineSlug,
			MemberSlug:       args.MemberSlug,
			Provider:         result.Provider,
			ProposalID:       result.ProposalID,
			ProposalURL:      result.ProposalURL,
			ProposalState:    result.ProposalState,
			BranchName:       result.BranchName,
			BranchHeadSHA:    result.BranchHeadSHA,
			DraftSpecHash:    args.DraftSpecHash,
			PublishAttemptID: args.PublishAttemptID,
			Authority:        args.Authority,
		}); err != nil {
			w.logger.ErrorContext(ctx, "pipeline git proposal metadata persistence failed", slog.Any("error", err))
			return err
		}
	}

	w.logger.InfoContext(ctx, "pipeline git proposal job succeeded")
	return nil
}

func (w *pipelineGitPublishProposalWorker) recordProposalFailure(
	ctx context.Context,
	args jobs.PipelineGitPublishProposalArgs,
	failure error,
) {
	if args.MemberSlug == "" {
		return
	}
	failed := proposalFailure(
		args.OrganizationSlug, args.ProjectSlug, args.PipelineSlug, args.MemberSlug,
		args.PublishAttemptID, failure,
	)
	failed.CreationSlug = args.CreationSlug
	if err := w.recorder.FailProposal(ctx, failed); err != nil {
		w.logger.ErrorContext(ctx, "pipeline git proposal failure state persistence failed", slog.Any("error", err))
	}
}

func (w *pipelineGitRefreshProposalWorker) Work(
	ctx context.Context,
	job *river.Job[jobs.PipelineGitRefreshProposalArgs],
) error {
	args := job.Args
	result, err := w.pipelineGit.RefreshProposal(ctx, args)
	if err != nil {
		w.recordRefreshFailure(ctx, args, err)
		w.logger.ErrorContext(ctx, "pipeline git proposal refresh failed", slog.Any("error", err))
		return pipelineGitJobError(err)
	}
	if result == nil {
		err := xerror.Unexpected().Detail("pipeline git proposal refresh returned nil result")
		w.recordRefreshFailure(ctx, args, err)
		return err
	}
	if result.ProposalID != args.ProposalID {
		err := xerror.Unexpected().Detail("provider proposal refresh returned a different proposal ID")
		w.recordRefreshFailure(ctx, args, err)
		return err
	}
	if err := w.recorder.CompleteProposalRefresh(ctx, pipelinegit.ProposalRefreshCompletion{
		OrganizationSlug: args.OrganizationSlug,
		ProjectSlug:      args.ProjectSlug,
		PipelineSlug:     args.PipelineSlug,
		MemberSlug:       args.MemberSlug,
		Provider:         result.Provider,
		ProposalID:       result.ProposalID,
		ProposalURL:      result.ProposalURL,
		ProposalState:    result.ProposalState,
		BranchName:       result.BranchName,
		BranchHeadSHA:    result.BranchHeadSHA,
		RefreshAttemptID: args.RefreshAttemptID,
	}); err != nil {
		return err
	}
	w.logger.InfoContext(ctx, "pipeline git proposal refresh succeeded",
		slog.String("organization_slug", args.OrganizationSlug.String()),
		slog.String("pipeline_slug", args.PipelineSlug.String()),
		slog.String("proposal_id", args.ProposalID))
	return nil
}

func (w *pipelineGitRefreshProposalWorker) recordRefreshFailure(
	ctx context.Context,
	args jobs.PipelineGitRefreshProposalArgs,
	failure error,
) {
	failed := proposalFailure(args.OrganizationSlug, args.ProjectSlug, args.PipelineSlug, args.MemberSlug, "", failure)
	if err := w.recorder.FailProposalRefresh(ctx, pipelinegit.ProposalRefreshFailure{
		OrganizationSlug: args.OrganizationSlug, ProjectSlug: args.ProjectSlug,
		PipelineSlug: args.PipelineSlug, MemberSlug: args.MemberSlug,
		ProposalID: args.ProposalID, RefreshAttemptID: args.RefreshAttemptID,
		FailureCode: failed.FailureCode, FailureMessage: failed.FailureMessage,
	}); err != nil {
		w.logger.ErrorContext(ctx, "pipeline git proposal refresh failure state persistence failed", slog.Any("error", err))
	}
}

func pipelineGitJobError(err error) error {
	if xerror.Is(err, pipelinegit.ErrCreationTargetExists) {
		return xerror.Wrapf("cancel conflicting pipeline creation: %w", river.JobCancel(err))
	}
	if xerror.Is(err, executiondelivery.ErrAccountUnavailable) {
		return xerror.Wrapf("cancel account-bound pipeline Git job: %w", river.JobCancel(err))
	}
	return err
}

func proposalFailure(
	organizationSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	publishAttemptID string,
	failure error,
) pipelinegit.ProposalFailure {
	failureCode := "provider_unavailable"
	failureMessage := "Git proposal provider operation failed; retry is available."
	if xerror.Is(failure, pipelinegit.ErrCreationTargetExists) {
		failureCode = "target_file_exists"
		failureMessage = "The pipeline file already exists; sync the repository or choose another pipeline name."
	}
	if xerror.Is(failure, executiondelivery.ErrAccountUnavailable) {
		failureCode = "account_unavailable"
		failureMessage = "The initiating account is unavailable; this job has been cancelled."
	}
	var conflict *pipelinegit.ProposalBranchHeadConflictError
	if xerror.As(failure, &conflict) {
		failureCode = "branch_conflict"
		failureMessage = "The proposal branch changed after it was last observed; refresh before retrying."
	}
	return pipelinegit.ProposalFailure{
		OrganizationSlug: organizationSlug,
		ProjectSlug:      projectSlug,
		PipelineSlug:     pipelineSlug,
		MemberSlug:       memberSlug,
		PublishAttemptID: publishAttemptID,
		FailureCode:      failureCode,
		FailureMessage:   failureMessage,
	}
}
