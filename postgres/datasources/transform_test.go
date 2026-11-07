package datasources

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	dbint "github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func TestToDataSourceWithCredentialMapsSafeMetadata(t *testing.T) {
	t.Parallel()
	dataSourceUpdatedAt := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	credentialUpdatedAt := time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC)
	accessTokenExpiresAt := time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC)
	lastRefreshedAt := time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC)

	dataSource, err := toDataSourceWithCredential(dbint.DataSource{
		Slug:               must.Slug("hubspot-crm"),
		Name:               "HubSpot CRM",
		Type:               model.DataSourceTypeHubSpot.String(),
		SchemaImportStatus: model.DataSourceSchemaImportStatusNotImported.String(),
		CreatedAt:          pgtype.Timestamptz{Time: dataSourceUpdatedAt, Valid: true},
		UpdatedAt:          pgtype.Timestamptz{Time: dataSourceUpdatedAt, Valid: true},
	}, []byte(`{
		"slug":"credential-hubspot-crm",
		"kind":"oauth2_grant",
		"status":"active",
		"provider":"hubspot",
		"updated_at":"2026-07-17T09:00:00Z",
		"access_token_expires_at":"2026-07-17T10:00:00Z",
		"scopes":["crm.objects.contacts.read"],
		"account":{"ID":"acct-1","Name":"Acme CRM","Email":"ops@example.com"},
		"last_refreshed_at":"2026-07-17T08:30:00Z",
		"revoked_at":null,
		"failure_code":"refresh_failed",
		"failure_message":"Reconnect HubSpot"
	}`))

	require.NoError(t, err)
	require.NotNil(t, dataSource.Credential)
	assert.Equal(t, must.Slug("credential-hubspot-crm"), dataSource.Credential.Slug)
	assert.Equal(t, "oauth2_grant", dataSource.Credential.Kind)
	assert.Equal(t, "active", dataSource.Credential.Status)
	assert.Equal(t, "hubspot", dataSource.Credential.Provider)
	assert.Equal(t, credentialUpdatedAt, dataSource.Credential.UpdatedAt)
	assert.Equal(t, accessTokenExpiresAt, *dataSource.Credential.AccessTokenExpiresAt)
	assert.Equal(t, []string{"crm.objects.contacts.read"}, dataSource.Credential.Scopes)
	assert.Equal(t, ProviderAccount{ID: "acct-1", Name: "Acme CRM", Email: "ops@example.com"}, dataSource.Credential.Account)
	assert.Equal(t, lastRefreshedAt, *dataSource.Credential.LastRefreshedAt)
	assert.Nil(t, dataSource.Credential.RevokedAt)
	assert.Equal(t, "refresh_failed", dataSource.Credential.FailureCode)
	assert.Equal(t, "Reconnect HubSpot", dataSource.Credential.FailureMessage)
}
