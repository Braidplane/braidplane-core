package e2e

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	pgauthorization "github.com/Braidplane/braidplane-core/postgres/authorization"
	"github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

func TestPipelineFilterGrantIsIsolatedByOrganization(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	db := e2edb.OpenIsolatedPostgresPool(t, "pipeline report scope")
	_, err := db.Exec(t.Context(), `
INSERT INTO organizations (id,name,slug) VALUES (10,'First','first'),(20,'Second','second');
INSERT INTO projects (id,organization_id,name,slug) VALUES (11,10,'Primary','primary'),(21,20,'Primary','primary');
INSERT INTO roles (id,organization_id,name,slug) VALUES
    (13,10,'Source reader','source-reader'),(14,10,'Pipeline reader','pipeline-reader'),(23,20,'Source reader','source-reader');
INSERT INTO permissions (resource,action) VALUES ('datasources','read'),('pipelines','read') ON CONFLICT (resource,action) DO NOTHING;
INSERT INTO role_permissions (role_id,permission_id)
    SELECT r.id,p.id FROM roles r JOIN permissions p ON p.action='read' AND
    ((r.slug='source-reader' AND p.resource='datasources') OR (r.slug='pipeline-reader' AND p.resource='pipelines'));
INSERT INTO users (id,slug,email,role,verified_at) VALUES (18,'member','member@example.test','super_admin',now());
INSERT INTO organization_members (id,organization_id,user_id,role_id) VALUES (19,10,18,13),(29,20,18,23);
INSERT INTO pipeline_specs (project_id,slug,spec_jsonb,source_path,source_ref,last_applied_commit) VALUES
    (11,'daily','{}','daily.json','main','one'),(21,'daily','{}','daily.json','main','one'),
    (11,'weekly','{}','weekly.json','main','one');
INSERT INTO authorization_scoped_grants (organization_id,member_id,role_id,scope,target_kind,target_id)
    SELECT 10,19,14,'resource','pipelines',id FROM pipeline_specs WHERE project_id=11 AND slug='daily';
INSERT INTO drift_impact_reports (pipeline_spec_id,slug,report_type)
    SELECT id,concat(project_id,'-',slug,'-report'),'contract_impact' FROM pipeline_specs;
`)
	require.NoError(t, err)

	svc, err := authorization.New(nil, func(ctx context.Context, org, user, key slug.Slug, target model.AuthorizationTarget) (model.AuthorizationFacts, error) {
		facts, err := pgauthorization.Resolve(ctx, db, org, user, key, target)
		require.NoError(t, err)
		require.True(t, facts.Valid)
		return facts, nil
	})
	require.NoError(t, err)
	project, daily := slug.Slug("primary"), slug.Slug("daily")
	for _, tc := range []struct {
		org, pipeline slug.Slug
		want          bool
	}{
		{"first", daily, true},
		{"first", "weekly", false},
		{"second", daily, false},
	} {
		input := authorization.Input{
			Operation: "listDriftImpactReports", Source: authorization.SourceSession, UserSlug: "member",
			OrgSlug: &tc.org, Action: model.Read, Resource: model.ResourcePipelines, CheckPurpose: "pipeline_filter",
			Target: model.AuthorizationTarget{Kind: "pipelines", Slug: tc.pipeline, ProjectSlug: project},
		}
		allowed, err := svc.Can(t.Context(), input)
		require.NoError(t, err)
		require.Equal(t, tc.want, allowed, "%s / %s", tc.org, tc.pipeline)

		input.Resource, input.CheckPurpose, input.Target = model.ResourceDataSources, "", model.AuthorizationTarget{}
		allowed, err = svc.Can(t.Context(), input)
		require.NoError(t, err)
		require.True(t, allowed, "%s has organization-wide data source read", tc.org)
	}
	reports, err := driftimpact.New().List(t.Context(), db, "first", driftimpact.ListFilters{
		ProjectSlug: &project, PipelineSlug: &daily, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, slug.Slug("11-daily-report"), reports[0].Slug)
}
