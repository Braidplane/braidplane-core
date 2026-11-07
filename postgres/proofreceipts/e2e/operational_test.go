package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"
	"github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/postgres/retention"
	"github.com/stretchr/testify/require"
)

func TestOperationalReceiptsPreserveStaticHistoryAndIndependentRetention(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "operational Proof storage and retention")
	org := slug.Slug(fmt.Sprintf("proof-operational-%d", time.Now().UnixNano()))
	other := slug.Slug(org.String() + "-other")
	seedTarget(t, ctx, pool, org)
	seedTarget(t, ctx, pool, other)
	seedSiblingScopes(t, ctx, pool, org)
	t.Cleanup(func() { cleanup(t, pool, org, other) })
	installation := e2edb.EnableGitHubProof(t, pool, org, "github", "42")
	candidates := proofcandidates.New()
	mapping, err := candidates.CreateMapping(ctx, pool, proofcandidates.CreateMappingParams{
		OrganizationSlug: org, ProjectSlug: "data", EnvironmentSlug: "staging", ProviderConnectionSlug: "github",
		Provider: "github", ProviderConnectionID: installation, RepositoryID: "42", TargetRef: "refs/heads/main", PipelinePath: "pipelines/acme.pipeline.json",
	})
	require.NoError(t, err)
	canonical := json.RawMessage(`{"metadata":{"slug":"acme","name":"Acme"},"steps":[{"kind":"query","slug":"extract","query":{"data_source_ref":"source","operation_type":"sql","operation_ref":"select"}}]}`)
	_, err = candidates.Create(ctx, pool, org, proofcandidates.CreateCandidateParams{
		MappingID: mapping, Slug: "candidate-1", CandidateKey: "operational-candidate", ChangeRequestID: "7", SourceRef: "feature",
		HeadSHA: strings.Repeat("a", 40), SourceSHA256: digest("source"), SpecHash: digest(string(canonical)), CanonicalSpec: canonical,
	})
	require.NoError(t, err)
	repo := proofreceipts.New()
	target, err := repo.FindCandidateTarget(ctx, pool, org, "candidate-1")
	require.NoError(t, err)
	evidence, err := repo.CreateEvidence(ctx, pool, target, proofreceipts.EvidenceParams{Kind: "validation", Digest: digest("static-evidence"), SourceIdentifier: "candidate-1", BindingFingerprint: digest("binding"), CapabilityFingerprint: digest("capability"), ObservedAt: time.Now().UTC()})
	require.NoError(t, err)
	static := newReceipt(t, target, "static-proof", digest("binding"), digest("capability"), evidence.Digest, nil)
	_, err = repo.Create(ctx, pool, target, proofreceipts.CreateReceiptParams{EvidenceID: evidence.ID, ReceiptKey: "static-key", Receipt: static})
	require.NoError(t, err)
	operational := static
	operational.ID, operational.Claims = "operational-proof", append([]model.ProofClaim(nil), static.Claims...)
	operational.CreatedAt = static.CreatedAt.Add(time.Second)
	operational.RetainUntil = operational.CreatedAt.Add(7 * 24 * time.Hour)
	level := model.ProofEvidenceLevelOperational
	operational.Claims[0].RequiredEvidenceLevel, operational.Claims[0].ActualEvidenceLevel = level, &level
	operational.Claims[0].Identity.Family = model.ProofClaimFamilyFieldsTypes
	operational.Claims[0].Identity.SemanticPath = "/operational/checks/" + strings.Repeat("c", 64)
	operational.Claims[0].Evidence = []model.ProofEvidenceReference{{Kind: model.ProofEvidenceReferenceKindExecution, Digest: digest("retained-check")}}
	operational.Operational = &model.ProofOperationalCapture{
		SourceReceiptID: static.ID, PolicyVersion: "proof-operational-structural-policy-v1", RecordDigest: digest("retained-check"), ExpiresAt: static.CreatedAt.Add(24 * time.Hour),
		Selection: model.ProofOperationalSelection{
			Candidate: static.Candidate, CandidateSlug: "candidate-1", Pipeline: "acme", Run: "run-1", Attempt: 1, VersionCommit: static.Candidate.HeadSHA,
			CompiledPlanDigest: digest("compiled"), DeploymentResolutionDigest: digest("resolution"), CheckID: strings.Repeat("c", 64), Step: "extract", Origin: "step", Phase: "producer_output", Declaration: "step/producer_output", ProjectionDigest: strings.Repeat("d", 64),
			Window: model.StructuralCheckWindow{Kind: "full", SourceSteps: []slug.Slug{"extract"}}, Evaluator: "braidplane/structural-runtime/v1", EvaluatorPolicy: "braidplane/structural-policy/v1", StartedAt: &static.CreatedAt, CompletedAt: &static.CreatedAt, EvidenceDigest: strings.Repeat("e", 64),
		},
	}
	require.NoError(t, operational.Validate())
	created, err := repo.Create(ctx, pool, target, proofreceipts.CreateReceiptParams{EvidenceID: evidence.ID, ReceiptKey: "operational-key", Receipt: operational})
	require.NoError(t, err)
	require.Equal(t, operational.ID, created.Value.ID)
	latest, err := repo.FindLatestForCandidate(ctx, pool, org, "candidate-1")
	require.NoError(t, err)
	require.Equal(t, static.ID, latest.Value.ID)
	forChange, err := repo.FindLatest(ctx, pool, target, "next-static-key")
	require.NoError(t, err)
	require.Equal(t, static.ID, forChange.Slug)
	original, err := repo.FindBySlug(ctx, pool, org, "data", "staging", static.ID)
	require.NoError(t, err)
	require.False(t, original.Superseded)
	require.Equal(t, static, original.Value)
	publication, err := repo.PublicationEnabled(ctx, pool, org, operational.ID)
	require.NoError(t, err)
	require.False(t, publication, "operational receipts cannot enter the Git publication path")
	for _, scope := range []struct{ org, project, environment slug.Slug }{{other, "data", "staging"}, {org, "other-data", "staging"}, {org, "data", "other-staging"}} {
		_, err := repo.FindBySlug(ctx, pool, scope.org, scope.project, scope.environment, operational.ID)
		require.ErrorIs(t, err, proofreceipts.ErrReceiptNotFound)
	}
	// Idempotent replay cannot renew the captured receipt or its expiry.
	replayedDraft := operational
	replayedDraft.CreatedAt, replayedDraft.RetainUntil = operational.CreatedAt.Add(time.Hour), operational.RetainUntil.Add(time.Hour)
	replayed, err := repo.Create(ctx, pool, target, proofreceipts.CreateReceiptParams{EvidenceID: evidence.ID, ReceiptKey: "operational-key", Receipt: replayedDraft})
	require.NoError(t, err)
	require.Equal(t, created.Value, replayed.Value)
	// The protected run reader must scope the saved version and resolution too.
	seedProofRuntime(t, ctx, pool, org, strings.TrimPrefix(target.SpecHash, "sha256:"), target.HeadSHA, "running")

	_, err = repo.LinkDeployment(ctx, pool, org, "data", "staging", "acme", proofreceipts.LinkDeploymentParams{ReceiptSlug: operational.ID, Identifier: "operational-deployment", SpecHash: strings.TrimPrefix(target.SpecHash, "sha256:"), LinkedAt: time.Now().UTC()})
	require.ErrorIs(t, err, proofreceipts.ErrLifecycleLinkNotFound, "operational evidence cannot become static deployment provenance")
	_, err = compiledplans.NewRepository().Create(ctx, pool, org, "data", compiledplans.CreateParams{PipelineSlug: "acme", Digest: digest("compiled"), AuthoredSpecHash: strings.TrimPrefix(target.SpecHash, "sha256:"), FormatVersion: 3, Materialization: []byte(`{}`)})
	require.NoError(t, err)
	_, err = deploymentresolutions.NewRepository().Create(ctx, pool, org, "data", "staging", "acme", digest("compiled"), digest("resolution"), []byte(`{}`))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE pipeline_runs SET status='succeeded', deployment_resolution_digest=$1, ended_at=statement_timestamp()-interval '2 days' WHERE project_id=$2 AND run_slug='run-1'`, digest("resolution"), target.ProjectID)
	require.NoError(t, err)
	retained, err := repo.FindOperationalRun(ctx, pool, org, "data", "staging", "run-1")
	require.NoError(t, err)
	require.Equal(t, target.HeadSHA, retained.VersionCommit)
	for _, scope := range []struct{ org, project, environment slug.Slug }{{other, "data", "staging"}, {org, "other-data", "staging"}, {org, "data", "other-staging"}} {
		_, err := repo.FindOperationalRun(ctx, pool, scope.org, scope.project, scope.environment, "run-1")
		require.ErrorIs(t, err, proofreceipts.ErrReceiptNotFound)
	}
	deleted, err := retention.New().DeletePipelineRunsBefore(ctx, pool, org, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	_, err = repo.FindOperationalRun(ctx, pool, org, "data", "staging", "run-1")
	require.ErrorIs(t, err, proofreceipts.ErrReceiptNotFound)
	_, err = repo.FindBySlug(ctx, pool, org, "data", "staging", operational.ID)
	require.NoError(t, err, "linking must not cascade away historical captures with source run pruning")
	_, err = pool.Exec(ctx, `UPDATE proof_receipts SET created_at=statement_timestamp()-interval '8 days', retain_until=statement_timestamp()+interval '30 days' WHERE organization_id=$1 AND slug=$2`, target.OrganizationID, operational.ID)
	require.NoError(t, err)
	_, err = repo.FindBySlug(ctx, pool, org, "data", "staging", operational.ID)
	require.ErrorIs(t, err, proofreceipts.ErrReceiptNotFound, "the independent seven-day cap cannot be extended")
}
