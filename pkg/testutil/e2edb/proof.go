package e2edb

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// EnableGitHubProof binds an existing test source to a managed installation.
func EnableGitHubProof(t testing.TB, pool *pgxpool.Pool, org, source slug.Slug, repositoryID string) string {
	t.Helper()
	var installationID string
	err := pool.QueryRow(context.Background(), `
 WITH installation AS (
  INSERT INTO github_app_installations (organization_id, installation_id, account_id, account_login, account_type, repository_selection)
  SELECT id, id+1000000, id, 'fixture', 'Organization', 'selected' FROM organizations WHERE slug=$1
  RETURNING id, organization_id, installation_id
 ), repository AS (
  INSERT INTO github_app_repositories (organization_id, installation_id, repository_id, full_name, owner_login, name)
  SELECT organization_id, id, $2::bigint, 'fixture/pipelines', 'fixture', 'pipelines' FROM installation
 ) SELECT installation_id::text FROM installation`, org, repositoryID).Scan(&installationID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `UPDATE data_sources SET config=jsonb_build_object('repository',jsonb_build_object('id',$3::text)) WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug=$2`, org, source, repositoryID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `UPDATE data_sources SET proof_enabled=true,proof_checks_enabled=true WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug=$2`, org, source)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, statement := range []string{`DELETE FROM github_app_repositories WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, `DELETE FROM github_app_installations WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`} {
			_, err := pool.Exec(context.Background(), statement, org)
			require.NoError(t, err)
		}
	})
	return installationID
}
