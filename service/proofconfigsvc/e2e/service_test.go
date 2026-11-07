package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/proofconfig"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/proofconfigsvc"
	"github.com/stretchr/testify/require"
)

type repositories struct{}

func (repositories) ListProviderRepositories(context.Context, slug.Slug, slug.Slug) ([]datasourcessvc.ProviderRepository, error) {
	return []datasourcessvc.ProviderRepository{{ID: "42"}}, nil
}
func (repositories) EnsureProofWebhook(context.Context, slug.Slug, slug.Slug) error { return nil }

func TestSharedConfigurationLifecycleAndTenantScope(t *testing.T) {
	if testing.Short() {
		t.Skip("requires worktree PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "Proof configuration lifecycle")
	org := slug.Slug(fmt.Sprintf("proof-config-%d", time.Now().UnixNano()))
	_, err := pool.Exec(t.Context(), `
 WITH organization AS (INSERT INTO organizations(slug,name) VALUES($1,$1) RETURNING id),
 credential AS (INSERT INTO credentials(organization_id,slug,kind,status,encrypted_payload) SELECT id,'git-credential','static_secret','active','fixture'::bytea FROM organization RETURNING id,organization_id),
 source AS (INSERT INTO data_sources(organization_id,credential_id,name,slug,type,config,is_active,read_only) SELECT organization_id,id,'GitHub','github','github','{}',true,true FROM credential RETURNING id,organization_id),
 project AS (INSERT INTO projects(organization_id,slug,name,pipeline_backend_data_source_id,pipeline_backend_repo,pipeline_backend_tracked_branch,pipeline_backend_pipeline_dir) SELECT organization_id,'etl','ETL',id,'https://github.com/fixture/pipelines','main','pipelines' FROM source RETURNING id,organization_id),
 environment AS (INSERT INTO environments(organization_id,slug,name) SELECT id,'staging','Staging' FROM organization RETURNING id)
 INSERT INTO project_environments(organization_id,project_id,environment_id,is_default) SELECT project.organization_id,project.id,environment.id,true FROM project,environment`, org)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, statement := range []string{
			`DELETE FROM proof_change_candidate_mappings WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`,
			`DELETE FROM projects WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`,
			`DELETE FROM data_sources WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`,
			`DELETE FROM organizations WHERE slug=$1`,
		} {
			_, err := pool.Exec(context.Background(), statement, org)
			require.NoError(t, err)
		}
	})
	e2edb.EnableGitHubProof(t, pool, org, "github", "42")
	txm := postgres.NewTransactionManager(pool)
	svc, err := proofconfigsvc.New(txm, proofconfig.New(), repositories{}, eventtest.NewProducer(nil))
	require.NoError(t, err)
	actor := model.UserEventActor("reviewer")
	target := proofconfig.Target{Project: "etl", Environment: "staging", Ref: "refs/heads/main", Path: "pipelines/contacts.yaml"}
	settings := proofconfigsvc.Settings{Enabled: true, PublishChecks: true, ExpectedRepositoryID: "42", Targets: []proofconfigsvc.Target{proofconfigsvc.Target(target)}}
	configuration, err := svc.Set(t.Context(), org, "github", actor, settings)
	require.NoError(t, err)
	require.True(t, configuration.Source.Enabled)
	require.True(t, configuration.Source.PublishChecks)
	require.Equal(t, []proofconfig.Target{target}, configuration.Targets)
	all, err := svc.List(t.Context(), org)
	require.NoError(t, err)
	require.Equal(t, []proofconfigsvc.Configuration{*configuration}, all)
	var mappingID int64
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT id FROM proof_change_candidate_mappings WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND disabled_at IS NULL`, org).Scan(&mappingID))
	_, err = svc.Set(t.Context(), org, "github", actor, settings)
	require.NoError(t, err)
	var count int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM proof_change_candidate_mappings WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org).Scan(&count))
	require.Equal(t, 1, count, "saving unchanged settings retains the mapping identity")

	_, err = pool.Exec(t.Context(), `UPDATE github_app_installations SET installation_id=installation_id+1000000000 WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
	require.NoError(t, err)
	configuration, err = svc.Get(t.Context(), org, "github")
	require.NoError(t, err)
	require.False(t, configuration.Source.Enabled)
	require.Contains(t, configuration.Status, "Provider connection changed")
	_, err = svc.Set(t.Context(), org, "github", actor, settings)
	require.NoError(t, err)
	var retired bool
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT disabled_at IS NOT NULL FROM proof_change_candidate_mappings WHERE id=$1`, mappingID).Scan(&retired))
	require.True(t, retired, "reconfirmation retires the old installation target")
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM active_proof_candidate_mappings WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org).Scan(&count))
	require.Equal(t, 1, count)

	_, err = svc.Get(t.Context(), "other-tenant", "github")
	require.ErrorIs(t, err, proofconfigsvc.ErrSourceNotFound)
	invalid := settings
	invalid.Targets = []proofconfigsvc.Target{{Project: "other-project", Environment: target.Environment, Ref: target.Ref, Path: target.Path}}
	_, err = svc.Set(t.Context(), org, "github", actor, invalid)
	require.ErrorIs(t, err, proofconfigsvc.ErrConfiguration)
	invalid = settings
	invalid.ExpectedRepositoryID = "43"
	_, err = svc.Set(t.Context(), org, "github", actor, invalid)
	require.ErrorIs(t, err, proofconfigsvc.ErrConfiguration)

	configuration, err = svc.Set(t.Context(), org, "github", actor, proofconfigsvc.Settings{})
	require.NoError(t, err)
	require.False(t, configuration.Source.Enabled)
	require.False(t, configuration.Source.PublishChecks)
	require.True(t, configuration.Source.Active)
	require.Equal(t, []proofconfig.Target{target}, configuration.Targets)
	var readOnly bool
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT read_only FROM data_sources WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug='github'`, org).Scan(&readOnly))
	require.True(t, readOnly)

	// The same data-source-owned controls must also accept managed GitLab OAuth.
	_, err = pool.Exec(t.Context(), `UPDATE credentials SET kind='oauth2_grant' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `UPDATE data_sources SET type='gitlab' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
	require.NoError(t, err)
	configuration, err = svc.Set(t.Context(), org, "github", actor, settings)
	require.NoError(t, err)
	require.Equal(t, "gitlab", configuration.Source.Provider)
	require.True(t, configuration.Source.Enabled)
	require.True(t, configuration.Source.PublishChecks)
	configuration, err = svc.Set(t.Context(), org, "github", actor, proofconfigsvc.Settings{})
	require.NoError(t, err)
	require.False(t, configuration.Source.Enabled)
	require.False(t, configuration.Source.PublishChecks)

	_, err = pool.Exec(t.Context(), `UPDATE credentials SET status='revoked',revoked_at=now() WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
	require.NoError(t, err)
	_, err = svc.Set(t.Context(), org, "github", actor, settings)
	require.ErrorIs(t, err, proofconfigsvc.ErrConfiguration)
	configuration, err = svc.Set(t.Context(), org, "github", actor, proofconfigsvc.Settings{})
	require.NoError(t, err, "disabling remains possible after access loss")
	require.Contains(t, configuration.Status, "Reconnect")
}
