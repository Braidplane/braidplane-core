package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/testutil/tenantfixture"
	"github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/Braidplane/braidplane-core/postgres/githubapps"
	"github.com/Braidplane/braidplane-core/postgres/repositorysetups"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestGitLabRepositoryConsentPersistsTenantScopedAttemptContext(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "GitLab repository consent isolation")
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	f := tenantfixture.SeedEnvironmentBoundary(t, tx, true)
	var org [2]slug.Slug
	for i := range 2 {
		require.NoError(t, tx.QueryRow(t.Context(), `SELECT slug FROM organizations WHERE id=$1`, f.Org[i]).Scan(&org[i]))
	}
	_, err = repositorysetups.New().Create(t.Context(), tx, org[0], "project", "gitlab-setup", []byte(`{"provider":"gitlab"}`))
	require.NoError(t, err)
	repo := credentials.New()
	for i := range 2 {
		_, err = repo.CreateOrganizationGitLabConnection(t.Context(), tx, credentials.CredentialRecord{Slug: "gitlab-grant", OrgSlug: org[i], OwnerKind: credentials.OwnerKindOrganizationGitLab, Kind: "oauth2_grant", Status: "pending"})
		require.NoError(t, err)
	}
	now := time.Now().UTC()
	actor := slug.Slug("alice")
	install := credentials.CredentialInstall{Slug: "gitlab-install", OrgSlug: org[0], OwnerKind: credentials.OwnerKindOrganizationGitLab, Provider: "gitlab", StateHash: "synthetic-state-hash", Intent: "create_grant", ActorType: "user", ActorSlug: &actor, RequestedScopes: []string{"api"}, ExpiresAt: now.Add(time.Hour), RepositorySetupSlug: "gitlab-setup", RepositorySetupProjectSlug: "project"}
	created, err := repo.CreateOAuthInstall(t.Context(), tx, install)
	require.NoError(t, err)
	require.Equal(t, install.RepositorySetupSlug, created.RepositorySetupSlug)
	require.Equal(t, install.RepositorySetupProjectSlug, created.RepositorySetupProjectSlug)
	found, err := repo.FindOAuthInstallByState(t.Context(), tx, "gitlab", install.StateHash, now)
	require.NoError(t, err)
	require.Equal(t, install.OrgSlug, found.OrgSlug)
	require.Equal(t, install.RepositorySetupSlug, found.RepositorySetupSlug)
	require.Equal(t, install.RepositorySetupProjectSlug, found.RepositorySetupProjectSlug)
	require.Equal(t, actor, *found.ActorSlug)
	for _, invalid := range []string{"other-tenant", "missing-project"} {
		sub, err := tx.Begin(t.Context())
		require.NoError(t, err)
		wrong := install
		wrong.Slug, wrong.StateHash = "invalid-install", invalid
		if invalid == "other-tenant" {
			wrong.OrgSlug = org[1]
		} else {
			wrong.RepositorySetupProjectSlug = "missing-project"
		}
		_, err = repo.CreateOAuthInstall(t.Context(), sub, wrong)
		require.Error(t, err)
		require.NoError(t, sub.Rollback(t.Context()))
	}
	consumed, err := repo.ConsumeOAuthInstall(t.Context(), tx, "gitlab", install.StateHash, now)
	require.NoError(t, err)
	require.Equal(t, install.RepositorySetupSlug, consumed.RepositorySetupSlug)
	require.Equal(t, install.RepositorySetupProjectSlug, consumed.RepositorySetupProjectSlug)
	require.NotNil(t, consumed.ConsumedAt)
	_, err = repo.ConsumeOAuthInstall(t.Context(), tx, "gitlab", install.StateHash, now)
	require.ErrorIs(t, err, credentials.ErrOAuthStateConsumed)
	ordinary := install
	ordinary.Slug, ordinary.StateHash = "ordinary-install", "ordinary-state-hash"
	ordinary.RepositorySetupSlug, ordinary.RepositorySetupProjectSlug = "", ""
	_, err = repo.CreateOAuthInstall(t.Context(), tx, ordinary)
	require.NoError(t, err)
	found, err = repo.FindOAuthInstallByState(t.Context(), tx, "gitlab", ordinary.StateHash, now)
	require.NoError(t, err)
	require.Empty(t, found.RepositorySetupSlug)
	require.Empty(t, found.RepositorySetupProjectSlug)
}

func TestRepositorySetupTenantAndLeaseIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "project repository setup isolation")
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	f := tenantfixture.SeedEnvironmentBoundary(t, tx, true)
	var org [2]slug.Slug
	for i := range 2 {
		require.NoError(t, tx.QueryRow(t.Context(), `SELECT slug FROM organizations WHERE id=$1`, f.Org[i]).Scan(&org[i]))
	}
	repo := repositorysetups.New()
	created, err := repo.Create(t.Context(), tx, org[0], "project", "repo-first", []byte(`{"provider":"gitlab"}`))
	require.NoError(t, err)
	require.True(t, created)
	created, err = repo.Create(t.Context(), tx, org[0], "project", "repo-duplicate", []byte(`{"provider":"github"}`))
	require.NoError(t, err)
	require.False(t, created)
	_, err = repo.Get(t.Context(), tx, org[1], "project")
	require.ErrorIs(t, err, repositorysetups.ErrNotFound)
	_, err = repo.Claim(t.Context(), tx, org[1], "project", "repo-first", "wrong-org")
	require.ErrorIs(t, err, repositorysetups.ErrBusy)
	row, err := repo.Claim(t.Context(), tx, org[0], "project", "repo-first", "first-owner")
	require.NoError(t, err)
	require.Equal(t, slug.Slug("repo-first"), row.Slug)
	_, err = repo.Claim(t.Context(), tx, org[0], "project", "repo-first", "second-owner")
	require.ErrorIs(t, err, repositorysetups.ErrBusy)
	row.Status = "repository_created"
	row.Result = []byte(`{"repository_id":"42"}`)
	require.ErrorIs(t, repo.Update(t.Context(), tx, org[1], "project", "first-owner", row), repositorysetups.ErrLeaseLost)
	require.ErrorIs(t, repo.Update(t.Context(), tx, org[0], "project", "second-owner", row), repositorysetups.ErrLeaseLost)
	require.NoError(t, repo.Update(t.Context(), tx, org[0], "project", "first-owner", row))
	require.ErrorIs(t, repo.Release(t.Context(), tx, org[0], "project", "second-owner"), repositorysetups.ErrLeaseLost)
	require.NoError(t, repo.Release(t.Context(), tx, org[0], "project", "first-owner"))
	_, err = repo.Claim(t.Context(), tx, org[0], "project", "repo-first", "second-owner")
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `UPDATE project_repository_setups SET lease_until=clock_timestamp()-INTERVAL '1 second' WHERE project_id=$1`, f.Project[0])
	require.NoError(t, err)
	_, err = repo.Claim(t.Context(), tx, org[0], "project", "repo-first", "third-owner")
	require.NoError(t, err)
	require.ErrorIs(t, repo.Update(t.Context(), tx, org[0], "project", "second-owner", row), repositorysetups.ErrLeaseLost)
	got, err := repo.Get(t.Context(), tx, org[0], "project")
	require.NoError(t, err)
	require.JSONEq(t, `{"repository_id":"42"}`, string(got.Result))
	sub, err := tx.Begin(t.Context())
	require.NoError(t, err)
	_, err = sub.Exec(t.Context(), `DELETE FROM project_repository_setups WHERE project_id=$1`, f.Project[0])
	require.NoError(t, err)
	_, err = sub.Exec(t.Context(), `INSERT INTO project_repository_setups(organization_id,project_id,slug,request) VALUES ($1,$2,'cross-tenant','{}')`, f.Org[1], f.Project[0])
	var constraint *pgconn.PgError
	require.ErrorAs(t, err, &constraint)
	require.Equal(t, "23503", constraint.Code)
	require.NoError(t, sub.Rollback(t.Context()))
}

func TestAbandonedRepositorySetupRetainsProvenanceAndCannotClaimNewAttempt(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "abandoned repository setup isolation")
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	f := tenantfixture.SeedEnvironmentBoundary(t, tx, true)
	_, err = tx.Exec(t.Context(), `UPDATE projects SET pipeline_backend_data_source_id=NULL WHERE id=$1`, f.Project[0])
	require.NoError(t, err)
	var org [2]slug.Slug
	for i := range 2 {
		require.NoError(t, tx.QueryRow(t.Context(), `SELECT slug FROM organizations WHERE id=$1`, f.Org[i]).Scan(&org[i]))
	}
	repo := repositorysetups.New()
	created, err := repo.Create(t.Context(), tx, org[0], "project", "repo-old", []byte(`{"provider":"github","repository_name":"old"}`))
	require.NoError(t, err)
	require.True(t, created)
	row, err := repo.Claim(t.Context(), tx, org[0], "project", "repo-old", "old-lease")
	require.NoError(t, err)
	row.Result = []byte(`{"repository_id":"42","repository_url":"https://github.com/customer/old.git"}`)
	require.NoError(t, repo.Update(t.Context(), tx, org[0], "project", "old-lease", row))
	_, err = repo.Abandon(t.Context(), tx, org[1], "project", "repo-old", "old-lease")
	require.ErrorIs(t, err, repositorysetups.ErrLeaseLost)
	_, err = repo.Abandon(t.Context(), tx, org[0], "project", "repo-old", "wrong-lease")
	require.ErrorIs(t, err, repositorysetups.ErrLeaseLost)
	archived, err := repo.Abandon(t.Context(), tx, org[0], "project", "repo-old", "old-lease")
	require.NoError(t, err)
	require.NotNil(t, archived.AbandonedAt)
	require.JSONEq(t, string(row.Result), string(archived.Result))
	_, err = repo.Get(t.Context(), tx, org[0], "project")
	require.ErrorIs(t, err, repositorysetups.ErrNotFound)
	_, err = repo.ProjectForSetup(t.Context(), tx, org[0], "repo-old")
	require.ErrorIs(t, err, repositorysetups.ErrNotFound)
	for _, scope := range []struct{ org, project slug.Slug }{{org[1], "project"}, {org[0], "another-project"}} {
		_, err = repo.GetAttempt(t.Context(), tx, scope.org, scope.project, "repo-old")
		require.ErrorIs(t, err, repositorysetups.ErrNotFound)
	}
	created, err = repo.Create(t.Context(), tx, org[0], "project", "repo-new", []byte(`{"provider":"github","repository_name":"new"}`))
	require.NoError(t, err)
	require.True(t, created)
	_, err = repo.Claim(t.Context(), tx, org[0], "project", "repo-old", "stale-callback")
	require.ErrorIs(t, err, repositorysetups.ErrBusy)
	require.ErrorIs(t, repo.Update(t.Context(), tx, org[0], "project", "old-lease", row), repositorysetups.ErrLeaseLost)
	retained, err := repo.GetAttempt(t.Context(), tx, org[0], "project", "repo-old")
	require.NoError(t, err)
	require.JSONEq(t, string(archived.Request), string(retained.Request))
	require.JSONEq(t, string(archived.Result), string(retained.Result))
	require.NotNil(t, retained.AbandonedAt)
	row, err = repo.Claim(t.Context(), tx, org[0], "project", "repo-new", "new-lease")
	require.NoError(t, err)
	row.Status = "attached"
	require.NoError(t, repo.Update(t.Context(), tx, org[0], "project", "new-lease", row))
	_, err = repo.Abandon(t.Context(), tx, org[0], "project", "repo-new", "new-lease")
	require.ErrorIs(t, err, repositorysetups.ErrLeaseLost)
	active, err := repo.Get(t.Context(), tx, org[0], "project")
	require.NoError(t, err)
	require.Equal(t, "attached", active.Status)
}

func TestGitHubRepositoryConsentIsTenantBoundAndPreservesInstallation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "GitHub repository consent isolation")
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	f := tenantfixture.SeedEnvironmentBoundary(t, tx, true)
	var org [2]slug.Slug
	for i := range 2 {
		require.NoError(t, tx.QueryRow(t.Context(), `SELECT slug FROM organizations WHERE id=$1`, f.Org[i]).Scan(&org[i]))
	}
	setupRepo := repositorysetups.New()
	_, err = setupRepo.Create(t.Context(), tx, org[0], "project", "repo-consent", []byte(`{"provider":"github"}`))
	require.NoError(t, err)
	repo := githubapps.New()
	require.NoError(t, repo.CreateIntent(t.Context(), tx, org[0], "alice", "isolated-state-hash", time.Now().Add(time.Hour), "repo-consent", ""))
	intent, err := repo.GetIntent(t.Context(), tx, "isolated-state-hash")
	require.NoError(t, err)
	require.Equal(t, org[0], intent.Organization)
	require.Equal(t, slug.Slug("repo-consent"), intent.RepositorySetupSlug)
	sub, err := tx.Begin(t.Context())
	require.NoError(t, err)
	err = repo.CreateIntent(t.Context(), sub, org[1], "alice", "other-state-hash", time.Now().Add(time.Hour), "repo-consent", "")
	var constraint *pgconn.PgError
	require.ErrorAs(t, err, &constraint)
	require.Equal(t, "23503", constraint.Code)
	require.NoError(t, sub.Rollback(t.Context()))
	ordinary := intent
	ordinary.RepositorySetupSlug = ""
	verified := githubapps.VerifiedInstallation{InstallationID: 41, AccountID: 100, AccountLogin: "acme", AccountType: "Organization", RepositorySelection: "selected"}
	_, err = repo.UpsertInstallation(t.Context(), tx, ordinary, verified)
	require.NoError(t, err)
	verified.InstallationID = 42
	_, err = repo.UpsertInstallation(t.Context(), tx, intent, verified)
	require.ErrorIs(t, err, githubapps.ErrNotFound)
	existingAuthorization := ordinary
	existingAuthorization.RequestedAccountLogin = "acme"
	_, err = repo.UpsertInstallation(t.Context(), tx, existingAuthorization, verified)
	require.ErrorIs(t, err, githubapps.ErrNotFound)
	existing, err := repo.GetInstallation(t.Context(), tx, org[0])
	require.NoError(t, err)
	require.Equal(t, int64(41), existing.InstallationID)
	_, err = repo.UpsertInstallation(t.Context(), tx, ordinary, verified)
	require.NoError(t, err)
	existing, err = repo.GetInstallation(t.Context(), tx, org[0])
	require.NoError(t, err)
	require.Equal(t, int64(42), existing.InstallationID)
	project, err := setupRepo.ProjectForSetup(t.Context(), tx, org[0], "repo-consent")
	require.NoError(t, err)
	require.Equal(t, slug.Slug("project"), project)
	_, err = setupRepo.ProjectForSetup(t.Context(), tx, org[1], "repo-consent")
	require.ErrorIs(t, err, repositorysetups.ErrNotFound)
}
