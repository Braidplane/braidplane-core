package pipelinesvc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
)

func TestProofLifecycleLinkReportsRedeploymentMismatch(t *testing.T) {
	t.Parallel()
	linkedAt := time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC)
	link := &proofreceipts.LifecycleLink{Identifier: "sync:hash:" + linkedAt.Format(time.RFC3339Nano), SpecHash: "hash"}

	got := toProofLifecycleLink(link, "hash", "sync:hash:"+linkedAt.Add(time.Minute).Format(time.RFC3339Nano), linkedAt)

	require.Equal(t, "deployment_mismatch", got.Status)
}

func TestProofFingerprintRequiresResolver(t *testing.T) {
	t.Parallel()
	_, err := proofBindingFingerprint(nil)
	require.ErrorIs(t, err, xerror.ExecutionUnavailable())
	_, err = proofCapabilityFingerprint(nil)
	require.ErrorIs(t, err, xerror.ExecutionUnavailable())
}

func TestProofReceiptMatchesDeployment(t *testing.T) {
	t.Parallel()
	commit, path, ref := "abc123", "pipelines/sync.pipeline.json", "refs/heads/main"
	org, project, environment := must.Slug("acme"), must.Slug("data"), must.Slug("staging")
	receipt := &proofreceipts.Receipt{Value: model.ProofReceipt{Candidate: model.ProofCandidateIdentity{
		OrganizationSlug: org, ProjectSlug: project, EnvironmentSlug: environment,
		HeadSHA: commit, TargetRef: ref, PipelinePath: path, SpecHash: "sha256:" + strings.Repeat("a", 64),
		BindingFingerprint: "binding", CapabilityFingerprint: "capability",
	}}}
	version := &pipelinesrepo.PipelineVersion{GitCommitSHA: &commit, SpecHash: strings.Repeat("a", 64)}
	spec := &pipelinesrepo.PipelineSpec{SourcePath: &path, SourceRef: &ref}

	require.True(t, proofReceiptMatchesDeployment(receipt, org, project, environment, version, spec, "binding", "capability"))
	superseded := *receipt
	superseded.Superseded = true
	require.False(t, proofReceiptMatchesDeployment(&superseded, org, project, environment, version, spec, "binding", "capability"))
	for name, mutate := range map[string]func(*model.ProofCandidateIdentity){
		"organization": func(candidate *model.ProofCandidateIdentity) { candidate.OrganizationSlug = must.Slug("other") },
		"project":      func(candidate *model.ProofCandidateIdentity) { candidate.ProjectSlug = must.Slug("other") },
		"environment":  func(candidate *model.ProofCandidateIdentity) { candidate.EnvironmentSlug = must.Slug("other") },
		"commit":       func(candidate *model.ProofCandidateIdentity) { candidate.HeadSHA = "other" },
		"ref":          func(candidate *model.ProofCandidateIdentity) { candidate.TargetRef = "other" },
		"path":         func(candidate *model.ProofCandidateIdentity) { candidate.PipelinePath = "other" },
		"spec":         func(candidate *model.ProofCandidateIdentity) { candidate.SpecHash = "other" },
		"binding":      func(candidate *model.ProofCandidateIdentity) { candidate.BindingFingerprint = "other" },
		"capability":   func(candidate *model.ProofCandidateIdentity) { candidate.CapabilityFingerprint = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			mismatched := *receipt
			mutate(&mismatched.Value.Candidate)
			require.False(t, proofReceiptMatchesDeployment(&mismatched, org, project, environment, version, spec, "binding", "capability"))
		})
	}
}

func TestService_DeployPointsEnvironmentAtExistingVersion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("backoffice")
	environmentSlug := must.Slug("dev")
	sourceEnvironmentSlug := must.Slug("stg")
	pipelineSlug := must.Slug("sync-users")
	actorSlug := must.Slug("alice")
	specHash := "6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8"
	previousSpecHash := "5c6d7e8f9a0b1c2d3e4f"
	pipeline := testPipelineSpecWithSlug(pipelineSlug)
	deployedAt := time.Date(2026, time.May, 26, 10, 30, 0, 0, time.UTC)
	updatedAt := deployedAt.Add(time.Minute)
	versionID := int64(42)
	previousVersionID := int64(41)

	var deployed bool
	store := &fakeServicePipelineStore{
		findVersionBySpecHashFn: func(
			_ context.Context,
			gotOrgSlug, gotProjectSlug, gotPipelineSlug slug.Slug,
			gotSpecHash string,
		) (*pipelinesrepo.PipelineVersion, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, projectSlug, gotProjectSlug)
			require.Equal(t, pipelineSlug, gotPipelineSlug)
			require.Equal(t, specHash, gotSpecHash)
			return &pipelinesrepo.PipelineVersion{
				ID:           versionID,
				PipelineSlug: pipelineSlug,
				SpecHash:     specHash,
				SpecJSONB:    mustMarshalPipelineForMergeTest(t, pipeline),
			}, nil
		},
		deployFn: func(
			_ context.Context,
			gotOrgSlug, gotProjectSlug, gotEnvironmentSlug slug.Slug,
			params pipelinesrepo.DeployParams,
		) (*pipelinesrepo.PipelineDeployment, error) {
			deployed = true
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, projectSlug, gotProjectSlug)
			require.Equal(t, environmentSlug, gotEnvironmentSlug)
			require.Equal(t, pipelineSlug, params.PipelineSlug)
			require.Equal(t, versionID, params.VersionID)
			require.Nil(t, params.ExpectedCurrentSpecHash)
			require.Equal(t, model.EventActorTypeUser.String(), params.DeployedByActorType)
			require.NotNil(t, params.DeployedByActorSlug)
			require.Equal(t, actorSlug, *params.DeployedByActorSlug)
			return &pipelinesrepo.PipelineDeployment{
				ProjectSlug:             projectSlug,
				EnvironmentSlug:         environmentSlug,
				PipelineSlug:            pipelineSlug,
				CurrentVersionID:        versionID,
				CurrentVersionSpecHash:  specHash,
				PreviousVersionID:       &previousVersionID,
				PreviousVersionSpecHash: &previousSpecHash,
				DeployedByActorType:     model.EventActorTypeUser.String(),
				DeployedByActorSlug:     strPtr(actorSlug.String()),
				DeployedAt:              deployedAt,
				UpdatedAt:               updatedAt,
			}, nil
		},
		projectEnvironmentProtectionFn: func(
			_ context.Context,
			gotOrgSlug, gotProjectSlug, gotEnvironmentSlug slug.Slug,
		) (*model.ProjectEnvironmentProtection, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, projectSlug, gotProjectSlug)
			require.Equal(t, environmentSlug, gotEnvironmentSlug)
			return &model.ProjectEnvironmentProtection{
				EnvironmentSlug: environmentSlug,
				Protected:       true,
			}, nil
		},
	}
	events := &fakePipelineEventRecorder{}
	svc := newCRUDTestServiceWithEvents(t, store, events)

	got, err := svc.Deploy(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
		model.UserEventActor(actorSlug),
		DeployPipelineRequest{
			SpecHash:                         specHash,
			SourceEnvironmentSlug:            &sourceEnvironmentSlug,
			ProtectedEnvironmentConfirmation: &environmentSlug,
		},
	)

	require.NoError(t, err)
	require.True(t, deployed)
	require.Equal(t, projectSlug, got.ProjectSlug)
	require.Equal(t, environmentSlug, got.EnvironmentSlug)
	require.Equal(t, pipelineSlug, got.PipelineSlug)
	require.Equal(t, versionID, got.CurrentVersionID)
	require.Equal(t, specHash, got.CurrentVersionSpecHash)
	require.Equal(t, "6d7b6f5f6c8d", got.CurrentVersionShortSpecHash)
	require.Equal(t, previousVersionID, *got.PreviousVersionID)
	require.Equal(t, previousSpecHash, *got.PreviousVersionSpecHash)
	require.Equal(t, "5c6d7e8f9a0b", *got.PreviousVersionShortSpecHash)
	require.Equal(t, model.EventActorTypeUser.String(), got.DeployedByActorType)
	require.Equal(t, actorSlug.String(), *got.DeployedByActorSlug)
	require.Equal(t, deployedAt, got.DeployedAt)
	require.Equal(t, updatedAt, got.UpdatedAt)

	require.Len(t, events.calls, 1)
	event := events.calls[0]
	require.NotEmpty(t, event.Slug)
	require.Equal(t, orgSlug, event.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, event.ActorType)
	require.NotNil(t, event.ActorSlug)
	require.Equal(t, actorSlug, *event.ActorSlug)
	require.Equal(t, model.EventEntityTypePipelineDeployment, event.EntityType)
	require.NotNil(t, event.EntitySlug)
	require.Equal(t, model.EventActionDeploy, event.Action)
	require.Equal(t, map[string]any{
		"project_slug":                        projectSlug.String(),
		"environment_slug":                    environmentSlug.String(),
		"pipeline_slug":                       pipelineSlug.String(),
		"source_pipeline_version_spec_hash":   specHash,
		"current_pipeline_version_spec_hash":  specHash,
		"previous_pipeline_version_spec_hash": previousSpecHash,
		"source_environment_slug":             sourceEnvironmentSlug.String(),
		"protected_environment_confirmed":     true,
	}, event.Metadata)
}

func TestService_DeploySyncsLogicalRefsAndPreflightsBindings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("backoffice")
	environmentSlug := must.Slug("dev")
	pipelineSlug := must.Slug("sync-users")
	specHash := "6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8"
	versionID := int64(42)
	pipeline := testPipelineSpecWithSlug(pipelineSlug)
	syncResult := &datasourcerefssvc.PipelineDataSourceRefSyncResult{
		Items: []datasourcerefssvc.PipelineDataSourceRefSyncItem{{
			RefSlug:              must.Slug("users-source"),
			DataSourceType:       model.DataSourceTypePostgres,
			RequiredCapabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource},
			Status:               datasourcerefssvc.PipelineDataSourceRefSyncStatusCreated,
		}},
	}

	var deployed bool
	var resolver *fakeDataSourceRefResolver
	store := &fakeServicePipelineStore{
		findVersionBySpecHashFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			gotSpecHash string,
		) (*pipelinesrepo.PipelineVersion, error) {
			require.Equal(t, specHash, gotSpecHash)
			return &pipelinesrepo.PipelineVersion{
				ID:           versionID,
				PipelineSlug: pipelineSlug,
				SpecHash:     specHash,
				SpecJSONB:    mustMarshalPipelineForMergeTest(t, pipeline),
			}, nil
		},
		deployFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			params pipelinesrepo.DeployParams,
		) (*pipelinesrepo.PipelineDeployment, error) {
			deployed = true
			require.NotNil(t, resolver)
			require.Equal(t, 1, resolver.resolveCalls)
			require.Equal(t, versionID, params.VersionID)
			return &pipelinesrepo.PipelineDeployment{
				ProjectSlug:            projectSlug,
				EnvironmentSlug:        environmentSlug,
				PipelineSlug:           pipelineSlug,
				CurrentVersionID:       versionID,
				CurrentVersionSpecHash: specHash,
				DeployedByActorType:    model.EventActorTypeUser.String(),
				DeployedAt:             time.Now(),
				UpdatedAt:              time.Now(),
			}, nil
		},
	}
	resolver = &fakeDataSourceRefResolver{
		syncResult: syncResult,
		resolvedPipelines: map[slug.Slug]*pipelinespec.Pipeline{
			environmentSlug: pipeline,
		},
	}
	svc := newCRUDTestServiceWithDataSourceRefResolver(t, store, resolver)

	got, err := svc.Deploy(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
		model.UserEventActor(must.Slug("alice")),
		DeployPipelineRequest{SpecHash: specHash},
	)

	require.NoError(t, err)
	require.True(t, deployed)
	require.Equal(t, 1, resolver.syncCalls)
	require.Equal(t, 0, resolver.reconcileCalls)
	require.Equal(t, 1, resolver.resolveCalls)
	require.Equal(t, environmentSlug, resolver.lastEnvironmentSlug)
	require.NotNil(t, got.DataSourceRefSync)
	require.Equal(t, syncResult.Items, got.DataSourceRefSync.Items)
}

func TestService_DeployStopsBeforePointerUpdateWhenBindingMissingAfterSync(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("backoffice")
	environmentSlug := must.Slug("dev")
	pipelineSlug := must.Slug("sync-users")
	specHash := "6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8"
	pipeline := testPipelineSpecWithSlug(pipelineSlug)

	var deployed bool
	store := &fakeServicePipelineStore{
		findVersionBySpecHashFn: func(
			context.Context,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			string,
		) (*pipelinesrepo.PipelineVersion, error) {
			return &pipelinesrepo.PipelineVersion{
				ID:           42,
				PipelineSlug: pipelineSlug,
				SpecHash:     specHash,
				SpecJSONB:    mustMarshalPipelineForMergeTest(t, pipeline),
			}, nil
		},
		deployFn: func(
			context.Context,
			slug.Slug,
			slug.Slug,
			slug.Slug,
			pipelinesrepo.DeployParams,
		) (*pipelinesrepo.PipelineDeployment, error) {
			deployed = true
			return nil, xerror.Unexpected().Detail("deploy should not run after binding preflight fails")
		},
	}
	resolver := &fakeDataSourceRefResolver{
		syncResult: &datasourcerefssvc.PipelineDataSourceRefSyncResult{
			Items: []datasourcerefssvc.PipelineDataSourceRefSyncItem{{
				RefSlug: must.Slug("users-source"),
				Status:  datasourcerefssvc.PipelineDataSourceRefSyncStatusCreated,
			}},
		},
		err: datasourcerefssvc.ErrDataSourceBindingNotFound.Detail(
			`validate steps: step[0] "fetch-users": data source binding users-source is not configured for environment dev`,
		),
	}
	svc := newCRUDTestServiceWithDataSourceRefResolver(t, store, resolver)

	got, err := svc.Deploy(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
		model.UserEventActor(must.Slug("alice")),
		DeployPipelineRequest{SpecHash: specHash},
	)

	require.ErrorIs(t, err, datasourcerefssvc.ErrDataSourceBindingNotFound)
	var typedErr *xerror.Error
	require.ErrorAs(t, err, &typedErr)
	require.Equal(t, "DATA_SOURCE_BINDING_NOT_FOUND", typedErr.Code())
	require.Nil(t, got)
	require.Equal(t, 1, resolver.syncCalls)
	require.Equal(t, 0, resolver.reconcileCalls)
	require.Equal(t, 1, resolver.resolveCalls)
	require.False(t, deployed)
}

func TestService_DeployResolvesShortExpectedCurrentSpecHash(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("backoffice")
	environmentSlug := must.Slug("dev")
	pipelineSlug := must.Slug("sync-users")
	nextSpecHash := "6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8"
	expectedFullSpecHash := "5c6d7e8f9a0b1c2d3e4f50617283940516273849506172839405061728394"
	expectedShortSpecHash := expectedFullSpecHash[:shortSpecHashLength]
	pipeline := testPipelineSpecWithSlug(pipelineSlug)

	var gotExpected *string
	store := &fakeServicePipelineStore{
		findVersionBySpecHashFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			gotSpecHash string,
		) (*pipelinesrepo.PipelineVersion, error) {
			if gotSpecHash == nextSpecHash {
				return &pipelinesrepo.PipelineVersion{
					ID:           42,
					PipelineSlug: pipelineSlug,
					SpecHash:     nextSpecHash,
					SpecJSONB:    mustMarshalPipelineForMergeTest(t, pipeline),
				}, nil
			}
			return nil, pipelinesrepo.ErrPipelineVersionNotFound
		},
		findVersionBySpecHashPrefixFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			specHashPrefix string,
		) (*pipelinesrepo.PipelineVersion, error) {
			require.Equal(t, expectedShortSpecHash, specHashPrefix)
			return &pipelinesrepo.PipelineVersion{
				ID:           41,
				PipelineSlug: pipelineSlug,
				SpecHash:     expectedFullSpecHash,
			}, nil
		},
		deployFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			params pipelinesrepo.DeployParams,
		) (*pipelinesrepo.PipelineDeployment, error) {
			gotExpected = params.ExpectedCurrentSpecHash
			return &pipelinesrepo.PipelineDeployment{
				ProjectSlug:            projectSlug,
				EnvironmentSlug:        environmentSlug,
				PipelineSlug:           pipelineSlug,
				CurrentVersionID:       42,
				CurrentVersionSpecHash: nextSpecHash,
				DeployedByActorType:    model.EventActorTypeUser.String(),
				DeployedAt:             time.Now(),
				UpdatedAt:              time.Now(),
			}, nil
		},
	}
	svc := newCRUDTestService(t, store)

	_, err := svc.Deploy(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
		model.UserEventActor(must.Slug("alice")),
		DeployPipelineRequest{
			SpecHash:                nextSpecHash,
			ExpectedCurrentSpecHash: &expectedShortSpecHash,
		},
	)

	require.NoError(t, err)
	require.NotNil(t, gotExpected)
	require.Equal(t, expectedFullSpecHash, *gotExpected)
}

func TestService_DeployReturnsConflictForStaleExpectedCurrentSpecHash(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("backoffice")
	environmentSlug := must.Slug("dev")
	pipelineSlug := must.Slug("sync-users")
	specHash := "6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8"
	expectedSpecHash := "stale"
	pipeline := testPipelineSpecWithSlug(pipelineSlug)

	var attemptedDeploy bool
	store := &fakeServicePipelineStore{
		findVersionBySpecHashFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			gotSpecHash string,
		) (*pipelinesrepo.PipelineVersion, error) {
			if gotSpecHash == specHash {
				return &pipelinesrepo.PipelineVersion{
					ID:           42,
					PipelineSlug: pipelineSlug,
					SpecHash:     specHash,
					SpecJSONB:    mustMarshalPipelineForMergeTest(t, pipeline),
				}, nil
			}
			return nil, pipelinesrepo.ErrPipelineVersionNotFound
		},
		findVersionBySpecHashPrefixFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			_ string,
		) (*pipelinesrepo.PipelineVersion, error) {
			return nil, pipelinesrepo.ErrPipelineVersionNotFound
		},
		deployFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			_ pipelinesrepo.DeployParams,
		) (*pipelinesrepo.PipelineDeployment, error) {
			attemptedDeploy = true
			return nil, pipelinesrepo.ErrPipelineDeploymentNotFound
		},
	}
	svc := newCRUDTestService(t, store)

	_, err := svc.Deploy(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
		model.UserEventActor(must.Slug("alice")),
		DeployPipelineRequest{
			SpecHash:                specHash,
			ExpectedCurrentSpecHash: &expectedSpecHash,
		},
	)

	require.ErrorIs(t, err, ErrPipelineDeploymentConflict)
	require.True(t, attemptedDeploy)
	_, err = svc.Deploy(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, model.UserEventActor(must.Slug("alice")), DeployPipelineRequest{SpecHash: specHash})
	require.ErrorIs(t, err, ErrPipelineDeploymentConflict)
}

func TestService_ListAndGetDeploymentsReturnDeploymentDetails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("backoffice")
	environmentSlug := must.Slug("prod")
	pipelineSlug := must.Slug("sync-users")
	specHash := "6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6"
	deployment := &pipelinesrepo.PipelineDeployment{
		ProjectSlug:            projectSlug,
		EnvironmentSlug:        environmentSlug,
		PipelineSlug:           pipelineSlug,
		CurrentVersionID:       42,
		CurrentVersionSpecHash: specHash,
		DeployedByActorType:    model.EventActorTypeUser.String(),
		DeployedByActorSlug:    strPtr("alice"),
		DeployedAt:             time.Date(2026, time.May, 26, 11, 0, 0, 0, time.UTC),
		UpdatedAt:              time.Date(2026, time.May, 26, 11, 5, 0, 0, time.UTC),
	}
	store := &fakeServicePipelineStore{
		listDeploymentsFn: func(
			_ context.Context,
			gotOrgSlug, gotProjectSlug, gotEnvironmentSlug slug.Slug,
		) ([]*pipelinesrepo.PipelineDeployment, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, projectSlug, gotProjectSlug)
			require.Equal(t, environmentSlug, gotEnvironmentSlug)
			return []*pipelinesrepo.PipelineDeployment{deployment}, nil
		},
		findDeploymentFn: func(
			_ context.Context,
			gotOrgSlug, gotProjectSlug, gotEnvironmentSlug, gotPipelineSlug slug.Slug,
		) (*pipelinesrepo.PipelineDeployment, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, projectSlug, gotProjectSlug)
			require.Equal(t, environmentSlug, gotEnvironmentSlug)
			require.Equal(t, pipelineSlug, gotPipelineSlug)
			return deployment, nil
		},
	}
	svc := newCRUDTestService(t, store)

	list, err := svc.ListDeployments(ctx, orgSlug, projectSlug, environmentSlug)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, deployment.ProjectSlug, list[0].ProjectSlug)
	require.Equal(t, deployment.EnvironmentSlug, list[0].EnvironmentSlug)
	require.Equal(t, deployment.PipelineSlug, list[0].PipelineSlug)
	require.Equal(t, "6d7b6f5f6c8d", list[0].CurrentVersionShortSpecHash)

	got, err := svc.GetDeployment(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug)
	require.NoError(t, err)
	require.Equal(t, deployment.ProjectSlug, got.ProjectSlug)
	require.Equal(t, deployment.EnvironmentSlug, got.EnvironmentSlug)
	require.Equal(t, deployment.PipelineSlug, got.PipelineSlug)
	require.Equal(t, specHash, got.CurrentVersionSpecHash)
}

func TestService_DeployRejectsDisconnectedStoredVersionBeforeMutation(t *testing.T) {
	t.Parallel()
	pipeline := testPipelineSpec()
	extra := pipeline.Steps[0]
	extra.Slug = "independent-source"
	pipeline.Steps = append(pipeline.Steps, extra)
	store := &fakeServicePipelineStore{
		projectEnvironmentProtectionFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*model.ProjectEnvironmentProtection, error) {
			return &model.ProjectEnvironmentProtection{}, nil
		},
		findVersionBySpecHashFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, string) (*pipelinesrepo.PipelineVersion, error) {
			return &pipelinesrepo.PipelineVersion{PipelineSlug: pipeline.Metadata.Slug, SpecHash: "stored-hash", SpecJSONB: mustMarshalPipelineForMergeTest(t, pipeline)}, nil
		},
		deployFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, pipelinesrepo.DeployParams) (*pipelinesrepo.PipelineDeployment, error) {
			t.Fatal("disconnected version must not update deployment")
			return nil, xerror.Unexpected()
		},
	}
	events := &fakePipelineEventRecorder{}
	svc := newCRUDTestServiceWithEvents(t, store, events)
	got, err := svc.Deploy(context.Background(), "acme", defaultProjectSlug, "dev", pipeline.Metadata.Slug,
		model.UserEventActor("alice"), DeployPipelineRequest{SpecHash: "stored-hash"})
	require.Nil(t, got)
	require.ErrorContains(t, err, "disconnected data-flow groups")
}
