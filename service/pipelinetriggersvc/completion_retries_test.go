package pipelinetriggersvc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestCompletionFollowUpRetryPersistsTiming(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		jitter    bool
		elapsed   int
		delay     time.Duration
		exhausted bool
	}{
		{name: "disabled", elapsed: 45, delay: 15 * time.Second},
		{name: "enabled at elapsed cap", jitter: true, elapsed: 38, delay: 8 * time.Second},
		{name: "over elapsed cap", jitter: true, elapsed: 37, delay: 8 * time.Second, exhausted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rootAt := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
			evaluatedAt := rootAt.Add(30 * time.Second)
			ref := pipelinetriggers.CompletionDispatchRef{
				OrgSlug: "org", ProjectSlug: "project", EnvironmentSlug: "dev", ID: 1, FailedRunSlug: "completion-1-retry-2",
			}
			snapshot := &model.PipelineCompletionSnapshot{
				State: "accepted", ChildRunSlug: "completion-1", PipelineSlug: "child",
				TriggerSlug: "on-completion", TriggerRevision: 1, VersionHash: "version",
				RetryPolicy: model.PipelineTriggerRetryPolicy{
					Enabled: true, MaxAttempts: 3, BackoffStrategy: model.PipelineTriggerBackoffStrategyExponential,
					InitialDelaySeconds: 10, MaxDelaySeconds: 15, Jitter: tc.jitter, MaxElapsedSeconds: &tc.elapsed,
					RetryableFailureKinds: []model.PipelineTriggerRetryFailureKind{model.PipelineTriggerRetryFailureKindDataSource},
				},
			}
			failureKind := "data_source"
			repo := &completionRetryRepository{evaluatedAt: evaluatedAt}
			executor := &completionRetryExecutor{runs: map[string]*pipelinesvc.PipelineRunDetail{
				snapshot.ChildRunSlug.String(): {Metadata: pipelinesvc.PipelineRunMetadata{StartedAt: rootAt}},
				ref.FailedRunSlug.String(): {Metadata: pipelinesvc.PipelineRunMetadata{
					RunSlug: ref.FailedRunSlug.String(), PipelineSlug: "child", PipelineVersionSpecHash: "version",
					Status: "failed", StartedAt: rootAt.Add(20 * time.Second), FailureKind: &failureKind,
					Source: pipelinerun.RunSourceSummary{
						Type: model.PipelineRunSourceTypeCompletion, AttemptNumber: 2, RootRunSlug: snapshot.ChildRunSlug,
					},
				}},
			}}
			draws := 0
			dispatcher := &CompletionDispatcher{
				txm: testutil.FakeTransactionManager{}, repo: repo, executor: executor, checker: completionRetryChecker{snapshot},
				retryRandom: func(int64) int64 {
					draws++
					return int64(500 * time.Millisecond / time.Microsecond)
				},
			}

			require.NoError(t, dispatcher.finalizeAttempt(t.Context(), ref))
			require.NoError(t, dispatcher.finalizeAttempt(t.Context(), ref))
			if tc.jitter {
				require.Equal(t, 1, draws)
			} else {
				require.Zero(t, draws)
			}
			require.Len(t, repo.records, 1)
			record := repo.records[0]
			require.Equal(t, 3, record.AttemptNumber)
			require.Equal(t, evaluatedAt.Add(tc.delay), record.ScheduledAt)
			if tc.exhausted {
				require.Equal(t, model.PipelineTriggerRetryAttemptStatusExhausted, record.Status)
				require.Equal(t, "max_elapsed_exhausted", record.DecisionReason)
				require.Empty(t, executor.accepted)
				require.Empty(t, repo.executionArgs)
				return
			}
			require.Equal(t, model.PipelineTriggerRetryAttemptStatusQueued, record.Status)
			require.Equal(t, []time.Time{record.ScheduledAt}, executor.accepted)
			var saved jobs.PipelineRunExecutionArgs
			require.NoError(t, json.Unmarshal(repo.executionArgs, &saved))
			require.Equal(t, record.ScheduledAt, saved.ScheduledAt)
		})
	}
}

type completionRetryChecker struct {
	snapshot *model.PipelineCompletionSnapshot
}

func (c completionRetryChecker) CheckCompletionSnapshot(context.Context, slug.Slug, slug.Slug, slug.Slug, int) (*model.PipelineCompletionSnapshot, error) {
	return c.snapshot, nil
}

type completionRetryRepository struct {
	pipelinetriggers.Repository
	evaluatedAt   time.Time
	records       []pipelinetriggers.TriggerRetryAttemptRecord
	executionArgs []byte
}

func (r *completionRetryRepository) FenceCompletionRetryDecision(context.Context, postgres.DBTX, slug.Slug, int) (time.Time, error) {
	return r.evaluatedAt, nil
}

func (r *completionRetryRepository) ListTriggerRetryAttemptsForRun(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) ([]pipelinetriggers.TriggerRetryAttemptDiagnostic, error) {
	if len(r.records) == 0 {
		return nil, nil
	}
	return []pipelinetriggers.TriggerRetryAttemptDiagnostic{{RetryOfRunSlug: r.records[0].RetryOfRunSlug}}, nil
}

func (r *completionRetryRepository) RecordTriggerRetryAttempt(_ context.Context, _ postgres.DBTX, record pipelinetriggers.TriggerRetryAttemptRecord) (pipelinetriggers.TriggerRetryAttempt, error) {
	r.records = append(r.records, record)
	return pipelinetriggers.TriggerRetryAttempt{}, nil
}

func (r *completionRetryRepository) SaveCompletionRetryExecution(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ int, _ slug.Slug, args []byte) error {
	r.executionArgs = args
	return nil
}

type completionRetryExecutor struct {
	CompletionRunExecutor
	runs     map[string]*pipelinesvc.PipelineRunDetail
	accepted []time.Time
}

func (e *completionRetryExecutor) GetRun(_ context.Context, _, _, _ slug.Slug, run string) (*pipelinesvc.PipelineRunDetail, error) {
	return e.runs[run], nil
}

func (e *completionRetryExecutor) AcceptCompletionRetryRun(_ context.Context, _, _, _ slug.Slug, _ *model.PipelineCompletionSnapshot, _ slug.Slug, _ pipelinerun.RunSourceSummary, at time.Time) (jobs.PipelineRunExecutionArgs, error) {
	e.accepted = append(e.accepted, at)
	return jobs.PipelineRunExecutionArgs{ScheduledAt: at}, nil
}
