package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	testfakes "github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	transport "github.com/Braidplane/braidplane-core/transport/http"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	"github.com/stretchr/testify/require"
)

type observationTestAuthority struct {
	org   slug.Slug
	allow bool
}

func (a *observationTestAuthority) Can(_ context.Context, input authorization.Input) (bool, error) {
	return a.allow && input.UserSlug == "reviewer" && input.OrgSlug != nil && *input.OrgSlug == a.org && (input.Action == model.Read || input.Action == model.Execute || input.Action == model.Update), nil
}

func TestObservationHTTPPreviewRequiresConfirmationBeforeAdmission(t *testing.T) {
	meter, err := metrics.New(metrics.Config{Enabled: true, ServiceName: "api", Environment: "test"})
	require.NoError(t, err)
	t.Cleanup(metrics.SetProcessMeter(meter))
	f := newFixture(t)
	compiler, services := newReceiptCompiler(t, f)
	authority := &observationTestAuthority{org: f.scope.Organization, allow: true}
	server := transport.NewServer(
		nil,                           // usersService
		nil,                           // accountClosureService
		nil,                           // oidcService
		session.NewStore(),            // sessionStore
		authority,                     // authorizationService
		nil,                           // orgService
		&testfakes.AdmissionService{}, // admissionService
		nil,                           // projectsService
		nil,                           // repositorySetupService
		nil,                           // pipelineCreationsService
		nil,                           // odcsContractsService
		nil,                           // compiledPlansService
		nil,                           // odcsArtifactImporter
		nil,                           // environmentsService
		nil,                           // membersService
		nil,                           // rolesService
		nil,                           // apiKeysService
		nil,                           // patsService
		nil,                           // credentialsService
		nil,                           // datasourcesService
		nil,                           // dataSourceRefsService
		nil,                           // queriesService
		eventssvc.New(postgres.NewTransactionManager(f.pool), events.New(), clock.New()), // eventsService
		nil,      // githubAppService
		nil,      // notificationsService
		nil,      // retentionService
		nil,      // pipelinesService
		compiler, // proofReceiptService
		nil,      // proofConfigurationService
		compiler, // proofObservationService
		nil,      // pipelineTriggersService
		nil,      // pipelineWebhookService
		nil,      // runRecencyService
		"",       // frontendURL
		"",       // googleRedirectURI
		false,    // selfServiceRegistrationEnabled
		false,    // externalOnboardingEnabled
		nil,
	)
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Use(func(c fiber.Ctx) error {
		reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{Slug: "reviewer", Source: reqctx.UserPrincipalSourceSession})
		return c.Next()
	})
	schema, err := openapi3.NewLoader().LoadFromFile("../../../api-schema/schema.yml")
	require.NoError(t, err)
	schema.Servers = nil // This fixture registers the API without its deployment host/mount.
	validator, err := transport.NewOpenAPIRequestValidator(schema)
	require.NoError(t, err)
	app.Use(validator)
	api.RegisterHandlers(app, server)
	routeScope := f.scope
	call := func(method, path string, input, output any) int {
		body, err := json.Marshal(input)
		require.NoError(t, err)
		request := httptest.NewRequestWithContext(t.Context(), method, "/organizations/"+routeScope.Organization.String()+"/projects/"+routeScope.Project.String()+"/environments/"+routeScope.Environment.String()+"/"+path, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response, err := app.Test(request)
		require.NoError(t, err)
		defer func() { _ = response.Body.Close() }()
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		if response.StatusCode < 300 && path != "proof-observation-policy" {
			require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		}
		if response.StatusCode < 300 && output != nil {
			require.NoError(t, json.Unmarshal(raw, output))
		}
		if response.StatusCode < 300 && path != "proof-observation-previews" {
			for _, private := range []string{"private_dataset", "private_table", "private_email", "target_ciphertext", "snapshot_ciphertext", "authorization_decision", "intent_token"} {
				require.NotContains(t, string(raw), private)
			}
		}
		return response.StatusCode
	}
	settings := api.ProofObservationPolicyInput{Enabled: true, TimeoutSeconds: 30, EvidenceTtlSeconds: 300, MaxRequests: 20, MaxRows: 10000, MaxBytes: 10 << 20}
	var policy api.ProofObservationPolicy
	require.Equal(t, http.StatusOK, call(http.MethodPut, "proof-observation-policy", settings, &policy))
	var read api.ProofObservationPolicy
	require.Equal(t, http.StatusOK, call(http.MethodGet, "proof-observation-policy", nil, &read))
	require.Equal(t, policy, read)
	f.retentionAudit.failAfter = 1
	f.retentionAudit.recorded = 1
	require.Equal(t, http.StatusServiceUnavailable, call(http.MethodPut, "proof-observation-policy", settings, nil))
	f.retentionAudit.failAfter = 0
	require.Equal(t, http.StatusBadRequest, call(http.MethodPut, "proof-observation-policy", map[string]any{"enabled": true, "timeout_seconds": 30, "evidence_ttl_seconds": 300, "max_requests": 20, "max_rows": 10000, "max_bytes": 10 << 20, "allow_writes": true}, nil))
	var target api.ProofObservationTarget
	require.NoError(t, target.FromProofBigQueryTarget(api.ProofBigQueryTarget{ProjectId: f.target.ProjectID, DatasetId: f.target.DatasetID, TableId: f.target.TableID}))
	var preview api.ProofObservationPreview
	require.Equal(t, http.StatusOK, call(http.MethodPost, "proof-observation-previews", api.ProofObservationPreviewInput{CandidateSlug: f.request.CandidateSlug.String(), StepSlug: "sink", Target: target}, &preview))
	require.Equal(t, target, preview.Target)
	require.Equal(t, api.ProofObservationOperationTableMetadata, preview.Operation)
	require.Equal(t, "/steps/sink/destination/append_capability", preview.Claim.SemanticPath)
	require.Equal(t, 3600, preview.RateWindowSeconds)
	require.True(t, preview.MetadataExpiresAt.After(preview.EvidenceExpiresAt))
	require.Equal(t, f.request.Candidate.HeadSHA, preview.Candidate.HeadSha)
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM proof_observation_jobs WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization).Scan(&count))
	require.Zero(t, count)
	intent := api.ProofObservationIntent{IntentToken: preview.IntentToken, RequestDigest: preview.RequestDigest}
	authority.allow = false
	require.Equal(t, http.StatusForbidden, call(http.MethodPost, "proof-observations", intent, nil))
	var denialClass, denialActor, denialAction string
	var denialMetadata []byte
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT event_class,actor_slug,action,metadata FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND action='decline'`, f.scope.Organization).Scan(&denialClass, &denialActor, &denialAction, &denialMetadata))
	require.Equal(t, "security_audit", denialClass)
	require.Equal(t, "reviewer", denialActor)
	require.Equal(t, "decline", denialAction)
	require.NotContains(t, string(denialMetadata), preview.IntentToken)
	require.NotContains(t, string(denialMetadata), preview.RequestDigest)
	routeScope.Organization = "nonexistent-audit-organization"
	require.Equal(t, http.StatusForbidden, call(http.MethodPost, "proof-observations", intent, nil))
	routeScope = f.scope
	authority.allow = true
	f.retentionAudit.failAfter = 1
	f.retentionAudit.recorded = 1
	require.Equal(t, http.StatusServiceUnavailable, call(http.MethodPost, "proof-observations", intent, nil))
	f.retentionAudit.failAfter = 0
	var job api.ProofObservationJob
	require.Equal(t, http.StatusOK, call(http.MethodPost, "proof-observations", intent, &job))
	require.Equal(t, preview.RequestDigest, job.RequestDigest)
	require.Equal(t, api.ProofObservationJobStateFinished, job.State)
	var replay api.ProofObservationJob
	require.Equal(t, http.StatusOK, call(http.MethodPost, "proof-observations", intent, &replay))
	require.Equal(t, job, replay)
	statusPath := "proof-observations/" + job.Slug
	var status api.ProofObservationJob
	require.Equal(t, http.StatusOK, call(http.MethodGet, statusPath, nil, &status))
	require.Equal(t, job, status)
	require.Equal(t, api.ProofObservationOutcomeComplete, *status.Outcome)
	require.False(t, status.CancellationRequested)
	compileInput := api.CompileObservedProofReceiptInput{CandidateSlug: f.request.CandidateSlug.String(), ObservationSlugs: []api.Slug{job.Slug}}
	f.retentionAudit.failAction = model.EventActionExecute
	require.Equal(t, http.StatusServiceUnavailable, call(http.MethodPost, "proof-receipts", compileInput, nil))
	var receipts int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM proof_receipts WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization).Scan(&receipts))
	require.Zero(t, receipts, "audit failure rolls back receipt creation")
	f.retentionAudit.failAction = model.EventActionUnknown
	require.Equal(t, http.StatusOK, call(http.MethodPost, "proof-receipts", compileInput, nil))
	for _, wrong := range []proofobservations.Scope{{Organization: f.scope.Organization, Project: "other-project", Environment: f.scope.Environment}, {Organization: f.scope.Organization, Project: f.scope.Project, Environment: "other-environment"}} {
		routeScope = wrong
		require.Equal(t, http.StatusNotFound, call(http.MethodGet, statusPath, nil, nil))
		require.Equal(t, http.StatusNotFound, call(http.MethodPost, statusPath+"/cancel", nil, nil))
		require.Equal(t, http.StatusNotFound, call(http.MethodPost, "proof-receipts", compileInput, nil))
	}
	routeScope = f.scope
	f.retentionAudit.failAfter = 1
	f.retentionAudit.recorded = 1
	require.Equal(t, http.StatusServiceUnavailable, call(http.MethodPost, statusPath+"/cancel", nil, nil))
	f.retentionAudit.failAfter = 0
	require.Equal(t, http.StatusOK, call(http.MethodPost, statusPath+"/cancel", nil, &status))
	require.Equal(t, api.ProofObservationJobStateFinished, status.State)
	require.Equal(t, api.ProofObservationOutcomeComplete, *status.Outcome)
	require.False(t, status.CancellationRequested, "completed evidence is immutable")
	require.Equal(t, http.StatusOK, call(http.MethodPost, statusPath+"/cancel", nil, &replay))
	require.Equal(t, status, replay)
	compileInput.ObservationSlugs = []api.Slug{job.Slug}
	var receipt api.ProofReceipt
	require.Equal(t, http.StatusOK, call(http.MethodPost, "proof-receipts", compileInput, &receipt))
	found := false
	for _, claim := range receipt.Claims {
		if claim.Identity.SemanticPath == "/steps/sink/destination/append_capability" {
			found = true
			require.Equal(t, api.ProofVerdictFailed, claim.Verdict)
		}
	}
	require.True(t, found)
	var repeated api.ProofReceipt
	require.Equal(t, http.StatusOK, call(http.MethodPost, "proof-receipts", compileInput, &repeated))
	require.Equal(t, receipt, repeated)
	require.Equal(t, http.StatusOK, call(http.MethodGet, "proof-receipts/"+receipt.Id, nil, &repeated))
	require.Equal(t, receipt, repeated)
	require.Equal(t, http.StatusBadRequest, call(http.MethodPost, "proof-receipts", map[string]any{"candidate_slug": compileInput.CandidateSlug, "observation_slugs": []string{job.Slug, job.Slug}}, nil))
	settings.Enabled = false
	require.Equal(t, http.StatusOK, call(http.MethodPut, "proof-observation-policy", settings, nil))
	require.Equal(t, http.StatusOK, call(http.MethodGet, statusPath, nil, &replay))
	require.Equal(t, status, replay, "policy disable preserves sanitized status")
	require.Equal(t, http.StatusForbidden, call(http.MethodPost, "proof-receipts", compileInput, nil))
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM proof_observation_jobs WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization).Scan(&count))
	require.Equal(t, 1, count, "status, cancellation, and compilation cannot dispatch observations")
	require.Equal(t, 1, services.observationCalls, "confirmation replay cannot dispatch the executor twice")
	metricsApp := fiber.New()
	meter.RegisterRoutes(metricsApp)
	response, err := metricsApp.Test(httptest.NewRequestWithContext(t.Context(), "GET", "/metrics", nil))
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	for _, action := range []string{"policy", "confirm", "cancel", "compile", "access"} {
		require.Contains(t, string(body), `braidplane_proof_control_total{action="`+action+`",deployment_environment="test",outcome="audit_unavailable",service_name="api"} 1`)
	}
	require.Contains(t, string(body), `braidplane_proof_control_total{action="confirm",deployment_environment="test",outcome="accepted",service_name="api"} 2`)
	require.Contains(t, string(body), `braidplane_proof_control_total{action="access",deployment_environment="test",outcome="denied",service_name="api"} 1`)
	require.NotContains(t, string(body), "private_")
}
