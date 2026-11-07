package views

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestProofReceiptContentKeepsNonPassingEvidenceVisible(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	actual := api.ProofEvidenceLevelCachedObserved
	receipt := &api.ProofReceipt{Id: "proof-123", AggregateVerdict: api.ProofVerdictStale, CreatedAt: now, Candidate: api.ProofCandidateIdentity{RepositoryId: "repo", ChangeRequestId: "42", HeadSha: "abcdef1", PipelinePath: "pipelines/orders.yaml", ProjectSlug: "ops", EnvironmentSlug: "prod", TargetRef: "main", SpecHash: "sha256:spec", BindingFingerprint: "sha256:binding", CompilerVersion: "v1", PolicyVersion: "v1"}, Claims: []api.ProofClaim{{Identity: api.ProofClaimIdentity{Family: api.ProofClaimFamilyFieldsTypes, SemanticPath: "/source"}, Description: "fields and types match", RequiredEvidenceLevel: api.ProofEvidenceLevelLiveReadOnly, ActualEvidenceLevel: &actual, Verdict: api.ProofVerdictStale, ObservedAt: &now, ExpiresAt: &now, Evidence: []api.ProofEvidenceReference{{Kind: api.ProofEvidenceReferenceKindIntrospection, Digest: "sha256:evidence"}}, Diagnostics: []api.ProofDiagnostic{{Code: "EVIDENCE_STALE", Summary: "Schema evidence expired", Remediation: "Refresh introspection"}}}}}

	var out bytes.Buffer
	require.NoError(t, ProofReceiptPageContent(receipt, "").Render(WithEntityPageURL(context.Background(), "/?section=claims"), &out))
	html := out.String()
	for _, text := range []string{"stale", "Cached observed", "Live read only", "Schema evidence expired", "Next: Refresh introspection", "sha256:evidence", "sm:grid-cols-2"} {
		require.Contains(t, html, text)
	}
}

func TestProofReceiptContentShowsTargetMismatchPartialEvidenceAndSafeLinks(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	pipelineSlug, runSlug := api.Slug("orders"), api.Slug("run-123")
	selectedHash := "sha256:selected"
	receipt := &api.ProofReceipt{
		Id: "proof-123", AggregateVerdict: api.ProofVerdictInconclusive, CreatedAt: now, RetainUntil: now.Add(24 * time.Hour),
		Candidate: api.ProofCandidateIdentity{
			OrganizationSlug: "acme", ProjectSlug: "ops", EnvironmentSlug: "prod", ProviderConnectionSlug: "github-prod",
			RepositoryId: "repo", ChangeRequestId: "42", HeadSha: "abcdef1", PipelinePath: "pipelines/orders.yaml",
			TargetRef: "main", SpecHash: "sha256:candidate", BindingFingerprint: "sha256:binding", CapabilityFingerprint: "sha256:capability", CompilerVersion: "v1", PolicyVersion: "v1",
		},
		LifecycleLinks: []api.ProofLifecycleLink{
			{Kind: api.ProofLifecycleLinkKindDeployment, ReceiptId: "proof-123", Identifier: "orders:old", LinkedAt: now, RetainUntil: now.Add(24 * time.Hour), Status: api.ProofLifecycleLinkStatusVersionMismatch, PipelineSlug: &pipelineSlug, CurrentVersionSpecHash: &selectedHash},
			{Kind: api.ProofLifecycleLinkKindRun, ReceiptId: "proof-123", Identifier: "run-123", LinkedAt: now, RetainUntil: now.Add(24 * time.Hour), Status: api.ProofLifecycleLinkStatusMatched, RunSlug: &runSlug, CurrentVersionSpecHash: &selectedHash},
		},
		Claims: []api.ProofClaim{{
			Identity: api.ProofClaimIdentity{Family: api.ProofClaimFamilyAccessCapability, SemanticPath: "/source"}, Description: "provider access could not be verified",
			RequiredEvidenceLevel: api.ProofEvidenceLevelLiveReadOnly, Verdict: api.ProofVerdictInconclusive,
			Diagnostics: []api.ProofDiagnostic{{Code: "PROVIDER_UNAVAILABLE", Summary: "Provider connection failed", Remediation: "Retry the provider connection test"}},
		}},
	}

	var out bytes.Buffer
	for _, section := range []string{"overview", "claims", "technical"} {
		require.NoError(t, ProofReceiptPageContent(receipt, "").Render(WithEntityPageURL(t.Context(), "/?section="+section), &out))
	}
	html := out.String()
	for _, text := range []string{
		"Provider connection", "github-prod", "version mismatch", "Candidate version", "sha256:candidate", "Selected version", "sha256:selected",
		"Provider connection failed", "not observed", "Referenced deployment and run records", "sm:grid-cols-2", "focus-visible:outline",
		"/orgs/acme/projects/ops/pipelines/orders/deployment?environment_slug=prod", "/orgs/acme/projects/ops/runs/run-123?environment_slug=prod",
	} {
		require.Contains(t, html, text)
	}
}

func TestProofReceiptContentShowsNoLifecycleEvidence(t *testing.T) {
	receipt := &api.ProofReceipt{Id: "proof-123", Candidate: api.ProofCandidateIdentity{}, Claims: []api.ProofClaim{}}
	var out bytes.Buffer
	require.NoError(t, ProofReceiptPageContent(receipt, "").Render(context.Background(), &out))
	require.Contains(t, out.String(), "No deployment or run records reference this receipt yet.")
}

func TestProofFreshnessLabel(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)
	for _, tc := range []struct {
		name   string
		claims []api.ProofClaim
		want   string
	}{
		{"missing", nil, "No observed evidence"},
		{"observed without expiry", []api.ProofClaim{{ObservedAt: &now}}, "No evidence expiry recorded"},
		{"earliest expiry", []api.ProofClaim{{ExpiresAt: &later}, {ExpiresAt: &now}}, "Earliest expiry: 2026-09-19 12:00:00 UTC"},
		{"stale takes precedence", []api.ProofClaim{{ExpiresAt: &later}, {Verdict: api.ProofVerdictStale}}, "Stale evidence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ProofFreshnessLabel(api.ProofReceipt{Claims: tc.claims}))
		})
	}
}

func TestProofReceiptContentShowsUnavailableState(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, ProofReceiptPageContent(nil, "").Render(context.Background(), &out))
	require.Contains(t, out.String(), "receipt could not be displayed")
}

func TestProofReceiptMatchingEvidenceUsesOneLabel(t *testing.T) {
	level := api.ProofEvidenceLevelLiveReadOnly
	receipt := &api.ProofReceipt{Claims: []api.ProofClaim{{RequiredEvidenceLevel: level, ActualEvidenceLevel: &level, Verdict: api.ProofVerdictPassed}}}
	var out bytes.Buffer
	require.NoError(t, ProofReceiptPageContent(receipt, "").Render(WithEntityPageURL(t.Context(), "/?section=claims"), &out))
	require.Contains(t, out.String(), "Live read only")
	require.NotContains(t, out.String(), "Required evidence")
	require.NotContains(t, out.String(), "Actual evidence")
	require.Contains(t, out.String(), "Evidence details")
}

func TestProofReceiptSectionsAndScopedReturn(t *testing.T) {
	receipt := &api.ProofReceipt{Id: "receipt", Candidate: api.ProofCandidateIdentity{OrganizationSlug: "acme", ProjectSlug: "ops", EnvironmentSlug: "prod", RepositoryId: "private-repository", HeadSha: "exact-commit"}, Claims: []api.ProofClaim{{Description: "claim-description", Diagnostics: []api.ProofDiagnostic{{Summary: "claim-diagnostic", Remediation: "claim-remediation"}}, Evidence: []api.ProofEvidenceReference{{Digest: "evidence-digest"}}}}}
	list := ProofHistoryHref("acme", "ops", "prod", "a+b", "next")
	for _, section := range []string{"", "unknown", "overview", "claims", "technical"} {
		ctx := WithEntityPageURL(t.Context(), "/receipt?section="+section+"&list_return="+url.QueryEscape(list))
		require.Equal(t, list, proofHistoryBackHref(ctx, receipt))
		for _, next := range []string{"overview", "claims", "technical"} {
			parsed, err := url.Parse(proofReceiptSectionHref(ctx, receipt, next))
			require.NoError(t, err)
			require.Equal(t, list, parsed.Query().Get("list_return"))
			require.Equal(t, next, parsed.Query().Get("section"))
		}
		var out bytes.Buffer
		require.NoError(t, ProofReceiptPageContent(receipt, "").Render(ctx, &out))
		html := out.String()
		require.Equal(t, section == "technical", strings.Contains(html, "private-repository"))
		require.Equal(t, section == "claims", strings.Contains(html, "evidence-digest"))
		for _, detail := range []string{"claim-description", "claim-diagnostic", "claim-remediation"} {
			require.Equal(t, section == "claims", strings.Contains(html, detail), detail)
		}
		isOverview := section != "claims" && section != "technical"
		require.Equal(t, isOverview, strings.Contains(html, "1 total"))
		if isOverview {
			require.Contains(t, html, "Review claims and evidence")
			require.Contains(t, html, strings.ReplaceAll(proofReceiptSectionHref(ctx, receipt, "claims"), "&", "&amp;"))
		}
		require.Equal(t, section != "claims" && section != "technical", strings.Contains(html, "Evaluated candidate"))
	}
	for _, invalid := range []string{"https://example.com" + list, "/orgs/other/projects/ops/proof-receipts?environment_slug=prod", ProofHistoryHref("acme", "ops", "stage", "token", "next"), list + "#fragment"} {
		ctx := WithEntityPageURL(t.Context(), "/receipt?list_return="+url.QueryEscape(invalid))
		require.Equal(t, ProofHistoryHref("acme", "ops", "prod", "", ""), proofHistoryBackHref(ctx, receipt))
	}
}

func TestProofRecordedEvidenceScope(t *testing.T) {
	metadata, live := api.ProofEvidenceLevelStatic, api.ProofEvidenceLevelLiveReadOnly
	receipt := &api.ProofReceipt{Claims: []api.ProofClaim{{RequiredEvidenceLevel: live}, {ActualEvidenceLevel: &metadata}, {ActualEvidenceLevel: &live}}}
	require.Equal(t, "1 Not observed · 1 Live read only · 1 Static (metadata only)", ProofEvidenceScopeLabel(receipt))
	require.Equal(t, "No recorded evidence", ProofEvidenceScopeLabel(&api.ProofReceipt{}))
}

func TestProofHistoryPreservesPaginationAndAllVerdictsWithoutTechnicalIDs(t *testing.T) {
	shell := AuthenticatedShellData{ActiveOrganizationSlug: "acme", ActiveProjectSlug: "ops", ActiveEnvironmentSlug: "prod"}
	cursor := "next-page"
	history := &api.ProofReceiptListResponse{NextCursor: &cursor}
	for _, verdict := range []api.ProofVerdict{api.ProofVerdictPassed, api.ProofVerdictFailed, api.ProofVerdictStale, api.ProofVerdictInconclusive, api.ProofVerdictUnverified, api.ProofVerdictSuperseded} {
		history.Receipts = append(history.Receipts, api.ProofReceipt{Id: string(verdict), AggregateVerdict: verdict, Candidate: api.ProofCandidateIdentity{ProjectSlug: "ops", EnvironmentSlug: "prod", HeadSha: "full-commit-not-in-list", RepositoryId: "repository-not-in-list", ProviderConnectionSlug: "connection-not-in-list", TargetRef: "main"}})
	}
	list := ProofHistoryHref("acme", "ops", "prod", "older", "next")
	var out bytes.Buffer
	require.NoError(t, ProofHistoryContent(shell, history).Render(WithEntityPageURL(t.Context(), list), &out))
	html := out.String()
	for _, verdict := range []string{"passed", "failed", "stale", "inconclusive", "unverified", "superseded"} {
		require.Contains(t, html, verdict)
	}
	for _, identifier := range []string{"full-commit-not-in-list", "repository-not-in-list", "connection-not-in-list"} {
		require.NotContains(t, html, identifier)
	}
	require.Contains(t, html, "list_return="+url.QueryEscape(list))
	require.Contains(t, html, "cursor=next-page&amp;direction=next&amp;environment_slug=prod")
}
