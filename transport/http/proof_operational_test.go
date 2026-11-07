package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

type fakeOperationalReceiptService struct {
	fakeProofReceiptService
	compile func(context.Context, proofreceiptsvc.ObservationScope, slug.Slug, slug.Slug, proofreceiptsvc.OperationalSelection) (*proofreceipts.Receipt, error)
}

func (s *fakeOperationalReceiptService) CompileOperational(ctx context.Context, scope proofreceiptsvc.ObservationScope, actor, source slug.Slug, selection proofreceiptsvc.OperationalSelection) (*proofreceipts.Receipt, error) {
	if s.compile != nil {
		return s.compile(ctx, scope, actor, source, selection)
	}
	panic("denied requests must not reach operational admission")
}

func TestOperationalAdmissionRequiresUserAndAllPermissions(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"machine", "execute-denied", "pipeline-read-denied", "source-read-denied"} {
		t.Run(mode, func(t *testing.T) {
			service := &fakeOperationalReceiptService{}
			cfg := testServerConfig{
				proofReceiptService: service, eventsService: &fakeEventsService{CreateEventFn: func(context.Context, eventssvc.CreateEventParams) (*model.Event, error) { return &model.Event{}, nil }},
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					if mode == "execute-denied" && input.Action == model.Execute {
						return false, nil
					}
					if input.Action == model.Read && (mode == "pipeline-read-denied" && input.Resource == model.ResourcePipelines || mode == "source-read-denied" && input.Resource == model.ResourceDataSources) {
						return false, nil
					}
					return true, nil
				}},
			}
			if mode == "machine" {
				cfg.middleware = []fiber.Handler{apiKeyPrincipalMiddleware(reqctx.APIKeyPrincipal{Slug: "machine", OrgSlug: "acme", Role: "admin"})}
			}
			app, store := newTestApp(t, cfg)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme/projects/default/environments/prod/proof-operational-receipts", strings.NewReader(`{"source_receipt_id":"private-denied-input"}`))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			resp := authenticatedRequest(t, app, store, "test-user", req)
			t.Cleanup(func() { _ = resp.Body.Close() })
			want := http.StatusForbidden
			require.Equal(t, want, resp.StatusCode)
			require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NotContains(t, string(body), "private-denied-input")
		})
	}
}

func TestOperationalReceiptHTTPMapsCapturedAndCurrentState(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 19, 10, 0, 0, 0, time.UTC)
	candidate := model.ProofCandidateIdentity{OrganizationSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", ProviderConnectionSlug: "github", RepositoryID: "42", ChangeRequestID: "7", HeadSHA: strings.Repeat("a", 40), TargetRef: "main", PipelinePath: "pipelines/orders.json", SpecHash: "sha256:" + strings.Repeat("b", 64), BindingFingerprint: "sha256:" + strings.Repeat("c", 64), CapabilityFingerprint: "sha256:" + strings.Repeat("d", 64), CompilerVersion: proofreceiptsvc.CompilerVersion, PolicyVersion: proofreceiptsvc.PolicyVersion}
	selection := model.ProofOperationalSelection{CandidateSlug: "candidate-1", Candidate: candidate, Pipeline: "orders", VersionCommit: candidate.HeadSHA, CompiledPlanDigest: candidate.SpecHash, DeploymentResolutionDigest: candidate.SpecHash, Run: "run-1", Attempt: 2, CheckID: strings.Repeat("e", 64), Step: "extract", Origin: "step", Phase: "producer_output", Declaration: "step/producer_output", ProjectionDigest: strings.Repeat("f", 64), Target: "source", Window: model.StructuralCheckWindow{Kind: "backfill", SourceSteps: []slug.Slug{"extract"}, BackfillSeriesSlug: "backfill-1", BackfillChunkIndex: 3}, Evaluator: "braidplane/structural-runtime/v1", EvaluatorPolicy: "braidplane/structural-policy/v1", StartedAt: &now, CompletedAt: &now, EvidenceDigest: strings.Repeat("c", 64)}
	dto := toAPIOperationalSelection(selection)
	restored, err := fromAPIOperationalSelection(dto)
	require.NoError(t, err)
	require.Equal(t, selection, restored)
	receipt := model.ProofReceipt{SchemaVersion: 1, ID: "proof-operational", Candidate: candidate, AggregateVerdict: model.ProofVerdictPassed, CreatedAt: now, RetainUntil: now.Add(time.Hour), Claims: []model.ProofClaim{}, Operational: &model.ProofOperationalCapture{SourceReceiptID: "proof-static", PolicyVersion: proofreceiptsvc.OperationalPolicyVersion, Selection: selection, RecordDigest: candidate.SpecHash, ExpiresAt: now.Add(time.Hour)}}
	calls := 0
	service := &fakeOperationalReceiptService{compile: func(_ context.Context, scope proofreceiptsvc.ObservationScope, actor, source slug.Slug, selected proofreceiptsvc.OperationalSelection) (*proofreceipts.Receipt, error) {
		calls++
		require.Equal(t, proofreceiptsvc.ObservationScope{Organization: "acme", Project: "default", Environment: "prod"}, scope)
		require.Equal(t, slug.Slug("test-user"), actor)
		require.Equal(t, slug.Slug("proof-static"), source)
		require.Equal(t, selection, selected)
		return &proofreceipts.Receipt{Value: receipt}, nil
	}}
	service.GetFn = func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*proofreceiptsvc.ReceiptDetail, error) {
		return &proofreceiptsvc.ReceiptDetail{Receipt: receipt, OperationalEvidenceStatus: model.ProofOperationalEvidenceStatusMissing, OperationalCurrentVerdict: model.ProofVerdictUnverified}, nil
	}
	app, store := newTestApp(t, testServerConfig{proofReceiptService: service, eventsService: &fakeEventsService{CreateEventFn: func(context.Context, eventssvc.CreateEventParams) (*model.Event, error) { return &model.Event{}, nil }}})
	raw, err := json.Marshal(api.CompileOperationalProofReceiptInput{SourceReceiptId: "proof-static", Selection: dto})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme/projects/default/environments/prod/proof-operational-receipts", bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	response := authenticatedRequest(t, app, store, "test-user", req)
	t.Cleanup(func() { _ = response.Body.Close() })
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, 1, calls)
	var result api.ProofReceipt
	require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
	require.Equal(t, api.ProofVerdictPassed, result.AggregateVerdict)
	require.Equal(t, api.ProofVerdictUnverified, *result.OperationalCurrentVerdict)
	require.Equal(t, api.ProofReceiptOperationalEvidenceStatusMissing, *result.OperationalEvidenceStatus)
	require.Equal(t, dto, result.Operational.Selection)
}

func TestOperationalSelectionSchemaRejectsMalformedIdentity(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromFile("../../api-schema/schema.yml")
	require.NoError(t, err)
	selection := document.Components.Schemas["ProofOperationalSelection"].Value
	for _, tc := range []struct {
		field          string
		invalid, valid string
	}{
		{"compiled_plan_digest", strings.Repeat("a", 64), "sha256:" + strings.Repeat("a", 64)},
		{"deployment_resolution_digest", "sha256:invalid", "sha256:" + strings.Repeat("b", 64)},
		{"check_id", strings.Repeat("A", 64), strings.Repeat("a", 64)},
		{"projection_digest", "short", strings.Repeat("c", 64)},
		{"origin", "inferred", "step"},
		{"phase", "quality", "pre_sink"},
		{"target", "invalid target", "source"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			rule := selection.Properties[tc.field].Value
			var schemaErr *openapi3.SchemaError
			require.ErrorAs(t, rule.VisitJSON(tc.invalid), &schemaErr)
			require.NoError(t, rule.VisitJSON(tc.valid))
		})
	}
	require.NoError(t, selection.Properties["target"].Value.VisitJSON(""))
}
