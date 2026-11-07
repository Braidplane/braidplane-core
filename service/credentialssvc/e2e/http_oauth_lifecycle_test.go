package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/httpoauth"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPOAuthMachineLifecyclePreservesEncryptedProviderBinding(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		assert.NoError(t, r.ParseForm())
		_, secret, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "original-client-secret", secret)
		if r.URL.Path == "/revoke" {
			assert.Equal(t, "machine-access-token", r.Form.Get("token"))
			w.WriteHeader(http.StatusOK)
			return
		}
		assert.Equal(t, "client_credentials", r.Form.Get("grant_type"))
		_, _ = fmt.Fprint(w, `{"access_token":"machine-access-token","token_type":"Bearer","expires_in":60}`)
	}))
	defer server.Close()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	pool := e2edb.OpenPostgresPool(t, "HTTP OAuth machine lifecycle")
	org := uniqueCredentialE2ESlug("http-machine-org")
	source := uniqueCredentialE2ESlug("http-machine-source")
	seedCredentialLifecycleDataSource(t, pool, org, source)
	_, err := pool.Exec(t.Context(), `UPDATE data_sources SET type = 'http' WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1) AND slug = $2`, org, source)
	require.NoError(t, err)
	cipher, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": []byte("0123456789abcdef0123456789abcdef")}, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	now := time.Now()
	svc, err := credentialssvc.New(postgres.NewTransactionManager(pool), repocredentials.New(), cipher, nil, testutil.FakeClock{NowFn: func() time.Time { return now }}, true)
	require.NoError(t, err)
	owner := credentialOwner(org, source)
	app := credentialssvc.HTTPOAuthApp{Config: httpoauth.Config{Issuer: server.URL, TokenEndpoint: server.URL + "/token", RevocationEndpoint: server.URL + "/revoke", ClientID: "machine-client", GrantType: "client_credentials"}, ClientSecret: "original-client-secret", Destination: "https://api.example/v1", AllowPrivateHosts: true}
	require.NoError(t, svc.ConfigureHTTPOAuthApp(t.Context(), credentialssvc.ConfigureHTTPOAuthAppParams{Owner: owner, App: app}))
	_, err = svc.ConnectHTTPOAuthClientCredentials(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	var encryptedApp, encryptedTokens []byte
	err = pool.QueryRow(t.Context(), `SELECT app.encrypted_config, g.encrypted_token_set FROM credential_http_oauth_apps app JOIN organizations o ON o.id = app.organization_id JOIN data_sources ds ON ds.organization_id = app.organization_id AND ds.id = app.data_source_id JOIN credential_oauth_grants g ON g.credential_id = ds.credential_id WHERE o.slug = $1 AND ds.slug = $2`, org, source).Scan(&encryptedApp, &encryptedTokens)
	require.NoError(t, err)
	for _, blob := range [][]byte{encryptedApp, encryptedTokens} {
		assert.NotContains(t, string(blob), "original-client-secret")
		assert.NotContains(t, string(blob), "machine-access-token")
	}
	other := owner
	other.OrgSlug = uniqueCredentialE2ESlug("nonexistent-http-org")
	_, err = svc.ConnectHTTPOAuthClientCredentials(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{Owner: other})
	require.ErrorIs(t, err, credentialssvc.ErrCredentialNotFound)
	changed := app
	changed.Config.TokenEndpoint = "https://other.example/token"
	changed.Config.RevocationEndpoint = "https://other.example/revoke"
	changed.ClientSecret = "replacement-client-secret"
	require.NoError(t, svc.ConfigureHTTPOAuthApp(t.Context(), credentialssvc.ConfigureHTTPOAuthAppParams{Owner: owner, App: changed}))
	now = now.Add(2 * time.Minute)
	runtime, err := svc.ResolveRuntimeCredential(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	var runtimePayload map[string]any
	require.NoError(t, json.Unmarshal(runtime.PlaintextJSON, &runtimePayload))
	require.Equal(t, map[string]any{"bearer_token": "machine-access-token", "oauth_destination": app.Destination}, runtimePayload)
	_, err = svc.Revoke(t.Context(), credentialssvc.RevokeParams{Owner: owner})
	require.NoError(t, err)
	require.Equal(t, 3, calls)
	_, err = svc.ResolveRuntimeCredential(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
	require.ErrorIs(t, err, credentialssvc.ErrProviderGrantRevoked)
}

func TestHTTPOAuthCallbackRejectsChangedSourceRevision(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "HTTP OAuth callback revision")
	org := uniqueCredentialE2ESlug("http-callback-org")
	source := uniqueCredentialE2ESlug("http-callback-source")
	seedCredentialLifecycleDataSource(t, pool, org, source)
	_, err := pool.Exec(t.Context(), `UPDATE data_sources SET type = 'http' WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1) AND slug = $2`, org, source)
	require.NoError(t, err)
	cipher, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": []byte("0123456789abcdef0123456789abcdef")}, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	svc, err := credentialssvc.New(postgres.NewTransactionManager(pool), repocredentials.New(), cipher, nil, testutil.FakeClock{NowFn: time.Now}, false)
	require.NoError(t, err)
	owner := credentialOwner(org, source)
	app := credentialssvc.HTTPOAuthApp{Config: httpoauth.Config{Issuer: "https://issuer.example", AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token", ClientID: "client", GrantType: "authorization_code"}, ClientSecret: "client-secret", RedirectURI: "https://braidplane.example/callback", Destination: "https://api.example/v1"}
	require.NoError(t, svc.ConfigureHTTPOAuthApp(t.Context(), credentialssvc.ConfigureHTTPOAuthAppParams{Owner: owner, App: app}))
	install, err := svc.StartOAuthInstall(t.Context(), credentialssvc.StartOAuthInstallParams{Owner: owner, Provider: "http", Intent: credentialssvc.OAuthInstallIntentRotate})
	require.NoError(t, err)
	app.ClientSecret = "new-client-secret"
	require.NoError(t, svc.ConfigureHTTPOAuthApp(t.Context(), credentialssvc.ConfigureHTTPOAuthAppParams{Owner: owner, App: app}))
	// The revision check must fail before any request to these non-existent provider endpoints.
	_, err = svc.CompleteOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "http", State: install.State, Code: "code", Issuer: app.Config.Issuer})
	require.ErrorIs(t, err, credentialssvc.ErrOAuthIntentConflict)
}

func TestHTTPOAuthConcurrentRefreshPreservesGrant(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	started := make(chan struct{}, 1)
	blocked := make(chan struct{}, 1)
	release := make(chan struct{})
	var refreshes atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		assert.NoError(t, r.ParseForm())
		_, secret, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "concurrent-client-secret", secret)
		if r.URL.Path == "/revoke" {
			assert.Equal(t, "rotated-refresh-token", r.Form.Get("token"))
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			if refreshes.Add(1) != 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(w, `{"error":"invalid_grant"}`)
				return
			}
			assert.Equal(t, "refresh-token", r.Form.Get("refresh_token"))
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
			_, _ = fmt.Fprint(w, `{"access_token":"renewed-token","refresh_token":"rotated-refresh-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"access_token":"initial-token","refresh_token":"refresh-token","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	pool := e2edb.OpenPostgresPool(t, "HTTP OAuth concurrent refresh")
	org := uniqueCredentialE2ESlug("http-refresh-org")
	source := uniqueCredentialE2ESlug("http-refresh-source")
	seedCredentialLifecycleDataSource(t, pool, org, source)
	_, err := pool.Exec(ctx, `UPDATE data_sources SET type = 'http' WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1) AND slug = $2`, org, source)
	require.NoError(t, err)
	cipher, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": []byte("0123456789abcdef0123456789abcdef")}, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	svc, err := credentialssvc.New(postgres.NewTransactionManager(pool), &recordingHTTPOAuthClaimRepo{Repository: repocredentials.New(), blocked: blocked}, cipher, nil, testutil.FakeClock{NowFn: time.Now}, true)
	require.NoError(t, err)
	owner := credentialOwner(org, source)
	app := credentialssvc.HTTPOAuthApp{Config: httpoauth.Config{Issuer: server.URL, AuthorizationEndpoint: server.URL + "/authorize", TokenEndpoint: server.URL + "/token", RevocationEndpoint: server.URL + "/revoke", ClientID: "concurrent-client", GrantType: "authorization_code"}, ClientSecret: "concurrent-client-secret", RedirectURI: "https://braidplane.example/callback", Destination: "https://api.example/v1", AllowPrivateHosts: true}
	require.NoError(t, svc.ConfigureHTTPOAuthApp(ctx, credentialssvc.ConfigureHTTPOAuthAppParams{Owner: owner, App: app}))
	install, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{Owner: owner, Provider: "http", Intent: credentialssvc.OAuthInstallIntentRotate})
	require.NoError(t, err)
	_, err = svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{Provider: "http", State: install.State, Code: "code", Issuer: app.Config.Issuer})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE credential_oauth_grants g SET access_token_expires_at = NOW() - INTERVAL '1 minute' FROM data_sources ds JOIN organizations o ON o.id = ds.organization_id WHERE g.credential_id = ds.credential_id AND o.slug = $1 AND ds.slug = $2`, org, source)
	require.NoError(t, err)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			runtime, err := svc.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
			if err == nil {
				var payload map[string]any
				err = json.Unmarshal(runtime.PlaintextJSON, &payload)
				if err == nil && payload["bearer_token"] != "renewed-token" {
					err = fmt.Errorf("unexpected runtime token after concurrent refresh")
				}
			}
			results <- err
		}()
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(release)
	for range 2 {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	require.Equal(t, int32(1), refreshes.Load())
	// A second resolve uses the committed grant rather than refreshing or returning a revoked grant.
	_, err = svc.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
	require.NoError(t, err)
	require.Equal(t, int32(1), refreshes.Load())

	repo := repocredentials.New()
	repoOwner := repocredentials.ResolveRuntimeCredentialParams{OwnerKind: repocredentials.OwnerKindDataSource, OrgSlug: org, DataSourceSlug: source}
	stored, err := repo.FindOAuthGrant(ctx, pool, repoOwner)
	require.NoError(t, err)
	claimed, err := repo.ClaimHTTPOAuthRefresh(ctx, pool, stored, "crashed-worker")
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, repo.ReleaseHTTPOAuthRefresh(ctx, pool, repoOwner, "wrong-worker"))
	claimed, err = repo.ClaimHTTPOAuthRefresh(ctx, pool, stored, "replacement-worker")
	require.NoError(t, err)
	require.False(t, claimed)
	_, err = pool.Exec(ctx, `UPDATE credential_http_oauth_apps app SET refresh_claim_expires_at = NOW() - INTERVAL '1 second' FROM organizations o WHERE o.id = app.organization_id AND o.slug = $1`, org)
	require.NoError(t, err)
	claimed, err = repo.ClaimHTTPOAuthRefresh(ctx, pool, stored, "replacement-worker")
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, repo.ReleaseHTTPOAuthRefresh(ctx, pool, repoOwner, "crashed-worker"))
	otherOwner := repoOwner
	otherOwner.OrgSlug = "unrelated-org"
	require.NoError(t, repo.ReleaseHTTPOAuthRefresh(ctx, pool, otherOwner, "replacement-worker"))
	claimed, err = repo.ClaimHTTPOAuthRefresh(ctx, pool, stored, "third-worker")
	require.NoError(t, err)
	require.False(t, claimed)
	require.NoError(t, repo.ReleaseHTTPOAuthRefresh(ctx, pool, repoOwner, "replacement-worker"))
	_, err = svc.Revoke(ctx, credentialssvc.RevokeParams{Owner: owner})
	require.NoError(t, err)
}

// Records a real database claim rejection while the first provider exchange is blocked.
type recordingHTTPOAuthClaimRepo struct {
	repocredentials.Repository
	blocked chan struct{}
}

func (r *recordingHTTPOAuthClaimRepo) ClaimHTTPOAuthRefresh(ctx context.Context, db postgres.DBTX, grant repocredentials.CredentialGrant, claimID string) (bool, error) {
	claimed, err := r.Repository.ClaimHTTPOAuthRefresh(ctx, db, grant, claimID)
	if err == nil && !claimed {
		select {
		case r.blocked <- struct{}{}:
		default:
		}
	}
	return claimed, err
}
