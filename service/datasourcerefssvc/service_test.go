package datasourcerefssvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/protectedenv"
)

func TestService_CreateDataSourceRefAcceptsHubSpotSinkCapability(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			"users-source": newTestDataSourceRef(
				must.Slug("users-source"),
				model.DataSourceTypePostgres,
				model.DataSourceRefCapabilityPipelineSource,
			),
		},
	})

	got, err := svc.CreateDataSourceRef(
		context.Background(),
		must.Slug("acme"),
		must.Slug("operations"),
		defaultTestPipelineSlug,
		model.NewDataSourceRef{
			Name:                 "Contacts CRM",
			Slug:                 must.Slug("contacts-crm"),
			DataSourceType:       model.DataSourceTypeHubSpot,
			RequiredCapabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSink},
		},
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, model.DataSourceTypeHubSpot, got.DataSourceType)
}

func TestService_CreateDataSourceRefPropagatesRequiredAuditFailure(t *testing.T) {
	t.Parallel()

	auditErr := xerror.Unexpected().Detail("audit write failed")
	repo := newFakeDataSourceRefsRepository(testServiceConfig{})
	svc := New(
		testutil.FakeTransactionManager{},
		repo,
		&fakeDataSourcesService{},
		eventtest.NewProducer(failingEventRecorder{err: auditErr}),
	)

	got, err := svc.CreateDataSourceRef(
		context.Background(),
		must.Slug("acme"),
		must.Slug("operations"),
		defaultTestPipelineSlug,
		model.NewDataSourceRef{
			Name:                 "Users Source",
			DataSourceType:       model.DataSourceTypePostgres,
			RequiredCapabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource},
		},
		model.UserEventActor(must.Slug("alice")),
	)

	require.Nil(t, got)
	require.ErrorIs(t, err, auditErr)
	require.ErrorContains(t, err, "enqueue required data source ref audit event")
}

func TestService_SyncPipelineDataSourceRefsCreatesAndReusesRefsIdempotently(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, testServiceConfig{})
	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("operations")
	pipeline := newPipelineWithLogicalRef(must.Slug("analytics-warehouse"))
	pipeline.Steps[0].Query.OperationType = "bigquery"

	created, err := svc.SyncPipelineDataSourceRefs(
		ctx,
		orgSlug,
		projectSlug,
		pipeline,
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.Len(t, created.Items, 1)
	require.Equal(t, must.Slug("analytics-warehouse"), created.Items[0].RefSlug)
	require.Equal(t, model.DataSourceTypeBigQuery, created.Items[0].DataSourceType)
	require.Equal(t, []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource}, created.Items[0].RequiredCapabilities)
	require.Equal(t, PipelineDataSourceRefSyncStatusCreated, created.Items[0].Status)

	reused, err := svc.SyncPipelineDataSourceRefs(
		ctx,
		orgSlug,
		projectSlug,
		pipeline,
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.Len(t, reused.Items, 1)
	require.Equal(t, PipelineDataSourceRefSyncStatusUnchanged, reused.Items[0].Status)
}

func TestService_SyncPipelineDataSourceRefsRetainsRefsOutsideSpec(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			"analytics-warehouse": newTestDataSourceRef(
				must.Slug("analytics-warehouse"),
				model.DataSourceTypeBigQuery,
				model.DataSourceRefCapabilityPipelineSource,
			),
			"legacy-warehouse": newTestDataSourceRef(
				must.Slug("legacy-warehouse"),
				model.DataSourceTypeBigQuery,
				model.DataSourceRefCapabilityPipelineSource,
			),
		},
	})
	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("operations")
	pipeline := newPipelineWithLogicalRef(must.Slug("analytics-warehouse"))
	pipeline.Steps[0].Query.OperationType = "bigquery"

	got, err := svc.SyncPipelineDataSourceRefs(
		ctx,
		orgSlug,
		projectSlug,
		pipeline,
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	require.Equal(t, PipelineDataSourceRefSyncStatusUnchanged, got.Items[0].Status)
	retained, err := svc.GetDataSourceRef(ctx, orgSlug, projectSlug, defaultTestPipelineSlug, must.Slug("legacy-warehouse"))
	require.NoError(t, err)
	require.Equal(t, must.Slug("legacy-warehouse"), retained.Slug)
}

func TestService_ReconcilePipelineDataSourceRefsPrunesRefsOutsideSpec(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			"analytics-warehouse": newTestDataSourceRef(
				must.Slug("analytics-warehouse"),
				model.DataSourceTypeBigQuery,
				model.DataSourceRefCapabilityPipelineSource,
			),
			"legacy-warehouse": newTestDataSourceRef(
				must.Slug("legacy-warehouse"),
				model.DataSourceTypeBigQuery,
				model.DataSourceRefCapabilityPipelineSource,
			),
		},
	})
	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("operations")
	pipeline := newPipelineWithLogicalRef(must.Slug("analytics-warehouse"))
	pipeline.Steps[0].Query.OperationType = "bigquery"

	got, err := svc.ReconcilePipelineDataSourceRefs(
		ctx,
		orgSlug,
		projectSlug,
		pipeline,
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	require.Equal(t, PipelineDataSourceRefSyncStatusUnchanged, got.Items[0].Status)
	_, err = svc.GetDataSourceRef(ctx, orgSlug, projectSlug, defaultTestPipelineSlug, must.Slug("legacy-warehouse"))
	require.ErrorIs(t, err, ErrDataSourceRefNotFound)
}

func TestService_SyncPipelineDataSourceRefsScopesDuplicateSlugsByPipeline(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, testServiceConfig{})
	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("operations")
	refSlug := must.Slug("shared-source")
	ordersPipeline := newPipelineWithLogicalRef(refSlug)
	ordersPipeline.Metadata.Slug = must.Slug("orders-sync")
	customersPipeline := newPipelineWithLogicalRef(refSlug)
	customersPipeline.Metadata.Slug = must.Slug("customers-sync")

	ordersResult, err := svc.SyncPipelineDataSourceRefs(
		ctx,
		orgSlug,
		projectSlug,
		ordersPipeline,
		model.UserEventActor(must.Slug("alice")),
	)
	require.NoError(t, err)
	require.Len(t, ordersResult.Items, 1)
	require.Equal(t, ordersPipeline.Metadata.Slug, ordersResult.Items[0].PipelineSlug)
	require.Equal(t, PipelineDataSourceRefSyncStatusCreated, ordersResult.Items[0].Status)

	customersResult, err := svc.SyncPipelineDataSourceRefs(
		ctx,
		orgSlug,
		projectSlug,
		customersPipeline,
		model.UserEventActor(must.Slug("alice")),
	)
	require.NoError(t, err)
	require.Len(t, customersResult.Items, 1)
	require.Equal(t, customersPipeline.Metadata.Slug, customersResult.Items[0].PipelineSlug)
	require.Equal(t, PipelineDataSourceRefSyncStatusCreated, customersResult.Items[0].Status)

	ordersRef, err := svc.GetDataSourceRef(ctx, orgSlug, projectSlug, ordersPipeline.Metadata.Slug, refSlug)
	require.NoError(t, err)
	require.Equal(t, ordersPipeline.Metadata.Slug, ordersRef.PipelineSlug)
	customersRef, err := svc.GetDataSourceRef(ctx, orgSlug, projectSlug, customersPipeline.Metadata.Slug, refSlug)
	require.NoError(t, err)
	require.Equal(t, customersPipeline.Metadata.Slug, customersRef.PipelineSlug)

	listed, err := svc.ListDataSourceRefs(ctx, orgSlug, projectSlug, model.ListRequest{})
	require.NoError(t, err)
	require.Len(t, listed.Items, 2)
}

func TestService_SyncPipelineDataSourceRefsExpandsSourceAndSinkCapabilities(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			"customer-api": newTestDataSourceRef(
				must.Slug("customer-api"),
				model.DataSourceTypeHTTP,
				model.DataSourceRefCapabilityPipelineSource,
			),
		},
	})
	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("operations")
	pipeline := newPipelineWithLogicalRef(must.Slug("customer-api"))
	pipeline.Steps[0].Query.OperationType = "http"
	pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{
		Kind: "sink",
		Slug: "write-customer-api",
		Sink: &pipelinespec.SinkSpec{
			DataSourceSlug:   must.Slug("customer-api"),
			OperationType:    "http",
			OperationRef:     must.Slug("upsert-customers"),
			Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
			IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
		},
	})

	got, err := svc.SyncPipelineDataSourceRefs(
		ctx,
		orgSlug,
		projectSlug,
		pipeline,
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	require.Equal(t, PipelineDataSourceRefSyncStatusUpdated, got.Items[0].Status)
	require.Equal(t, []model.DataSourceRefCapability{
		model.DataSourceRefCapabilityPipelineSource,
		model.DataSourceRefCapabilityPipelineSink,
	}, got.Items[0].RequiredCapabilities)

	ref, err := svc.GetDataSourceRef(ctx, orgSlug, projectSlug, pipeline.Metadata.Slug, must.Slug("customer-api"))
	require.NoError(t, err)
	require.Equal(t, got.Items[0].RequiredCapabilities, ref.RequiredCapabilities)
}

func TestService_SyncPipelineDataSourceRefsRejectsProtectedBindingCapabilityExpansion(t *testing.T) {
	t.Parallel()

	ref := newTestDataSourceRef(
		must.Slug("customer-api"),
		model.DataSourceTypeHTTP,
		model.DataSourceRefCapabilityPipelineSource,
	)
	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			ref.Slug.String(): ref,
		},
		protectedBindingEnvs: []model.ProjectEnvironmentProtection{{
			EnvironmentSlug: must.Slug("prod"),
			Protected:       true,
		}},
	})
	pipeline := newPipelineWithLogicalRef(ref.Slug)
	pipeline.Steps[0].Query.OperationType = "http"
	pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{
		Kind: "sink",
		Slug: "write-customer-api",
		Sink: &pipelinespec.SinkSpec{
			DataSourceSlug:   ref.Slug,
			OperationType:    "http",
			OperationRef:     must.Slug("upsert-customers"),
			Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
			IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
		},
	})

	got, err := svc.SyncPipelineDataSourceRefs(
		context.Background(),
		must.Slug("acme"),
		must.Slug("operations"),
		pipeline,
		model.UserEventActor(must.Slug("alice")),
	)

	require.ErrorIs(t, err, ErrDataSourceRefIncompatible)
	require.ErrorContains(t, err, "protected environment prod")
	require.ErrorContains(t, err, `step[1] "write-customer-api"`)
	require.Nil(t, got)
}

func TestService_SyncPipelineDataSourceRefsCreatesUntypedRefForAmbiguousConnectorType(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, testServiceConfig{})
	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("operations")

	got, err := svc.SyncPipelineDataSourceRefs(
		ctx,
		orgSlug,
		projectSlug,
		newPipelineWithLogicalRef(must.Slug("users-source")),
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	require.Equal(t, must.Slug("users-source"), got.Items[0].RefSlug)
	require.Equal(t, model.DataSourceTypeUnknown, got.Items[0].DataSourceType)
	require.Equal(t, []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource}, got.Items[0].RequiredCapabilities)
	require.Equal(t, PipelineDataSourceRefSyncStatusCreated, got.Items[0].Status)

	ref, err := svc.GetDataSourceRef(ctx, orgSlug, projectSlug, defaultTestPipelineSlug, must.Slug("users-source"))
	require.NoError(t, err)
	require.Equal(t, model.DataSourceTypeUnknown, ref.DataSourceType)
}

func TestService_UntypedDataSourceRefBindsToOperatorSelectedConcreteTarget(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			"users-source": newTestDataSourceRef(
				must.Slug("users-source"),
				model.DataSourceTypeUnknown,
				model.DataSourceRefCapabilityPipelineSource,
			),
		},
		concreteDataSources: map[string]*datasourcessvc.DataSource{
			"prod-users-postgres": {
				Slug:     must.Slug("prod-users-postgres"),
				Name:     "Prod Users Postgres",
				Type:     model.DataSourceTypePostgres,
				IsActive: true,
			},
		},
	})

	binding, err := svc.UpsertEnvironmentBinding(
		context.Background(),
		must.Slug("acme"),
		must.Slug("operations"),
		defaultTestPipelineSlug,
		must.Slug("prod"),
		must.Slug("users-source"),
		model.UpsertDataSourceBinding{DataSourceSlug: must.Slug("prod-users-postgres")},
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.Equal(t, model.DataSourceTypePostgres, binding.DataSourceType)

	resolved, err := svc.ResolvePipelineDataSourceRefs(
		context.Background(),
		must.Slug("acme"),
		must.Slug("operations"),
		must.Slug("prod"),
		newPipelineWithLogicalRef(must.Slug("users-source")),
	)

	require.NoError(t, err)
	require.Equal(t, must.Slug("prod-users-postgres"), resolved.Pipeline.Steps[0].Query.DataSourceSlug)
}

func TestService_ResolvePipelineDataSourceRefsRejectsUntypedRefBindingOutsideSpecOperationFamily(t *testing.T) {
	t.Parallel()

	envSlug := must.Slug("prod")
	refSlug := must.Slug("users-source")
	dataSourceSlug := must.Slug("prod-users-postgres")
	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			refSlug.String(): newTestDataSourceRef(
				refSlug,
				model.DataSourceTypeUnknown,
				model.DataSourceRefCapabilityPipelineSource,
			),
		},
		bindings: map[string]*model.DataSourceBinding{
			bindingKey(envSlug, refSlug): {
				DataSourceRefSlug:    refSlug,
				DataSourceRefName:    refSlug.String(),
				EnvironmentSlug:      envSlug,
				DataSourceSlug:       dataSourceSlug,
				DataSourceName:       "Prod Users Postgres",
				DataSourceType:       model.DataSourceTypePostgres,
				DataSourceActive:     true,
				RequiredCapabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource},
			},
		},
	})
	pipeline := newPipelineWithLogicalRef(refSlug)
	pipeline.Steps[0].Query.OperationType = "bigquery"

	got, err := svc.ResolvePipelineDataSourceRefs(
		context.Background(),
		must.Slug("acme"),
		must.Slug("operations"),
		envSlug,
		pipeline,
	)

	require.ErrorIs(t, err, ErrDataSourceRefIncompatible)
	require.Nil(t, got)
	diagnostic, ok := AsResolutionDiagnostic(err)
	require.True(t, ok)
	require.Equal(t, refSlug, diagnostic.DataSourceRefSlug)
	require.Equal(t, dataSourceSlug, diagnostic.ResolvedDataSourceSlug)
	require.Equal(t, model.DataSourceTypePostgres, diagnostic.DataSourceType)
	require.Equal(t, model.DataSourceTypeBigQuery, diagnostic.ExpectedDataSourceType)
}

func TestService_SyncPipelineDataSourceRefsDoesNotCreateConcreteDataSources(t *testing.T) {
	t.Parallel()

	dataSources := &fakeDataSourcesService{}
	svc := New(
		testutil.FakeTransactionManager{},
		newFakeDataSourceRefsRepository(testServiceConfig{}),
		dataSources,
		eventtest.NewProducer(nil),
	)
	pipeline := newPipelineWithLogicalRef(must.Slug("webhook-api"))
	pipeline.Steps[0].Query.OperationType = "http"

	got, err := svc.SyncPipelineDataSourceRefs(
		context.Background(),
		must.Slug("acme"),
		must.Slug("operations"),
		pipeline,
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	require.Equal(t, 0, dataSources.createCalls)
}

func TestService_DataSourceRefLifecycle(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, testServiceConfig{})
	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("operations")
	pipelineSlug := defaultTestPipelineSlug
	actor := model.UserEventActor(must.Slug("alice"))

	created, err := svc.CreateDataSourceRef(
		ctx,
		orgSlug,
		projectSlug,
		pipelineSlug,
		model.NewDataSourceRef{
			Name:                 "Users Source",
			DataSourceType:       model.DataSourceTypePostgres,
			RequiredCapabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource},
		},
		actor,
	)
	require.NoError(t, err)
	require.Equal(t, must.Slug("users-source"), created.Slug)
	require.Equal(t, "Users Source", created.Name)

	got, err := svc.GetDataSourceRef(ctx, orgSlug, projectSlug, pipelineSlug, created.Slug)
	require.NoError(t, err)
	require.Equal(t, created.Slug, got.Slug)

	listed, err := svc.ListDataSourceRefs(ctx, orgSlug, projectSlug, model.ListRequest{})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	require.Equal(t, created.Slug, listed.Items[0].Slug)

	updated, err := svc.UpdateDataSourceRef(
		ctx,
		orgSlug,
		projectSlug,
		pipelineSlug,
		created.Slug,
		model.UpdateDataSourceRef{
			Name:                 "Users Source Updated",
			DataSourceType:       model.DataSourceTypePostgres,
			RequiredCapabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource},
		},
		actor,
	)
	require.NoError(t, err)
	require.Equal(t, "Users Source Updated", updated.Name)

	require.NoError(t, svc.DeleteDataSourceRef(ctx, orgSlug, projectSlug, pipelineSlug, created.Slug, actor))

	deleted, err := svc.GetDataSourceRef(ctx, orgSlug, projectSlug, pipelineSlug, created.Slug)
	require.ErrorIs(t, err, ErrDataSourceRefNotFound)
	require.Nil(t, deleted)
}

func TestService_DeleteDataSourceRefBlocksProtectedEnvironmentBindingCascade(t *testing.T) {
	t.Parallel()

	ref := newTestDataSourceRef(
		must.Slug("users-source"),
		model.DataSourceTypePostgres,
		model.DataSourceRefCapabilityPipelineSource,
	)
	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			ref.Slug.String(): ref,
		},
		protectedBindingEnvs: []model.ProjectEnvironmentProtection{{
			EnvironmentSlug: must.Slug("prod"),
			Protected:       true,
		}},
	})

	err := svc.DeleteDataSourceRef(
		context.Background(),
		must.Slug("acme"),
		must.Slug("ops"),
		defaultTestPipelineSlug,
		ref.Slug,
		model.UserEventActor(must.Slug("alice")),
	)

	require.True(t, xerror.Is(err, ErrInvalidDataSourceRefInput))
	require.ErrorContains(t, err, "protected environment prod")
}

func TestService_UpdateDataSourceRefRequiresProtectedEnvironmentConfirmation(t *testing.T) {
	t.Parallel()

	ref := newTestDataSourceRef(
		must.Slug("users-source"),
		model.DataSourceTypePostgres,
		model.DataSourceRefCapabilityPipelineSource,
	)
	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			ref.Slug.String(): ref,
		},
		protectedBindingEnvs: []model.ProjectEnvironmentProtection{{
			EnvironmentSlug: must.Slug("prod"),
			Protected:       true,
		}},
	})

	updated, err := svc.UpdateDataSourceRef(
		context.Background(),
		must.Slug("acme"),
		must.Slug("ops"),
		defaultTestPipelineSlug,
		ref.Slug,
		model.UpdateDataSourceRef{
			Name:                 "Users Sink",
			DataSourceType:       model.DataSourceTypePostgres,
			RequiredCapabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSink},
		},
		model.UserEventActor(must.Slug("alice")),
	)

	require.True(t, xerror.Is(err, protectedenv.ErrConfirmationRequired))
	require.Nil(t, updated)
}

func TestService_UpdateDataSourceRefAcceptsProtectedEnvironmentConfirmation(t *testing.T) {
	t.Parallel()

	ref := newTestDataSourceRef(
		must.Slug("users-source"),
		model.DataSourceTypePostgres,
		model.DataSourceRefCapabilityPipelineSource,
	)
	confirmation := must.Slug("prod")
	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			ref.Slug.String(): ref,
		},
		protectedBindingEnvs: []model.ProjectEnvironmentProtection{{
			EnvironmentSlug: confirmation,
			Protected:       true,
		}},
	})

	updated, err := svc.UpdateDataSourceRef(
		context.Background(),
		must.Slug("acme"),
		must.Slug("ops"),
		defaultTestPipelineSlug,
		ref.Slug,
		model.UpdateDataSourceRef{
			Name:                             "Users Sink",
			DataSourceType:                   model.DataSourceTypePostgres,
			RequiredCapabilities:             []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSink},
			ProtectedEnvironmentConfirmation: &confirmation,
		},
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.Equal(t, "Users Sink", updated.Name)
	require.Equal(t, []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSink}, updated.RequiredCapabilities)
}

func TestService_UpdateDataSourceRefRejectsMultipleProtectedEnvironmentBindings(t *testing.T) {
	t.Parallel()

	ref := newTestDataSourceRef(
		must.Slug("users-source"),
		model.DataSourceTypePostgres,
		model.DataSourceRefCapabilityPipelineSource,
	)
	confirmation := must.Slug("prod")
	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			ref.Slug.String(): ref,
		},
		protectedBindingEnvs: []model.ProjectEnvironmentProtection{
			{EnvironmentSlug: must.Slug("prod"), Protected: true},
			{EnvironmentSlug: must.Slug("stg"), Protected: true},
		},
	})

	updated, err := svc.UpdateDataSourceRef(
		context.Background(),
		must.Slug("acme"),
		must.Slug("ops"),
		defaultTestPipelineSlug,
		ref.Slug,
		model.UpdateDataSourceRef{
			Name:                             "Users Sink",
			DataSourceType:                   model.DataSourceTypePostgres,
			RequiredCapabilities:             []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSink},
			ProtectedEnvironmentConfirmation: &confirmation,
		},
		model.UserEventActor(must.Slug("alice")),
	)

	require.True(t, xerror.Is(err, ErrInvalidDataSourceRefInput))
	require.ErrorContains(t, err, "multiple protected environments")
	require.Nil(t, updated)
}

func TestService_UpdateDataSourceRefRejectsIncompatibleExistingBinding(t *testing.T) {
	t.Parallel()

	ref := newTestDataSourceRef(
		must.Slug("users-source"),
		model.DataSourceTypePostgres,
		model.DataSourceRefCapabilityPipelineSource,
	)
	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			ref.Slug.String(): ref,
		},
		bindings: map[string]*model.DataSourceBinding{
			bindingKey(must.Slug("dev"), ref.Slug): {
				DataSourceRefSlug:    ref.Slug,
				DataSourceRefName:    ref.Name,
				EnvironmentSlug:      must.Slug("dev"),
				DataSourceSlug:       must.Slug("readonly-postgres"),
				DataSourceName:       "Read-only Postgres",
				DataSourceType:       model.DataSourceTypePostgres,
				DataSourceActive:     true,
				DataSourceReadOnly:   true,
				RequiredCapabilities: ref.RequiredCapabilities,
			},
		},
	})

	updated, err := svc.UpdateDataSourceRef(
		context.Background(),
		must.Slug("acme"),
		must.Slug("ops"),
		defaultTestPipelineSlug,
		ref.Slug,
		model.UpdateDataSourceRef{
			Name:                 "Users Sink",
			DataSourceType:       model.DataSourceTypePostgres,
			RequiredCapabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSink},
		},
		model.UserEventActor(must.Slug("alice")),
	)

	require.True(t, xerror.Is(err, datasourcessvc.ErrDataSourceReadOnly))
	require.Nil(t, updated)
}

func TestService_EnvironmentBindingLifecycle(t *testing.T) {
	t.Parallel()

	ref := newTestDataSourceRef(
		must.Slug("users-source"),
		model.DataSourceTypePostgres,
		model.DataSourceRefCapabilityPipelineSource,
	)
	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			ref.Slug.String(): ref,
		},
		concreteDataSources: map[string]*datasourcessvc.DataSource{
			"prod-users-postgres": {
				Slug:     must.Slug("prod-users-postgres"),
				Name:     "Prod Users Postgres",
				Type:     model.DataSourceTypePostgres,
				IsActive: true,
			},
		},
	})
	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("operations")
	pipelineSlug := defaultTestPipelineSlug
	environmentSlug := must.Slug("prod")
	actor := model.UserEventActor(must.Slug("alice"))

	upserted, err := svc.UpsertEnvironmentBinding(
		ctx,
		orgSlug,
		projectSlug,
		pipelineSlug,
		environmentSlug,
		ref.Slug,
		model.UpsertDataSourceBinding{DataSourceSlug: must.Slug("prod-users-postgres")},
		actor,
	)
	require.NoError(t, err)
	require.Equal(t, ref.Slug, upserted.DataSourceRefSlug)
	require.Equal(t, must.Slug("prod-users-postgres"), upserted.DataSourceSlug)

	got, err := svc.GetEnvironmentBinding(ctx, orgSlug, projectSlug, pipelineSlug, environmentSlug, ref.Slug)
	require.NoError(t, err)
	require.Equal(t, upserted.DataSourceSlug, got.DataSourceSlug)

	listed, err := svc.ListEnvironmentBindings(ctx, orgSlug, projectSlug, environmentSlug, model.ListRequest{})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	require.Equal(t, ref.Slug, listed.Items[0].DataSourceRefSlug)

	require.NoError(t, svc.DeleteEnvironmentBinding(ctx, orgSlug, projectSlug, pipelineSlug, environmentSlug, ref.Slug, nil, actor))

	deleted, err := svc.GetEnvironmentBinding(ctx, orgSlug, projectSlug, pipelineSlug, environmentSlug, ref.Slug)
	require.ErrorIs(t, err, ErrDataSourceBindingNotFound)
	require.Nil(t, deleted)
}

func TestEventRecordsIncludeDataSourceRefAndBindingMetadata(t *testing.T) {
	t.Parallel()

	refRecord, err := dataSourceRefEvent{
		projectSlug:    must.Slug("operations"),
		pipelineSlug:   defaultTestPipelineSlug,
		refSlug:        must.Slug("users-source"),
		dataSourceType: model.DataSourceTypePostgres,
		capabilities: []model.DataSourceRefCapability{
			model.DataSourceRefCapabilityPipelineSource,
		},
		action: model.EventActionCreate,
	}.EventRecord()
	require.NoError(t, err)
	require.Equal(t, model.EventEntityTypeDataSourceRef, refRecord.EntityType)
	require.Equal(t, "operations", refRecord.Metadata["project_slug"])
	require.Equal(t, defaultTestPipelineSlug.String(), refRecord.Metadata["pipeline_slug"])
	require.Equal(t, "users-source", refRecord.Metadata["data_source_ref_slug"])
	require.Equal(t, "postgres", refRecord.Metadata["data_source_type"])
	require.Equal(t, []string{"pipeline_source"}, refRecord.Metadata["required_capabilities"])

	bindingRecord, err := dataSourceBindingEvent{
		projectSlug:     must.Slug("operations"),
		pipelineSlug:    defaultTestPipelineSlug,
		environmentSlug: must.Slug("prod"),
		refSlug:         must.Slug("users-source"),
		dataSourceSlug:  must.Slug("prod-users-postgres"),
		dataSourceType:  model.DataSourceTypePostgres,
		readOnly:        true,
		active:          true,
		action:          model.EventActionUpdate,
	}.EventRecord()
	require.NoError(t, err)
	require.Equal(t, model.EventEntityTypeDataSourceBinding, bindingRecord.EntityType)
	require.Equal(t, model.EventActionUpdate, bindingRecord.Action)
	require.Equal(t, "operations", bindingRecord.Metadata["project_slug"])
	require.Equal(t, defaultTestPipelineSlug.String(), bindingRecord.Metadata["pipeline_slug"])
	require.Equal(t, "prod", bindingRecord.Metadata["environment_slug"])
	require.Equal(t, "users-source", bindingRecord.Metadata["data_source_ref_slug"])
	require.Equal(t, "prod-users-postgres", bindingRecord.Metadata["data_source_slug"])
	require.Equal(t, true, bindingRecord.Metadata["data_source_read_only"])
	require.Equal(t, true, bindingRecord.Metadata["data_source_active"])

	_, err = dataSourceRefEvent{action: model.EventActionExecute}.EventRecord()
	require.Error(t, err)
	_, err = dataSourceBindingEvent{action: model.EventActionCreate}.EventRecord()
	require.Error(t, err)
}

func TestService_UpsertEnvironmentBindingRejectsInvalidConcreteTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ref        *model.DataSourceRef
		target     *datasourcessvc.DataSource
		wantErr    error
		wantDetail string
	}{
		{
			name: "MissingBindingTarget",
			ref: newTestDataSourceRef(
				must.Slug("users-source"),
				model.DataSourceTypePostgres,
				model.DataSourceRefCapabilityPipelineSource,
			),
			wantErr: datasourcessvc.ErrDataSourceNotFound,
		},
		{
			name: "InactiveConcreteDataSource",
			ref: newTestDataSourceRef(
				must.Slug("users-source"),
				model.DataSourceTypePostgres,
				model.DataSourceRefCapabilityPipelineSource,
			),
			target: &datasourcessvc.DataSource{
				Slug:     must.Slug("prod-users-postgres"),
				Type:     model.DataSourceTypePostgres,
				IsActive: false,
			},
			wantErr: ErrDataSourceBindingUnavailable,
		},
		{
			name: "ConnectorTypeMismatch",
			ref: newTestDataSourceRef(
				must.Slug("users-source"),
				model.DataSourceTypePostgres,
				model.DataSourceRefCapabilityPipelineSource,
			),
			target: &datasourcessvc.DataSource{
				Slug:     must.Slug("prod-users-mysql"),
				Type:     model.DataSourceTypeMySQL,
				IsActive: true,
			},
			wantErr: ErrDataSourceRefIncompatible,
		},
		{
			name: "ReadOnlySinkTarget",
			ref: newTestDataSourceRef(
				must.Slug("users-warehouse"),
				model.DataSourceTypeMySQL,
				model.DataSourceRefCapabilityPipelineSink,
			),
			target: &datasourcessvc.DataSource{
				Slug:     must.Slug("prod-users-mysql"),
				Type:     model.DataSourceTypeMySQL,
				IsActive: true,
				ReadOnly: true,
			},
			wantErr: datasourcessvc.ErrDataSourceReadOnly,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newTestService(t, testServiceConfig{
				dataSourceRefs: map[string]*model.DataSourceRef{
					tt.ref.Slug.String(): tt.ref,
				},
				concreteDataSources: map[string]*datasourcessvc.DataSource{
					"prod-users-postgres": tt.target,
					"prod-users-mysql":    tt.target,
				},
			})

			got, err := svc.UpsertEnvironmentBinding(
				context.Background(),
				must.Slug("acme"),
				must.Slug("operations"),
				defaultTestPipelineSlug,
				must.Slug("prod"),
				tt.ref.Slug,
				model.UpsertDataSourceBinding{DataSourceSlug: must.Slug("prod-users-postgres")},
				model.UserEventActor(must.Slug("alice")),
			)

			require.ErrorIs(t, err, tt.wantErr)
			require.Nil(t, got)
			if tt.wantDetail != "" {
				require.ErrorContains(t, err, tt.wantDetail)
			}
		})
	}
}

func TestService_ResolvePipelineDataSourceRefsUsesSelectedEnvironmentBindings(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			"users-source": newTestDataSourceRef(
				must.Slug("users-source"),
				model.DataSourceTypePostgres,
				model.DataSourceRefCapabilityPipelineSource,
			),
		},
		bindings: map[string]*model.DataSourceBinding{
			"dev/users-source": {
				DataSourceRefSlug: must.Slug("users-source"),
				EnvironmentSlug:   must.Slug("dev"),
				DataSourceSlug:    must.Slug("dev-users-postgres"),
				DataSourceType:    model.DataSourceTypePostgres,
				DataSourceActive:  true,
				RequiredCapabilities: []model.DataSourceRefCapability{
					model.DataSourceRefCapabilityPipelineSource,
				},
				BoundAt: time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
			},
			"prod/users-source": {
				DataSourceRefSlug: must.Slug("users-source"),
				EnvironmentSlug:   must.Slug("prod"),
				DataSourceSlug:    must.Slug("prod-users-postgres"),
				DataSourceType:    model.DataSourceTypePostgres,
				DataSourceActive:  true,
				RequiredCapabilities: []model.DataSourceRefCapability{
					model.DataSourceRefCapabilityPipelineSource,
				},
				BoundAt: time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
			},
		},
	})

	got, err := svc.ResolvePipelineDataSourceRefs(
		context.Background(),
		must.Slug("acme"),
		must.Slug("operations"),
		must.Slug("prod"),
		newPipelineWithLogicalRef(must.Slug("users-source")),
	)

	require.NoError(t, err)
	require.Equal(t, must.Slug("prod-users-postgres"), got.Pipeline.Steps[0].Query.DataSourceSlug)
	require.Equal(t, must.Slug("users-source"), got.Resolutions[0].RefSlug)
	require.Equal(t, must.Slug("prod-users-postgres"), got.Resolutions[0].DataSourceSlug)
}

func TestService_ResolvePipelineDataSourceRefsResolvesEnvironmentVariablesBeforeBindings(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			"users-source": newTestDataSourceRef(
				must.Slug("users-source"),
				model.DataSourceTypePostgres,
				model.DataSourceRefCapabilityPipelineSource,
			),
		},
		bindings: map[string]*model.DataSourceBinding{
			"prod/users-source": {
				DataSourceRefSlug: must.Slug("users-source"),
				EnvironmentSlug:   must.Slug("prod"),
				DataSourceSlug:    must.Slug("prod-users-postgres"),
				DataSourceType:    model.DataSourceTypePostgres,
				DataSourceActive:  true,
				RequiredCapabilities: []model.DataSourceRefCapability{
					model.DataSourceRefCapabilityPipelineSource,
				},
				BoundAt: time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
			},
		},
		runtimeVariables: []model.RuntimeVariable{
			{Key: "s3_prefix", ValueType: model.RuntimeVariableValueTypeText, Value: "prod/dir"},
			{Key: "batch_size", ValueType: model.RuntimeVariableValueTypeInteger, Value: "1000"},
		},
	})
	pipeline := newPipelineWithLogicalRef(must.Slug("users-source"))
	pipeline.Steps[0].Query.Parameters = map[string]any{
		"prefix": "{{runtime.env.s3_prefix}}",
		"limit":  "{{runtime.env.batch_size}}",
	}

	got, err := svc.ResolvePipelineDataSourceRefs(
		context.Background(),
		must.Slug("acme"),
		must.Slug("operations"),
		must.Slug("prod"),
		pipeline,
	)

	require.NoError(t, err)
	require.Equal(t, must.Slug("prod-users-postgres"), got.Pipeline.Steps[0].Query.DataSourceSlug)
	require.Equal(t, "prod/dir", got.Pipeline.Steps[0].Query.Parameters["prefix"])
	require.Equal(t, int64(1000), got.Pipeline.Steps[0].Query.Parameters["limit"])
}

func TestService_ResolvePipelineDataSourceRefsReturnsDiagnosticErrorForMissingBinding(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, testServiceConfig{
		dataSourceRefs: map[string]*model.DataSourceRef{
			"users-source": newTestDataSourceRef(
				must.Slug("users-source"),
				model.DataSourceTypePostgres,
				model.DataSourceRefCapabilityPipelineSource,
			),
		},
	})

	got, err := svc.ResolvePipelineDataSourceRefs(
		context.Background(),
		must.Slug("acme"),
		must.Slug("operations"),
		must.Slug("prod"),
		newPipelineWithLogicalRef(must.Slug("users-source")),
	)

	require.ErrorIs(t, err, ErrDataSourceBindingNotFound)
	require.True(t, xerror.Is(err, ErrDataSourceBindingNotFound))
	require.Nil(t, got)
}
