package e2e

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/datasourcerefs"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

func TestService_DataSourceRefBindingsIntegration(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres data source ref service integration test in short mode")
	}

	pool := e2edb.OpenPostgresPool(t, "data source ref service integration test")
	ctx := context.Background()
	orgA := must.Slug("ds-ref-bindings-org-a")
	orgB := must.Slug("ds-ref-bindings-org-b")
	project := must.Slug("operations")

	resetOrganization(t, pool, orgA)
	resetOrganization(t, pool, orgB)
	t.Cleanup(func() {
		resetOrganization(t, pool, orgA)
		resetOrganization(t, pool, orgB)
	})

	seedOrganizationProjectEnvironments(t, pool, orgA, project, "Data Source Ref Bindings Org A", []slug.Slug{
		must.Slug("dev"),
		must.Slug("prod"),
	})
	seedOrganizationProjectEnvironments(t, pool, orgB, project, "Data Source Ref Bindings Org B", []slug.Slug{
		must.Slug("dev"),
		must.Slug("prod"),
	})
	seedDataSource(t, pool, orgA, concreteDataSourceSeed{
		slug:     must.Slug("dev-users-postgres"),
		name:     "Dev Users Postgres",
		dsType:   model.DataSourceTypePostgres,
		active:   true,
		readOnly: false,
	})
	seedDataSource(t, pool, orgA, concreteDataSourceSeed{
		slug:     must.Slug("prod-users-postgres"),
		name:     "Prod Users Postgres",
		dsType:   model.DataSourceTypePostgres,
		active:   true,
		readOnly: false,
	})
	seedDataSource(t, pool, orgB, concreteDataSourceSeed{
		slug:     must.Slug("prod-users-postgres"),
		name:     "Other Tenant Prod Users Postgres",
		dsType:   model.DataSourceTypePostgres,
		active:   true,
		readOnly: false,
	})
	pipeline := pipelineWithLogicalSource(must.Slug("users-source"))
	seedPipelineSpec(t, pool, orgA, project, pipeline)
	seedPipelineSpec(t, pool, orgB, project, pipeline)

	svc := newDataSourceRefsService(pool, map[string]*datasourcessvc.DataSource{
		sourceKey(orgA, must.Slug("dev-users-postgres")): {
			Slug:     must.Slug("dev-users-postgres"),
			Name:     "Dev Users Postgres",
			Type:     model.DataSourceTypePostgres,
			IsActive: true,
		},
		sourceKey(orgA, must.Slug("prod-users-postgres")): {
			Slug:     must.Slug("prod-users-postgres"),
			Name:     "Prod Users Postgres",
			Type:     model.DataSourceTypePostgres,
			IsActive: true,
		},
		sourceKey(orgB, must.Slug("prod-users-postgres")): {
			Slug:     must.Slug("prod-users-postgres"),
			Name:     "Other Tenant Prod Users Postgres",
			Type:     model.DataSourceTypePostgres,
			IsActive: true,
		},
	})

	_, err := svc.CreateDataSourceRef(ctx, orgA, project, pipeline.Metadata.Slug, model.NewDataSourceRef{
		Name:           "Users Source",
		Slug:           must.Slug("users-source"),
		DataSourceType: model.DataSourceTypePostgres,
		RequiredCapabilities: []model.DataSourceRefCapability{
			model.DataSourceRefCapabilityPipelineSource,
		},
	}, model.UserEventActor(must.Slug("alice")))
	require.NoError(t, err)
	_, err = svc.CreateDataSourceRef(ctx, orgB, project, pipeline.Metadata.Slug, model.NewDataSourceRef{
		Name:           "Users Source",
		Slug:           must.Slug("users-source"),
		DataSourceType: model.DataSourceTypePostgres,
		RequiredCapabilities: []model.DataSourceRefCapability{
			model.DataSourceRefCapabilityPipelineSource,
		},
	}, model.UserEventActor(must.Slug("bob")))
	require.NoError(t, err)

	devBinding, err := svc.UpsertEnvironmentBinding(
		ctx,
		orgA,
		project,
		pipeline.Metadata.Slug,
		must.Slug("dev"),
		must.Slug("users-source"),
		model.UpsertDataSourceBinding{DataSourceSlug: must.Slug("dev-users-postgres")},
		model.UserEventActor(must.Slug("alice")),
	)
	require.NoError(t, err)
	require.Equal(t, must.Slug("dev-users-postgres"), devBinding.DataSourceSlug)
	require.Equal(t, "Dev Users Postgres", devBinding.DataSourceName)
	require.Equal(t, model.DataSourceTypePostgres, devBinding.DataSourceType)
	require.True(t, devBinding.DataSourceActive)
	require.False(t, devBinding.DataSourceReadOnly)

	prodBinding, err := svc.UpsertEnvironmentBinding(
		ctx,
		orgA,
		project,
		pipeline.Metadata.Slug,
		must.Slug("prod"),
		must.Slug("users-source"),
		model.UpsertDataSourceBinding{DataSourceSlug: must.Slug("prod-users-postgres")},
		model.UserEventActor(must.Slug("alice")),
	)
	require.NoError(t, err)
	require.Equal(t, must.Slug("prod-users-postgres"), prodBinding.DataSourceSlug)

	devResolved, err := svc.ResolvePipelineDataSourceRefs(
		ctx,
		orgA,
		project,
		must.Slug("dev"),
		pipeline,
	)
	require.NoError(t, err)
	require.Equal(t, must.Slug("dev-users-postgres"), devResolved.Pipeline.Steps[0].Query.DataSourceSlug)
	require.Equal(t, must.Slug("users-source"), devResolved.Resolutions[0].RefSlug)

	prodResolved, err := svc.ResolvePipelineDataSourceRefs(
		ctx,
		orgA,
		project,
		must.Slug("prod"),
		pipeline,
	)
	require.NoError(t, err)
	require.Equal(t, must.Slug("prod-users-postgres"), prodResolved.Pipeline.Steps[0].Query.DataSourceSlug)

	_, err = svc.GetEnvironmentBinding(ctx, orgB, project, pipeline.Metadata.Slug, must.Slug("prod"), must.Slug("users-source"))
	require.ErrorIs(t, err, datasourcerefssvc.ErrDataSourceBindingNotFound)

	_, err = svc.ResolvePipelineDataSourceRefs(
		ctx,
		orgB,
		project,
		must.Slug("prod"),
		pipeline,
	)
	require.ErrorIs(t, err, datasourcerefssvc.ErrDataSourceBindingNotFound)
}

func TestService_ResolvePipelineDataSourceRefsRejectsUnavailableBoundTargetIntegration(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres data source ref service integration test in short mode")
	}

	pool := e2edb.OpenPostgresPool(t, "data source ref service integration test")
	ctx := context.Background()
	org := must.Slug("ds-ref-unavailable-org")
	project := must.Slug("operations")

	resetOrganization(t, pool, org)
	t.Cleanup(func() {
		resetOrganization(t, pool, org)
	})

	seedOrganizationProjectEnvironments(t, pool, org, project, "Data Source Ref Unavailable Org", []slug.Slug{
		must.Slug("prod"),
	})
	seedDataSource(t, pool, org, concreteDataSourceSeed{
		slug:     must.Slug("prod-users-postgres"),
		name:     "Prod Users Postgres",
		dsType:   model.DataSourceTypePostgres,
		active:   true,
		readOnly: false,
	})
	pipeline := pipelineWithLogicalSource(must.Slug("users-source"))
	seedPipelineSpec(t, pool, org, project, pipeline)

	svc := newDataSourceRefsService(pool, map[string]*datasourcessvc.DataSource{
		sourceKey(org, must.Slug("prod-users-postgres")): {
			Slug:     must.Slug("prod-users-postgres"),
			Name:     "Prod Users Postgres",
			Type:     model.DataSourceTypePostgres,
			IsActive: true,
		},
	})

	_, err := svc.CreateDataSourceRef(ctx, org, project, pipeline.Metadata.Slug, model.NewDataSourceRef{
		Name:           "Users Source",
		Slug:           must.Slug("users-source"),
		DataSourceType: model.DataSourceTypePostgres,
		RequiredCapabilities: []model.DataSourceRefCapability{
			model.DataSourceRefCapabilityPipelineSource,
		},
	}, model.UserEventActor(must.Slug("alice")))
	require.NoError(t, err)
	_, err = svc.UpsertEnvironmentBinding(
		ctx,
		org,
		project,
		pipeline.Metadata.Slug,
		must.Slug("prod"),
		must.Slug("users-source"),
		model.UpsertDataSourceBinding{DataSourceSlug: must.Slug("prod-users-postgres")},
		model.UserEventActor(must.Slug("alice")),
	)
	require.NoError(t, err)

	// The concrete data source service is faked in this e2e test, and this case
	// needs to simulate a bound target becoming inactive after binding creation.
	_, err = pool.Exec(
		ctx,
		`UPDATE data_sources
		SET is_active = false
		WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)
			AND slug = $2`,
		org,
		must.Slug("prod-users-postgres"),
	)
	require.NoError(t, err)

	resolved, err := svc.ResolvePipelineDataSourceRefs(
		ctx,
		org,
		project,
		must.Slug("prod"),
		pipeline,
	)

	require.ErrorIs(t, err, datasourcerefssvc.ErrDataSourceBindingUnavailable)
	require.Nil(t, resolved)
	diagnostic, ok := datasourcerefssvc.AsResolutionDiagnostic(err)
	require.True(t, ok)
	require.Equal(t, "steps[0].query", diagnostic.Field)
	require.Equal(t, must.Slug("users-source"), diagnostic.DataSourceRefSlug)
	require.Equal(t, must.Slug("prod-users-postgres"), diagnostic.ResolvedDataSourceSlug)
	require.Equal(t, must.Slug("prod"), diagnostic.EnvironmentSlug)
}

func TestService_SyncPipelineDataSourceRefsIntegration(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres data source ref sync integration test in short mode")
	}

	pool := e2edb.OpenPostgresPool(t, "data source ref sync integration test")
	ctx := context.Background()
	org := must.Slug("ds-ref-sync-org")
	project := must.Slug("operations")
	env := must.Slug("prod")
	refSlug := must.Slug("users-sql")
	dataSourceSlug := must.Slug("prod-users-postgres")

	resetOrganization(t, pool, org)
	t.Cleanup(func() {
		resetOrganization(t, pool, org)
	})

	seedOrganizationProjectEnvironments(t, pool, org, project, "Data Source Ref Sync Org", []slug.Slug{env})
	seedDataSource(t, pool, org, concreteDataSourceSeed{
		slug:     dataSourceSlug,
		name:     "Prod Users Postgres",
		dsType:   model.DataSourceTypePostgres,
		active:   true,
		readOnly: false,
	})

	svc := newDataSourceRefsService(pool, map[string]*datasourcessvc.DataSource{
		sourceKey(org, dataSourceSlug): {
			Slug:     dataSourceSlug,
			Name:     "Prod Users Postgres",
			Type:     model.DataSourceTypePostgres,
			IsActive: true,
		},
	})
	pipeline := pipelineWithLogicalSource(refSlug)
	seedPipelineSpec(t, pool, org, project, pipeline)

	created, err := svc.SyncPipelineDataSourceRefs(ctx, org, project, pipeline, model.UserEventActor(must.Slug("alice")))
	require.NoError(t, err)
	require.Len(t, created.Items, 1)
	require.Equal(t, datasourcerefssvc.PipelineDataSourceRefSyncStatusCreated, created.Items[0].Status)
	require.Equal(t, model.DataSourceTypeUnknown, created.Items[0].DataSourceType)
	require.Equal(t, []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource}, created.Items[0].RequiredCapabilities)

	unchanged, err := svc.SyncPipelineDataSourceRefs(ctx, org, project, pipeline, model.UserEventActor(must.Slug("alice")))
	require.NoError(t, err)
	require.Len(t, unchanged.Items, 1)
	require.Equal(t, datasourcerefssvc.PipelineDataSourceRefSyncStatusUnchanged, unchanged.Items[0].Status)

	pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{
		Kind: "sink",
		Slug: must.Slug("write-users"),
		Sink: &pipelinespec.SinkSpec{
			DataSourceSlug:   refSlug,
			OperationType:    pipelinespec.OperationTypeSQL,
			OperationRef:     must.Slug("upsert-users"),
			Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
			IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
		},
	})
	updated, err := svc.SyncPipelineDataSourceRefs(ctx, org, project, pipeline, model.UserEventActor(must.Slug("alice")))
	require.NoError(t, err)
	require.Len(t, updated.Items, 1)
	require.Equal(t, datasourcerefssvc.PipelineDataSourceRefSyncStatusUpdated, updated.Items[0].Status)
	require.Equal(t, []model.DataSourceRefCapability{
		model.DataSourceRefCapabilityPipelineSource,
		model.DataSourceRefCapabilityPipelineSink,
	}, updated.Items[0].RequiredCapabilities)

	_, err = svc.UpsertEnvironmentBinding(
		ctx,
		org,
		project,
		pipeline.Metadata.Slug,
		env,
		refSlug,
		model.UpsertDataSourceBinding{DataSourceSlug: dataSourceSlug},
		model.UserEventActor(must.Slug("alice")),
	)
	require.NoError(t, err)

	resolved, err := svc.ResolvePipelineDataSourceRefs(ctx, org, project, env, pipeline)
	require.NoError(t, err)
	require.Equal(t, dataSourceSlug, resolved.Pipeline.Steps[0].Query.DataSourceSlug)
	require.Equal(t, dataSourceSlug, resolved.Pipeline.Steps[1].Sink.DataSourceSlug)
}

func TestService_UpsertEnvironmentBindingRejectsIncompatibleTargetIntegration(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres data source ref service integration test in short mode")
	}

	pool := e2edb.OpenPostgresPool(t, "data source ref service integration test")
	ctx := context.Background()
	org := must.Slug("ds-ref-incompatible-org")
	project := must.Slug("operations")

	resetOrganization(t, pool, org)
	t.Cleanup(func() {
		resetOrganization(t, pool, org)
	})

	seedOrganizationProjectEnvironments(t, pool, org, project, "Data Source Ref Incompatible Org", []slug.Slug{
		must.Slug("prod"),
	})
	seedDataSource(t, pool, org, concreteDataSourceSeed{
		slug:     must.Slug("users-mysql"),
		name:     "Users MySQL",
		dsType:   model.DataSourceTypeMySQL,
		active:   true,
		readOnly: false,
	})
	pipeline := pipelineWithLogicalSource(must.Slug("users-source"))
	seedPipelineSpec(t, pool, org, project, pipeline)

	svc := newDataSourceRefsService(pool, map[string]*datasourcessvc.DataSource{
		sourceKey(org, must.Slug("users-mysql")): {
			Slug:     must.Slug("users-mysql"),
			Name:     "Users MySQL",
			Type:     model.DataSourceTypeMySQL,
			IsActive: true,
		},
	})

	_, err := svc.CreateDataSourceRef(ctx, org, project, pipeline.Metadata.Slug, model.NewDataSourceRef{
		Name:           "Users Source",
		Slug:           must.Slug("users-source"),
		DataSourceType: model.DataSourceTypePostgres,
		RequiredCapabilities: []model.DataSourceRefCapability{
			model.DataSourceRefCapabilityPipelineSource,
		},
	}, model.UserEventActor(must.Slug("alice")))
	require.NoError(t, err)

	got, err := svc.UpsertEnvironmentBinding(
		ctx,
		org,
		project,
		pipeline.Metadata.Slug,
		must.Slug("prod"),
		must.Slug("users-source"),
		model.UpsertDataSourceBinding{DataSourceSlug: must.Slug("users-mysql")},
		model.UserEventActor(must.Slug("alice")),
	)

	require.ErrorIs(t, err, datasourcerefssvc.ErrDataSourceRefIncompatible)
	require.Nil(t, got)
}

func newDataSourceRefsService(
	pool *pgxpool.Pool,
	concreteDataSources map[string]*datasourcessvc.DataSource,
) datasourcerefssvc.Service {
	return datasourcerefssvc.New(
		postgres.NewTransactionManager(pool),
		datasourcerefs.NewRepository(),
		&fakes.DataSourceService{
			FindBySlugFn: func(_ context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
				ds, ok := concreteDataSources[sourceKey(orgSlug, dsSlug)]
				if !ok {
					return nil, datasourcessvc.ErrDataSourceNotFound
				}
				return ds, nil
			},
		},
		eventtest.NewProducer(nil),
	)
}

func sourceKey(orgSlug, dsSlug slug.Slug) string {
	return orgSlug.String() + "/" + dsSlug.String()
}

func pipelineWithLogicalSource(refSlug slug.Slug) *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: must.Slug("users-sync"), Name: "Users Sync"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: refSlug,
					OperationType:  pipelinespec.OperationTypeSQL,
					OperationRef:   must.Slug("list-users"),
				},
			},
		},
	}
}

type concreteDataSourceSeed struct {
	slug     slug.Slug
	name     string
	dsType   model.DataSourceType
	active   bool
	readOnly bool
}

func resetOrganization(t *testing.T, pool *pgxpool.Pool, orgSlug slug.Slug) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// There is no service-level test reset path, and bindings must be removed
	// before deleting their concrete data sources because the FK is RESTRICT.
	_, err := pool.Exec(
		ctx,
		`DELETE FROM data_source_bindings
		WHERE project_environment_id IN (
			SELECT pe.id
			FROM project_environments pe
			JOIN projects p ON p.id = pe.project_id
			JOIN organizations o ON o.id = p.organization_id
			WHERE o.slug = $1
		)`,
		orgSlug,
	)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM organizations WHERE slug = $1`, orgSlug)
	require.NoError(t, err)
}

func seedOrganizationProjectEnvironments(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	orgName string,
	environmentSlugs []slug.Slug,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// These are prerequisite ownership/runtime-target fixtures for the service
	// under test; using org/project/environment services here would expand this
	// focused e2e into unrelated service wiring and authorization setup.
	_, err := pool.Exec(
		ctx,
		`INSERT INTO organizations (name, slug)
		VALUES ($1, $2)`,
		orgName,
		orgSlug,
	)
	require.NoError(t, err)

	_, err = pool.Exec(
		ctx,
		`INSERT INTO projects (organization_id, name, slug)
		SELECT id, 'Operations', $2
		FROM organizations
		WHERE slug = $1`,
		orgSlug,
		projectSlug,
	)
	require.NoError(t, err)

	for i, environmentSlug := range environmentSlugs {
		_, err = pool.Exec(
			ctx,
			`WITH org AS (
				SELECT id
				FROM organizations
				WHERE slug = $1
			), project AS (
				SELECT p.id, p.organization_id
				FROM projects p
				JOIN org ON org.id = p.organization_id
				WHERE p.slug = $2
			), environment AS (
				INSERT INTO environments (organization_id, slug, name)
				SELECT org.id, $3, $4
				FROM org
				RETURNING id, organization_id
			)
				INSERT INTO project_environments (organization_id, project_id, environment_id, is_default)
				SELECT project.organization_id, project.id, environment.id, $5
				FROM project
				JOIN environment ON environment.organization_id = project.organization_id`,
			orgSlug,
			projectSlug,
			environmentSlug,
			environmentSlug.String(),
			i == 0,
		)
		require.NoError(t, err)
	}
}

func seedPipelineSpec(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	specJSON, err := json.Marshal(pipeline)
	require.NoError(t, err)

	// Data source refs are owned by pipeline_specs. These tests exercise the
	// data source ref service directly, so they seed the owning pipeline row
	// instead of wiring the full pipeline service.
	_, err = pool.Exec(
		ctx,
		`INSERT INTO pipeline_specs (
			project_id,
			slug,
			name,
			spec_jsonb,
			spec_hash,
				sync_status,
				source_path,
				source_ref,
				last_applied_commit
		)
		SELECT
			p.id,
			$3::text,
			$4,
			$5::jsonb,
			$6,
				'synced',
				'pipelines/' || $3::text || '.pipeline.json',
				'refs/heads/main',
				'0123456789abcdef0123456789abcdef01234567'
		FROM projects p
		JOIN organizations o ON o.id = p.organization_id
		WHERE o.slug = $1
			AND p.slug = $2`,
		orgSlug,
		projectSlug,
		pipeline.Metadata.Slug,
		pipeline.Metadata.Name,
		string(specJSON),
		"test-"+pipeline.Metadata.Slug.String(),
	)
	require.NoError(t, err)
}

func seedDataSource(t *testing.T, pool *pgxpool.Pool, orgSlug slug.Slug, seed concreteDataSourceSeed) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The data source ref service only needs concrete data source identity rows
	// for FK-backed binding queries. Creating them through datasourcessvc would
	// run connector validation/encryption behavior outside this test's scope.
	_, err := pool.Exec(
		ctx,
		`WITH credential AS (
			INSERT INTO credentials (
				organization_id,
				slug,
				kind,
				credential_type,
				status,
				encrypted_payload
			)
			SELECT id, $3 || '-credential', 'static_secret', $4, 'active', decode('00', 'hex')
			FROM organizations
			WHERE slug = $1
			RETURNING id, organization_id
		)
		INSERT INTO data_sources (
			organization_id,
			credential_id,
			name,
			slug,
			type,
			config,
			is_active,
			read_only
		)
		SELECT
			organization_id,
			id,
			$2,
			$3,
			$4,
			'{}'::jsonb,
			$5,
			$6
		FROM credential`,
		orgSlug,
		seed.name,
		seed.slug,
		seed.dsType,
		seed.active,
		seed.readOnly,
	)
	require.NoError(t, err)
}
