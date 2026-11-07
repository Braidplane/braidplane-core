package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
)

func TestGetProofReceiptReturnsProtectedTargetScopedPreview(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 31, 8, 0, 0, 0, time.UTC)
	receipt := &model.ProofReceipt{
		SchemaVersion: model.ProofReceiptSchemaVersion, ID: "proof-123",
		Candidate: model.ProofCandidateIdentity{
			OrganizationSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", ProviderConnectionSlug: "github",
			RepositoryID: "42", ChangeRequestID: "7", HeadSHA: strings.Repeat("a", 40), TargetRef: "refs/heads/main",
			PipelinePath: "pipelines/orders.yaml", SpecHash: "sha256:" + strings.Repeat("b", 64),
			BindingFingerprint: "sha256:" + strings.Repeat("c", 64), CapabilityFingerprint: "sha256:" + strings.Repeat("d", 64),
			CompilerVersion: "proof-preview-compiler-v1", PolicyVersion: "proof-preview-policy-v1",
		},
		Claims: []model.ProofClaim{{
			Identity:    model.ProofClaimIdentity{Family: model.ProofClaimFamilyDriftImpact, PipelineVersionHash: "sha256:" + strings.Repeat("b", 64), SemanticPath: "/deployment"},
			Description: "deployment comparison is unavailable", RequiredEvidenceLevel: model.ProofEvidenceLevelStatic, Verdict: model.ProofVerdictUnverified,
		}},
		AggregateVerdict: model.ProofVerdictUnverified, CreatedAt: now, RetainUntil: now.Add(90 * 24 * time.Hour),
	}
	app, store := newTestApp(t, testServerConfig{eventsService: &fakeEventsService{CreateEventFn: func(_ context.Context, params eventssvc.CreateEventParams) (*model.Event, error) {
		require.Equal(t, model.EventActionRead, params.Action)
		require.Equal(t, model.EventClassSecurityAudit, params.EventClass)
		require.Equal(t, slug.Slug("acme"), params.OrgSlug)
		require.Equal(t, "proof-123", params.Metadata["proof_receipt_slug"])
		return &model.Event{}, nil
	}}, proofReceiptService: &fakeProofReceiptService{GetFn: func(_ context.Context, org, project, environment, receiptSlug slug.Slug) (*proofreceiptsvc.ReceiptDetail, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("default"), project)
		require.Equal(t, slug.Slug("prod"), environment)
		require.Equal(t, slug.Slug("proof-123"), receiptSlug)
		pipelineSlug := slug.Slug("orders")
		return &proofreceiptsvc.ReceiptDetail{Receipt: *receipt, LifecycleLinks: []proofreceiptsvc.LifecycleLink{{
			Kind: model.ProofLifecycleLinkKindDeployment, Identifier: "orders:old", LinkedAt: now, RetainUntil: receipt.RetainUntil,
			Status: "version_mismatch", PipelineSlug: &pipelineSlug, CurrentVersionSpecHash: "sha256:" + strings.Repeat("e", 64),
		}}}, nil
	}}})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme/projects/default/environments/prod/proof-receipts/proof-123", nil)
	require.NoError(t, err)
	resp := authenticatedRequest(t, app, store, "test-user", req)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	var body api.ProofReceipt
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, api.Slug("proof-123"), body.Id)
	require.Equal(t, api.ProofVerdictUnverified, body.AggregateVerdict)
	require.Equal(t, api.Slug("prod"), body.Candidate.EnvironmentSlug)
	require.Equal(t, api.Slug("github"), body.Candidate.ProviderConnectionSlug)
	require.Len(t, body.LifecycleLinks, 1)
	require.Equal(t, api.ProofLifecycleLinkStatusVersionMismatch, body.LifecycleLinks[0].Status)
	require.Equal(t, api.Slug("orders"), *body.LifecycleLinks[0].PipelineSlug)
}

func TestListProofReceiptsReturnsProtectedScopedPage(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 19, 8, 0, 0, 0, time.UTC)
	next := "2026-09-19T08:00:00Z_proof-2"
	app, store := newTestApp(t, testServerConfig{eventsService: &fakeEventsService{CreateEventFn: func(_ context.Context, params eventssvc.CreateEventParams) (*model.Event, error) {
		require.Equal(t, "proof-2", params.Metadata["proof_receipt_slug"])
		return &model.Event{}, nil
	}}, proofReceiptService: &fakeProofReceiptService{ListFn: func(_ context.Context, org, project, environment slug.Slug, req model.ListRequest) (model.ListResponse[*proofreceiptsvc.ReceiptDetail], error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("default"), project)
		require.Equal(t, slug.Slug("prod"), environment)
		require.Equal(t, 1, req.Limit)
		return model.ListResponse[*proofreceiptsvc.ReceiptDetail]{Items: []*proofreceiptsvc.ReceiptDetail{{Receipt: model.ProofReceipt{
			SchemaVersion: model.ProofReceiptSchemaVersion, ID: "proof-2", CreatedAt: now, RetainUntil: now.Add(time.Hour),
			Candidate: model.ProofCandidateIdentity{OrganizationSlug: org, ProjectSlug: project, EnvironmentSlug: environment},
			Claims:    []model.ProofClaim{},
		}}}, HasMore: true, NextCursor: &next}, nil
	}}})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/organizations/acme/projects/default/environments/prod/proof-receipts?limit=1", nil)
	require.NoError(t, err)
	resp := authenticatedRequest(t, app, store, "test-user", req)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	var body api.ProofReceiptListResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.True(t, body.HasMore)
	require.Equal(t, &next, body.NextCursor)
	require.Len(t, body.Receipts, 1)
	require.Equal(t, api.Slug("proof-2"), body.Receipts[0].Id)
}

func TestListProofReceiptsWithholdsPageWhenAuditFails(t *testing.T) {
	t.Parallel()
	app, store := newTestApp(t, testServerConfig{
		eventsService: &fakeEventsService{CreateEventFn: func(context.Context, eventssvc.CreateEventParams) (*model.Event, error) {
			return nil, proofreceiptsvc.ErrObservationAuditUnavailable
		}},
		proofReceiptService: &fakeProofReceiptService{ListFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.ListRequest) (model.ListResponse[*proofreceiptsvc.ReceiptDetail], error) {
			return model.ListResponse[*proofreceiptsvc.ReceiptDetail]{Items: []*proofreceiptsvc.ReceiptDetail{{Receipt: model.ProofReceipt{ID: "protected-receipt-sentinel"}}}}, nil
		}},
	})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/organizations/acme/projects/default/environments/prod/proof-receipts", nil)
	require.NoError(t, err)
	resp := authenticatedRequest(t, app, store, "test-user", req)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NotContains(t, string(body), "protected-receipt-sentinel")
	require.NotContains(t, string(body), "receipts")
}

func TestListProofReceiptsRequiresPipelineReadPermission(t *testing.T) {
	t.Parallel()
	app, store := newTestApp(t, testServerConfig{authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
		require.Equal(t, model.Read, input.Action)
		require.Equal(t, model.ResourcePipelines, input.Resource)
		return false, nil
	}}})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/organizations/acme/projects/default/environments/prod/proof-receipts", nil)
	require.NoError(t, err)
	resp := authenticatedRequest(t, app, store, "test-user", req)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
}

func TestGetProofReceiptRequiresPipelineReadPermission(t *testing.T) {
	t.Parallel()
	app, store := newTestApp(t, testServerConfig{authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
		require.Equal(t, model.Read, input.Action)
		require.Equal(t, model.ResourcePipelines, input.Resource)
		return false, nil
	}}})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme/projects/default/environments/prod/proof-receipts/proof-123", nil)
	require.NoError(t, err)
	resp := authenticatedRequest(t, app, store, "test-user", req)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
}

func TestGetProofReceiptIsNotRegisteredWhenProofIsDisabled(t *testing.T) {
	t.Parallel()
	app, store := newTestApp(t, testServerConfig{disableProofReceipts: true})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme/projects/default/environments/prod/proof-receipts/proof-123", nil)
	require.NoError(t, err)
	resp := authenticatedRequest(t, app, store, "test-user", req)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestGetProofReceiptWithholdsEvidenceWhenAuditFails(t *testing.T) {
	t.Parallel()
	app, store := newTestApp(t, testServerConfig{
		eventsService: &fakeEventsService{CreateEventFn: func(context.Context, eventssvc.CreateEventParams) (*model.Event, error) {
			return nil, proofreceiptsvc.ErrObservationAuditUnavailable
		}},
		proofReceiptService: &fakeProofReceiptService{GetFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*proofreceiptsvc.ReceiptDetail, error) {
			return &proofreceiptsvc.ReceiptDetail{Receipt: model.ProofReceipt{ID: "protected-receipt-sentinel"}}, nil
		}},
	})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/organizations/acme/projects/default/environments/prod/proof-receipts/proof-123", nil)
	require.NoError(t, err)
	resp := authenticatedRequest(t, app, store, "test-user", req)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NotContains(t, string(body), "protected-receipt-sentinel")
	require.NotContains(t, string(body), "claims")
}
