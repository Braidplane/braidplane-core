package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	pipelinedrift "github.com/Braidplane/braidplane-core/pkg/pipeline/drift"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	testfakes "github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
)

func TestServer_ListPipelines(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsPipelineReferences", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		nextCursor := "next-cursor"
		prevCursor := "prev-cursor"
		sourcePath := "pipelines/acme-users-sync.pipeline.yaml"
		sourceRef := "refs/heads/main"
		lastAppliedCommit := "abc123def456"
		currentVersionID := int64(12)
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				ListFn: func(
					_ context.Context,
					orgSlug, projectSlug, memberSlug slug.Slug,
					req model.ListRequest,
				) (model.ListResponse[*pipelinesvc.PipelineSummary], error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)
					require.Equal(
						t,
						model.ListRequest{Limit: 2, Cursor: "page-1", Direction: "prev"},
						req,
					)
					return model.ListResponse[*pipelinesvc.PipelineSummary]{
						Items: []*pipelinesvc.PipelineSummary{{
							Slug:                   slug.Slug("acme-users-sync"),
							Name:                   "ACME Users Sync",
							SyncStatus:             pipelinesrepo.SyncStatusDrifted,
							SourcePath:             &sourcePath,
							SourceRef:              &sourceRef,
							LastAppliedCommit:      &lastAppliedCommit,
							CurrentVersionID:       &currentVersionID,
							CurrentVersionSpecHash: strPtr("spec-hash-12"),
							CreatedAt:              now,
							UpdatedAt:              now,
						}},
						HasMore:    true,
						NextCursor: &nextCursor,
						PrevCursor: &prevCursor,
					}, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodGet,
			"/organizations/acme/projects/default/pipelines?limit=2&cursor=page-1&direction=prev",
			nil,
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineListResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Len(t, body.Pipelines, 1)
		require.Equal(t, "acme-users-sync", body.Pipelines[0].Slug)
		require.Equal(t, api.Drifted, body.Pipelines[0].SyncStatus)
		require.Equal(t, sourcePath, body.Pipelines[0].SourcePath)
		require.Equal(t, sourceRef, body.Pipelines[0].SourceRef)
		require.Equal(t, lastAppliedCommit, body.Pipelines[0].LastAppliedCommit)
		require.NotNil(t, body.Pipelines[0].CurrentVersionSpecHash)
		require.Equal(t, "spec-hash-12", *body.Pipelines[0].CurrentVersionSpecHash)
		require.True(t, body.HasMore)
		require.Equal(t, &nextCursor, body.NextCursor)
		require.Equal(t, &prevCursor, body.PrevCursor)
	})
}

func TestToAPIPipelineRunEventInputSummary(t *testing.T) {
	t.Parallel()

	digest := strings.Repeat("a", 64)
	summary := &pipelinerun.EventInputSummary{
		Keys:            []string{"event"},
		Representation:  model.ContractRepresentationJSON,
		SizeBytes:       42,
		DigestAlgorithm: "sha256",
		Digest:          digest,
	}
	got := toAPIPipelineRunEventInputSummary(summary)
	summary.Keys[0] = "changed"

	require.Equal(t, []string{"event"}, got.Keys)
	require.Equal(t, api.PipelineRunEventInputSummaryRepresentationJson, got.Representation)
	require.Equal(t, int64(42), got.SizeBytes)
	require.Equal(t, api.PipelineRunEventInputSummaryDigestAlgorithmSha256, got.DigestAlgorithm)
	require.Equal(t, digest, got.Digest)
	require.Nil(t, toAPIPipelineRunEventInputSummary(nil))
}

func TestToAPIPipelineRunTimelineOmitsDataSourceForEventInput(t *testing.T) {
	t.Parallel()

	got := toAPIPipelineRunTimeline(pipelinerun.RunTimeline{
		Snapshots: []pipelinerun.StepSnapshot{{
			Step: "event",
			Kind: "input",
			Input: pipelinerun.StepInputSnapshot{
				OrganizationSlug: "acme",
				EnvironmentSlug:  "prod",
				ParameterKeys:    []string{},
				EventInput: &pipelinerun.EventInputSummary{
					Keys: []string{"event"}, Representation: model.ContractRepresentationJSON,
					SizeBytes: 42, DigestAlgorithm: "sha256", Digest: "abc123",
				},
			},
		}},
	})

	require.Len(t, got.Snapshots, 1)
	require.Nil(t, got.Snapshots[0].Input.DataSourceSlug)
	require.Equal(t, []string{"event"}, got.Snapshots[0].Input.EventInput.Keys)
	require.Equal(t, int64(42), got.Snapshots[0].Input.EventInput.SizeBytes)
	require.Equal(t, "abc123", got.Snapshots[0].Input.EventInput.Digest)
}

func TestServer_ListPipelinesExposesOnlyGitProvenance(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	sourcePath := "pipelines/acme-users-sync.pipeline.yaml"
	sourceRef := "refs/heads/main"
	lastAppliedCommit := "abc123def456"

	app, store := newTestApp(t, testServerConfig{
		pipelinesService: &fakePipelinesService{
			ListFn: func(
				_ context.Context,
				orgSlug, projectSlug, memberSlug slug.Slug,
				req model.ListRequest,
			) (model.ListResponse[*pipelinesvc.PipelineSummary], error) {
				require.Equal(t, slug.Slug("acme"), orgSlug)
				require.Equal(t, slug.Slug("default"), projectSlug)
				require.Equal(t, slug.Slug("test-user"), memberSlug)
				require.Equal(t, model.ListRequest{}, req)
				return model.ListResponse[*pipelinesvc.PipelineSummary]{
					Items: []*pipelinesvc.PipelineSummary{{
						Slug:              slug.Slug("acme-users-sync"),
						Name:              "ACME Users Sync",
						SyncStatus:        pipelinesrepo.SyncStatusSynced,
						SourcePath:        &sourcePath,
						SourceRef:         &sourceRef,
						LastAppliedCommit: &lastAppliedCommit,
						CreatedAt:         now,
						UpdatedAt:         now,
					}},
				}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/organizations/acme/projects/default/pipelines", nil)
	resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
	defer func() { _ = resp.Body.Close() }()

	body := parseResponse[map[string]any](t, resp)
	pipelines, ok := body["pipelines"].([]any)
	require.True(t, ok)
	require.Len(t, pipelines, 1)
	first, ok := pipelines[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, sourcePath, first["source_path"])
	assert.Equal(t, sourceRef, first["source_ref"])
	assert.Equal(t, lastAppliedCommit, first["last_applied_commit"])
	_, hasManagedBy := first["managed_by"]
	assert.False(t, hasManagedBy, "pipeline list response must not expose an ownership mode")
	_, hasSourceDataSourceSlug := first["source_data_source_slug"]
	assert.False(t, hasSourceDataSourceSlug, "pipeline list response must stop exposing source_data_source_slug")
	_, hasSourceRepo := first["source_repo"]
	assert.False(t, hasSourceRepo, "pipeline list response must stop exposing source_repo")
}

func TestServer_ReconcilePipelines(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsReconcileReport", func(t *testing.T) {
		t.Parallel()

		dryRun := true
		pipelineDir := "deploy/pipelines"
		requestedSlug := api.Slug("acme-users-sync")
		sourceRepo := "github.com/Braidplane/acme-pipelines"
		sourceRef := "refs/heads/main"
		sourceCommit := "abc123def456"
		sourcePath := "deploy/pipelines/acme-users-sync.pipeline.yaml"
		legacyPath := "deploy/pipelines/legacy.pipeline.yaml"
		skippedPath := "deploy/pipelines/other.pipeline.yaml"
		failurePath := "deploy/pipelines/broken.pipeline.yaml"
		resultDataSourceSlug := slug.Slug("pipelines-git")
		failureSlug := slug.Slug("broken-sync")

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				ReconcilePipelinesFn: func(
					_ context.Context,
					orgSlug, projectSlug, memberSlug slug.Slug,
					actor model.EventActor,
					req pipelinesvc.ReconcilePipelinesRequest,
				) (*pipelinesvc.ReconcilePipelinesResult, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)
					require.True(t, req.DryRun)
					require.Equal(t, "deploy/pipelines", req.PipelineDir)
					require.Equal(t, []slug.Slug{slug.Slug("acme-users-sync")}, req.PipelineSlugs)

					return &pipelinesvc.ReconcilePipelinesResult{
						DryRun:                 true,
						PipelineDir:            "deploy/pipelines",
						RequestedPipelineSlugs: []slug.Slug{slug.Slug("acme-users-sync")},
						Source: pipelinesvc.ReconcilePipelinesSourceMetadata{
							DataSourceSlug: &resultDataSourceSlug,
							Repo:           &sourceRepo,
							Ref:            &sourceRef,
							Commit:         &sourceCommit,
						},
						Summary: pipelinesvc.ReconcilePipelinesSummary{
							Scanned:    3,
							Synced:     1,
							Deleted:    1,
							Skipped:    1,
							Failed:     1,
							DriftReset: 1,
						},
						Synced: []pipelinesvc.ReconcilePipelinesSyncedPipeline{{
							PipelineSlug: slug.Slug("acme-users-sync"),
							SourcePath:   sourcePath,
							SpecHash:     "applied-hash",
						}},
						Deleted: []pipelinesvc.ReconcilePipelinesDeletedPipeline{{
							PipelineSlug:       slug.Slug("legacy-sync"),
							PreviousSourcePath: &legacyPath,
						}},
						Skipped: []pipelinesvc.ReconcilePipelinesSkippedPipeline{{
							PipelineSlug: slug.Slug("other-sync"),
							SourcePath:   &skippedPath,
							Reason:       "filtered_out",
						}},
						DriftResets: []pipelinesvc.ReconcilePipelinesDriftReset{{
							PipelineSlug:     slug.Slug("acme-users-sync"),
							SourcePath:       sourcePath,
							PreviousSpecHash: "drifted-hash",
							AppliedSpecHash:  "applied-hash",
						}},
						Failures: []pipelinesvc.ReconcilePipelinesFailure{{
							PipelinePath: &failurePath,
							PipelineSlug: &failureSlug,
							Stage:        "load",
							Message:      "pipeline metadata.slug is required",
						}},
					}, nil
				},
			},
		})

		req := jsonRequest(t, http.MethodPost, "/organizations/acme/projects/default/pipelines:reconcile", api.PipelineReconcileRequest{
			DryRun:        &dryRun,
			PipelineDir:   &pipelineDir,
			PipelineSlugs: &[]api.Slug{requestedSlug},
		})
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineReconcileResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.True(t, body.DryRun)
		require.Equal(t, "deploy/pipelines", body.PipelineDir)
		require.Equal(t, []api.Slug{"acme-users-sync"}, body.RequestedPipelineSlugs)
		require.NotNil(t, body.Source.DataSourceSlug)
		require.Equal(t, api.NullableSlug("pipelines-git"), *body.Source.DataSourceSlug)
		require.Equal(t, &sourceRepo, body.Source.Repo)
		require.Equal(t, &sourceRef, body.Source.Ref)
		require.Equal(t, &sourceCommit, body.Source.Commit)
		require.Equal(t, 3, body.Summary.Scanned)
		require.Equal(t, 1, body.Summary.Synced)
		require.Equal(t, 1, body.Summary.Deleted)
		require.Equal(t, 1, body.Summary.Skipped)
		require.Equal(t, 1, body.Summary.Failed)
		require.Equal(t, 1, body.Summary.DriftReset)
		require.Len(t, body.Synced, 1)
		require.Equal(t, api.Slug("acme-users-sync"), body.Synced[0].PipelineSlug)
		require.Equal(t, sourcePath, body.Synced[0].SourcePath)
		require.Equal(t, "applied-hash", body.Synced[0].SpecHash)
		require.Len(t, body.Deleted, 1)
		require.Equal(t, api.Slug("legacy-sync"), body.Deleted[0].PipelineSlug)
		require.Equal(t, &legacyPath, body.Deleted[0].PreviousSourcePath)
		require.Len(t, body.Skipped, 1)
		require.Equal(t, api.Slug("other-sync"), body.Skipped[0].PipelineSlug)
		require.Equal(t, &skippedPath, body.Skipped[0].SourcePath)
		require.Equal(t, api.PipelineReconcileSkippedReasonFilteredOut, body.Skipped[0].Reason)
		require.Len(t, body.DriftResets, 1)
		require.Equal(t, api.Slug("acme-users-sync"), body.DriftResets[0].PipelineSlug)
		require.Equal(t, "drifted-hash", body.DriftResets[0].PreviousSpecHash)
		require.Equal(t, "applied-hash", body.DriftResets[0].AppliedSpecHash)
		require.Len(t, body.Failures, 1)
		require.Equal(t, &failurePath, body.Failures[0].PipelinePath)
		require.NotNil(t, body.Failures[0].PipelineSlug)
		require.Equal(t, api.NullableSlug("broken-sync"), *body.Failures[0].PipelineSlug)
		require.Equal(t, api.PipelineReconcileFailureStageLoad, body.Failures[0].Stage)
		require.Equal(t, "pipeline metadata.slug is required", body.Failures[0].Message)
	})

	t.Run("RejectsInvalidPipelineSlugFilterBeforeServiceCall", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				ReconcilePipelinesFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					model.EventActor,
					pipelinesvc.ReconcilePipelinesRequest,
				) (*pipelinesvc.ReconcilePipelinesResult, error) {
					t.Fatal("service must not be called with an invalid pipeline slug filter")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		req := jsonRequest(t, http.MethodPost, "/organizations/acme/projects/default/pipelines:reconcile", map[string]any{
			"dry_run":        true,
			"pipeline_slugs": []string{"INVALID SLUG"},
		})
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("ReturnsForbiddenWhenUnauthorized", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			authService: fakeAuthService{
				CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, slug.Slug("test-user"), input.UserSlug)
					require.NotNil(t, input.OrgSlug)
					require.Equal(t, slug.Slug("acme"), *input.OrgSlug)
					require.Equal(t, model.Update, input.Action)
					require.Equal(t, model.ResourcePipelines, input.Resource)
					return false, nil
				},
			},
			pipelinesService: &fakePipelinesService{
				ReconcilePipelinesFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					model.EventActor,
					pipelinesvc.ReconcilePipelinesRequest,
				) (*pipelinesvc.ReconcilePipelinesResult, error) {
					t.Fatal("service must not be called when authorization denies reconcile")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		req := jsonRequest(t, http.MethodPost, "/organizations/acme/projects/default/pipelines:reconcile", map[string]any{
			"dry_run": true,
		})
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

func TestServer_ReconcilePipelinesRejectsLegacyBranchOverride(t *testing.T) {
	t.Parallel()

	called := false
	app, store := newTestApp(t, testServerConfig{
		pipelinesService: &fakePipelinesService{
			ReconcilePipelinesFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
				slug.Slug,
				model.EventActor,
				pipelinesvc.ReconcilePipelinesRequest,
			) (*pipelinesvc.ReconcilePipelinesResult, error) {
				called = true
				return &pipelinesvc.ReconcilePipelinesResult{}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/projects/default/pipelines:reconcile", map[string]any{
		"branch": "main",
	})
	resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.False(t, called, "reconcile service must not be called when legacy branch override is provided")
}

func TestServer_ValidatePipeline(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsValidResult", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			projectsService: existingPipelineValidationProjectService(t),
			pipelinesService: &fakePipelinesService{
				ValidateFn: func(
					_ context.Context,
					orgSlug, projectSlug, environmentSlug, memberSlug slug.Slug,
					pipeline *pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineValidationResult, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)
					require.Equal(t, slug.Slug("prod"), environmentSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipeline.Metadata.Slug)

					return &pipelinesvc.PipelineValidationResult{
						Valid:       true,
						Diagnostics: []pipelinesvc.PipelineValidationDiagnostic{},
					}, nil
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipeline-validation",
			testPipelineRequestBody(),
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineValidationResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.True(t, body.Valid)
		require.Empty(t, body.Diagnostics)
	})

	t.Run("ReturnsImpactReport", func(t *testing.T) {
		t.Parallel()

		createdAt := time.Date(2026, 6, 5, 9, 30, 0, 0, time.UTC)
		app, store := newTestApp(t, testServerConfig{
			projectsService: existingPipelineValidationProjectService(t),
			pipelinesService: &fakePipelinesService{
				ValidateFn: func(
					_ context.Context,
					_, _, _, _ slug.Slug,
					_ *pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineValidationResult, error) {
					return &pipelinesvc.PipelineValidationResult{
						Valid: true,
						Diagnostics: []pipelinesvc.PipelineValidationDiagnostic{{
							Severity: "warning",
							Code:     "PIPELINE_CONTRACT_IMPACT_WARNING",
							Field:    "step[fetch-users].query.output_contract.email",
							StepSlug: strPtr("fetch-users"),
							Message:  "1 contract impact finding(s), highest severity warning",
						}},
						ImpactReport: &pipelinesvc.DriftImpactReport{
							Slug:         slug.Slug("contract-impact-users-a1b2c3"),
							ReportType:   pipelinedrift.ReportTypeContractImpact,
							Status:       pipelinedrift.ReportStatusOpen,
							Severity:     pipelinedrift.ReportSeverityWarning,
							Summary:      "1 contract impact finding(s), highest severity warning",
							PipelineSlug: slugPtr(slug.Slug("acme-users-sync")),
							ProjectSlug:  slugPtr(slug.Slug("default")),
							CreatedAt:    createdAt,
							Changes: []pipelinedrift.Change{{
								Kind:       "addition",
								Path:       "step[fetch-users].query.output_contract.email",
								Before:     "<none>",
								After:      "string(required=false,nullable=false)",
								Severity:   pipelinedrift.ReportSeverityWarning,
								ReasonCode: "addition",
							}},
							Impacts: []pipelinedrift.Impact{{
								Kind:         "unknown_consumer",
								PipelineSlug: "acme-users-sync",
								StepSlug:     "fetch-users",
								Severity:     pipelinedrift.ReportSeverityWarning,
								Message:      "external app or consumer impact is unknown in the MVP",
							}},
						},
					}, nil
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipeline-validation",
			testPipelineRequestBody(),
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineValidationResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.True(t, body.Valid)
		require.Len(t, body.Diagnostics, 1)
		require.Equal(t, api.PipelineValidationDiagnosticSeverityWarning, body.Diagnostics[0].Severity)
		require.NotNil(t, body.ImpactReport)
		require.Equal(t, api.DriftImpactReportSeverity("warning"), body.ImpactReport.Severity)
		require.Equal(t, "1 contract impact finding(s), highest severity warning", body.ImpactReport.Summary)
		require.Len(t, body.ImpactReport.Changes, 1)
		require.Equal(t, "addition", body.ImpactReport.Changes[0].Kind)
		require.Len(t, body.ImpactReport.Impacts, 1)
		require.Equal(t, api.DriftImpactImpactKind("unknown_consumer"), body.ImpactReport.Impacts[0].Kind)
	})

	t.Run("ReturnsBadRequestForMalformedJSON", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			projectsService: existingPipelineValidationProjectService(t),
			pipelinesService: &fakePipelinesService{
				ValidateFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					*pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineValidationResult, error) {
					t.Fatal("service must not be called for malformed validation JSON")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipeline-validation",
			strings.NewReader("{"))

		require.NoError(t, err)
		req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("ReturnsInvalidServiceResult", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			projectsService: existingPipelineValidationProjectService(t),
			pipelinesService: &fakePipelinesService{
				ValidateFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					*pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineValidationResult, error) {
					return &pipelinesvc.PipelineValidationResult{Valid: false}, nil
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipeline-validation",
			testPipelineRequestBody(),
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineValidationResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.False(t, body.Valid)
		require.Empty(t, body.Diagnostics)
	})

	t.Run("ReturnsValidationDiagnosticsForInvalidContractFieldValidation", func(t *testing.T) {
		t.Parallel()

		recorded := false
		app, store := newTestApp(t, testServerConfig{
			projectsService: existingPipelineValidationProjectService(t),
			pipelinesService: &fakePipelinesService{
				ValidateFn: func(
					_ context.Context,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
					pipeline *pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineValidationResult, error) {
					require.NotNil(t, pipeline.Steps[0].Query.OutputContract)
					require.Len(t, pipeline.Steps[0].Query.OutputContract.Fields, 1)
					require.Equal(t, "email", pipeline.Steps[0].Query.OutputContract.Fields[0].Name)
					require.NotNil(t, pipeline.Steps[0].Query.OutputContract.Fields[0].Validations)
					require.Equal(
						t,
						json.Number("10"),
						pipeline.Steps[0].Query.OutputContract.Fields[0].Validations.Min,
					)

					return &pipelinesvc.PipelineValidationResult{
						Valid: false,
						Diagnostics: []pipelinesvc.PipelineValidationDiagnostic{{
							Severity: "error",
							Code:     "PIPELINE_INVALID_INPUT",
							Field:    "steps[0].query",
							StepSlug: strPtr("fetch-users"),
							Message:  `field "email" validation "min" is invalid: min/max require number, integer, or datetime field type`,
						}},
					}, nil
				},
				RecordValidationResultFn: func(
					_ context.Context,
					orgSlug, projectSlug, environmentSlug slug.Slug,
					principal pipelinesvc.ValidationHistoryPrincipal,
					source pipelinesvc.PipelineValidationResultSource,
					pipeline *pipelinespec.Pipeline,
					result *pipelinesvc.PipelineValidationResult,
				) (*pipelinesvc.PipelineValidationResultRecord, error) {
					recorded = true
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("prod"), environmentSlug)
					require.Equal(t, slug.Slug("test-user"), principal.UserSlug)
					require.Equal(t, pipelinesvc.PipelineValidationResultSourceAPI, source)
					require.Equal(t, slug.Slug("acme-users-sync"), pipeline.Metadata.Slug)
					require.False(t, result.Valid)
					require.Len(t, result.Diagnostics, 1)
					return testPipelineValidationRecord(), nil
				},
			},
		})

		requestBody := testPipelineRequestBody()
		spec := requestBody["spec"].(map[string]any)
		steps := spec["steps"].([]map[string]any)
		query := steps[0]["query"].(map[string]any)
		query["output_contract"] = map[string]any{
			"name":           "users_rows",
			"representation": "rows",
			"fields": []map[string]any{{
				"name":     "email",
				"type":     "string",
				"required": true,
				"validations": map[string]any{
					"min": 10,
				},
			}},
		}

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipeline-validation",
			requestBody,
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineValidationResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.False(t, body.Valid)
		require.Len(t, body.Diagnostics, 1)
		require.Equal(t, api.PipelineValidationDiagnosticSeverityError, body.Diagnostics[0].Severity)
		require.Equal(t, "PIPELINE_INVALID_INPUT", body.Diagnostics[0].Code)
		require.Equal(t, "steps[0].query", body.Diagnostics[0].Field)
		require.NotNil(t, body.Diagnostics[0].StepSlug)
		require.Equal(t, api.NullableSlug("fetch-users"), *body.Diagnostics[0].StepSlug)
		require.Contains(t, body.Diagnostics[0].Message, `validation "min"`)
		require.True(t, recorded)
	})

	t.Run("SkipsDurableHistoryForReadOnlyValidation", func(t *testing.T) {
		t.Parallel()

		updateChecked := false
		app, store := newTestApp(t, testServerConfig{
			authService: fakeAuthService{
				CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					if input.Resource == model.ResourcePipelines && input.Action == model.Update {
						updateChecked = true
						return false, nil
					}
					return true, nil
				},
			},
			projectsService: existingPipelineValidationProjectService(t),
			pipelinesService: &fakePipelinesService{
				ValidateFn: func(
					_ context.Context,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
					_ *pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineValidationResult, error) {
					return &pipelinesvc.PipelineValidationResult{
						Valid: false,
						Diagnostics: []pipelinesvc.PipelineValidationDiagnostic{{
							Severity: "error",
							Code:     "PIPELINE_INVALID_INPUT",
							Field:    "steps[0].query",
							Message:  "invalid contract field",
						}},
					}, nil
				},
				RecordValidationResultFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					pipelinesvc.ValidationHistoryPrincipal,
					pipelinesvc.PipelineValidationResultSource,
					*pipelinespec.Pipeline,
					*pipelinesvc.PipelineValidationResult,
				) (*pipelinesvc.PipelineValidationResultRecord, error) {
					t.Fatal("read-only validation must not record durable history")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipeline-validation",
			testPipelineRequestBody(),
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineValidationResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.False(t, body.Valid)
		require.Len(t, body.Diagnostics, 1)
		require.True(t, updateChecked)
	})

	t.Run("ReturnsDiagnosticsWhenHistoryAuthorizationFails", func(t *testing.T) {
		t.Parallel()

		updateChecked := false
		app, store := newTestApp(t, testServerConfig{
			authService: fakeAuthService{
				CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					if input.Resource == model.ResourcePipelines && input.Action == model.Update {
						updateChecked = true
						return false, xerror.ExecutionUnavailable().Detail("authorization unavailable")
					}
					return true, nil
				},
			},
			projectsService: existingPipelineValidationProjectService(t),
			pipelinesService: &fakePipelinesService{
				ValidateFn: func(
					_ context.Context,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
					_ *pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineValidationResult, error) {
					return &pipelinesvc.PipelineValidationResult{
						Valid: false,
						Diagnostics: []pipelinesvc.PipelineValidationDiagnostic{{
							Severity: "error",
							Code:     "PIPELINE_INVALID_INPUT",
							Field:    "steps[0].query",
							Message:  "invalid contract field",
						}},
					}, nil
				},
				RecordValidationResultFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					pipelinesvc.ValidationHistoryPrincipal,
					pipelinesvc.PipelineValidationResultSource,
					*pipelinespec.Pipeline,
					*pipelinesvc.PipelineValidationResult,
				) (*pipelinesvc.PipelineValidationResultRecord, error) {
					t.Fatal("history authorization failure must skip durable history recording")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipeline-validation",
			testPipelineRequestBody(),
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineValidationResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.False(t, body.Valid)
		require.Len(t, body.Diagnostics, 1)
		require.True(t, updateChecked)
	})

	t.Run("MapsServiceError", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			projectsService: existingPipelineValidationProjectService(t),
			pipelinesService: &fakePipelinesService{
				ValidateFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					*pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineValidationResult, error) {
					return nil, pipelinesvc.ErrPipelineNotFound
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipeline-validation",
			testPipelineRequestBody(),
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("ReturnsNotFoundForUnknownProject", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			projectsService: &fakeProjectsService{
				GetProjectFn: func(_ context.Context, orgSlug, projectSlug slug.Slug) (*model.Project, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("missing-project"), projectSlug)
					return nil, projectssvc.ErrProjectNotFound
				},
			},
			pipelinesService: &fakePipelinesService{
				ValidateFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					*pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineValidationResult, error) {
					t.Fatal("pipeline validation service must not be called for an unknown project")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/missing-project/environments/prod/pipeline-validation",
			testPipelineRequestBody(),
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestServer_ValidateODCSReferenceRequiresCatalogRead(t *testing.T) {
	t.Parallel()
	for _, port := range []string{"query", "event", "named"} {
		for _, permitted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/CatalogRead%t", port, permitted), func(t *testing.T) {
				t.Parallel()
				validated := false
				app, store := newTestApp(t, testServerConfig{
					projectsService: existingPipelineValidationProjectService(t),
					authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						if input.Resource == model.ResourceProjects {
							require.Equal(t, model.Read, input.Action)
							return permitted, nil
						}
						return true, nil
					}},
					pipelinesService: &fakePipelinesService{ValidateFn: func(_ context.Context, _, _, _, _ slug.Slug, pipeline *pipelinespec.Pipeline) (*pipelinesvc.PipelineValidationResult, error) {
						validated = true
						var ref *pipelinespec.ODCSContractRef
						switch port {
						case "query":
							ref = pipeline.Steps[0].Query.OutputContractRef
						case "event":
							ref = pipeline.Steps[0].Input.OutputContractRef
						case "named":
							ref = pipeline.Steps[0].Transform.InputContracts["orders"].ContractRef
						}
						require.NotNil(t, ref)
						require.Equal(t, slug.Slug("orders"), ref.ContractSlug)
						require.Equal(t, slug.Slug("v1"), ref.RevisionSlug)
						require.Equal(t, "order", ref.SchemaObjectID)
						return &pipelinesvc.PipelineValidationResult{Valid: true}, nil
					}},
				})
				body := testPipelineRequestBody()
				query := body["spec"].(map[string]any)["steps"].([]map[string]any)[0]["query"].(map[string]any)
				delete(query, "output_contract")
				query["output_contract_ref"] = map[string]any{"contract_slug": "orders", "revision_slug": "v1", "schema_object_id": "order"}
				ref := query["output_contract_ref"].(map[string]any)
				if port == "event" {
					ref["representation"], ref["root_shape"] = "json", "object"
					body["spec"].(map[string]any)["steps"] = []map[string]any{{"kind": "input", "slug": "event", "input": map[string]any{"output_contract_ref": ref}}}
				}
				if port == "named" {
					body["spec"].(map[string]any)["steps"] = []map[string]any{{"kind": "transform", "slug": "audit", "transform": map[string]any{"sql": "SELECT id FROM orders", "output_kind": "rows", "inputs": map[string]any{"orders": "fetch"}, "input_contracts": map[string]any{"orders": map[string]any{"contract_ref": ref}}}}}
				}
				response := authenticatedRequest(t, app, store, "test-user", jsonRequest(t, http.MethodPost, "/organizations/acme/projects/default/environments/prod/pipeline-validation", body))
				defer func() { require.NoError(t, response.Body.Close()) }()
				if permitted {
					require.Equal(t, http.StatusOK, response.StatusCode)
					require.True(t, validated)
				} else {
					require.Equal(t, http.StatusForbidden, response.StatusCode)
					require.False(t, validated)
				}
			})
		}
	}
}

func TestServer_PipelineValidationResults(t *testing.T) {
	t.Parallel()

	t.Run("ListReturnsNonRunValidationHistory", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				ListValidationResultsFn: func(
					_ context.Context,
					orgSlug, projectSlug, environmentSlug slug.Slug,
					principal pipelinesvc.ValidationHistoryPrincipal,
					filters pipelinesvc.PipelineValidationResultFilters,
					req model.ListRequest,
				) (model.ListResponse[*pipelinesvc.PipelineValidationResultRecord], error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("prod"), environmentSlug)
					require.Equal(t, slug.Slug("test-user"), principal.UserSlug)
					require.NotNil(t, filters.PipelineSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), *filters.PipelineSlug)
					require.Equal(t, model.ListRequest{Limit: 5}, req)
					return model.ListResponse[*pipelinesvc.PipelineValidationResultRecord]{
						Items:   []*pipelinesvc.PipelineValidationResultRecord{testPipelineValidationRecord()},
						HasMore: true,
					}, nil
				},
			},
		})

		req, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			"/organizations/acme/projects/default/environments/prod/pipeline-validation-results?pipeline_slug=acme-users-sync&limit=5",
			nil,
		)
		require.NoError(t, err)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineValidationResultListResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Len(t, body.ValidationResults, 1)
		require.Equal(t, api.Slug("validation-acme-users-sync-a1b2c3"), body.ValidationResults[0].ValidationSlug)
		require.Equal(t, api.PipelineValidationResultSourceRevalidate, body.ValidationResults[0].Source)
		require.False(t, body.ValidationResults[0].Valid)
		require.Len(t, body.ValidationResults[0].Diagnostics, 1)
		require.Equal(t, "PIPELINE_DATA_SOURCE_REF_MISSING_BINDING", body.ValidationResults[0].Diagnostics[0].Code)
		require.NotNil(t, body.ValidationResults[0].Diagnostics[0].DataSourceRefSlug)
		require.Equal(t, api.NullableSlug("orders-db"), *body.ValidationResults[0].Diagnostics[0].DataSourceRefSlug)
	})

	t.Run("GetReturnsScopedValidationResult", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				GetValidationResultFn: func(
					_ context.Context,
					orgSlug, projectSlug, environmentSlug slug.Slug,
					principal pipelinesvc.ValidationHistoryPrincipal,
					resultSlug slug.Slug,
				) (*pipelinesvc.PipelineValidationResultRecord, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("prod"), environmentSlug)
					require.Equal(t, slug.Slug("test-user"), principal.UserSlug)
					require.Equal(t, slug.Slug("validation-acme-users-sync-a1b2c3"), resultSlug)
					return testPipelineValidationRecord(), nil
				},
			},
		})

		req, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			"/organizations/acme/projects/default/environments/prod/pipeline-validation-results/validation-acme-users-sync-a1b2c3",
			nil,
		)
		require.NoError(t, err)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineValidationResultRecord
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, api.Slug("validation-acme-users-sync-a1b2c3"), body.ValidationSlug)
		require.Equal(t, api.Slug("prod"), body.EnvironmentSlug)
	})

	t.Run("RevalidateReturnsStoredResult", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				RevalidatePipelineFn: func(
					_ context.Context,
					orgSlug, projectSlug, environmentSlug slug.Slug,
					principal pipelinesvc.ValidationHistoryPrincipal,
					pipelineSlug, memberSlug slug.Slug,
				) (*pipelinesvc.PipelineValidationResultRecord, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("prod"), environmentSlug)
					require.Equal(t, slug.Slug("test-user"), principal.UserSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)
					return testPipelineValidationRecord(), nil
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipelines/acme-users-sync:revalidate",
			nil,
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineValidationResultRecord
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, api.PipelineValidationResultSourceRevalidate, body.Source)
		require.Equal(t, api.Slug("acme-users-sync"), body.PipelineSlug)
		require.False(t, body.Valid)
	})

	t.Run("RevalidateRequiresUpdatePermission", func(t *testing.T) {
		t.Parallel()

		updateChecked := false
		app, store := newTestApp(t, testServerConfig{
			authService: fakeAuthService{
				CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					if input.Resource == model.ResourcePipelines && input.Action == model.Update {
						updateChecked = true
						return false, nil
					}
					return true, nil
				},
			},
			pipelinesService: &fakePipelinesService{
				RevalidatePipelineFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					pipelinesvc.ValidationHistoryPrincipal,
					slug.Slug,
					slug.Slug,
				) (*pipelinesvc.PipelineValidationResultRecord, error) {
					t.Fatal("revalidation must not run without update permission")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipelines/acme-users-sync:revalidate",
			nil,
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		require.True(t, updateChecked)
	})

	t.Run("RevalidateMapsErrorsWithoutExposingInternalDetails", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			name   string
			err    error
			status int
			code   string
		}{
			{"MissingPipeline", pipelinesvc.ErrPipelineNotFound, http.StatusNotFound, "PIPELINE_NOT_FOUND"},
			{"InternalFailure", xerror.Unexpected().Detail("private database diagnostic"), http.StatusInternalServerError, "INTERNAL_ERROR"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				var logs bytes.Buffer
				app, store := newTestApp(t, testServerConfig{
					pipelinesService: pipelinesvc.NewServiceWithLogging(&fakePipelinesService{
						RevalidatePipelineFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug,
							pipelinesvc.ValidationHistoryPrincipal, slug.Slug, slug.Slug,
						) (*pipelinesvc.PipelineValidationResultRecord, error) {
							return nil, tc.err
						},
					}, slog.New(slog.NewJSONHandler(&logs, nil))),
				})
				req := jsonRequest(t, http.MethodPost,
					"/organizations/acme/projects/default/environments/prod/pipelines/acme-users-sync:revalidate", nil)
				resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
				defer func() { _ = resp.Body.Close() }()

				require.Equal(t, tc.status, resp.StatusCode)
				var body apierror.ErrorResponse
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
				require.Equal(t, tc.code, body.Error.Code)
				require.Empty(t, body.Error.DetailedError)
				require.NotContains(t, body.Error.Message, "private database diagnostic")
				var entry map[string]any
				require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
				require.Equal(t, "service/pipelinesvc.Service.RevalidatePipeline", entry["braidplane.method"])
				require.Equal(t, tc.code, entry["error.type"])
				require.Equal(t, "error", entry["outcome"])
				require.Equal(t, tc.err.Error(), entry["error.message"])
			})
		}
	})

	t.Run("RevalidateRejectsOrganizationAPIKeyPrincipal", func(t *testing.T) {
		t.Parallel()

		apiKeySlug := slug.Slug("automation-key")
		app, _ := newTestApp(t, testServerConfig{
			middleware: []fiber.Handler{
				func(c fiber.Ctx) error {
					reqctx.SetAPIKeyPrincipal(c, reqctx.APIKeyPrincipal{
						Slug:    apiKeySlug,
						OrgSlug: slug.Slug("acme"),
						Role:    slug.Slug("pipeline-runner"),
					})
					return c.Next()
				},
			},
			pipelinesService: &fakePipelinesService{
				RevalidatePipelineFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					pipelinesvc.ValidationHistoryPrincipal,
					slug.Slug,
					slug.Slug,
				) (*pipelinesvc.PipelineValidationResultRecord, error) {
					t.Fatal("revalidation must not run for organization api key principals")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipelines/acme-users-sync:revalidate",
			nil,
		)
		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

func TestServer_DriftImpactReports(t *testing.T) {
	t.Parallel()

	t.Run("ListReturnsFilteredReports", func(t *testing.T) {
		t.Parallel()

		createdAt := time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC)
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				ListDriftImpactReportsFn: func(
					_ context.Context,
					orgSlug slug.Slug,
					filters pipelinesvc.DriftImpactReportFilters,
				) ([]*pipelinesvc.DriftImpactReport, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), *filters.ProjectSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), *filters.PipelineSlug)
					require.Equal(t, slug.Slug("users-db"), *filters.DataSourceSlug)
					require.Equal(t, pipelinedrift.ReportStatusOpen, *filters.Status)
					require.Equal(t, pipelinedrift.ReportSeverityWarning, *filters.Severity)
					require.Equal(t, int32(7), filters.Limit)
					return []*pipelinesvc.DriftImpactReport{{
						Slug:           slug.Slug("contract-impact-users-a1b2c3"),
						ReportType:     pipelinedrift.ReportTypeContractImpact,
						Status:         pipelinedrift.ReportStatusOpen,
						Severity:       pipelinedrift.ReportSeverityWarning,
						Summary:        "1 contract impact finding(s), highest severity warning",
						ProjectSlug:    slugPtr(slug.Slug("default")),
						PipelineSlug:   slugPtr(slug.Slug("acme-users-sync")),
						DataSourceSlug: slugPtr(slug.Slug("users-db")),
						CreatedAt:      createdAt,
						Changes: []pipelinedrift.Change{{
							Kind:       "addition",
							Path:       "step[fetch-users].query.output_contract.email",
							Before:     "<none>",
							After:      "string(required=false,nullable=false)",
							Severity:   pipelinedrift.ReportSeverityWarning,
							ReasonCode: "addition",
						}},
						Impacts: []pipelinedrift.Impact{{
							Kind:         "edge",
							PipelineSlug: "acme-users-sync",
							StepSlug:     "write-users",
							EdgeSlug:     "fetch-users->write-users",
							Severity:     pipelinedrift.ReportSeverityWarning,
							Message:      "downstream edge fetch-users->write-users may be affected",
						}},
					}}, nil
				},
			},
		})

		req, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			"/organizations/acme/projects/default/drift-impact-reports?pipeline_slug=acme-users-sync&data_source_slug=users-db&status=open&severity=warning&limit=7",
			nil,
		)
		require.NoError(t, err)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.DriftImpactReportListResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Len(t, body.Reports, 1)
		require.Equal(t, api.Slug("contract-impact-users-a1b2c3"), body.Reports[0].Slug)
		require.Equal(t, api.DriftImpactReportStatusOpen, body.Reports[0].Status)
		require.Equal(t, api.DriftImpactReportSeverityWarning, body.Reports[0].Severity)
		require.Equal(t, api.NullableSlug("users-db"), *body.Reports[0].DataSourceSlug)
		require.Len(t, body.Reports[0].Changes, 1)
		require.Equal(t, api.DriftImpactImpactKindEdge, body.Reports[0].Impacts[0].Kind)
	})

	t.Run("GetReturnsProjectScopedReport", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				GetDriftImpactReportFn: func(
					_ context.Context,
					orgSlug, projectSlug, reportSlug slug.Slug,
				) (*pipelinesvc.DriftImpactReport, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("contract-impact-users-a1b2c3"), reportSlug)
					return &pipelinesvc.DriftImpactReport{
						Slug:        reportSlug,
						ReportType:  pipelinedrift.ReportTypeContractImpact,
						Status:      pipelinedrift.ReportStatusOpen,
						Severity:    pipelinedrift.ReportSeverityBreaking,
						Summary:     "1 contract impact finding(s), highest severity breaking",
						ProjectSlug: slugPtr(slug.Slug("default")),
						CreatedAt:   time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC),
					}, nil
				},
			},
		})

		req, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			"/organizations/acme/projects/default/drift-impact-reports/contract-impact-users-a1b2c3",
			nil,
		)
		require.NoError(t, err)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.DriftImpactReport
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, api.Slug("contract-impact-users-a1b2c3"), body.Slug)
		require.Equal(t, api.DriftImpactReportSeverityBreaking, body.Severity)
		require.Equal(t, api.Slug("default"), body.ProjectSlug)
	})

	t.Run("ListReturnsForbiddenWithoutDataSourceRead", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			authService: fakeAuthService{
				CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, slug.Slug("test-user"), input.UserSlug)
					require.NotNil(t, input.OrgSlug)
					require.Equal(t, slug.Slug("acme"), *input.OrgSlug)
					require.Equal(t, model.Read, input.Action)
					switch input.Resource {
					case model.ResourcePipelines:
						return true, nil
					case model.ResourceDataSources:
						return false, nil
					default:
						t.Fatalf("unexpected authorization resource %s", input.Resource)
						return false, nil
					}
				},
			},
			pipelinesService: &fakePipelinesService{
				ListDriftImpactReportsFn: func(
					context.Context,
					slug.Slug,
					pipelinesvc.DriftImpactReportFilters,
				) ([]*pipelinesvc.DriftImpactReport, error) {
					t.Fatal("service must not be called when data source authorization denies report list")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		req, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			"/organizations/acme/projects/default/drift-impact-reports",
			nil,
		)
		require.NoError(t, err)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("GetReturnsForbiddenWithoutDataSourceRead", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			authService: fakeAuthService{
				CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, slug.Slug("test-user"), input.UserSlug)
					require.NotNil(t, input.OrgSlug)
					require.Equal(t, slug.Slug("acme"), *input.OrgSlug)
					require.Equal(t, model.Read, input.Action)
					switch input.Resource {
					case model.ResourcePipelines:
						return true, nil
					case model.ResourceDataSources:
						return false, nil
					default:
						t.Fatalf("unexpected authorization resource %s", input.Resource)
						return false, nil
					}
				},
			},
			pipelinesService: &fakePipelinesService{
				GetDriftImpactReportFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
				) (*pipelinesvc.DriftImpactReport, error) {
					t.Fatal("service must not be called when data source authorization denies report detail")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		req, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			"/organizations/acme/projects/default/drift-impact-reports/contract-impact-users-a1b2c3",
			nil,
		)
		require.NoError(t, err)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

func existingPipelineValidationProjectService(t *testing.T) *fakeProjectsService {
	t.Helper()

	return &fakeProjectsService{
		GetProjectFn: func(_ context.Context, orgSlug, projectSlug slug.Slug) (*model.Project, error) {
			require.Equal(t, slug.Slug("acme"), orgSlug)
			require.Equal(t, slug.Slug("default"), projectSlug)
			return &model.Project{
				Slug: projectSlug,
				Name: "Default",
			}, nil
		},
	}
}

func TestToAPIPipelineValidationResponseHandlesNilResult(t *testing.T) {
	t.Parallel()

	body := toAPIPipelineValidationResponse(nil)

	require.False(t, body.Valid)
	require.Empty(t, body.Diagnostics)
}

func TestServer_ValidatePipelineReturnsStructuredDiagnostics(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsStructuredDiagnostics", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			projectsService: existingPipelineValidationProjectService(t),
			pipelinesService: &fakePipelinesService{
				ValidateFn: func(
					_ context.Context,
					orgSlug, projectSlug, environmentSlug, memberSlug slug.Slug,
					pipeline *pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineValidationResult, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)
					require.Equal(t, slug.Slug("prod"), environmentSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipeline.Metadata.Slug)

					return &pipelinesvc.PipelineValidationResult{
						Valid: false,
						Diagnostics: []pipelinesvc.PipelineValidationDiagnostic{{
							Severity: "error",
							Code:     "PIPELINE_INVALID_INPUT",
							Field:    "steps[0].query",
							StepSlug: strPtr("fetch-users"),
							Message:  "query target must set exactly one of operation_ref or operation_inline",
						}},
					}, nil
				},
			},
		})

		requestBody := testPipelineRequestBody()
		spec := requestBody["spec"].(map[string]any)
		steps := spec["steps"].([]map[string]any)
		steps[0]["query"] = map[string]any{
			"data_source_ref":  "acme-postgres",
			"operation_type":   "sql",
			"operation_ref":    "list-active-users",
			"operation_inline": map[string]any{"sql": "select * from users"},
		}

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipeline-validation",
			requestBody,
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineValidationResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.False(t, body.Valid)
		require.Len(t, body.Diagnostics, 1)
		require.Equal(t, api.PipelineValidationDiagnosticSeverityError, body.Diagnostics[0].Severity)
		require.Equal(t, "PIPELINE_INVALID_INPUT", body.Diagnostics[0].Code)
		require.Equal(t, "steps[0].query", body.Diagnostics[0].Field)
		require.NotNil(t, body.Diagnostics[0].StepSlug)
		require.Equal(t, api.NullableSlug("fetch-users"), *body.Diagnostics[0].StepSlug)
		require.Equal(
			t,
			"query target must set exactly one of operation_ref or operation_inline",
			body.Diagnostics[0].Message,
		)
	})

	t.Run("ReturnsForbiddenWhenUnauthorized", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			authService: fakeAuthService{
				CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, slug.Slug("test-user"), input.UserSlug)
					require.NotNil(t, input.OrgSlug)
					require.Equal(t, slug.Slug("acme"), *input.OrgSlug)
					require.Equal(t, model.Read, input.Action)
					require.Equal(t, model.ResourcePipelines, input.Resource)
					return false, nil
				},
			},
			pipelinesService: &fakePipelinesService{
				ValidateFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					*pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineValidationResult, error) {
					t.Fatal("service must not be called when authorization denies validation")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipeline-validation",
			testPipelineRequestBody(),
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("ReturnsForbiddenWithoutDataSourceRead", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			projectsService: &fakeProjectsService{
				GetProjectFn: func(context.Context, slug.Slug, slug.Slug) (*model.Project, error) {
					t.Fatal("project lookup must not run when data source authorization denies validation")
					return nil, errFakeProjectsServiceFnNotSet
				},
			},
			authService: fakeAuthService{
				CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, slug.Slug("test-user"), input.UserSlug)
					require.NotNil(t, input.OrgSlug)
					require.Equal(t, slug.Slug("acme"), *input.OrgSlug)
					require.Equal(t, model.Read, input.Action)
					switch input.Resource {
					case model.ResourcePipelines:
						return true, nil
					case model.ResourceDataSources:
						return false, nil
					default:
						t.Fatalf("unexpected authorization resource %s", input.Resource)
						return false, nil
					}
				},
			},
			pipelinesService: &fakePipelinesService{
				ValidateFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					*pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineValidationResult, error) {
					t.Fatal("service must not be called when data source authorization denies validation")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipeline-validation",
			testPipelineRequestBody(),
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

func TestServer_GetPipeline(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsPipeline", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		sourcePath := "pipelines/acme-users-sync.pipeline.yaml"
		sourceRef := "refs/heads/main"
		lastAppliedCommit := "abc123def456"
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				GetFn: func(
					_ context.Context,
					orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
				) (*pipelinesvc.PipelineDetail, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)
					return testPipelineDetail(
						now,
						nil,
						&sourcePath,
						&sourceRef,
						&lastAppliedCommit,
					), nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(t, http.MethodGet, "/organizations/acme/projects/default/pipelines/acme-users-sync", nil)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.Pipeline
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "acme-users-sync", body.Slug)
		require.Equal(t, "ACME Users Sync", body.Name)
		require.Equal(t, api.Drifted, body.SyncStatus)
		require.Equal(t, sourcePath, body.SourcePath)
		require.Equal(t, sourceRef, body.SourceRef)
		require.Equal(t, lastAppliedCommit, body.LastAppliedCommit)
		require.NotNil(t, body.CurrentVersionSpecHash)
		require.Equal(t, "spec-hash-12", *body.CurrentVersionSpecHash)
		require.Equal(t, "acme-users-sync", body.Spec.Metadata.Slug)
	})

	t.Run("ReturnsNotFoundWhenPipelineMissing", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				GetFn: func(
					_ context.Context,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
				) (*pipelinesvc.PipelineDetail, error) {
					return nil, pipelinesvc.ErrPipelineNotFound
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(t, http.MethodGet, "/organizations/acme/projects/default/pipelines/acme-users-sync", nil)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestServer_ListPipelineVersions(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsPipelineVersions", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		fullSpecHash := "abc1234fffff000000000000000000000000000000000000000000000000000"
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				ListVersionsFn: func(
					_ context.Context,
					orgSlug, projectSlug, pipelineSlug slug.Slug,
				) ([]*pipelinesvc.PipelineVersionSummary, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					return []*pipelinesvc.PipelineVersionSummary{{
						SpecHash:      fullSpecHash,
						ShortSpecHash: "abc1234fffff",
						PipelineSlug:  pipelineSlug,
						CreatedAt:     now,
					}}, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodGet,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/versions",
			nil,
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineVersionListResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Len(t, body.Versions, 1)
		require.Equal(t, fullSpecHash, body.Versions[0].SpecHash)
		require.Equal(t, "abc1234fffff", body.Versions[0].ShortSpecHash)
		require.Equal(t, "acme-users-sync", body.Versions[0].PipelineSlug)
	})
}

func TestServer_GetPipelineVersion(t *testing.T) {
	t.Parallel()

	t.Run("composition available", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		specHash := "abc1234fffff000000000000000000000000000000000000000000000000000"
		app, store := newTestApp(t, testServerConfig{
			compiledPlansService: fakeCompiledPlans{},
			pipelinesService: &fakePipelinesService{
				GetVersionFn: func(
					_ context.Context,
					orgSlug, projectSlug, pipelineSlug slug.Slug,
					versionHash string,
				) (*pipelinesvc.PipelineVersionDetail, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.Equal(t, specHash, versionHash)
					return &pipelinesvc.PipelineVersionDetail{
						Summary: &pipelinesvc.PipelineVersionSummary{
							SpecHash:      versionHash,
							ShortSpecHash: "abc1234fffff",
							PipelineSlug:  pipelineSlug,
							CreatedAt:     now,
						},
						Spec: testPipelineDetail(now, nil, nil, nil, nil).Spec,
					}, nil
				},
			},
		})
		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodGet,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/versions/"+specHash,
			nil,
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineVersion
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.True(t, body.CompositionAdmissionEnabled)
		require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
		require.Equal(t, specHash, body.SpecHash)
		require.Equal(t, "abc1234fffff", body.ShortSpecHash)
		require.Equal(t, "acme-users-sync", body.PipelineSlug)
		require.Equal(t, "acme-users-sync", body.Spec.Metadata.Slug)
	})
}

func TestServer_PipelineDeployments(t *testing.T) {
	t.Parallel()

	t.Run("ListsEnvironmentDeployments", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		specHash := "abc1234fffff000000000000000000000000000000000000000000000000000"
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				ListDeploymentsFn: func(
					_ context.Context,
					orgSlug, projectSlug, environmentSlug slug.Slug,
				) ([]*pipelinesvc.PipelineDeploymentDetail, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("dev"), environmentSlug)
					return []*pipelinesvc.PipelineDeploymentDetail{{
						ProjectSlug:                 projectSlug,
						EnvironmentSlug:             environmentSlug,
						PipelineSlug:                slug.Slug("acme-users-sync"),
						CurrentVersionSpecHash:      specHash,
						CurrentVersionShortSpecHash: "abc1234ffff",
						DeployedByActorType:         "user",
						DeployedByActorSlug:         strPtr("alice"),
						DeployedAt:                  now,
						UpdatedAt:                   now,
					}}, nil
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodGet,
			"/organizations/acme/projects/default/environments/dev/pipeline-deployments",
			nil,
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineDeploymentListResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Len(t, body.Deployments, 1)
		require.Equal(t, "default", body.Deployments[0].ProjectSlug)
		require.Equal(t, "dev", body.Deployments[0].EnvironmentSlug)
		require.Equal(t, "acme-users-sync", body.Deployments[0].PipelineSlug)
		require.Equal(t, specHash, body.Deployments[0].CurrentVersionSpecHash)
		require.Equal(t, "alice", *body.Deployments[0].DeployedByActorSlug)
	})

	t.Run("DeploysPipelineVersionToEnvironment", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		specHash := "abc1234fffff000000000000000000000000000000000000000000000000000"
		expectedCurrentSpecHash := "previous"
		sourceEnvironmentSlug := "stg"
		proofReceiptSlug := "proof-123"
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				GetDeploymentFn: func(
					context.Context,
					slug.Slug, slug.Slug, slug.Slug, slug.Slug,
				) (*pipelinesvc.PipelineDeploymentDetail, error) {
					return &pipelinesvc.PipelineDeploymentDetail{}, nil
				},
				DeployFn: func(
					_ context.Context,
					orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
					actor model.EventActor,
					req pipelinesvc.DeployPipelineRequest,
				) (*pipelinesvc.PipelineDeploymentDetail, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("prod"), environmentSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.Equal(t, model.EventActorTypeUser, actor.Type)
					require.NotNil(t, actor.Slug)
					require.Equal(t, slug.Slug("test-user"), *actor.Slug)
					require.Equal(t, specHash, req.SpecHash)
					require.NotNil(t, req.ExpectedRevision)
					require.EqualValues(t, 7, *req.ExpectedRevision)
					require.NotNil(t, req.CompiledPlanDigest)
					require.Equal(t, "sha256:6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8", *req.CompiledPlanDigest)
					require.Equal(t, "string", req.ResolutionInputs["KIND"].Value)
					require.Equal(t, "vars-v2", req.ResolutionInputs["KIND"].Revision)
					require.NotNil(t, req.ExpectedCurrentSpecHash)
					require.Equal(t, expectedCurrentSpecHash, *req.ExpectedCurrentSpecHash)
					require.NotNil(t, req.SourceEnvironmentSlug)
					require.Equal(t, slug.Slug(sourceEnvironmentSlug), *req.SourceEnvironmentSlug)
					require.NotNil(t, req.ProofReceiptSlug)
					require.Equal(t, slug.Slug(proofReceiptSlug), *req.ProofReceiptSlug)
					return &pipelinesvc.PipelineDeploymentDetail{
						ProjectSlug:                 projectSlug,
						EnvironmentSlug:             environmentSlug,
						PipelineSlug:                pipelineSlug,
						CurrentVersionSpecHash:      specHash,
						CurrentVersionShortSpecHash: "abc1234ffff",
						PreviousVersionSpecHash:     &expectedCurrentSpecHash,
						DeployedByActorType:         "user",
						DeployedByActorSlug:         strPtr("test-user"),
						DeployedAt:                  now,
						UpdatedAt:                   now,
						DataSourceRefSync: &datasourcerefssvc.PipelineDataSourceRefSyncResult{
							Items: []datasourcerefssvc.PipelineDataSourceRefSyncItem{{
								PipelineSlug:         pipelineSlug,
								RefSlug:              slug.Slug("users-source"),
								DataSourceType:       model.DataSourceTypePostgres,
								RequiredCapabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource},
								Status:               datasourcerefssvc.PipelineDataSourceRefSyncStatusCreated,
							}},
						},
					}, nil
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipelines/acme-users-sync:deploy",
			map[string]any{
				"spec_hash":                  specHash,
				"expected_revision":          7,
				"compiled_plan_digest":       "sha256:6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8",
				"resolution_inputs":          map[string]any{"KIND": map[string]any{"value": "string", "revision": "vars-v2"}},
				"proof_receipt_slug":         proofReceiptSlug,
				"expected_current_spec_hash": expectedCurrentSpecHash,
				"source_environment_slug":    sourceEnvironmentSlug,
			},
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineDeployment
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "default", body.ProjectSlug)
		require.Equal(t, "prod", body.EnvironmentSlug)
		require.Equal(t, "acme-users-sync", body.PipelineSlug)
		require.Equal(t, specHash, body.CurrentVersionSpecHash)
		require.NotNil(t, body.PreviousVersionSpecHash)
		require.Equal(t, expectedCurrentSpecHash, *body.PreviousVersionSpecHash)
		require.Len(t, body.DataSourceRefSync.DataSourceRefs, 1)
		require.Equal(t, "acme-users-sync", body.DataSourceRefSync.DataSourceRefs[0].PipelineSlug)
		require.Equal(t, "users-source", body.DataSourceRefSync.DataSourceRefs[0].DataSourceRefSlug)
	})

	t.Run("RejectsNewDeploymentWhenPlanLimitReached", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			admissionService: &testfakes.AdmissionService{
				CheckDeploymentAdmissionFn: func(context.Context, slug.Slug) error {
					return xerror.Forbidden(xerror.Organization)
				},
			},
			pipelinesService: &fakePipelinesService{
				GetDeploymentFn: func(
					context.Context,
					slug.Slug, slug.Slug, slug.Slug, slug.Slug,
				) (*pipelinesvc.PipelineDeploymentDetail, error) {
					return nil, pipelinesvc.ErrPipelineDeploymentNotFound
				},
				DeployFn: func(
					context.Context,
					slug.Slug, slug.Slug, slug.Slug, slug.Slug,
					model.EventActor,
					pipelinesvc.DeployPipelineRequest,
				) (*pipelinesvc.PipelineDeploymentDetail, error) {
					t.Fatal("deployment must not start after entitlement denial")
					return nil, xerror.Unexpected()
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipelines/acme-users-sync:deploy",
			map[string]any{"spec_hash": "spec-hash"},
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

func TestPipelineRunFailureHelpers(t *testing.T) {
	t.Parallel()

	require.Equal(t, api.PipelineRunFailureKindDataSource, toAPIFailureKindValue("data_source"))
	require.Equal(t, api.PipelineRunFailureKindPlatform, toAPIFailureKindValue("platform"))
	require.Equal(t, api.PipelineRunFailureKindPlatform, toAPIFailureKindValue("unknown"))
	require.Equal(t, api.PipelineRunFailureSideSource, toAPIFailureSideValue("source"))
	require.Equal(t, api.PipelineRunFailureSideSink, toAPIFailureSideValue("sink"))
	require.Equal(t, api.PipelineRunFailureSideTransform, toAPIFailureSideValue("transform"))
	require.Equal(t, api.PipelineRunFailureSideConnector, toAPIFailureSideValue("connector"))
	require.Equal(t, api.PipelineRunFailureSideTransform, toAPIFailureSideValue("unknown"))
	require.Nil(t, toAPIFailureKind(nil))
	require.Nil(t, toAPIFailureReasonCode(pipelinerun.FailureReasonCodeUnknown))
	require.Equal(t, api.ResourceScopeRejected, *toAPIFailureReasonCode(pipelinerun.FailureReasonCodeResourceScopeRejected))
	for _, reason := range []pipelinerun.FailureReasonCode{
		pipelinerun.FailureReasonCodeDataSourcePermissionDenied,
		pipelinerun.FailureReasonCodeDataSourceConnectionFailed,
		pipelinerun.FailureReasonCodeDataSourceTimeout,
		pipelinerun.FailureReasonCodeEventInputRejected,
		pipelinerun.FailureReasonCodeDestinationConstraintViolation,
	} {
		require.Equal(t, api.PipelineRunFailureReasonCode(reason.String()), *toAPIFailureReasonCode(reason))
	}
	require.Equal(
		t,
		api.S3ObjectWriteFailed,
		*toAPIFailureReasonCode(pipelinerun.FailureReasonCodeS3ObjectWriteFailed),
	)
	require.Nil(t, optionalStepSlug(""))
	require.Nil(t, optionalString(""))

	contract := toAPIPipelineRunContractFailureDiagnostic(&pipelinerun.ContractFailureDiagnostic{
		ContractName:   "users_rows",
		FieldName:      "email",
		Rule:           "format",
		ViolationCount: 2,
		ReasonCode:     pipelinevalidate.ContractReasonFieldValidationFailed,
	})
	require.NotNil(t, contract)
	require.Equal(t, "users_rows", contract.ContractName)
	require.Equal(t, "email", contract.FieldName)
	require.Equal(t, "format", contract.Rule)
	require.Equal(t, 2, contract.ViolationCount)
	require.Equal(t, "contract_field_validation_failed", contract.ReasonCode)
	require.Nil(t, toAPIPipelineRunContractFailureDiagnostic(nil))
}

func TestServer_FormatPipelineSpec(t *testing.T) {
	t.Parallel()

	t.Run("FormatsYAML", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{})
		req := jsonRequest(t, http.MethodPost, "/organizations/acme/pipeline-spec:format", map[string]any{
			"format": "yaml",
			"data": `
metadata:
  slug: orders-sync
  name: Orders Sync
steps:
  - kind: sink
    slug: load-orders
    sink:
      input: shape-orders
      data_source_ref: acme-warehouse
      operation_type: sql
      operation_ref: load-orders-operation
  - kind: transform
    slug: shape-orders
    transform:
      sql: SELECT * FROM input
      output_kind: rows
      inputs:
        orders: fetch-orders
  - kind: query
    slug: fetch-orders
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: fetch-orders-operation
`,
		})

		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Format      string `json:"format"`
			Data        string `json:"data"`
			Diagnostics []any  `json:"diagnostics"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "yaml", body.Format)
		require.Empty(t, body.Diagnostics)
		require.Contains(t, body.Data, "slug: fetch-orders")
		require.Less(t, strings.Index(body.Data, "slug: fetch-orders"), strings.Index(body.Data, "slug: shape-orders"))
		require.Less(t, strings.Index(body.Data, "slug: shape-orders"), strings.Index(body.Data, "slug: load-orders"))
	})

	t.Run("Returns400ForMalformedSerializedSpecData", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{})
		req := jsonRequest(t, http.MethodPost, "/organizations/acme/pipeline-spec:format", map[string]any{
			"format": "json",
			"data":   `{"metadata":`,
		})

		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		var body api.ErrorResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "PIPELINE_INVALID_INPUT", body.Error.Code)
	})

	t.Run("Returns400ForTrailingSerializedJSONData", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{})
		req := jsonRequest(t, http.MethodPost, "/organizations/acme/pipeline-spec:format", map[string]any{
			"format": "json",
			"data":   `{"metadata":{"slug":"orders-sync","name":"Orders Sync"},"steps":[]} {"metadata":{"slug":"other","name":"Other"},"steps":[]}`,
		})

		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		var body api.ErrorResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "PIPELINE_INVALID_INPUT", body.Error.Code)
	})

	t.Run("Returns400ForExtraSerializedYAMLDocuments", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{})
		req := jsonRequest(t, http.MethodPost, "/organizations/acme/pipeline-spec:format", map[string]any{
			"format": "yaml",
			"data": `
metadata:
  slug: orders-sync
  name: Orders Sync
steps: []
---
metadata:
  slug: other
  name: Other
steps: []
`,
		})

		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		var body api.ErrorResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "PIPELINE_INVALID_INPUT", body.Error.Code)
	})

	t.Run("DoesNotMatchRouteWithMissingLiteralFormatColon", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{})
		req := jsonRequest(t, http.MethodPost, "/organizations/acme/pipeline-specanything", map[string]any{
			"format": "json",
			"data":   `{"metadata":{"slug":"orders-sync","name":"Orders Sync"},"steps":[]}`,
		})

		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("Returns400ForNonCanonicalFormatEnum", func(t *testing.T) {
		t.Parallel()

		testCases := []struct {
			name   string
			format string
		}{
			{name: "WhitespacePadded", format: " yaml "},
			{name: "Unsupported", format: "toml"},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				app, store := newTestApp(t, testServerConfig{})
				req := jsonRequest(t, http.MethodPost, "/organizations/acme/pipeline-spec:format", map[string]any{
					"format": tc.format,
					"data": `
metadata:
  slug: orders-sync
  name: Orders Sync
steps: []
`,
				})

				resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
				defer func() { _ = resp.Body.Close() }()

				require.Equal(t, http.StatusBadRequest, resp.StatusCode)
				var body api.ErrorResponse
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
				require.Equal(t, "VALIDATION_FAILED", body.Error.Code)
			})
		}
	})

	t.Run("ReturnsMalformedUnresolvedReferencesAsStringDiagnostics", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{})
		req := jsonRequest(t, http.MethodPost, "/organizations/acme/pipeline-spec:format", map[string]any{
			"format": "json",
			"data": `{
				"metadata": {"slug": "orders-sync", "name": "Orders Sync"},
				"steps": [{
					"kind": "query",
					"slug": "fetch-users",
					"query": {
						"data_source_ref": "acme-postgres",
						"operation_type": "sql",
						"operation_ref": "fetch-users-operation",
						"parameters": {
							"source": "$steps.fetch_users.rows"
						}
					}
				}]
			}`,
		})

		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Diagnostics []struct {
				StepSlug           string `json:"step_slug"`
				ReferencedStepSlug string `json:"referenced_step_slug"`
			} `json:"diagnostics"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Len(t, body.Diagnostics, 1)
		require.Equal(t, "fetch-users", body.Diagnostics[0].StepSlug)
		require.Equal(t, "fetch_users", body.Diagnostics[0].ReferencedStepSlug)
	})

	t.Run("ReturnsMalformedDiagnosticSourceStepSlugAsString", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{})
		req := jsonRequest(t, http.MethodPost, "/organizations/acme/pipeline-spec:format", map[string]any{
			"format": "json",
			"data": `{
				"metadata": {"slug": "orders-sync", "name": "Orders Sync"},
				"steps": [{
					"kind": "query",
					"slug": "fetch_users",
					"query": {
						"data_source_ref": "acme-postgres",
						"operation_type": "sql",
						"operation_ref": "fetch-users-operation",
						"parameters": {
							"source": "$steps.missing-users.rows"
						}
					}
				}]
			}`,
		})

		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Diagnostics []struct {
				StepSlug           string `json:"step_slug"`
				ReferencedStepSlug string `json:"referenced_step_slug"`
			} `json:"diagnostics"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Len(t, body.Diagnostics, 1)
		require.Equal(t, "fetch_users", body.Diagnostics[0].StepSlug)
		require.Equal(t, "missing-users", body.Diagnostics[0].ReferencedStepSlug)
	})
}

func TestServer_UpdatePipeline(t *testing.T) {
	t.Parallel()

	t.Run("UpdatesPipeline", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				UpdateFn: func(
					_ context.Context,
					orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
					actor model.EventActor,
					pipeline *pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineDetail, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)
					require.Equal(t, model.EventActorTypeUser, actor.Type)
					require.NotNil(t, actor.Slug)
					require.Equal(t, slug.Slug("test-user"), *actor.Slug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipeline.Metadata.Slug)
					require.Equal(t, "Updated Pipeline", pipeline.Metadata.Name)
					detail := testPipelineDetail(now, nil, nil, nil, nil)
					detail.Summary.Name = "Updated Pipeline"
					detail.Spec.Metadata.Name = "Updated Pipeline"
					return detail, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPut,
			"/organizations/acme/projects/default/pipelines/acme-users-sync",
			map[string]any{
				"spec": map[string]any{
					"metadata": map[string]any{"name": "Updated Pipeline"},
					"steps": []map[string]any{
						{
							"kind": "query",
							"slug": "fetch-users",
							"name": "Fetch users",
							"query": map[string]any{
								"data_source_ref": "acme-postgres",
								"operation_type":  "sql",
								"operation_ref":   "list-active-users",
							},
						},
					},
				},
			},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.Pipeline
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "Updated Pipeline", body.Name)
		require.Equal(t, "acme-users-sync", body.Spec.Metadata.Slug)
	})

	t.Run("ReturnsForbiddenWhenUnauthorized", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			authService: fakeAuthService{
				CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
					return false, nil
				},
			},
			pipelinesService: &fakePipelinesService{},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPut,
			"/organizations/acme/projects/default/pipelines/acme-users-sync",
			testPipelineRequestBody(),
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("ReturnsRateLimitedBeforeQueueingProposal", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			middleware: []fiber.Handler{func(c fiber.Ctx) error {
				c.Locals(authorizedAbuseControlLocalKey, &authorizedAbuseControl{
					limiter:            deniedAbuseLimiter{},
					logger:             slog.New(slog.DiscardHandler),
					expensiveTenantMax: 1,
					expensiveWindow:    time.Minute,
				})
				return c.Next()
			}},
			pipelinesService: &fakePipelinesService{
				UpdateFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					model.EventActor,
					*pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineDetail, error) {
					t.Fatal("service must not queue a proposal after the organization budget is exhausted")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPut,
			"/organizations/acme/projects/default/pipelines/acme-users-sync",
			testPipelineRequestBody(),
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
		assertErrorResponse(t, resp, apierror.RateLimited())
	})

	t.Run("RejectsModeAndSourcePath", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				UpdateFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					model.EventActor,
					*pipelinespec.Pipeline,
				) (*pipelinesvc.PipelineDetail, error) {
					t.Fatal("service must not be called when update attempts ownership or source-path input")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPut,
			"/organizations/acme/projects/default/pipelines/acme-users-sync",
			map[string]any{
				"mode":        "git",
				"source_path": "pipelines/acme-users-sync.pipeline.yaml",
				"spec": map[string]any{
					"metadata": map[string]any{"name": "Updated Pipeline"},
					"steps":    []map[string]any{},
				},
			},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestPipelineFromCreateBody(t *testing.T) {
	t.Parallel()

	t.Run("RejectsContractValidationTypo", func(t *testing.T) {
		t.Parallel()

		requestBody := testPipelineRequestBody()
		addUnknownValidationKeyToPipelineRequest(requestBody)

		data, err := json.Marshal(requestBody)
		require.NoError(t, err)

		_, err = pipelineFromCreateBody(data)
		require.Error(t, err)
		require.ErrorContains(t, err, "minimum")
	})

	t.Run("AllowsInlineOperationValidationPayload", func(t *testing.T) {
		t.Parallel()

		requestBody := testPipelineRequestBody()
		spec := requestBody["spec"].(map[string]any)
		steps := spec["steps"].([]map[string]any)
		query := steps[0]["query"].(map[string]any)
		query["operation_inline"] = map[string]any{
			"validations": map[string]any{
				"minimum": 1,
			},
		}
		delete(query, "operation_ref")

		data, err := json.Marshal(requestBody)
		require.NoError(t, err)

		_, err = pipelineFromCreateBody(data)
		require.NoError(t, err)
	})
}

func TestServer_PipelineDraftEndpoints(t *testing.T) {
	t.Parallel()

	t.Run("SavePipelineDraftReturnsDraft", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				SaveDraftFn: func(
					_ context.Context,
					orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
					actor model.EventActor,
					pipeline *pipelinespec.Pipeline,
					expectedSpecHash *string,
				) (*pipelinesvc.PipelineDraftDetail, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)
					require.Nil(t, expectedSpecHash)
					require.Equal(t, "Updated Pipeline", pipeline.Metadata.Name)
					return &pipelinesvc.PipelineDraftDetail{
						PipelineSlug:          pipelineSlug,
						DraftBasedOnVersionID: int64Ptr(12),
						BasedOnSpecHash:       strPtr("spec-hash-12"),
						DraftState:            "proposal_open",
						DraftSpecHash:         "draft-spec-hash",
						Repository:            "https://github.com/acme/pipelines.git",
						BaseRef:               strPtr("refs/heads/main"),
						BranchName:            strPtr("braidplane/default/acme-users-sync/test-user"),
						ProposalProvider:      strPtr("github"),
						ProposalState:         strPtr("open"),
						ProposalIsStale:       true,
						ProposalUpdatedAt:     &now,
						CreatedAt:             now,
						UpdatedAt:             now,
						Spec:                  pipeline,
					}, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPut,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft",
			map[string]any{
				"spec": map[string]any{
					"metadata": map[string]any{"name": "Updated Pipeline"},
					"steps": []map[string]any{
						{
							"kind": "query",
							"slug": "fetch-users",
							"name": "Fetch users",
							"query": map[string]any{
								"data_source_ref": "acme-postgres",
								"operation_type":  "sql",
								"operation_ref":   "list-active-users",
							},
						},
					},
				},
			},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineDraftResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "acme-users-sync", body.Draft.PipelineSlug)
		require.NotNil(t, body.Draft.BasedOnSpecHash)
		require.Equal(t, "spec-hash-12", *body.Draft.BasedOnSpecHash)
	})

	t.Run("SavePipelineDraftRejectsUnknownContractValidationKeys", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				SaveDraftFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					model.EventActor,
					*pipelinespec.Pipeline,
					*string,
				) (*pipelinesvc.PipelineDraftDetail, error) {
					t.Fatal("draft service must not be called with unknown contract validation keys")
					return nil, errFakePipelinesServiceFnNotSet
				},
			},
		})

		requestBody := map[string]any{
			"spec": map[string]any{
				"metadata": map[string]any{"name": "Updated Pipeline"},
				"steps": []map[string]any{
					{
						"kind": "query",
						"slug": "fetch-users",
						"query": map[string]any{
							"data_source_ref": "acme-postgres",
							"operation_type":  "sql",
							"operation_ref":   "list-active-users",
						},
					},
				},
			},
		}
		addUnknownValidationKeyToPipelineRequest(requestBody)

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPut,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft",
			requestBody,
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("SavePipelineDraftReturnsStructuredConflict", func(t *testing.T) {
		t.Parallel()

		expectedSpecHash := "stale-spec-hash"
		currentSpecHash := "current-spec-hash"
		draftBaseSpecHash := "draft-base-spec-hash"
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				SaveDraftFn: func(
					_ context.Context,
					_, _, pipelineSlug, _ slug.Slug,
					_ model.EventActor,
					_ *pipelinespec.Pipeline,
					receivedExpectedSpecHash *string,
				) (*pipelinesvc.PipelineDraftDetail, error) {
					require.Equal(t, &expectedSpecHash, receivedExpectedSpecHash)
					return nil, &pipelinesvc.PipelineDraftConflictError{
						PipelineSlug:         pipelineSlug,
						ExpectedSpecHash:     &expectedSpecHash,
						CurrentSpecHash:      &currentSpecHash,
						DraftBasedOnSpecHash: &draftBaseSpecHash,
						DraftPreserved:       true,
						CanOverwrite:         false,
					}
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPut,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft",
			map[string]any{
				"expected_spec_hash": expectedSpecHash,
				"spec": map[string]any{
					"metadata": map[string]any{"name": "Updated Pipeline"},
					"steps":    []map[string]any{},
				},
			},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusConflict, resp.StatusCode)
		var body api.PipelineDraftConflictResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "PIPELINE_DRAFT_CONFLICT", body.Error.Code)
		require.Equal(t, "acme-users-sync", body.PipelineSlug)
		require.Equal(t, expectedSpecHash, *body.ExpectedSpecHash)
		require.Equal(t, currentSpecHash, *body.CurrentSpecHash)
		require.Equal(t, draftBaseSpecHash, *body.DraftBasedOnSpecHash)
		require.True(t, body.DraftPreserved)
		require.False(t, body.CanOverwrite)
	})

	t.Run("GetPipelineDraftReturnsDraft", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				GetDraftFn: func(
					_ context.Context,
					orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
				) (*pipelinesvc.PipelineDraftDetail, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)
					return &pipelinesvc.PipelineDraftDetail{
						PipelineSlug:          pipelineSlug,
						DraftBasedOnVersionID: int64Ptr(12),
						BasedOnSpecHash:       strPtr("spec-hash-12"),
						DraftState:            "proposal_open",
						DraftSpecHash:         "draft-spec-hash",
						Repository:            "https://github.com/acme/pipelines.git",
						ProposalProvider:      strPtr("github"),
						ProposalState:         strPtr("open"),
						ProposalIsStale:       true,
						ProposalUpdatedAt:     &now,
						CreatedAt:             now,
						UpdatedAt:             now,
						Spec:                  testPipelineDetail(now, nil, nil, nil, nil).Spec,
					}, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodGet,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft",
			nil,
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineDraftResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "acme-users-sync", body.Draft.PipelineSlug)
		require.Equal(t, api.ProposalOpen, body.Draft.DraftState)
		require.Equal(t, "draft-spec-hash", body.Draft.DraftSpecHash)
		require.Equal(t, "https://github.com/acme/pipelines.git", body.Draft.Repository)
		require.Equal(t, "github", string(*body.Draft.ProposalProvider))
		require.Equal(t, "open", string(*body.Draft.ProposalState))
		require.True(t, body.Draft.ProposalIsStale)
		require.Equal(t, now, *body.Draft.ProposalUpdatedAt)
	})

	t.Run("GetPipelineDraftReturnsNotFoundWhenDraftMissing", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				GetDraftFn: func(
					_ context.Context,
					_, _, _, _ slug.Slug,
				) (*pipelinesvc.PipelineDraftDetail, error) {
					return nil, pipelinesvc.ErrPipelineDraftNotFound
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodGet,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft",
			nil,
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("PublishPipelineDraftReturnsPipeline", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				PublishDraftFn: func(
					_ context.Context,
					orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
					_ model.EventActor,
					expectedSpecHash *string,
					force bool,
				) (*pipelinesvc.PipelineDetail, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)
					require.Nil(t, expectedSpecHash)
					require.False(t, force)
					detail := testPipelineDetail(now, nil, nil, nil, nil)
					detail.Summary.HasDraft = false
					detail.Draft = nil
					return detail, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft:publish",
			map[string]any{},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.Pipeline
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "acme-users-sync", body.Slug)
		require.False(t, body.HasDraft)
	})

	t.Run("PublishPipelineDraftReturnsStructuredConflict", func(t *testing.T) {
		t.Parallel()

		expectedSpecHash := "stale-spec-hash"
		currentSpecHash := "current-spec-hash"
		draftBaseSpecHash := "draft-base-spec-hash"
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				PublishDraftFn: func(
					_ context.Context,
					_, _, pipelineSlug, _ slug.Slug,
					_ model.EventActor,
					receivedExpectedSpecHash *string,
					receivedForce bool,
				) (*pipelinesvc.PipelineDetail, error) {
					require.Equal(t, &expectedSpecHash, receivedExpectedSpecHash)
					require.False(t, receivedForce)
					return nil, &pipelinesvc.PipelineDraftConflictError{
						PipelineSlug:         pipelineSlug,
						ExpectedSpecHash:     &expectedSpecHash,
						CurrentSpecHash:      &currentSpecHash,
						DraftBasedOnSpecHash: &draftBaseSpecHash,
						DraftPreserved:       true,
						CanOverwrite:         true,
					}
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft:publish",
			map[string]any{
				"expected_spec_hash": expectedSpecHash,
				"force":              false,
			},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusConflict, resp.StatusCode)
		var body api.PipelineDraftConflictResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "PIPELINE_DRAFT_CONFLICT", body.Error.Code)
		require.Equal(t, "acme-users-sync", body.PipelineSlug)
		require.Equal(t, expectedSpecHash, *body.ExpectedSpecHash)
		require.Equal(t, currentSpecHash, *body.CurrentSpecHash)
		require.Equal(t, draftBaseSpecHash, *body.DraftBasedOnSpecHash)
		require.True(t, body.DraftPreserved)
		require.True(t, body.CanOverwrite)
	})

	t.Run("RefreshPipelineDraftProposalQueuesExactProviderRead", func(t *testing.T) {
		t.Parallel()
		now := time.Now().UTC()
		app, store := newTestApp(t, testServerConfig{pipelinesService: &fakePipelinesService{
			RefreshDraftProposalFn: func(
				_ context.Context, orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug, _ model.EventActor,
			) (*pipelinesvc.PipelineDetail, error) {
				require.Equal(t, slug.Slug("acme"), orgSlug)
				require.Equal(t, slug.Slug("default"), projectSlug)
				require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
				require.Equal(t, slug.Slug("test-user"), memberSlug)
				return testPipelineDetail(now, nil, nil, nil, nil), nil
			},
		}})
		userSlug := must.Slug("test-user")
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft:refresh-proposal", nil)
		require.NoError(t, err)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		responseBody, readErr := io.ReadAll(resp.Body)
		require.NoError(t, readErr)
		require.Equalf(t, http.StatusOK, resp.StatusCode, "response: %s", responseBody)
	})

	t.Run("DeletePipelineDraftReturnsNoContent", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				DeleteDraftFn: func(
					_ context.Context,
					orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
					_ model.EventActor,
				) error {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)
					return nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodDelete,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft",
			nil,
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("SavePipelineDraftReturnsDraftForGitManagedPipeline", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				SaveDraftFn: func(
					_ context.Context,
					orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
					actor model.EventActor,
					pipeline *pipelinespec.Pipeline,
					_ *string,
				) (*pipelinesvc.PipelineDraftDetail, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)
					require.Equal(t, "Updated Pipeline", pipeline.Metadata.Name)
					return &pipelinesvc.PipelineDraftDetail{
						PipelineSlug:          pipelineSlug,
						DraftBasedOnVersionID: int64Ptr(12),
						BasedOnSpecHash:       strPtr("spec-hash-12"),
						CreatedAt:             now,
						UpdatedAt:             now,
						Spec:                  pipeline,
					}, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPut,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft",
			map[string]any{
				"spec": map[string]any{
					"metadata": map[string]any{"name": "Updated Pipeline"},
					"steps": []map[string]any{
						{
							"kind": "query",
							"slug": "fetch-users",
							"name": "Fetch users",
							"query": map[string]any{
								"data_source_ref": "acme-postgres",
								"operation_type":  "sql",
								"operation_ref":   "list-active-users",
							},
						},
					},
				},
			},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineDraftResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "acme-users-sync", body.Draft.PipelineSlug)
		require.NotNil(t, body.Draft.BasedOnSpecHash)
		require.Equal(t, "spec-hash-12", *body.Draft.BasedOnSpecHash)
	})

	t.Run("GetPipelineDraftReturnsDraftForGitManagedPipeline", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				GetDraftFn: func(
					_ context.Context,
					orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
				) (*pipelinesvc.PipelineDraftDetail, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)
					return &pipelinesvc.PipelineDraftDetail{
						PipelineSlug:          pipelineSlug,
						DraftBasedOnVersionID: int64Ptr(12),
						BasedOnSpecHash:       strPtr("spec-hash-12"),
						CreatedAt:             now,
						UpdatedAt:             now,
						Spec:                  testPipelineDetail(now, nil, nil, nil, nil).Spec,
					}, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodGet,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft",
			nil,
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineDraftResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "acme-users-sync", body.Draft.PipelineSlug)
	})

	t.Run("DeletePipelineDraftReturnsNoContentForGitManagedPipeline", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				DeleteDraftFn: func(
					_ context.Context,
					orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
					_ model.EventActor,
				) error {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)
					return nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodDelete,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft",
			nil,
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("PublishPipelineDraftReturnsPipelineForGitManagedPipeline", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				PublishDraftFn: func(
					_ context.Context,
					orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
					_ model.EventActor,
					_ *string,
					_ bool,
				) (*pipelinesvc.PipelineDetail, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.Equal(t, slug.Slug("test-user"), memberSlug)

					detail := testPipelineDetail(now, nil, nil, nil, nil)
					detail.Summary.HasDraft = true
					detail.Spec.Metadata.Name = "Deployed Pipeline"
					detail.Draft = &pipelinesvc.PipelineDraftDetail{
						PipelineSlug:    pipelineSlug,
						BasedOnSpecHash: strPtr("spec-hash-12"),
						CreatedAt:       now,
						UpdatedAt:       now,
						Spec: func() *pipelinespec.Pipeline {
							draft := testPipelineDetail(now, nil, nil, nil, nil).Spec
							draft.Metadata.Name = "Draft Pipeline"
							return draft
						}(),
					}
					return detail, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft:publish",
			map[string]any{},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.Pipeline
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.True(t, body.HasDraft)
		require.Equal(t, "Deployed Pipeline", body.Spec.Metadata.Name)
		require.NotNil(t, body.Draft)
		require.Equal(t, "Draft Pipeline", body.Draft.Spec.Metadata.Name)
	})

	t.Run("PublishPipelineDraftReturnsNotFoundWhenDraftMissing", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				PublishDraftFn: func(
					_ context.Context,
					_, _, _, _ slug.Slug,
					_ model.EventActor,
					_ *string,
					_ bool,
				) (*pipelinesvc.PipelineDetail, error) {
					return nil, pipelinesvc.ErrPipelineDraftNotFound
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft:publish",
			map[string]any{},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("DeletePipelineDraftReturnsNotFoundWhenDraftMissing", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				DeleteDraftFn: func(_ context.Context, _, _, _, _ slug.Slug, _ model.EventActor) error {
					return pipelinesvc.ErrPipelineDraftNotFound
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodDelete,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft",
			nil,
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("SavePipelineDraftReturnsForbiddenWhenUnauthorized", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			authService: fakeAuthService{
				CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
					return false, nil
				},
			},
			pipelinesService: &fakePipelinesService{},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPut,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft",
			map[string]any{
				"spec": map[string]any{
					"metadata": map[string]any{"name": "Updated Pipeline"},
					"steps":    []map[string]any{},
				},
			},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("SavePipelineDraftRejectsOrganizationAPIKeyPrincipal", func(t *testing.T) {
		t.Parallel()

		app, _ := newTestApp(t, testServerConfig{
			middleware: []fiber.Handler{
				func(c fiber.Ctx) error {
					reqctx.SetAPIKeyPrincipal(c, reqctx.APIKeyPrincipal{
						Slug:    "automation-key",
						OrgSlug: "acme",
						Role:    "admin",
					})
					return c.Next()
				},
			},
			pipelinesService: &fakePipelinesService{},
		})

		req := jsonRequest(
			t,
			http.MethodPut,
			"/organizations/acme/projects/default/pipelines/acme-users-sync/draft",
			map[string]any{
				"spec": map[string]any{
					"metadata": map[string]any{"name": "Updated Pipeline"},
					"steps":    []map[string]any{},
				},
			},
		)
		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		assertErrorResponse(t, resp, apierror.UserPrincipalRequired())
	})
}

func TestServer_ExecutePipeline(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsAcceptedQueuedRunRecord", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				StartRunFn: func(
					_ context.Context,
					orgSlug slug.Slug,
					projectSlug slug.Slug,
					environmentSlug slug.Slug,
					pipelineSlug slug.Slug,
					runSlug *slug.Slug,
					_ *slug.Slug,
					_ model.EventActor,
					source pipelinerun.RunSourceSummary,
					runtimeInput map[string]any,
				) (*pipelinesvc.PipelineRunDetail, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("prod"), environmentSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.NotNil(t, runSlug)
					require.Equal(t, slug.Slug("run-001"), *runSlug)
					require.Equal(t, model.PipelineRunSourceTypeManual, source.Type)
					require.Equal(t, "Manual run", source.Summary)
					require.Equal(t, model.PipelineRunSourceEntrypointUI, source.Entrypoint)
					require.Equal(t, model.EventActorTypeUser, source.ActorType)
					require.NotNil(t, source.ActorSlug)
					require.Equal(t, slug.Slug("test-user"), *source.ActorSlug)
					require.Nil(t, source.RequestID)
					require.Nil(t, source.TraceID)
					require.Equal(t, []string{"input.customer_id", "input.dry_run", "run_slug"}, source.ParameterKeys)
					require.Nil(t, source.SourceEventSlug)
					require.Equal(t, map[string]any{
						"customer_id": "cus_123",
						"dry_run":     true,
					}, runtimeInput)

					now := time.Now().UTC()
					return &pipelinesvc.PipelineRunDetail{
						Metadata: pipelinesvc.PipelineRunMetadata{
							PipelineVersionID:       12,
							EnvironmentSlug:         "prod",
							PipelineVersionSpecHash: "spec-hash-12",
							PipelineSlug:            "acme-users-sync",
							RunSlug:                 "run-001",
							Status:                  "queued",
							StartedAt:               now,
							CreatedAt:               now,
						},
						Timeline: pipelinerun.RunTimeline{
							RunSlug:         "run-001",
							PipelineSlug:    slug.Slug("acme-users-sync"),
							EnvironmentSlug: slug.Slug("prod"),
							Status:          "queued",
							Events: []pipelinerun.TimelineEvent{{
								At:     now,
								Type:   "run_queued",
								Status: "queued",
							}},
							Snapshots: []pipelinerun.StepSnapshot{},
						},
					}, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipelines/acme-users-sync:execute",
			map[string]any{
				"run_slug": "run-001",
				"input": map[string]any{
					"dry_run":     true,
					"customer_id": "cus_123",
				},
			},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusAccepted, resp.StatusCode)
		var body api.PipelineRunRecord
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, api.Slug("prod"), body.Metadata.EnvironmentSlug)
		require.Equal(t, "run-001", body.Metadata.RunSlug)
		require.Equal(t, "spec-hash-12", body.Metadata.PipelineVersionSpecHash)
		require.Equal(t, api.PipelineRunMetadataStatus("queued"), body.Metadata.Status)
	})

	t.Run("ExecutesAPIKeyRunWithAPISourceContext", func(t *testing.T) {
		t.Parallel()

		requestID := "req-execute-api-key-001"
		traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
		require.NoError(t, err)
		spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
		require.NoError(t, err)
		spanContext := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    traceID,
			SpanID:     spanID,
			TraceFlags: trace.FlagsSampled,
		})
		apiKeySlug := slug.Slug("automation-key")
		apiKeyRoleSlug := slug.Slug("pipeline-runner")
		sourceEventSlug := slug.Slug("evt-pipeline-run-execute-api-001")

		app, _ := newTestApp(t, testServerConfig{
			authService: fakeAuthService{
				CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, apiKeySlug, input.APIKeySlug)
					require.Equal(t, apiKeyRoleSlug, input.APIKeyRoleSlug)
					require.NotNil(t, input.OrgSlug)
					require.Equal(t, slug.Slug("acme"), *input.OrgSlug)
					require.Equal(t, model.Execute, input.Action)
					require.Equal(t, model.ResourcePipelines, input.Resource)
					return true, nil
				},
			},
			middleware: []fiber.Handler{
				func(c fiber.Ctx) error {
					reqctx.SetAPIKeyPrincipal(c, reqctx.APIKeyPrincipal{
						Slug:    apiKeySlug,
						OrgSlug: slug.Slug("acme"),
						Role:    apiKeyRoleSlug,
					})
					ctx := reqctx.WithRequestID(c.Context(), requestID)
					c.SetContext(trace.ContextWithSpanContext(ctx, spanContext))
					return c.Next()
				},
			},
			pipelinesService: &fakePipelinesService{
				StartRunFn: func(
					_ context.Context,
					orgSlug slug.Slug,
					projectSlug slug.Slug,
					environmentSlug slug.Slug,
					pipelineSlug slug.Slug,
					runSlug *slug.Slug,
					_ *slug.Slug,
					actor model.EventActor,
					source pipelinerun.RunSourceSummary,
					runtimeInput map[string]any,
				) (*pipelinesvc.PipelineRunDetail, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("prod"), environmentSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), pipelineSlug)
					require.NotNil(t, runSlug)
					require.Equal(t, slug.Slug("api-run-001"), *runSlug)
					require.Equal(t, model.EventActorTypeAPIKey, actor.Type)
					require.NotNil(t, actor.Slug)
					require.Equal(t, apiKeySlug, *actor.Slug)
					require.Equal(t, model.PipelineRunSourceTypeAPI, source.Type)
					require.Equal(t, "API key run", source.Summary)
					require.Equal(t, model.PipelineRunSourceEntrypointAPI, source.Entrypoint)
					require.Equal(t, model.EventActorTypeAPIKey, source.ActorType)
					require.NotNil(t, source.ActorSlug)
					require.Equal(t, apiKeySlug, *source.ActorSlug)
					require.NotNil(t, source.RequestID)
					require.Equal(t, requestID, *source.RequestID)
					require.NotNil(t, source.TraceID)
					require.Equal(t, traceID.String(), *source.TraceID)
					require.Equal(t, []string{"input.customer_id", "input.dry_run", "run_slug"}, source.ParameterKeys)
					require.Equal(t, map[string]any{
						"customer_id": "cus_raw_value_must_not_persist",
						"dry_run":     true,
					}, runtimeInput)
					require.NotContains(t, source.ParameterKeys, "cus_raw_value_must_not_persist")

					now := time.Now().UTC()
					source.EnvironmentSlug = slug.Slug("prod")
					source.SourceEventSlug = &sourceEventSlug
					return &pipelinesvc.PipelineRunDetail{
						Metadata: pipelinesvc.PipelineRunMetadata{
							PipelineVersionID:       12,
							EnvironmentSlug:         "prod",
							PipelineVersionSpecHash: "spec-hash-12",
							PipelineSlug:            "acme-users-sync",
							RunSlug:                 "api-run-001",
							Source:                  source,
							Status:                  "queued",
							StartedAt:               now,
							CreatedAt:               now,
						},
						Timeline: pipelinerun.RunTimeline{
							RunSlug:         "api-run-001",
							PipelineSlug:    slug.Slug("acme-users-sync"),
							EnvironmentSlug: slug.Slug("prod"),
							Status:          "queued",
						},
					}, nil
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipelines/acme-users-sync:execute",
			map[string]any{
				"run_slug": "api-run-001",
				"input": map[string]any{
					"customer_id": "cus_raw_value_must_not_persist",
					"dry_run":     true,
				},
			},
		)
		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusAccepted, resp.StatusCode)
		var body api.PipelineRunRecord
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.NotNil(t, body.Metadata.Source)
		apiSource, err := body.Metadata.Source.AsPipelineRunAPISourceSummary()
		require.NoError(t, err)
		require.Equal(t, api.PipelineRunAPISourceSummaryType("api"), apiSource.Type)
		require.Equal(t, "API key run", apiSource.Summary)
		require.Equal(t, api.PipelineRunAPISourceSummaryEntrypoint("api"), apiSource.Entrypoint)
		require.Equal(t, api.EventActorType("api_key"), apiSource.ActorType)
		require.NotNil(t, apiSource.ActorSlug)
		require.Equal(t, api.NullableSlug(apiKeySlug), *apiSource.ActorSlug)
		require.NotNil(t, apiSource.RequestId)
		require.Equal(t, requestID, *apiSource.RequestId)
		require.NotNil(t, apiSource.TraceId)
		require.Equal(t, traceID.String(), *apiSource.TraceId)
		require.NotNil(t, apiSource.SourceEventSlug)
		require.Equal(t, api.NullableSlug(sourceEventSlug), *apiSource.SourceEventSlug)
		require.Equal(t, []string{"input.customer_id", "input.dry_run", "run_slug"}, apiSource.ParameterKeys)
		sourceJSON, err := json.Marshal(body.Metadata.Source)
		require.NoError(t, err)
		require.NotContains(t, string(sourceJSON), "cus_raw_value_must_not_persist")
	})

	t.Run("CapturesRequestAndTraceContext", func(t *testing.T) {
		t.Parallel()

		requestID := "req-execute-pipeline-001"
		traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
		require.NoError(t, err)
		spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
		require.NoError(t, err)
		spanContext := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    traceID,
			SpanID:     spanID,
			TraceFlags: trace.FlagsSampled,
		})

		app, store := newTestApp(t, testServerConfig{
			middleware: []fiber.Handler{
				func(c fiber.Ctx) error {
					ctx := reqctx.WithRequestID(c.Context(), requestID)
					c.SetContext(trace.ContextWithSpanContext(ctx, spanContext))
					return c.Next()
				},
			},
			pipelinesService: &fakePipelinesService{
				StartRunFn: func(
					_ context.Context,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
					_ *slug.Slug,
					_ *slug.Slug,
					_ model.EventActor,
					source pipelinerun.RunSourceSummary,
					_ map[string]any,
				) (*pipelinesvc.PipelineRunDetail, error) {
					require.NotNil(t, source.RequestID)
					require.Equal(t, requestID, *source.RequestID)
					require.NotNil(t, source.TraceID)
					require.Equal(t, traceID.String(), *source.TraceID)

					now := time.Now().UTC()
					return &pipelinesvc.PipelineRunDetail{
						Metadata: pipelinesvc.PipelineRunMetadata{
							PipelineVersionID:       12,
							EnvironmentSlug:         "prod",
							PipelineVersionSpecHash: "spec-hash-12",
							PipelineSlug:            "acme-users-sync",
							RunSlug:                 "run-001",
							Status:                  "queued",
							StartedAt:               now,
							CreatedAt:               now,
						},
						Timeline: pipelinerun.RunTimeline{
							RunSlug:         "run-001",
							PipelineSlug:    slug.Slug("acme-users-sync"),
							EnvironmentSlug: slug.Slug("prod"),
							Status:          "queued",
							Events: []pipelinerun.TimelineEvent{{
								At:     now,
								Type:   "run_queued",
								Status: "queued",
							}},
							Snapshots: []pipelinerun.StepSnapshot{},
						},
					}, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipelines/acme-users-sync:execute",
			map[string]any{"run_slug": "run-001"},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusAccepted, resp.StatusCode)
	})

	t.Run("ReturnsValidationFailedForInvalidRunSlug", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipelines/acme-users-sync:execute",
			map[string]any{"run_slug": "INVALID RUN SLUG"},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("ReturnsNotFoundWhenPipelineMissing", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				StartRunFn: func(
					_ context.Context,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
					_ *slug.Slug,
					_ *slug.Slug,
					_ model.EventActor,
					_ pipelinerun.RunSourceSummary,
					_ map[string]any,
				) (*pipelinesvc.PipelineRunDetail, error) {
					return nil, pipelinesvc.ErrPipelineNotFound
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipelines/missing-pipeline:execute",
			map[string]any{},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("ReturnsForbiddenWhenPipelineRunLimitReached", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			admissionService: &testfakes.AdmissionService{
				CheckRunAccessFn: func(context.Context, slug.Slug) error {
					return xerror.Forbidden(xerror.Organization)
				},
			},
			pipelinesService: &fakePipelinesService{
				StartRunFn: func(
					context.Context,
					slug.Slug, slug.Slug, slug.Slug, slug.Slug,
					*slug.Slug, *slug.Slug,
					model.EventActor,
					pipelinerun.RunSourceSummary,
					map[string]any,
				) (*pipelinesvc.PipelineRunDetail, error) {
					t.Fatal("run must not start after entitlement denial")
					return nil, xerror.Unexpected()
				},
			},
		})

		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipelines/acme-users-sync:execute",
			map[string]any{},
		)
		resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("ReturnsForbiddenWhenUnauthorized", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			authService: fakeAuthService{
				CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
					return false, nil
				},
			},
			pipelinesService: &fakePipelinesService{},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodPost,
			"/organizations/acme/projects/default/environments/prod/pipelines/acme-users-sync:execute",
			map[string]any{},
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

func TestToAPIPipelineRunSourceSummary(t *testing.T) {
	t.Parallel()

	actorSlug := slug.Slug("alice")
	sourceEventSlug := slug.Slug("evt-pipeline-run-001")
	requestID := "req-001"
	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	scheduledFireAt := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	actualFireAt := time.Date(2026, time.June, 9, 7, 0, 2, 0, time.UTC)

	tests := []struct {
		name    string
		source  pipelinerun.RunSourceSummary
		wantNil bool
	}{
		{
			name:    "OmitsEmptySource",
			source:  pipelinerun.RunSourceSummary{},
			wantNil: true,
		},
		{
			name: "OmitsPartiallyPopulatedSource",
			source: pipelinerun.RunSourceSummary{
				Type:       model.PipelineRunSourceTypeManual,
				Summary:    "Manual run",
				Entrypoint: model.PipelineRunSourceEntrypointUI,
			},
			wantNil: true,
		},
		{
			name: "OmitsUnsupportedSourceType",
			source: pipelinerun.RunSourceSummary{
				Type:       model.PipelineRunSourceType(99),
				Summary:    "Manual run",
				Entrypoint: model.PipelineRunSourceEntrypointUI,
				ActorType:  model.EventActorTypeUser,
			},
			wantNil: true,
		},
		{
			name: "OmitsUnsupportedSourceEntrypoint",
			source: pipelinerun.RunSourceSummary{
				Type:       model.PipelineRunSourceTypeManual,
				Summary:    "Manual run",
				Entrypoint: model.PipelineRunSourceEntrypoint(99),
				ActorType:  model.EventActorTypeUser,
			},
			wantNil: true,
		},
		{
			name: "OmitsUnsupportedActorType",
			source: pipelinerun.RunSourceSummary{
				Type:       model.PipelineRunSourceTypeManual,
				Summary:    "Manual run",
				Entrypoint: model.PipelineRunSourceEntrypointUI,
				ActorType:  model.EventActorType(99),
			},
			wantNil: true,
		},
		{
			name: "MapsPopulatedSource",
			source: pipelinerun.RunSourceSummary{
				Type:            model.PipelineRunSourceTypeAPI,
				Summary:         "API run",
				Entrypoint:      model.PipelineRunSourceEntrypointAPI,
				ActorType:       model.EventActorTypeAPIKey,
				ActorSlug:       &actorSlug,
				RequestID:       &requestID,
				TraceID:         &traceID,
				ParameterKeys:   []string{"run_slug", "correlation_id"},
				SourceEventSlug: &sourceEventSlug,
			},
		},
		{
			name: "PreservesAPISourceWithInvalidScheduleMetadata",
			source: pipelinerun.RunSourceSummary{
				Type:            model.PipelineRunSourceTypeAPI,
				Summary:         "API run",
				Entrypoint:      model.PipelineRunSourceEntrypointAPI,
				ActorType:       model.EventActorTypeAPIKey,
				ActorSlug:       &actorSlug,
				RequestID:       &requestID,
				TraceID:         &traceID,
				ParameterKeys:   []string{"run_slug", "correlation_id"},
				SourceEventSlug: &sourceEventSlug,
				Schedule:        &pipelinerun.RunSourceScheduleSummary{},
			},
		},
		{
			name: "MapsScheduledSource",
			source: pipelinerun.RunSourceSummary{
				Type:          model.PipelineRunSourceTypeSchedule,
				Summary:       "Scheduled run from trigger nightly-users",
				Entrypoint:    model.PipelineRunSourceEntrypointScheduler,
				ActorType:     model.EventActorTypeSystem,
				ParameterKeys: []string{"customer_id", "dry_run"},
				Schedule: &pipelinerun.RunSourceScheduleSummary{
					TriggerSlug:     must.Slug("nightly-users"),
					TriggerRevision: 4,
					Cron:            "0 9 * * *",
					Timezone:        "Europe/Warsaw",
					ScheduledFireAt: scheduledFireAt,
					ActualFireAt:    actualFireAt,
					MisfireResult:   model.PipelineTriggerScheduleMisfireResultOnTime,
				},
				SourceEventSlug: &sourceEventSlug,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := toAPIPipelineRunSourceSummary(tt.source)
			if tt.wantNil {
				require.Nil(t, got)
				return
			}

			require.NotNil(t, got)
			switch tt.source.Type {
			case model.PipelineRunSourceTypeAPI:
				apiSource, err := got.AsPipelineRunAPISourceSummary()
				require.NoError(t, err)
				require.Equal(t, api.PipelineRunAPISourceSummaryTypeApi, apiSource.Type)
				require.Equal(t, tt.source.Summary, apiSource.Summary)
				require.Equal(t, api.PipelineRunAPISourceSummaryEntrypointApi, apiSource.Entrypoint)
				require.Equal(t, api.EventActorType(tt.source.ActorType.String()), apiSource.ActorType)
				require.NotNil(t, apiSource.ActorSlug)
				require.Equal(t, tt.source.ActorSlug.String(), *apiSource.ActorSlug)
				require.NotNil(t, apiSource.RequestId)
				require.Equal(t, *tt.source.RequestID, *apiSource.RequestId)
				require.NotNil(t, apiSource.TraceId)
				require.Equal(t, *tt.source.TraceID, *apiSource.TraceId)
				require.Equal(t, tt.source.ParameterKeys, apiSource.ParameterKeys)
				require.NotNil(t, apiSource.SourceEventSlug)
				require.Equal(t, api.NullableSlug("evt-pipeline-run-001"), *apiSource.SourceEventSlug)
			case model.PipelineRunSourceTypeSchedule:
				scheduleSource, err := got.AsPipelineRunScheduleSourceSummary()
				require.NoError(t, err)
				require.Equal(t, api.PipelineRunScheduleSourceSummaryTypeSchedule, scheduleSource.Type)
				require.Equal(t, tt.source.Summary, scheduleSource.Summary)
				require.Equal(t, api.PipelineRunScheduleSourceSummaryEntrypointScheduler, scheduleSource.Entrypoint)
				require.Equal(t, api.EventActorType(tt.source.ActorType.String()), scheduleSource.ActorType)
				require.Nil(t, scheduleSource.ActorSlug)
				require.Nil(t, scheduleSource.RequestId)
				require.Nil(t, scheduleSource.TraceId)
				require.Equal(t, tt.source.ParameterKeys, scheduleSource.ParameterKeys)
				require.NotNil(t, scheduleSource.SourceEventSlug)
				require.Equal(t, api.NullableSlug("evt-pipeline-run-001"), *scheduleSource.SourceEventSlug)
				require.Equal(t, tt.source.Schedule.TriggerSlug.String(), scheduleSource.Schedule.TriggerSlug)
				require.Equal(t, tt.source.Schedule.TriggerRevision, scheduleSource.Schedule.TriggerRevision)
				require.Equal(t, tt.source.Schedule.Cron, scheduleSource.Schedule.Cron)
				require.Equal(t, tt.source.Schedule.Timezone, scheduleSource.Schedule.Timezone)
				require.True(t, scheduleSource.Schedule.ScheduledFireAt.Equal(tt.source.Schedule.ScheduledFireAt))
				require.True(t, scheduleSource.Schedule.ActualFireAt.Equal(tt.source.Schedule.ActualFireAt))
				require.Equal(
					t,
					api.PipelineRunScheduleMisfireResult(tt.source.Schedule.MisfireResult.String()),
					scheduleSource.Schedule.MisfireResult,
				)
			default:
				require.Failf(t, "unexpected source type", "type=%s", tt.source.Type)
			}
		})
	}
}

func TestToAPICompletionRunSourceSummary(t *testing.T) {
	source := pipelinerun.RunSourceSummary{
		Type:       model.PipelineRunSourceTypeCompletion,
		Entrypoint: model.PipelineRunSourceEntrypointScheduler, ActorType: model.EventActorTypeSystem,
		Summary: "Completion-triggered run", AttemptNumber: 1, RootRunSlug: "completion-42", ParameterKeys: []string{},
	}
	got := toAPIPipelineRunSourceSummary(source)
	require.NotNil(t, got)
	completion, err := got.AsPipelineRunCompletionSourceSummary()
	require.NoError(t, err)
	require.Equal(t, api.PipelineRunCompletionSourceSummaryTypeCompletion, completion.Type)
	require.Equal(t, source.Summary, completion.Summary)
	require.Equal(t, "completion-42", completion.RootRunSlug)
	require.Equal(t, 1, completion.AttemptNumber)
	source.Entrypoint = model.PipelineRunSourceEntrypointAPI
	require.Nil(t, toAPIPipelineRunSourceSummary(source))
}

func TestServer_ListPipelineRuns(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsRunList", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				ListRunsFn: func(
					_ context.Context,
					orgSlug slug.Slug,
					projectSlug slug.Slug,
					environmentSlug slug.Slug,
					filters *pipelinesvc.RunFilters,
					req model.ListRequest,
				) (model.ListResponse[*pipelinesvc.PipelineRunMetadata], error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("prod"), environmentSlug)
					require.NotNil(t, filters)
					require.NotNil(t, filters.Status)
					require.Equal(t, "failed", *filters.Status)
					require.NotNil(t, filters.PipelineSlug)
					require.Equal(t, slug.Slug("acme-users-sync"), *filters.PipelineSlug)
					require.Equal(t, model.ListSortCreatedAt, req.SortBy)

					now := time.Now().UTC()
					return model.ListResponse[*pipelinesvc.PipelineRunMetadata]{
						Items: []*pipelinesvc.PipelineRunMetadata{
							{
								PipelineVersionID:       12,
								EnvironmentSlug:         "prod",
								PipelineVersionSpecHash: "spec-hash-12",
								PipelineSlug:            "acme-users-sync",
								RunSlug:                 "run-001",
								Status:                  "failed",
								DurationMs:              120,
								StepCount:               3,
								StepSlug:                "fetch-users",
								StartedAt:               now,
								CreatedAt:               now,
							},
						},
						HasMore: false,
					}, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodGet,
			"/organizations/acme/projects/default/environments/prod/runs?status=failed&pipeline_slug=acme-users-sync&sort_by=run_created_at",
			nil,
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineRunListResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Len(t, body.Runs, 1)
		require.Equal(t, api.Slug("prod"), body.Runs[0].EnvironmentSlug)
		require.Equal(t, "run-001", body.Runs[0].RunSlug)
		require.Equal(t, "spec-hash-12", body.Runs[0].PipelineVersionSpecHash)
		require.Equal(t, api.PipelineRunMetadataStatus("failed"), body.Runs[0].Status)
	})

	t.Run("ReturnsIncrementalSummaryAndFiltersBackfills", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				ListRunsFn: func(
					_ context.Context,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
					filters *pipelinesvc.RunFilters,
					_ model.ListRequest,
				) (model.ListResponse[*pipelinesvc.PipelineRunMetadata], error) {
					require.NotNil(t, filters)
					require.NotNil(t, filters.Mode)
					require.Equal(t, "backfill", *filters.Mode)
					require.NotNil(t, filters.BackfillSeriesSlug)
					require.Equal(t, "series-001", *filters.BackfillSeriesSlug)

					now := time.Date(2026, time.May, 24, 11, 0, 0, 0, time.UTC)
					return model.ListResponse[*pipelinesvc.PipelineRunMetadata]{
						Items: []*pipelinesvc.PipelineRunMetadata{{
							PipelineVersionID:       12,
							PipelineVersionSpecHash: "spec-hash-12",
							PipelineSlug:            "users-sync",
							RunSlug:                 "run-backfill-001",
							Status:                  "succeeded",
							DurationMs:              120,
							StepCount:               2,
							StepSlug:                "persist-users",
							StartedAt:               now,
							CreatedAt:               now,
							Incremental: pipelinerun.IncrementalRunSummary{
								Mode:               "backfill",
								CheckpointStatus:   "skipped",
								SourceStepSlug:     "fetch-users",
								ReplaySafetyStatus: "safe",
								BackfillSeriesSlug: "series-001",
								BackfillChunkIndex: 1,
								BackfillChunkCount: 10,
							},
						}},
					}, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodGet,
			"/organizations/acme/projects/default/environments/prod/runs?mode=backfill&backfill_series_slug=series-001",
			nil,
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineRunListResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Len(t, body.Runs, 1)
		require.Equal(t, "backfill", string(body.Runs[0].Incremental.Mode))
		require.Equal(t, "skipped", string(body.Runs[0].Incremental.CheckpointStatus))
		require.Equal(t, "series-001", body.Runs[0].Incremental.BackfillSeriesSlug)
		require.Equal(t, 1, body.Runs[0].Incremental.BackfillChunkIndex)
		require.Equal(t, 10, body.Runs[0].Incremental.BackfillChunkCount)
	})

	t.Run("ReturnsValidationFailedForInvalidModeFilter", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(t, http.MethodGet, "/organizations/acme/projects/default/environments/prod/runs?mode=realtime", nil)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("ReturnsValidationFailedForInvalidPipelineSlugFilter", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodGet,
			"/organizations/acme/projects/default/environments/prod/runs?pipeline_slug=invalid_slug",
			nil,
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("ReturnsForbiddenWhenUnauthorized", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			authService: fakeAuthService{
				CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
					return false, nil
				},
			},
			pipelinesService: &fakePipelinesService{},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(t, http.MethodGet, "/organizations/acme/projects/default/environments/prod/runs", nil)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

func TestServer_GetPipelineRun(t *testing.T) {
	t.Parallel()

	summary := "HubSpot request failed: query invalid input"

	t.Run("ReturnsRunDetails", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				GetRunFn: func(
					_ context.Context,
					orgSlug slug.Slug,
					projectSlug slug.Slug,
					environmentSlug slug.Slug,
					runSlug string,
				) (*pipelinesvc.PipelineRunDetail, error) {
					require.Equal(t, slug.Slug("acme"), orgSlug)
					require.Equal(t, slug.Slug("default"), projectSlug)
					require.Equal(t, slug.Slug("prod"), environmentSlug)
					require.Equal(t, "run-001", runSlug)

					now := time.Now().UTC()
					return &pipelinesvc.PipelineRunDetail{
						Metadata: pipelinesvc.PipelineRunMetadata{
							PipelineVersionID:       12,
							EnvironmentSlug:         "prod",
							PipelineVersionSpecHash: "spec-hash-12",
							PipelineSlug:            "acme-users-sync",
							RunSlug:                 "run-001",
							Status:                  "failed",
							DurationMs:              135,
							StepCount:               3,
							ErrorSummary:            &summary,
							StepSlug:                "fetch-users",
							StartedAt:               now,
							CreatedAt:               now,
						},
						Timeline: pipelinerun.RunTimeline{
							RunSlug:         "run-001",
							PipelineSlug:    slug.Slug("acme-users-sync"),
							EnvironmentSlug: slug.Slug("prod"),
							Status:          "failed",
							Events: []pipelinerun.TimelineEvent{{
								At:     now,
								Type:   "run_started",
								Status: "running",
							}},
							Snapshots: []pipelinerun.StepSnapshot{{
								Step: slug.Slug("fetch-users"),
								Kind: "query",
								Input: pipelinerun.StepInputSnapshot{
									OrganizationSlug: slug.Slug("acme"),
									EnvironmentSlug:  slug.Slug("prod"),
									DataSourceSlug:   slug.Slug("acme-postgres"),
									OperationType:    "sql",
									UsesInline:       true,
									ParameterKeys:    []string{},
								},
							}},
							Failure: &pipelinerun.FailureContext{
								Step:  slug.Slug("fetch-users"),
								Error: summary,
								At:    now,
							},
						},
					}, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(
			t,
			http.MethodGet,
			"/organizations/acme/projects/default/environments/prod/runs/run-001",
			nil,
		)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineRunRecord
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, api.Slug("prod"), body.Metadata.EnvironmentSlug)
		require.Equal(t, "run-001", body.Metadata.RunSlug)
		require.Equal(t, "spec-hash-12", body.Metadata.PipelineVersionSpecHash)
		require.Equal(t, "run_started", body.Timeline.Events[0].Type)
		require.Len(t, body.Timeline.Snapshots, 1)
		require.NotNil(t, body.Timeline.Snapshots[0].Kind)
		require.Equal(t, api.PipelineStepKind("query"), *body.Timeline.Snapshots[0].Kind)
		require.NotNil(t, body.Timeline.Snapshots[0].Input.UsesInline)
		require.True(t, *body.Timeline.Snapshots[0].Input.UsesInline)
		require.NotNil(t, body.Timeline.Failure)
		require.Equal(t, summary, body.Timeline.Failure.Error)
		require.Equal(t, &summary, body.Metadata.ErrorSummary)
	})

	t.Run("ReturnsIncrementalDiagnostics", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				GetRunFn: func(
					_ context.Context,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
					runSlug string,
				) (*pipelinesvc.PipelineRunDetail, error) {
					require.Equal(t, "run-incremental-001", runSlug)
					now := time.Date(2026, time.May, 24, 11, 0, 0, 0, time.UTC)
					return &pipelinesvc.PipelineRunDetail{
						Metadata: pipelinesvc.PipelineRunMetadata{
							PipelineVersionID:       12,
							PipelineVersionSpecHash: "spec-hash-12",
							PipelineSlug:            "users-sync",
							RunSlug:                 "run-incremental-001",
							Status:                  "succeeded",
							DurationMs:              120,
							StepCount:               2,
							StepSlug:                "persist-users",
							StartedAt:               now,
							CreatedAt:               now,
							Incremental: pipelinerun.IncrementalRunSummary{
								Mode:               "incremental",
								CheckpointStatus:   "committed",
								SourceStepSlug:     "fetch-users",
								ReplaySafetyStatus: "safe",
							},
						},
						Timeline: pipelinerun.RunTimeline{
							RunSlug:      "run-incremental-001",
							PipelineSlug: slug.Slug("users-sync"),
							Status:       "succeeded",
							Events: []pipelinerun.TimelineEvent{
								{At: now, Type: "checkpoint_observed", Step: slug.Slug("fetch-users"), Status: "observed"},
								{At: now, Type: "checkpoint_committed", Step: slug.Slug("fetch-users"), Status: "committed"},
							},
						},
						IncrementalDiagnostics: pipelinerun.IncrementalRunDiagnostics{
							Sources: []pipelinerun.IncrementalSourceDiagnostic{{
								SourceStepSlug: "fetch-users",
								CheckpointKind: "watermark",
								PreviousCheckpoint: pipelinerun.IncrementalCheckpointSummary{
									Summary:         "up to 10:00",
									RawValueExposed: false,
								},
								ObservedCheckpoint: pipelinerun.IncrementalCheckpointSummary{
									Summary:         "up to 11:00",
									RawValueExposed: false,
								},
								CommitResult: pipelinerun.IncrementalCheckpointCommitResult{
									Status: "committed",
								},
							}},
						},
					}, nil
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(t, http.MethodGet, "/organizations/acme/projects/default/environments/prod/runs/run-incremental-001", nil)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body api.PipelineRunRecord
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "incremental", string(body.Metadata.Incremental.Mode))
		require.Equal(t, "committed", string(body.Metadata.Incremental.CheckpointStatus))
		require.Len(t, body.IncrementalDiagnostics.Sources, 1)
		require.Equal(t, "fetch-users", body.IncrementalDiagnostics.Sources[0].SourceStepSlug)
		require.Equal(t, "up to 10:00", body.IncrementalDiagnostics.Sources[0].PreviousCheckpoint.Summary)
		require.Equal(t, "up to 11:00", body.IncrementalDiagnostics.Sources[0].ObservedCheckpoint.Summary)
		require.Equal(t, "committed", string(body.IncrementalDiagnostics.Sources[0].CommitResult.Status))
	})

	t.Run("ReturnsValidationFailedForInvalidRunSlug", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(t, http.MethodGet, "/organizations/acme/projects/default/environments/prod/runs/run_invalid", nil)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("ReturnsNotFoundWhenRunMissing", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			pipelinesService: &fakePipelinesService{
				GetRunFn: func(
					_ context.Context,
					_ slug.Slug,
					_ slug.Slug,
					_ slug.Slug,
					_ string,
				) (*pipelinesvc.PipelineRunDetail, error) {
					return nil, pipelinesvc.ErrPipelineRunNotFound
				},
			},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(t, http.MethodGet, "/organizations/acme/projects/default/environments/prod/runs/run-missing-404", nil)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("ReturnsForbiddenWhenUnauthorized", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			authService: fakeAuthService{
				CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
					return false, nil
				},
			},
			pipelinesService: &fakePipelinesService{},
		})

		userSlug, err := slug.New("test-user")
		require.NoError(t, err)
		req := jsonRequest(t, http.MethodGet, "/organizations/acme/projects/default/environments/prod/runs/run-001", nil)
		resp := authenticatedRequest(t, app, store, userSlug, req)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

func TestServer_ListPipelineRunTriggerRetryAttempts(t *testing.T) {
	t.Parallel()

	scheduledAt := time.Date(2026, time.June, 9, 7, 1, 0, 0, time.UTC)
	enqueuedAt := scheduledAt.Add(time.Second)
	startedAt := scheduledAt.Add(2 * time.Second)
	completedAt := scheduledAt.Add(3 * time.Second)
	updatedAt := completedAt.Add(time.Second)
	decisionReason := "worker_failed"
	errorSummary := "connector timeout"
	failureKind := model.PipelineTriggerRetryFailureKindPlatform
	sourceEventSlug := must.Slug("evt-retry-run-2")

	successAttempt := model.PipelineTriggerRetryAttempt{
		AttemptSlug:     must.Slug("retry-run-001-2"),
		Status:          model.PipelineTriggerRetryAttemptStatusFailed,
		DecisionReason:  &decisionReason,
		TriggerSlug:     must.Slug("daily-users"),
		TriggerRevision: 3,
		PipelineSlug:    must.Slug("sync-users"),
		RootRunSlug:     must.Slug("run-001"),
		RetryOfRunSlug:  must.Slug("run-001"),
		RunSlug:         must.Slug("run-001-retry-2"),
		AttemptNumber:   2,
		FailureKind:     &failureKind,
		SourceEventSlug: &sourceEventSlug,
		ScheduledAt:     scheduledAt,
		EnqueuedAt:      &enqueuedAt,
		StartedAt:       &startedAt,
		CompletedAt:     &completedAt,
		ErrorSummary:    &errorSummary,
		Timestamps: model.Timestamps{
			CreatedAt: scheduledAt,
			UpdatedAt: updatedAt,
		},
	}

	tests := []struct {
		name            string
		getRunErr       error
		retryAttempts   []model.PipelineTriggerRetryAttempt
		wantStatus      int
		wantTriggerCall bool
		assertResponse  func(*testing.T, *http.Response)
	}{
		{
			name:            "ReturnsRetryAttempts",
			retryAttempts:   []model.PipelineTriggerRetryAttempt{successAttempt},
			wantStatus:      http.StatusOK,
			wantTriggerCall: true,
			assertResponse: func(t *testing.T, resp *http.Response) {
				t.Helper()

				var body api.PipelineRunTriggerRetryAttemptListResponse
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
				require.Len(t, body.RetryAttempts, 1)
				attempt := body.RetryAttempts[0]
				require.Equal(t, api.Slug("retry-run-001-2"), attempt.AttemptSlug)
				require.Equal(t, api.PipelineTriggerRetryAttemptStatusFailed, attempt.Status)
				require.Equal(t, &decisionReason, attempt.DecisionReason)
				require.Equal(t, api.Slug("daily-users"), attempt.TriggerSlug)
				require.Equal(t, int64(3), attempt.TriggerRevision)
				require.Equal(t, api.Slug("sync-users"), attempt.PipelineSlug)
				require.Equal(t, api.Slug("run-001"), attempt.RootRunSlug)
				require.Equal(t, api.Slug("run-001"), attempt.RetryOfRunSlug)
				require.NotNil(t, attempt.RunSlug)
				require.Equal(t, api.Slug("run-001-retry-2"), *attempt.RunSlug)
				require.Equal(t, 2, attempt.AttemptNumber)
				require.NotNil(t, attempt.FailureKind)
				require.Equal(t, api.PipelineTriggerRetryFailureKindPlatform, *attempt.FailureKind)
				require.NotNil(t, attempt.SourceEventSlug)
				require.Equal(t, api.NullableSlug("evt-retry-run-2"), *attempt.SourceEventSlug)
				require.Equal(t, scheduledAt, attempt.ScheduledAt)
				require.NotNil(t, attempt.EnqueuedAt)
				require.Equal(t, enqueuedAt, *attempt.EnqueuedAt)
				require.NotNil(t, attempt.StartedAt)
				require.Equal(t, startedAt, *attempt.StartedAt)
				require.NotNil(t, attempt.CompletedAt)
				require.Equal(t, completedAt, *attempt.CompletedAt)
				require.Equal(t, &errorSummary, attempt.ErrorSummary)
				require.Equal(t, updatedAt, attempt.UpdatedAt)
			},
		},
		{
			name:            "RunNotFound",
			getRunErr:       pipelinesvc.ErrPipelineRunNotFound,
			wantStatus:      http.StatusNotFound,
			wantTriggerCall: false,
			assertResponse: func(t *testing.T, resp *http.Response) {
				t.Helper()
				assertErrorResponse(t, resp, apierror.New(http.StatusNotFound, "PIPELINE_RUN_NOT_FOUND", "pipeline run not found"))
			},
		},
		{
			name:            "EmptyRetryList",
			retryAttempts:   []model.PipelineTriggerRetryAttempt{},
			wantStatus:      http.StatusOK,
			wantTriggerCall: true,
			assertResponse: func(t *testing.T, resp *http.Response) {
				t.Helper()

				var body api.PipelineRunTriggerRetryAttemptListResponse
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
				require.Empty(t, body.RetryAttempts)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			triggerCalled := false
			app, store := newTestApp(t, testServerConfig{
				pipelinesService: &fakePipelinesService{
					GetRunFn: func(
						_ context.Context,
						orgSlug slug.Slug,
						projectSlug slug.Slug,
						environmentSlug slug.Slug,
						runSlug string,
					) (*pipelinesvc.PipelineRunDetail, error) {
						require.Equal(t, slug.Slug("acme"), orgSlug)
						require.Equal(t, slug.Slug("default"), projectSlug)
						require.Equal(t, slug.Slug("prod"), environmentSlug)
						require.Equal(t, "run-001", runSlug)
						if tt.getRunErr != nil {
							return nil, tt.getRunErr
						}
						return &pipelinesvc.PipelineRunDetail{}, nil
					},
				},
				pipelineTriggersService: &fakePipelineTriggersService{
					ListPipelineRunTriggerRetryAttemptsFn: func(
						_ context.Context,
						orgSlug slug.Slug,
						projectSlug slug.Slug,
						environmentSlug slug.Slug,
						runSlug slug.Slug,
					) ([]model.PipelineTriggerRetryAttempt, error) {
						triggerCalled = true
						require.Equal(t, slug.Slug("acme"), orgSlug)
						require.Equal(t, slug.Slug("default"), projectSlug)
						require.Equal(t, slug.Slug("prod"), environmentSlug)
						require.Equal(t, slug.Slug("run-001"), runSlug)
						return tt.retryAttempts, nil
					},
				},
			})

			userSlug, err := slug.New("test-user")
			require.NoError(t, err)
			req := jsonRequest(
				t,
				http.MethodGet,
				"/organizations/acme/projects/default/environments/prod/runs/run-001/trigger-retry-attempts",
				nil,
			)
			resp := authenticatedRequest(t, app, store, userSlug, req)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, tt.wantStatus, resp.StatusCode)
			tt.assertResponse(t, resp)
			require.Equal(t, tt.wantTriggerCall, triggerCalled)
		})
	}
}

func TestToAPIPipelineRunStepOutputSnapshot(t *testing.T) {
	t.Parallel()

	t.Run("MapsRowsDetails", func(t *testing.T) {
		t.Parallel()

		rowCount := 10
		mapped := toAPIPipelineRunStepOutputSnapshot(&pipelinerun.StepOutputSnapshot{
			ResultType: "sql",
			VendorType: "postgres",
			DurationMs: 12,
			Details: &pipelinerun.StepOutputDetails{
				Kind:        pipelinerun.StepOutputDetailKindRows,
				RowCount:    &rowCount,
				ColumnNames: []string{"id", "email"},
			},
		})

		require.NotNil(t, mapped)
		rows, err := mapped.Details.AsPipelineRunRowsOutputDetails()
		require.NoError(t, err)
		require.Equal(t, 10, rows.RowCount)
		require.NotNil(t, rows.ColumnNames)
		require.Equal(t, []string{"id", "email"}, *rows.ColumnNames)

		body, err := json.Marshal(mapped)
		require.NoError(t, err)
		require.NotContains(t, string(body), `"document_count":null`)
		require.NotContains(t, string(body), `"status_code":null`)
	})

	t.Run("MapsDocumentsDetails", func(t *testing.T) {
		t.Parallel()

		documentCount := 2
		mapped := toAPIPipelineRunStepOutputSnapshot(&pipelinerun.StepOutputSnapshot{
			ResultType:    "mongodb",
			VendorType:    "mongodb",
			DurationMs:    18,
			OperationType: "find",
			Details: &pipelinerun.StepOutputDetails{
				Kind:          pipelinerun.StepOutputDetailKindDocuments,
				DocumentCount: &documentCount,
			},
		})

		require.NotNil(t, mapped)
		require.NotNil(t, mapped.OperationType)
		require.Equal(t, "find", *mapped.OperationType)
		documents, err := mapped.Details.AsPipelineRunDocumentsOutputDetails()
		require.NoError(t, err)
		require.NotNil(t, documents.DocumentCount)
		require.Equal(t, 2, *documents.DocumentCount)

		body, err := json.Marshal(mapped)
		require.NoError(t, err)
		require.NotContains(t, string(body), `"row_count":null`)
		require.NotContains(t, string(body), `"status_code":null`)
	})

	t.Run("MapsHTTPResponseDetails", func(t *testing.T) {
		t.Parallel()

		statusCode := 201
		mapped := toAPIPipelineRunStepOutputSnapshot(&pipelinerun.StepOutputSnapshot{
			ResultType: "http",
			VendorType: "http",
			DurationMs: 21,
			Details: &pipelinerun.StepOutputDetails{
				Kind:       pipelinerun.StepOutputDetailKindHTTPResponse,
				StatusCode: &statusCode,
			},
		})

		require.NotNil(t, mapped)
		response, err := mapped.Details.AsPipelineRunHTTPResponseOutputDetails()
		require.NoError(t, err)
		require.Equal(t, 201, response.StatusCode)

		body, err := json.Marshal(mapped)
		require.NoError(t, err)
		require.NotContains(t, string(body), `"row_count":null`)
		require.NotContains(t, string(body), `"document_count":null`)
	})

	t.Run("MapsFileReadDetailsWithRowCount", func(t *testing.T) {
		t.Parallel()

		rowCount := 2
		mapped := toAPIPipelineRunStepOutputSnapshot(&pipelinerun.StepOutputSnapshot{
			ResultType:    "ftp",
			VendorType:    "ftp",
			DurationMs:    15,
			OperationType: "read",
			Details: &pipelinerun.StepOutputDetails{
				Kind:        pipelinerun.StepOutputDetailKindFileRead,
				TargetPath:  strPtr("/imports/users.csv"),
				RowCount:    &rowCount,
				ColumnNames: []string{"id", "email"},
			},
		})

		require.NotNil(t, mapped)
		read, err := mapped.Details.AsPipelineRunFileReadOutputDetails()
		require.NoError(t, err)
		require.NotNil(t, read.TargetPath)
		require.Equal(t, "/imports/users.csv", *read.TargetPath)
		require.NotNil(t, read.RowCount)
		require.Equal(t, rowCount, *read.RowCount)
		require.NotNil(t, read.ColumnNames)
		require.Equal(t, []string{"id", "email"}, *read.ColumnNames)

		body, err := json.Marshal(mapped)
		require.NoError(t, err)
		require.NotContains(t, string(body), `"document_count":null`)
		require.NotContains(t, string(body), `"status_code":null`)
		require.Contains(t, string(body), `"kind":"file_read"`)
	})

	t.Run("MapsFileReadDetailsWithoutRowCount", func(t *testing.T) {
		t.Parallel()

		content := []byte{0, 1, 2}
		metadata := filepayload.NewFilePayloadMetadata("users.bin", "application/octet-stream", content)
		mapped := toAPIPipelineRunStepOutputSnapshot(&pipelinerun.StepOutputSnapshot{
			ResultType:    "ftp",
			VendorType:    "ftp",
			DurationMs:    15,
			OperationType: "read",
			Details: &pipelinerun.StepOutputDetails{
				Kind:       pipelinerun.StepOutputDetailKindFileRead,
				TargetPath: strPtr("/imports/users.bin"),
				File:       &metadata,
			},
		})

		require.NotNil(t, mapped)
		read, err := mapped.Details.AsPipelineRunFileReadOutputDetails()
		require.NoError(t, err)
		require.NotNil(t, read.TargetPath)
		require.Equal(t, "/imports/users.bin", *read.TargetPath)
		require.Nil(t, read.RowCount)
		require.Nil(t, read.ColumnNames)
		require.NotNil(t, read.File)
		require.NotNil(t, read.File.Filename)
		require.Equal(t, "users.bin", *read.File.Filename)

		body, err := json.Marshal(mapped)
		require.NoError(t, err)
		require.NotContains(t, string(body), `"row_count":null`)
		require.NotContains(t, string(body), `"document_count":null`)
		require.NotContains(t, string(body), `"status_code":null`)
		require.Contains(t, string(body), `"kind":"file_read"`)
	})

	t.Run("MapsFileWriteDetails", func(t *testing.T) {
		t.Parallel()

		content := []byte{0, 1, 2}
		metadata := filepayload.NewFilePayloadMetadata("users.bin", "application/octet-stream", content)
		writtenRows := 2
		writtenBytes := len(content)
		mapped := toAPIPipelineRunStepOutputSnapshot(&pipelinerun.StepOutputSnapshot{
			ResultType:    "ftp",
			VendorType:    "ftp",
			DurationMs:    12,
			OperationType: "write",
			Details: &pipelinerun.StepOutputDetails{
				Kind:         pipelinerun.StepOutputDetailKindFileWrite,
				File:         &metadata,
				TargetPath:   strPtr("/exports/users.bin"),
				WrittenRows:  &writtenRows,
				WrittenBytes: &writtenBytes,
			},
		})

		require.NotNil(t, mapped)
		write, err := mapped.Details.AsPipelineRunFileWriteOutputDetails()
		require.NoError(t, err)
		require.Equal(t, "/exports/users.bin", write.TargetPath)
		require.NotNil(t, write.File)
		require.NotNil(t, write.File.Filename)
		require.Equal(t, "users.bin", *write.File.Filename)
		require.Equal(t, "application/octet-stream", write.File.MimeType)
		require.EqualValues(t, len(content), write.File.SizeBytes)
		require.NotNil(t, write.WrittenRows)
		require.Equal(t, writtenRows, *write.WrittenRows)
		require.NotNil(t, write.WrittenBytes)
		require.Equal(t, writtenBytes, *write.WrittenBytes)

		body, err := json.Marshal(mapped)
		require.NoError(t, err)
		require.NotContains(t, string(body), `"row_count":null`)
		require.NotContains(t, string(body), `"document_count":null`)
		require.NotContains(t, string(body), `"status_code":null`)
		require.Contains(t, string(body), `"kind":"file_write"`)
	})

	t.Run("MapsUnknownDetailsWhenKindIsUnrecognized", func(t *testing.T) {
		t.Parallel()

		mapped := toAPIPipelineRunStepOutputSnapshot(&pipelinerun.StepOutputSnapshot{
			ResultType: "custom",
			VendorType: "custom",
			DurationMs: 7,
			Details: &pipelinerun.StepOutputDetails{
				Kind: pipelinerun.StepOutputDetailKind(999),
			},
		})

		require.NotNil(t, mapped)
		unknown, err := mapped.Details.AsPipelineRunUnknownOutputDetails()
		require.NoError(t, err)
		require.Equal(t, api.PipelineRunUnknownOutputDetailsKindUnknown, unknown.Kind)

		body, err := json.Marshal(mapped)
		require.NoError(t, err)
		require.Contains(t, string(body), `"kind":"unknown"`)
		require.NotContains(t, string(body), `"row_count":null`)
		require.NotContains(t, string(body), `"document_count":null`)
		require.NotContains(t, string(body), `"status_code":null`)
	})
}

func testPipelineDetail(
	now time.Time,
	_ *string,
	sourcePath, sourceRef, lastAppliedCommit *string,
) *pipelinesvc.PipelineDetail {
	if sourcePath == nil {
		sourcePath = strPtr("pipelines/acme-users-sync.pipeline.yaml")
	}
	if sourceRef == nil {
		sourceRef = strPtr("refs/heads/main")
	}
	if lastAppliedCommit == nil {
		lastAppliedCommit = strPtr("0123456789abcdef0123456789abcdef01234567")
	}
	currentVersionID := int64(12)
	return &pipelinesvc.PipelineDetail{
		Summary: &pipelinesvc.PipelineSummary{
			Slug:                   slug.Slug("acme-users-sync"),
			Name:                   "ACME Users Sync",
			SyncStatus:             pipelinesrepo.SyncStatusDrifted,
			SourcePath:             sourcePath,
			SourceRef:              sourceRef,
			LastAppliedCommit:      lastAppliedCommit,
			CurrentVersionID:       &currentVersionID,
			CurrentVersionSpecHash: strPtr("spec-hash-12"),
			CreatedAt:              now,
			UpdatedAt:              now,
		},
		Spec: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: slug.Slug("acme-users-sync"),
				Name: "ACME Users Sync",
			},
			Steps: []pipelinespec.Step{{
				Kind: "query",
				Slug: slug.Slug("fetch-users"),
				Name: "Fetch users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: slug.Slug("acme-postgres"),
					OperationType:  "sql",
					OperationRef:   slug.Slug("list-active-users"),
				},
			}},
		},
	}
}

func strPtr(v string) *string {
	return &v
}

func slugPtr(v slug.Slug) *slug.Slug {
	return &v
}

func testPipelineValidationRecord() *pipelinesvc.PipelineValidationResultRecord {
	stepSlug := "fetch-users"
	dataSourceRefSlug := "orders-db"
	dataSourceRef := "orders-db"
	resolvedDataSourceSlug := "orders-prod"
	environmentSlug := "prod"
	dataSourceType := model.DataSourceTypePostgres
	requiredCapability := model.DataSourceRefCapabilityPipelineSource
	specHash := "6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8"
	now := time.Date(2026, 6, 27, 14, 45, 0, 0, time.UTC)
	return &pipelinesvc.PipelineValidationResultRecord{
		Slug:            slug.Slug("validation-acme-users-sync-a1b2c3"),
		ProjectSlug:     slug.Slug("default"),
		EnvironmentSlug: slug.Slug("prod"),
		PipelineSlug:    slug.Slug("acme-users-sync"),
		Source:          pipelinesvc.PipelineValidationResultSourceRevalidate,
		Valid:           false,
		SpecHash:        &specHash,
		Diagnostics: []pipelinesvc.PipelineValidationDiagnostic{{
			Severity:               "error",
			Code:                   "PIPELINE_DATA_SOURCE_REF_MISSING_BINDING",
			Field:                  "steps[0].query.data_source_ref",
			StepSlug:               &stepSlug,
			DataSourceRef:          &dataSourceRef,
			DataSourceRefSlug:      &dataSourceRefSlug,
			ResolvedDataSourceSlug: &resolvedDataSourceSlug,
			EnvironmentSlug:        &environmentSlug,
			DataSourceType:         &dataSourceType,
			RequiredCapability:     &requiredCapability,
			Message:                "logical data source ref orders-db has no binding for prod",
		}},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func int64Ptr(v int64) *int64 {
	return &v
}

func testPipelineRequestBody() map[string]any {
	return map[string]any{
		"spec": map[string]any{
			"metadata": map[string]any{
				"slug": "acme-users-sync",
				"name": "ACME Users Sync",
			},
			"steps": []map[string]any{
				{
					"kind": "query",
					"slug": "fetch-users",
					"name": "Fetch users",
					"query": map[string]any{
						"data_source_ref": "acme-postgres",
						"operation_type":  "sql",
						"operation_ref":   "list-active-users",
					},
				},
			},
		},
	}
}

func addUnknownValidationKeyToPipelineRequest(requestBody map[string]any) {
	spec := requestBody["spec"].(map[string]any)
	steps := spec["steps"].([]map[string]any)
	query := steps[0]["query"].(map[string]any)
	query["output_contract"] = map[string]any{
		"representation": "rows",
		"fields": []map[string]any{{
			"name": "amount",
			"type": "number",
			"validations": map[string]any{
				"minimum": 1,
			},
		}},
	}
}

func TestServer_ReconcilePipelinesExpectedSource(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, ds string
		status   int
	}{
		{"stale preview", "git-source", http.StatusConflict},
		{"invalid source slug", "not a slug", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			called := false
			app, store := newTestApp(t, testServerConfig{pipelinesService: &fakePipelinesService{
				ReconcilePipelinesFn: func(_ context.Context, org, project, _ slug.Slug, _ model.EventActor, req pipelinesvc.ReconcilePipelinesRequest) (*pipelinesvc.ReconcilePipelinesResult, error) {
					called = true
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("default"), project)
					require.Equal(t, &pipelinesvc.ReconcilePipelinesExpectedSource{DataSourceSlug: "git-source", Repo: "https://example.com/repo.git", Ref: "refs/heads/main", Commit: "abc123"}, req.ExpectedSource)
					return nil, pipelinesvc.ErrPipelineReconcileSourceChanged
				},
			}})
			req := jsonRequest(t, http.MethodPost, "/organizations/acme/projects/default/pipelines:reconcile", api.PipelineReconcileRequest{ExpectedSource: &api.PipelineReconcileExpectedSource{DataSourceSlug: tc.ds, Repo: "https://example.com/repo.git", Ref: "refs/heads/main", Commit: "abc123"}})
			resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, tc.status, resp.StatusCode)
			require.Equal(t, tc.status == http.StatusConflict, called)
			if called {
				var body api.ErrorResponse
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
				require.Equal(t, "PIPELINE_RECONCILE_SOURCE_CHANGED", body.Error.Code)
			}
		})
	}
}

func TestODCSPipelineValidationDiagnosticsRetainPortAndRevisionPath(t *testing.T) {
	t.Parallel()
	result := &pipelinesvc.PipelineValidationResult{Valid: false, Diagnostics: []pipelinesvc.PipelineValidationDiagnostic{{
		Severity: "error", Code: "ODCS_TYPE_UNSUPPORTED", Field: "steps[0].query.output_contract_ref", StepSlug: strPtr("fetch"),
		Message: "$.schema[0].properties[0].logicalType: Declare a supported type.",
	}}}
	encoded, err := json.Marshal(toAPIPipelineValidationResponse(result))
	require.NoError(t, err)
	var decoded api.PipelineValidationResponse
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.False(t, decoded.Valid)
	require.Equal(t, "ODCS_TYPE_UNSUPPORTED", decoded.Diagnostics[0].Code)
	require.Equal(t, "steps[0].query.output_contract_ref", decoded.Diagnostics[0].Field)
	require.Equal(t, api.NullableSlug("fetch"), *decoded.Diagnostics[0].StepSlug)
	require.Equal(t, result.Diagnostics[0].Message, decoded.Diagnostics[0].Message)
}

func TestServer_ExecutePipelineBackfill(t *testing.T) {
	t.Parallel()
	for _, allowed := range []bool{true, false} {
		t.Run(fmt.Sprintf("authorized=%t", allowed), func(t *testing.T) {
			t.Parallel()
			called := false
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					if input.Resource == model.ResourcePipelines && input.Action == model.Execute {
						return allowed, nil
					}
					return true, nil
				}},
				pipelinesService: &fakePipelinesService{StartBackfillFn: func(_ context.Context, org, project, environment, pipeline slug.Slug, run, confirmation *slug.Slug, actor model.EventActor, source pipelinerun.RunSourceSummary, input map[string]any, windows map[slug.Slug]pipelinerun.IncrementalSourceWindow) (*pipelinesvc.PipelineRunDetail, error) {
					called = true
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("default"), project)
					require.Equal(t, slug.Slug("prod"), environment)
					require.Equal(t, slug.Slug("users-sync"), pipeline)
					require.Equal(t, slug.Slug("backfill-001"), *run)
					require.Equal(t, model.EventActorTypeUser, actor.Type)
					require.Equal(t, `{"value":9007199254740993}`, string(windows["source"].LowerBound))
					require.Equal(t, `{"value":9007199254740995}`, string(windows["source"].UpperBound))
					return &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{PipelineSlug: pipeline.String(), RunSlug: run.String(), Status: "queued", EnvironmentSlug: environment.String()}}, nil
				}},
			})
			req := jsonRequest(t, http.MethodPost, "/organizations/acme/projects/default/environments/prod/pipelines/users-sync:execute", json.RawMessage(`{"run_slug":"backfill-001","backfill":{"source_windows":{"source":{"lower_bound":{"value":9007199254740993},"upper_bound":{"value":9007199254740995}}}}}`))
			resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
			defer func() { _ = resp.Body.Close() }()
			if allowed {
				require.Equal(t, http.StatusAccepted, resp.StatusCode)
			} else {
				require.Equal(t, http.StatusForbidden, resp.StatusCode)
			}
			require.Equal(t, allowed, called)
		})
	}
}

func TestServer_BootstrapPipelineCheckpointsRequiresUpdate(t *testing.T) {
	t.Parallel()
	for _, allowed := range []bool{true, false} {
		t.Run(fmt.Sprintf("authorized=%t", allowed), func(t *testing.T) {
			t.Parallel()
			called := false
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					if input.Resource == model.ResourcePipelines && input.Action == model.Update {
						return allowed, nil
					}
					return true, nil
				}},
				pipelinesService: &fakePipelinesService{BootstrapCheckpointsFn: func(_ context.Context, org, project, environment, pipeline, request slug.Slug, confirmation *slug.Slug, actor model.EventActor, input map[string]any, seeds map[slug.Slug]json.RawMessage) error {
					called = true
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("default"), project)
					require.Equal(t, slug.Slug("prod"), environment)
					require.Equal(t, slug.Slug("users-sync"), pipeline)
					require.Equal(t, slug.Slug("bootstrap-001"), request)
					require.Equal(t, slug.Slug("prod"), *confirmation)
					require.JSONEq(t, `{"cursor":"opaque-token"}`, string(seeds["source"]))
					return nil
				}},
			})
			req := jsonRequest(t, http.MethodPost, "/organizations/acme/projects/default/environments/prod/pipelines/users-sync:bootstrap-checkpoints", json.RawMessage(`{"request_slug":"bootstrap-001","protected_environment_confirmation":"prod","source_seeds":{"source":{"cursor":"opaque-token"}}}`))
			resp := authenticatedRequest(t, app, store, slug.Slug("test-user"), req)
			defer func() { _ = resp.Body.Close() }()
			if allowed {
				require.Equal(t, http.StatusNoContent, resp.StatusCode)
			} else {
				require.Equal(t, http.StatusForbidden, resp.StatusCode)
			}
			require.Equal(t, allowed, called)
		})
	}
}
