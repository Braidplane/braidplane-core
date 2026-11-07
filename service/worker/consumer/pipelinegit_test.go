package consumer

import (
	"context"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestPipelineGitWriteFileWorkerDelegatesToExecutor(t *testing.T) {
	t.Parallel()

	executor := &recordingPipelineGitExecutor{}
	worker := pipelineGitWriteFileWorker{
		pipelineGit: executor,
		logger:      slog.New(slog.DiscardHandler),
	}
	branch := "main"

	err := worker.Work(context.Background(), &river.Job[jobs.PipelineGitWriteFileArgs]{
		Args: jobs.PipelineGitWriteFileArgs{
			OrganizationSlug: slug.Slug("acme"),
			DataSourceSlug:   slug.Slug("pipelines-git"),
			PipelineSlug:     slug.Slug("daily-sync"),
			Branch:           &branch,
			Path:             "pipelines/daily-sync.pipeline.yaml",
			Content:          []byte("metadata:\n  slug: daily-sync\n"),
			CommitMessage:    "export pipeline daily-sync",
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, executor.writeCalls)
	require.Equal(t, jobs.PipelineGitWriteFileArgs{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Branch:           &branch,
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("metadata:\n  slug: daily-sync\n"),
		CommitMessage:    "export pipeline daily-sync",
	}, executor.write)
}

func TestPipelineGitDeleteFileWorkerDelegatesToExecutor(t *testing.T) {
	t.Parallel()

	executor := &recordingPipelineGitExecutor{}
	worker := pipelineGitDeleteFileWorker{
		pipelineGit: executor,
		logger:      slog.New(slog.DiscardHandler),
	}

	err := worker.Work(context.Background(), &river.Job[jobs.PipelineGitDeleteFileArgs]{
		Args: jobs.PipelineGitDeleteFileArgs{
			OrganizationSlug: slug.Slug("acme"),
			DataSourceSlug:   slug.Slug("pipelines-git"),
			PipelineSlug:     slug.Slug("daily-sync"),
			Path:             "pipelines/daily-sync.pipeline.yaml",
			CommitMessage:    "remove pipeline daily-sync",
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, executor.deleteCalls)
	require.Equal(t, jobs.PipelineGitDeleteFileArgs{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Path:             "pipelines/daily-sync.pipeline.yaml",
		CommitMessage:    "remove pipeline daily-sync",
	}, executor.delete)
}

func TestPipelineGitPublishProposalWorkerPersistsSafeProviderMetadata(t *testing.T) {
	t.Parallel()

	executor := &recordingPipelineGitExecutor{
		publishResult: &pipelinegit.ProposalResult{
			Provider:      "github",
			ProposalID:    "42",
			ProposalURL:   "https://github.com/acme/pipelines/pull/42",
			ProposalState: "open",
			BranchName:    "braidplane/default/daily-sync/member-a",
			BranchHeadSHA: "def456",
		},
	}
	recorder := &recordingPipelineGitProposalRecorder{}
	worker := pipelineGitPublishProposalWorker{
		pipelineGit: executor,
		authorizer:  recorder,
		recorder:    recorder,
		logger:      slog.New(slog.DiscardHandler),
	}
	baseRef := "refs/heads/main"
	baseCommitSHA := "abc123"

	err := worker.Work(context.Background(), &river.Job[jobs.PipelineGitPublishProposalArgs]{
		Args: jobs.PipelineGitPublishProposalArgs{
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
			PublishAttemptID: "attempt-42",
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, executor.publishCalls)
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
		DraftSpecHash:    "",
		PublishAttemptID: "attempt-42",
	}, executor.publish)
	require.Equal(t, 1, recorder.calls)
	require.Equal(t, pipelinegit.ProposalCompletion{
		OrganizationSlug: slug.Slug("acme"),
		ProjectSlug:      slug.Slug("default"),
		PipelineSlug:     slug.Slug("daily-sync"),
		MemberSlug:       slug.Slug("member-a"),
		Provider:         "github",
		ProposalID:       "42",
		ProposalURL:      "https://github.com/acme/pipelines/pull/42",
		ProposalState:    "open",
		BranchName:       "braidplane/default/daily-sync/member-a",
		BranchHeadSHA:    "def456",
		PublishAttemptID: "attempt-42",
	}, recorder.completed)
}

func TestPipelineGitWorkerReturnsExecutorErrorForRetry(t *testing.T) {
	t.Parallel()

	executorErr := xerror.Unexpected().Detail("git push failed")
	worker := pipelineGitWriteFileWorker{
		pipelineGit: &recordingPipelineGitExecutor{writeErr: executorErr},
		logger:      slog.New(slog.DiscardHandler),
	}

	err := worker.Work(context.Background(), &river.Job[jobs.PipelineGitWriteFileArgs]{
		Args: jobs.PipelineGitWriteFileArgs{
			OrganizationSlug: slug.Slug("acme"),
			DataSourceSlug:   slug.Slug("pipelines-git"),
			PipelineSlug:     slug.Slug("daily-sync"),
			Path:             "pipelines/daily-sync.pipeline.yaml",
			Content:          []byte("metadata:\n  slug: daily-sync\n"),
			CommitMessage:    "export pipeline daily-sync",
		},
	})

	require.ErrorIs(t, err, executorErr)
}

func TestPipelineGitWorkersCancelUnavailableAccountWithoutCancellingTransientFailures(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"Write", "Delete", "Publish", "Refresh"} {
		for _, failure := range []struct {
			name string
			err  error
		}{
			{"AccountUnavailable", xerror.Wrapf("admission: %w", executiondelivery.ErrAccountUnavailable)},
			{"ExecutorUnavailable", xerror.ExecutionUnavailable()},
		} {
			t.Run(operation+failure.name, func(t *testing.T) {
				t.Parallel()
				executor := &recordingPipelineGitExecutor{writeErr: failure.err, deleteErr: failure.err, publishErr: failure.err}
				recorder := &recordingPipelineGitProposalRecorder{}
				logger := slog.New(slog.DiscardHandler)
				var err error
				switch operation {
				case "Write":
					err = NewPipelineGitWriteFileWorker(executor, logger).Work(t.Context(), &river.Job[jobs.PipelineGitWriteFileArgs]{})
				case "Delete":
					err = NewPipelineGitDeleteFileWorker(executor, logger).Work(t.Context(), &river.Job[jobs.PipelineGitDeleteFileArgs]{})
				case "Publish":
					err = NewPipelineGitPublishProposalWorker(executor, recorder, recorder, logger).Work(t.Context(), &river.Job[jobs.PipelineGitPublishProposalArgs]{
						Args: jobs.PipelineGitPublishProposalArgs{MemberSlug: "removed"},
					})
				case "Refresh":
					err = NewPipelineGitRefreshProposalWorker(executor, recorder, logger).Work(t.Context(), &river.Job[jobs.PipelineGitRefreshProposalArgs]{})
				}
				require.ErrorIs(t, err, failure.err)
				var cancelled *rivertype.JobCancelError
				if failure.name == "AccountUnavailable" {
					require.ErrorAs(t, err, &cancelled)
					if operation == "Publish" {
						require.Equal(t, "account_unavailable", recorder.failed.FailureCode)
					}
					if operation == "Refresh" {
						require.Equal(t, "account_unavailable", recorder.refreshFailed.FailureCode)
					}
				} else {
					require.False(t, xerror.As(err, &cancelled))
				}
			})
		}
	}
}

func TestPipelineGitRefreshProposalWorkerPersistsExactProviderState(t *testing.T) {
	t.Parallel()
	executor := &recordingPipelineGitExecutor{publishResult: &pipelinegit.ProposalResult{
		Provider: "github", ProposalID: "42", ProposalState: "merged", BranchName: "bp/member", BranchHeadSHA: "head",
	}}
	recorder := &recordingPipelineGitProposalRecorder{}
	worker := pipelineGitRefreshProposalWorker{pipelineGit: executor, recorder: recorder, logger: slog.New(slog.DiscardHandler)}
	args := jobs.PipelineGitRefreshProposalArgs{
		OrganizationSlug: slug.Slug("acme"), ProjectSlug: slug.Slug("default"),
		DataSourceSlug: slug.Slug("pipelines-git"), PipelineSlug: slug.Slug("daily-sync"),
		MemberSlug: slug.Slug("member-a"), ProposalID: "42",
		RefreshAttemptID: "refresh-42",
	}

	err := worker.Work(context.Background(), &river.Job[jobs.PipelineGitRefreshProposalArgs]{Args: args})

	require.NoError(t, err)
	require.Equal(t, pipelinegit.ProposalRefreshCompletion{
		OrganizationSlug: args.OrganizationSlug, ProjectSlug: args.ProjectSlug,
		PipelineSlug: args.PipelineSlug, MemberSlug: args.MemberSlug,
		Provider: "github", ProposalID: "42", ProposalState: "merged", BranchName: "bp/member", BranchHeadSHA: "head",
		RefreshAttemptID: "refresh-42",
	}, recorder.refreshed)
}

func TestPipelineGitRefreshProposalWorkerRecordsFailureBeforeReturning(t *testing.T) {
	t.Parallel()

	executorErr := xerror.Unexpected().Detail("private provider diagnostic")
	tests := []struct {
		name   string
		result *pipelinegit.ProposalResult
		err    error
	}{
		{name: "executor failure", err: executorErr},
		{name: "nil result"},
		{name: "proposal ID mismatch", result: &pipelinegit.ProposalResult{ProposalID: "43"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder := &recordingPipelineGitProposalRecorder{}
			worker := pipelineGitRefreshProposalWorker{
				pipelineGit: &recordingPipelineGitExecutor{publishResult: tc.result, publishErr: tc.err},
				recorder:    recorder,
				logger:      slog.New(slog.DiscardHandler),
			}
			args := jobs.PipelineGitRefreshProposalArgs{
				OrganizationSlug: slug.Slug("acme"), ProjectSlug: slug.Slug("default"),
				DataSourceSlug: slug.Slug("pipelines-git"), PipelineSlug: slug.Slug("daily-sync"),
				MemberSlug: slug.Slug("member-a"), ProposalID: "42", RefreshAttemptID: "refresh-42",
			}

			err := worker.Work(context.Background(), &river.Job[jobs.PipelineGitRefreshProposalArgs]{Args: args})

			require.Error(t, err)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			}
			require.Equal(t, 1, recorder.failureCalls)
			require.Equal(t, pipelinegit.ProposalRefreshFailure{
				OrganizationSlug: args.OrganizationSlug,
				ProjectSlug:      args.ProjectSlug,
				PipelineSlug:     args.PipelineSlug,
				MemberSlug:       args.MemberSlug,
				ProposalID:       args.ProposalID,
				RefreshAttemptID: args.RefreshAttemptID,
				FailureCode:      "provider_unavailable",
				FailureMessage:   "Git proposal provider operation failed; retry is available.",
			}, recorder.refreshFailed)
		})
	}
}

func TestPipelineGitPublishProposalWorkerRecordsSafeFailureStateBeforeRetry(t *testing.T) {
	t.Parallel()

	executorErr := xerror.Unexpected().Detail("private provider diagnostic")
	recorder := &recordingPipelineGitProposalRecorder{}
	worker := pipelineGitPublishProposalWorker{
		pipelineGit: &recordingPipelineGitExecutor{publishErr: executorErr},
		authorizer:  recorder,
		recorder:    recorder,
		logger:      slog.New(slog.DiscardHandler),
	}

	err := worker.Work(context.Background(), &river.Job[jobs.PipelineGitPublishProposalArgs]{
		Args: jobs.PipelineGitPublishProposalArgs{
			OrganizationSlug: slug.Slug("acme"),
			ProjectSlug:      slug.Slug("default"),
			PipelineSlug:     slug.Slug("daily-sync"),
			MemberSlug:       slug.Slug("member-a"),
			PublishAttemptID: "attempt-42",
		},
	})

	require.ErrorIs(t, err, executorErr)
	require.Equal(t, 1, recorder.failureCalls)
	require.Equal(t, pipelinegit.ProposalFailure{
		OrganizationSlug: slug.Slug("acme"),
		ProjectSlug:      slug.Slug("default"),
		PipelineSlug:     slug.Slug("daily-sync"),
		MemberSlug:       slug.Slug("member-a"),
		PublishAttemptID: "attempt-42",
		FailureCode:      "provider_unavailable",
		FailureMessage:   "Git proposal provider operation failed; retry is available.",
	}, recorder.failed)
}

func TestPipelineProposalWorkerCancelsInvalidPublicationResult(t *testing.T) {
	t.Parallel()
	recorder := &recordingPipelineGitProposalRecorder{}
	worker := pipelineGitPublishProposalWorker{
		pipelineGit: &recordingPipelineGitExecutor{publishResult: &pipelinegit.ProposalResult{Provider: "git"}},
		authorizer:  recorder, recorder: recorder, logger: slog.New(slog.DiscardHandler),
	}
	err := worker.Work(t.Context(), &river.Job[jobs.PipelineGitPublishProposalArgs]{Args: jobs.PipelineGitPublishProposalArgs{MemberSlug: "alice", PublishAttemptID: "attempt-1"}})
	var cancelled *rivertype.JobCancelError
	require.ErrorAs(t, err, &cancelled)
	require.Equal(t, 1, recorder.failureCalls)
	require.Zero(t, recorder.calls)
}

func TestPipelineCreationWorkerCancelsExistingTarget(t *testing.T) {
	t.Parallel()
	recorder := &recordingPipelineGitProposalRecorder{}
	worker := pipelineGitPublishProposalWorker{
		pipelineGit: &recordingPipelineGitExecutor{publishErr: pipelinegit.ErrCreationTargetExists},
		authorizer:  recorder, recorder: recorder, logger: slog.New(slog.DiscardHandler),
	}
	err := worker.Work(t.Context(), &river.Job[jobs.PipelineGitPublishProposalArgs]{
		Args: jobs.PipelineGitPublishProposalArgs{CreationSlug: "creation-first", MemberSlug: "alice", PublishAttemptID: "creation-first"},
	})
	var cancelled *rivertype.JobCancelError
	require.ErrorAs(t, err, &cancelled)
	require.Equal(t, "target_file_exists", recorder.failed.FailureCode)
	require.Equal(t, slug.Slug("creation-first"), recorder.failed.CreationSlug)
}

func TestPipelineCreationWorkerRetainsCreationIdentityOnSuccessAndFailure(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			executor := &recordingPipelineGitExecutor{publishResult: &pipelinegit.ProposalResult{ProposalID: "42", ProposalState: "open"}}
			if failed {
				executor.publishErr = xerror.Unexpected().Detail("synthetic provider failure")
			}
			recorder := &recordingPipelineGitProposalRecorder{}
			worker := pipelineGitPublishProposalWorker{pipelineGit: executor, authorizer: recorder, recorder: recorder, logger: slog.New(slog.DiscardHandler)}
			err := worker.Work(t.Context(), &river.Job[jobs.PipelineGitPublishProposalArgs]{Args: jobs.PipelineGitPublishProposalArgs{CreationSlug: "creation-first", MemberSlug: "alice", PublishAttemptID: "creation-first"}})
			if failed {
				require.ErrorIs(t, err, executor.publishErr)
				require.Equal(t, slug.Slug("creation-first"), recorder.failed.CreationSlug)
			} else {
				require.NoError(t, err)
				require.Equal(t, slug.Slug("creation-first"), recorder.completed.CreationSlug)
			}
		})
	}
}

func TestPipelineGitPublishProposalWorkerRechecksAuthorityBeforeProviderMutation(t *testing.T) {
	t.Parallel()

	authorityErr := jobs.ErrPipelineProposalAuthorityDenied.Detail("provider authority was revoked")
	executor := &recordingPipelineGitExecutor{}
	recorder := &recordingPipelineGitProposalRecorder{authorizeErr: authorityErr}
	worker := pipelineGitPublishProposalWorker{
		pipelineGit: executor,
		authorizer:  recorder,
		recorder:    recorder,
		logger:      slog.New(slog.DiscardHandler),
	}

	err := worker.Work(context.Background(), &river.Job[jobs.PipelineGitPublishProposalArgs]{
		Args: jobs.PipelineGitPublishProposalArgs{
			OrganizationSlug: slug.Slug("acme"),
			ProjectSlug:      slug.Slug("default"),
			PipelineSlug:     slug.Slug("daily-sync"),
			MemberSlug:       slug.Slug("member-a"),
			PublishAttemptID: "attempt-42",
		},
	})

	require.ErrorIs(t, err, authorityErr)
	var cancelErr *rivertype.JobCancelError
	require.ErrorAs(t, err, &cancelErr)
	require.Zero(t, executor.publishCalls)
	require.Equal(t, 1, recorder.failureCalls)
}

func TestPipelineGitPublishProposalWorkerRetriesTransientAuthorizationFailure(t *testing.T) {
	t.Parallel()

	authorityErr := xerror.Unexpected().Detail("temporary authority backend failure")
	executor := &recordingPipelineGitExecutor{}
	recorder := &recordingPipelineGitProposalRecorder{authorizeErr: authorityErr}
	worker := pipelineGitPublishProposalWorker{
		pipelineGit: executor,
		authorizer:  recorder,
		recorder:    recorder,
		logger:      slog.New(slog.DiscardHandler),
	}

	err := worker.Work(context.Background(), &river.Job[jobs.PipelineGitPublishProposalArgs]{
		Args: jobs.PipelineGitPublishProposalArgs{PublishAttemptID: "attempt-42"},
	})

	require.ErrorIs(t, err, authorityErr)
	var cancelErr *rivertype.JobCancelError
	require.NotErrorAs(t, err, &cancelErr)
	require.Zero(t, executor.publishCalls)
	require.Zero(t, recorder.failureCalls)
}

func TestNewPipelineGitExecutorAdaptsPipelineGitFileExecutor(t *testing.T) {
	t.Parallel()

	fileExecutor := &recordingPipelineGitFileExecutor{}
	proposalExecutor := &recordingPipelineGitProposalExecutor{}
	executor, err := NewPipelineGitExecutor(fileExecutor, proposalExecutor)
	require.NoError(t, err)
	branch := "main"

	require.NoError(t, executor.WriteFile(context.Background(), jobs.PipelineGitWriteFileArgs{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Branch:           &branch,
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("metadata:\n  slug: daily-sync\n"),
		CommitMessage:    "export pipeline daily-sync",
		FailIfExists:     true,
	}))
	require.Equal(t, pipelinegit.WriteFileOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Branch:           &branch,
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("metadata:\n  slug: daily-sync\n"),
		CommitMessage:    "export pipeline daily-sync",
		FailIfExists:     true,
	}, fileExecutor.write)

	require.NoError(t, executor.DeleteFile(context.Background(), jobs.PipelineGitDeleteFileArgs{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Branch:           &branch,
		Path:             "pipelines/daily-sync.pipeline.yaml",
		CommitMessage:    "remove pipeline daily-sync",
	}))
	require.Equal(t, pipelinegit.DeleteFileOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Branch:           &branch,
		Path:             "pipelines/daily-sync.pipeline.yaml",
		CommitMessage:    "remove pipeline daily-sync",
	}, fileExecutor.delete)
}

func TestNewPipelineGitExecutorRequiresFileExecutor(t *testing.T) {
	t.Parallel()

	executor, err := NewPipelineGitExecutor(nil, &recordingPipelineGitProposalExecutor{})

	require.Nil(t, executor)
	require.ErrorContains(t, err, "pipeline git file executor is required")
}

func TestNewPipelineGitExecutorRequiresProposalExecutor(t *testing.T) {
	t.Parallel()

	executor, err := NewPipelineGitExecutor(&recordingPipelineGitFileExecutor{}, nil)

	require.Nil(t, executor)
	require.ErrorContains(t, err, "pipeline git proposal executor is required")
}

type recordingPipelineGitExecutor struct {
	writeCalls    int
	write         jobs.PipelineGitWriteFileArgs
	writeErr      error
	deleteCalls   int
	delete        jobs.PipelineGitDeleteFileArgs
	deleteErr     error
	publishCalls  int
	publish       jobs.PipelineGitPublishProposalArgs
	publishResult *pipelinegit.ProposalResult
	publishErr    error
}

func (e *recordingPipelineGitExecutor) WriteFile(
	_ context.Context,
	op jobs.PipelineGitWriteFileArgs,
) error {
	e.writeCalls++
	e.write = op
	return e.writeErr
}

func (e *recordingPipelineGitExecutor) DeleteFile(
	_ context.Context,
	op jobs.PipelineGitDeleteFileArgs,
) error {
	e.deleteCalls++
	e.delete = op
	return e.deleteErr
}

func (e *recordingPipelineGitExecutor) PublishProposal(
	_ context.Context,
	op jobs.PipelineGitPublishProposalArgs,
) (*pipelinegit.ProposalResult, error) {
	e.publishCalls++
	e.publish = op
	return e.publishResult, e.publishErr
}

func (e *recordingPipelineGitExecutor) RefreshProposal(
	_ context.Context,
	op jobs.PipelineGitRefreshProposalArgs,
) (*pipelinegit.ProposalResult, error) {
	e.publishCalls++
	return e.publishResult, e.publishErr
}

type recordingPipelineGitFileExecutor struct {
	write  pipelinegit.WriteFileOperation
	delete pipelinegit.DeleteFileOperation
}

func (e *recordingPipelineGitFileExecutor) WriteFile(
	_ context.Context,
	op pipelinegit.WriteFileOperation,
) error {
	e.write = op
	return nil
}

func (e *recordingPipelineGitFileExecutor) DeleteFile(
	_ context.Context,
	op pipelinegit.DeleteFileOperation,
) error {
	e.delete = op
	return nil
}

type recordingPipelineGitProposalExecutor struct{}

func (e *recordingPipelineGitProposalExecutor) PublishProposal(
	context.Context,
	pipelinegit.ProposalOperation,
) (*pipelinegit.ProposalResult, error) {
	return &pipelinegit.ProposalResult{}, nil
}

func (e *recordingPipelineGitProposalExecutor) RefreshProposal(
	context.Context, pipelinegit.ProposalRefreshOperation,
) (*pipelinegit.ProposalResult, error) {
	return &pipelinegit.ProposalResult{}, nil
}

type recordingPipelineGitProposalRecorder struct {
	calls         int
	failureCalls  int
	completed     pipelinegit.ProposalCompletion
	failed        pipelinegit.ProposalFailure
	err           error
	refreshed     pipelinegit.ProposalRefreshCompletion
	refreshFailed pipelinegit.ProposalRefreshFailure
	authorizeErr  error
}

func (r *recordingPipelineGitProposalRecorder) AuthorizeProposalPublish(
	context.Context, jobs.PipelineGitPublishProposalArgs,
) error {
	return r.authorizeErr
}

func (r *recordingPipelineGitProposalRecorder) FailProposal(
	_ context.Context,
	failed pipelinegit.ProposalFailure,
) error {
	r.failureCalls++
	r.failed = failed
	return r.err
}

func (r *recordingPipelineGitProposalRecorder) CompleteProposal(
	_ context.Context,
	completed pipelinegit.ProposalCompletion,
) error {
	r.calls++
	r.completed = completed
	return r.err
}

func (r *recordingPipelineGitProposalRecorder) CompleteProposalRefresh(
	_ context.Context, completed pipelinegit.ProposalRefreshCompletion,
) error {
	r.refreshed = completed
	return r.err
}

func (r *recordingPipelineGitProposalRecorder) FailProposalRefresh(
	_ context.Context, failed pipelinegit.ProposalRefreshFailure,
) error {
	r.failureCalls++
	r.refreshFailed = failed
	return r.err
}
