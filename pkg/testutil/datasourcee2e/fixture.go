// Package datasourcee2e provides reusable real-Postgres data-source E2E setup.
package datasourcee2e

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type Stack struct {
	Pool        *pgxpool.Pool
	Txm         postgres.TransactionManager
	Locker      postgres.SessionLocker
	Repo        repods.Repository
	SchemaRepo  repods.SchemaMetadataRepository
	Credentials credentialssvc.Service
	Admission   admissionsvc.Service
}

func NewStack(t testing.TB, pool *pgxpool.Pool, registries ...credentialssvc.ProviderRegistry) *Stack {
	t.Helper()
	txm := postgres.NewTransactionManager(pool)
	encryptor, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": []byte("0123456789abcdef0123456789abcdef")}, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	var providers credentialssvc.ProviderRegistry
	if len(registries) > 0 {
		providers = registries[0]
	}
	credentials, err := credentialssvc.New(txm, repocredentials.New(), encryptor, providers, clock.New(), false)
	require.NoError(t, err)
	locker := postgres.NewSessionLocker(pool)
	return &Stack{Pool: pool, Txm: txm, Locker: locker, Repo: repods.New(), SchemaRepo: repods.NewSchemaMetadataRepository(), Credentials: credentials, Admission: admissionsvc.New(txm, organizations.NewRepository())}
}

type ServiceConfig struct {
	SnowflakeWIFIssuer   string
	ConnectionTester     datasourcessvc.ConnectionTestExecutor
	ConnectorFactory     connector.NewConnectorFunc
	DriftReports         driftimpactrepo.Repository
	SchemaImportProducer workerproducer.DataSourceSchemaImportProducer
	GitBranchExecutor    datasourcessvc.GitBranchExecutor
	GitHubApps           datasourcessvc.GitHubAppManager
}

func (s *Stack) Service(t testing.TB, executor datasourcessvc.ConnectionTestExecutor) datasourcessvc.Service {
	t.Helper()
	return s.ServiceWithConfig(t, ServiceConfig{ConnectionTester: executor})
}

func (s *Stack) ServiceWithConfig(t testing.TB, c ServiceConfig) datasourcessvc.Service {
	t.Helper()
	producer := c.SchemaImportProducer
	if producer == nil {
		producer = SchemaImportProducerStub{}
	}
	config := datasourcessvc.ServiceConfig{SnowflakeWIFIssuer: c.SnowflakeWIFIssuer, ConnectionTestExecutor: c.ConnectionTester, GitBranchExecutor: c.GitBranchExecutor}
	if c.GitBranchExecutor != nil {
		config.GitBranchMaxBytes = 1 << 20
		config.GitBranchMaxItems = 100
	}
	apps := c.GitHubApps
	if apps == nil {
		apps = datasourcessvc.NewNoopGitHubAppManager()
	}
	service, err := datasourcessvc.New(s.Txm, s.Repo, s.SchemaRepo, c.DriftReports, s.Credentials, apps, datasourcessvc.NewNoopGitLabWebhookManager(), s.Admission, c.ConnectorFactory, eventtest.NewProducer(nil), producer, config)
	require.NoError(t, err)
	return service
}

func (s *Stack) FindDataSource(t testing.TB, org, source slug.Slug) *repods.DataSource {
	t.Helper()
	var found *repods.DataSource
	err := s.Txm.RunRead(t.Context(), func(ctx context.Context, db postgres.DBTX) error {
		var e error
		found, e = s.Repo.FindBySlug(ctx, db, org, source)
		return e
	})
	require.NoError(t, err)
	return found
}

type ConnectionTestExecutorFunc func(context.Context, datasourcessvc.ConnectionTestRequest) error

func (f ConnectionTestExecutorFunc) TestConnection(ctx context.Context, r datasourcessvc.ConnectionTestRequest) error {
	return f(ctx, r)
}

type GitBranchExecutorFunc func(context.Context, datasourcessvc.GitBranchRequest) ([]datasourcessvc.DataSourceBranch, error)

func (f GitBranchExecutorFunc) ListGitBranches(ctx context.Context, r datasourcessvc.GitBranchRequest) ([]datasourcessvc.DataSourceBranch, error) {
	return f(ctx, r)
}

type SchemaImportProducerStub struct{}

func (SchemaImportProducerStub) EnqueueDataSourceSchemaImport(context.Context, postgres.DBTX, jobs.DataSourceSchemaImportArgs) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func SeedOrganization(t testing.TB, pool *pgxpool.Pool, org slug.Slug) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `INSERT INTO organizations (name, slug) VALUES ($1, $2)`, `Data Source E2E Org`, org)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := pool.Exec(ctx, `DELETE FROM organizations WHERE slug = $1`, org)
		require.NoError(t, err)
	})
}

func UniqueSlug(t testing.TB, prefix string) slug.Slug {
	t.Helper()
	value, err := slug.WithRandomSuffix(slug.Slug(prefix), 8)
	require.NoError(t, err)
	return value
}

func NewPostgresDataSourceParams(source slug.Slug, name string) *datasourcessvc.CreateParams {
	return &datasourcessvc.CreateParams{Name: name, Slug: source, Type: model.DataSourceTypePostgres, ConfigJSON: json.RawMessage(`{"host":"db.example","port":5432,"database":"app","ssl_mode":"verify-full"}`), Credentials: map[string]any{"username": "app", "password": "test-only"}}
}

func NewPostgresCredentialUpdateParams() *datasourcessvc.UpdateCredentialsParams {
	return &datasourcessvc.UpdateCredentialsParams{Credentials: map[string]any{"username": "rotated-app", "password": "rotated-test-only"}}
}

func StoredDataSourceCredentialSlug(t testing.TB, pool *pgxpool.Pool, org, source slug.Slug) slug.Slug {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var result string
	err := pool.QueryRow(ctx, `SELECT c.slug FROM data_sources ds JOIN organizations o ON o.id=ds.organization_id JOIN credentials c ON c.id=ds.credential_id WHERE o.slug=$1 AND ds.slug=$2 AND ds.deleted_at IS NULL`, org, source).Scan(&result)
	require.NoError(t, err)
	return slug.Slug(result)
}

func AssertStoredCredentialState(t testing.TB, pool *pgxpool.Pool, org, credential slug.Slug, status string, length int) {
	t.Helper()
	gotStatus, gotLength := storedCredentialState(t, pool, org, credential)
	require.Equal(t, status, gotStatus)
	require.Equal(t, length, gotLength)
}

func AssertStoredCredentialHasPayload(t testing.TB, pool *pgxpool.Pool, org, credential slug.Slug, status string) {
	t.Helper()
	gotStatus, gotLength := storedCredentialState(t, pool, org, credential)
	require.Equal(t, status, gotStatus)
	require.Positive(t, gotLength)
}

func storedCredentialState(t testing.TB, pool *pgxpool.Pool, org, credential slug.Slug) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var status string
	var length int
	err := pool.QueryRow(ctx, `SELECT c.status, COALESCE(octet_length(c.encrypted_payload),0) FROM credentials c JOIN organizations o ON o.id=c.organization_id WHERE o.slug=$1 AND c.slug=$2`, org, credential).Scan(&status, &length)
	require.NoError(t, err)
	return status, length
}
