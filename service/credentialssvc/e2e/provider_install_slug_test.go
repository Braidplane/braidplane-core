package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	repodatasources "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

// TestProviderInstallGeneratedSlugHonorsActiveAndDeletedReservations verifies OAuth-first creation suffixes database-reserved slugs without crossing organizations.
func TestProviderInstallGeneratedSlugHonorsActiveAndDeletedReservations(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres provider install slug reservation E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "provider install slug reservation service integration test")
	reservedOrgSlug := uniqueSlug(t, "provider-reserved-org")
	otherOrgSlug := uniqueSlug(t, "provider-other-org")
	seedReservedDataSource(t, pool, reservedOrgSlug, "crm")
	seedOrganization(t, pool, otherOrgSlug)
	svc := newProviderInstallService(t, pool)

	activeResult, err := svc.StartProviderDataSourceInstall(
		ctx,
		reservedOrgSlug,
		model.SystemEventActor(),
		"hubspot",
		providerInstallParams(),
	)
	require.NoError(t, err)
	require.NotNil(t, activeResult)
	assert.Regexp(t, `^crm-[a-z0-9]{6}$`, activeResult.DataSource.Slug.String())

	commandTag, err := pool.Exec(ctx, `UPDATE data_sources SET deleted_at = NOW() WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1) AND slug = 'crm'`, reservedOrgSlug)
	require.NoError(t, err)
	require.EqualValues(t, 1, commandTag.RowsAffected())

	deletedResult, err := svc.StartProviderDataSourceInstall(
		ctx,
		reservedOrgSlug,
		model.SystemEventActor(),
		"hubspot",
		providerInstallParams(),
	)
	require.NoError(t, err)
	require.NotNil(t, deletedResult)
	assert.Regexp(t, `^crm-[a-z0-9]{6}$`, deletedResult.DataSource.Slug.String())

	otherOrgResult, err := svc.StartProviderDataSourceInstall(
		ctx,
		otherOrgSlug,
		model.SystemEventActor(),
		"hubspot",
		providerInstallParams(),
	)
	require.NoError(t, err)
	require.NotNil(t, otherOrgResult)
	assert.Equal(t, "crm", otherOrgResult.DataSource.Slug.String())
}

// providerInstallParams returns valid OAuth-first HubSpot input with a generated slug.
func providerInstallParams() *datasourcessvc.StartProviderDataSourceInstallParams {
	return &datasourcessvc.StartProviderDataSourceInstallParams{
		Name:       "CRM",
		Type:       model.DataSourceTypeHubSpot,
		ConfigJSON: json.RawMessage(`{"timeout_seconds":5}`),
	}
}

// newProviderInstallService composes the real data-source and credential repositories for reservation coverage.
func newProviderInstallService(t *testing.T, pool *pgxpool.Pool) datasourcessvc.Service {
	t.Helper()

	txm := postgres.NewTransactionManager(pool)
	enc, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": []byte("0123456789abcdef0123456789abcdef")}, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	credentials, err := credentialssvc.New(
		txm,
		repocredentials.New(),
		enc,
		credentialssvc.ProviderRegistry{"hubspot": providerAdapter{}},
		testutil.FakeClock{NowFn: func() time.Time { return time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC) }}, false)
	require.NoError(t, err)
	svc, err := datasourcessvc.New(
		txm,
		repodatasources.New(),
		testutil.NewFakeSchemaMetadataRepo(),
		nil,
		credentials,
		datasourcessvc.NewNoopGitHubAppManager(),
		datasourcessvc.NewNoopGitLabWebhookManager(),
		&fakes.AdmissionService{},
		nil,
		nil,
		noopSchemaImportProducer{},
		datasourcessvc.ServiceConfig{},
	)
	require.NoError(t, err)
	return svc
}

// seedReservedDataSource creates an active row whose slug remains reserved after soft deletion.
func seedReservedDataSource(t *testing.T, pool *pgxpool.Pool, orgSlug slug.Slug, dataSourceSlug string) {
	t.Helper()
	seedOrganization(t, pool, orgSlug)

	credentialSlug := fmt.Sprintf("%s-credential", dataSourceSlug)
	_, err := pool.Exec(t.Context(), `
WITH org AS (
	SELECT id FROM organizations WHERE slug = $1
), credential AS (
	INSERT INTO credentials (organization_id, slug, kind, credential_type, status, encrypted_payload)
	SELECT id, $2, 'static_secret', 'hubspot', 'active', decode('706c616365686f6c646572', 'hex')
	FROM org
	RETURNING id, organization_id
)
INSERT INTO data_sources (organization_id, credential_id, name, slug, type, config, is_active, read_only)
SELECT organization_id, id, 'Reserved CRM', $3, 'hubspot', '{}'::jsonb, true, true
FROM credential`, orgSlug, credentialSlug, dataSourceSlug)
	require.NoError(t, err)
}

// seedOrganization creates an isolated organization and registers cascade cleanup.
func seedOrganization(t *testing.T, pool *pgxpool.Pool, orgSlug slug.Slug) {
	t.Helper()

	_, err := pool.Exec(t.Context(), `INSERT INTO organizations (name, slug) VALUES ($1, $2)`, "Provider Install E2E Org", orgSlug)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = $1`, orgSlug)
		require.NoError(t, cleanupErr)
	})
}

// uniqueSlug returns a collision-resistant slug for shared-database E2E tests.
func uniqueSlug(t *testing.T, prefix string) slug.Slug {
	t.Helper()

	value, err := slug.WithRandomSuffix(slug.Slug(prefix), 8)
	require.NoError(t, err)
	return value
}

// providerAdapter supplies the authorization URL needed to persist OAuth installs.
type providerAdapter struct{}

// OAuthScopePlan supplies a non-empty required scope for the persisted install contract.
func (providerAdapter) OAuthScopePlan() credentialssvc.OAuthScopePlan {
	return credentialssvc.OAuthScopePlan{Required: []string{"crm.objects.contacts.read"}}
}

// AuthorizationURL returns a deterministic provider redirect for the E2E install flow.
func (providerAdapter) AuthorizationURL(context.Context, credentialssvc.AuthorizationURLParams) (string, error) {
	return "https://hubspot.example/oauth/authorize", nil
}

// ExchangeCode is unavailable because this test stops before callback completion.
func (providerAdapter) ExchangeCode(context.Context, credentialssvc.ExchangeCodeParams) (credentialssvc.OAuthTokenSet, error) {
	return credentialssvc.OAuthTokenSet{}, assert.AnError
}

// Refresh is unavailable because this test never resolves a persisted grant.
func (providerAdapter) Refresh(context.Context, credentialssvc.RefreshParams) (credentialssvc.OAuthTokenSet, error) {
	return credentialssvc.OAuthTokenSet{}, assert.AnError
}

// Revoke is unavailable because this test never persists an active grant.
func (providerAdapter) Revoke(context.Context, credentialssvc.RevokeProviderGrantParams) error {
	return assert.AnError
}

type noopSchemaImportProducer struct{}

// EnqueueDataSourceSchemaImport is unused because provider install creation does not enqueue schema work.
func (noopSchemaImportProducer) EnqueueDataSourceSchemaImport(context.Context, postgres.DBTX, jobs.DataSourceSchemaImportArgs) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, assert.AnError
}
