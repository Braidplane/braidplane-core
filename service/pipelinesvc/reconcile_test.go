package pipelinesvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
)

type fakeReconcileGitSourceResolver struct {
	resolveFn func(context.Context, slug.Slug, pipelinegit.SourceRequest) (*pipelinegit.Source, error)
}

func (f fakeReconcileGitSourceResolver) Resolve(
	ctx context.Context,
	orgSlug slug.Slug,
	req pipelinegit.SourceRequest,
) (*pipelinegit.Source, error) {
	return f.resolveFn(ctx, orgSlug, req)
}

func TestService_ReconcilePipelines_UsesResolvedGitSource(t *testing.T) {
	t.Parallel()

	dataSourceSlug := slug.Slug("pipelines-git")
	sourceRepo := "https://example.com/acme/pipelines.git"
	sourceRef := "refs/heads/main"
	lastCommit := "abc123"
	repoStore := &fakeRepoStore{}
	var cleanupCalled bool
	var reconcileRepoStore repostore.RepoStore
	var reconcileParams ReconcileFromGitParams

	svc := &service{
		proofLinks: newTestProofLinks(),
		pipelineStore: &fakeServicePipelineStore{
			reconcileFromGitFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				projectSlug slug.Slug,
				store repostore.RepoStore,
				params ReconcileFromGitParams,
			) (*ReconcileFromGitResult, error) {
				require.Equal(t, slug.Slug("acme"), orgSlug)
				require.Equal(t, defaultProjectSlug, projectSlug)
				reconcileRepoStore = store
				reconcileParams = params
				return &ReconcileFromGitResult{
					Scanned: 1,
					Synced:  1,
					SyncedPipelines: []ReconcileFromGitSynced{
						{
							PipelineSlug: "daily-sync",
							SourcePath:   "pipelines/daily-sync.yaml",
							SpecHash:     "sha256:spec",
						},
					},
				}, nil
			},
		},
		gitSourceResolver: fakeReconcileGitSourceResolver{
			resolveFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				req pipelinegit.SourceRequest,
			) (*pipelinegit.Source, error) {
				require.Equal(t, slug.Slug("acme"), orgSlug)
				require.Equal(t, defaultProjectSlug, req.ProjectSlug)
				return &pipelinegit.Source{
					RepoStore:         repoStore,
					Cleanup:           func() { cleanupCalled = true },
					DataSourceSlug:    dataSourceSlug,
					SourceRepo:        &sourceRepo,
					SourceRef:         &sourceRef,
					LastAppliedCommit: &lastCommit,
				}, nil
			},
		},
	}

	result, err := svc.ReconcilePipelines(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("member-1"),
		model.UserEventActor(slug.Slug("member-1")),
		ReconcilePipelinesRequest{
			DryRun:      true,
			PipelineDir: "custom-pipelines",
		},
	)

	require.NoError(t, err)
	require.True(t, cleanupCalled)
	require.Same(t, repoStore, reconcileRepoStore)
	require.Equal(t, ReconcileFromGitParams{
		PipelineDir:       "custom-pipelines",
		SourceRef:         &sourceRef,
		LastAppliedCommit: &lastCommit,
		DryRun:            true,
	}, reconcileParams)
	require.Equal(t, dataSourceSlug, *result.Source.DataSourceSlug)
	require.Equal(t, sourceRepo, *result.Source.Repo)
	require.Equal(t, sourceRef, *result.Source.Ref)
	require.Equal(t, lastCommit, *result.Source.Commit)
	require.Equal(t, 1, result.Summary.Synced)
	require.Len(t, result.Synced, 1)
	require.Equal(t, slug.Slug("daily-sync"), result.Synced[0].PipelineSlug)
}

func TestService_ReconcilePipelines_UsesResolvedBackendSourceWhenRequestOmitsSource(t *testing.T) {
	t.Parallel()

	dataSourceSlug := slug.Slug("org-backend-git")
	repoStore := &fakeRepoStore{}
	var reconcileParams ReconcileFromGitParams

	svc := &service{
		proofLinks: newTestProofLinks(),
		pipelineStore: &fakeServicePipelineStore{
			reconcileFromGitFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				projectSlug slug.Slug,
				store repostore.RepoStore,
				params ReconcileFromGitParams,
			) (*ReconcileFromGitResult, error) {
				require.Equal(t, slug.Slug("acme"), orgSlug)
				require.Equal(t, defaultProjectSlug, projectSlug)
				require.Same(t, repoStore, store)
				reconcileParams = params
				return &ReconcileFromGitResult{Scanned: 1, Synced: 1}, nil
			},
		},
		gitSourceResolver: fakeReconcileGitSourceResolver{
			resolveFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				req pipelinegit.SourceRequest,
			) (*pipelinegit.Source, error) {
				require.Equal(t, slug.Slug("acme"), orgSlug)
				require.Equal(t, defaultProjectSlug, req.ProjectSlug)
				return &pipelinegit.Source{
					RepoStore:      repoStore,
					DataSourceSlug: dataSourceSlug,
				}, nil
			},
		},
	}

	result, err := svc.ReconcilePipelines(
		context.Background(),
		slug.Slug("acme"),
		defaultProjectSlug,
		slug.Slug("member-1"),
		model.UserEventActor(slug.Slug("member-1")),
		ReconcilePipelinesRequest{},
	)

	require.NoError(t, err)
	require.False(t, reconcileParams.DryRun)
	require.Equal(t, "pipelines", reconcileParams.PipelineDir)
	require.NotNil(t, result.Source.DataSourceSlug)
	require.Equal(t, dataSourceSlug, *result.Source.DataSourceSlug)
}

func TestService_ReconcilePipelines_EmitsEventsForPersistedPipelineChanges(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := defaultProjectSlug
	actorSlug := must.Slug("alice")
	dataSourceSlug := must.Slug("pipelines-git")
	sourceRepo := "https://example.com/acme/pipelines.git"
	sourceRef := "refs/heads/main"
	lastCommit := "abc123"
	repoStore := &fakeRepoStore{}
	events := &fakePipelineEventRecorder{}

	svc := &service{
		proofLinks:            newTestProofLinks(),
		dataSourceRefResolver: &fakeDataSourceRefResolver{},
		pipelineStore: &fakeServicePipelineStore{
			findVersionBySpecHashFn: func(_ context.Context, _, _, pipelineSlug slug.Slug, hash string) (*pipelinesrepo.PipelineVersion, error) {
				return &pipelinesrepo.PipelineVersion{SpecHash: hash, SpecJSONB: mustMarshalPipelineForMergeTest(t, testPipelineSpecWithSlug(pipelineSlug))}, nil
			},
			reconcileFromGitFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
				repostore.RepoStore,
				ReconcileFromGitParams,
			) (*ReconcileFromGitResult, error) {
				deletedPath := "pipelines/removed.pipeline.yaml"
				return &ReconcileFromGitResult{
					Scanned:      3,
					Synced:       2,
					Deleted:      1,
					ResetDrifted: 1,
					SyncedPipelines: []ReconcileFromGitSynced{
						{
							PipelineSlug: "daily-sync",
							SourcePath:   "pipelines/daily-sync.pipeline.yaml",
							SpecHash:     "abc123",
						},
						{
							PipelineSlug: "drifted-sync",
							SourcePath:   "pipelines/drifted-sync.pipeline.yaml",
							SpecHash:     "def456",
						},
					},
					DeletedPipelines: []ReconcileFromGitDeleted{
						{
							PipelineSlug:       "removed-sync",
							PreviousSourcePath: &deletedPath,
						},
					},
					ResetDriftedPipelines: []ReconcileFromGitReset{
						{
							PipelineSlug:     "drifted-sync",
							SourcePath:       "pipelines/drifted-sync.pipeline.yaml",
							PreviousSpecHash: "old123",
							AppliedSpecHash:  "def456",
						},
					},
				}, nil
			},
		},
		gitSourceResolver: fakeReconcileGitSourceResolver{
			resolveFn: func(context.Context, slug.Slug, pipelinegit.SourceRequest) (*pipelinegit.Source, error) {
				return &pipelinegit.Source{
					RepoStore:         repoStore,
					DataSourceSlug:    dataSourceSlug,
					SourceRepo:        &sourceRepo,
					SourceRef:         &sourceRef,
					LastAppliedCommit: &lastCommit,
				}, nil
			},
		},
		txm:           &recordingPipelineGitTxManager{},
		eventProducer: eventtest.NewProducer(events),
	}

	_, err := svc.ReconcilePipelines(
		context.Background(),
		orgSlug,
		projectSlug,
		actorSlug,
		model.UserEventActor(actorSlug),
		ReconcilePipelinesRequest{},
	)
	require.NoError(t, err)

	require.Len(t, events.calls, 3)
	assertPipelineEventWithMetadata(t, events.calls[0], orgSlug, actorSlug, must.Slug("daily-sync"), model.EventActionReconcile, map[string]any{
		"project_slug":               projectSlug.String(),
		"source_kind":                "git",
		"source_data_source_slug":    dataSourceSlug.String(),
		"source_ref":                 sourceRef,
		"source_commit":              lastCommit,
		"reconcile_result":           "synced",
		"source_path":                "pipelines/daily-sync.pipeline.yaml",
		"pipeline_version_spec_hash": "abc123",
	})
	assertPipelineEventWithMetadata(t, events.calls[1], orgSlug, actorSlug, must.Slug("drifted-sync"), model.EventActionReconcile, map[string]any{
		"project_slug":               projectSlug.String(),
		"source_kind":                "git",
		"source_data_source_slug":    dataSourceSlug.String(),
		"source_ref":                 sourceRef,
		"source_commit":              lastCommit,
		"reconcile_result":           "drift_reset",
		"source_path":                "pipelines/drifted-sync.pipeline.yaml",
		"previous_spec_hash":         "old123",
		"pipeline_version_spec_hash": "def456",
	})
	assertPipelineEventWithMetadata(t, events.calls[2], orgSlug, actorSlug, must.Slug("removed-sync"), model.EventActionReconcile, map[string]any{
		"project_slug":            projectSlug.String(),
		"source_kind":             "git",
		"source_data_source_slug": dataSourceSlug.String(),
		"source_ref":              sourceRef,
		"source_commit":           lastCommit,
		"reconcile_result":        "deleted",
		"previous_source_path":    "pipelines/removed.pipeline.yaml",
	})
}

func TestService_ReconcilePipelines_SkipsEventsForDryRun(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := defaultProjectSlug
	actorSlug := must.Slug("alice")
	dataSourceSlug := must.Slug("pipelines-git")
	repoStore := &fakeRepoStore{}

	events := &fakePipelineEventRecorder{}
	svc := &service{
		proofLinks: newTestProofLinks(),
		pipelineStore: &fakeServicePipelineStore{
			reconcileFromGitFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
				repostore.RepoStore,
				ReconcileFromGitParams,
			) (*ReconcileFromGitResult, error) {
				return &ReconcileFromGitResult{
					Scanned: 1,
					Synced:  1,
					SyncedPipelines: []ReconcileFromGitSynced{{
						PipelineSlug: "daily-sync",
						SourcePath:   "pipelines/daily-sync.pipeline.yaml",
						SpecHash:     "abc123",
					}},
				}, nil
			},
		},
		gitSourceResolver: fakeReconcileGitSourceResolver{
			resolveFn: func(context.Context, slug.Slug, pipelinegit.SourceRequest) (*pipelinegit.Source, error) {
				return &pipelinegit.Source{
					RepoStore:      repoStore,
					DataSourceSlug: dataSourceSlug,
				}, nil
			},
		},
		eventProducer: eventtest.NewProducer(events),
	}

	_, err := svc.ReconcilePipelines(
		context.Background(),
		orgSlug,
		projectSlug,
		actorSlug,
		model.UserEventActor(actorSlug),
		ReconcilePipelinesRequest{DryRun: true},
	)
	require.NoError(t, err)
	require.Empty(t, events.calls)
}

func TestService_ReconcilePipelines_ReturnsEventEnqueueError(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := defaultProjectSlug
	actorSlug := must.Slug("alice")
	dataSourceSlug := must.Slug("pipelines-git")
	repoStore := &fakeRepoStore{}
	events := &fakePipelineEventRecorder{err: errFakeRepositoryMethodNotUsed}
	svc := &service{
		proofLinks:            newTestProofLinks(),
		dataSourceRefResolver: &fakeDataSourceRefResolver{},
		pipelineStore: &fakeServicePipelineStore{
			findVersionBySpecHashFn: func(_ context.Context, _, _, pipelineSlug slug.Slug, hash string) (*pipelinesrepo.PipelineVersion, error) {
				return &pipelinesrepo.PipelineVersion{SpecHash: hash, SpecJSONB: mustMarshalPipelineForMergeTest(t, testPipelineSpecWithSlug(pipelineSlug))}, nil
			},
			reconcileFromGitFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
				repostore.RepoStore,
				ReconcileFromGitParams,
			) (*ReconcileFromGitResult, error) {
				return &ReconcileFromGitResult{
					Scanned: 1,
					Synced:  1,
					SyncedPipelines: []ReconcileFromGitSynced{{
						PipelineSlug: "daily-sync",
						SourcePath:   "pipelines/daily-sync.pipeline.yaml",
						SpecHash:     "abc123",
					}},
				}, nil
			},
		},
		gitSourceResolver: fakeReconcileGitSourceResolver{
			resolveFn: func(context.Context, slug.Slug, pipelinegit.SourceRequest) (*pipelinegit.Source, error) {
				return &pipelinegit.Source{
					RepoStore:      repoStore,
					DataSourceSlug: dataSourceSlug,
				}, nil
			},
		},
		txm:           &recordingPipelineGitTxManager{},
		eventProducer: eventtest.NewProducer(events),
	}

	result, err := svc.ReconcilePipelines(
		context.Background(),
		orgSlug,
		projectSlug,
		actorSlug,
		model.UserEventActor(actorSlug),
		ReconcilePipelinesRequest{},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.Summary.Synced)
	require.Len(t, events.calls, 1)
}

func TestReconcilePipelinesFailuresKeepIndependentPipelinePathPointers(t *testing.T) {
	t.Parallel()

	failures := toReconcilePipelinesFailures([]ReconcileFromGitFailure{
		{
			PipelinePath: "pipelines/first.pipeline.yaml",
			Stage:        "load",
			Message:      "first failed",
		},
		{
			PipelinePath: "pipelines/second.pipeline.yaml",
			Stage:        "load",
			Message:      "second failed",
		},
	})

	require.Len(t, failures, 2)
	require.NotNil(t, failures[0].PipelinePath)
	require.NotNil(t, failures[1].PipelinePath)
	require.Equal(t, "pipelines/first.pipeline.yaml", *failures[0].PipelinePath)
	require.Equal(t, "pipelines/second.pipeline.yaml", *failures[1].PipelinePath)
	require.NotSame(t, failures[0].PipelinePath, failures[1].PipelinePath)
}

func TestService_ReconcilePipelines_ExpectedSource(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"match", "data source", "repo", "ref", "commit", "incomplete"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			repo, ref, commit := "https://example.com/pipelines.git", "refs/heads/main", "abc123"
			expected := &ReconcilePipelinesExpectedSource{DataSourceSlug: "git-source", Repo: repo, Ref: ref, Commit: commit}
			switch field {
			case "data source":
				expected.DataSourceSlug = "other"
			case "repo":
				expected.Repo = "https://example.com/other.git"
			case "ref":
				expected.Ref = "refs/heads/other"
			case "commit":
				expected.Commit = "old-commit"
			case "incomplete":
				expected.Commit = ""
			}
			called, cleaned := false, false
			svc := &service{
				pipelineStore: &fakeServicePipelineStore{reconcileFromGitFn: func(_ context.Context, _, _ slug.Slug, _ repostore.RepoStore, params ReconcileFromGitParams) (*ReconcileFromGitResult, error) {
					called = true
					require.False(t, params.DryRun)
					return &ReconcileFromGitResult{}, nil
				}},
				gitSourceResolver: fakeReconcileGitSourceResolver{resolveFn: func(context.Context, slug.Slug, pipelinegit.SourceRequest) (*pipelinegit.Source, error) {
					return &pipelinegit.Source{DataSourceSlug: "git-source", SourceRepo: &repo, SourceRef: &ref, LastAppliedCommit: &commit, Cleanup: func() { cleaned = true }}, nil
				}},
			}
			_, err := svc.ReconcilePipelines(t.Context(), "acme", "default", "member", model.UserEventActor("member"), ReconcilePipelinesRequest{ExpectedSource: expected})
			switch field {
			case "match":
				require.NoError(t, err)
				require.True(t, called)
			case "incomplete":
				require.ErrorIs(t, err, xerror.InvalidInput(xerror.Pipeline))
				require.False(t, called)
			default:
				require.ErrorIs(t, err, ErrPipelineReconcileSourceChanged)
				require.False(t, called)
			}
			require.Equal(t, field != "incomplete", cleaned)
		})
	}
}

func TestService_ReconcilePipelinesRegistersImportedRefsBeforeDeployment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		dryRun     bool
		versionErr error
		syncErr    error
		wantSync   int
	}{
		{name: "apply", wantSync: 1},
		{name: "preview", dryRun: true},
		{name: "version unavailable", versionErr: ErrPipelineVersionNotFound},
		{name: "reference registration fails", syncErr: xerror.Unexpected(), wantSync: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			org, project, pipelineSlug := must.Slug("acme"), defaultProjectSlug, must.Slug("daily-sync")
			actor := model.UserEventActor(must.Slug("alice"))
			resolver := &fakeDataSourceRefResolver{syncErr: tc.syncErr}
			pipeline := testPipelineSpecWithSlug(pipelineSlug)
			store := &fakeServicePipelineStore{
				reconcileFromGitFn: func(context.Context, slug.Slug, slug.Slug, repostore.RepoStore, ReconcileFromGitParams) (*ReconcileFromGitResult, error) {
					return &ReconcileFromGitResult{Synced: 1, SyncedPipelines: []ReconcileFromGitSynced{{PipelineSlug: pipelineSlug.String(), SpecHash: "imported-version"}}}, nil
				},
				findVersionBySpecHashFn: func(_ context.Context, gotOrg, gotProject, gotPipeline slug.Slug, hash string) (*pipelinesrepo.PipelineVersion, error) {
					require.False(t, tc.dryRun)
					require.Equal(t, org, gotOrg)
					require.Equal(t, project, gotProject)
					require.Equal(t, pipelineSlug, gotPipeline)
					require.Equal(t, "imported-version", hash)
					return &pipelinesrepo.PipelineVersion{SpecHash: hash, SpecJSONB: mustMarshalPipelineForMergeTest(t, pipeline)}, tc.versionErr
				},
			}
			svc := &service{
				pipelineStore: store, dataSourceRefResolver: resolver,
				gitSourceResolver: fakeReconcileGitSourceResolver{resolveFn: func(context.Context, slug.Slug, pipelinegit.SourceRequest) (*pipelinegit.Source, error) {
					return &pipelinegit.Source{RepoStore: &fakeRepoStore{}, DataSourceSlug: must.Slug("demo-git")}, nil
				}},
				txm: &recordingPipelineGitTxManager{}, eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{}),
			}
			got, err := svc.ReconcilePipelines(context.Background(), org, project, must.Slug("alice"), actor, ReconcilePipelinesRequest{DryRun: tc.dryRun})
			require.NotNil(t, got)
			if tc.versionErr != nil || tc.syncErr != nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantSync, resolver.syncCalls)
			require.Zero(t, resolver.reconcileCalls, "apply must retain references used by deployed versions")
			if tc.wantSync > 0 {
				require.Equal(t, pipelineSlug, resolver.lastSyncPipeline.Metadata.Slug)
				require.Equal(t, actor, resolver.lastSyncActor)
			}
		})
	}
}
