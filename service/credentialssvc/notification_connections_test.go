package credentialssvc

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/stretchr/testify/require"
)

func (r *credentialRepo) CreateNotificationConnection(_ context.Context, _ postgres.DBTX, record repocredentials.CredentialRecord, _, _ string) (repocredentials.CredentialRecord, error) {
	for _, existing := range r.pendingOAuthCredentials {
		if existing.OrgSlug == record.OrgSlug && existing.NotificationConnectionSlug == record.NotificationConnectionSlug {
			return repocredentials.CredentialRecord{}, repocredentials.ErrNotificationConnectionSlugTaken
		}
	}
	record.Kind = "oauth2_grant"
	record.Status = "pending"
	r.pendingOAuthCredentials = append(r.pendingOAuthCredentials, record)
	return record, nil
}

func (r *credentialRepo) FindNotificationConnectionCredential(_ context.Context, _ postgres.DBTX, owner repocredentials.ResolveRuntimeCredentialParams) (repocredentials.NotificationConnectionCredential, error) {
	for _, record := range r.pendingOAuthCredentials {
		if record.OrgSlug == owner.OrgSlug && record.NotificationConnectionSlug == owner.NotificationConnectionSlug && record.OwnerKind == repocredentials.OwnerKindNotificationConnection {
			return repocredentials.NotificationConnectionCredential{CredentialRecord: record, Provider: "slack"}, nil
		}
	}
	return repocredentials.NotificationConnectionCredential{}, ErrCredentialNotFound
}

func (*credentialRepo) ListNotificationConnections(context.Context, postgres.DBTX, slug.Slug) ([]repocredentials.NotificationConnection, error) {
	return []repocredentials.NotificationConnection{}, nil
}

func TestNotificationConnectionStoresOnlyPendingCredential(t *testing.T) {
	repo := newCredentialRepo()
	svc := newTestService(t, repo, ProviderRegistry{"slack": &fakeProviderAdapter{}})
	connection, err := svc.CreateNotificationConnection(t.Context(), CreateNotificationConnectionParams{Provider: "slack", OrgSlug: must.Slug("acme"), Slug: must.Slug("alerts"), Name: "Slack alerts"})
	require.NoError(t, err)
	require.Equal(t, CredentialStatusPending, connection.Status)
	require.Len(t, repo.pendingOAuthCredentials, 1)
	require.Empty(t, repo.pendingOAuthCredentials[0].EncryptedPayload)
	require.Equal(t, repocredentials.OwnerKindNotificationConnection, repo.pendingOAuthCredentials[0].OwnerKind)
	require.Equal(t, must.Slug("alerts"), repo.pendingOAuthCredentials[0].NotificationConnectionSlug)
}

func TestNotificationConnectionOwnerRejectsMixedIdentities(t *testing.T) {
	owner := CredentialOwner{Kind: CredentialOwnerKindNotificationConnection, OrgSlug: must.Slug("acme"), NotificationConnectionSlug: must.Slug("alerts")}
	require.NoError(t, validateOAuthOwner(owner))
	owner.DataSourceSlug = must.Slug("source")
	require.Error(t, validateOAuthOwner(owner))
	owner.Kind = CredentialOwnerKindDataSource
	require.Error(t, validateOAuthOwner(owner))
	owner = CredentialOwner{Kind: CredentialOwnerKindNotificationConnection, OrgSlug: must.Slug("acme")}
	require.Error(t, validateOAuthOwner(owner))
}

func TestNotificationConnectionRequiresSupportedExplicitProvider(t *testing.T) {
	for _, provider := range []string{"", "pagerduty", "hubspot"} {
		repo := newCredentialRepo()
		svc := newTestService(t, repo, ProviderRegistry{"slack": &fakeProviderAdapter{}})
		_, err := svc.CreateNotificationConnection(t.Context(), CreateNotificationConnectionParams{Provider: provider, OrgSlug: must.Slug("acme"), Slug: must.Slug("alerts"), Name: "Alerts"})
		require.Error(t, err)
		require.Empty(t, repo.pendingOAuthCredentials)
	}
}

func TestNotificationConnectionGeneratedIdentifiers(t *testing.T) {
	repo := newCredentialRepo()
	svc := newTestService(t, repo, ProviderRegistry{"slack": &fakeProviderAdapter{}})
	input := CreateNotificationConnectionParams{Provider: "slack", OrgSlug: "acme", Name: "Operations"}
	first, err := svc.CreateNotificationConnection(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, slug.Slug("operations"), first.Slug)
	second, err := svc.CreateNotificationConnection(t.Context(), input)
	require.NoError(t, err)
	require.NotEqual(t, first.Slug, second.Slug)
	require.Contains(t, second.Slug.String(), "operations-")
	input.Slug = first.Slug
	_, err = svc.CreateNotificationConnection(t.Context(), input)
	require.ErrorIs(t, err, repocredentials.ErrNotificationConnectionSlugTaken)
	input.Slug = "INVALID"
	_, err = svc.CreateNotificationConnection(t.Context(), input)
	require.ErrorIs(t, err, ErrInvalidInput)
	input.Slug = ""
	input.Name = "日本語"
	third, err := svc.CreateNotificationConnection(t.Context(), input)
	require.NoError(t, err)
	_, err = slug.New(third.Slug.String())
	require.NoError(t, err)
}
