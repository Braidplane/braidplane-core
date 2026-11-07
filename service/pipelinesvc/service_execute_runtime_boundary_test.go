package pipelinesvc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	pipelineruns "github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	pipelinevalidationsrepo "github.com/Braidplane/braidplane-core/postgres/pipelinevalidations"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func testRunSource(actor model.EventActor) pipelinerun.RunSourceSummary {
	return pipelinerun.RunSourceSummary{
		Type:          model.PipelineRunSourceTypeManual,
		Summary:       "Manual run",
		Entrypoint:    model.PipelineRunSourceEntrypointUI,
		ActorType:     actor.Type,
		ActorSlug:     actor.Slug,
		ParameterKeys: []string{},
	}
}

func TestVerifiedEventInputUsesDeclaredInputStep(t *testing.T) {
	t.Parallel()

	document := json.RawMessage(`{"event":{"id":"evt-1"}}`)
	input, err := verifiedEventInput(&pipelinespec.Pipeline{Steps: []pipelinespec.Step{
		{Slug: must.Slug("event"), Input: &pipelinespec.InputSpec{}},
	}}, document)

	require.NoError(t, err)
	require.Equal(t, must.Slug("event"), input.Key())
	require.JSONEq(t, string(document), string(input.Payload().Document))
}

func TestVerifiedEventInputRejectsMultipleInputSteps(t *testing.T) {
	t.Parallel()

	_, err := verifiedEventInput(&pipelinespec.Pipeline{Steps: []pipelinespec.Step{
		{Slug: must.Slug("first"), Input: &pipelinespec.InputSpec{}},
		{Slug: must.Slug("second"), Input: &pipelinespec.InputSpec{}},
	}}, json.RawMessage(`{"event":{}}`))

	require.ErrorContains(t, err, "more than one input step")
}

func TestService_ExecuteDelegatesToRuntimeBoundary(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	pipelineSlug := must.Slug("runtime-boundary")
	runSlug := must.Slug("manual-run-001")
	versionID := int64(42)
	specHash := "runtime-boundary-hash"
	startedAt := time.Date(2026, time.May, 13, 9, 30, 0, 0, time.UTC)
	endedAt := startedAt.Add(25 * time.Millisecond)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: "Runtime Boundary",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("users-db"),
					OperationType:  "query",
					OperationRef:   must.Slug("list-users"),
				},
			},
		},
	}
	pipelineStore := &runtimeBoundaryPipelineStore{
		spec: &pipelinesrepo.PipelineSpec{
			Slug:                   pipelineSlug,
			SyncStatus:             pipelinesrepo.SyncStatusSynced,
			CurrentVersionID:       &versionID,
			CurrentVersionSpecHash: &specHash,
		},
		version: &pipelinesrepo.PipelineVersion{
			ID:           versionID,
			PipelineSlug: pipelineSlug,
			SpecHash:     specHash,
			SpecJSONB:    mustJSON(t, pipeline),
			CreatedAt:    startedAt.Add(-time.Minute),
		},
		deployment: &pipelinesrepo.PipelineDeployment{
			ID: 99, Revision: 7,
			PipelineSlug:           pipelineSlug,
			EnvironmentSlug:        environmentSlug,
			CurrentVersionID:       versionID,
			CurrentVersionSpecHash: specHash,
		},
	}
	guardHeld := false
	runtime := &fakePipelineRuntime{
		executeFn: func(ctx context.Context, req pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error) {
			require.True(t, guardHeld, "runtime execution must remain inside the entitlement guard")
			require.Equal(t, slug.Slug("alice"), executiondelivery.InitiatingUser(ctx))
			require.Equal(t, orgSlug, req.OrgSlug)
			require.Equal(t, environmentSlug, req.EnvironmentSlug)
			require.Equal(t, runSlug.String(), req.RunSlug)
			require.Equal(t, pipeline, req.Pipeline)
			require.Equal(t, versionID, req.PipelineVersionID)
			require.NotNil(t, req.DeploymentID)
			require.EqualValues(t, 99, *req.DeploymentID)
			require.NotNil(t, req.DeploymentRevision)
			require.EqualValues(t, 7, *req.DeploymentRevision)
			require.Equal(t, map[string]any{
				"customer_id": "cus_123",
				"dry_run":     true,
			}, req.RuntimeInput)
			require.Equal(t, environmentSlug, req.Source.EnvironmentSlug)
			require.Equal(t, []string{"input.customer_id", "input.dry_run"}, req.Source.ParameterKeys)
			return &pipelineruntime.RunOutcome{
				Metadata: pipelineruntime.RunOutcomeMetadata{
					PipelineVersionID: req.PipelineVersionID,
					PipelineSlug:      req.Pipeline.Metadata.Slug,
					EnvironmentSlug:   req.EnvironmentSlug,
					RunSlug:           req.RunSlug,
					Source:            req.Source,
					Status:            "succeeded",
					DurationMs:        endedAt.Sub(startedAt).Milliseconds(),
					StepCount:         len(req.Pipeline.Steps),
					StepSlug:          "fetch-users",
					StartedAt:         startedAt,
					EndedAt:           &endedAt,
				},
				Timeline: pipelinerun.RunTimeline{
					RunSlug:         req.RunSlug,
					PipelineSlug:    req.Pipeline.Metadata.Slug,
					EnvironmentSlug: req.EnvironmentSlug,
					Status:          "succeeded",
					Events: []pipelinerun.TimelineEvent{
						{At: startedAt, Type: "run_started", Status: "running"},
						{At: endedAt, Type: "run_completed", Status: "succeeded"},
					},
				},
			}, nil
		},
	}

	admission := testAdmissionService()
	admission.WithPipelineExecutionFn = func(ctx context.Context, org slug.Slug, admitted bool, execute func(context.Context) error) error {
		require.Equal(t, orgSlug, org)
		require.False(t, admitted, "synchronous execution has no pre-admitted queued run")
		guardHeld = true
		defer func() { guardHeld = false }()
		return execute(ctx)
	}
	svc := &service{
		proofLinks:     newTestProofLinks(),
		admission:      admission,
		pipelineStore:  pipelineStore,
		txm:            &recordingPipelineGitTxManager{},
		runRepo:        &fakeRunRepository{},
		executionClock: fixedClock{at: startedAt},
		runtime:        runtime,
		eventProducer:  eventtest.NewProducer(&fakePipelineEventRecorder{}),
	}

	actor := model.UserEventActor(must.Slug("alice"))
	run, err := svc.Execute(
		ctx,
		orgSlug,
		defaultProjectSlug,
		environmentSlug,
		pipelineSlug,
		&runSlug,
		nil,
		actor,
		testRunSource(actor),
		map[string]any{
			"dry_run":     true,
			"customer_id": "cus_123",
		},
	)
	require.NoError(t, err)
	require.Equal(t, runSlug.String(), run.Metadata.RunSlug)
	require.Equal(t, environmentSlug.String(), run.Metadata.EnvironmentSlug)
	require.Equal(t, environmentSlug, run.Metadata.Source.EnvironmentSlug)
	require.Equal(t, "succeeded", run.Metadata.Status)
	require.Equal(t, int32(1), run.Metadata.StepCount)
	require.Equal(t, "fetch-users", run.Metadata.StepSlug)
	require.False(t, guardHeld, "the entitlement guard must release after runtime execution")
	require.Equal(t, 1, runtime.executeCalls)
	require.Zero(t, pipelineStore.findSpecCalls)
	require.Equal(t, 1, pipelineStore.findDeploymentCalls)
	require.Equal(t, 1, pipelineStore.findVersionBySpecHashCalls)
	require.Zero(t, pipelineStore.getCalls)
}

func TestService_StartRunPersistsQueuedRunAndEnqueuesExecution(t *testing.T) {
	t.Parallel()
	for _, backfill := range []bool{false, true} {
		t.Run(fmt.Sprintf("backfill=%t", backfill), func(t *testing.T) {
			orgSlug := must.Slug("acme")
			projectSlug := defaultProjectSlug
			environmentSlug := must.Slug("prod")
			pipelineSlug := must.Slug("runtime-boundary")
			runSlug := must.Slug("manual-run-queued-001")
			versionID := int64(42)
			specHash := "runtime-boundary-hash"
			acceptedAt := time.Date(2026, time.May, 13, 9, 30, 0, 0, time.UTC)

			pipelineStore := &runtimeBoundaryPipelineStore{
				deployment: &pipelinesrepo.PipelineDeployment{
					PipelineSlug:           pipelineSlug,
					EnvironmentSlug:        environmentSlug,
					CurrentVersionID:       versionID,
					CurrentVersionSpecHash: specHash,
				},
			}
			kind := model.IncrementalCheckpointKindWatermark
			specJSON, err := json.Marshal(&pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: pipelineSlug}, Steps: []pipelinespec.Step{{Slug: "source", Query: &pipelinespec.QueryRef{Incremental: &pipelinespec.IncrementalSourceContract{CheckpointKind: &kind}}}}})
			require.NoError(t, err)
			pipelineStore.version = &pipelinesrepo.PipelineVersion{ID: versionID, SpecHash: specHash, SpecJSONB: specJSON}
			runtime := &fakePipelineRuntime{
				executeFn: func(context.Context, pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error) {
					t.Fatal("StartRun must enqueue execution instead of running the runtime inline")
					return nil, errFakeRepositoryMethodNotUsed
				},
			}
			var createdParams *pipelineruns.CreateRunParams
			runRepo := &fakeRunRepository{
				createRunFn: func(
					_ context.Context,
					_ postgres.DBTX,
					gotOrgSlug, gotProjectSlug, gotEnvironmentSlug slug.Slug,
					params *pipelineruns.CreateRunParams,
				) (*pipelineruns.PipelineRun, error) {
					require.Equal(t, orgSlug, gotOrgSlug)
					require.Equal(t, projectSlug, gotProjectSlug)
					require.Equal(t, environmentSlug, gotEnvironmentSlug)
					createdParams = params
					return &pipelineruns.PipelineRun{
						PipelineVersionID:       params.PipelineVersionID,
						PipelineVersionSpecHash: specHash,
						EnvironmentSlug:         gotEnvironmentSlug.String(),
						PipelineSlug:            params.PipelineSlug,
						RunSlug:                 params.RunSlug,
						Source:                  params.Source,
						RequestID:               params.RequestID,
						Status:                  params.Status,
						DurationMs:              params.DurationMs,
						StepCount:               params.StepCount,
						StepSlug:                params.StepSlug,
						StartedAt:               params.StartedAt,
						EndedAt:                 params.EndedAt,
						Timeline:                params.Timeline,
						Incremental:             params.Incremental,
						IncrementalDiagnostics:  emptyPipelineRunDiagnostics(),
						CreatedAt:               acceptedAt,
					}, nil
				},
			}
			runProducer := &recordingPipelineRunProducer{}
			recordedEvents := &fakePipelineEventRecorder{}
			svc := &service{
				proofLinks:          newTestProofLinks(),
				admission:           testAdmissionService(),
				pipelineStore:       pipelineStore,
				txm:                 &recordingPipelineGitTxManager{},
				runRepo:             runRepo,
				executionClock:      fixedClock{at: acceptedAt},
				runtime:             runtime,
				pipelineRunProducer: runProducer,
				eventProducer:       eventtest.NewProducer(recordedEvents),
			}

			actor := model.UserEventActor(must.Slug("alice"))
			ctx := reqctx.WithRequestID(context.Background(), "req-start-001")
			start := svc.StartRun
			windows := map[slug.Slug]pipelinerun.IncrementalSourceWindow{"source": {LowerBound: json.RawMessage(`{"value":1}`), UpperBound: json.RawMessage(`{"value":5}`)}}
			if backfill {
				start = func(ctx context.Context, org, project, environment, pipeline slug.Slug, run, confirmation *slug.Slug, actor model.EventActor, source pipelinerun.RunSourceSummary, input map[string]any) (*PipelineRunDetail, error) {
					return svc.StartBackfill(ctx, org, project, environment, pipeline, run, confirmation, actor, source, input, windows)
				}
			}
			run, err := start(
				ctx,
				orgSlug,
				projectSlug,
				environmentSlug,
				pipelineSlug,
				&runSlug,
				nil,
				actor,
				testRunSource(actor),
				map[string]any{"customer_id": "cus_123", "dry_run": true},
			)

			require.NoError(t, err)
			require.NotNil(t, createdParams)
			require.Equal(t, versionID, createdParams.PipelineVersionID)
			require.Equal(t, pipelineSlug.String(), createdParams.PipelineSlug)
			require.Equal(t, runSlug.String(), createdParams.RunSlug)
			require.Equal(t, pipelineRunStatusQueued, createdParams.Status)
			require.Zero(t, createdParams.DurationMs)
			require.Zero(t, createdParams.StepCount)
			require.Empty(t, createdParams.StepSlug)
			require.Equal(t, acceptedAt, createdParams.StartedAt)
			require.Nil(t, createdParams.EndedAt)
			require.NotNil(t, createdParams.RequestID)
			require.Equal(t, "req-start-001", *createdParams.RequestID)
			require.Equal(t, environmentSlug, createdParams.Source.EnvironmentSlug)
			require.ElementsMatch(t, []string{"input.customer_id", "input.dry_run"}, createdParams.Source.ParameterKeys)

			var timeline pipelinerun.RunTimeline
			require.NoError(t, json.Unmarshal(createdParams.Timeline, &timeline))
			require.Equal(t, runSlug.String(), timeline.RunSlug)
			require.Equal(t, pipelineRunStatusQueued, timeline.Status)
			require.Len(t, timeline.Events, 1)
			require.Equal(t, pipelineRunEventQueued, timeline.Events[0].Type)

			require.Equal(t, runSlug.String(), run.Metadata.RunSlug)
			require.Equal(t, pipelineRunStatusQueued, run.Metadata.Status)
			require.Equal(t, specHash, run.Metadata.PipelineVersionSpecHash)
			require.Equal(t, 0, runtime.executeCalls)
			require.Equal(t, orgSlug, runProducer.args.OrganizationSlug)
			require.Equal(t, projectSlug, runProducer.args.ProjectSlug)
			require.Equal(t, environmentSlug, runProducer.args.EnvironmentSlug)
			require.Equal(t, pipelineSlug, runProducer.args.PipelineSlug)
			require.Equal(t, specHash, runProducer.args.PipelineVersionHash)
			require.Equal(t, runSlug, runProducer.args.RunSlug)
			require.Equal(t, acceptedAt, runProducer.args.AcceptedAt)
			require.Equal(t, map[string]any{"customer_id": "cus_123", "dry_run": true}, runProducer.args.RuntimeInput)
			require.False(t, runProducer.args.ProtectedEnvironment)
			if backfill {
				require.Len(t, recordedEvents.calls, 1)
				require.Equal(t, model.EventClassSecurityAudit, recordedEvents.calls[0].EventClass)
				require.Equal(t, "backfill_requested", recordedEvents.calls[0].Metadata["operation"])
				require.NotContains(t, fmt.Sprint(recordedEvents.calls[0].Metadata), "lower_bound")
				require.Equal(t, "backfill", createdParams.Incremental.Mode)
				require.Equal(t, runSlug.String(), createdParams.Incremental.BackfillSeriesSlug)
				var diagnostics pipelinerun.IncrementalRunDiagnostics
				require.NoError(t, json.Unmarshal(createdParams.IncrementalDiagnostics, &diagnostics))
				require.Len(t, diagnostics.Sources, 1)
				require.Equal(t, "source", diagnostics.Sources[0].SourceStepSlug)
				require.Equal(t, "1", diagnostics.Sources[0].Window.LowerBound)
				require.Equal(t, "5", diagnostics.Sources[0].Window.UpperBound)
				require.Equal(t, windows, runProducer.args.Incremental.SourceWindows)
				require.True(t, runProducer.args.Incremental.SkipCheckpointCommit)
				require.False(t, runProducer.args.Incremental.PromoteCheckpoint)
			} else {
				require.Empty(t, runProducer.args.Incremental.SourceWindows)
			}
		})
	}
}

func TestService_StartRunRejectsEntitlementBeforePersistence(t *testing.T) {
	t.Parallel()

	wantErr := xerror.Forbidden(xerror.Organization)
	admission := testAdmissionService()
	admission.CheckRunAccessFn = func(context.Context, slug.Slug) error { return wantErr }
	svc := &service{
		proofLinks: newTestProofLinks(),
		admission:  admission,
		runRepo: &fakeRunRepository{createRunFn: func(
			context.Context,
			postgres.DBTX,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			*pipelineruns.CreateRunParams,
		) (*pipelineruns.PipelineRun, error) {
			t.Fatal("run must not be persisted after entitlement denial")
			return nil, wantErr
		}},
	}

	_, err := svc.StartRun(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("runtime-boundary"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.ErrorIs(t, err, wantErr)
}

func TestService_CreateQueuedRunRejectsEntitlementBeforePersistence(t *testing.T) {
	t.Parallel()

	wantErr := xerror.Forbidden(xerror.Organization)
	admission := testAdmissionService()
	admission.CheckPipelineRunAdmissionFn = func(context.Context, slug.Slug) error { return wantErr }
	svc := &service{
		admission: admission,
		runRepo: &fakeRunRepository{createRunFn: func(
			context.Context,
			postgres.DBTX,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			*pipelineruns.CreateRunParams,
		) (*pipelineruns.PipelineRun, error) {
			t.Fatal("run must not be persisted after entitlement denial")
			return nil, wantErr
		}},
	}

	_, _, err := svc.createQueuedRun(
		context.Background(),
		&fakeDBTX{},
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("runtime-boundary"),
		must.Slug("run-001"),
		42,
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		time.Now().UTC(),
		&pipelinesrepo.PipelineDeployment{},
		pipelinerun.IncrementalRunOptions{},
	)
	require.ErrorIs(t, err, wantErr)
}

func TestService_ExecuteQueuedPipelineRunCompletesEntitlementDenial(t *testing.T) {
	t.Parallel()

	actor := model.UserEventActor(must.Slug("alice"))
	args := jobs.PipelineRunExecutionArgs{
		Incremental:      pipelinerun.IncrementalRunOptions{BackfillSeriesSlug: "backfill-001", BackfillChunkCount: 1, SkipCheckpointCommit: true, SourceWindows: map[slug.Slug]pipelinerun.IncrementalSourceWindow{"source": {LowerBound: json.RawMessage(`{"cursor":"opaque-a"}`), UpperBound: json.RawMessage(`{"cursor":"opaque-b"}`)}}},
		OrganizationSlug: must.Slug("acme"),
		ProjectSlug:      defaultProjectSlug,
		EnvironmentSlug:  must.Slug("prod"),
		PipelineSlug:     must.Slug("users-sync"),
		RunSlug:          must.Slug("queued-denied-001"),
		Actor:            actor,
		Source:           testRunSource(actor),
	}
	admission := testAdmissionService()
	admission.CheckWriteAccessFn = func(context.Context, slug.Slug) error {
		return admissionsvc.ErrAdmissionDenied
	}
	completed := false
	runRepo := &fakeRunRepository{
		findRunBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string) (*pipelineruns.PipelineRun, error) {
			raw, err := json.Marshal(pipelinerun.RunTimeline{
				Revision: 4, RunSlug: args.RunSlug.String(), PipelineSlug: args.PipelineSlug, ProjectSlug: args.ProjectSlug, EnvironmentSlug: args.EnvironmentSlug,
				Events:  []pipelinerun.TimelineEvent{{Type: "step_started", Step: "fetch-users", Status: "running"}},
				Failure: &pipelinerun.FailureContext{Error: "retained provider failure", Kind: "data_source", Side: "source", AutomaticRetryProhibited: true},
				StructuralChecks: []pipelinerun.StructuralCheck{
					{ID: "completed", Status: pipelinerun.CheckStatusPassed, ReasonCode: "conformant"},
					{ID: "interrupted", Status: pipelinerun.CheckStatusUnavailable, ReasonCode: "evaluating"},
				},
			})
			require.NoError(t, err)
			summary, diagnostics := requestedIncrementalState(args.Incremental)
			return &pipelineruns.PipelineRun{Timeline: raw, Incremental: summary, IncrementalDiagnostics: diagnostics, StepCount: 3, StepSlug: "fetch-users", StartedAt: time.Date(2026, time.August, 20, 9, 59, 0, 0, time.UTC)}, nil
		}, completeRunFn: func(
			_ context.Context,
			_ postgres.DBTX,
			_, _, environmentSlug slug.Slug,
			runSlug string,
			params *pipelineruns.CompleteRunParams,
		) (*pipelineruns.PipelineRun, error) {
			var timeline pipelinerun.RunTimeline
			require.NoError(t, json.Unmarshal(params.Timeline, &timeline))
			require.EqualValues(t, 5, timeline.Revision)
			require.Equal(t, args.RunSlug.String(), timeline.RunSlug)
			require.Len(t, timeline.Events, 2)
			require.Equal(t, "step_started", timeline.Events[0].Type)
			require.Equal(t, "fetch-users", timeline.Events[0].Step.String())
			require.Equal(t, "run_completed", timeline.Events[1].Type)
			require.True(t, timeline.Failure.OutcomeUncertain)
			require.True(t, timeline.Failure.AutomaticRetryProhibited)
			require.EqualValues(t, 3, params.StepCount)
			require.Equal(t, "fetch-users", params.StepSlug)
			require.Equal(t, "data_source", *params.FailureKind)
			require.Equal(t, "source", *params.FailureSide)
			require.Equal(t, 9, params.StartedAt.Hour())
			require.Len(t, timeline.StructuralChecks, 2)
			require.Equal(t, pipelinerun.CheckStatusPassed, timeline.StructuralChecks[0].Status)
			require.Equal(t, pipelinerun.CheckStatusError, timeline.StructuralChecks[1].Status)
			require.Equal(t, "backfill", params.Incremental.Mode)
			require.Equal(t, "backfill-001", params.Incremental.BackfillSeriesSlug)
			var diagnostics pipelinerun.IncrementalRunDiagnostics
			require.NoError(t, json.Unmarshal(params.IncrementalDiagnostics, &diagnostics))
			require.Len(t, diagnostics.Sources, 1)
			require.NotContains(t, string(params.IncrementalDiagnostics), "opaque-")
			completed = true
			require.Equal(t, args.RunSlug.String(), runSlug)
			require.Equal(t, pipelineRunStatusFailed, params.Status)
			require.Equal(t, "retained provider failure", *params.ErrorSummary)
			return &pipelineruns.PipelineRun{
				EnvironmentSlug:        environmentSlug.String(),
				PipelineSlug:           params.PipelineSlug,
				RunSlug:                runSlug,
				Status:                 params.Status,
				Source:                 args.Source,
				Timeline:               params.Timeline,
				IncrementalDiagnostics: emptyPipelineRunDiagnostics(),
			}, nil
		},
	}
	runtime := &fakePipelineRuntime{executeFn: func(context.Context, pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error) {
		t.Fatal("runtime must not start after entitlement denial")
		return nil, errFakeRepositoryMethodNotUsed
	}}
	svc := &service{
		proofLinks:     newTestProofLinks(),
		admission:      admission,
		txm:            &recordingPipelineGitTxManager{},
		runRepo:        runRepo,
		executionClock: fixedClock{at: time.Date(2026, time.August, 20, 10, 0, 0, 0, time.UTC)},
		runtime:        runtime,
		eventProducer:  eventtest.NewProducer(&fakePipelineEventRecorder{}),
	}

	require.NoError(t, svc.ExecuteQueuedPipelineRun(t.Context(), args))
	require.True(t, completed)
	require.Zero(t, runtime.executeCalls)
}

func TestService_ExecuteQueuedPipelineRunReturnsTransientEntitlementFailure(t *testing.T) {
	t.Parallel()

	transientErr := xerror.Unexpected().Detail("entitlement store unavailable")
	admission := testAdmissionService()
	admission.CheckWriteAccessFn = func(context.Context, slug.Slug) error { return transientErr }
	runtime := &fakePipelineRuntime{executeFn: func(context.Context, pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error) {
		t.Fatal("runtime must not start after a transient entitlement failure")
		return nil, errFakeRepositoryMethodNotUsed
	}}
	svc := &service{
		proofLinks: newTestProofLinks(),
		admission:  admission,
		runRepo: &fakeRunRepository{completeRunFn: func(
			context.Context,
			postgres.DBTX,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			string,
			*pipelineruns.CompleteRunParams,
		) (*pipelineruns.PipelineRun, error) {
			t.Fatal("transient entitlement failures must leave the queued run retryable")
			return nil, errFakeRepositoryMethodNotUsed
		}},
		executionClock: fixedClock{at: time.Date(2026, time.August, 20, 10, 0, 0, 0, time.UTC)},
		runtime:        runtime,
	}

	err := svc.ExecuteQueuedPipelineRun(t.Context(), jobs.PipelineRunExecutionArgs{
		OrganizationSlug: must.Slug("acme"),
	})

	require.True(t, xerror.Is(err, transientErr))
	require.ErrorContains(t, err, "check organization admission")
	require.Zero(t, runtime.executeCalls)
}

func TestService_ExecuteQueuedPipelineRunCompletesPersistedRunFailureWithoutCommercialState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := defaultProjectSlug
	environmentSlug := must.Slug("prod")
	pipelineSlug := must.Slug("runtime-boundary")
	runSlug := must.Slug("manual-run-queued-failed-001")
	versionID := int64(42)
	specHash := "runtime-boundary-hash"
	startedAt := time.Date(2026, time.May, 13, 9, 30, 0, 0, time.UTC)
	endedAt := startedAt.Add(25 * time.Millisecond)
	runErr := xerror.ExecutionFailed().Detail("sink write failed")
	errorSummary := "sink write failed"
	failureKind := "runtime"

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: "Runtime Boundary",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("users-db"),
					OperationType:  "query",
					OperationRef:   must.Slug("list-users"),
				},
			},
		},
	}
	pipelineStore := &runtimeBoundaryPipelineStore{
		version: &pipelinesrepo.PipelineVersion{
			ID:           versionID,
			PipelineSlug: pipelineSlug,
			SpecHash:     specHash,
			SpecJSONB:    mustJSON(t, pipeline),
			CreatedAt:    startedAt.Add(-time.Minute),
		},
	}
	runRepo := &fakeRunRepository{
		markRunRunningFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug, gotProjectSlug, gotEnvironmentSlug slug.Slug,
			gotPipelineSlug slug.Slug,
			gotRunSlug string,
			_ time.Time,
			_ json.RawMessage,
		) (*pipelineruns.PipelineRun, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, projectSlug, gotProjectSlug)
			require.Equal(t, environmentSlug, gotEnvironmentSlug)
			require.Equal(t, pipelineSlug, gotPipelineSlug)
			require.Equal(t, runSlug.String(), gotRunSlug)
			return &pipelineruns.PipelineRun{}, nil
		},
		completeRunFn: func(
			context.Context,
			postgres.DBTX,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			string,
			*pipelineruns.CompleteRunParams,
		) (*pipelineruns.PipelineRun, error) {
			t.Fatal("persisted runtime failure should not be completed a second time")
			return nil, errFakeRepositoryMethodNotUsed
		},
	}
	runtime := &fakePipelineRuntime{
		executeFn: func(_ context.Context, req pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error) {
			require.Equal(t, runSlug.String(), req.RunSlug)
			require.True(t, req.Incremental.SkipCheckpointCommit)
			require.Equal(t, slug.Slug("backfill-002"), req.Incremental.BackfillSeriesSlug)
			return &pipelineruntime.RunOutcome{
				Metadata: pipelineruntime.RunOutcomeMetadata{
					PipelineVersionID: req.PipelineVersionID,
					PipelineSlug:      req.Pipeline.Metadata.Slug,
					EnvironmentSlug:   req.EnvironmentSlug,
					RunSlug:           req.RunSlug,
					Source:            req.Source,
					Status:            pipelineRunStatusFailed,
					DurationMs:        endedAt.Sub(startedAt).Milliseconds(),
					StepCount:         1,
					StepSlug:          "fetch-users",
					ErrorSummary:      &errorSummary,
					FailureKind:       &failureKind,
					StartedAt:         startedAt,
					EndedAt:           &endedAt,
				},
				Timeline: pipelinerun.RunTimeline{
					RunSlug:         req.RunSlug,
					PipelineSlug:    req.Pipeline.Metadata.Slug,
					EnvironmentSlug: req.EnvironmentSlug,
					Status:          pipelineRunStatusFailed,
				},
			}, runErr
		},
	}
	communityEntitlements := admissionsvc.New(testutil.FakeTransactionManager{},
		&testutil.FakeOrgsRepo{FindBySlugFn: func(_ context.Context, _ postgres.DBTX, org slug.Slug) (*organizations.Organization, error) {
			require.Equal(t, orgSlug, org)
			return &organizations.Organization{Slug: org}, nil
		}})

	svc := &service{
		proofLinks:     newTestProofLinks(),
		admission:      communityEntitlements,
		pipelineStore:  pipelineStore,
		txm:            &recordingPipelineGitTxManager{},
		runRepo:        runRepo,
		executionClock: fixedClock{at: startedAt},
		runtime:        runtime,
		eventProducer:  eventtest.NewProducer(&fakePipelineEventRecorder{}),
	}

	err := svc.ExecuteQueuedPipelineRun(ctx, jobs.PipelineRunExecutionArgs{
		Incremental:         pipelinerun.IncrementalRunOptions{SkipCheckpointCommit: true, BackfillSeriesSlug: "backfill-002"},
		OrganizationSlug:    orgSlug,
		ProjectSlug:         projectSlug,
		EnvironmentSlug:     environmentSlug,
		PipelineSlug:        pipelineSlug,
		PipelineVersionHash: specHash,
		RunSlug:             runSlug,
		Actor:               model.UserEventActor(must.Slug("alice")),
		Source:              testRunSource(model.UserEventActor(must.Slug("alice"))),
	})

	require.NoError(t, err)
	require.Equal(t, 1, runtime.executeCalls)
}

func TestService_ExecuteRecordsPreRunValidationFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	pipelineSlug := must.Slug("runtime-inputs")
	versionID := int64(84)
	specHash := "runtime-inputs-hash"
	now := time.Date(2026, time.May, 14, 11, 0, 0, 0, time.UTC)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: "Runtime Inputs",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("users-db"),
					OperationType:  "query",
					OperationRef:   must.Slug("list-users"),
				},
			},
		},
	}
	resolvedPipeline := &pipelinespec.Pipeline{
		Metadata: pipeline.Metadata,
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("prod-users-postgres"),
					OperationType:  "query",
					OperationRef:   must.Slug("list-users"),
				},
			},
		},
	}
	pipelineStore := &runtimeBoundaryPipelineStore{
		spec: &pipelinesrepo.PipelineSpec{
			Slug:                   pipelineSlug,
			SyncStatus:             pipelinesrepo.SyncStatusSynced,
			CurrentVersionID:       &versionID,
			CurrentVersionSpecHash: &specHash,
		},
		version: &pipelinesrepo.PipelineVersion{
			ID:           versionID,
			PipelineSlug: pipelineSlug,
			SpecHash:     specHash,
			SpecJSONB:    mustJSON(t, pipeline),
			CreatedAt:    now.Add(-time.Minute),
		},
		deployment: &pipelinesrepo.PipelineDeployment{
			PipelineSlug:           pipelineSlug,
			EnvironmentSlug:        environmentSlug,
			CurrentVersionID:       versionID,
			CurrentVersionSpecHash: specHash,
		},
	}
	resolver := &fakeDataSourceRefResolver{
		resolvedPipelines: map[slug.Slug]*pipelinespec.Pipeline{
			environmentSlug: resolvedPipeline,
		},
	}
	runtime := &fakePipelineRuntime{
		executeFn: func(_ context.Context, req pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error) {
			require.Equal(t, must.Slug("prod-users-postgres"), req.Pipeline.Steps[0].Query.DataSourceSlug)
			return nil, xerror.InvalidInput(xerror.RuntimeVariable).Detail("input.customer_id is required")
		},
	}
	validationResults := &fakeValidationResultsRepository{createdAt: now}
	svc := &service{
		proofLinks:            newTestProofLinks(),
		admission:             testAdmissionService(),
		dbtx:                  &fakeDBTX{},
		pipelineStore:         pipelineStore,
		txm:                   &recordingPipelineGitTxManager{},
		runRepo:               &fakeRunRepository{},
		validationResults:     validationResults,
		executionClock:        fixedClock{at: now},
		runtime:               runtime,
		dataSourceRefResolver: resolver,
		eventProducer:         eventtest.NewProducer(&fakePipelineEventRecorder{}),
	}

	actor := model.UserEventActor(must.Slug("alice"))
	_, err := svc.Execute(
		ctx,
		orgSlug,
		defaultProjectSlug,
		environmentSlug,
		pipelineSlug,
		nil,
		nil,
		actor,
		testRunSource(actor),
		map[string]any{},
	)
	require.ErrorIs(t, err, ErrPipelineExecutionFailed)
	require.Equal(t, 1, validationResults.createCalls)
	require.Equal(t, pipelineSlug, validationResults.createdParams.PipelineSlug)
	require.Equal(t, PipelineValidationResultSourceExecute.String(), validationResults.createdParams.Source)
	require.False(t, validationResults.createdParams.Valid)
	require.NotNil(t, validationResults.createdParams.SpecHash)
	expectedSpecHash, err := validationSpecHash(pipeline)
	require.NoError(t, err)
	resolvedSpecHash, err := validationSpecHash(resolvedPipeline)
	require.NoError(t, err)
	require.Equal(t, expectedSpecHash, validationResults.createdParams.SpecHash)
	require.NotEqual(t, *resolvedSpecHash, *validationResults.createdParams.SpecHash)

	var diagnostics []PipelineValidationDiagnostic
	require.NoError(t, json.Unmarshal(validationResults.createdParams.DiagnosticsJSONB, &diagnostics))
	require.Len(t, diagnostics, 1)
	require.Equal(t, "RUNTIME_VARIABLE_INVALID_INPUT", diagnostics[0].Code)
	require.Equal(t, "spec", diagnostics[0].Field)
	require.Contains(t, diagnostics[0].Message, "input.customer_id is required")
}

func TestRecordValidationResultTxPersistsEmptyDiagnosticArray(t *testing.T) {
	t.Parallel()

	validationResults := &fakeValidationResultsRepository{}
	svc := &service{validationResults: validationResults}
	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "users-sync",
			Name: "Users Sync",
		},
	}

	record, err := svc.recordValidationResultTx(
		context.Background(),
		&fakeDBTX{},
		"acme",
		defaultProjectSlug,
		"prod",
		PipelineValidationResultSourceAPI,
		pipeline,
		&PipelineValidationResult{Valid: true},
	)

	require.NoError(t, err)
	require.NotNil(t, record)
	require.JSONEq(t, "[]", string(validationResults.createdParams.DiagnosticsJSONB))
	require.Empty(t, record.Diagnostics)
}

func TestService_ExecuteVersionFallsBackToShortSpecHash(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	pipelineSlug := must.Slug("runtime-boundary")
	versionID := int64(42)
	fullSpecHash := "1234567890abcdef"
	shortSpecHash := "1234567890ab"
	startedAt := time.Date(2026, time.May, 13, 9, 30, 0, 0, time.UTC)
	endedAt := startedAt.Add(25 * time.Millisecond)
	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: "Runtime Boundary",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("users-db"),
					OperationType:  "query",
					OperationRef:   must.Slug("list-users"),
				},
			},
		},
	}
	pipelineStore := &runtimeBoundaryPipelineStore{
		versionBySpecHashErr: pipelinesrepo.ErrPipelineVersionNotFound,
		versionBySpecHashPrefix: &pipelinesrepo.PipelineVersion{
			ID:           versionID,
			PipelineSlug: pipelineSlug,
			SpecHash:     fullSpecHash,
			SpecJSONB:    mustJSON(t, pipeline),
			CreatedAt:    startedAt.Add(-time.Minute),
		},
		deployment: &pipelinesrepo.PipelineDeployment{
			PipelineSlug:           pipelineSlug,
			EnvironmentSlug:        environmentSlug,
			CurrentVersionID:       versionID,
			CurrentVersionSpecHash: fullSpecHash,
		},
	}
	runtime := &fakePipelineRuntime{
		executeFn: func(_ context.Context, req pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error) {
			require.Equal(t, versionID, req.PipelineVersionID)
			return &pipelineruntime.RunOutcome{
				Metadata: pipelineruntime.RunOutcomeMetadata{
					EnvironmentSlug:   req.EnvironmentSlug,
					PipelineVersionID: req.PipelineVersionID,
					PipelineSlug:      req.Pipeline.Metadata.Slug,
					RunSlug:           "generated-run",
					Source:            req.Source,
					Status:            "succeeded",
					DurationMs:        endedAt.Sub(startedAt).Milliseconds(),
					StepCount:         len(req.Pipeline.Steps),
					StepSlug:          "fetch-users",
					StartedAt:         startedAt,
					EndedAt:           &endedAt,
				},
				Timeline: pipelinerun.RunTimeline{
					RunSlug:         "generated-run",
					PipelineSlug:    req.Pipeline.Metadata.Slug,
					EnvironmentSlug: req.EnvironmentSlug,
					Status:          "succeeded",
				},
			}, nil
		},
	}
	svc := &service{
		proofLinks:     newTestProofLinks(),
		admission:      testAdmissionService(),
		pipelineStore:  pipelineStore,
		txm:            &recordingPipelineGitTxManager{},
		runRepo:        &fakeRunRepository{},
		executionClock: fixedClock{at: startedAt},
		runtime:        runtime,
		eventProducer:  eventtest.NewProducer(&fakePipelineEventRecorder{}),
	}

	actor := model.UserEventActor(must.Slug("alice"))
	run, err := svc.ExecuteVersion(
		ctx,
		orgSlug,
		defaultProjectSlug,
		environmentSlug,
		pipelineSlug,
		shortSpecHash,
		nil,
		nil,
		actor,
		testRunSource(actor),
		nil,
	)

	require.NoError(t, err)
	require.Equal(t, fullSpecHash, run.Metadata.PipelineVersionSpecHash)
	require.Equal(t, 1, pipelineStore.findVersionBySpecHashCalls)
	require.Equal(t, 1, pipelineStore.findVersionBySpecHashPrefixCalls)
	require.Equal(t, 1, runtime.executeCalls)
	// An environment's resolution cannot be attached to another explicit version.
	digest := "sha256:" + strings.Repeat("a", 64)
	pipelineStore.deployment.CurrentVersionSpecHash = "other-version"
	pipelineStore.deployment.CurrentResolutionDigest = &digest
	_, err = svc.ExecuteVersion(ctx, orgSlug, defaultProjectSlug, environmentSlug, pipelineSlug, shortSpecHash, nil, nil, actor, testRunSource(actor), nil)
	require.NoError(t, err)
	pipelineStore.versionBySpecHashErr = nil
	pipelineStore.version = pipelineStore.versionBySpecHashPrefix
	_, err = svc.ExecuteVerifiedEventVersion(ctx, orgSlug, defaultProjectSlug, environmentSlug, pipelineSlug, fullSpecHash, nil, actor, testRunSource(actor), nil, nil)
	require.NoError(t, err)
	require.Equal(t, 3, runtime.executeCalls)
}

// TestService_ExecuteVersionPassesRetryIdentityToRuntime verifies that scheduled retry and replay identities reach runtime execution.
func TestService_ExecuteVersionPassesRetryIdentityToRuntime(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	pipelineSlug := must.Slug("runtime-boundary")
	retryRunSlug := must.Slug("scheduled-retry-002")
	originalRunSlug := must.Slug("scheduled-run-001")
	expectedPredecessor := originalRunSlug
	versionID := int64(42)
	specHash := "retry-identity-spec-hash"
	startedAt := time.Date(2026, time.May, 13, 9, 30, 0, 0, time.UTC)
	endedAt := startedAt.Add(25 * time.Millisecond)
	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: pipelineSlug, Name: "Runtime Boundary"},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: must.Slug("fetch-users"),
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: must.Slug("users-db"),
				OperationType:  "query",
				OperationRef:   must.Slug("list-users"),
			},
		}},
	}
	pipelineStore := &runtimeBoundaryPipelineStore{
		version: &pipelinesrepo.PipelineVersion{
			ID:           versionID,
			PipelineSlug: pipelineSlug,
			SpecHash:     specHash,
			SpecJSONB:    mustJSON(t, pipeline),
			CreatedAt:    startedAt.Add(-time.Minute),
		},
		deployment: &pipelinesrepo.PipelineDeployment{
			PipelineSlug:           pipelineSlug,
			EnvironmentSlug:        environmentSlug,
			CurrentVersionID:       versionID,
			CurrentVersionSpecHash: specHash,
		},
	}
	runtime := &fakePipelineRuntime{
		executeFn: func(_ context.Context, req pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error) {
			require.Equal(t, retryRunSlug.String(), req.RunSlug)
			require.NotNil(t, req.ReplayOfRunSlug)
			require.Equal(t, expectedPredecessor.String(), *req.ReplayOfRunSlug)
			require.Equal(t, originalRunSlug, req.Source.RootRunSlug)
			return &pipelineruntime.RunOutcome{
				Metadata: pipelineruntime.RunOutcomeMetadata{
					EnvironmentSlug: req.EnvironmentSlug, PipelineVersionID: req.PipelineVersionID,
					PipelineSlug: req.Pipeline.Metadata.Slug, RunSlug: req.RunSlug, Source: req.Source,
					Status: "succeeded", DurationMs: endedAt.Sub(startedAt).Milliseconds(),
					StepCount: len(req.Pipeline.Steps), StepSlug: "fetch-users", StartedAt: startedAt, EndedAt: &endedAt,
				},
				Timeline: pipelinerun.RunTimeline{RunSlug: req.RunSlug, PipelineSlug: req.Pipeline.Metadata.Slug, EnvironmentSlug: req.EnvironmentSlug, Status: "succeeded"},
			}, nil
		},
	}
	svc := &service{
		proofLinks:    newTestProofLinks(),
		admission:     testAdmissionService(),
		pipelineStore: pipelineStore, txm: &recordingPipelineGitTxManager{}, runRepo: &fakeRunRepository{},
		executionClock: fixedClock{at: startedAt}, runtime: runtime, eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
	}
	source := pipelinerun.RunSourceSummary{
		Type: model.PipelineRunSourceTypeSchedule, Summary: "Scheduled retry", Entrypoint: model.PipelineRunSourceEntrypointScheduler,
		ActorType: model.EventActorTypeSystem, ParameterKeys: []string{}, AttemptNumber: 2, RootRunSlug: originalRunSlug,
		RetryOfRunSlug: &originalRunSlug, Schedule: &pipelinerun.RunSourceScheduleSummary{
			TriggerSlug: must.Slug("daily-users"), TriggerRevision: 1, Cron: "0 7 * * *", Timezone: "UTC",
			ScheduledFireAt: startedAt, ActualFireAt: startedAt, MisfireResult: model.PipelineTriggerScheduleMisfireResultOnTime,
		},
	}

	for attempt := 2; attempt <= 3; attempt++ {
		source.AttemptNumber = attempt
		if attempt == 3 {
			previous := retryRunSlug
			source.RetryOfRunSlug = &previous
			expectedPredecessor = previous
			retryRunSlug = must.Slug("scheduled-retry-003")
		}
		_, err := svc.ExecuteVersion(ctx, orgSlug, defaultProjectSlug, environmentSlug, pipelineSlug, specHash, &retryRunSlug, nil, model.SystemEventActor(), source, nil)
		require.NoError(t, err)
	}
	require.Equal(t, 2, runtime.executeCalls)
}

func TestService_ExecuteUsesDeploymentVersionAndResolvedPipelineForEnvironment(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := defaultProjectSlug
	environmentSlug := must.Slug("prod")
	pipelineSlug := must.Slug("runtime-boundary")
	versionID := int64(42)
	currentVersionID := int64(99)
	currentSpecHash := "head-spec-hash"
	deployedSpecHash := "prod-deployed-spec-hash"
	startedAt := time.Date(2026, time.May, 13, 9, 30, 0, 0, time.UTC)
	endedAt := startedAt.Add(25 * time.Millisecond)

	logicalPipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: "Runtime Boundary",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("users-source"),
					OperationType:  "sql",
					OperationRef:   must.Slug("list-users"),
				},
			},
		},
	}
	resolvedPipeline := &pipelinespec.Pipeline{
		Metadata: logicalPipeline.Metadata,
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("prod-users-postgres"),
					OperationType:  "sql",
					OperationRef:   must.Slug("list-users"),
				},
			},
		},
	}

	pipelineStore := &runtimeBoundaryPipelineStore{
		spec: &pipelinesrepo.PipelineSpec{
			Slug:                   pipelineSlug,
			SyncStatus:             pipelinesrepo.SyncStatusSynced,
			CurrentVersionID:       &currentVersionID,
			CurrentVersionSpecHash: &currentSpecHash,
		},
		version: &pipelinesrepo.PipelineVersion{
			ID:           versionID,
			PipelineSlug: pipelineSlug,
			SpecHash:     deployedSpecHash,
			SpecJSONB:    mustJSON(t, logicalPipeline),
			CreatedAt:    startedAt.Add(-time.Minute),
		},
		deployment: &pipelinesrepo.PipelineDeployment{
			PipelineSlug:           pipelineSlug,
			EnvironmentSlug:        environmentSlug,
			CurrentVersionID:       versionID,
			CurrentVersionSpecHash: deployedSpecHash,
		},
	}
	resolver := &fakeDataSourceRefResolver{
		resolvedPipelines: map[slug.Slug]*pipelinespec.Pipeline{
			environmentSlug: resolvedPipeline,
		},
	}
	runtime := &fakePipelineRuntime{
		executeFn: func(_ context.Context, req pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error) {
			require.Equal(t, orgSlug, req.OrgSlug)
			require.Equal(t, projectSlug, req.ProjectSlug)
			require.Equal(t, environmentSlug, req.EnvironmentSlug)
			require.Equal(t, versionID, req.PipelineVersionID)
			require.Equal(t, must.Slug("prod-users-postgres"), req.Pipeline.Steps[0].Query.DataSourceSlug)
			return &pipelineruntime.RunOutcome{
				Metadata: pipelineruntime.RunOutcomeMetadata{
					EnvironmentSlug:   req.EnvironmentSlug,
					PipelineVersionID: req.PipelineVersionID,
					PipelineSlug:      req.Pipeline.Metadata.Slug,
					RunSlug:           "generated-run",
					Source:            req.Source,
					Status:            "succeeded",
					DurationMs:        endedAt.Sub(startedAt).Milliseconds(),
					StepCount:         len(req.Pipeline.Steps),
					StepSlug:          "fetch-users",
					StartedAt:         startedAt,
					EndedAt:           &endedAt,
				},
				Timeline: pipelinerun.RunTimeline{
					RunSlug:         "generated-run",
					PipelineSlug:    req.Pipeline.Metadata.Slug,
					EnvironmentSlug: req.EnvironmentSlug,
					Status:          "succeeded",
				},
			}, nil
		},
	}

	svc := &service{
		proofLinks:            newTestProofLinks(),
		admission:             testAdmissionService(),
		pipelineStore:         pipelineStore,
		txm:                   &recordingPipelineGitTxManager{},
		runRepo:               &fakeRunRepository{},
		executionClock:        fixedClock{at: startedAt},
		runtime:               runtime,
		dataSourceRefResolver: resolver,
		eventProducer:         eventtest.NewProducer(&fakePipelineEventRecorder{}),
	}

	actor := model.UserEventActor(must.Slug("alice"))
	run, err := svc.Execute(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
		nil,
		nil,
		actor,
		testRunSource(actor),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, environmentSlug.String(), run.Metadata.EnvironmentSlug)
	require.Equal(t, 1, runtime.executeCalls)
	require.Equal(t, environmentSlug, resolver.lastEnvironmentSlug)
	require.Equal(t, must.Slug("users-source"), resolver.lastPipeline.Steps[0].Query.DataSourceSlug)
	require.Equal(t, 1, pipelineStore.findDeploymentCalls)
}

func TestValidateRunSourceSummary(t *testing.T) {
	t.Parallel()

	actor := model.UserEventActor(must.Slug("alice"))
	validSource := pipelinerun.RunSourceSummary{
		Type:          model.PipelineRunSourceTypeManual,
		Summary:       "Manual run",
		Entrypoint:    model.PipelineRunSourceEntrypointUI,
		ActorType:     model.EventActorTypeUser,
		ActorSlug:     actor.Slug,
		ParameterKeys: []string{"run_slug"},
	}
	retryRootRunSlug := must.Slug("fire-daily-users-20260609")
	retryOfRunSlug := must.Slug("fire-daily-users-20260609")
	invalidRootRunSlug := slug.Slug("root run")
	invalidRetryOfRunSlug := slug.Slug("retry of run")
	validWebhookSource := pipelinerun.RunSourceSummary{
		Type:          model.PipelineRunSourceTypeWebhook,
		Summary:       "Webhook run from trigger customer-hook",
		Entrypoint:    model.PipelineRunSourceEntrypointWebhook,
		ActorType:     model.EventActorTypeExternal,
		ParameterKeys: []string{"customer"},
		AttemptNumber: 1,
		RootRunSlug:   must.Slug("webhook-run"),
		Webhook: &pipelinerun.RunSourceWebhookSummary{
			DeliveryRecordID: 42,
			TriggerSlug:      must.Slug("customer-hook"), TriggerRevision: 3,
			DeliveryID: "delivery-1", PayloadDigest: strings.Repeat("a", 64), PayloadSizeBytes: 17,
			ReceivedAt: time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC), VerificationStatus: "verified",
		},
	}
	retryRunSource := func(attemptNumber int, rootRunSlug slug.Slug, retryOfRunSlug *slug.Slug) pipelinerun.RunSourceSummary {
		return pipelinerun.RunSourceSummary{
			Type:           model.PipelineRunSourceTypeSchedule,
			Summary:        "Scheduled run from trigger daily-users",
			Entrypoint:     model.PipelineRunSourceEntrypointScheduler,
			ActorType:      model.EventActorTypeSystem,
			ParameterKeys:  []string{},
			AttemptNumber:  attemptNumber,
			RootRunSlug:    rootRunSlug,
			RetryOfRunSlug: retryOfRunSlug,
			Schedule: &pipelinerun.RunSourceScheduleSummary{
				TriggerSlug:     must.Slug("daily-users"),
				TriggerRevision: 1,
				Cron:            "0 7 * * *",
				Timezone:        "UTC",
				ScheduledFireAt: time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC),
				ActualFireAt:    time.Date(2026, time.June, 9, 7, 5, 0, 0, time.UTC),
				MisfireResult:   model.PipelineTriggerScheduleMisfireResultOnTime,
			},
		}
	}

	tests := []struct {
		name    string
		source  pipelinerun.RunSourceSummary
		want    pipelinerun.RunSourceSummary
		wantErr string
	}{
		{
			name:   "AcceptsVerifiedWebhookSource",
			source: validWebhookSource,
			want:   validWebhookSource,
		},
		{
			name: "RejectsMissingWebhookDeliveryRecord",
			source: func() pipelinerun.RunSourceSummary {
				source := validWebhookSource
				webhook := *validWebhookSource.Webhook
				source.Webhook = &webhook
				source.Webhook.DeliveryRecordID = 0
				return source
			}(),
			wantErr: "webhook run source summary is invalid",
		},
		{
			name: "RejectsNegativeWebhookDeliveryRecord",
			source: func() pipelinerun.RunSourceSummary {
				source := validWebhookSource
				webhook := *validWebhookSource.Webhook
				source.Webhook = &webhook
				source.Webhook.DeliveryRecordID = -1
				return source
			}(),
			wantErr: "webhook run source summary is invalid",
		},
		{
			name: "NormalizesSafeTextMetadata",
			source: pipelinerun.RunSourceSummary{
				Type:          model.PipelineRunSourceTypeManual,
				Summary:       " Manual run ",
				Entrypoint:    model.PipelineRunSourceEntrypointUI,
				ActorType:     model.EventActorTypeUser,
				ActorSlug:     actor.Slug,
				ParameterKeys: []string{"run_slug", "run_slug", " protected_environment_confirmation "},
			},
			want: pipelinerun.RunSourceSummary{
				Type:          model.PipelineRunSourceTypeManual,
				Summary:       "Manual run",
				Entrypoint:    model.PipelineRunSourceEntrypointUI,
				ActorType:     model.EventActorTypeUser,
				ActorSlug:     actor.Slug,
				ParameterKeys: []string{"protected_environment_confirmation", "run_slug"},
				AttemptNumber: 1,
			},
		},
		{
			name:    "RejectsMissingType",
			source:  pipelinerun.RunSourceSummary{},
			wantErr: "run source type is required",
		},
		{
			name: "RejectsUnsupportedType",
			source: func() pipelinerun.RunSourceSummary {
				source := validSource
				source.Type = model.PipelineRunSourceType(99)
				return source
			}(),
			wantErr: "run source type must be manual, api, schedule, or webhook",
		},
		{
			name: "RejectsMissingSummary",
			source: func() pipelinerun.RunSourceSummary {
				source := validSource
				source.Summary = " "
				return source
			}(),
			wantErr: "run source summary is required",
		},
		{
			name: "RejectsMissingEntrypoint",
			source: func() pipelinerun.RunSourceSummary {
				source := validSource
				source.Entrypoint = model.PipelineRunSourceEntrypointUnknown
				return source
			}(),
			wantErr: "run source entrypoint is required",
		},
		{
			name: "RejectsUnsupportedEntrypoint",
			source: func() pipelinerun.RunSourceSummary {
				source := validSource
				source.Entrypoint = model.PipelineRunSourceEntrypoint(99)
				return source
			}(),
			wantErr: "run source entrypoint must be ui, api, scheduler, or webhook",
		},
		{
			name: "RejectsMissingActorType",
			source: func() pipelinerun.RunSourceSummary {
				source := validSource
				source.ActorType = model.EventActorTypeUnknown
				return source
			}(),
			wantErr: "run source actor type is required",
		},
		{
			name: "RejectsUnsupportedActorType",
			source: func() pipelinerun.RunSourceSummary {
				source := validSource
				source.ActorType = model.EventActorType(99)
				return source
			}(),
			wantErr: "run source actor type must be a known event actor type",
		},
		{
			name:   "AcceptsRetryRunSource",
			source: retryRunSource(2, retryRootRunSlug, &retryOfRunSlug),
			want:   retryRunSource(2, retryRootRunSlug, &retryOfRunSlug),
		},
		{
			name:    "RejectsRetryAttemptMissingRootRunSlug",
			source:  retryRunSource(2, "", &retryOfRunSlug),
			wantErr: "retry run source root_run_slug is required",
		},
		{
			name:    "RejectsRetryAttemptInvalidRootRunSlug",
			source:  retryRunSource(2, invalidRootRunSlug, &retryOfRunSlug),
			wantErr: "retry run source root_run_slug is invalid",
		},
		{
			name:    "RejectsRetryAttemptMissingRetryOfRunSlug",
			source:  retryRunSource(2, retryRootRunSlug, nil),
			wantErr: "retry run source retry_of_run_slug is required",
		},
		{
			name:    "RejectsRetryAttemptInvalidRetryOfRunSlug",
			source:  retryRunSource(2, retryRootRunSlug, &invalidRetryOfRunSlug),
			wantErr: "retry run source retry_of_run_slug is invalid",
		},
		{
			name: "RejectsFirstAttemptWithRetryOfRunSlug",
			source: func() pipelinerun.RunSourceSummary {
				source := validSource
				source.AttemptNumber = 1
				source.RetryOfRunSlug = &retryOfRunSlug
				return source
			}(),
			wantErr: "first run attempt must not include retry_of_run_slug",
		},
		{
			name: "RejectsSkippedScheduledRunSource",
			source: pipelinerun.RunSourceSummary{
				Type:       model.PipelineRunSourceTypeSchedule,
				Summary:    "Scheduled run from trigger daily-users",
				Entrypoint: model.PipelineRunSourceEntrypointScheduler,
				ActorType:  model.EventActorTypeSystem,
				Schedule: &pipelinerun.RunSourceScheduleSummary{
					TriggerSlug:     must.Slug("daily-users"),
					TriggerRevision: 1,
					Cron:            "0 7 * * *",
					Timezone:        "UTC",
					ScheduledFireAt: time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC),
					ActualFireAt:    time.Date(2026, time.June, 9, 7, 5, 0, 0, time.UTC),
					MisfireResult:   model.PipelineTriggerScheduleMisfireResultSkipped,
				},
			},
			wantErr: "scheduled run source schedule summary is invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := validateRunSourceSummary(tt.source)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestService_ExecuteEmitsPipelineRunEvent(t *testing.T) {
	t.Parallel()

	requestID := "req-manual-001"
	ctx := reqctx.WithRequestID(context.Background(), requestID)
	orgSlug := must.Slug("acme")
	projectSlug := defaultProjectSlug
	environmentSlug := must.Slug("prod")
	pipelineSlug := must.Slug("runtime-boundary")
	runSlug := must.Slug("manual-run-001")
	actorSlug := must.Slug("alice")
	versionID := int64(42)
	specHash := "runtime-boundary-hash"
	startedAt := time.Date(2026, time.May, 13, 9, 30, 0, 0, time.UTC)
	endedAt := startedAt.Add(25 * time.Millisecond)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: "Runtime Boundary",
		},
		Steps: []pipelinespec.Step{{Kind: "query", Slug: must.Slug("fetch-users")}},
	}
	pipelineStore := &runtimeBoundaryPipelineStore{
		spec: &pipelinesrepo.PipelineSpec{
			Slug:                   pipelineSlug,
			SyncStatus:             pipelinesrepo.SyncStatusSynced,
			CurrentVersionID:       &versionID,
			CurrentVersionSpecHash: &specHash,
		},
		version: &pipelinesrepo.PipelineVersion{
			ID:           versionID,
			PipelineSlug: pipelineSlug,
			SpecHash:     specHash,
			SpecJSONB:    mustJSON(t, pipeline),
		},
		deployment: &pipelinesrepo.PipelineDeployment{
			PipelineSlug:           pipelineSlug,
			EnvironmentSlug:        environmentSlug,
			CurrentVersionID:       versionID,
			CurrentVersionSpecHash: specHash,
		},
	}
	runtime := &fakePipelineRuntime{
		executeFn: func(_ context.Context, req pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error) {
			return &pipelineruntime.RunOutcome{
				Metadata: pipelineruntime.RunOutcomeMetadata{
					PipelineVersionID: req.PipelineVersionID,
					PipelineSlug:      req.Pipeline.Metadata.Slug,
					EnvironmentSlug:   req.EnvironmentSlug,
					RunSlug:           req.RunSlug,
					Source:            req.Source,
					Status:            "succeeded",
					DurationMs:        endedAt.Sub(startedAt).Milliseconds(),
					StepCount:         1,
					StepSlug:          "fetch-users",
					StartedAt:         startedAt,
					EndedAt:           &endedAt,
				},
				Timeline: pipelinerun.RunTimeline{
					RunSlug:      req.RunSlug,
					PipelineSlug: req.Pipeline.Metadata.Slug,
					Status:       "succeeded",
				},
			}, nil
		},
	}
	events := &fakePipelineEventRecorder{}
	runRepo := &fakeRunRepository{}

	svc := &service{
		proofLinks:     newTestProofLinks(),
		admission:      testAdmissionService(),
		pipelineStore:  pipelineStore,
		txm:            &recordingPipelineGitTxManager{},
		runRepo:        runRepo,
		executionClock: fixedClock{at: startedAt},
		runtime:        runtime,
		eventProducer:  eventtest.NewProducer(events),
	}

	actor := model.UserEventActor(actorSlug)
	source := testRunSource(actor)
	source.RequestID = &requestID
	run, err := svc.Execute(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
		&runSlug,
		nil,
		actor,
		source,
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, runSlug.String(), run.Metadata.RunSlug)
	require.Equal(t, model.PipelineRunSourceTypeManual, run.Metadata.Source.Type)
	require.Equal(t, "Manual run", run.Metadata.Source.Summary)
	require.Equal(t, model.PipelineRunSourceEntrypointUI, run.Metadata.Source.Entrypoint)
	require.Equal(t, []string{}, run.Metadata.Source.ParameterKeys)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.Equal(t, 1, runRepo.updateRunSourceEventCalls)
	require.Equal(t, got.Slug, runRepo.lastUpdateRunSourceEventSlug)

	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypePipelineRun, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	expectedEntitySlug, err := pipelineRunEntitySlug(projectSlug, environmentSlug, runSlug)
	require.NoError(t, err)
	require.Equal(t, expectedEntitySlug, *got.EntitySlug)
	devEntitySlug, err := pipelineRunEntitySlug(projectSlug, must.Slug("dev"), runSlug)
	require.NoError(t, err)
	require.NotEqual(t, devEntitySlug, *got.EntitySlug)
	require.Equal(t, model.EventActionExecute, got.Action)
	require.Equal(t, map[string]any{
		"project_slug":                   projectSlug.String(),
		"environment_slug":               environmentSlug.String(),
		"pipeline_slug":                  pipelineSlug.String(),
		"pipeline_run_slug":              runSlug.String(),
		"pipeline_version_spec_hash":     specHash,
		"pipeline_run_status":            "succeeded",
		"pipeline_run_duration_ms":       int64(25),
		"pipeline_run_step_count":        int32(1),
		"pipeline_run_last_step_slug":    "fetch-users",
		"pipeline_run_source_type":       "manual",
		"pipeline_run_source_summary":    "Manual run",
		"pipeline_run_source_entrypoint": "ui",
		"request_id":                     requestID,
	}, got.Metadata)
}

func TestService_ExecuteIgnoresPipelineRunEventRecordingFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := defaultProjectSlug
	environmentSlug := must.Slug("prod")
	pipelineSlug := must.Slug("runtime-boundary")
	runSlug := must.Slug("manual-run-001")
	versionID := int64(42)
	specHash := "runtime-boundary-hash"
	startedAt := time.Date(2026, time.May, 13, 9, 30, 0, 0, time.UTC)
	endedAt := startedAt.Add(25 * time.Millisecond)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: "Runtime Boundary",
		},
		Steps: []pipelinespec.Step{{Kind: "query", Slug: must.Slug("fetch-users")}},
	}
	pipelineStore := &runtimeBoundaryPipelineStore{
		spec: &pipelinesrepo.PipelineSpec{
			Slug:                   pipelineSlug,
			SyncStatus:             pipelinesrepo.SyncStatusSynced,
			CurrentVersionID:       &versionID,
			CurrentVersionSpecHash: &specHash,
		},
		version: &pipelinesrepo.PipelineVersion{
			ID:           versionID,
			PipelineSlug: pipelineSlug,
			SpecHash:     specHash,
			SpecJSONB:    mustJSON(t, pipeline),
		},
		deployment: &pipelinesrepo.PipelineDeployment{
			PipelineSlug:           pipelineSlug,
			EnvironmentSlug:        environmentSlug,
			CurrentVersionID:       versionID,
			CurrentVersionSpecHash: specHash,
		},
	}
	runtime := &fakePipelineRuntime{
		executeFn: func(_ context.Context, req pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error) {
			return &pipelineruntime.RunOutcome{
				Metadata: pipelineruntime.RunOutcomeMetadata{
					PipelineVersionID: req.PipelineVersionID,
					PipelineSlug:      req.Pipeline.Metadata.Slug,
					RunSlug:           req.RunSlug,
					Source:            req.Source,
					Status:            "succeeded",
					DurationMs:        endedAt.Sub(startedAt).Milliseconds(),
					StepCount:         1,
					StepSlug:          "fetch-users",
					StartedAt:         startedAt,
					EndedAt:           &endedAt,
				},
				Timeline: pipelinerun.RunTimeline{
					RunSlug:      req.RunSlug,
					PipelineSlug: req.Pipeline.Metadata.Slug,
					Status:       "succeeded",
				},
			}, nil
		},
	}

	svc := &service{
		proofLinks:     newTestProofLinks(),
		admission:      testAdmissionService(),
		pipelineStore:  pipelineStore,
		txm:            &recordingPipelineGitTxManager{},
		executionClock: fixedClock{at: startedAt},
		runtime:        runtime,
		eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{
			err: errFakeRepositoryMethodNotUsed,
		}),
	}

	actor := model.UserEventActor(must.Slug("alice"))
	run, err := svc.Execute(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
		&runSlug,
		nil,
		actor,
		testRunSource(actor),
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, run)
	require.Equal(t, runSlug.String(), run.Metadata.RunSlug)
	require.Equal(t, "succeeded", run.Metadata.Status)
}

func TestService_ExecuteUsesCurrentVersionSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	pipelineSlug := must.Slug("snapshot-boundary")
	versionID := int64(42)
	specHash := "snapshot-hash-42"
	startedAt := time.Date(2026, time.May, 13, 10, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(15 * time.Millisecond)

	mutablePipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: "Mutable Spec",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-stale-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("users-db"),
					OperationType:  "query",
					OperationRef:   must.Slug("list-stale-users"),
				},
			},
		},
	}
	versionPipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: "Version Snapshot",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-versioned-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("users-db"),
					OperationType:  "query",
					OperationRef:   must.Slug("list-versioned-users"),
				},
			},
		},
	}

	pipelineStore := &runtimeBoundaryPipelineStore{
		spec: &pipelinesrepo.PipelineSpec{
			Slug:                   pipelineSlug,
			SyncStatus:             pipelinesrepo.SyncStatusSynced,
			CurrentVersionID:       &versionID,
			CurrentVersionSpecHash: &specHash,
		},
		pipeline: mutablePipeline,
		version: &pipelinesrepo.PipelineVersion{
			ID:           versionID,
			PipelineSlug: pipelineSlug,
			SpecHash:     specHash,
			SpecJSONB:    mustJSON(t, versionPipeline),
			CreatedAt:    startedAt.Add(-time.Minute),
		},
		deployment: &pipelinesrepo.PipelineDeployment{
			PipelineSlug:           pipelineSlug,
			EnvironmentSlug:        environmentSlug,
			CurrentVersionID:       versionID,
			CurrentVersionSpecHash: specHash,
		},
	}
	runtime := &fakePipelineRuntime{
		executeFn: func(_ context.Context, req pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error) {
			require.Equal(t, orgSlug, req.OrgSlug)
			require.Equal(t, versionID, req.PipelineVersionID)
			require.Equal(t, pipelineSlug, req.Pipeline.Metadata.Slug)
			require.Equal(t, "Version Snapshot", req.Pipeline.Metadata.Name)
			require.Len(t, req.Pipeline.Steps, 1)
			require.Equal(t, must.Slug("fetch-versioned-users"), req.Pipeline.Steps[0].Slug)

			return &pipelineruntime.RunOutcome{
				Metadata: pipelineruntime.RunOutcomeMetadata{
					EnvironmentSlug:   req.EnvironmentSlug,
					PipelineVersionID: req.PipelineVersionID,
					PipelineSlug:      req.Pipeline.Metadata.Slug,
					RunSlug:           "generated-run",
					Source:            req.Source,
					Status:            "succeeded",
					DurationMs:        endedAt.Sub(startedAt).Milliseconds(),
					StepCount:         len(req.Pipeline.Steps),
					StepSlug:          req.Pipeline.Steps[0].Slug.String(),
					StartedAt:         startedAt,
					EndedAt:           &endedAt,
				},
				Timeline: pipelinerun.RunTimeline{
					RunSlug:         "generated-run",
					PipelineSlug:    req.Pipeline.Metadata.Slug,
					EnvironmentSlug: req.EnvironmentSlug,
					Status:          "succeeded",
				},
			}, nil
		},
	}

	svc := &service{
		proofLinks:     newTestProofLinks(),
		admission:      testAdmissionService(),
		pipelineStore:  pipelineStore,
		txm:            &recordingPipelineGitTxManager{},
		runRepo:        &fakeRunRepository{},
		executionClock: fixedClock{at: startedAt},
		runtime:        runtime,
		eventProducer:  eventtest.NewProducer(&fakePipelineEventRecorder{}),
	}

	actor := model.UserEventActor(must.Slug("alice"))
	run, err := svc.Execute(
		ctx,
		orgSlug,
		defaultProjectSlug,
		environmentSlug,
		pipelineSlug,
		nil,
		nil,
		actor,
		testRunSource(actor),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, versionID, run.Metadata.PipelineVersionID)
	require.Equal(t, "fetch-versioned-users", run.Metadata.StepSlug)
	require.Equal(t, 1, runtime.executeCalls)
	require.Zero(t, pipelineStore.findSpecCalls)
	require.Equal(t, 1, pipelineStore.findDeploymentCalls)
	require.Equal(t, 1, pipelineStore.findVersionBySpecHashCalls)
	require.Zero(t, pipelineStore.getCalls)
}

type fakePipelineRuntime struct {
	prepareBootstrapFn func(context.Context, pipelineruntime.RunRequest) ([]pipelinerun.IncrementalCheckpointLookup, error)
	recoverFn          func(context.Context, pipelineruntime.ReplayRequest) (*pipelineruntime.RunOutcome, error)
	executeCalls       int
	executeFn          func(context.Context, pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error)
}

func (f *fakePipelineRuntime) Execute(
	ctx context.Context,
	req pipelineruntime.RunRequest,
) (*pipelineruntime.RunOutcome, error) {
	f.executeCalls++
	return f.executeFn(ctx, req)
}

func (f *fakePipelineRuntime) PreValidateTransforms(
	context.Context,
	slug.Slug,
	*pipelinespec.Pipeline,
) error {
	return nil
}

type fakeValidationResultsRepository struct {
	createCalls   int
	createdParams pipelinevalidationsrepo.CreateParams
	createdAt     time.Time
}

func (f *fakeValidationResultsRepository) Create(
	_ context.Context,
	_ postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params pipelinevalidationsrepo.CreateParams,
) (*pipelinevalidationsrepo.Result, error) {
	f.createCalls++
	f.createdParams = params
	return &pipelinevalidationsrepo.Result{
		Slug:             params.Slug,
		OrgSlug:          orgSlug,
		ProjectSlug:      projectSlug,
		EnvironmentSlug:  environmentSlug,
		PipelineSlug:     params.PipelineSlug,
		Source:           params.Source,
		Valid:            params.Valid,
		SpecHash:         params.SpecHash,
		DiagnosticsJSONB: params.DiagnosticsJSONB,
		ImpactReportSlug: params.ImpactReportSlug,
		CreatedAt:        f.createdAt,
		UpdatedAt:        f.createdAt,
	}, nil
}

func (f *fakeValidationResultsRepository) FindBySlug(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*pipelinevalidationsrepo.Result, error) {
	return nil, pipelinevalidationsrepo.ErrResultNotFound
}

func (f *fakeValidationResultsRepository) List(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	pipelinevalidationsrepo.ListFilters,
) ([]*pipelinevalidationsrepo.Result, error) {
	return nil, nil
}

type fixedClock struct {
	at time.Time
}

func (c fixedClock) Now() time.Time {
	return c.at
}

type runtimeBoundaryPipelineStore struct {
	spec                             *pipelinesrepo.PipelineSpec
	pipeline                         *pipelinespec.Pipeline
	version                          *pipelinesrepo.PipelineVersion
	versionBySpecHashErr             error
	versionBySpecHashPrefix          *pipelinesrepo.PipelineVersion
	versionBySpecHashPrefixErr       error
	deployment                       *pipelinesrepo.PipelineDeployment
	findSpecCalls                    int
	findDeploymentCalls              int
	getCalls                         int
	findVersionBySpecHashCalls       int
	findVersionBySpecHashPrefixCalls int
}

func (*runtimeBoundaryPipelineStore) ResolvePipelineContracts(_ context.Context, _, _ slug.Slug, p *pipelinespec.Pipeline) (*pipelinespec.Pipeline, []model.PinnedODCSPort, error) {
	return p, nil, nil
}

func (*runtimeBoundaryPipelineStore) ResolveProposalAuthority(
	context.Context,
	slug.Slug,
	slug.Slug,
) (model.PipelineProposalAuthority, error) {
	return model.PipelineProposalAuthority{}, errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) FindSpec(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*pipelinesrepo.PipelineSpec, error) {
	s.findSpecCalls++
	return s.spec, nil
}

func (s *runtimeBoundaryPipelineStore) FindSpecForUpdate(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (*pipelinesrepo.PipelineSpec, error) {
	return s.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug)
}

func (s *runtimeBoundaryPipelineStore) Get(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*pipelinespec.Pipeline, error) {
	s.getCalls++
	return s.pipeline, nil
}

func (s *runtimeBoundaryPipelineStore) Save(
	context.Context,
	slug.Slug,
	slug.Slug,
	*pipelinespec.Pipeline,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) Delete(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) Restore(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) Purge(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) List(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.ListRequest,
) ([]*pipelinesrepo.PipelineSpec, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) Exists(context.Context, slug.Slug, slug.Slug, slug.Slug) (bool, error) {
	return false, errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) ListVersions(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) ([]*pipelinesrepo.PipelineVersion, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) FindVersionBySpecHash(
	_ context.Context,
	_ slug.Slug,
	_ slug.Slug,
	_ slug.Slug,
	_ string,
) (*pipelinesrepo.PipelineVersion, error) {
	s.findVersionBySpecHashCalls++
	if s.versionBySpecHashErr != nil {
		return nil, s.versionBySpecHashErr
	}
	return s.version, nil
}

func (s *runtimeBoundaryPipelineStore) FindVersionBySpecHashPrefix(
	_ context.Context,
	_ slug.Slug,
	_ slug.Slug,
	_ slug.Slug,
	_ string,
) (*pipelinesrepo.PipelineVersion, error) {
	s.findVersionBySpecHashPrefixCalls++
	if s.versionBySpecHashPrefixErr != nil {
		return nil, s.versionBySpecHashPrefixErr
	}
	if s.versionBySpecHashPrefix != nil {
		return s.versionBySpecHashPrefix, nil
	}
	return nil, errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) Deploy(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	pipelinesrepo.DeployParams,
) (*pipelinesrepo.PipelineDeployment, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) FindDeployment(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*pipelinesrepo.PipelineDeployment, error) {
	s.findDeploymentCalls++
	if s.deployment == nil {
		return nil, errFakeRepositoryMethodNotUsed
	}
	return s.deployment, nil
}

func (s *runtimeBoundaryPipelineStore) ListDeployments(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) ([]*pipelinesrepo.PipelineDeployment, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) ProjectEnvironmentEnabled(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (bool, error) {
	return false, errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) ProjectEnvironmentProtection(
	_ context.Context,
	_, _, environmentSlug slug.Slug,
) (*model.ProjectEnvironmentProtection, error) {
	return &model.ProjectEnvironmentProtection{EnvironmentSlug: environmentSlug}, nil
}

func (s *runtimeBoundaryPipelineStore) FindDraft(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*pipelinesrepo.PipelineDraft, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) SaveDraft(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*pipelinespec.Pipeline,
	*string,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) PublishDraft(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*string,
	bool,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) PrepareGitDraftPublish(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*string,
	*string,
	bool,
) (*GitDraftPublishRequest, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) CompleteProposal(
	context.Context,
	pipelinegit.ProposalCompletion,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) PrepareGitProposalRefresh(
	context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug,
) (*GitProposalRefreshRequest, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) CompleteProposalRefresh(
	context.Context, pipelinegit.ProposalRefreshCompletion,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) FailProposal(
	context.Context,
	pipelinegit.ProposalFailure,
) error {
	return nil
}

func (s *runtimeBoundaryPipelineStore) FailProposalRefresh(
	context.Context, pipelinegit.ProposalRefreshFailure,
) error {
	return nil
}

func (s *runtimeBoundaryPipelineStore) DeleteDraft(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) error {
	return errFakeRepositoryMethodNotUsed
}

func (s *runtimeBoundaryPipelineStore) ReconcileFromGit(
	context.Context,
	slug.Slug,
	slug.Slug,
	repostore.RepoStore,
	ReconcileFromGitParams,
) (*ReconcileFromGitResult, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakePipelineRuntime) Recover(ctx context.Context, req pipelineruntime.ReplayRequest) (*pipelineruntime.RunOutcome, error) {
	if f.recoverFn == nil {
		return nil, xerror.Unexpected().Detail("unexpected recovery")
	}
	return f.recoverFn(ctx, req)
}

func (f *fakePipelineRuntime) PrepareCheckpointBootstrap(ctx context.Context, req pipelineruntime.RunRequest) ([]pipelinerun.IncrementalCheckpointLookup, error) {
	if f.prepareBootstrapFn != nil {
		return f.prepareBootstrapFn(ctx, req)
	}
	return nil, errFakeRepositoryMethodNotUsed
}
