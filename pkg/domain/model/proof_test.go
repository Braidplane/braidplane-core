package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOperationalEvidenceDoesNotSatisfyTheNoWriteLadder(t *testing.T) {
	t.Parallel()
	for _, level := range []ProofEvidenceLevel{ProofEvidenceLevelStatic, ProofEvidenceLevelCachedObserved, ProofEvidenceLevelLiveReadOnly, ProofEvidenceLevelBoundedExecution} {
		require.False(t, ProofEvidenceLevelSatisfies(ProofEvidenceLevelOperational, level))
		require.False(t, ProofEvidenceLevelSatisfies(level, ProofEvidenceLevelOperational))
	}
	require.True(t, ProofEvidenceLevelSatisfies(ProofEvidenceLevelOperational, ProofEvidenceLevelOperational))
	require.True(t, ProofEvidenceLevelSatisfies(ProofEvidenceLevelBoundedExecution, ProofEvidenceLevelLiveReadOnly))
}

func TestProofReceiptContract(t *testing.T) {
	t.Parallel()

	newDraft := func() ProofReceipt {
		observedAt := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
		expiresAt := observedAt.Add(time.Hour)
		level := ProofEvidenceLevelStatic
		specHash := "sha256:" + strings.Repeat("a", 64)
		return ProofReceipt{
			SchemaVersion: ProofReceiptSchemaVersion,
			ID:            "proof-1",
			Candidate: ProofCandidateIdentity{
				OrganizationSlug:       "acme",
				ProjectSlug:            "billing",
				EnvironmentSlug:        "production",
				ProviderConnectionSlug: "github",
				RepositoryID:           "repo-1",
				ChangeRequestID:        "42",
				HeadSHA:                strings.Repeat("1", 40),
				TargetRef:              "refs/heads/main",
				PipelinePath:           "pipelines/orders.yaml",
				SpecHash:               specHash,
				BindingFingerprint:     "sha256:" + strings.Repeat("b", 64),
				CapabilityFingerprint:  "sha256:" + strings.Repeat("c", 64),
				CompilerVersion:        "v1",
				PolicyVersion:          "v1",
			},
			Claims: []ProofClaim{{
				Identity: ProofClaimIdentity{
					Family:              ProofClaimFamilyFieldsTypes,
					PipelineVersionHash: specHash,
					SemanticPath:        "/steps/extract/output/fields/customer_id",
				},
				Description:           "token-secret must match the declared type",
				RequiredEvidenceLevel: ProofEvidenceLevelStatic,
				ActualEvidenceLevel:   &level,
				ObservedAt:            &observedAt,
				ExpiresAt:             &expiresAt,
				Verdict:               ProofVerdictPassed,
				Evidence: []ProofEvidenceReference{{
					Kind:   ProofEvidenceReferenceKindValidation,
					Digest: "sha256:" + strings.Repeat("d", 64),
				}},
				Diagnostics: []ProofDiagnostic{{
					Code:        "contract.matches",
					Summary:     "request_body token-secret Authorization=token-secret query=token-secret full_provider_response=token-secret",
					Remediation: "refresh token-secret evidence",
				}},
			}},
			AggregateVerdict: ProofVerdictPassed,
			CreatedAt:        observedAt,
			RetainUntil:      observedAt.Add(30 * 24 * time.Hour),
		}
	}

	t.Run("redacts strings and renders a fixed Git summary", func(t *testing.T) {
		t.Parallel()

		draft := newDraft()
		receipt, err := NewProofReceipt(draft, func(value string) string {
			return strings.ReplaceAll(value, "token-secret", "[REDACTED]")
		})
		require.NoError(t, err)

		raw, err := json.Marshal(receipt)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "token-secret")
		require.Contains(t, string(raw), "[REDACTED]")
		require.NotContains(t, receipt.GitSummary(), "request_body")
		require.Equal(t, "Braidplane Proof (informational): passed; passed=1, failed=0, unverified=0, inconclusive=0, stale=0", receipt.GitSummary())
	})

	t.Run("rejects an unsupported schema version", func(t *testing.T) {
		t.Parallel()

		draft := newDraft()
		draft.SchemaVersion++
		_, err := NewProofReceipt(draft, strings.Clone)
		require.ErrorContains(t, err, "unsupported proof receipt schema version")
	})

	t.Run("rejects a non-canonical head SHA", func(t *testing.T) {
		t.Parallel()

		draft := newDraft()
		draft.Candidate.HeadSHA = strings.Repeat("A", 40)
		_, err := NewProofReceipt(draft, strings.Clone)
		require.ErrorContains(t, err, "proof candidate hashes and fingerprints are invalid")
	})

	t.Run("rejects an invalid evidence digest", func(t *testing.T) {
		t.Parallel()

		draft := newDraft()
		draft.Claims[0].Evidence[0].Digest = "https://provider.example/raw?token=token-secret"
		_, err := NewProofReceipt(draft, strings.Clone)
		require.ErrorContains(t, err, "evidence reference is invalid")
	})

	t.Run("rejects an incomplete candidate", func(t *testing.T) {
		t.Parallel()

		draft := newDraft()
		draft.Candidate.OrganizationSlug = ""
		_, err := NewProofReceipt(draft, strings.Clone)
		require.ErrorContains(t, err, "candidate identity is incomplete")
	})

	t.Run("rejects insufficient actual evidence on a passed claim", func(t *testing.T) {
		t.Parallel()

		draft := newDraft()
		draft.Claims[0].RequiredEvidenceLevel = ProofEvidenceLevelLiveReadOnly
		require.ErrorContains(t, draft.Validate(), "does not meet its required evidence level")
	})

	t.Run("rejects a failed claim without actual evidence", func(t *testing.T) {
		t.Parallel()

		draft := newDraft()
		draft.Claims[0].ActualEvidenceLevel = nil
		draft.Claims[0].Verdict = ProofVerdictFailed
		draft.AggregateVerdict = ProofVerdictFailed
		require.ErrorContains(t, draft.Validate(), "requires an actual evidence level")
	})

	t.Run("rejects a stale claim without expiry", func(t *testing.T) {
		t.Parallel()

		draft := newDraft()
		draft.Claims[0].ExpiresAt = nil
		draft.Claims[0].Verdict = ProofVerdictStale
		draft.AggregateVerdict = ProofVerdictStale
		require.ErrorContains(t, draft.Validate(), "requires expired observed evidence")
	})

	t.Run("rejects a stale claim with unexpired evidence", func(t *testing.T) {
		t.Parallel()

		draft := newDraft()
		draft.Claims[0].Verdict = ProofVerdictStale
		draft.AggregateVerdict = ProofVerdictStale
		require.ErrorContains(t, draft.Validate(), "has not expired")
	})

	t.Run("rejects duplicate claim identities", func(t *testing.T) {
		t.Parallel()

		draft := newDraft()
		draft.Claims = append(draft.Claims, draft.Claims[0])
		require.ErrorContains(t, draft.Validate(), "identity is duplicated")
	})

	t.Run("rejects a self-superseding receipt", func(t *testing.T) {
		t.Parallel()

		draft := newDraft()
		draft.SupersedesReceiptID = &draft.ID
		require.ErrorContains(t, draft.Validate(), "cannot supersede itself")
	})

	t.Run("rejects retention before creation", func(t *testing.T) {
		t.Parallel()

		draft := newDraft()
		draft.RetainUntil = draft.CreatedAt.Add(-time.Second)
		require.ErrorContains(t, draft.Validate(), "retention window is invalid")
	})

	t.Run("rejects a mismatched aggregate and constructs the derived aggregate", func(t *testing.T) {
		t.Parallel()

		draft := newDraft()
		draft.Claims = append(draft.Claims, ProofClaim{
			Identity: ProofClaimIdentity{
				Family:              ProofClaimFamilyReplay,
				PipelineVersionHash: draft.Candidate.SpecHash,
				SemanticPath:        "/steps/load/replay",
			},
			Description:           "provider did not expose replay semantics",
			RequiredEvidenceLevel: ProofEvidenceLevelLiveReadOnly,
			Verdict:               ProofVerdictUnverified,
		})
		require.ErrorContains(t, draft.Validate(), "aggregate verdict does not match")

		receipt, err := NewProofReceipt(draft, strings.Clone)
		require.NoError(t, err)
		require.Equal(t, ProofVerdictUnverified, receipt.AggregateVerdict)
	})
}
