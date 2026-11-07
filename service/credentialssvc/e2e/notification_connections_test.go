package e2e

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/stretchr/testify/require"
)

func TestNotificationConnectionOAuthLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	pool := e2edb.OpenPostgresPool(t, "notification connection lifecycle")
	org, otherOrg := uniqueCredentialE2ESlug("slack-org"), uniqueCredentialE2ESlug("other-slack-org")
	source := uniqueCredentialE2ESlug("unrelated-source")
	seedCredentialLifecycleDataSource(t, pool, org, source)
	seedCredentialLifecycleDataSource(t, pool, otherOrg, source)
	now := time.Now().UTC()
	provider := &fakeProviderAdapter{authURL: "https://slack.com/oauth/v2/authorize", exchange: credentialssvc.OAuthTokenSet{
		AccessToken: "local-test-slack-token", SlackWebhookURL: "https://hooks.slack.com/services/TTEST/BTEST/local-test-secret",
		Scopes: []string{"incoming-webhook"}, Account: credentialssvc.ProviderAccount{ID: "TTEST", Name: "Test workspace", ChannelID: "CTEST", ChannelName: "alerts"},
	}}
	cipher, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": []byte("0123456789abcdef0123456789abcdef")}, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	svc, err := credentialssvc.New(postgres.NewTransactionManager(pool), repocredentials.New(), cipher, credentialssvc.ProviderRegistry{"slack": provider}, testutil.FakeClock{NowFn: func() time.Time { return now }}, false)
	require.NoError(t, err)
	connectionSlug := uniqueCredentialE2ESlug("slack-alerts")
	owner := credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindNotificationConnection, OrgSlug: org, NotificationConnectionSlug: connectionSlug}
	connection, err := svc.CreateNotificationConnection(t.Context(), credentialssvc.CreateNotificationConnectionParams{Provider: "slack", OrgSlug: org, Slug: connectionSlug, Name: "Slack alerts"})
	require.NoError(t, err)
	require.Equal(t, credentialssvc.CredentialStatusPending, connection.Status)
	begin := func() credentialssvc.ProviderConnectorInstall {
		t.Helper()
		install, err := svc.ConnectNotificationConnection(t.Context(), owner, model.SystemEventActor())
		require.NoError(t, err)
		return install
	}
	expired := begin()
	now = now.Add(credentialssvc.DefaultInstallTTL + time.Second)
	_, err = svc.CompleteOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "slack", State: expired.State, Code: "local-expired-code"})
	require.ErrorIs(t, err, credentialssvc.ErrOAuthStateExpired)
	install := begin()
	found, err := svc.FindOAuthInstall(t.Context(), credentialssvc.FindOAuthInstallParams{Provider: "slack", State: install.State})
	require.NoError(t, err)
	require.Equal(t, owner, found.Owner)
	completed, err := svc.CompleteOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "slack", State: install.State, Code: "local-test-code"})
	require.NoError(t, err)
	require.Equal(t, owner, completed.Owner)
	_, err = svc.CompleteOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "slack", State: install.State, Code: "replay"})
	require.ErrorIs(t, err, credentialssvc.ErrOAuthStateConsumed)
	runtime, err := svc.ResolveRuntimeCredential(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
	require.NoError(t, err)
	require.JSONEq(t, `{"webhook_url":"https://hooks.slack.com/services/TTEST/BTEST/local-test-secret"}`, string(runtime.PlaintextJSON))
	connections, err := svc.ListNotificationConnections(t.Context(), org)
	require.NoError(t, err)
	require.Len(t, connections, 1)
	safe, err := json.Marshal(connections)
	require.NoError(t, err)
	require.NotContains(t, string(safe), "local-test-secret")
	require.NotContains(t, string(safe), "local-test-slack-token")
	require.Equal(t, "CTEST", connections[0].Account.ChannelID)
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT encode(g.encrypted_token_set,'escape') FROM credential_oauth_grants g JOIN credentials c ON c.id=g.credential_id WHERE c.slug=$1`, completed.Credential.Slug).Scan(&safe))
	require.NotContains(t, string(safe), "local-test-secret")
	wrongOwner := owner
	wrongOwner.OrgSlug = otherOrg
	_, err = svc.ResolveRuntimeCredential(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{Owner: wrongOwner})
	require.ErrorIs(t, err, credentialssvc.ErrCredentialNotFound)
	_, err = svc.Rotate(t.Context(), credentialssvc.RotateParams{Owner: wrongOwner})
	require.ErrorIs(t, err, credentialssvc.ErrCredentialNotFound)
	rotation, err := svc.Rotate(t.Context(), credentialssvc.RotateParams{Owner: owner})
	require.NoError(t, err)
	stale, err := svc.Rotate(t.Context(), credentialssvc.RotateParams{Owner: owner})
	require.NoError(t, err)
	rotated, err := svc.CompleteOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "slack", State: rotation.State, Code: "replacement"})
	require.NoError(t, err)
	require.NotEqual(t, completed.Credential.Slug, rotated.Credential.Slug)
	_, err = svc.CompleteOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "slack", State: stale.State, Code: "stale-replacement"})
	require.ErrorIs(t, err, credentialssvc.ErrOAuthIntentConflict)
	_, err = svc.Revoke(t.Context(), credentialssvc.RevokeParams{Owner: owner})
	require.NoError(t, err)
	_, err = svc.ResolveRuntimeCredential(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
	require.ErrorIs(t, err, credentialssvc.ErrProviderGrantRevoked)
	var remaining int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT octet_length(g.encrypted_token_set) FROM credential_oauth_grants g JOIN credentials c ON c.id=g.credential_id WHERE c.slug=$1`, rotated.Credential.Slug).Scan(&remaining))
	require.Zero(t, remaining)
}

func TestNotificationConnectionGeneratedIdentifiers(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	pool := e2edb.OpenPostgresPool(t, "generated notification identifiers")
	org := uniqueCredentialE2ESlug("generated-slack-org")
	seedCredentialLifecycleDataSource(t, pool, org, uniqueCredentialE2ESlug("source"))
	cipher, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": []byte("0123456789abcdef0123456789abcdef")}, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	svc, err := credentialssvc.New(postgres.NewTransactionManager(pool), repocredentials.New(), cipher, credentialssvc.ProviderRegistry{"slack": &fakeProviderAdapter{}}, testutil.FakeClock{}, false)
	require.NoError(t, err)
	input := credentialssvc.CreateNotificationConnectionParams{Provider: "slack", OrgSlug: org, Name: "Operations"}
	first, err := svc.CreateNotificationConnection(t.Context(), input)
	require.NoError(t, err)
	second, err := svc.CreateNotificationConnection(t.Context(), input)
	require.NoError(t, err)
	require.NotEqual(t, first.Slug, second.Slug)
	connections, err := svc.ListNotificationConnections(t.Context(), org)
	require.NoError(t, err)
	require.Len(t, connections, 2)
	input.Slug = first.Slug
	_, err = svc.CreateNotificationConnection(t.Context(), input)
	require.ErrorIs(t, err, repocredentials.ErrNotificationConnectionSlugTaken)
}
