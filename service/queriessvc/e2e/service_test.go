package e2e

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	internalfake "github.com/Braidplane/braidplane-core/pkg/fake"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/environments"
	eventrepo "github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/postgres/memberinvitations"
	"github.com/Braidplane/braidplane-core/postgres/members"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/postgres/permissions"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	qrepo "github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/postgres/roles"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/environmentssvc"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/memberssvc"
	"github.com/Braidplane/braidplane-core/service/orgsvc"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

const defaultProjectSlug = slug.Slug("default")

type fakeDataSourceSchemaImportProducer struct{}

func (fakeDataSourceSchemaImportProducer) EnqueueDataSourceSchemaImport(
	context.Context,
	postgres.DBTX,
	jobs.DataSourceSchemaImportArgs,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{Kind: "data_source_schema_import", Queue: jobs.DataSourceSchemaImportQueue, State: "queued"}, nil
}

func appDBConnString() string {
	if connString := os.Getenv("DATABASE_URL"); connString != "" {
		return connString
	}

	return "postgres://postgres:postgres@localhost:35432/braidplane?sslmode=disable"
}

func connectorDBConnString() string {
	return fmt.Sprintf(
		"postgres://postgres:postgres@localhost:%d/braidplane?sslmode=disable",
		depsports.PostgresConnectorPort(),
	)
}

func connectorMySQLDSN() string {
	return fmt.Sprintf(
		"braidplane:braidplanepass@tcp(localhost:%d)/braidplane?parseTime=true",
		depsports.MySQLConnectorPort(),
	)
}

func httpConnectorBaseURL() string {
	return depsports.HTTPConnectorBaseURL()
}

func hubSpotMockBaseURL() string {
	return depsports.HubSpotMockBaseURL()
}

//go:embed testdata/postgres-db-sink-e2e.pipeline.yaml
var postgresDBSinkE2EPipeline string

//go:embed testdata/mysql-db-sink-e2e.pipeline.yaml
var mysqlDBSinkE2EPipeline string

//go:embed testdata/mongodb-sink-e2e.pipeline.yaml
var mongoDBSinkE2EPipeline string

//go:embed testdata/http-source-db-sink-e2e.pipeline.yaml
var httpSourceDBSinkE2EPipeline string

//go:embed testdata/decimal-price-file-db-sink-e2e.pipeline.yaml
var decimalPriceFileDBSinkE2EPipeline string

//go:embed testdata/hubspot-source-count-db-sink-e2e.pipeline.yaml
var hubspotSourceCountDBSinkE2EPipeline string

//go:embed testdata/git-source-db-sink-e2e.pipeline.yaml
var gitSourceDBSinkE2EPipeline string

//go:embed testdata/postgres-null-aggregate-casts-db-sink-e2e.pipeline.yaml
var postgresNullAggregateCastsDBSinkE2EPipeline string

//go:embed testdata/http-sink-e2e.pipeline.yaml
var httpSinkE2EPipeline string

//go:embed testdata/postgres-join-fanout-sinks-e2e.pipeline.yaml
var postgresJoinFanOutSinksE2EPipeline string

//go:embed testdata/mixed-source-join-fanout-sinks-e2e.pipeline.yaml
var mixedSourceJoinFanOutSinksE2EPipeline string

type noopSinkRuntime struct{}

func (noopSinkRuntime) Execute(context.Context, *pipelineruntime.SinkRuntimeInput) error {
	return nil
}

type runnerQueryExecutorAdapter struct {
	executor queriessvc.Service
}

func (a runnerQueryExecutorAdapter) Execute(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	paramValues map[string]any,
) (*result.ExecutionResult, error) {
	return a.executor.Execute(
		ctx,
		orgSlug,
		dsSlug,
		querySlug,
		model.SystemEventActor(),
		paramValues,
	)
}

func (a runnerQueryExecutorAdapter) GetExpectedInputContract(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
) (*payload.Contract, error) {
	return a.executor.GetExpectedInputContract(ctx, orgSlug, dsSlug, querySlug)
}

func (a runnerQueryExecutorAdapter) ExecuteInline(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	operationInline map[string]any,
	paramValues map[string]any,
) (*result.ExecutionResult, error) {
	return a.executor.ExecuteInline(ctx, orgSlug, dsSlug, operationInline, paramValues)
}

func (a runnerQueryExecutorAdapter) ExecuteIncremental(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	paramValues map[string]any,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	return a.executor.ExecuteIncremental(ctx, orgSlug, dsSlug, querySlug, paramValues, checkpoint)
}

func (a runnerQueryExecutorAdapter) ExecuteInlineIncremental(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	operationInline map[string]any,
	paramValues map[string]any,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	return a.executor.ExecuteInlineIncremental(ctx, orgSlug, dsSlug, operationInline, paramValues, checkpoint)
}

type testStack struct {
	orgService     orgsvc.Service
	projectsSvc    projectssvc.Service
	usersService   userssvc.UsersService
	datasourcesSvc datasourcessvc.Service
	queriesSvc     queriessvc.Service
	runtimePool    *connector.Manager
	pool           *pgxpool.Pool
}

func refreshDataSourceSchemaNow(
	t *testing.T,
	stack *testStack,
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) {
	t.Helper()

	_, err := stack.datasourcesSvc.RefreshSchema(ctx, orgSlug, dsSlug, model.SystemEventActor())
	require.NoError(t, err)

	var attemptID string
	var requestedAt time.Time
	err = stack.pool.QueryRow(
		ctx,
		`
SELECT
    ds.schema_import_attempt_id,
    ds.schema_import_started_at
FROM data_sources ds
JOIN organizations o ON ds.organization_id = o.id
WHERE o.slug = $1
    AND ds.slug = $2
    AND ds.deleted_at IS NULL`,
		orgSlug,
		dsSlug,
	).Scan(&attemptID, &requestedAt)
	require.NoError(t, err)
	require.NotEmpty(t, attemptID)
	require.False(t, requestedAt.IsZero())

	_, err = stack.datasourcesSvc.ExecuteQueuedSchemaImport(
		ctx,
		jobs.DataSourceSchemaImportArgs{
			OrganizationSlug: orgSlug,
			DataSourceSlug:   dsSlug,
			AttemptID:        attemptID,
			RequestedAt:      requestedAt,
			Actor:            model.SystemEventActor(),
		},
	)
	require.NoError(t, err)
}

// E2E tests intentionally run serially due shared external dependencies.
func TestQueryServiceE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	setupCtx, setupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer setupCancel()

	stack, err := newTestStack(setupCtx, e2eExecutionLimits())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, stack.runtimePool.Close())
		stack.pool.Close()
	})

	tests := []struct {
		name  string
		runFn func(t *testing.T, stack *testStack)
	}{
		{
			name:  "ExecutesFullPipelineRunSuccessfully",
			runFn: testExecutesFullPipelineRunSuccessfully,
		},
		{
			name:  "ExecutesFullPipelineRunWithStepInputAndSinkSuccessfully",
			runFn: testExecutesFullPipelineRunWithStepInputAndSinkSuccessfully,
		},
		{
			name:  "RejectsCrossOrgExecutionOfDestructivePostgresSinkQueryWithoutSideEffects",
			runFn: testRejectsCrossOrgExecutionOfDestructivePostgresSinkQueryWithoutSideEffects,
		},
		{
			name:  "RejectsCrossOrgExecutionOfDestructiveHTTPSinkQueryWithoutSideEffects",
			runFn: testRejectsCrossOrgExecutionOfDestructiveHTTPSinkQueryWithoutSideEffects,
		},
		{
			name:  "ExecutesPostgresPipelineRunWithRealDBInsertSinkSuccessfully",
			runFn: testExecutesPostgresPipelineRunWithRealDBInsertSinkSuccessfully,
		},
		{
			name:  "ReExecutesPostgresPipelineRunWithRealDBUpsertSinkIdempotently",
			runFn: testReExecutesPostgresPipelineRunWithRealDBUpsertSinkIdempotently,
		},
		{
			name:  "ExecutesMySQLPipelineRunWithRealDBSinkSuccessfully",
			runFn: testExecutesMySQLPipelineRunWithRealDBSinkSuccessfully,
		},
		{
			name:  "ExecutesMongoDBPipelineRunWithRealMongoSinkSuccessfully",
			runFn: testExecutesMongoDBPipelineRunWithRealMongoSinkSuccessfully,
		},
		{name: "RenewsHTTPOAuthForSavedQueriesAndPipelineReads", runFn: testRenewsHTTPOAuthForSavedQueriesAndPipelineReads},
		{
			name:  "ExecutesHTTPPipelineRunWithRealHTTPSinkSuccessfully",
			runFn: testExecutesHTTPPipelineRunWithRealHTTPSinkSuccessfully,
		},
		{
			name:  "ExecutesHTTPPipelineRunWithNativeCSVBodySinkSuccessfully",
			runFn: testExecutesHTTPPipelineRunWithNativeCSVBodySinkSuccessfully,
		},
		{
			name:  "ExecutesPostgresJoinPipelineRunWithTwoRealSinksSuccessfully",
			runFn: testExecutesPostgresJoinPipelineRunWithTwoRealSinksSuccessfully,
		},
		{
			name:  "ExecutesHTTPJSONAndCSVRowNormalizedSQLTransformWithTwoRealSinksSuccessfully",
			runFn: testExecutesHTTPJSONAndCSVRowNormalizedSQLTransformWithTwoRealSinksSuccessfully,
		},
		{
			name:  "ExecutesMySQLPipelineRunSuccessfully",
			runFn: testExecutesMySQLPipelineRunSuccessfully,
		},
		{
			name:  "ExecutesMongoDocumentSourceRowNormalizedSQLTransformOutputJSONSuccessfully",
			runFn: testExecutesMongoDocumentSourceRowNormalizedSQLTransformOutputJSONSuccessfully,
		},
		{
			name:  "RejectsFullPipelineRunWhenTransformContractMismatchesCastOutput",
			runFn: testRejectsFullPipelineRunWhenTransformContractMismatchesCastOutput,
		},
		{
			name:  "ExecutesHTTPSourcePipelineRunWithRealDBSinkSuccessfully",
			runFn: testExecutesHTTPSourcePipelineRunWithRealDBSinkSuccessfully,
		},
		{
			name:  "ExecutesDecimalPriceFilePipelineWithRealDBSinkSuccessfully",
			runFn: testExecutesDecimalPriceFilePipelineWithRealDBSinkSuccessfully,
		},
		{name: "SupplierPriceDeskUnionAll", runFn: testSupplierPriceDeskUnionAll},
		{
			name:  "PricingPatchAndDeterministicExport",
			runFn: testPricingPatchAndDeterministicExport,
		},
		{
			name:  "ExecutesHubSpotSourceCountTransformWithRealDBSinkSuccessfully",
			runFn: testExecutesHubSpotSourceCountTransformWithRealDBSinkSuccessfully,
		},
		{
			name:  "ExecutesGitSourceSQLTransformWithRealDBSinkSuccessfully",
			runFn: testExecutesGitSourceSQLTransformWithRealDBSinkSuccessfully,
		},
		{
			name:  "ExecutesNullAggregateCastsWithRealDBSinkSuccessfully",
			runFn: testExecutesNullAggregateCastsWithRealDBSinkSuccessfully,
		},
		{
			name:  "ExecutesPostgresWatermarkIncrementalPipelineWithRealConnector",
			runFn: testExecutesPostgresWatermarkIncrementalPipelineWithRealConnector,
		},
		{
			name:  "ExecutesMySQLWatermarkIncrementalPipelineWithRealConnector",
			runFn: testExecutesMySQLWatermarkIncrementalPipelineWithRealConnector,
		},
		{
			name:  "ExecutesMongoWatermarkIncrementalPipelineWithRealConnector",
			runFn: testExecutesMongoWatermarkIncrementalPipelineWithRealConnector,
		},
		{name: "HTTPWatermarkCommittedAndBlockedCheckpoints", runFn: testHTTPWatermarkCommittedAndBlockedCheckpoints},
		{
			name:  "ExecutesHTTPCursorIncrementalPipelineWithRealConnector",
			runFn: testExecutesHTTPCursorIncrementalPipelineWithRealConnector,
		},
		{
			name:  "ExecutesHubSpotCursorIncrementalPipelineWithRealConnector",
			runFn: testExecutesHubSpotCursorIncrementalPipelineWithRealConnector,
		},
		{
			name:  "ExecutesRowsSourceSQLTransformCSVOutputSuccessfully",
			runFn: testExecutesRowsSourceSQLTransformCSVOutputSuccessfully,
		},
		{name: "ExecutesPostgresQuerySuccessfully", runFn: testExecutesPostgresQuerySuccessfully},
		{
			name:  "EnforcesReadOnlySQLClassification",
			runFn: testEnforcesReadOnlySQLClassification,
		},
		{
			name:  "EnforcesReadOnlyForDirectHTTPExecution",
			runFn: testEnforcesReadOnlyForDirectHTTPExecution,
		},
		{
			name:  "RestoresSoftDeletedQueryWithExecutionHistory",
			runFn: testRestoresSoftDeletedQueryWithExecutionHistory,
		},
		{
			name:  "PurgesSoftDeletedQueryAndExecutionHistory",
			runFn: testPurgesSoftDeletedQueryAndExecutionHistory,
		},
		{
			name:  "RestoresSoftDeletedDataSourceWithMetadataAndQueries",
			runFn: testRestoresSoftDeletedDataSourceWithMetadataAndQueries,
		},
		{
			name:  "ReturnsUnavailableWhenDataSourceDeleted",
			runFn: testReturnsUnavailableWhenDataSourceDeleted,
		},
		{
			name:  "ExecutionSummaryScopesOrganizationAndExcludesDeletedResources",
			runFn: testExecutionSummaryScopesOrganizationAndExcludesDeletedResources,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.runFn(t, stack)
		})
	}
}

func newTestStack(ctx context.Context, limits queriessvc.ExecutionLimits) (*testStack, error) {
	pool, err := pgxpool.New(ctx, appDBConnString())
	if err != nil {
		return nil, fmt.Errorf("connect app db: %w", err)
	}

	txm := postgres.NewTransactionManager(pool)
	orgRepo := organizations.NewRepository()
	datasourcesRepo := datasources.New()
	schemaRepo := datasources.NewSchemaMetadataRepository()
	queriesRepo := qrepo.New()
	membersRepo := members.NewRepository()
	memberInvitationsRepo := memberinvitations.NewRepository()
	rolesRepo := roles.NewRepository()
	permissionsRepo := permissions.NewRepository()
	usersRepo := users.NewRepository()
	projectsRepo := projects.NewRepository()
	eventsService := eventssvc.New(txm, eventrepo.New(), clock.New())
	eventProducer := eventtest.NewProducer(eventsService)

	rolesService := rolessvc.New(txm, rolesRepo, permissionsRepo, eventProducer)
	usersService := userssvc.New(
		txm,
		crypto.NewArgon2(),
		usersRepo,
		&testutil.FakeEmailProducer{},
		eventProducer,
		"http://localhost:3000",
	)
	membersService := memberssvc.New(
		txm,
		membersRepo,
		memberInvitationsRepo,
		orgRepo,
		rolesService,
		usersService,
		&testutil.FakeEmailProducer{},
		eventProducer,
		"http://localhost:3000",
		clock.New(),
	)

	encryptionKey := make([]byte, 32)
	aesEnc, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": encryptionKey}, encryptionKey)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("init aes: %w", err)
	}
	// Match the source policy below: these fixtures explicitly permit loopback providers.
	credentialsSvc, err := credentialssvc.New(
		txm,
		repocredentials.New(),
		aesEnc,
		credentialssvc.ProviderRegistry{},
		clock.New(), true)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("create credentials service: %w", err)
	}
	admissionService := admissionsvc.New(txm, orgRepo)

	datasourcesSvc, err := datasourcessvc.New(
		txm,
		datasourcesRepo,
		schemaRepo,
		driftimpactrepo.New(),
		credentialsSvc,
		datasourcessvc.NewNoopGitHubAppManager(),
		datasourcessvc.NewNoopGitLabWebhookManager(),
		admissionService,
		connector.NewConnectorWithTransportPolicy(connector.NewConnector, true),
		eventProducer,
		fakeDataSourceSchemaImportProducer{},
		datasourcessvc.ServiceConfig{HTTPOAuthRedirectURI: "https://api.example/provider-connectors/http/callback", CustomerConfigPolicy: datasourcessvc.CustomerConfigPolicy{AllowPrivateDataSourceHosts: true, AllowPlaintextConnectors: true}},
	)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("create datasources service: %w", err)
	}

	environmentsRepo := environments.NewRepository()
	environmentsSvc := environmentssvc.New(txm, environmentsRepo, eventProducer)
	projectsSvc := projectssvc.New(txm, projectsRepo, nil, environmentsRepo, nil, nil, nil, nil, eventProducer)
	orgService := orgsvc.New(
		txm,
		orgRepo,
		environmentsSvc,
		membersService,
		rolesService,
		eventProducer,
		admissionService,
	)

	queryRuntimePool, err := connector.NewManager(connector.NewConnectorWithTransportPolicy(connector.NewConnector, true), connector.RuntimePoolSettings{})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("create query connector runtime pool: %w", err)
	}
	queryConnectorExecutor, err := queriessvc.NewLocalConnectorExecutor(queryRuntimePool)
	if err != nil {
		_ = queryRuntimePool.Close()
		pool.Close()
		return nil, fmt.Errorf("create local query connector executor: %w", err)
	}

	queriesSvc, err := queriessvc.NewWithExecutionLimits(
		txm,
		queriesRepo,
		datasourcesSvc,
		queryConnectorExecutor,
		credentialsSvc,
		clock.New(),
		limits,
		eventProducer,
	)
	if err != nil {
		_ = queryRuntimePool.Close()
		pool.Close()
		return nil, fmt.Errorf("create queries service: %w", err)
	}

	return &testStack{
		orgService:     orgService,
		projectsSvc:    projectsSvc,
		usersService:   usersService,
		datasourcesSvc: datasourcesSvc,
		queriesSvc:     queriesSvc,
		runtimePool:    queryRuntimePool,
		pool:           pool,
	}, nil
}

func e2eExecutionLimits() queriessvc.ExecutionLimits {
	return queriessvc.ExecutionLimits{
		QueryTimeout:                      30 * time.Second,
		ConnectorTimeout:                  30 * time.Second,
		MaxResultRows:                     1000,
		MaxResponseBytes:                  10 * 1024 * 1024,
		ConnectorConcurrencyPerDataSource: 4,
		HTTPTimeout:                       10 * time.Second,
		HTTPMaxRedirects:                  3,
		HTTPMaxRequestBytes:               1024 * 1024,
		HTTPMaxResponseBytes:              10 * 1024 * 1024,
		HTTPRetryAttempts:                 1,
		HTTPRetryBackoff:                  250 * time.Millisecond,
	}
}

func persistPipelineVersion(
	t *testing.T,
	stack *testStack,
	orgSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
) int64 {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	txm := postgres.NewTransactionManager(stack.pool)
	store, err := pipelinesvc.NewPipelineStore(stack.pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)

	materializePipelines(t, ctx, store, orgSlug, defaultProjectSlug, pipeline)

	spec, err := store.FindSpec(ctx, orgSlug, defaultProjectSlug, pipeline.Metadata.Slug)
	require.NoError(t, err)
	require.NotNil(t, spec)
	require.NotNil(t, spec.CurrentVersionID)

	return *spec.CurrentVersionID
}

func newPipelineRunRequest(
	t *testing.T,
	stack *testStack,
	orgSlug slug.Slug,
	runSlug string,
	pipeline *pipelinespec.Pipeline,
) pipelineruntime.RunRequest {
	t.Helper()

	return pipelineruntime.RunRequest{
		OrgSlug:           orgSlug,
		ProjectSlug:       defaultProjectSlug,
		EnvironmentSlug:   must.Slug("prod"),
		RunSlug:           runSlug,
		Pipeline:          pipeline,
		PipelineVersionID: persistPipelineVersion(t, stack, orgSlug, pipeline),
	}
}

func newE2EPipelineRunner(t *testing.T, stack *testStack) *pipelineruntime.Runner {
	t.Helper()

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		pipelineruntime.NewSinkRuntime(stack.queriesSvc),
	)
	require.NoError(t, err)
	return runner
}

func createPipelineWorkspaceProject(
	t *testing.T,
	stack *testStack,
	ctx context.Context,
	orgSlug slug.Slug,
	actorSlug slug.Slug,
) {
	t.Helper()

	project, err := stack.projectsSvc.CreateProject(ctx, orgSlug, model.NewProject{
		Name: "Pipeline workspace",
		Slug: defaultProjectSlug,
	}, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	require.Equal(t, defaultProjectSlug, project.Slug)

	_, err = stack.pool.Exec(ctx, `
		WITH org_row AS (
			SELECT id AS organization_id FROM organizations WHERE slug = $1
		),
		project_row AS (
			SELECT p.id AS project_id, org_row.organization_id
			FROM projects p
			JOIN org_row ON p.organization_id = org_row.organization_id
			WHERE p.slug = $2
		),
		environment_row AS (
			INSERT INTO environments (organization_id, slug, name)
			SELECT organization_id, $3, $4 FROM org_row
			ON CONFLICT (organization_id, slug) DO UPDATE SET deleted_at = NULL
			RETURNING id AS environment_id
		)
		INSERT INTO project_environments (organization_id, project_id, environment_id, is_default)
		SELECT project_row.organization_id, project_id, environment_id, false
		FROM project_row, environment_row
		ON CONFLICT (project_id, environment_id) DO NOTHING
	`, orgSlug.String(), defaultProjectSlug.String(), "prod", "prod")
	require.NoError(t, err)
}

func incrementalPostgresWatermarkPipeline(
	pipelineSlug, dsSlug, sourceQuerySlug, sinkQuerySlug, orgSlug slug.Slug,
) *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: "E2E Postgres Watermark Incremental",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: dsSlug,
					OperationType:  "sql",
					OperationRef:   sourceQuerySlug,
					Incremental: &pipelinespec.IncrementalSourceContract{
						CheckpointKind: incrementalCheckpointKind(model.IncrementalCheckpointKindWatermark),
						Field:          "updated_at",
						Ordering:       []string{"updated_at", "id"},
						DedupeKey:      "id",
					},
					OutputContract: incrementalUsersContract(),
					Parameters: map[string]any{
						"org_slug": orgSlug.String(),
					},
				},
			},
			{
				Kind: "sink",
				Slug: must.Slug("persist-users"),
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
					Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
					InputContract:    incrementalUsersContract(),
					DataSourceSlug:   dsSlug,
					OperationType:    "sql",
					OperationRef:     sinkQuerySlug,
					Config: map[string]any{
						"org_slug": orgSlug.String(),
					},
				},
			},
		},
	}
}

func incrementalWatermarkSourceOnlyPipeline(
	pipelineSlug slug.Slug,
	name string,
	dsSlug slug.Slug,
	sourceQuerySlug slug.Slug,
	operationType string,
	field string,
	parameters map[string]any,
	outputContract *payload.Contract,
) *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: name,
		},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: must.Slug("fetch-users"),
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: dsSlug,
				OperationType:  operationType,
				OperationRef:   sourceQuerySlug,
				Incremental: &pipelinespec.IncrementalSourceContract{
					CheckpointKind: incrementalCheckpointKind(model.IncrementalCheckpointKindWatermark),
					Field:          field,
					Ordering:       []string{field, "id"},
					DedupeKey:      "id",
				},
				OutputContract: outputContract,
				Parameters:     parameters,
			},
		}},
	}
}

func incrementalHTTPCursorPipeline(
	pipelineSlug, dsSlug, sourceQuerySlug slug.Slug,
) *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: "E2E HTTP Cursor Incremental",
		},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: must.Slug("fetch-users"),
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: dsSlug,
				OperationType:  "http",
				OperationRef:   sourceQuerySlug,
				Incremental: &pipelinespec.IncrementalSourceContract{
					CheckpointKind: incrementalCheckpointKind(model.IncrementalCheckpointKindCursor),
					Cursor:         "next_page_token",
				},
			},
		}},
	}
}

func incrementalCursorSourceOnlyPipeline(
	pipelineSlug slug.Slug,
	name string,
	dsSlug slug.Slug,
	sourceQuerySlug slug.Slug,
	operationType string,
	cursor string,
) *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: name,
		},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: must.Slug("fetch-users"),
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: dsSlug,
				OperationType:  operationType,
				OperationRef:   sourceQuerySlug,
				Incremental: &pipelinespec.IncrementalSourceContract{
					CheckpointKind: incrementalCheckpointKind(model.IncrementalCheckpointKindCursor),
					Cursor:         cursor,
				},
			},
		}},
	}
}

func incrementalUsersContract() *payload.Contract {
	return &payload.Contract{
		Name:           "incremental-users",
		Representation: model.ContractRepresentationRows,
		Fields: []payload.ContractField{
			{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
			{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
			{Name: "updated_at", Type: model.ContractFieldTypeValueString, Required: true},
		},
	}
}

func incrementalCheckpointKind(value model.IncrementalCheckpointKind) *model.IncrementalCheckpointKind {
	return &value
}

func currentConnectorE2EIncrementalCheckpoint(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, projectSlug, pipelineSlug, sourceStepSlug slug.Slug,
) (json.RawMessage, string) {
	t.Helper()

	var upperBound []byte
	var status string
	err := pool.QueryRow(context.Background(), `
		SELECT pic.observed_upper_bound, pic.commit_status
		FROM pipeline_incremental_checkpoints pic
		JOIN projects p ON p.id = pic.project_id
		JOIN organizations o ON o.id = p.organization_id
		WHERE o.slug = $1
			AND p.slug = $2
			AND pic.pipeline_slug = $3
			AND pic.source_step_slug = $4
			AND pic.commit_status = 'committed'
			AND pic.superseded_at IS NULL
	`, orgSlug.String(), projectSlug.String(), pipelineSlug.String(), sourceStepSlug.String()).Scan(&upperBound, &status)
	require.NoError(t, err)
	return json.RawMessage(upperBound), status
}

func createOrgAndPostgresDataSource(
	t *testing.T,
	stack *testStack,
	orgName string,
) (slug.Slug, slug.Slug) {
	t.Helper()

	orgSlug, dsSlug, _ := createOrgUserAndPostgresDataSourceWithReadOnly(t, stack, orgName, false)
	return orgSlug, dsSlug
}

func createOrgUserAndPostgresDataSource(
	t *testing.T,
	stack *testStack,
	orgName string,
) (slug.Slug, slug.Slug, slug.Slug) {
	t.Helper()

	return createOrgUserAndPostgresDataSourceWithReadOnly(t, stack, orgName, false)
}

func createOrgUserAndPostgresDataSourceWithReadOnly(
	t *testing.T,
	stack *testStack,
	orgName string,
	readOnly bool,
) (slug.Slug, slug.Slug, slug.Slug) {
	t.Helper()

	orgSlug := must.Slug(fmt.Sprintf("e2e-org-%d", time.Now().UnixNano()))
	userSlugSuffix := time.Now().UnixNano()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	user, err := stack.usersService.CreateUser(ctx, model.NewUser{
		FullName: fmt.Sprintf("E2E User %d", userSlugSuffix),
		Email:    fmt.Sprintf("e2e-user-%d@example.com", userSlugSuffix),
		Password: "password123",
		Role:     model.RoleUser,
		Locale:   "en",
	})
	require.NoError(t, err)
	require.NotNil(t, user)

	org, err := stack.orgService.CreateOrganization(ctx, user.Slug, model.UserEventActor(user.Slug), model.NewOrganization{
		Name: orgName,
		Slug: orgSlug,
	})
	require.NoError(t, err)
	require.NotNil(t, org)

	createPipelineWorkspaceProject(t, stack, ctx, org.Slug, user.Slug)

	dsSlug := must.Slug(fmt.Sprintf("e2e-postgres-ds-%d", time.Now().UnixNano()))

	dsConfig := json.RawMessage(
		fmt.Sprintf(
			`{"host":"localhost","port":%d,"database":"braidplane","ssl_mode":"disable","allow_private_hosts":true}`,
			depsports.PostgresConnectorPort(),
		),
	)
	_, err = stack.datasourcesSvc.Create(ctx, org.Slug, model.UserEventActor(user.Slug), &datasourcessvc.CreateParams{
		Name:       "E2E Postgres DS",
		Slug:       dsSlug,
		Type:       model.DataSourceTypePostgres,
		ConfigJSON: dsConfig,
		Credentials: map[string]any{
			"username": "postgres",
			"password": "postgres",
		},
		ReadOnly: readOnly,
	})
	require.NoError(t, err)

	return org.Slug, dsSlug, user.Slug
}

func createOrgAndMySQLDataSource(
	t *testing.T,
	stack *testStack,
	orgName string,
) (slug.Slug, slug.Slug) {
	t.Helper()

	return createOrgAndMySQLDataSourceWithReadOnly(t, stack, orgName, false)
}

func createOrgAndMySQLDataSourceWithReadOnly(
	t *testing.T,
	stack *testStack,
	orgName string,
	readOnly bool,
) (slug.Slug, slug.Slug) {
	t.Helper()

	orgSlug := must.Slug(fmt.Sprintf("e2e-org-mysql-%d", time.Now().UnixNano()))
	userSlugSuffix := time.Now().UnixNano()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	user, err := stack.usersService.CreateUser(ctx, model.NewUser{
		FullName: fmt.Sprintf("E2E MySQL User %d", userSlugSuffix),
		Email:    fmt.Sprintf("e2e-mysql-user-%d@example.com", userSlugSuffix),
		Password: "password123",
		Role:     model.RoleUser,
		Locale:   "en",
	})
	require.NoError(t, err)

	org, err := stack.orgService.CreateOrganization(ctx, user.Slug, model.UserEventActor(user.Slug), model.NewOrganization{
		Name: orgName,
		Slug: orgSlug,
	})
	require.NoError(t, err)
	createPipelineWorkspaceProject(t, stack, ctx, org.Slug, user.Slug)

	dsSlug := must.Slug(fmt.Sprintf("e2e-mysql-ds-%d", time.Now().UnixNano()))
	dsConfig := json.RawMessage(
		fmt.Sprintf(
			`{"host":"localhost","port":%d,"database":"braidplane","allow_private_hosts":true}`,
			depsports.MySQLConnectorPort(),
		),
	)

	_, err = stack.datasourcesSvc.Create(ctx, org.Slug, model.UserEventActor(user.Slug), &datasourcessvc.CreateParams{
		Name:       "E2E MySQL DS",
		Slug:       dsSlug,
		Type:       model.DataSourceTypeMySQL,
		ConfigJSON: dsConfig,
		Credentials: map[string]any{
			"username": "braidplane",
			"password": "braidplanepass",
		},
		ReadOnly: readOnly,
	})
	require.NoError(t, err)

	return org.Slug, dsSlug
}

func seedReadOnlyBigQueryDataSource(
	t *testing.T,
	stack *testStack,
	orgSlug, credentialSourceDSSlug slug.Slug,
) slug.Slug {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	credentialSource, err := stack.datasourcesSvc.FindBySlug(ctx, orgSlug, credentialSourceDSSlug)
	require.NoError(t, err)
	require.NotNil(t, credentialSource.Credential)

	dsSlug := must.Slug(fmt.Sprintf("e2e-bigquery-ds-%d", time.Now().UnixNano()))
	repo := datasources.New()
	created, err := repo.Create(ctx, stack.pool, orgSlug, &datasources.CreateParams{
		Name:           "E2E BigQuery Classification DS",
		Slug:           dsSlug,
		Type:           model.DataSourceTypeBigQuery,
		ConfigJSON:     json.RawMessage(`{"project_id":"analytics-prod","location":"US"}`),
		CredentialSlug: credentialSource.Credential.Slug,
		ReadOnly:       true,
	})
	require.NoError(t, err)
	_, err = repo.MarkTestSuccess(
		ctx,
		stack.pool,
		orgSlug,
		dsSlug,
		&datasources.MarkTestSuccessParams{
			ExpectedRevision: created.ConnectionTestRevision,
			TestedAt:         time.Now().UTC(),
		},
	)
	require.NoError(t, err)

	return dsSlug
}

func createOrgAndMongoDataSource(
	t *testing.T,
	stack *testStack,
	orgName string,
) (slug.Slug, slug.Slug) {
	t.Helper()

	orgSlug := must.Slug(fmt.Sprintf("e2e-org-mongo-%d", time.Now().UnixNano()))
	userSlugSuffix := time.Now().UnixNano()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	user, err := stack.usersService.CreateUser(ctx, model.NewUser{
		FullName: fmt.Sprintf("E2E Mongo User %d", userSlugSuffix),
		Email:    fmt.Sprintf("e2e-mongo-user-%d@example.com", userSlugSuffix),
		Password: "password123",
		Role:     model.RoleUser,
		Locale:   "en",
	})
	require.NoError(t, err)

	org, err := stack.orgService.CreateOrganization(ctx, user.Slug, model.UserEventActor(user.Slug), model.NewOrganization{
		Name: orgName,
		Slug: orgSlug,
	})
	require.NoError(t, err)
	createPipelineWorkspaceProject(t, stack, ctx, org.Slug, user.Slug)

	dsSlug := must.Slug(fmt.Sprintf("e2e-mongo-ds-%d", time.Now().UnixNano()))
	dsConfig := json.RawMessage(
		fmt.Sprintf(
			`{"host":"localhost","port":%d,"database":"braidplane","auth_database":"admin","allow_private_hosts":true}`,
			depsports.MongoConnectorPort(),
		),
	)

	_, err = stack.datasourcesSvc.Create(ctx, org.Slug, model.UserEventActor(user.Slug), &datasourcessvc.CreateParams{
		Name:       "E2E Mongo DS",
		Slug:       dsSlug,
		Type:       model.DataSourceTypeMongoDB,
		ConfigJSON: dsConfig,
		Credentials: map[string]any{
			"username": "root",
			"password": "rootpass",
		},
	})
	require.NoError(t, err)

	return org.Slug, dsSlug
}

func createMongoDataSource(t *testing.T, stack *testStack, orgSlug slug.Slug) slug.Slug {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dsSlug := must.Slug(fmt.Sprintf("e2e-mongo-sink-ds-%d", time.Now().UnixNano()))
	dsConfig := json.RawMessage(
		fmt.Sprintf(
			`{"host":"localhost","port":%d,"database":"braidplane","auth_database":"admin","allow_private_hosts":true}`,
			depsports.MongoConnectorPort(),
		),
	)

	_, err := stack.datasourcesSvc.Create(ctx, orgSlug, model.SystemEventActor(), &datasourcessvc.CreateParams{
		Name:       "E2E Mongo Sink DS",
		Slug:       dsSlug,
		Type:       model.DataSourceTypeMongoDB,
		ConfigJSON: dsConfig,
		Credentials: map[string]any{
			"username": "root",
			"password": "rootpass",
		},
	})
	require.NoError(t, err)

	return dsSlug
}

func createHTTPDataSource(
	t *testing.T,
	stack *testStack,
	orgSlug slug.Slug,
	baseURL string,
) slug.Slug {
	t.Helper()

	return createHTTPDataSourceWithReadOnly(t, stack, orgSlug, baseURL, false)
}

func createHTTPDataSourceWithReadOnly(
	t *testing.T,
	stack *testStack,
	orgSlug slug.Slug,
	baseURL string,
	readOnly bool,
) slug.Slug {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dsSlug := must.Slug(fmt.Sprintf("e2e-http-ds-%d", time.Now().UnixNano()))
	dsConfig := json.RawMessage(fmt.Sprintf(
		`{"base_url":%q,"health_path":"/health","timeout_seconds":5,"allow_private_hosts":true}`,
		baseURL,
	))

	_, err := stack.datasourcesSvc.Create(ctx, orgSlug, model.SystemEventActor(), &datasourcessvc.CreateParams{
		Name:        "E2E HTTP DS",
		Slug:        dsSlug,
		Type:        model.DataSourceTypeHTTP,
		ConfigJSON:  dsConfig,
		Credentials: map[string]any{},
		ReadOnly:    readOnly,
	})
	require.NoError(t, err)

	return dsSlug
}

func createHubSpotDataSource(t *testing.T, stack *testStack, orgSlug slug.Slug) slug.Slug {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dsSlug := must.Slug(fmt.Sprintf("e2e-hubspot-ds-%d", time.Now().UnixNano()))
	dsConfig := json.RawMessage(fmt.Sprintf(
		`{"base_url":%q,"timeout_seconds":5,"allow_private_hosts":true}`,
		hubSpotMockBaseURL(),
	))

	_, err := stack.datasourcesSvc.Create(ctx, orgSlug, model.SystemEventActor(), &datasourcessvc.CreateParams{
		Name:       "E2E HubSpot DS",
		Slug:       dsSlug,
		Type:       model.DataSourceTypeHubSpot,
		ConfigJSON: dsConfig,
		Credentials: map[string]any{
			"private_app_token": "pat-na1-test-token",
		},
	})
	require.NoError(t, err)

	return dsSlug
}

func createSeededGitDataSource(t *testing.T, stack *testStack, orgSlug slug.Slug) slug.Slug {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	repoURL, cleanupGitFixture, err := internalfake.BootstrapIsolatedSeededGitFixture(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		require.NoError(t, cleanupGitFixture(cleanupCtx))
	})
	privateKey, err := os.ReadFile(depsports.GitTestPrivateKeyPath())
	require.NoError(t, err)
	knownHostsOutput, err := exec.CommandContext(
		ctx,
		"ssh-keyscan",
		"-p",
		fmt.Sprintf("%d", depsports.GitSSHPort()),
		"127.0.0.1",
	).CombinedOutput()
	require.NoError(t, err, strings.TrimSpace(string(knownHostsOutput)))
	knownHosts := strings.TrimSpace(string(knownHostsOutput))
	require.NotEmpty(t, knownHosts)

	dsSlug := must.Slug(fmt.Sprintf("e2e-git-ds-%d", time.Now().UnixNano()))
	dsConfig, err := json.Marshal(map[string]any{
		"repo_url":            repoURL,
		"default_branch":      "main",
		"allow_private_hosts": true,
	})
	require.NoError(t, err)

	_, err = stack.datasourcesSvc.Create(ctx, orgSlug, model.SystemEventActor(), &datasourcessvc.CreateParams{
		Name:       "E2E Git DS",
		Slug:       dsSlug,
		Type:       model.DataSourceTypeGit,
		ConfigJSON: dsConfig,
		Credentials: map[string]any{
			"auth_type":       "ssh",
			"ssh_private_key": string(privateKey),
			"ssh_known_hosts": knownHosts + "\n",
		},
	})
	require.NoError(t, err)

	return dsSlug
}

func createQuery(
	t *testing.T,
	stack *testStack,
	orgSlug, dsSlug slug.Slug,
	name string,
	statement map[string]any,
	timeoutSeconds int,
	parameters []*queriessvc.QueryParameter,
) slug.Slug {
	t.Helper()

	statementJSON, err := json.Marshal(statement)
	require.NoError(t, err)

	queryDataJSON, err := json.Marshal(map[string]any{
		"statement":       string(statementJSON),
		"timeout_seconds": timeoutSeconds,
	})
	require.NoError(t, err)

	querySlug := must.Slug(fmt.Sprintf("e2e-query-%d", time.Now().UnixNano()))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err = stack.queriesSvc.Create(ctx, orgSlug, dsSlug, model.SystemEventActor(), &queriessvc.CreateParams{
		Name:       name,
		Slug:       querySlug,
		Data:       queryDataJSON,
		Parameters: parameters,
	})
	require.NoError(t, err)

	return querySlug
}

func resetHubSpotMock(t *testing.T, ctx context.Context, httpClient *nethttp.Client) {
	t.Helper()

	req, err := nethttp.NewRequestWithContext(
		ctx,
		nethttp.MethodPost,
		hubSpotMockBaseURL()+"/reset",
		nil,
	)
	require.NoError(t, err)

	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, nethttp.StatusOK, resp.StatusCode)
}

func seedHubSpotMockContacts(
	t *testing.T,
	ctx context.Context,
	httpClient *nethttp.Client,
	contacts []map[string]any,
) {
	t.Helper()

	bodyBytes, err := json.Marshal(map[string]any{"contacts": contacts})
	require.NoError(t, err)

	req, err := nethttp.NewRequestWithContext(
		ctx,
		nethttp.MethodPost,
		hubSpotMockBaseURL()+"/seed",
		strings.NewReader(string(bodyBytes)),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, nethttp.StatusOK, resp.StatusCode)
}

func testExecutesPostgresQuerySuccessfully(t *testing.T, stack *testStack) {
	orgSlug, dsSlug := createOrgAndPostgresDataSource(t, stack, "E2E Org Success")
	querySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Query Success",
		map[string]any{"type": "postgres", "sql": "SELECT 1 AS n"},
		30,
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	execResult, err := stack.queriesSvc.Execute(ctx, orgSlug, dsSlug, querySlug, model.SystemEventActor(), map[string]any{})
	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.True(t, execResult.IsSuccess())

	sqlResult, ok := execResult.SQL()
	require.True(t, ok)
	require.NotNil(t, sqlResult)
	require.Equal(t, 1, sqlResult.RowCount)
	require.Len(t, sqlResult.Rows, 1)
}

func testEnforcesReadOnlySQLClassification(t *testing.T, stack *testStack) {
	pgOrgSlug, pgDSSlug, _ := createOrgUserAndPostgresDataSourceWithReadOnly(
		t,
		stack,
		"E2E Read Only PostgreSQL",
		true,
	)
	mysqlOrgSlug, mysqlDSSlug := createOrgAndMySQLDataSourceWithReadOnly(
		t,
		stack,
		"E2E Read Only MySQL",
		true,
	)
	bigQueryDSSlug := seedReadOnlyBigQueryDataSource(t, stack, pgOrgSlug, pgDSSlug)

	readTests := []struct {
		name      string
		orgSlug   slug.Slug
		dsSlug    slug.Slug
		operation map[string]any
	}{
		{
			name:      "PostgresEscapeString",
			orgSlug:   pgOrgSlug,
			dsSlug:    pgDSSlug,
			operation: map[string]any{"type": "postgres", "sql": `SELECT E'can\';still text' AS value`},
		},
		{
			name:      "PostgresDollarQuotedString",
			orgSlug:   pgOrgSlug,
			dsSlug:    pgDSSlug,
			operation: map[string]any{"type": "postgres", "sql": `SELECT $tag$display;only$tag$ AS value`},
		},
		{
			name:      "PostgresReadCTE",
			orgSlug:   pgOrgSlug,
			dsSlug:    pgDSSlug,
			operation: map[string]any{"type": "postgres", "sql": "WITH source AS (SELECT 1 AS value) SELECT value FROM source"},
		},
		{
			name:      "PostgresShow",
			orgSlug:   pgOrgSlug,
			dsSlug:    pgDSSlug,
			operation: map[string]any{"type": "postgres", "sql": "SHOW transaction_read_only"},
		},
		{
			name:      "PostgresValues",
			orgSlug:   pgOrgSlug,
			dsSlug:    pgDSSlug,
			operation: map[string]any{"type": "postgres", "sql": "VALUES (1)"},
		},
		{
			name:      "PostgresExplainRead",
			orgSlug:   pgOrgSlug,
			dsSlug:    pgDSSlug,
			operation: map[string]any{"type": "postgres", "sql": "EXPLAIN SELECT 1"},
		},
		{
			name:      "PostgresLeadingComments",
			orgSlug:   pgOrgSlug,
			dsSlug:    pgDSSlug,
			operation: map[string]any{"type": "postgres", "sql": "/* display only */ -- read\n SELECT \"value\" FROM (VALUES (1)) AS source(\"value\")"},
		},
		{
			name:      "MySQLHashComment",
			orgSlug:   mysqlOrgSlug,
			dsSlug:    mysqlDSSlug,
			operation: map[string]any{"type": "mysql", "sql": "SELECT 1 # display;only\n"},
		},
		{
			name:      "MySQLReadCTE",
			orgSlug:   mysqlOrgSlug,
			dsSlug:    mysqlDSSlug,
			operation: map[string]any{"type": "mysql", "sql": "WITH source AS (SELECT 1 AS value) SELECT value FROM source"},
		},
		{
			name:      "MySQLShow",
			orgSlug:   mysqlOrgSlug,
			dsSlug:    mysqlDSSlug,
			operation: map[string]any{"type": "mysql", "sql": "SHOW STATUS LIKE 'Threads_connected'"},
		},
		{
			name:      "MySQLLeadingComments",
			orgSlug:   mysqlOrgSlug,
			dsSlug:    mysqlDSSlug,
			operation: map[string]any{"type": "mysql", "sql": "/* display only */ -- read\n SELECT `value` FROM (SELECT 1 AS `value`) AS source"},
		},
	}

	for _, tc := range readTests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()

			execResult, executeErr := stack.queriesSvc.ExecuteInline(
				ctx,
				tc.orgSlug,
				tc.dsSlug,
				tc.operation,
				map[string]any{},
			)
			require.NoError(t, executeErr)
			require.NotNil(t, execResult)
			require.True(t, execResult.IsSuccess())
		})
	}

	tests := []struct {
		name       string
		orgSlug    slug.Slug
		dsSlug     slug.Slug
		operation  map[string]any
		diagnostic string
	}{
		{
			name:       "PostgresSideEffectingFunction",
			orgSlug:    pgOrgSlug,
			dsSlug:     pgDSSlug,
			operation:  map[string]any{"type": "postgres", "sql": "SELECT pg_advisory_lock(730)"},
			diagnostic: "PostgreSQL side-effecting functions",
		},
		{
			name:       "PostgresLockingRead",
			orgSlug:    pgOrgSlug,
			dsSlug:     pgDSSlug,
			operation:  map[string]any{"type": "postgres", "sql": "SELECT 1 FOR UPDATE"},
			diagnostic: "PostgreSQL row-locking SELECT",
		},
		{
			name:       "PostgresMultipleStatements",
			orgSlug:    pgOrgSlug,
			dsSlug:     pgDSSlug,
			operation:  map[string]any{"type": "postgres", "sql": "SELECT 1; SELECT 2"},
			diagnostic: "exactly one statement",
		},
		{
			name:       "PostgresMutatingCTE",
			orgSlug:    pgOrgSlug,
			dsSlug:     pgDSSlug,
			operation:  map[string]any{"type": "postgres", "sql": "WITH changed AS (DELETE FROM users RETURNING id) SELECT id FROM changed"},
			diagnostic: "SQL common table expression",
		},
		{
			name:       "PostgresSelectInto",
			orgSlug:    pgOrgSlug,
			dsSlug:     pgDSSlug,
			operation:  map[string]any{"type": "postgres", "sql": "SELECT 1 INTO TEMP read-only-fixture_output"},
			diagnostic: "PostgreSQL SELECT INTO",
		},
		{
			name:       "PostgresUnicodeEscapedIdentifier",
			orgSlug:    pgOrgSlug,
			dsSlug:     pgDSSlug,
			operation:  map[string]any{"type": "postgres", "sql": `SELECT pg_catalog.U&"pg_advisory\005flock"(730)`},
			diagnostic: "PostgreSQL Unicode-escaped identifiers",
		},
		{
			name:       "PostgresPlainBackslashString",
			orgSlug:    pgOrgSlug,
			dsSlug:     pgDSSlug,
			operation:  map[string]any{"type": "postgres", "sql": `SELECT 'can\'t'`},
			diagnostic: "require E-prefixed escape syntax",
		},
		{
			name:       "PostgresExplainAnalyze",
			orgSlug:    pgOrgSlug,
			dsSlug:     pgDSSlug,
			operation:  map[string]any{"type": "postgres", "sql": "EXPLAIN ANALYZE SELECT 1"},
			diagnostic: "EXPLAIN ANALYZE",
		},
		{
			name:       "PostgresUnterminatedDollarQuote",
			orgSlug:    pgOrgSlug,
			dsSlug:     pgDSSlug,
			operation:  map[string]any{"type": "postgres", "sql": "SELECT $tag$unterminated"},
			diagnostic: "unterminated string, identifier, or comment",
		},
		{
			name:       "MySQLOutputFile",
			orgSlug:    mysqlOrgSlug,
			dsSlug:     mysqlDSSlug,
			operation:  map[string]any{"type": "mysql", "sql": "SELECT 1 INTO OUTFILE '/tmp/read-only-fixture'"},
			diagnostic: "MySQL output-file clauses",
		},
		{
			name:       "MySQLDottedUserVariableMutation",
			orgSlug:    mysqlOrgSlug,
			dsSlug:     mysqlDSSlug,
			operation:  map[string]any{"type": "mysql", "sql": "SELECT @counter.name := 1"},
			diagnostic: "MySQL user-variable mutation",
		},
		{
			name:       "MySQLSideEffectingFunction",
			orgSlug:    mysqlOrgSlug,
			dsSlug:     mysqlDSSlug,
			operation:  map[string]any{"type": "mysql", "sql": "SELECT GET_LOCK('read-only-fixture', 0)"},
			diagnostic: "MySQL side-effecting functions",
		},
		{
			name:       "MySQLLockingRead",
			orgSlug:    mysqlOrgSlug,
			dsSlug:     mysqlDSSlug,
			operation:  map[string]any{"type": "mysql", "sql": "SELECT 1 FOR UPDATE"},
			diagnostic: "MySQL locking reads",
		},
		{
			name:       "MySQLExecutableComment",
			orgSlug:    mysqlOrgSlug,
			dsSlug:     mysqlDSSlug,
			operation:  map[string]any{"type": "mysql", "sql": "SELECT 1 /*!50000 INTO OUTFILE '/tmp/read-only-fixture' */"},
			diagnostic: "MySQL executable comments",
		},
		{
			name:       "MySQLMultipleStatements",
			orgSlug:    mysqlOrgSlug,
			dsSlug:     mysqlDSSlug,
			operation:  map[string]any{"type": "mysql", "sql": "SELECT 1; SELECT 2"},
			diagnostic: "exactly one statement",
		},
		{
			name:       "MySQLBackslashEscapedString",
			orgSlug:    mysqlOrgSlug,
			dsSlug:     mysqlDSSlug,
			operation:  map[string]any{"type": "mysql", "sql": `SELECT 'can\'t'`},
			diagnostic: "session SQL mode is unknown",
		},
		{
			name:       "MySQLUnterminatedQuotedIdentifier",
			orgSlug:    mysqlOrgSlug,
			dsSlug:     mysqlDSSlug,
			operation:  map[string]any{"type": "mysql", "sql": "SELECT `unterminated"},
			diagnostic: "unterminated string, identifier, or comment",
		},
		{
			name:       "BigQueryExportData",
			orgSlug:    pgOrgSlug,
			dsSlug:     bigQueryDSSlug,
			operation:  map[string]any{"type": "bigquery", "operation": "query", "sql": "EXPORT DATA OPTIONS(uri='gs://bucket/out') AS SELECT 1"},
			diagnostic: "BigQuery statement type EXPORT",
		},
		{
			name:       "BigQueryMultipleStatements",
			orgSlug:    pgOrgSlug,
			dsSlug:     bigQueryDSSlug,
			operation:  map[string]any{"type": "bigquery", "operation": "query", "sql": "SELECT 1; DELETE FROM users"},
			diagnostic: "exactly one statement",
		},
		{
			name:       "BigQueryUnterminatedTripleQuote",
			orgSlug:    pgOrgSlug,
			dsSlug:     bigQueryDSSlug,
			operation:  map[string]any{"type": "bigquery", "operation": "query", "sql": "SELECT '''unterminated"},
			diagnostic: "unterminated string, identifier, or comment",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()

			execResult, executeErr := stack.queriesSvc.ExecuteInline(
				ctx,
				tc.orgSlug,
				tc.dsSlug,
				tc.operation,
				map[string]any{},
			)
			require.Nil(t, execResult)
			require.ErrorIs(t, executeErr, datasourcessvc.ErrDataSourceReadOnly)
			require.ErrorContains(t, executeErr, tc.diagnostic)
		})
	}

	bigQueryClassifierOnlyReads := []struct {
		name string
		sql  string
	}{
		{name: "RemoteRoutine", sql: "SELECT `analytics.remote_routine`(1)"},
		{name: "TripleQuotedString", sql: `SELECT '''line\n;still text'''`},
		{name: "RawString", sql: `SELECT r'path\name;still text'`},
		{name: "RawBytesPrefixes", sql: `SELECT rb'path\name;still text', br'path\name;still text'`},
	}
	for _, tc := range bigQueryClassifierOnlyReads {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			execResult, executeErr := stack.queriesSvc.ExecuteInline(
				ctx,
				pgOrgSlug,
				bigQueryDSSlug,
				map[string]any{"type": "bigquery", "operation": "query", "sql": tc.sql},
				map[string]any{},
			)
			cancel()
			require.Nil(t, execResult)
			require.NotErrorIs(t, executeErr, datasourcessvc.ErrDataSourceReadOnly)
			require.ErrorIs(t, executeErr, xerror.InvalidCredentials())
			require.ErrorContains(t, executeErr, "BigQuery service_account_json is required")
		})
	}
}

func testEnforcesReadOnlyForDirectHTTPExecution(t *testing.T, stack *testStack) {
	type receivedRequest struct {
		method string
		path   string
	}

	var (
		requestsMu sync.Mutex
		requests   []receivedRequest
	)

	server := httptest.NewServer(
		nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			requestsMu.Lock()
			requests = append(requests, receivedRequest{method: r.Method, path: r.URL.Path})
			requestsMu.Unlock()

			switch {
			case r.Method == nethttp.MethodGet && r.URL.Path == "/health":
				w.WriteHeader(nethttp.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true}`))
			case r.Method == nethttp.MethodGet && r.URL.Path == "/items":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(nethttp.StatusOK)
				_, _ = w.Write([]byte(`[{"id":1}]`))
			case r.Method == nethttp.MethodPost && r.URL.Path == "/items":
				w.WriteHeader(nethttp.StatusAccepted)
				_, _ = w.Write([]byte(`{"accepted":true}`))
			default:
				w.WriteHeader(nethttp.StatusNotFound)
			}
		}),
	)
	t.Cleanup(server.Close)

	orgSlug, _ := createOrgAndPostgresDataSource(t, stack, "E2E Org Read Only HTTP")
	httpDSSlug := createHTTPDataSourceWithReadOnly(t, stack, orgSlug, server.URL, true)

	requestsMu.Lock()
	requests = nil
	requestsMu.Unlock()

	writeQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		httpDSSlug,
		"E2E Read Only HTTP POST",
		map[string]any{
			"type":   "http",
			"method": "POST",
			"path":   "/items",
			"body":   map[string]any{"id": 1},
		},
		30,
		nil,
	)
	readQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		httpDSSlug,
		"E2E Read Only HTTP GET",
		map[string]any{
			"type":   "http",
			"method": "GET",
			"path":   "/items",
		},
		30,
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	execResult, err := stack.queriesSvc.Execute(
		ctx,
		orgSlug,
		httpDSSlug,
		writeQuerySlug,
		model.SystemEventActor(),
		map[string]any{},
	)
	require.Nil(t, execResult)
	require.ErrorIs(t, err, datasourcessvc.ErrDataSourceReadOnly)

	executions, err := stack.queriesSvc.ListExecutions(
		ctx,
		orgSlug,
		httpDSSlug,
		writeQuerySlug,
		model.ListRequest{Limit: 20},
	)
	require.NoError(t, err)
	require.Empty(t, executions.Items)

	execResult, err = stack.queriesSvc.ExecuteInline(
		ctx,
		orgSlug,
		httpDSSlug,
		map[string]any{
			"type":   "http",
			"method": "POST",
			"path":   "/items",
			"body":   map[string]any{"id": 2},
		},
		map[string]any{},
	)
	require.Nil(t, execResult)
	require.ErrorIs(t, err, datasourcessvc.ErrDataSourceReadOnly)

	execResult, err = stack.queriesSvc.Execute(
		ctx,
		orgSlug,
		httpDSSlug,
		readQuerySlug,
		model.SystemEventActor(),
		map[string]any{},
	)
	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.True(t, execResult.IsSuccess())

	requestsMu.Lock()
	defer requestsMu.Unlock()
	require.Equal(t, []receivedRequest{
		{method: nethttp.MethodGet, path: "/items"},
	}, requests)
}

func testRestoresSoftDeletedQueryWithExecutionHistory(t *testing.T, stack *testStack) {
	orgSlug, dsSlug, actorSlug := createOrgUserAndPostgresDataSource(
		t,
		stack,
		"E2E Org Query Restore",
	)
	querySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Query Restore",
		map[string]any{"type": "postgres", "sql": "SELECT 1 AS n"},
		30,
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := stack.queriesSvc.Execute(ctx, orgSlug, dsSlug, querySlug, model.SystemEventActor(), map[string]any{})
	require.NoError(t, err)

	err = stack.queriesSvc.Delete(ctx, orgSlug, dsSlug, querySlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	assertQueryDeleted(t, stack, orgSlug, dsSlug, querySlug)

	_, err = stack.queriesSvc.FindBySlug(ctx, orgSlug, dsSlug, querySlug)
	require.ErrorIs(t, err, queriessvc.ErrQueryNotFound)

	_, err = stack.queriesSvc.Execute(ctx, orgSlug, dsSlug, querySlug, model.SystemEventActor(), map[string]any{})
	require.ErrorIs(t, err, queriessvc.ErrQueryNotFound)

	err = stack.queriesSvc.Restore(ctx, orgSlug, dsSlug, querySlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)

	restored, err := stack.queriesSvc.FindBySlug(ctx, orgSlug, dsSlug, querySlug)
	require.NoError(t, err)
	require.Equal(t, querySlug, restored.Slug)

	executions, err := stack.queriesSvc.ListExecutions(
		ctx,
		orgSlug,
		dsSlug,
		querySlug,
		model.ListRequest{Limit: 20},
	)
	require.NoError(t, err)
	require.Len(t, executions.Items, 1)

	execResult, err := stack.queriesSvc.Execute(ctx, orgSlug, dsSlug, querySlug, model.SystemEventActor(), map[string]any{})
	require.NoError(t, err)
	require.True(t, execResult.IsSuccess())
}

func testPurgesSoftDeletedQueryAndExecutionHistory(t *testing.T, stack *testStack) {
	orgSlug, dsSlug, actorSlug := createOrgUserAndPostgresDataSource(
		t,
		stack,
		"E2E Org Query Purge",
	)
	querySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Query Purge",
		map[string]any{"type": "postgres", "sql": "SELECT 1 AS n"},
		30,
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := stack.queriesSvc.Execute(ctx, orgSlug, dsSlug, querySlug, model.SystemEventActor(), map[string]any{})
	require.NoError(t, err)

	err = stack.queriesSvc.Purge(ctx, orgSlug, dsSlug, querySlug, model.UserEventActor(actorSlug))
	require.ErrorIs(t, err, queriessvc.ErrQueryNotDeleted)

	err = stack.queriesSvc.Delete(ctx, orgSlug, dsSlug, querySlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	assertQueryDeleted(t, stack, orgSlug, dsSlug, querySlug)

	err = stack.queriesSvc.Purge(ctx, orgSlug, dsSlug, querySlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)

	_, err = stack.queriesSvc.FindBySlug(ctx, orgSlug, dsSlug, querySlug)
	require.ErrorIs(t, err, queriessvc.ErrQueryNotFound)

	_, err = stack.queriesSvc.ListExecutions(
		ctx,
		orgSlug,
		dsSlug,
		querySlug,
		model.ListRequest{Limit: 20},
	)
	require.ErrorIs(t, err, queriessvc.ErrQueryNotFound)

	recreated, err := stack.queriesSvc.Create(ctx, orgSlug, dsSlug, model.SystemEventActor(), &queriessvc.CreateParams{
		Name: "E2E Query Purge Recreated",
		Slug: querySlug,
		Data: []byte(`{"statement":"{\"type\":\"postgres\",\"sql\":\"SELECT 2 AS n\"}","timeout_seconds":30}`),
	})
	require.NoError(t, err)
	require.Equal(t, querySlug, recreated.Slug)
}

func testExecutionSummaryScopesOrganizationAndExcludesDeletedResources(t *testing.T, stack *testStack) {
	orgSlug, dsSlug, actorSlug := createOrgUserAndPostgresDataSource(t, stack, "E2E Execution Summary")
	otherOrgSlug, otherDSSlug, _ := createOrgUserAndPostgresDataSource(t, stack, "E2E Execution Summary Other")
	firstQuerySlug := createQuery(t, stack, orgSlug, dsSlug, "E2E First Summary Query", map[string]any{"type": "postgres", "sql": "SELECT 1"}, 30, nil)
	latestQuerySlug := createQuery(t, stack, orgSlug, dsSlug, "E2E Latest Summary Query", map[string]any{"type": "postgres", "sql": "SELECT 2"}, 30, nil)
	otherQuerySlug := createQuery(t, stack, otherOrgSlug, otherDSSlug, "E2E Other Summary Query", map[string]any{"type": "postgres", "sql": "SELECT 3"}, 30, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	base := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	insertExecution := func(querySlug, executionSlug slug.Slug, executedAt time.Time) {
		t.Helper()
		_, err := stack.pool.Exec(ctx, `
			INSERT INTO query_executions (query_id, slug, status, result, executed_at)
			SELECT q.id, $1, 'success', '{}'::jsonb, $2
			FROM queries q
			JOIN data_sources ds ON q.data_source_id = ds.id
			JOIN organizations o ON ds.organization_id = o.id
			WHERE o.slug = $3 AND ds.slug = $4 AND q.slug = $5
		`, executionSlug, executedAt, orgSlug, dsSlug, querySlug)
		require.NoError(t, err)
	}

	insertExecution(firstQuerySlug, must.Slug("summary-first"), base)
	insertExecution(latestQuerySlug, must.Slug("summary-latest"), base.Add(time.Minute))
	_, err := stack.pool.Exec(ctx, `
		INSERT INTO query_executions (query_id, slug, status, result, executed_at)
		SELECT q.id, $1, 'success', '{}'::jsonb, $2
		FROM queries q
		JOIN data_sources ds ON q.data_source_id = ds.id
		JOIN organizations o ON ds.organization_id = o.id
		WHERE o.slug = $3 AND ds.slug = $4 AND q.slug = $5
	`, must.Slug("summary-other"), base.Add(2*time.Minute), otherOrgSlug, otherDSSlug, otherQuerySlug)
	require.NoError(t, err)

	summary, err := stack.queriesSvc.GetExecutionSummary(ctx, orgSlug)
	require.NoError(t, err)
	require.NotNil(t, summary)
	require.Equal(t, must.Slug("summary-latest"), summary.ExecutionSlug)
	require.Equal(t, latestQuerySlug, summary.QuerySlug)

	require.NoError(t, stack.queriesSvc.Delete(ctx, orgSlug, dsSlug, latestQuerySlug, model.UserEventActor(actorSlug)))
	summary, err = stack.queriesSvc.GetExecutionSummary(ctx, orgSlug)
	require.NoError(t, err)
	require.NotNil(t, summary)
	require.Equal(t, must.Slug("summary-first"), summary.ExecutionSlug)

	require.NoError(t, stack.datasourcesSvc.Delete(ctx, orgSlug, dsSlug, model.UserEventActor(actorSlug)))
	summary, err = stack.queriesSvc.GetExecutionSummary(ctx, orgSlug)
	require.NoError(t, err)
	require.Nil(t, summary)
}

func assertQueryDeleted(
	t *testing.T,
	stack *testStack,
	orgSlug, dsSlug, querySlug slug.Slug,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var deletedAt sql.NullTime
	err := stack.pool.QueryRow(ctx, `
		SELECT q.deleted_at
		FROM queries q
		JOIN data_sources ds ON q.data_source_id = ds.id
		JOIN organizations o ON ds.organization_id = o.id
		WHERE o.slug = $1
			AND ds.slug = $2
			AND q.slug = $3
	`, orgSlug.String(), dsSlug.String(), querySlug.String()).Scan(&deletedAt)
	require.NoError(t, err)
	require.True(t, deletedAt.Valid)
}

func testRestoresSoftDeletedDataSourceWithMetadataAndQueries(t *testing.T, stack *testStack) {
	orgSlug, dsSlug, actorSlug := createOrgUserAndPostgresDataSource(
		t,
		stack,
		"E2E Org Data Source Restore",
	)
	querySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Query Data Source Restore",
		map[string]any{"type": "postgres", "sql": "SELECT 1 AS n"},
		30,
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	refreshDataSourceSchemaNow(t, stack, ctx, orgSlug, dsSlug)

	_, err := stack.queriesSvc.Execute(ctx, orgSlug, dsSlug, querySlug, model.SystemEventActor(), map[string]any{})
	require.NoError(t, err)

	metadataRowsBefore := countDataSourceSchemaMetadata(t, stack, orgSlug, dsSlug)
	require.Equal(t, 1, metadataRowsBefore)

	err = stack.datasourcesSvc.Delete(ctx, orgSlug, dsSlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	assertDataSourceDeleted(t, stack, orgSlug, dsSlug)
	assertSoftDeletedDataSourceRetainsCascadedRows(
		t,
		stack,
		orgSlug,
		dsSlug,
		querySlug,
		metadataRowsBefore,
	)

	dsList, err := stack.datasourcesSvc.ListByOrg(ctx, orgSlug, model.ListRequest{Limit: 20})
	require.NoError(t, err)
	for _, ds := range dsList.Items {
		require.NotEqual(t, dsSlug, ds.Slug)
	}

	_, err = stack.datasourcesSvc.FindBySlug(ctx, orgSlug, dsSlug)
	require.ErrorIs(t, err, datasourcessvc.ErrDataSourceNotFound)

	_, err = stack.datasourcesSvc.Update(
		ctx,
		orgSlug,
		dsSlug,
		model.UserEventActor(actorSlug),
		&datasourcessvc.UpdateParams{Name: "Renamed Postgres DS"},
	)
	require.ErrorIs(t, err, datasourcessvc.ErrDataSourceNotFound)

	_, err = stack.datasourcesSvc.UpdateCredentials(
		ctx,
		orgSlug,
		dsSlug,
		model.UserEventActor(actorSlug),
		&datasourcessvc.UpdateCredentialsParams{
			Credentials: map[string]any{"username": "postgres", "password": "postgres"},
		},
	)
	require.ErrorIs(t, err, datasourcessvc.ErrDataSourceNotFound)

	_, err = stack.datasourcesSvc.IntrospectSchema(ctx, orgSlug, dsSlug)
	require.ErrorIs(t, err, datasourcessvc.ErrDataSourceNotFound)

	_, err = stack.datasourcesSvc.RefreshSchema(ctx, orgSlug, dsSlug, model.SystemEventActor())
	require.ErrorIs(t, err, datasourcessvc.ErrDataSourceNotFound)

	_, err = stack.queriesSvc.Create(ctx, orgSlug, dsSlug, model.SystemEventActor(), &queriessvc.CreateParams{
		Name: "E2E Query Hidden Data Source",
		Slug: must.Slug("hidden-data-source-query"),
		Data: []byte(`{"statement":"{\"type\":\"postgres\",\"sql\":\"SELECT 2 AS n\"}","timeout_seconds":30}`),
	})
	require.ErrorIs(t, err, qrepo.ErrDataSourceNotFound)

	_, err = stack.queriesSvc.Execute(ctx, orgSlug, dsSlug, querySlug, model.SystemEventActor(), map[string]any{})
	require.ErrorIs(t, err, queriessvc.ErrDataSourceUnavailable)

	_, err = stack.queriesSvc.ListExecutions(
		ctx,
		orgSlug,
		dsSlug,
		querySlug,
		model.ListRequest{Limit: 20},
	)
	require.ErrorIs(t, err, qrepo.ErrQueryNotFound)

	_, err = stack.queriesSvc.GetExecution(
		ctx,
		orgSlug,
		dsSlug,
		querySlug,
		must.Slug("missing-execution"),
	)
	require.ErrorIs(t, err, qrepo.ErrQueryNotFound)

	err = stack.datasourcesSvc.Restore(ctx, orgSlug, dsSlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	assertDataSourceTombstoneCleared(t, stack, orgSlug, dsSlug)

	restored, err := stack.datasourcesSvc.FindBySlug(ctx, orgSlug, dsSlug)
	require.NoError(t, err)
	require.Equal(t, dsSlug, restored.Slug)

	execResult, err := stack.queriesSvc.Execute(ctx, orgSlug, dsSlug, querySlug, model.SystemEventActor(), map[string]any{})
	require.NoError(t, err)
	require.True(t, execResult.IsSuccess())

	executions, err := stack.queriesSvc.ListExecutions(
		ctx,
		orgSlug,
		dsSlug,
		querySlug,
		model.ListRequest{Limit: 20},
	)
	require.NoError(t, err)
	require.Len(t, executions.Items, 2)
}

func countDataSourceSchemaMetadata(
	t *testing.T,
	stack *testStack,
	orgSlug, dsSlug slug.Slug,
) int {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var count int
	err := stack.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM schema_metadata sm
		JOIN data_sources ds ON sm.data_source_id = ds.id
		JOIN organizations o ON ds.organization_id = o.id
		WHERE o.slug = $1
			AND ds.slug = $2
	`, orgSlug.String(), dsSlug.String()).Scan(&count)
	require.NoError(t, err)

	return count
}

func assertSoftDeletedDataSourceRetainsCascadedRows(
	t *testing.T,
	stack *testStack,
	orgSlug, dsSlug, querySlug slug.Slug,
	wantMetadataRows int,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var dataSourceRows int
	err := stack.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM data_sources ds
		JOIN organizations o ON ds.organization_id = o.id
		WHERE o.slug = $1
			AND ds.slug = $2
			AND ds.deleted_at IS NOT NULL
	`, orgSlug.String(), dsSlug.String()).Scan(&dataSourceRows)
	require.NoError(t, err)
	require.Equal(t, 1, dataSourceRows)

	var queryRows int
	err = stack.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM queries q
		JOIN data_sources ds ON q.data_source_id = ds.id
		JOIN organizations o ON ds.organization_id = o.id
		WHERE o.slug = $1
			AND ds.slug = $2
			AND q.slug = $3
	`, orgSlug.String(), dsSlug.String(), querySlug.String()).Scan(&queryRows)
	require.NoError(t, err)
	require.Equal(t, 1, queryRows)
	require.Equal(t, wantMetadataRows, countDataSourceSchemaMetadata(t, stack, orgSlug, dsSlug))
}

func assertDataSourceDeleted(
	t *testing.T,
	stack *testStack,
	orgSlug, dsSlug slug.Slug,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var deletedAt sql.NullTime
	err := stack.pool.QueryRow(ctx, `
		SELECT ds.deleted_at
		FROM data_sources ds
		JOIN organizations o ON ds.organization_id = o.id
		WHERE o.slug = $1
			AND ds.slug = $2
	`, orgSlug.String(), dsSlug.String()).Scan(&deletedAt)
	require.NoError(t, err)
	require.True(t, deletedAt.Valid)
}

func assertDataSourceTombstoneCleared(
	t *testing.T,
	stack *testStack,
	orgSlug, dsSlug slug.Slug,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var deletedAt sql.NullTime
	var deleteReason sql.NullString
	err := stack.pool.QueryRow(ctx, `
		SELECT ds.deleted_at, ds.delete_reason
		FROM data_sources ds
		JOIN organizations o ON ds.organization_id = o.id
		WHERE o.slug = $1
			AND ds.slug = $2
	`, orgSlug.String(), dsSlug.String()).Scan(&deletedAt, &deleteReason)
	require.NoError(t, err)
	require.False(t, deletedAt.Valid)
	require.False(t, deleteReason.Valid)
}

func testExecutesFullPipelineRunSuccessfully(t *testing.T, stack *testStack) {
	orgSlug, dsSlug := createOrgAndPostgresDataSource(t, stack, "E2E Org Full Pipeline")
	querySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Query Pipeline Run",
		map[string]any{
			"type": "postgres",
			"sql":  "SELECT 1 AS id UNION ALL SELECT 5 UNION ALL SELECT 3 UNION ALL SELECT 2 UNION ALL SELECT 4",
		},
		30,
		nil,
	)

	pipelineSlug := slug.Slug(orgSlug.String())
	pipelineRunSlug := fmt.Sprintf("e2e-pipeline-run-%d", time.Now().UnixNano())
	pipelineSpecYAML := fmt.Sprintf(`metadata:
  slug: %s
  name: E2E Full Pipeline
steps:
  - kind: query
    slug: fetch-e2e-data
    query:
      data_source_ref: %s
      operation_type: sql
      operation_ref: %s
      output_contract:
        fields:
          - name: id
            type: integer
            required: true
  - kind: transform
    slug: transform-e2e-data
    transform:
      inputs:
        input: fetch-e2e-data
      sql: SELECT id AS id FROM input WHERE id > 1 ORDER BY id DESC
      input_contract:
        fields:
          - name: id
            type: integer
            required: true
      output_kind: rows
`, pipelineSlug, dsSlug, querySlug)

	pipelineSpec, err := pipelinevalidate.Load("e2e-full-pipeline.yaml", []byte(pipelineSpecYAML))
	require.NoError(t, err)
	require.Equal(t, pipelineSlug, pipelineSpec.Metadata.Slug)
	require.Equal(t, "E2E Full Pipeline", pipelineSpec.Metadata.Name)
	require.Len(t, pipelineSpec.Steps, 2)
	require.NotNil(t, pipelineSpec.Steps[0].Query)
	require.NotNil(t, pipelineSpec.Steps[1].Transform)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		noopSinkRuntime{},
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	runResult, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, pipelineRunSlug, pipelineSpec),
	)
	require.NoError(t, err)
	require.Equal(t, pipelineRunSlug, runResult.Metadata.RunSlug)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Equal(t, 2, runResult.Metadata.StepCount)

	queryStepResult, ok := runResult.StepResults[must.Slug("fetch-e2e-data")]
	require.True(t, ok)
	sqlResult, ok := queryStepResult.SQL()
	require.True(t, ok)
	require.Equal(t, 5, sqlResult.RowCount)
	require.Equal(t, []string{"id"}, sqlResult.ColumnNames)
	require.Equal(
		t,
		[]map[string]any{
			{"id": int64(1)},
			{"id": int64(5)},
			{"id": int64(3)},
			{"id": int64(2)},
			{"id": int64(4)},
		},
		sqlResult.Rows,
	)

	transformStepResult, ok := runResult.StepResults[must.Slug("transform-e2e-data")]
	require.True(t, ok)
	transformOutput, ok := transformStepResult.Transform()
	require.True(t, ok)
	require.Equal(t, 4, transformOutput.RowCount)
	require.Equal(t, []string{"id"}, transformOutput.ColumnNames)
	require.Equal(
		t,
		[]map[string]any{{"id": int64(5)}, {"id": int64(4)}, {"id": int64(3)}, {"id": int64(2)}},
		transformOutput.Rows,
	)

	runRepo := pipelineruns.New()
	persistedRun, err := runRepo.FindRunBySlug(
		ctx,
		stack.pool,
		orgSlug,
		defaultProjectSlug,
		must.Slug("prod"),
		pipelineRunSlug,
	)
	require.NoError(t, err)
	require.Equal(t, pipelineRunSlug, persistedRun.RunSlug)
	require.Equal(t, pipelineSlug.String(), persistedRun.PipelineSlug)
	require.Equal(t, "succeeded", persistedRun.Status)
	require.Equal(t, int32(2), persistedRun.StepCount)
}

func testRejectsCrossOrgExecutionOfDestructivePostgresSinkQueryWithoutSideEffects(
	t *testing.T,
	stack *testStack,
) {
	orgASlug, dsASlug := createOrgAndPostgresDataSource(
		t,
		stack,
		"E2E Org Postgres Sink Isolation Source",
	)
	orgBSlug, _ := createOrgAndPostgresDataSource(
		t,
		stack,
		"E2E Org Postgres Sink Isolation Attacker",
	)

	setupCtx, setupCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer setupCancel()

	connectorPool, err := pgxpool.New(setupCtx, connectorDBConnString())
	require.NoError(t, err)
	defer connectorPool.Close()

	_, err = connectorPool.Exec(setupCtx, `
		CREATE TABLE IF NOT EXISTS e2e_sink_users_isolation (
			org_slug text NOT NULL,
			id bigint NOT NULL,
			email text NOT NULL,
			PRIMARY KEY (org_slug, id)
		)
	`)
	require.NoError(t, err)

	_, err = connectorPool.Exec(
		setupCtx,
		"DELETE FROM e2e_sink_users_isolation WHERE org_slug = $1 OR org_slug = $2",
		orgASlug.String(),
		orgBSlug.String(),
	)
	require.NoError(t, err)

	sinkQuerySlug := createQuery(
		t,
		stack,
		orgASlug,
		dsASlug,
		"E2E Cross Org Sink Isolation Insert",
		map[string]any{
			"type": "postgres",
			"sql": `
				INSERT INTO e2e_sink_users_isolation (org_slug, id, email)
				VALUES ({{org_slug}}, {{id}}::bigint, {{email}})
			`,
		},
		30,
		[]*queriessvc.QueryParameter{
			{Name: "org_slug", DataType: queriessvc.ParameterDataTypeText, IsRequired: true},
			{Name: "id", DataType: queriessvc.ParameterDataTypeText, IsRequired: true},
			{Name: "email", DataType: queriessvc.ParameterDataTypeText, IsRequired: true},
		},
	)

	execCtx, execCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer execCancel()

	execResult, err := stack.queriesSvc.Execute(
		execCtx,
		orgBSlug,
		dsASlug,
		sinkQuerySlug,
		model.SystemEventActor(),
		map[string]any{
			"org_slug": orgASlug.String(),
			"id":       "101",
			"email":    "blocked-cross-org@example.com",
		},
	)
	require.Nil(t, execResult)
	require.ErrorIs(t, err, queriessvc.ErrDataSourceUnavailable)

	var orgARowCount int
	err = connectorPool.QueryRow(
		execCtx,
		"SELECT COUNT(*) FROM e2e_sink_users_isolation WHERE org_slug = $1",
		orgASlug.String(),
	).Scan(&orgARowCount)
	require.NoError(t, err)
	require.Equal(t, 0, orgARowCount)

	var orgBRowCount int
	err = connectorPool.QueryRow(
		execCtx,
		"SELECT COUNT(*) FROM e2e_sink_users_isolation WHERE org_slug = $1",
		orgBSlug.String(),
	).Scan(&orgBRowCount)
	require.NoError(t, err)
	require.Equal(t, 0, orgBRowCount)
}

func testRejectsCrossOrgExecutionOfDestructiveHTTPSinkQueryWithoutSideEffects(
	t *testing.T,
	stack *testStack,
) {
	type receivedRequest struct {
		method string
		path   string
		body   map[string]any
	}

	var (
		requestsMu sync.Mutex
		requests   []receivedRequest
	)

	sinkServer := httptest.NewServer(
		nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			request := receivedRequest{method: r.Method, path: r.URL.Path}

			if r.Method == nethttp.MethodGet && r.URL.Path == "/health" {
				requestsMu.Lock()
				requests = append(requests, request)
				requestsMu.Unlock()
				w.WriteHeader(nethttp.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}

			if r.Method == nethttp.MethodPost && r.URL.Path == "/sink" {
				defer func() { _ = r.Body.Close() }()

				if err := json.NewDecoder(r.Body).Decode(&request.body); err != nil {
					w.WriteHeader(nethttp.StatusBadRequest)
					return
				}
			}

			requestsMu.Lock()
			requests = append(requests, request)
			requestsMu.Unlock()

			if r.Method != nethttp.MethodPost || r.URL.Path != "/sink" {
				w.WriteHeader(nethttp.StatusNotFound)
				return
			}
			w.WriteHeader(nethttp.StatusAccepted)
			_, _ = w.Write([]byte(`{"accepted":true}`))
		}),
	)
	t.Cleanup(sinkServer.Close)

	orgASlug, _ := createOrgAndPostgresDataSource(t, stack, "E2E Org HTTP Sink Isolation Owner")
	sinkDSSlug := createHTTPDataSource(t, stack, orgASlug, sinkServer.URL)
	requestsMu.Lock()
	requests = nil
	requestsMu.Unlock()

	orgBSlug, _ := createOrgAndPostgresDataSource(
		t,
		stack,
		"E2E Org HTTP Sink Isolation Attacker",
	)

	sinkQuerySlug := createQuery(
		t,
		stack,
		orgASlug,
		sinkDSSlug,
		"E2E Cross Org Sink Isolation HTTP POST",
		map[string]any{
			"type":   "http",
			"method": "POST",
			"path":   "/sink",
			"body": map[string]any{
				"source": "cross-org-test",
				"id":     "{{id}}",
				"email":  "{{email}}",
			},
		},
		30,
		[]*queriessvc.QueryParameter{
			{Name: "id", DataType: queriessvc.ParameterDataTypeText, IsRequired: true},
			{Name: "email", DataType: queriessvc.ParameterDataTypeText, IsRequired: true},
		},
	)

	execCtx, execCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer execCancel()

	execResult, err := stack.queriesSvc.Execute(
		execCtx,
		orgBSlug,
		sinkDSSlug,
		sinkQuerySlug,
		model.SystemEventActor(),
		map[string]any{
			"id":    "401",
			"email": "blocked-cross-org-http@example.com",
		},
	)
	require.Nil(t, execResult)
	require.ErrorIs(t, err, queriessvc.ErrDataSourceUnavailable)

	// The blocked cross-org execution must not reach the connector at all,
	// including health probes.
	requestsMu.Lock()
	receivedRequests := append([]receivedRequest(nil), requests...)
	requestsMu.Unlock()
	require.Empty(t, receivedRequests)
}

func testExecutesPostgresPipelineRunWithRealDBInsertSinkSuccessfully(
	t *testing.T,
	stack *testStack,
) {
	orgSlug, dsSlug := createOrgAndPostgresDataSource(t, stack, "E2E Org Postgres DB Insert Sink")

	refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer refreshCancel()

	refreshDataSourceSchemaNow(t, stack, refreshCtx, orgSlug, dsSlug)

	setupCtx, setupCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer setupCancel()

	connectorPool, err := pgxpool.New(setupCtx, connectorDBConnString())
	require.NoError(t, err)
	defer connectorPool.Close()

	_, err = connectorPool.Exec(setupCtx, `
		CREATE TABLE IF NOT EXISTS e2e_sink_users_insert (
			org_slug text NOT NULL,
			id bigint NOT NULL,
			email text NOT NULL,
			PRIMARY KEY (org_slug, id)
		)
	`)
	require.NoError(t, err)

	_, err = connectorPool.Exec(
		setupCtx,
		"DELETE FROM e2e_sink_users_insert WHERE org_slug = $1",
		orgSlug.String(),
	)
	require.NoError(t, err)

	sourceQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Source Query DB Insert Sink",
		map[string]any{
			"type": "postgres",
			"sql":  "SELECT id, email FROM users WHERE email IN ('alice@example.com', 'bob@example.com') ORDER BY id",
		},
		30,
		nil,
	)

	sinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Sink Query DB Insert",
		map[string]any{
			"type": "postgres",
			"sql": `
				INSERT INTO e2e_sink_users_insert (org_slug, id, email)
				SELECT {{org_slug}}, id, email
				FROM input
			`,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	pipelineSlug := fmt.Sprintf("e2e-postgres-db-insert-%d", time.Now().UnixNano())
	pipelineYAML := postgresDBSinkE2EPipeline
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PIPELINE_SLUG}", pipelineSlug)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${DATA_SOURCE_SLUG}", dsSlug.String())
	pipelineYAML = strings.ReplaceAll(
		pipelineYAML,
		"${SOURCE_QUERY_SLUG}",
		sourceQuerySlug.String(),
	)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${SINK_QUERY_SLUG}", sinkQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${ORG_SLUG}", orgSlug.String())

	pipelineSpec, err := pipelinevalidate.Load(
		"postgres-db-sink-insert-e2e.pipeline.yaml",
		[]byte(pipelineYAML),
	)
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		pipelineruntime.NewSinkRuntime(stack.queriesSvc),
	)
	require.NoError(t, err)

	runCtx, runCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer runCancel()

	runSlug := fmt.Sprintf("e2e-db-insert-run-%d", time.Now().UnixNano())
	runResult, err := runner.Run(
		runCtx,
		newPipelineRunRequest(t, stack, orgSlug, runSlug, pipelineSpec),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Len(t, runResult.Timeline.Snapshots, 3)
	require.Equal(t, must.Slug("persist-users"), runResult.Timeline.Snapshots[2].Step)

	rows, err := connectorPool.Query(
		runCtx,
		"SELECT id, email FROM e2e_sink_users_insert WHERE org_slug = $1 ORDER BY id",
		orgSlug.String(),
	)
	require.NoError(t, err)
	defer rows.Close()

	var actual []map[string]any
	for rows.Next() {
		var id int64
		var email string
		err := rows.Scan(&id, &email)
		require.NoError(t, err)
		actual = append(actual, map[string]any{"id": id, "email": email})
	}
	require.NoError(t, rows.Err())

	require.Equal(t, []map[string]any{
		{"id": int64(1), "email": "alice@example.com"},
		{"id": int64(2), "email": "bob@example.com"},
	}, actual)
}

func testReExecutesPostgresPipelineRunWithRealDBUpsertSinkIdempotently(
	t *testing.T,
	stack *testStack,
) {
	orgSlug, dsSlug := createOrgAndPostgresDataSource(t, stack, "E2E Org Postgres DB Upsert Rerun")

	refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer refreshCancel()

	refreshDataSourceSchemaNow(t, stack, refreshCtx, orgSlug, dsSlug)

	setupCtx, setupCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer setupCancel()

	connectorPool, err := pgxpool.New(setupCtx, connectorDBConnString())
	require.NoError(t, err)
	defer connectorPool.Close()

	_, err = connectorPool.Exec(setupCtx, `
		CREATE TABLE IF NOT EXISTS e2e_sink_users_upsert (
			org_slug text NOT NULL,
			id bigint NOT NULL,
			email text NOT NULL,
			PRIMARY KEY (org_slug, id)
		)
	`)
	require.NoError(t, err)

	_, err = connectorPool.Exec(
		setupCtx,
		"DELETE FROM e2e_sink_users_upsert WHERE org_slug = $1",
		orgSlug.String(),
	)
	require.NoError(t, err)

	sourceQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Source Query DB Upsert Rerun",
		map[string]any{
			"type": "postgres",
			"sql":  "SELECT id, email FROM users WHERE email IN ('alice@example.com', 'bob@example.com') ORDER BY id",
		},
		30,
		nil,
	)

	sinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Sink Query DB Upsert Rerun",
		map[string]any{
			"type": "postgres",
			"sql": `
				INSERT INTO e2e_sink_users_upsert (org_slug, id, email)
				SELECT {{org_slug}}, id, email
				FROM input
				ON CONFLICT (org_slug, id)
				DO UPDATE SET email = EXCLUDED.email
			`,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	pipelineSlug := fmt.Sprintf("e2e-postgres-db-upsert-rerun-%d", time.Now().UnixNano())
	pipelineYAML := postgresDBSinkE2EPipeline
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PIPELINE_SLUG}", pipelineSlug)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${DATA_SOURCE_SLUG}", dsSlug.String())
	pipelineYAML = strings.ReplaceAll(
		pipelineYAML,
		"${SOURCE_QUERY_SLUG}",
		sourceQuerySlug.String(),
	)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${SINK_QUERY_SLUG}", sinkQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${ORG_SLUG}", orgSlug.String())

	pipelineSpec, err := pipelinevalidate.Load(
		"postgres-db-sink-upsert-rerun-e2e.pipeline.yaml",
		[]byte(pipelineYAML),
	)
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		pipelineruntime.NewSinkRuntime(stack.queriesSvc),
	)
	require.NoError(t, err)

	runCtx, runCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer runCancel()

	firstRun, err := runner.Run(
		runCtx,
		newPipelineRunRequest(
			t,
			stack,
			orgSlug,
			fmt.Sprintf("e2e-db-upsert-rerun-1-%d", time.Now().UnixNano()),
			pipelineSpec,
		),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", firstRun.Metadata.Status)

	secondRun, err := runner.Run(
		runCtx,
		newPipelineRunRequest(
			t,
			stack,
			orgSlug,
			fmt.Sprintf("e2e-db-upsert-rerun-2-%d", time.Now().UnixNano()),
			pipelineSpec,
		),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", secondRun.Metadata.Status)

	rows, err := connectorPool.Query(
		runCtx,
		"SELECT id, email FROM e2e_sink_users_upsert WHERE org_slug = $1 ORDER BY id",
		orgSlug.String(),
	)
	require.NoError(t, err)
	defer rows.Close()

	var actual []map[string]any
	for rows.Next() {
		var id int64
		var email string
		err := rows.Scan(&id, &email)
		require.NoError(t, err)
		actual = append(actual, map[string]any{"id": id, "email": email})
	}
	require.NoError(t, rows.Err())

	require.Equal(t, []map[string]any{
		{"id": int64(1), "email": "alice@example.com"},
		{"id": int64(2), "email": "bob@example.com"},
	}, actual)
}

func testExecutesMySQLPipelineRunWithRealDBSinkSuccessfully(t *testing.T, stack *testStack) {
	orgSlug, dsSlug := createOrgAndMySQLDataSource(t, stack, "E2E Org MySQL DB Sink")
	seedSuffix := time.Now().UnixNano()
	firstEmail := fmt.Sprintf("e2e-mysql-sink-%d-alice@example.com", seedSuffix)
	secondEmail := fmt.Sprintf("e2e-mysql-sink-%d-bob@example.com", seedSuffix)

	refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer refreshCancel()

	refreshDataSourceSchemaNow(t, stack, refreshCtx, orgSlug, dsSlug)

	setupCtx, setupCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer setupCancel()

	connectorDB, err := sql.Open("mysql", connectorMySQLDSN())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, connectorDB.Close())
	})

	require.NoError(t, connectorDB.PingContext(setupCtx))

	firstInsert, err := connectorDB.ExecContext(
		setupCtx,
		"INSERT INTO users (email, full_name, age, is_active, balance, bio) VALUES (?, ?, ?, ?, ?, ?)",
		firstEmail,
		"E2E MySQL Sink Alice",
		28,
		true,
		1250.50,
		"E2E seeded MySQL sink source row",
	)
	require.NoError(t, err)
	firstID, err := firstInsert.LastInsertId()
	require.NoError(t, err)

	secondInsert, err := connectorDB.ExecContext(
		setupCtx,
		"INSERT INTO users (email, full_name, age, is_active, balance, bio) VALUES (?, ?, ?, ?, ?, ?)",
		secondEmail,
		"E2E MySQL Sink Bob",
		35,
		true,
		750.00,
		"E2E seeded MySQL sink source row",
	)
	require.NoError(t, err)
	secondID, err := secondInsert.LastInsertId()
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		_, cleanupErr := connectorDB.ExecContext(
			cleanupCtx,
			"DELETE FROM users WHERE email IN (?, ?)",
			firstEmail,
			secondEmail,
		)
		require.NoError(t, cleanupErr)
	})

	_, err = connectorDB.ExecContext(setupCtx, `
		CREATE TABLE IF NOT EXISTS e2e_sink_users (
			org_slug VARCHAR(255) NOT NULL,
			id BIGINT NOT NULL,
			email VARCHAR(255) NOT NULL,
			PRIMARY KEY (org_slug, id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
	`)
	require.NoError(t, err)

	_, err = connectorDB.ExecContext(
		setupCtx,
		"DELETE FROM e2e_sink_users WHERE org_slug = ?",
		orgSlug.String(),
	)
	require.NoError(t, err)

	sourceQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Source Query MySQL DB Sink",
		map[string]any{
			"type": "mysql",
			"sql": fmt.Sprintf(
				"SELECT id, email FROM users WHERE email IN ('%s', '%s') ORDER BY id",
				firstEmail,
				secondEmail,
			),
		},
		30,
		nil,
	)

	sinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Sink Query MySQL DB Insert",
		map[string]any{
			"type": "mysql",
			"sql": `
				INSERT INTO e2e_sink_users (org_slug, id, email)
				SELECT {{org_slug}}, id, email
				FROM input
			`,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	pipelineSlug := fmt.Sprintf("e2e-mysql-db-sink-%d", time.Now().UnixNano())
	pipelineYAML := mysqlDBSinkE2EPipeline
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PIPELINE_SLUG}", pipelineSlug)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${DATA_SOURCE_SLUG}", dsSlug.String())
	pipelineYAML = strings.ReplaceAll(
		pipelineYAML,
		"${SOURCE_QUERY_SLUG}",
		sourceQuerySlug.String(),
	)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${SINK_QUERY_SLUG}", sinkQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${ORG_SLUG}", orgSlug.String())

	pipelineSpec, err := pipelinevalidate.Load(
		"mysql-db-sink-e2e.pipeline.yaml",
		[]byte(pipelineYAML),
	)
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		pipelineruntime.NewSinkRuntime(stack.queriesSvc),
	)
	require.NoError(t, err)

	runCtx, runCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer runCancel()

	runSlug := fmt.Sprintf("e2e-mysql-db-sink-run-%d", time.Now().UnixNano())
	runResult, err := runner.Run(
		runCtx,
		newPipelineRunRequest(t, stack, orgSlug, runSlug, pipelineSpec),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	rows, err := connectorDB.QueryContext(
		runCtx,
		"SELECT id, email FROM e2e_sink_users WHERE org_slug = ? ORDER BY id",
		orgSlug.String(),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var actual []map[string]any
	for rows.Next() {
		var id int64
		var email string
		err := rows.Scan(&id, &email)
		require.NoError(t, err)
		actual = append(actual, map[string]any{"id": id, "email": email})
	}
	require.NoError(t, rows.Err())

	require.Equal(t, []map[string]any{
		{"id": firstID, "email": firstEmail},
		{"id": secondID, "email": secondEmail},
	}, actual)
}

func testExecutesMongoDBPipelineRunWithRealMongoSinkSuccessfully(
	t *testing.T,
	stack *testStack,
) {
	orgSlug, sourceDSSlug := createOrgAndPostgresDataSource(t, stack, "E2E Org Mongo Sink")
	sinkDSSlug := createMongoDataSource(t, stack, orgSlug)

	refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer refreshCancel()

	refreshDataSourceSchemaNow(t, stack, refreshCtx, orgSlug, sourceDSSlug)

	cleanupQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		sinkDSSlug,
		"E2E Cleanup Query Mongo Sink",
		map[string]any{
			"type":       "mongodb",
			"collection": "e2e_sink_users",
			"operation":  "delete_many",
			"filter": map[string]any{
				"org_slug": "{{org_slug}}",
			},
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cleanupCancel()

	_, err := stack.queriesSvc.Execute(
		cleanupCtx,
		orgSlug,
		sinkDSSlug,
		cleanupQuerySlug,
		model.SystemEventActor(),
		map[string]any{
			"org_slug": orgSlug.String(),
		},
	)
	require.NoError(t, err)

	sourceQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		sourceDSSlug,
		"E2E Source Query Mongo Sink",
		map[string]any{
			"type": "postgres",
			"sql":  "SELECT id, email FROM users WHERE email IN ('alice@example.com', 'bob@example.com') ORDER BY id",
		},
		30,
		nil,
	)

	sinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		sinkDSSlug,
		"E2E Sink Query Mongo Insert Many",
		map[string]any{
			"type":       "mongodb",
			"collection": "e2e_sink_users",
			"operation":  "insert_many",
			"documents":  fmt.Sprintf("{{%s}}", sinkbinding.Input),
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       sinkbinding.Input,
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	verifyQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		sinkDSSlug,
		"E2E Verify Query Mongo Sink",
		map[string]any{
			"type":       "mongodb",
			"collection": "e2e_sink_users",
			"operation":  "find",
			"filter": map[string]any{
				"org_slug": "{{org_slug}}",
			},
			"projection": map[string]any{
				"_id":      0,
				"org_slug": 1,
				"id":       1,
				"email":    1,
			},
			"sort": map[string]any{
				"id": 1,
			},
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	pipelineSlug := fmt.Sprintf("e2e-mongodb-sink-%d", time.Now().UnixNano())
	pipelineYAML := mongoDBSinkE2EPipeline
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PIPELINE_SLUG}", pipelineSlug)
	pipelineYAML = strings.ReplaceAll(
		pipelineYAML,
		"${SOURCE_DATA_SOURCE_SLUG}",
		sourceDSSlug.String(),
	)
	pipelineYAML = strings.ReplaceAll(
		pipelineYAML,
		"${SOURCE_QUERY_SLUG}",
		sourceQuerySlug.String(),
	)
	pipelineYAML = strings.ReplaceAll(
		pipelineYAML,
		"${SINK_DATA_SOURCE_SLUG}",
		sinkDSSlug.String(),
	)
	pipelineYAML = strings.ReplaceAll(
		pipelineYAML,
		"${SINK_QUERY_SLUG}",
		sinkQuerySlug.String(),
	)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${ORG_SLUG}", orgSlug.String())

	pipelineSpec, err := pipelinevalidate.Load(
		"mongodb-sink-e2e.pipeline.yaml",
		[]byte(pipelineYAML),
	)
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		pipelineruntime.NewSinkRuntime(stack.queriesSvc),
	)
	require.NoError(t, err)

	runCtx, runCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer runCancel()

	runResult, err := runner.Run(
		runCtx,
		newPipelineRunRequest(
			t,
			stack,
			orgSlug,
			fmt.Sprintf("e2e-mongodb-sink-run-%d", time.Now().UnixNano()),
			pipelineSpec,
		),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer verifyCancel()

	verifyResult, err := stack.queriesSvc.Execute(
		verifyCtx,
		orgSlug,
		sinkDSSlug,
		verifyQuerySlug,
		model.SystemEventActor(),
		map[string]any{"org_slug": orgSlug.String()},
	)
	require.NoError(t, err)

	mongoResult, ok := verifyResult.MongoDB()
	require.True(t, ok)
	require.NotNil(t, mongoResult)
	require.Len(t, mongoResult.Documents, 2)
	require.Equal(t, orgSlug.String(), mongoResult.Documents[0]["org_slug"])
	require.Equal(t, "alice@example.com", mongoResult.Documents[0]["email"])
	require.Equal(t, "bob@example.com", mongoResult.Documents[1]["email"])
}

func testExecutesHTTPPipelineRunWithRealHTTPSinkSuccessfully(t *testing.T, stack *testStack) {
	requestBodies := make(chan map[string]any, 1)

	sinkServer := httptest.NewServer(
		nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			if r.Method == nethttp.MethodGet && r.URL.Path == "/health" {
				w.WriteHeader(nethttp.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}

			if r.Method != nethttp.MethodPost || r.URL.Path != "/sink" {
				w.WriteHeader(nethttp.StatusNotFound)
				return
			}

			defer func() { _ = r.Body.Close() }()

			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(nethttp.StatusBadRequest)
				return
			}

			requestBodies <- body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(nethttp.StatusAccepted)
			_, _ = w.Write([]byte(`{"accepted":true}`))
		}),
	)
	t.Cleanup(sinkServer.Close)

	orgSlug, sourceDSSlug := createOrgAndPostgresDataSource(t, stack, "E2E Org HTTP Sink")
	sinkDSSlug := createHTTPDataSource(t, stack, orgSlug, sinkServer.URL)

	sourceQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		sourceDSSlug,
		"E2E Source Query HTTP Sink",
		map[string]any{
			"type": "postgres",
			"sql":  "SELECT 1 AS id UNION ALL SELECT 2 AS id ORDER BY id",
		},
		30,
		nil,
	)

	sinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		sinkDSSlug,
		"E2E Sink Query HTTP Sink",
		map[string]any{
			"type":   "http",
			"method": "POST",
			"path":   "/sink",
			"body": map[string]any{
				"source":  "pipeline-e2e",
				"mode":    "{{mode}}",
				"payload": fmt.Sprintf("{{%s}}", sinkbinding.Input),
			},
		},
		30,
		[]*queriessvc.QueryParameter{
			{
				Name:       sinkbinding.Input,
				DataType:   queriessvc.ParameterDataTypeText,
				IsRequired: true,
			},
			{Name: "mode", DataType: queriessvc.ParameterDataTypeText, IsRequired: true},
		},
	)

	pipelineSlug := fmt.Sprintf("e2e-http-sink-%d", time.Now().UnixNano())
	pipelineYAML := httpSinkE2EPipeline
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PIPELINE_SLUG}", pipelineSlug)
	pipelineYAML = strings.ReplaceAll(
		pipelineYAML,
		"${SOURCE_DATA_SOURCE_SLUG}",
		sourceDSSlug.String(),
	)
	pipelineYAML = strings.ReplaceAll(
		pipelineYAML,
		"${SOURCE_QUERY_SLUG}",
		sourceQuerySlug.String(),
	)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${SINK_DATA_SOURCE_SLUG}", sinkDSSlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${SINK_QUERY_SLUG}", sinkQuerySlug.String())

	pipelineSpec, err := pipelinevalidate.Load(
		"http-sink-e2e.pipeline.yaml",
		[]byte(pipelineYAML),
	)
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		pipelineruntime.NewSinkRuntime(stack.queriesSvc),
	)
	require.NoError(t, err)

	runCtx, runCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer runCancel()

	runResult, err := runner.Run(
		runCtx,
		newPipelineRunRequest(
			t,
			stack,
			orgSlug,
			fmt.Sprintf("e2e-http-sink-run-%d", time.Now().UnixNano()),
			pipelineSpec,
		),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	select {
	case received := <-requestBodies:
		require.Equal(t, "pipeline-e2e", received["source"])
		require.Equal(t, "bulk", received["mode"])

		payload, ok := received["payload"].([]any)
		require.True(t, ok)
		require.Len(t, payload, 2)

		first, ok := payload[0].(map[string]any)
		require.True(t, ok)
		require.Contains(t, first, "id")
	case <-runCtx.Done():
		t.Fatalf(
			"expected HTTP sink request but none received before run context completed: %v",
			runCtx.Err(),
		)
	}
}

func testExecutesHTTPPipelineRunWithNativeCSVBodySinkSuccessfully(t *testing.T, stack *testStack) {
	type requestCapture struct {
		body        string
		contentType string
	}

	requestBodies := make(chan requestCapture, 1)

	sinkServer := httptest.NewServer(
		nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			if r.Method == nethttp.MethodGet && r.URL.Path == "/health" {
				w.WriteHeader(nethttp.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}

			if r.Method != nethttp.MethodPost || r.URL.Path != "/sink" {
				w.WriteHeader(nethttp.StatusNotFound)
				return
			}

			defer func() { _ = r.Body.Close() }()

			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(nethttp.StatusBadRequest)
				return
			}

			requestBodies <- requestCapture{
				body:        string(bodyBytes),
				contentType: r.Header.Get("Content-Type"),
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(nethttp.StatusAccepted)
			_, _ = w.Write([]byte(`{"accepted":true}`))
		}),
	)
	t.Cleanup(sinkServer.Close)

	orgSlug, sourceDSSlug := createOrgAndPostgresDataSource(
		t,
		stack,
		"E2E Org HTTP Native CSV Sink",
	)
	sinkDSSlug := createHTTPDataSource(t, stack, orgSlug, sinkServer.URL)

	sourceQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		sourceDSSlug,
		"E2E Source Query HTTP Native CSV Sink",
		map[string]any{
			"type": "postgres",
			"sql":  "SELECT '1' AS id, 'alice@example.com' AS email UNION ALL SELECT '2' AS id, 'bob@example.com' AS email ORDER BY id",
		},
		30,
		nil,
	)

	sinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		sinkDSSlug,
		"E2E Sink Query HTTP Native CSV Sink",
		map[string]any{
			"type":   "http",
			"method": "POST",
			"path":   "/sink",
			"body":   fmt.Sprintf("{{%s}}", sinkbinding.Input),
		},
		30,
		[]*queriessvc.QueryParameter{
			{
				Name:       sinkbinding.Input,
				DataType:   queriessvc.ParameterDataTypeText,
				IsRequired: true,
			},
		},
	)

	pipelineSlug := fmt.Sprintf("e2e-http-native-csv-sink-%d", time.Now().UnixNano())
	pipelineSpecYAML := fmt.Sprintf(
		strings.Join([]string{
			"metadata:",
			"  slug: %s",
			"  name: E2E HTTP Native CSV Sink",
			"steps:",
			"  - kind: query",
			"    slug: fetch-users",
			"    query:",
			"      data_source_ref: %s",
			"      operation_type: sql",
			"      operation_ref: %s",
			"  - kind: transform",
			"    slug: serialize-users",
			"    transform:",
			"      inputs:",
			"        input: fetch-users",
			"      sql: \"SELECT CAST(id AS CHAR) AS id, CAST(email AS CHAR) AS email FROM input ORDER BY id\"",
			"      input_contract:",
			"        fields:",
			"          - name: id",
			"            type: integer",
			"            required: true",
			"          - name: email",
			"            type: string",
			"            required: true",
			"      output_kind: csv",
			"      output_contract:",
			"        fields:",
			"          - name: id",
			"            type: string",
			"            required: true",
			"          - name: email",
			"            type: string",
			"            required: true",
			"  - kind: sink",
			"    slug: deliver-users",
			"    sink:",
			"      idempotency_class: deduplicated_by_ledger",
			"      input: serialize-users",
			"      input_contract:",
			"        name: deliver-users-input",
			"        representation: csv",
			"        fields:",
			"          - name: id",
			"            type: string",
			"            required: true",
			"          - name: email",
			"            type: string",
			"            required: true",
			"      data_source_ref: %s",
			"      operation_type: http",
			"      operation_ref: %s",
		}, "\n")+"\n",
		pipelineSlug,
		sourceDSSlug,
		sourceQuerySlug,
		sinkDSSlug,
		sinkQuerySlug,
	)

	pipelineSpec, err := pipelinevalidate.Load(
		"http-native-csv-sink-e2e.pipeline.yaml",
		[]byte(pipelineSpecYAML),
	)
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		pipelineruntime.NewSinkRuntime(stack.queriesSvc),
	)
	require.NoError(t, err)

	runCtx, runCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer runCancel()

	runResult, err := runner.Run(
		runCtx,
		newPipelineRunRequest(
			t,
			stack,
			orgSlug,
			fmt.Sprintf("e2e-http-native-csv-sink-run-%d", time.Now().UnixNano()),
			pipelineSpec,
		),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	select {
	case received := <-requestBodies:
		require.Equal(t, "text/csv", received.contentType)
		require.Equal(
			t,
			"id,email\n1,alice@example.com\n2,bob@example.com\n",
			received.body,
		)
	case <-runCtx.Done():
		t.Fatalf(
			"expected HTTP native CSV sink request but none received before run context completed: %v",
			runCtx.Err(),
		)
	}
}

func testExecutesPostgresJoinPipelineRunWithTwoRealSinksSuccessfully(
	t *testing.T,
	stack *testStack,
) {
	requestBodies := make(chan map[string]any, 1)

	sinkServer := httptest.NewServer(
		nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			if r.Method == nethttp.MethodGet && r.URL.Path == "/health" {
				w.WriteHeader(nethttp.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}

			if r.Method != nethttp.MethodPost || r.URL.Path != "/fanout" {
				w.WriteHeader(nethttp.StatusNotFound)
				return
			}

			defer func() { _ = r.Body.Close() }()

			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(nethttp.StatusBadRequest)
				return
			}

			requestBodies <- body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(nethttp.StatusAccepted)
			_, _ = w.Write([]byte(`{"accepted":true}`))
		}),
	)
	t.Cleanup(sinkServer.Close)

	orgSlug, pgDSSlug := createOrgAndPostgresDataSource(
		t,
		stack,
		"E2E Org Postgres Join Fan Out Sinks",
	)
	httpDSSlug := createHTTPDataSource(t, stack, orgSlug, sinkServer.URL)

	setupCtx, setupCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer setupCancel()

	connectorPool, err := pgxpool.New(setupCtx, connectorDBConnString())
	require.NoError(t, err)
	defer connectorPool.Close()

	_, err = connectorPool.Exec(setupCtx, `
		CREATE TABLE IF NOT EXISTS e2e_fanout_accounts (
			org_slug text NOT NULL,
			user_id bigint NOT NULL,
			account_name text NOT NULL,
			PRIMARY KEY (org_slug, user_id)
		)
	`)
	require.NoError(t, err)

	_, err = connectorPool.Exec(setupCtx, `
		CREATE TABLE IF NOT EXISTS e2e_fanout_joined_users (
			org_slug text NOT NULL,
			user_id bigint NOT NULL,
			email text NOT NULL,
			account_name text NOT NULL,
			PRIMARY KEY (org_slug, user_id)
		)
	`)
	require.NoError(t, err)

	_, err = connectorPool.Exec(
		setupCtx,
		"DELETE FROM e2e_fanout_accounts WHERE org_slug = $1",
		orgSlug.String(),
	)
	require.NoError(t, err)
	_, err = connectorPool.Exec(
		setupCtx,
		"DELETE FROM e2e_fanout_joined_users WHERE org_slug = $1",
		orgSlug.String(),
	)
	require.NoError(t, err)

	_, err = connectorPool.Exec(
		setupCtx,
		`
			INSERT INTO e2e_fanout_accounts (org_slug, user_id, account_name)
			VALUES ($1, 1, 'Acme Warehouse'), ($1, 2, 'Beta Export')
		`,
		orgSlug.String(),
	)
	require.NoError(t, err)

	refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer refreshCancel()
	refreshDataSourceSchemaNow(t, stack, refreshCtx, orgSlug, pgDSSlug)

	usersQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		pgDSSlug,
		"E2E Source Users Query Join Fan Out",
		map[string]any{
			"type": "postgres",
			"sql":  "SELECT id, email FROM users WHERE email IN ('alice@example.com', 'bob@example.com') ORDER BY id",
		},
		30,
		nil,
	)

	accountsQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		pgDSSlug,
		"E2E Source Accounts Query Join Fan Out",
		map[string]any{
			"type": "postgres",
			"sql": `
				SELECT user_id, account_name
				FROM e2e_fanout_accounts
				WHERE org_slug = {{org_slug}}
				ORDER BY user_id
			`,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	dbSinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		pgDSSlug,
		"E2E DB Sink Query Join Fan Out",
		map[string]any{
			"type": "postgres",
			"sql": `
				INSERT INTO e2e_fanout_joined_users (org_slug, user_id, email, account_name)
				SELECT {{org_slug}}, user_id, email, account_name
				FROM input
			`,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	httpSinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		httpDSSlug,
		"E2E HTTP Sink Query Join Fan Out",
		map[string]any{
			"type":   "http",
			"method": "POST",
			"path":   "/fanout",
			"body": map[string]any{
				"mode":    "{{mode}}",
				"payload": fmt.Sprintf("{{%s}}", sinkbinding.Input),
			},
		},
		30,
		[]*queriessvc.QueryParameter{
			{
				Name:       sinkbinding.Input,
				DataType:   queriessvc.ParameterDataTypeText,
				IsRequired: true,
			},
			{Name: "mode", DataType: queriessvc.ParameterDataTypeText, IsRequired: true},
		},
	)

	pipelineSlug := fmt.Sprintf("e2e-postgres-join-fanout-%d", time.Now().UnixNano())
	pipelineYAML := postgresJoinFanOutSinksE2EPipeline
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PIPELINE_SLUG}", pipelineSlug)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PG_DATA_SOURCE_SLUG}", pgDSSlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${HTTP_DATA_SOURCE_SLUG}", httpDSSlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${USERS_QUERY_SLUG}", usersQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${ACCOUNTS_QUERY_SLUG}", accountsQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${DB_SINK_QUERY_SLUG}", dbSinkQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${HTTP_SINK_QUERY_SLUG}", httpSinkQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${ORG_SLUG}", orgSlug.String())

	pipelineSpec, err := pipelinevalidate.Load(
		"postgres-join-fanout-sinks-e2e.pipeline.yaml",
		[]byte(pipelineYAML),
	)
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		pipelineruntime.NewSinkRuntime(stack.queriesSvc),
	)
	require.NoError(t, err)

	runCtx, runCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer runCancel()

	runResult, err := runner.Run(
		runCtx,
		newPipelineRunRequest(
			t,
			stack,
			orgSlug,
			fmt.Sprintf("e2e-postgres-join-fanout-run-%d", time.Now().UnixNano()),
			pipelineSpec,
		),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Len(t, runResult.Timeline.Snapshots, 5)
	require.Equal(t, must.Slug("persist-joined-users"), runResult.Timeline.Snapshots[3].Step)
	require.Equal(t, must.Slug("deliver-joined-users"), runResult.Timeline.Snapshots[4].Step)

	rows, err := connectorPool.Query(
		runCtx,
		`
			SELECT user_id, email, account_name
			FROM e2e_fanout_joined_users
			WHERE org_slug = $1
			ORDER BY user_id
		`,
		orgSlug.String(),
	)
	require.NoError(t, err)
	defer rows.Close()

	var persistedRows []map[string]any
	for rows.Next() {
		var userID int64
		var email string
		var accountName string
		err := rows.Scan(&userID, &email, &accountName)
		require.NoError(t, err)
		persistedRows = append(persistedRows, map[string]any{
			"user_id":      userID,
			"email":        email,
			"account_name": accountName,
		})
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []map[string]any{
		{"user_id": int64(1), "email": "alice@example.com", "account_name": "Acme Warehouse"},
		{"user_id": int64(2), "email": "bob@example.com", "account_name": "Beta Export"},
	}, persistedRows)

	select {
	case received := <-requestBodies:
		require.Equal(t, "fanout", received["mode"])
		payloadRows, ok := received["payload"].([]any)
		require.True(t, ok)
		require.Len(t, payloadRows, 2)

		first, ok := payloadRows[0].(map[string]any)
		require.True(t, ok)
		require.InDelta(t, 1, first["user_id"], 0)
		require.Equal(t, "alice@example.com", first["email"])
		require.Equal(t, "Acme Warehouse", first["account_name"])

		second, ok := payloadRows[1].(map[string]any)
		require.True(t, ok)
		require.InDelta(t, 2, second["user_id"], 0)
		require.Equal(t, "bob@example.com", second["email"])
		require.Equal(t, "Beta Export", second["account_name"])
	case <-runCtx.Done():
		t.Fatalf(
			"expected HTTP fan-out sink request but none received before run context completed: %v",
			runCtx.Err(),
		)
	}
}

func testExecutesHTTPJSONAndCSVRowNormalizedSQLTransformWithTwoRealSinksSuccessfully(
	t *testing.T,
	stack *testStack,
) {
	requestBodies := make(chan map[string]any, 1)

	httpServer := httptest.NewServer(
		nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			switch {
			case r.Method == nethttp.MethodGet && r.URL.Path == "/health":
				w.WriteHeader(nethttp.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true}`))
			case r.Method == nethttp.MethodGet && r.URL.Path == "/profiles":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(nethttp.StatusOK)
				_, _ = w.Write([]byte(`[
					{"id":1,"status":"active"},
					{"id":2,"status":"paused"}
				]`))
			case r.Method == nethttp.MethodGet && r.URL.Path == "/segments.csv":
				w.Header().Set("Content-Type", "text/csv")
				w.WriteHeader(nethttp.StatusOK)
				_, _ = w.Write([]byte("id,segment\n1,warehouse\n2,export\n"))
			case r.Method == nethttp.MethodPost && r.URL.Path == "/mixed-sink":
				defer func() { _ = r.Body.Close() }()

				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					w.WriteHeader(nethttp.StatusBadRequest)
					return
				}

				requestBodies <- body
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(nethttp.StatusAccepted)
				_, _ = w.Write([]byte(`{"accepted":true}`))
			default:
				w.WriteHeader(nethttp.StatusNotFound)
			}
		}),
	)
	t.Cleanup(httpServer.Close)

	orgSlug, pgDSSlug := createOrgAndPostgresDataSource(
		t,
		stack,
		"E2E Org Mixed Source Join Fan Out Sinks",
	)
	httpDSSlug := createHTTPDataSource(t, stack, orgSlug, httpServer.URL)

	setupCtx, setupCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer setupCancel()

	connectorPool, err := pgxpool.New(setupCtx, connectorDBConnString())
	require.NoError(t, err)
	defer connectorPool.Close()

	_, err = connectorPool.Exec(setupCtx, "DROP TABLE IF EXISTS e2e_mixed_fanout_joined_users")
	require.NoError(t, err)

	_, err = connectorPool.Exec(setupCtx, `
		CREATE TABLE e2e_mixed_fanout_joined_users (
			org_slug text NOT NULL,
			user_id bigint NOT NULL,
			email text NOT NULL,
			status text NOT NULL,
			segment text NOT NULL,
			PRIMARY KEY (org_slug, user_id)
		)
	`)
	require.NoError(t, err)

	_, err = connectorPool.Exec(
		setupCtx,
		"DELETE FROM e2e_mixed_fanout_joined_users WHERE org_slug = $1",
		orgSlug.String(),
	)
	require.NoError(t, err)

	refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer refreshCancel()
	refreshDataSourceSchemaNow(t, stack, refreshCtx, orgSlug, pgDSSlug)

	usersQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		pgDSSlug,
		"E2E Rows Source Query Mixed Join Fan Out",
		map[string]any{
			"type": "postgres",
			"sql":  "SELECT id, email FROM users WHERE email IN ('alice@example.com', 'bob@example.com') ORDER BY id",
		},
		30,
		nil,
	)

	profilesQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		httpDSSlug,
		"E2E JSON Source Query Mixed Join Fan Out",
		map[string]any{
			"type":   "http",
			"method": "GET",
			"path":   "/profiles",
		},
		30,
		nil,
	)

	segmentsQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		httpDSSlug,
		"E2E CSV Source Query Mixed Join Fan Out",
		map[string]any{
			"type":   "http",
			"method": "GET",
			"path":   "/segments.csv",
		},
		30,
		nil,
	)

	dbSinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		pgDSSlug,
		"E2E DB Sink Query Mixed Join Fan Out",
		map[string]any{
			"type": "postgres",
			"sql": `
				INSERT INTO e2e_mixed_fanout_joined_users (org_slug, user_id, email, status, segment)
				SELECT {{org_slug}}, user_id, email, status, segment
				FROM input
			`,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	httpSinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		httpDSSlug,
		"E2E HTTP Sink Query Mixed Join Fan Out",
		map[string]any{
			"type":   "http",
			"method": "POST",
			"path":   "/mixed-sink",
			"body": map[string]any{
				"mode":    "{{mode}}",
				"payload": fmt.Sprintf("{{%s}}", sinkbinding.Input),
			},
		},
		30,
		[]*queriessvc.QueryParameter{
			{
				Name:       sinkbinding.Input,
				DataType:   queriessvc.ParameterDataTypeText,
				IsRequired: true,
			},
			{Name: "mode", DataType: queriessvc.ParameterDataTypeText, IsRequired: true},
		},
	)

	pipelineSlug := fmt.Sprintf("e2e-mixed-source-join-fanout-%d", time.Now().UnixNano())
	pipelineYAML := mixedSourceJoinFanOutSinksE2EPipeline
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PIPELINE_SLUG}", pipelineSlug)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PG_DATA_SOURCE_SLUG}", pgDSSlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${HTTP_DATA_SOURCE_SLUG}", httpDSSlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${USERS_QUERY_SLUG}", usersQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PROFILES_QUERY_SLUG}", profilesQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${SEGMENTS_QUERY_SLUG}", segmentsQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${DB_SINK_QUERY_SLUG}", dbSinkQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${HTTP_SINK_QUERY_SLUG}", httpSinkQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${ORG_SLUG}", orgSlug.String())

	pipelineSpec, err := pipelinevalidate.Load(
		"mixed-source-join-fanout-sinks-e2e.pipeline.yaml",
		[]byte(pipelineYAML),
	)
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		pipelineruntime.NewSinkRuntime(stack.queriesSvc),
	)
	require.NoError(t, err)

	runCtx, runCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer runCancel()

	runResult, err := runner.Run(
		runCtx,
		newPipelineRunRequest(
			t,
			stack,
			orgSlug,
			fmt.Sprintf("e2e-mixed-source-join-fanout-run-%d", time.Now().UnixNano()),
			pipelineSpec,
		),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Len(t, runResult.Timeline.Snapshots, 6)
	require.Equal(t, must.Slug("persist-mixed-join"), runResult.Timeline.Snapshots[4].Step)
	require.Equal(t, must.Slug("deliver-mixed-join"), runResult.Timeline.Snapshots[5].Step)

	rows, err := connectorPool.Query(
		runCtx,
		`
			SELECT user_id, email, status, segment
			FROM e2e_mixed_fanout_joined_users
			WHERE org_slug = $1
			ORDER BY user_id
		`,
		orgSlug.String(),
	)
	require.NoError(t, err)
	defer rows.Close()

	var persistedRows []map[string]any
	for rows.Next() {
		var userID int64
		var email string
		var status string
		var segment string
		err := rows.Scan(&userID, &email, &status, &segment)
		require.NoError(t, err)
		persistedRows = append(persistedRows, map[string]any{
			"user_id": userID,
			"email":   email,
			"status":  status,
			"segment": segment,
		})
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []map[string]any{
		{"user_id": int64(1), "email": "alice@example.com", "status": "active", "segment": "warehouse"},
		{"user_id": int64(2), "email": "bob@example.com", "status": "paused", "segment": "export"},
	}, persistedRows)

	select {
	case received := <-requestBodies:
		require.Equal(t, "mixed-fanout", received["mode"])
		payloadRows, ok := received["payload"].([]any)
		require.True(t, ok)
		require.Len(t, payloadRows, 2)

		first, ok := payloadRows[0].(map[string]any)
		require.True(t, ok)
		require.InDelta(t, 1, first["user_id"], 0)
		require.Equal(t, "alice@example.com", first["email"])
		require.Equal(t, "active", first["status"])
		require.Equal(t, "warehouse", first["segment"])

		second, ok := payloadRows[1].(map[string]any)
		require.True(t, ok)
		require.InDelta(t, 2, second["user_id"], 0)
		require.Equal(t, "bob@example.com", second["email"])
		require.Equal(t, "paused", second["status"])
		require.Equal(t, "export", second["segment"])
	case <-runCtx.Done():
		t.Fatalf(
			"expected HTTP mixed fan-out sink request but none received before run context completed: %v",
			runCtx.Err(),
		)
	}
}

func testExecutesFullPipelineRunWithStepInputAndSinkSuccessfully(t *testing.T, stack *testStack) {
	orgSlug, dsSlug := createOrgAndPostgresDataSource(t, stack, "E2E Org Full Pipeline Step Input")
	querySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Query Pipeline Run Step Input",
		map[string]any{
			"type": "postgres",
			"sql":  "SELECT 1 AS id UNION ALL SELECT 5 UNION ALL SELECT 3 UNION ALL SELECT 2 UNION ALL SELECT 4",
		},
		30,
		nil,
	)

	pipelineSlug := slug.Slug(orgSlug.String())
	pipelineRunSlug := fmt.Sprintf("e2e-pipeline-run-step-input-%d", time.Now().UnixNano())
	pipelineSpecYAML := fmt.Sprintf(`metadata:
  slug: %s
  name: E2E Full Pipeline Step Input
steps:
  - kind: query
    slug: fetch-e2e-data
    query:
      data_source_ref: %s
      operation_type: sql
      operation_ref: %s
      output_contract:
        fields:
          - name: id
            type: integer
            required: true
  - kind: transform
    slug: transform-e2e-data
    transform:
      inputs:
        input: fetch-e2e-data
      sql: SELECT id AS id FROM input WHERE id > 1 ORDER BY id DESC
      input_contract:
        fields:
          - name: id
            type: integer
            required: true
      output_kind: rows
      output_contract:
        fields:
          - name: id
            type: integer
            required: true
  - kind: sink
    slug: %s
    sink:
      idempotency_class: deduplicated_by_ledger
      input: transform-e2e-data
      input_contract:
        fields:
          - name: id
            type: integer
            required: true
      data_source_ref: %s
      operation_type: sql
      operation_ref: %s
`, pipelineSlug, dsSlug, querySlug, pipelineSlug, dsSlug, querySlug)

	pipelineSpec, err := pipelinevalidate.Load(
		"e2e-full-pipeline-step-input.yaml",
		[]byte(pipelineSpecYAML),
	)
	require.NoError(t, err)
	require.Equal(t, pipelineSlug, pipelineSpec.Metadata.Slug)
	require.Equal(t, "E2E Full Pipeline Step Input", pipelineSpec.Metadata.Name)
	require.Len(t, pipelineSpec.Steps, 3)
	require.NotNil(t, pipelineSpec.Steps[0].Query)
	require.NotNil(t, pipelineSpec.Steps[1].Transform)
	require.NotNil(t, pipelineSpec.Steps[2].Sink)
	require.Equal(
		t,
		must.Slug("fetch-e2e-data"),
		pipelineSpec.Steps[1].Transform.Inputs["input"].FromStep,
	)
	require.NotNil(t, pipelineSpec.Steps[2].Sink.Input)
	require.Equal(t, must.Slug("transform-e2e-data"), pipelineSpec.Steps[2].Sink.Input.FromStep)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		noopSinkRuntime{},
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	runResult, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, pipelineRunSlug, pipelineSpec),
	)
	require.NoError(t, err)
	require.Equal(t, pipelineRunSlug, runResult.Metadata.RunSlug)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Equal(t, 3, runResult.Metadata.StepCount)

	queryStepResult, ok := runResult.StepResults[must.Slug("fetch-e2e-data")]
	require.True(t, ok)
	sqlResult, ok := queryStepResult.SQL()
	require.True(t, ok)
	require.Equal(t, 5, sqlResult.RowCount)
	require.Equal(t, []string{"id"}, sqlResult.ColumnNames)
	require.Equal(
		t,
		[]map[string]any{
			{"id": int64(1)},
			{"id": int64(5)},
			{"id": int64(3)},
			{"id": int64(2)},
			{"id": int64(4)},
		},
		sqlResult.Rows,
	)

	transformStepResult, ok := runResult.StepResults[must.Slug("transform-e2e-data")]
	require.True(t, ok)
	transformOutput, ok := transformStepResult.Transform()
	require.True(t, ok)
	require.Equal(t, 4, transformOutput.RowCount)
	require.Equal(t, []string{"id"}, transformOutput.ColumnNames)
	require.Equal(
		t,
		[]map[string]any{{"id": int64(5)}, {"id": int64(4)}, {"id": int64(3)}, {"id": int64(2)}},
		transformOutput.Rows,
	)

	runRepo := pipelineruns.New()
	persistedRun, err := runRepo.FindRunBySlug(
		ctx,
		stack.pool,
		orgSlug,
		defaultProjectSlug,
		must.Slug("prod"),
		pipelineRunSlug,
	)
	require.NoError(t, err)
	require.Equal(t, pipelineRunSlug, persistedRun.RunSlug)
	require.Equal(t, pipelineSlug.String(), persistedRun.PipelineSlug)
	require.Equal(t, "succeeded", persistedRun.Status)
	require.Equal(t, int32(3), persistedRun.StepCount)
}

func testExecutesMySQLPipelineRunSuccessfully(t *testing.T, stack *testStack) {
	orgSlug, dsSlug := createOrgAndMySQLDataSource(t, stack, "E2E Org MySQL Pipeline")
	querySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Query MySQL Pipeline",
		map[string]any{
			"type": "mysql",
			"sql":  "SELECT id, email, full_name FROM users WHERE id > {{min_id}} AND email IN ('alice.mysql@example.com', 'bob.mysql@example.com', 'charlie.mysql@example.com') ORDER BY id LIMIT 10",
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "min_id",
			DataType:   queriessvc.ParameterDataTypeInteger,
			IsRequired: false,
		}},
	)

	pipelineSlug := slug.Slug(orgSlug.String())
	pipelineRunSlug := fmt.Sprintf("e2e-mysql-pipeline-run-%d", time.Now().UnixNano())
	pipelineSpecYAML := fmt.Sprintf(`metadata:
  slug: %s
  name: E2E MySQL Pipeline
steps:
  - kind: query
    slug: fetch-mysql-data
    query:
      data_source_ref: %s
      operation_type: sql
      operation_ref: %s
      parameters:
        min_id: 0
  - kind: transform
    slug: transform-mysql-data
    transform:
      inputs:
        input: fetch-mysql-data
      sql: SELECT id, email FROM input ORDER BY id
      input_contract:
        fields:
          - name: id
            type: integer
            required: true
          - name: email
            type: string
            required: true
      output_kind: rows
`, pipelineSlug, dsSlug, querySlug)

	pipelineSpec, err := pipelinevalidate.Load("e2e-mysql-pipeline.yaml", []byte(pipelineSpecYAML))
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		noopSinkRuntime{},
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	runResult, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, pipelineRunSlug, pipelineSpec),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	transformStepResult, ok := runResult.StepResults[must.Slug("transform-mysql-data")]
	require.True(t, ok)
	transformOutput, ok := transformStepResult.Transform()
	require.True(t, ok)
	require.Equal(t, model.TransformOutputKindRows, transformOutput.OutputKind)
	require.Equal(t, []string{"id", "email"}, transformOutput.ColumnNames)
	require.Equal(t, 3, transformOutput.RowCount)
}

func testExecutesMongoDocumentSourceRowNormalizedSQLTransformOutputJSONSuccessfully(
	t *testing.T,
	stack *testStack,
) {
	orgSlug, dsSlug := createOrgAndMongoDataSource(t, stack, "E2E Org Mongo Pipeline")
	querySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Query Mongo Pipeline",
		map[string]any{
			"type":       "mongodb",
			"collection": "orders",
			"operation":  "find",
			"filter": map[string]any{
				"status": "{{status}}",
			},
			"limit": 5,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:         "status",
			DataType:     queriessvc.ParameterDataTypeText,
			IsRequired:   false,
			DefaultValue: ptrString("pending"),
		}},
	)

	pipelineSlug := slug.Slug(orgSlug.String())
	pipelineRunSlug := fmt.Sprintf("e2e-mongo-pipeline-run-%d", time.Now().UnixNano())
	pipelineSpecYAML := fmt.Sprintf(`metadata:
  slug: %s
  name: E2E Mongo Pipeline
steps:
  - kind: query
    slug: fetch-mongo-data
    query:
      data_source_ref: %s
      operation_type: sql
      operation_ref: %s
      parameters:
        status: pending
  - kind: transform
    slug: transform-mongo-data
    transform:
      inputs:
        input: fetch-mongo-data
      sql: SELECT orderNumber, status FROM input
      input_contract:
        fields:
          - name: orderNumber
            type: integer
            required: true
          - name: status
            type: string
            required: true
      output_kind: json
`, pipelineSlug, dsSlug, querySlug)

	pipelineSpec, err := pipelinevalidate.Load("e2e-mongo-pipeline.yaml", []byte(pipelineSpecYAML))
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		noopSinkRuntime{},
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	runResult, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, pipelineRunSlug, pipelineSpec),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	transformStepResult, ok := runResult.StepResults[must.Slug("transform-mongo-data")]
	require.True(t, ok)
	transformOutput, ok := transformStepResult.Transform()
	require.True(t, ok)
	require.Equal(t, model.TransformOutputKindJSON, transformOutput.OutputKind)
	require.Equal(t, 1, transformOutput.RowCount)
	require.NotEmpty(t, transformOutput.Document)
}

func ptrString(v string) *string {
	return &v
}

func testRejectsFullPipelineRunWhenTransformContractMismatchesCastOutput(
	t *testing.T,
	stack *testStack,
) {
	orgSlug, dsSlug := createOrgAndPostgresDataSource(
		t,
		stack,
		"E2E Org Full Pipeline Contract Mismatch",
	)
	querySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Query Pipeline Contract Mismatch",
		map[string]any{
			"type": "postgres",
			"sql":  "SELECT 1 AS id UNION ALL SELECT 2",
		},
		30,
		nil,
	)

	pipelineSlug := slug.Slug(orgSlug.String())
	pipelineRunSlug := fmt.Sprintf("e2e-pipeline-run-contract-mismatch-%d", time.Now().UnixNano())
	pipelineSpecYAML := fmt.Sprintf(`metadata:
  slug: %s
  name: E2E Full Pipeline Contract Mismatch
steps:
  - kind: query
    slug: fetch-e2e-data
    query:
      data_source_ref: %s
      operation_type: sql
      operation_ref: %s
  - kind: transform
    slug: transform-e2e-data
    transform:
      inputs:
        input: fetch-e2e-data
      sql: SELECT CAST(id AS CHAR) AS id FROM input
      input_contract:
        fields:
          - name: id
            type: integer
            required: true
      output_kind: rows
      output_contract:
        fields:
          - name: id
            type: integer
            required: true
`, pipelineSlug, dsSlug, querySlug)

	pipelineSpec, err := pipelinevalidate.Load(
		"e2e-full-pipeline-contract-mismatch.yaml",
		[]byte(pipelineSpecYAML),
	)
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		noopSinkRuntime{},
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err = runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, pipelineRunSlug, pipelineSpec),
	)
	require.ErrorIs(t, err, transformsql.ErrContractType)
	require.ErrorContains(t, err, "pre-validate transforms")
	require.ErrorContains(t, err, `output contract field "id" expects integer but projection resolves to string`)

	runRepo := pipelineruns.New()
	recorded, err := runRepo.FindRunBySlug(
		ctx,
		stack.pool,
		orgSlug,
		defaultProjectSlug,
		must.Slug("prod"),
		pipelineRunSlug,
	)
	require.NoError(t, err)
	require.Equal(t, "failed", recorded.Status)
	require.Contains(t, string(recorded.Timeline), `"execution_context"`)
}

func testExecutesHTTPSourcePipelineRunWithRealDBSinkSuccessfully(t *testing.T, stack *testStack) {
	orgSlug, pgDsSlug := createOrgAndPostgresDataSource(t, stack, "E2E Org HTTP Source DB Sink")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	httpDsConfig, err := json.Marshal(map[string]any{
		"base_url":            httpConnectorBaseURL(),
		"health_path":         "/health",
		"timeout_seconds":     5,
		"allow_private_hosts": true,
	})
	require.NoError(t, err)

	httpDsSlug := must.Slug(fmt.Sprintf("e2e-http-ds-%d", time.Now().UnixNano()))
	_, err = stack.datasourcesSvc.Create(ctx, orgSlug, model.SystemEventActor(), &datasourcessvc.CreateParams{
		Name:        "E2E HTTP DS",
		Slug:        httpDsSlug,
		Type:        model.DataSourceTypeHTTP,
		ConfigJSON:  httpDsConfig,
		Credentials: map[string]any{},
	})
	require.NoError(t, err)

	// Use a unique status to isolate this test's items from concurrent test runs.
	uniqueStatus := fmt.Sprintf("e2e-%d", time.Now().UnixNano())
	httpClient := &nethttp.Client{Timeout: 5 * time.Second}

	seedItem := func(name string) {
		t.Helper()
		body := fmt.Sprintf(`{"name":%q,"status":%q}`, name, uniqueStatus)
		req, err := nethttp.NewRequestWithContext(
			ctx, nethttp.MethodPost, httpConnectorBaseURL()+"/items", strings.NewReader(body),
		)
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, nethttp.StatusCreated, resp.StatusCode)
	}

	seedItem("alice-item")
	seedItem("bob-item")

	httpQuerySlug := createQuery(
		t, stack, orgSlug, httpDsSlug,
		"E2E HTTP Source Query",
		map[string]any{
			"method":       "GET",
			"path":         "/items",
			"query_params": map[string]any{"status": uniqueStatus},
			"type":         "http",
		},
		30, nil,
	)

	connectorPool, err := pgxpool.New(ctx, connectorDBConnString())
	require.NoError(t, err)
	defer connectorPool.Close()

	_, err = connectorPool.Exec(ctx, "DROP TABLE IF EXISTS e2e_sink_http_items")
	require.NoError(t, err)

	_, err = connectorPool.Exec(ctx, `
		CREATE TABLE e2e_sink_http_items (
			org_slug text NOT NULL,
			id bigint NOT NULL,
			name text NOT NULL,
			PRIMARY KEY (org_slug, id)
		)
	`)
	require.NoError(t, err)

	sinkQuerySlug := createQuery(
		t, stack, orgSlug, pgDsSlug,
		"E2E HTTP Sink Query",
		map[string]any{
			"type": "postgres",
			"sql": `
				INSERT INTO e2e_sink_http_items (org_slug, id, name)
				SELECT {{org_slug}}, id, name
				FROM input
				ON CONFLICT (org_slug, id)
				DO UPDATE SET name = EXCLUDED.name
			`,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	pipelineSlug := fmt.Sprintf("e2e-http-source-db-sink-%d", time.Now().UnixNano())
	pipelineYAML := httpSourceDBSinkE2EPipeline
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PIPELINE_SLUG}", pipelineSlug)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${HTTP_DATA_SOURCE_SLUG}", httpDsSlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${HTTP_QUERY_SLUG}", httpQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PG_DATA_SOURCE_SLUG}", pgDsSlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${SINK_QUERY_SLUG}", sinkQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${ORG_SLUG}", orgSlug.String())

	pipelineSpec, err := pipelinevalidate.Load(
		"http-source-db-sink-e2e.pipeline.yaml",
		[]byte(pipelineYAML),
	)
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		pipelineruntime.NewSinkRuntime(stack.queriesSvc),
	)
	require.NoError(t, err)

	runCtx, runCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer runCancel()

	runSlug := fmt.Sprintf("e2e-http-source-db-sink-run-%d", time.Now().UnixNano())
	runResult, err := runner.Run(
		runCtx,
		newPipelineRunRequest(t, stack, orgSlug, runSlug, pipelineSpec),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	rows, err := connectorPool.Query(
		runCtx,
		"SELECT name FROM e2e_sink_http_items WHERE org_slug = $1 ORDER BY name",
		orgSlug.String(),
	)
	require.NoError(t, err)
	defer rows.Close()

	var actual []string
	for rows.Next() {
		var name string
		err := rows.Scan(&name)
		require.NoError(t, err)
		actual = append(actual, name)
	}
	require.NoError(t, rows.Err())

	require.Equal(t, []string{"alice-item", "bob-item"}, actual)
}

func testExecutesDecimalPriceFilePipelineWithRealDBSinkSuccessfully(t *testing.T, stack *testStack) {
	priceServer := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		switch {
		case r.Method == nethttp.MethodGet && r.URL.Path == "/health":
			w.WriteHeader(nethttp.StatusOK)
		case r.Method == nethttp.MethodGet && r.URL.Path == "/prices.csv":
			w.Header().Set("Content-Type", "text/csv")
			_, _ = w.Write([]byte("sku,price,adjustment\naddition,0.10,0.20\nrounding,2.675,0\nrefund,-2.675,0\nlarge,9007199254740993.10,0.20\n"))
		default:
			w.WriteHeader(nethttp.StatusNotFound)
		}
	}))
	t.Cleanup(priceServer.Close)

	orgSlug, pgDSSlug := createOrgAndPostgresDataSource(t, stack, "E2E Org Decimal Price File DB Sink")
	httpDSSlug := createHTTPDataSource(t, stack, orgSlug, priceServer.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	connectorPool, err := pgxpool.New(ctx, connectorDBConnString())
	require.NoError(t, err)
	defer connectorPool.Close()

	_, err = connectorPool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS e2e_decimal_price_file_sink (
			org_slug text NOT NULL,
			sku text NOT NULL,
			total numeric(38,2) NOT NULL,
			PRIMARY KEY (org_slug, sku)
		)
	`)
	require.NoError(t, err)
	_, err = connectorPool.Exec(ctx, "DELETE FROM e2e_decimal_price_file_sink WHERE org_slug = $1", orgSlug.String())
	require.NoError(t, err)

	httpQuerySlug := createQuery(t, stack, orgSlug, httpDSSlug, "E2E Decimal Price CSV Source Query", map[string]any{
		"type": "http", "method": "GET", "path": "/prices.csv",
	}, 30, nil)
	sinkQuerySlug := createQuery(t, stack, orgSlug, pgDSSlug, "E2E Decimal Price PostgreSQL Sink Query", map[string]any{
		"type": "postgres",
		"sql": `
			INSERT INTO e2e_decimal_price_file_sink (org_slug, sku, total)
			SELECT {{org_slug}}, sku, total
			FROM input
			ON CONFLICT (org_slug, sku) DO UPDATE SET total = EXCLUDED.total
		`,
	}, 30, []*queriessvc.QueryParameter{{Name: "org_slug", DataType: queriessvc.ParameterDataTypeText, IsRequired: true}})

	pipelineSlug := fmt.Sprintf("e2e-decimal-price-file-db-sink-%d", time.Now().UnixNano())
	pipelineYAML := decimalPriceFileDBSinkE2EPipeline
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PIPELINE_SLUG}", pipelineSlug)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${HTTP_DATA_SOURCE_SLUG}", httpDSSlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${HTTP_QUERY_SLUG}", httpQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PG_DATA_SOURCE_SLUG}", pgDSSlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${SINK_QUERY_SLUG}", sinkQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${ORG_SLUG}", orgSlug.String())
	pipelineSpec, err := pipelinevalidate.Load("decimal-price-file-db-sink-e2e.pipeline.yaml", []byte(pipelineYAML))
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		pipelineruntime.NewSinkRuntime(stack.queriesSvc),
	)
	require.NoError(t, err)

	runResult, err := runner.Run(ctx, newPipelineRunRequest(t, stack, orgSlug, fmt.Sprintf("e2e-decimal-price-file-run-%d", time.Now().UnixNano()), pipelineSpec))
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	rows, err := connectorPool.Query(ctx, `
		SELECT sku, total::text
		FROM e2e_decimal_price_file_sink
		WHERE org_slug = $1
		ORDER BY sku
	`, orgSlug.String())
	require.NoError(t, err)
	defer rows.Close()

	actual := map[string]string{}
	for rows.Next() {
		var sku, total string
		require.NoError(t, rows.Scan(&sku, &total))
		actual[sku] = total
	}
	require.NoError(t, rows.Err())
	require.Equal(t, map[string]string{
		"addition": "0.30",
		"large":    "9007199254740993.30",
		"refund":   "-2.68",
		"rounding": "2.68",
	}, actual)
}

func testExecutesHubSpotSourceCountTransformWithRealDBSinkSuccessfully(t *testing.T, stack *testStack) {
	orgSlug, pgDsSlug := createOrgAndPostgresDataSource(t, stack, "E2E Org HubSpot Source DB Sink")
	hubspotDsSlug := createHubSpotDataSource(t, stack, orgSlug)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	httpClient := &nethttp.Client{Timeout: 5 * time.Second}
	resetHubSpotMock(t, ctx, httpClient)
	seedHubSpotMockContacts(
		t,
		ctx,
		httpClient,
		[]map[string]any{
			{
				"id": "201",
				"properties": map[string]any{
					"email":     "alice@example.com",
					"firstname": "Alice",
					"lastname":  "Able",
				},
			},
			{
				"id": "202",
				"properties": map[string]any{
					"email":     "bob@example.com",
					"firstname": "Bob",
					"lastname":  "Baker",
				},
			},
		},
	)

	hubspotQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		hubspotDsSlug,
		"E2E HubSpot Source Query",
		map[string]any{
			"type":        "hubspot",
			"object_type": "contacts",
			"operation":   "list",
			"properties":  []string{"email", "firstname", "lastname"},
			"limit":       100,
		},
		30,
		nil,
	)

	connectorPool, err := pgxpool.New(ctx, connectorDBConnString())
	require.NoError(t, err)
	defer connectorPool.Close()

	_, err = connectorPool.Exec(ctx, "DROP TABLE IF EXISTS e2e_sink_hubspot_contact_counts")
	require.NoError(t, err)
	_, err = connectorPool.Exec(ctx, "DROP TABLE IF EXISTS e2e_sink_hubspot_contacts")
	require.NoError(t, err)

	_, err = connectorPool.Exec(ctx, `
		CREATE TABLE e2e_sink_hubspot_contact_counts (
			org_slug text PRIMARY KEY,
			contact_count bigint NOT NULL,
			cast_contact_count bigint NOT NULL
		)
	`)
	require.NoError(t, err)
	_, err = connectorPool.Exec(ctx, `
		CREATE TABLE e2e_sink_hubspot_contacts (
			org_slug text NOT NULL,
			hubspot_id text NOT NULL,
			email text NOT NULL,
			display_name text NOT NULL,
			PRIMARY KEY (org_slug, hubspot_id)
		)
	`)
	require.NoError(t, err)

	contactsSinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		pgDsSlug,
		"E2E HubSpot Contacts Sink Query",
		map[string]any{
			"type": "postgres",
			"sql": `
				INSERT INTO e2e_sink_hubspot_contacts (org_slug, hubspot_id, email, display_name)
				SELECT {{org_slug}}, hs_object_id, email, display_name
				FROM input
				ON CONFLICT (org_slug, hubspot_id)
				DO UPDATE SET email = EXCLUDED.email, display_name = EXCLUDED.display_name
			`,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	countsSinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		pgDsSlug,
		"E2E HubSpot Count Sink Query",
		map[string]any{
			"type": "postgres",
			"sql": `
				INSERT INTO e2e_sink_hubspot_contact_counts (org_slug, contact_count, cast_contact_count)
				SELECT {{org_slug}}, contact_count, cast_contact_count
				FROM input
				ON CONFLICT (org_slug)
				DO UPDATE SET
					contact_count = EXCLUDED.contact_count,
					cast_contact_count = EXCLUDED.cast_contact_count
			`,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	pipelineSlug := fmt.Sprintf("e2e-hubspot-source-count-db-sink-%d", time.Now().UnixNano())
	pipelineYAML := hubspotSourceCountDBSinkE2EPipeline
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PIPELINE_SLUG}", pipelineSlug)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${HUBSPOT_DATA_SOURCE_SLUG}", hubspotDsSlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${HUBSPOT_QUERY_SLUG}", hubspotQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PG_DATA_SOURCE_SLUG}", pgDsSlug.String())
	pipelineYAML = strings.ReplaceAll(
		pipelineYAML,
		"${CONTACTS_SINK_QUERY_SLUG}",
		contactsSinkQuerySlug.String(),
	)
	pipelineYAML = strings.ReplaceAll(
		pipelineYAML,
		"${COUNTS_SINK_QUERY_SLUG}",
		countsSinkQuerySlug.String(),
	)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${ORG_SLUG}", orgSlug.String())

	pipelineSpec, err := pipelinevalidate.Load(
		"hubspot-source-count-db-sink-e2e.pipeline.yaml",
		[]byte(pipelineYAML),
	)
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		pipelineruntime.NewSinkRuntime(stack.queriesSvc),
	)
	require.NoError(t, err)

	runCtx, runCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer runCancel()

	runSlug := fmt.Sprintf("e2e-hubspot-source-count-db-sink-run-%d", time.Now().UnixNano())
	runResult, err := runner.Run(
		runCtx,
		newPipelineRunRequest(t, stack, orgSlug, runSlug, pipelineSpec),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	queryStepResult, ok := runResult.StepResults[must.Slug("fetch-contacts")]
	require.True(t, ok)
	hubspotResult, ok := queryStepResult.ObjectRows()
	require.True(t, ok)
	require.NotNil(t, hubspotResult)
	require.Equal(t, "contacts", hubspotResult.ObjectType)
	require.Equal(t, 2, hubspotResult.RowCount)
	require.Equal(
		t,
		[]string{"hs_object_id", "email", "firstname", "lastname"},
		hubspotResult.ColumnNames,
	)

	rows, err := connectorPool.Query(
		runCtx,
		`SELECT email, display_name FROM e2e_sink_hubspot_contacts WHERE org_slug = $1 ORDER BY email`,
		orgSlug.String(),
	)
	require.NoError(t, err)
	defer rows.Close()

	var actualContacts []string
	for rows.Next() {
		var email string
		var displayName string
		err := rows.Scan(&email, &displayName)
		require.NoError(t, err)
		actualContacts = append(actualContacts, email+":"+displayName)
	}
	require.NoError(t, rows.Err())
	require.Equal(
		t,
		[]string{
			"alice@example.com:Alice",
			"bob@example.com:Bob",
		},
		actualContacts,
	)

	var contactCount int64
	var castContactCount int64
	err = connectorPool.QueryRow(
		runCtx,
		`SELECT contact_count, cast_contact_count
		 FROM e2e_sink_hubspot_contact_counts
		 WHERE org_slug = $1`,
		orgSlug.String(),
	).Scan(&contactCount, &castContactCount)
	require.NoError(t, err)
	require.Equal(t, int64(2), contactCount)
	require.Equal(t, int64(2), castContactCount)
}

func testExecutesGitSourceSQLTransformWithRealDBSinkSuccessfully(t *testing.T, stack *testStack) {
	orgSlug, pgDsSlug := createOrgAndPostgresDataSource(t, stack, "E2E Org Git Source DB Sink")
	gitDsSlug := createSeededGitDataSource(t, stack, orgSlug)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	gitQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		gitDsSlug,
		"E2E Git Source Query",
		map[string]any{
			"type":       "git",
			"operation":  "read",
			"path":       "users.csv",
			"format":     "csv",
			"has_header": true,
			"branch":     "main",
		},
		30,
		nil,
	)

	connectorPool, err := pgxpool.New(ctx, connectorDBConnString())
	require.NoError(t, err)
	defer connectorPool.Close()

	_, err = connectorPool.Exec(ctx, "DROP TABLE IF EXISTS e2e_sink_git_users")
	require.NoError(t, err)
	_, err = connectorPool.Exec(ctx, `
		CREATE TABLE e2e_sink_git_users (
			org_slug text NOT NULL,
			account_id bigint NOT NULL,
			email text NOT NULL,
			PRIMARY KEY (org_slug, account_id)
		)
	`)
	require.NoError(t, err)

	sinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		pgDsSlug,
		"E2E Git Users Sink Query",
		map[string]any{
			"type": "postgres",
			"sql": `
				INSERT INTO e2e_sink_git_users (org_slug, account_id, email)
				SELECT {{org_slug}}, account_id, email
				FROM input
				ON CONFLICT (org_slug, account_id)
				DO UPDATE SET email = EXCLUDED.email
			`,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	pipelineSlug := fmt.Sprintf("e2e-git-source-db-sink-%d", time.Now().UnixNano())
	pipelineYAML := gitSourceDBSinkE2EPipeline
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PIPELINE_SLUG}", pipelineSlug)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${GIT_DATA_SOURCE_SLUG}", gitDsSlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${GIT_QUERY_SLUG}", gitQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PG_DATA_SOURCE_SLUG}", pgDsSlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${SINK_QUERY_SLUG}", sinkQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${ORG_SLUG}", orgSlug.String())

	pipelineSpec, err := pipelinevalidate.Load(
		"git-source-db-sink-e2e.pipeline.yaml",
		[]byte(pipelineYAML),
	)
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		pipelineruntime.NewSinkRuntime(stack.queriesSvc),
	)
	require.NoError(t, err)

	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer runCancel()

	runResult, err := runner.Run(
		runCtx,
		newPipelineRunRequest(
			t,
			stack,
			orgSlug,
			fmt.Sprintf("e2e-git-source-db-sink-run-%d", time.Now().UnixNano()),
			pipelineSpec,
		),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	var accountID int64
	var email string
	err = connectorPool.QueryRow(
		runCtx,
		`SELECT account_id, email FROM e2e_sink_git_users WHERE org_slug = $1`,
		orgSlug.String(),
	).Scan(&accountID, &email)
	require.NoError(t, err)
	require.Equal(t, int64(2), accountID)
	require.Equal(t, "bob@example.com", email)
}

func testExecutesNullAggregateCastsWithRealDBSinkSuccessfully(t *testing.T, stack *testStack) {
	orgSlug, pgDsSlug := createOrgAndPostgresDataSource(t, stack, "E2E Org Null Aggregate Casts")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	connectorPool, err := pgxpool.New(ctx, connectorDBConnString())
	require.NoError(t, err)
	defer connectorPool.Close()

	_, err = connectorPool.Exec(ctx, "DROP TABLE IF EXISTS e2e_null_aggregate_source")
	require.NoError(t, err)
	_, err = connectorPool.Exec(ctx, "DROP TABLE IF EXISTS e2e_sink_null_aggregate_casts")
	require.NoError(t, err)
	_, err = connectorPool.Exec(ctx, `
		CREATE TABLE e2e_null_aggregate_source (
			org_slug text NOT NULL,
			category text NOT NULL,
			amount numeric NULL
		)
	`)
	require.NoError(t, err)
	_, err = connectorPool.Exec(ctx, `
		CREATE TABLE e2e_sink_null_aggregate_casts (
			org_slug text NOT NULL,
			category text NOT NULL,
			total_char text NULL,
			total_signed bigint NULL,
			PRIMARY KEY (org_slug, category)
		)
	`)
	require.NoError(t, err)
	_, err = connectorPool.Exec(
		ctx,
		`INSERT INTO e2e_null_aggregate_source (org_slug, category, amount)
		 VALUES ($1, 'all-null', NULL), ($1, 'all-null', NULL)`,
		orgSlug.String(),
	)
	require.NoError(t, err)

	sourceQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		pgDsSlug,
		"E2E Nullable Aggregate Source Query",
		map[string]any{
			"type": "postgres",
			"sql":  "SELECT category, amount FROM e2e_null_aggregate_source WHERE org_slug = {{org_slug}}",
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)
	sinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		pgDsSlug,
		"E2E Nullable Aggregate Sink Query",
		map[string]any{
			"type": "postgres",
			"sql": `
				INSERT INTO e2e_sink_null_aggregate_casts
					(org_slug, category, total_char, total_signed)
				SELECT {{org_slug}}, category, total_char, total_signed::bigint
				FROM input
				ON CONFLICT (org_slug, category)
				DO UPDATE SET
					total_char = EXCLUDED.total_char,
					total_signed = EXCLUDED.total_signed
			`,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	pipelineSlug := fmt.Sprintf("e2e-null-aggregate-casts-db-sink-%d", time.Now().UnixNano())
	pipelineYAML := postgresNullAggregateCastsDBSinkE2EPipeline
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PIPELINE_SLUG}", pipelineSlug)
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${PG_DATA_SOURCE_SLUG}", pgDsSlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${SOURCE_QUERY_SLUG}", sourceQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${SINK_QUERY_SLUG}", sinkQuerySlug.String())
	pipelineYAML = strings.ReplaceAll(pipelineYAML, "${ORG_SLUG}", orgSlug.String())

	pipelineSpec, err := pipelinevalidate.Load(
		"postgres-null-aggregate-casts-db-sink-e2e.pipeline.yaml",
		[]byte(pipelineYAML),
	)
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		pipelineruntime.NewSinkRuntime(stack.queriesSvc),
	)
	require.NoError(t, err)

	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer runCancel()
	runResult, err := runner.Run(
		runCtx,
		newPipelineRunRequest(
			t,
			stack,
			orgSlug,
			fmt.Sprintf("e2e-null-aggregate-casts-run-%d", time.Now().UnixNano()),
			pipelineSpec,
		),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	var category string
	var totalChar sql.NullString
	var totalSigned sql.NullInt64
	err = connectorPool.QueryRow(
		runCtx,
		`SELECT category, total_char, total_signed
		 FROM e2e_sink_null_aggregate_casts
		 WHERE org_slug = $1`,
		orgSlug.String(),
	).Scan(&category, &totalChar, &totalSigned)
	require.NoError(t, err)
	require.Equal(t, "all-null", category)
	require.False(t, totalChar.Valid)
	require.False(t, totalSigned.Valid)
}

// testExecutesPostgresWatermarkIncrementalPipelineWithRealConnector verifies
// the real Postgres connector path for watermark incremental execution. The
// source query uses updated_at as the checkpoint field, reads rows where
// updated_at is greater than runtime.incremental.lower_bound.value, commits the
// max observed updated_at value, and proves the next run only moves the newly
// inserted row to the real DB sink.
func testExecutesPostgresWatermarkIncrementalPipelineWithRealConnector(
	t *testing.T,
	stack *testStack,
) {
	orgSlug, dsSlug := createOrgAndPostgresDataSource(t, stack, "E2E Org Postgres Incremental")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	connectorPool, err := pgxpool.New(ctx, connectorDBConnString())
	require.NoError(t, err)
	defer connectorPool.Close()

	_, err = connectorPool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS e2e_incremental_pg_source (
			org_slug text NOT NULL,
			id bigint NOT NULL,
			email text NOT NULL,
			updated_at timestamptz NOT NULL,
			PRIMARY KEY (org_slug, id)
		)
	`)
	require.NoError(t, err)
	_, err = connectorPool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS e2e_incremental_pg_sink (
			org_slug text NOT NULL,
			id bigint NOT NULL,
			email text NOT NULL,
			updated_at text NOT NULL,
			PRIMARY KEY (org_slug, id)
		)
	`)
	require.NoError(t, err)
	_, err = connectorPool.Exec(ctx, "DELETE FROM e2e_incremental_pg_source WHERE org_slug = $1", orgSlug.String())
	require.NoError(t, err)
	_, err = connectorPool.Exec(ctx, "DELETE FROM e2e_incremental_pg_sink WHERE org_slug = $1", orgSlug.String())
	require.NoError(t, err)
	_, err = connectorPool.Exec(ctx, `
		INSERT INTO e2e_incremental_pg_source (org_slug, id, email, updated_at)
		VALUES
			($1, 1, 'alice@example.com', '2026-05-24T10:00:00Z'::timestamptz),
			($1, 2, 'bob@example.com', '2026-05-24T10:05:00Z'::timestamptz)
	`, orgSlug.String())
	require.NoError(t, err)

	sourceQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Incremental Postgres Source",
		map[string]any{
			"type": "postgres",
			"sql": `
				SELECT
					id,
					email,
					to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS updated_at
				FROM e2e_incremental_pg_source
				WHERE org_slug = {{org_slug}}
					AND (
						{{runtime.incremental.lower_bound.value}}::timestamptz IS NULL
						OR updated_at > {{runtime.incremental.lower_bound.value}}::timestamptz
					)
				ORDER BY updated_at, id
			`,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)
	sinkQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Incremental Postgres Sink",
		map[string]any{
			"type": "postgres",
			"sql": `
				INSERT INTO e2e_incremental_pg_sink (org_slug, id, email, updated_at)
				SELECT {{org_slug}}, id, email, updated_at
				FROM input
				ON CONFLICT (org_slug, id)
				DO UPDATE SET email = EXCLUDED.email, updated_at = EXCLUDED.updated_at
			`,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)

	pipeline := incrementalPostgresWatermarkPipeline(
		must.Slug(fmt.Sprintf("e2e-pg-watermark-%d", time.Now().UnixNano())),
		dsSlug,
		sourceQuerySlug,
		sinkQuerySlug,
		orgSlug,
	)
	runner := newE2EPipelineRunner(t, stack)

	firstRun, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, fmt.Sprintf("e2e-pg-watermark-1-%d", time.Now().UnixNano()), pipeline),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", firstRun.Metadata.Status)
	require.Len(t, firstRun.IncrementalCheckpointTransitions, 1)
	require.Equal(t, pipelinerun.IncrementalCheckpointTransitionCommitted, firstRun.IncrementalCheckpointTransitions[0].Status)
	require.JSONEq(t, `{"value":"2026-05-24T10:05:00Z"}`, string(firstRun.IncrementalCheckpointTransitions[0].ObservedUpperBound))

	_, err = connectorPool.Exec(ctx, `
		INSERT INTO e2e_incremental_pg_source (org_slug, id, email, updated_at)
		VALUES ($1, 3, 'carol@example.com', '2026-05-24T10:10:00Z'::timestamptz)
	`, orgSlug.String())
	require.NoError(t, err)

	secondRun, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, fmt.Sprintf("e2e-pg-watermark-2-%d", time.Now().UnixNano()), pipeline),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", secondRun.Metadata.Status)
	sourceResult := secondRun.StepResults[must.Slug("fetch-users")]
	sqlResult, ok := sourceResult.SQL()
	require.True(t, ok)
	require.Equal(t, 1, sqlResult.RowCount)
	require.Equal(t, "carol@example.com", sqlResult.Rows[0]["email"])

	upperBound, status := currentConnectorE2EIncrementalCheckpoint(
		t,
		stack.pool,
		orgSlug,
		defaultProjectSlug,
		pipeline.Metadata.Slug,
		must.Slug("fetch-users"),
	)
	require.Equal(t, "committed", status)
	require.JSONEq(t, `{"value":"2026-05-24T10:10:00Z"}`, string(upperBound))

	var sinkCount int
	err = connectorPool.QueryRow(
		ctx,
		"SELECT count(*) FROM e2e_incremental_pg_sink WHERE org_slug = $1",
		orgSlug.String(),
	).Scan(&sinkCount)
	require.NoError(t, err)
	require.Equal(t, 3, sinkCount)
}

// testExecutesMySQLWatermarkIncrementalPipelineWithRealConnector verifies the
// real MySQL connector path for watermark incremental execution. The prepared
// statement binds the runtime lower-bound value as SQL args and the committed
// checkpoint advances from the max updated_at value returned by each run.
func testExecutesMySQLWatermarkIncrementalPipelineWithRealConnector(
	t *testing.T,
	stack *testStack,
) {
	orgSlug, dsSlug := createOrgAndMySQLDataSource(t, stack, "E2E Org MySQL Incremental")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	connectorDB, err := sql.Open("mysql", connectorMySQLDSN())
	require.NoError(t, err)
	defer func() { require.NoError(t, connectorDB.Close()) }()

	_, err = connectorDB.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS e2e_incremental_mysql_source (
			org_slug varchar(255) NOT NULL,
			id bigint NOT NULL,
			email varchar(255) NOT NULL,
			updated_at datetime NOT NULL,
			PRIMARY KEY (org_slug, id)
		)
	`)
	require.NoError(t, err)
	_, err = connectorDB.ExecContext(
		ctx,
		"DELETE FROM e2e_incremental_mysql_source WHERE org_slug = ?",
		orgSlug.String(),
	)
	require.NoError(t, err)
	_, err = connectorDB.ExecContext(ctx, `
		INSERT INTO e2e_incremental_mysql_source (org_slug, id, email, updated_at)
		VALUES
			(?, 1, 'alice.mysql.incremental@example.com', '2026-05-24 10:00:00'),
			(?, 2, 'bob.mysql.incremental@example.com', '2026-05-24 10:05:00')
	`, orgSlug.String(), orgSlug.String())
	require.NoError(t, err)

	sourceQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Incremental MySQL Source",
		map[string]any{
			"type": "mysql",
			"sql": `
				SELECT
					id,
					email,
					DATE_FORMAT(updated_at, '%Y-%m-%dT%H:%i:%sZ') AS updated_at
				FROM e2e_incremental_mysql_source
				WHERE org_slug = {{org_slug}}
					AND (
						STR_TO_DATE({{runtime.incremental.lower_bound.value}}, '%Y-%m-%dT%H:%i:%sZ') IS NULL
						OR updated_at > STR_TO_DATE({{runtime.incremental.lower_bound.value}}, '%Y-%m-%dT%H:%i:%sZ')
					)
				ORDER BY updated_at, id
			`,
		},
		30,
		[]*queriessvc.QueryParameter{{
			Name:       "org_slug",
			DataType:   queriessvc.ParameterDataTypeText,
			IsRequired: true,
		}},
	)
	pipeline := incrementalWatermarkSourceOnlyPipeline(
		must.Slug(fmt.Sprintf("e2e-mysql-watermark-%d", time.Now().UnixNano())),
		"E2E MySQL Watermark Incremental",
		dsSlug,
		sourceQuerySlug,
		"sql",
		"updated_at",
		map[string]any{"org_slug": orgSlug.String()},
		incrementalUsersContract(),
	)
	runner := newE2EPipelineRunner(t, stack)

	firstRun, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, fmt.Sprintf("e2e-mysql-watermark-1-%d", time.Now().UnixNano()), pipeline),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", firstRun.Metadata.Status)
	require.Len(t, firstRun.IncrementalCheckpointTransitions, 1)
	require.JSONEq(t, `{"value":"2026-05-24T10:05:00Z"}`, string(firstRun.IncrementalCheckpointTransitions[0].ObservedUpperBound))

	_, err = connectorDB.ExecContext(ctx, `
		INSERT INTO e2e_incremental_mysql_source (org_slug, id, email, updated_at)
		VALUES (?, 3, 'carol.mysql.incremental@example.com', '2026-05-24 10:10:00')
	`, orgSlug.String())
	require.NoError(t, err)

	secondRun, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, fmt.Sprintf("e2e-mysql-watermark-2-%d", time.Now().UnixNano()), pipeline),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", secondRun.Metadata.Status)
	sourceResult := secondRun.StepResults[must.Slug("fetch-users")]
	sqlResult, ok := sourceResult.SQL()
	require.True(t, ok)
	require.Equal(t, 1, sqlResult.RowCount)
	require.Equal(t, "carol.mysql.incremental@example.com", sqlResult.Rows[0]["email"])

	upperBound, status := currentConnectorE2EIncrementalCheckpoint(
		t,
		stack.pool,
		orgSlug,
		defaultProjectSlug,
		pipeline.Metadata.Slug,
		must.Slug("fetch-users"),
	)
	require.Equal(t, "committed", status)
	require.JSONEq(t, `{"value":"2026-05-24T10:10:00Z"}`, string(upperBound))
}

// testExecutesMongoWatermarkIncrementalPipelineWithRealConnector verifies the
// real MongoDB connector path for watermark incremental execution. The runtime
// lower-bound value is substituted into an aggregation expression and the max
// returned updated_at field is committed as the next checkpoint.
func testExecutesMongoWatermarkIncrementalPipelineWithRealConnector(
	t *testing.T,
	stack *testStack,
) {
	orgSlug, dsSlug := createOrgAndMongoDataSource(t, stack, "E2E Org Mongo Incremental")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cleanupQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Incremental Mongo Cleanup",
		map[string]any{
			"type":       "mongodb",
			"collection": "e2e_incremental_mongo_source",
			"operation":  "delete_many",
			"filter": map[string]any{
				"org_slug": orgSlug.String(),
			},
		},
		30,
		nil,
	)
	_, err := stack.queriesSvc.Execute(ctx, orgSlug, dsSlug, cleanupQuerySlug, model.SystemEventActor(), nil)
	require.NoError(t, err)

	seedMongoIncrementalSource(t, stack, orgSlug, dsSlug, []map[string]any{
		{"org_slug": orgSlug.String(), "id": 1, "email": "alice.mongo.incremental@example.com", "updated_at": "2026-05-24T10:00:00Z"},
		{"org_slug": orgSlug.String(), "id": 2, "email": "bob.mongo.incremental@example.com", "updated_at": "2026-05-24T10:05:00Z"},
	})

	sourceQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Incremental Mongo Source",
		map[string]any{
			"type":       "mongodb",
			"collection": "e2e_incremental_mongo_source",
			"operation":  "aggregate",
			"pipeline": []any{
				map[string]any{
					"$match": map[string]any{
						"org_slug": orgSlug.String(),
						"$expr": map[string]any{
							"$or": []any{
								map[string]any{"$eq": []any{"{{runtime.incremental.lower_bound.value}}", nil}},
								map[string]any{"$gt": []any{"$updated_at", "{{runtime.incremental.lower_bound.value}}"}},
							},
						},
					},
				},
				map[string]any{"$sort": map[string]any{"updated_at": 1, "id": 1}},
				map[string]any{"$project": map[string]any{"_id": 0, "id": 1, "email": 1, "updated_at": 1}},
			},
		},
		30,
		nil,
	)
	pipeline := incrementalWatermarkSourceOnlyPipeline(
		must.Slug(fmt.Sprintf("e2e-mongo-watermark-%d", time.Now().UnixNano())),
		"E2E Mongo Watermark Incremental",
		dsSlug,
		sourceQuerySlug,
		"mongodb",
		"updated_at",
		nil,
		incrementalUsersContract(),
	)
	runner := newE2EPipelineRunner(t, stack)

	firstRun, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, fmt.Sprintf("e2e-mongo-watermark-1-%d", time.Now().UnixNano()), pipeline),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", firstRun.Metadata.Status)
	require.Len(t, firstRun.IncrementalCheckpointTransitions, 1)
	require.JSONEq(t, `{"value":"2026-05-24T10:05:00Z"}`, string(firstRun.IncrementalCheckpointTransitions[0].ObservedUpperBound))

	seedMongoIncrementalSource(t, stack, orgSlug, dsSlug, []map[string]any{
		{"org_slug": orgSlug.String(), "id": 3, "email": "carol.mongo.incremental@example.com", "updated_at": "2026-05-24T10:10:00Z"},
	})

	secondRun, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, fmt.Sprintf("e2e-mongo-watermark-2-%d", time.Now().UnixNano()), pipeline),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", secondRun.Metadata.Status)
	sourceResult := secondRun.StepResults[must.Slug("fetch-users")]
	mongoResult, ok := sourceResult.MongoDB()
	require.True(t, ok)
	require.Len(t, mongoResult.Documents, 1)
	require.Equal(t, "carol.mongo.incremental@example.com", mongoResult.Documents[0]["email"])

	upperBound, status := currentConnectorE2EIncrementalCheckpoint(
		t,
		stack.pool,
		orgSlug,
		defaultProjectSlug,
		pipeline.Metadata.Slug,
		must.Slug("fetch-users"),
	)
	require.Equal(t, "committed", status)
	require.JSONEq(t, `{"value":"2026-05-24T10:10:00Z"}`, string(upperBound))
}

func seedMongoIncrementalSource(
	t *testing.T,
	stack *testStack,
	orgSlug slug.Slug,
	dsSlug slug.Slug,
	documents []map[string]any,
) {
	t.Helper()

	seedQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Incremental Mongo Seed",
		map[string]any{
			"type":       "mongodb",
			"collection": "e2e_incremental_mongo_source",
			"operation":  "insert_many",
			"documents":  documents,
		},
		30,
		nil,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := stack.queriesSvc.Execute(ctx, orgSlug, dsSlug, seedQuerySlug, model.SystemEventActor(), nil)
	require.NoError(t, err)
}

// testExecutesHTTPCursorIncrementalPipelineWithRealConnector verifies the real
// HTTP connector path for cursor incremental execution. The source request
// binds runtime.incremental.lower_bound.cursor to the cursor query parameter.
// The first run has no checkpoint yet, so the request must omit cursor entirely
// instead of sending cursor=<nil>. The response next_page_token is committed as
// the opaque cursor, and the next run sends cursor-1 before committing cursor-2.
func testExecutesHTTPCursorIncrementalPipelineWithRealConnector(t *testing.T, stack *testStack) {
	orgSlug, _, _ := createOrgUserAndPostgresDataSource(t, stack, "E2E Org HTTP Incremental")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var requestMu sync.Mutex
	requestedCursors := make([]string, 0, 2)
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(nethttp.StatusOK)
			return
		case "/users":
			query := r.URL.Query()
			cursor := query.Get("cursor")
			_, hasCursor := query["cursor"]
			requestMu.Lock()
			if hasCursor {
				requestedCursors = append(requestedCursors, cursor)
			} else {
				requestedCursors = append(requestedCursors, "<missing>")
			}
			requestMu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			if !hasCursor {
				_, writeErr := io.WriteString(w, `{"items":[{"id":1,"email":"alice@example.com"}],"next_page_token":"cursor-1"}`)
				if writeErr != nil {
					t.Errorf("write first cursor response: %v", writeErr)
				}
				return
			}

			if cursor != "cursor-1" {
				t.Errorf("unexpected cursor: got %q want %q", cursor, "cursor-1")
				w.WriteHeader(nethttp.StatusBadRequest)
				return
			}
			_, writeErr := io.WriteString(w, `{"items":[{"id":2,"email":"bob@example.com"}],"next_page_token":"cursor-2"}`)
			if writeErr != nil {
				t.Errorf("write second cursor response: %v", writeErr)
			}
		default:
			w.WriteHeader(nethttp.StatusNotFound)
		}
	}))
	defer server.Close()

	httpDsSlug := createHTTPDataSource(t, stack, orgSlug, server.URL)
	httpQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		httpDsSlug,
		"E2E Incremental HTTP Source",
		map[string]any{
			"type":         "http",
			"method":       "GET",
			"path":         "/users",
			"query_params": map[string]any{"cursor": "{{runtime.incremental.lower_bound.cursor}}"},
		},
		30,
		nil,
	)

	pipeline := incrementalHTTPCursorPipeline(
		must.Slug(fmt.Sprintf("e2e-http-cursor-%d", time.Now().UnixNano())),
		httpDsSlug,
		httpQuerySlug,
	)
	runner := newE2EPipelineRunner(t, stack)

	firstRun, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, fmt.Sprintf("e2e-http-cursor-1-%d", time.Now().UnixNano()), pipeline),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", firstRun.Metadata.Status)
	require.Len(t, firstRun.IncrementalCheckpointTransitions, 1)
	require.JSONEq(t, `{"cursor":"cursor-1"}`, string(firstRun.IncrementalCheckpointTransitions[0].ObservedUpperBound))

	secondRun, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, fmt.Sprintf("e2e-http-cursor-2-%d", time.Now().UnixNano()), pipeline),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", secondRun.Metadata.Status)
	require.Len(t, secondRun.IncrementalCheckpointTransitions, 1)
	require.JSONEq(t, `{"cursor":"cursor-2"}`, string(secondRun.IncrementalCheckpointTransitions[0].ObservedUpperBound))

	requestMu.Lock()
	gotCursors := append([]string(nil), requestedCursors...)
	requestMu.Unlock()
	require.Equal(t, []string{"<missing>", "cursor-1"}, gotCursors)

	upperBound, status := currentConnectorE2EIncrementalCheckpoint(
		t,
		stack.pool,
		orgSlug,
		defaultProjectSlug,
		pipeline.Metadata.Slug,
		must.Slug("fetch-users"),
	)
	require.Equal(t, "committed", status)
	require.JSONEq(t, `{"cursor":"cursor-2"}`, string(upperBound))
}

// testExecutesHubSpotCursorIncrementalPipelineWithRealConnector verifies the
// real HubSpot adapter against the local mock. The lower-bound cursor is bound
// to the native after parameter, and the returned next_cursor value is committed
// as opaque source progress.
func testExecutesHubSpotCursorIncrementalPipelineWithRealConnector(t *testing.T, stack *testStack) {
	orgSlug, _, _ := createOrgUserAndPostgresDataSource(t, stack, "E2E Org HubSpot Incremental")
	hubspotDsSlug := createHubSpotDataSource(t, stack, orgSlug)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	httpClient := &nethttp.Client{Timeout: 5 * time.Second}
	resetHubSpotMock(t, ctx, httpClient)
	seedHubSpotMockContacts(
		t,
		ctx,
		httpClient,
		[]map[string]any{
			{
				"id": "201",
				"properties": map[string]any{
					"email":     "alice.hubspot.incremental@example.com",
					"firstname": "Alice",
				},
			},
			{
				"id": "202",
				"properties": map[string]any{
					"email":     "bob.hubspot.incremental@example.com",
					"firstname": "Bob",
				},
			},
		},
	)

	hubspotQuerySlug := createQuery(
		t,
		stack,
		orgSlug,
		hubspotDsSlug,
		"E2E Incremental HubSpot Source",
		map[string]any{
			"type":        "hubspot",
			"object_type": "contacts",
			"operation":   "list",
			"properties":  []string{"email", "firstname"},
			"after":       "{{runtime.incremental.lower_bound.cursor}}",
			"limit":       1,
		},
		30,
		nil,
	)
	pipeline := incrementalCursorSourceOnlyPipeline(
		must.Slug(fmt.Sprintf("e2e-hubspot-cursor-%d", time.Now().UnixNano())),
		"E2E HubSpot Cursor Incremental",
		hubspotDsSlug,
		hubspotQuerySlug,
		"hubspot",
		"next_cursor",
	)
	runner := newE2EPipelineRunner(t, stack)

	firstRun, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, fmt.Sprintf("e2e-hubspot-cursor-1-%d", time.Now().UnixNano()), pipeline),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", firstRun.Metadata.Status)
	require.Len(t, firstRun.IncrementalCheckpointTransitions, 1)
	require.JSONEq(t, `{"cursor":"1"}`, string(firstRun.IncrementalCheckpointTransitions[0].ObservedUpperBound))

	secondRun, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, fmt.Sprintf("e2e-hubspot-cursor-2-%d", time.Now().UnixNano()), pipeline),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", secondRun.Metadata.Status)
	sourceResult := secondRun.StepResults[must.Slug("fetch-users")]
	hubspotResult, ok := sourceResult.ObjectRows()
	require.True(t, ok)
	require.Equal(t, 1, hubspotResult.RowCount)
	require.Equal(t, "202", hubspotResult.Rows[0]["hs_object_id"])
	require.Nil(t, hubspotResult.NextCursor)

	upperBound, status := currentConnectorE2EIncrementalCheckpoint(
		t,
		stack.pool,
		orgSlug,
		defaultProjectSlug,
		pipeline.Metadata.Slug,
		must.Slug("fetch-users"),
	)
	require.Equal(t, "committed", status)
	require.JSONEq(t, `{"cursor":"1"}`, string(upperBound))
}

// testExecutesRowsSourceSQLTransformCSVOutputSuccessfully verifies that a pipeline
// transform step producing output_kind: csv completes successfully and the run
// result exposes TransformOutputKindCSV. This exercises the csv representation path
// through loader, runner contract validation, and the persistence layer.
func testExecutesRowsSourceSQLTransformCSVOutputSuccessfully(t *testing.T, stack *testStack) {
	orgSlug, dsSlug := createOrgAndPostgresDataSource(t, stack, "E2E Org CSV Transform")
	querySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E CSV Transform Query",
		map[string]any{
			"type": "postgres",
			"sql":  `SELECT 'alice'::text AS csv_row UNION ALL SELECT 'bob'::text AS csv_row`,
		},
		30,
		nil,
	)

	pipelineSlug := slug.Slug(fmt.Sprintf("%s-csv", orgSlug))
	pipelineRunSlug := fmt.Sprintf("e2e-csv-pipeline-run-%d", time.Now().UnixNano())
	pipelineSpecYAML := fmt.Sprintf(`metadata:
  slug: %s
  name: E2E CSV Transform Pipeline
steps:
  - kind: query
    slug: fetch-csv-rows
    query:
      data_source_ref: %s
      operation_type: sql
      operation_ref: %s
  - kind: transform
    slug: project-csv-rows
    transform:
      inputs:
        input: fetch-csv-rows
      sql: "SELECT csv_row FROM input"
      input_contract:
        fields:
          - name: csv_row
            type: array
            required: true
      output_kind: csv
`, pipelineSlug, dsSlug, querySlug)

	pipelineSpec, err := pipelinevalidate.Load("e2e-csv-transform.yaml", []byte(pipelineSpecYAML))
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(stack.pool)
	runner, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: stack.queriesSvc},
		clock.New(),
		pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		stack.datasourcesSvc,
		noopSinkRuntime{},
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	runResult, err := runner.Run(
		ctx,
		newPipelineRunRequest(t, stack, orgSlug, pipelineRunSlug, pipelineSpec),
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	transformStepResult, ok := runResult.StepResults[must.Slug("project-csv-rows")]
	require.True(t, ok)
	transformOutput, ok := transformStepResult.Transform()
	require.True(t, ok)
	require.Equal(t, model.TransformOutputKindCSV, transformOutput.OutputKind)
	require.Equal(t, 2, transformOutput.RowCount)
	require.Equal(t, []string{"csv_row"}, transformOutput.ColumnNames)
	require.Equal(t, "csv_row\nalice\nbob\n", transformOutput.CSV)
}

func testReturnsUnavailableWhenDataSourceDeleted(t *testing.T, stack *testStack) {
	orgSlug, dsSlug, actorSlug := createOrgUserAndPostgresDataSource(
		t,
		stack,
		"E2E Org Deleted DS",
	)
	querySlug := createQuery(
		t,
		stack,
		orgSlug,
		dsSlug,
		"E2E Query Deleted DS",
		map[string]any{"type": "postgres", "sql": "SELECT 1"},
		30,
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := stack.datasourcesSvc.Delete(ctx, orgSlug, dsSlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)

	_, err = stack.queriesSvc.Execute(ctx, orgSlug, dsSlug, querySlug, model.SystemEventActor(), map[string]any{})
	require.Error(t, err)
	require.ErrorIs(t, err, queriessvc.ErrDataSourceUnavailable)
}
