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

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres/proofcandidates"
)

func TestRepositoryEnforcesExactMappingAndTenantLinks(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres proof candidate repository test in short mode")
	}
	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "proof candidate tenant boundary integration test")
	repo := proofcandidates.New()
	orgA := slug.Slug(fmt.Sprintf("proof-candidate-a-%d", time.Now().UnixNano()))
	orgB := slug.Slug(fmt.Sprintf("proof-candidate-b-%d", time.Now().UnixNano()))
	installationA := seedTarget(t, ctx, pool, orgA)
	installationB := seedTarget(t, ctx, pool, orgB)
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		orgs := []string{orgA.String(), orgB.String()}
		for _, statement := range []string{
			`DELETE FROM proof_change_candidates WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`,
			`DELETE FROM proof_change_candidate_mappings WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`,
			`DELETE FROM projects WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`,
			`DELETE FROM data_sources WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`,
			`DELETE FROM github_app_repositories WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`,
			`DELETE FROM github_app_installations WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`,
			`DELETE FROM organizations WHERE slug = ANY($1)`,
		} {
			_, err := pool.Exec(cleanupCtx, statement, orgs)
			require.NoError(t, err)
		}
	})

	mappingA := createMapping(t, ctx, repo, pool, orgA, installationA)
	resolved, err := repo.ResolveMappings(ctx, pool, proofcandidates.MappingLookup{
		OrganizationSlug: orgA, Provider: "github", ProviderConnectionID: installationA, RepositoryID: "42",
		TargetRef: "refs/heads/main", PipelinePath: "pipelines/acme.pipeline.yaml",
	})
	require.NoError(t, err)
	require.Len(t, resolved, 1)
	require.Equal(t, orgA, resolved[0].OrganizationSlug)
	webhookResolved, err := repo.ResolveWebhookMappings(ctx, pool, proofcandidates.WebhookMappingLookup{
		Provider: "github", ProviderConnectionID: installationA, RepositoryID: "42", TargetRef: "refs/heads/main",
	})
	require.NoError(t, err)
	require.Len(t, webhookResolved, 1)
	require.Equal(t, orgA, webhookResolved[0].OrganizationSlug)

	wrongRepo, err := repo.ResolveMappings(ctx, pool, proofcandidates.MappingLookup{
		OrganizationSlug: orgA, Provider: "github", ProviderConnectionID: installationA, RepositoryID: "repo-other",
		TargetRef: "refs/heads/main", PipelinePath: "pipelines/acme.pipeline.yaml",
	})
	require.NoError(t, err)
	require.Empty(t, wrongRepo)
	crossTenant, err := repo.ResolveMappings(ctx, pool, proofcandidates.MappingLookup{
		OrganizationSlug: orgB, Provider: "github", ProviderConnectionID: installationA, RepositoryID: "42",
		TargetRef: "refs/heads/main", PipelinePath: "pipelines/acme.pipeline.yaml",
	})
	require.NoError(t, err)
	require.Empty(t, crossTenant)
	_, err = pool.Exec(ctx, `
		UPDATE credentials
		SET status = 'revoked', revoked_at = CURRENT_TIMESTAMP
		WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)
	`, orgA)
	require.NoError(t, err)
	revoked, err := repo.ResolveWebhookMappings(ctx, pool, proofcandidates.WebhookMappingLookup{
		Provider: "github", ProviderConnectionID: installationA, RepositoryID: "42", TargetRef: "refs/heads/main",
	})
	require.NoError(t, err)
	require.Empty(t, revoked, "a revoked installation must fail closed")
	_, err = repo.FindByKey(ctx, pool, orgA, "missing-candidate")
	require.ErrorIs(t, err, proofcandidates.ErrCandidateNotFound)
	_, err = repo.FindLatest(ctx, pool, orgA, mappingA, "missing-change")
	require.ErrorIs(t, err, proofcandidates.ErrCandidateNotFound)
	_, err = repo.ReopenByKey(ctx, pool, orgA, "missing-candidate")
	require.ErrorIs(t, err, proofcandidates.ErrCandidateNotFound)

	_, err = repo.Create(ctx, pool, orgA, candidateParams(mappingA, "proof-candidate-revoked", nil))
	require.ErrorIs(t, err, proofcandidates.ErrMappingTargetNotFound)
	_, err = pool.Exec(ctx, `UPDATE credentials SET status='active', revoked_at=NULL WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, orgA)
	require.NoError(t, err)
	first, err := repo.Create(ctx, pool, orgA, candidateParams(mappingA, "proof-candidate-a", nil))
	require.NoError(t, err)
	require.Equal(t, mappingA, first.MappingID)
	require.Equal(t, "proof-candidate-a", first.CandidateKey)
	require.Equal(t, "a123456789012345678901234567890123456789", first.HeadSHA)
	require.Nil(t, first.SupersedesCandidateID)
	specSum := sha256.Sum256(first.CanonicalSpec)
	require.Equal(t, "sha256:"+hex.EncodeToString(specSum[:]), first.SpecHash)
	_, err = repo.CloseLatest(ctx, pool, orgA, mappingA, first.ChangeRequestID, "b123456789012345678901234567890123456789")
	require.ErrorIs(t, err, proofcandidates.ErrCandidateNotFound, "a stale close must not close the latest head")
	closed, err := repo.CloseLatest(ctx, pool, orgA, mappingA, first.ChangeRequestID, first.HeadSHA)
	require.NoError(t, err)
	require.NotNil(t, closed.ClosedAt)
	reopened, err := repo.ReopenByKey(ctx, pool, orgA, first.CandidateKey)
	require.NoError(t, err)
	require.Nil(t, reopened.ClosedAt)
	_, err = repo.FindByKey(ctx, pool, orgB, first.CandidateKey)
	require.ErrorIs(t, err, proofcandidates.ErrCandidateNotFound)
	_, err = repo.FindByID(ctx, pool, orgB, first.ID)
	require.ErrorIs(t, err, proofcandidates.ErrCandidateNotFound)
	_, err = repo.FindLatest(ctx, pool, orgB, mappingA, first.ChangeRequestID)
	require.ErrorIs(t, err, proofcandidates.ErrCandidateNotFound)

	_, err = repo.Create(ctx, pool, orgB, candidateParams(mappingA, "proof-candidate-cross-org", nil))
	require.ErrorIs(t, err, proofcandidates.ErrMappingTargetNotFound, "an organization cannot substitute another tenant's mapping")

	mappingB := createMapping(t, ctx, repo, pool, orgB, installationB)
	_, err = repo.Create(ctx, pool, orgB, candidateParams(mappingB, "proof-candidate-cross-supersede", &first.ID))
	require.Error(t, err, "supersession cannot link across organizations")
	_, err = repo.CreateMapping(ctx, pool, proofcandidates.CreateMappingParams{
		OrganizationSlug: orgA, ProjectSlug: "missing", EnvironmentSlug: "staging", ProviderConnectionSlug: "github",
		Provider: "github", ProviderConnectionID: installationA, RepositoryID: "42",
		TargetRef: "refs/heads/main", PipelinePath: "pipelines/acme.pipeline.yaml",
	})
	require.ErrorIs(t, err, proofcandidates.ErrMappingTargetNotFound)

	lookup := proofcandidates.MappingLookup{OrganizationSlug: orgA, Provider: "github", ProviderConnectionID: installationA, RepositoryID: "42", TargetRef: "refs/heads/main", PipelinePath: "pipelines/acme.pipeline.yaml"}
	for _, disabled := range []bool{true, false} {
		_, err = pool.Exec(ctx, `UPDATE data_sources SET proof_enabled=$2 WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, orgA, !disabled)
		require.NoError(t, err)
		mappings, err := repo.ResolveMappings(ctx, pool, lookup)
		require.NoError(t, err)
		if disabled {
			require.Empty(t, mappings)
		} else {
			require.Len(t, mappings, 1)
		}
	}
	// Restoring the old repository must not silently restore its retired targets.
	for _, repositoryID := range []string{"43", "42"} {
		_, err = pool.Exec(ctx, `UPDATE data_sources SET config=jsonb_set(config, '{repository,id}', to_jsonb($2::text)), proof_enabled=true WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, orgA, repositoryID)
		require.NoError(t, err)
		mappings, err := repo.ResolveMappings(ctx, pool, lookup)
		require.NoError(t, err)
		require.Empty(t, mappings)
	}
	retained, err := repo.FindByKey(ctx, pool, orgA, first.CandidateKey)
	require.NoError(t, err)
	require.Equal(t, first.ID, retained.ID)
	require.Equal(t, first.CanonicalSpec, retained.CanonicalSpec)
	var originalRepository string
	err = pool.QueryRow(ctx, `SELECT repository_id FROM proof_change_candidate_mappings WHERE id=$1`, mappingA).Scan(&originalRepository)
	require.NoError(t, err)
	require.Equal(t, "42", originalRepository)
}

func seedTarget(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org slug.Slug) string {
	t.Helper()
	_, err := pool.Exec(ctx, `
		WITH organization AS (
			INSERT INTO organizations (slug, name) VALUES ($1, $1) RETURNING id
		), credential AS (
			INSERT INTO credentials (organization_id, slug, kind, status, encrypted_payload)
			SELECT id, 'github-credential', 'static_secret', 'active', decode('01', 'hex') FROM organization RETURNING id, organization_id
		), data_source AS (
			INSERT INTO data_sources (organization_id, credential_id, name, slug, type, config, is_active, proof_enabled)
			SELECT organization_id, id, 'GitHub', 'github', 'github', '{"repository":{"id":"42"}}', true, true FROM credential RETURNING id, organization_id
		), project AS (
			INSERT INTO projects (
				organization_id, slug, name, pipeline_backend_data_source_id,
				pipeline_backend_repo, pipeline_backend_tracked_branch, pipeline_backend_pipeline_dir
			)
			SELECT organization_id, 'data', 'Data', id, 'https://github.com/acme/pipelines.git', 'main', 'pipelines'
			FROM data_source RETURNING id, organization_id
		), environment AS (
			INSERT INTO environments (organization_id, slug, name)
			SELECT id, 'staging', 'Staging' FROM organization RETURNING id
		)
		INSERT INTO project_environments (organization_id, project_id, environment_id, is_default)
		SELECT project.organization_id, project.id, environment.id, true FROM project, environment
	`, org)
	require.NoError(t, err)
	var installation string
	err = pool.QueryRow(ctx, `
		WITH installation AS (
			INSERT INTO github_app_installations (organization_id, installation_id, account_id, account_login, account_type, repository_selection)
			SELECT id, id + 1000000, id, 'fixture', 'Organization', 'selected' FROM organizations WHERE slug=$1
			RETURNING id, organization_id, installation_id
		), repository AS (
			INSERT INTO github_app_repositories (organization_id, installation_id, repository_id, full_name, owner_login, name)
			SELECT organization_id, id, 42, 'acme/pipelines', 'acme', 'pipelines' FROM installation
		)
		SELECT installation_id::text FROM installation
	`, org).Scan(&installation)
	require.NoError(t, err)
	return installation
}

func createMapping(t *testing.T, ctx context.Context, repo proofcandidates.Repository, pool *pgxpool.Pool, org slug.Slug, connectionID string) int64 {
	t.Helper()
	id, err := repo.CreateMapping(ctx, pool, proofcandidates.CreateMappingParams{
		OrganizationSlug: org, ProjectSlug: "data", EnvironmentSlug: "staging", ProviderConnectionSlug: "github",
		Provider: "github", ProviderConnectionID: connectionID, RepositoryID: "42",
		TargetRef: "refs/heads/main", PipelinePath: "pipelines/acme.pipeline.yaml",
	})
	require.NoError(t, err)
	return id
}

func candidateParams(mappingID int64, candidateSlug slug.Slug, supersedes *int64) proofcandidates.CreateCandidateParams {
	zeroHash := "sha256:" + strings.Repeat("0", 64)
	canonical := []byte(`{"metadata":{"slug":"acme"}}`)
	specSum := sha256.Sum256(canonical)
	return proofcandidates.CreateCandidateParams{
		MappingID: mappingID, Slug: candidateSlug, CandidateKey: candidateSlug.String(), ChangeRequestID: "7",
		SourceRef: "refs/heads/feature", HeadSHA: "a123456789012345678901234567890123456789",
		SourceSHA256: zeroHash, SpecHash: "sha256:" + hex.EncodeToString(specSum[:]), CanonicalSpec: canonical,
		SupersedesCandidateID: supersedes,
	}
}
