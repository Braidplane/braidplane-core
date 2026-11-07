package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
)

func TestRepositoryEnforcesTenantScopedIdempotentSupersession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-Postgres proof receipt repository test in short mode")
	}
	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "proof receipt tenant boundary integration test")
	orgA := slug.Slug(fmt.Sprintf("proof-receipt-a-%d", time.Now().UnixNano()))
	orgB := slug.Slug(fmt.Sprintf("proof-receipt-b-%d", time.Now().UnixNano()))
	seedTarget(t, ctx, pool, orgA)
	seedTarget(t, ctx, pool, orgB)
	seedSiblingScopes(t, ctx, pool, orgA)
	t.Cleanup(func() { cleanup(t, pool, orgA, orgB) })

	installationID := e2edb.EnableGitHubProof(t, pool, orgA, "github", "42")
	candidates := proofcandidates.New()
	mappingID, err := candidates.CreateMapping(ctx, pool, proofcandidates.CreateMappingParams{
		OrganizationSlug: orgA, ProjectSlug: "data", EnvironmentSlug: "staging", ProviderConnectionSlug: "github",
		Provider: "github", ProviderConnectionID: installationID, RepositoryID: "42",
		TargetRef: "refs/heads/main", PipelinePath: "pipelines/acme.pipeline.json",
	})
	require.NoError(t, err)
	canonical := []byte(`{"metadata":{"slug":"acme","name":"Acme"},"steps":[{"kind":"query","slug":"extract","query":{"data_source_ref":"source","operation_type":"sql","operation_ref":"select"}}]}`)
	specSum := sha256.Sum256(canonical)
	specHash := "sha256:" + hex.EncodeToString(specSum[:])
	candidate, err := candidates.Create(ctx, pool, orgA, proofcandidates.CreateCandidateParams{
		MappingID: mappingID, Slug: "candidate-1", CandidateKey: "candidate-key-1", ChangeRequestID: "7",
		SourceRef: "refs/heads/feature", HeadSHA: strings.Repeat("a", 40), SourceSHA256: digest("source"),
		SpecHash: specHash, CanonicalSpec: canonical,
	})
	require.NoError(t, err)

	repo := proofreceipts.New()
	target, err := repo.FindCandidateTarget(ctx, pool, orgA, "candidate-1")
	require.NoError(t, err)
	_, err = repo.FindCandidateTarget(ctx, pool, orgB, "candidate-1")
	require.ErrorIs(t, err, proofreceipts.ErrCandidateNotFound)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	require.NoError(t, repo.LockTarget(ctx, tx, orgA.String()+":7"))
	evidence, err := repo.CreateEvidence(ctx, tx, target, proofreceipts.EvidenceParams{
		Kind: "validation", Digest: digest("evidence-1"), SourceIdentifier: "validation-1",
		BindingFingerprint: digest("binding"), CapabilityFingerprint: digest("capability"), ObservedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	first := newReceipt(t, target, "proof-1", digest("binding"), digest("capability"), evidence.Digest, nil)
	created, err := repo.Create(ctx, tx, target, proofreceipts.CreateReceiptParams{EvidenceID: evidence.ID, ReceiptKey: digest("receipt-1"), Receipt: first})
	require.NoError(t, err)
	require.Equal(t, first.ID, created.Value.ID)
	replayed, err := repo.Create(ctx, tx, target, proofreceipts.CreateReceiptParams{EvidenceID: evidence.ID, ReceiptKey: digest("receipt-1"), Receipt: first})
	require.NoError(t, err)
	require.Equal(t, created.Value.ID, replayed.Value.ID)

	latest, err := repo.FindLatest(ctx, tx, target, digest("receipt-2"))
	require.NoError(t, err)
	require.Equal(t, first.ID, latest.Slug)
	second := newReceipt(t, target, "proof-2", digest("binding"), digest("capability"), digest("evidence-2"), &latest.Slug)
	compiledAt := time.Now().UTC()
	expiredAt := compiledAt.Add(-time.Hour)
	evidence2, err := repo.CreateEvidence(ctx, tx, target, proofreceipts.EvidenceParams{
		Kind: "validation", Digest: digest("evidence-2"), SourceIdentifier: "validation-2",
		BindingFingerprint: digest("binding"), CapabilityFingerprint: digest("capability"), ObservedAt: compiledAt, ExpiresAt: &expiredAt,
	})
	require.NoError(t, err)
	_, err = repo.Create(ctx, tx, target, proofreceipts.CreateReceiptParams{EvidenceID: evidence2.ID, ReceiptKey: digest("receipt-2"), Receipt: second, Supersedes: latest})
	require.NoError(t, err)

	_, err = candidates.Create(ctx, tx, orgA, proofcandidates.CreateCandidateParams{
		MappingID: mappingID, Slug: "candidate-2", CandidateKey: "candidate-key-2", ChangeRequestID: "7",
		SourceRef: "refs/heads/feature", HeadSHA: strings.Repeat("b", 40), SourceSHA256: digest("source-2"),
		SpecHash: specHash, CanonicalSpec: canonical, SupersedesCandidateID: &candidate.ID,
	})
	require.NoError(t, err)
	newerTarget, err := repo.FindCandidateTarget(ctx, tx, orgA, "candidate-2")
	require.NoError(t, err)
	newerEvidence, err := repo.CreateEvidence(ctx, tx, newerTarget, proofreceipts.EvidenceParams{
		Kind: "validation", Digest: digest("evidence-3"), SourceIdentifier: "validation-3",
		BindingFingerprint: digest("binding"), CapabilityFingerprint: digest("capability"), ObservedAt: compiledAt,
	})
	require.NoError(t, err)
	newerLatest, err := repo.FindLatest(ctx, tx, newerTarget, digest("receipt-3"))
	require.NoError(t, err)
	require.Equal(t, second.ID, newerLatest.Slug)
	third := newReceipt(t, newerTarget, "proof-3", digest("binding"), digest("capability"), newerEvidence.Digest, &newerLatest.Slug)
	_, err = repo.Create(ctx, tx, newerTarget, proofreceipts.CreateReceiptParams{EvidenceID: newerEvidence.ID, ReceiptKey: digest("receipt-3"), Receipt: third, Supersedes: newerLatest})
	require.NoError(t, err)
	newerCandidateLatest, err := repo.FindLatest(ctx, tx, newerTarget, digest("receipt-4"))
	require.NoError(t, err)
	require.Equal(t, third.ID, newerCandidateLatest.Slug)

	oldCandidateLatest, err := repo.FindLatest(ctx, tx, target, digest("receipt-5"))
	require.NoError(t, err)
	require.Equal(t, second.ID, oldCandidateLatest.Slug, "an older candidate must not supersede a newer candidate receipt")
	require.NoError(t, tx.Commit(ctx))

	old, err := repo.FindBySlug(ctx, pool, orgA, "data", "staging", "proof-1")
	require.NoError(t, err)
	require.True(t, old.Superseded)
	_, err = repo.FindBySlug(ctx, pool, orgB, "data", "staging", "proof-1")
	require.ErrorIs(t, err, proofreceipts.ErrReceiptNotFound)

	page, err := repo.List(ctx, pool, orgA, "data", "staging", model.ListRequest{Limit: 2, Order: model.ListOrderDesc})
	require.NoError(t, err)
	require.Len(t, page, 2)
	require.Equal(t, slug.Slug("proof-3"), page[0].Value.ID)
	require.Equal(t, slug.Slug("proof-2"), page[1].Value.ID)
	cursor := model.EncodeCursor(page[1].Value.CreatedAt.Format(time.RFC3339Nano), page[1].Value.ID.String())
	nextPage, err := repo.List(ctx, pool, orgA, "data", "staging", model.ListRequest{Limit: 2, Order: model.ListOrderDesc, Cursor: *cursor})
	require.NoError(t, err)
	require.Len(t, nextPage, 1)
	require.Equal(t, slug.Slug("proof-1"), nextPage[0].Value.ID)
	otherTenantPage, err := repo.List(ctx, pool, orgB, "data", "staging", model.ListRequest{Limit: 2, Order: model.ListOrderDesc})
	require.NoError(t, err)
	require.Empty(t, otherTenantPage)
	wrongProjectPage, err := repo.List(ctx, pool, orgA, "other-data", "staging", model.ListRequest{Limit: 2, Order: model.ListOrderDesc})
	require.NoError(t, err)
	require.Empty(t, wrongProjectPage)
	wrongEnvironmentPage, err := repo.List(ctx, pool, orgA, "data", "other-staging", model.ListRequest{Limit: 2, Order: model.ListOrderDesc})
	require.NoError(t, err)
	require.Empty(t, wrongEnvironmentPage)
	reverseCursor := model.EncodeCursor(nextPage[0].Value.CreatedAt.Format(time.RFC3339Nano), nextPage[0].Value.ID.String())
	reversePage, err := repo.List(ctx, pool, orgA, "data", "staging", model.ListRequest{Limit: 2, Order: model.ListOrderAsc, Cursor: *reverseCursor})
	require.NoError(t, err)
	require.Len(t, reversePage, 2)
	require.Equal(t, slug.Slug("proof-2"), reversePage[0].Value.ID)
	require.Equal(t, slug.Slug("proof-3"), reversePage[1].Value.ID)

	deploymentID := seedProofRuntime(t, ctx, pool, orgA, specHash, strings.Repeat("b", 40), "succeeded")
	linkedAt := time.Now().UTC()
	link, err := repo.LinkDeployment(ctx, pool, orgA, "data", "staging", "acme", proofreceipts.LinkDeploymentParams{
		ReceiptSlug: "proof-3", Identifier: "acme:" + specHash, SpecHash: specHash, LinkedAt: linkedAt,
	})
	require.NoError(t, err)
	require.Equal(t, slug.Slug("proof-3"), link.ReceiptSlug)
	require.Equal(t, specHash, link.SpecHash)
	link, err = repo.LinkDeployment(ctx, pool, orgA, "data", "staging", "acme", proofreceipts.LinkDeploymentParams{
		ReceiptSlug: "proof-3", Identifier: "acme:" + specHash, SpecHash: specHash, LinkedAt: linkedAt.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, linkedAt.UnixMicro(), link.LinkedAt.UnixMicro())

	_, err = repo.LinkDeployment(ctx, pool, orgB, "data", "staging", "acme", proofreceipts.LinkDeploymentParams{
		ReceiptSlug: "proof-3", Identifier: "wrong-tenant", SpecHash: specHash, LinkedAt: linkedAt,
	})
	require.ErrorIs(t, err, proofreceipts.ErrLifecycleLinkNotFound)

	_, err = repo.LinkRun(ctx, pool, orgA, "data", "staging", "run-1", deploymentID, "later-redeployment", linkedAt.Add(time.Second))
	require.ErrorIs(t, err, proofreceipts.ErrLifecycleLinkNotFound)
	runLink, err := repo.LinkRun(ctx, pool, orgA, "data", "staging", "run-1", deploymentID, "acme:"+specHash, linkedAt.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, model.ProofLifecycleLinkKindRun, runLink.Kind)
	require.Equal(t, slug.Slug("proof-3"), runLink.ReceiptSlug)

	links, err := repo.ListLifecycleLinksByReceipt(ctx, pool, orgA, "data", "staging", "proof-3")
	require.NoError(t, err)
	require.Len(t, links, 2)
	require.Equal(t, slug.Slug("acme"), *links[0].PipelineSlug)
	require.Equal(t, specHash, links[0].CurrentSpecHash)
	require.Equal(t, slug.Slug("run-1"), *links[1].RunSlug)
	otherTenantLinks, err := repo.ListLifecycleLinksByReceipt(ctx, pool, orgB, "data", "staging", "proof-3")
	require.NoError(t, err)
	require.Empty(t, otherTenantLinks)

	// Expiry hides links immediately and prevents even idempotent link writes.
	_, err = pool.Exec(ctx, `UPDATE proof_receipts SET retain_until=statement_timestamp() WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug='proof-3'`, orgA)
	require.NoError(t, err)
	_, err = repo.LinkDeployment(ctx, pool, orgA, "data", "staging", "acme", proofreceipts.LinkDeploymentParams{ReceiptSlug: "proof-3", Identifier: "acme:" + specHash, SpecHash: specHash, LinkedAt: linkedAt})
	require.ErrorIs(t, err, proofreceipts.ErrLifecycleLinkNotFound)
	_, err = repo.LinkRun(ctx, pool, orgA, "data", "staging", "run-1", deploymentID, "acme:"+specHash, linkedAt)
	require.ErrorIs(t, err, proofreceipts.ErrLifecycleLinkNotFound)
	_, err = repo.FindDeploymentLink(ctx, pool, orgA, "data", "staging", "acme")
	require.ErrorIs(t, err, proofreceipts.ErrLifecycleLinkNotFound)
	_, err = repo.FindRunLink(ctx, pool, orgA, "data", "staging", "run-1")
	require.ErrorIs(t, err, proofreceipts.ErrLifecycleLinkNotFound)
	page, err = repo.List(ctx, pool, orgA, "data", "staging", model.ListRequest{Limit: 2, Order: model.ListOrderDesc})
	require.NoError(t, err)
	require.Len(t, page, 2, "expiry is filtered before pagination")
	tx, err = pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	require.NoError(t, repo.LockTarget(ctx, tx, "proof-retention:"+orgA.String()))
	deleted, err := repo.DeleteExpired(ctx, tx, orgA)
	require.NoError(t, err)
	require.Len(t, deleted, 1)
	require.Equal(t, slug.Slug("proof-3"), deleted[0].Slug)
	require.NoError(t, tx.Commit(ctx))
	var linkCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM proof_lifecycle_links WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, orgA).Scan(&linkCount))
	require.Zero(t, linkCount, "physical receipt deletion cascades both lifecycle links")
}

func seedProofRuntime(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org slug.Slug, specHash, headSHA, status string) int64 {
	t.Helper()
	var deploymentID int64
	_, err := pool.Exec(ctx, `
		WITH project AS (
			SELECT p.id FROM projects p JOIN organizations o ON o.id = p.organization_id WHERE o.slug = $1 AND p.slug = 'data'
		)
		INSERT INTO pipeline_specs (project_id, slug, name, spec_jsonb, spec_hash, source_path, source_ref, last_applied_commit)
		SELECT id, 'acme', 'Acme', '{}', $2, 'pipelines/acme.pipeline.json', 'refs/heads/main', $3 FROM project
	`, org, specHash, headSHA)
	require.NoError(t, err)
	var versionID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO pipeline_versions (project_id, pipeline_slug, git_commit_sha, spec_jsonb, spec_hash)
		SELECT p.id, 'acme', $3, '{}', $2 FROM projects p JOIN organizations o ON o.id = p.organization_id WHERE o.slug = $1 AND p.slug = 'data'
		RETURNING id
	`, org, specHash, headSHA).Scan(&versionID)
	require.NoError(t, err)
	err = pool.QueryRow(ctx, `
		INSERT INTO pipeline_deployments (project_id, project_environment_id, pipeline_slug, current_version_id, deployed_by_actor_type)
		SELECT p.id, pe.id, 'acme', $2, 'member'
		FROM project_environments pe JOIN projects p ON p.id = pe.project_id JOIN organizations o ON o.id = p.organization_id
		JOIN environments e ON e.id = pe.environment_id WHERE o.slug = $1 AND p.slug = 'data' AND e.slug = 'staging'
		RETURNING id
	`, org, versionID).Scan(&deploymentID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO pipeline_runs (project_id, project_environment_id, pipeline_version_id, pipeline_version_spec_hash, api_version, pipeline_slug, run_slug, source_environment_slug, status, duration_ms, step_slug, timeline)
		SELECT p.id, pe.id, $2, $3, 'braidplane.com/pipeline/v0alpha1', 'acme', 'run-1', 'staging', $4, 1, '', '{}'
		FROM project_environments pe JOIN projects p ON p.id = pe.project_id JOIN organizations o ON o.id = p.organization_id
		JOIN environments e ON e.id = pe.environment_id WHERE o.slug = $1 AND p.slug = 'data' AND e.slug = 'staging'
	`, org, versionID, specHash, status)
	require.NoError(t, err)
	return deploymentID
}

func newReceipt(t *testing.T, target *proofreceipts.CandidateTarget, id slug.Slug, binding, capability, evidence string, supersedes *slug.Slug) model.ProofReceipt {
	t.Helper()
	now := time.Now().UTC()
	level := model.ProofEvidenceLevelStatic
	receipt, err := model.NewProofReceipt(model.ProofReceipt{
		SchemaVersion: model.ProofReceiptSchemaVersion, ID: id,
		Candidate: model.ProofCandidateIdentity{
			OrganizationSlug: target.OrganizationSlug, ProjectSlug: target.ProjectSlug, EnvironmentSlug: target.EnvironmentSlug,
			ProviderConnectionSlug: target.ProviderConnectionSlug, RepositoryID: target.RepositoryID, ChangeRequestID: target.ChangeRequestID,
			HeadSHA: target.HeadSHA, TargetRef: target.TargetRef, PipelinePath: target.PipelinePath, SpecHash: target.SpecHash,
			BindingFingerprint: binding, CapabilityFingerprint: capability, CompilerVersion: "v1", PolicyVersion: "v1",
		},
		Claims: []model.ProofClaim{{
			Identity:    model.ProofClaimIdentity{Family: model.ProofClaimFamilySourceIdentity, PipelineVersionHash: target.SpecHash, SemanticPath: "/steps/extract/source"},
			Description: "source resolves", RequiredEvidenceLevel: level, ActualEvidenceLevel: &level, ObservedAt: &now,
			Verdict: model.ProofVerdictPassed, Evidence: []model.ProofEvidenceReference{{Kind: model.ProofEvidenceReferenceKindValidation, Digest: evidence}},
		}},
		SupersedesReceiptID: supersedes, CreatedAt: now, RetainUntil: now.Add(24 * time.Hour),
	}, strings.Clone)
	require.NoError(t, err)
	return receipt
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func seedTarget(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org slug.Slug) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		WITH organization AS (
			INSERT INTO organizations (slug, name) VALUES ($1, $1) RETURNING id
		), credential AS (
			INSERT INTO credentials (organization_id, slug, kind, status, encrypted_payload)
			SELECT id, 'github-credential', 'static_secret', 'active', decode('01', 'hex') FROM organization RETURNING id, organization_id
		), data_source AS (
			INSERT INTO data_sources (organization_id, credential_id, name, slug, type, config, is_active)
			SELECT organization_id, id, 'GitHub', 'github', 'github', '{}', true FROM credential RETURNING id, organization_id
		), project AS (
			INSERT INTO projects (organization_id, slug, name, pipeline_backend_data_source_id, pipeline_backend_repo, pipeline_backend_tracked_branch, pipeline_backend_pipeline_dir)
			SELECT organization_id, 'data', 'Data', id, 'https://github.com/acme/pipelines.git', 'main', 'pipelines' FROM data_source RETURNING id, organization_id
		), environment AS (
			INSERT INTO environments (organization_id, slug, name) SELECT id, 'staging', 'Staging' FROM organization RETURNING id
		)
		INSERT INTO project_environments (organization_id, project_id, environment_id, is_default) SELECT project.organization_id, project.id, environment.id, true FROM project, environment
	`, org)
	require.NoError(t, err)
}

func seedSiblingScopes(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org slug.Slug) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		WITH organization AS (
			SELECT id FROM organizations WHERE slug = $1
		), data_source AS (
			SELECT id, organization_id FROM data_sources WHERE organization_id = (SELECT id FROM organization) LIMIT 1
		), project AS (
			INSERT INTO projects (organization_id, slug, name, pipeline_backend_data_source_id, pipeline_backend_repo, pipeline_backend_tracked_branch, pipeline_backend_pipeline_dir)
			SELECT organization_id, 'other-data', 'Other Data', id, 'https://github.com/acme/other.git', 'main', 'pipelines' FROM data_source RETURNING id, organization_id
		), environment AS (
			INSERT INTO environments (organization_id, slug, name) SELECT id, 'other-staging', 'Other Staging' FROM organization RETURNING id
		)
		INSERT INTO project_environments (organization_id, project_id, environment_id, is_default)
		SELECT project.organization_id, project.id, environment.id, true FROM project, environment
	`, org)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO project_environments (organization_id, project_id, environment_id, is_default)
		SELECT p.organization_id, p.id, e.id, false
		FROM projects p
		JOIN organizations o ON o.id = p.organization_id
		JOIN environments e ON e.organization_id = o.id
		WHERE o.slug = $1 AND p.slug = 'data' AND e.slug = 'other-staging'
	`, org)
	require.NoError(t, err)
}

func cleanup(t *testing.T, pool *pgxpool.Pool, orgs ...slug.Slug) {
	t.Helper()
	values := make([]string, len(orgs))
	for i, org := range orgs {
		values[i] = org.String()
	}
	for _, statement := range []string{
		`DELETE FROM proof_lifecycle_links WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`,
		`DELETE FROM proof_receipts WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`,
		`DELETE FROM proof_evidence WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`,
		`DELETE FROM proof_change_candidates WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`,
		`DELETE FROM proof_change_candidate_mappings WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`,
		`DELETE FROM projects WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`,
		`DELETE FROM data_sources WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`,
		`DELETE FROM organizations WHERE slug = ANY($1)`,
	} {
		_, err := pool.Exec(context.Background(), statement, values)
		require.NoError(t, err)
	}
}
