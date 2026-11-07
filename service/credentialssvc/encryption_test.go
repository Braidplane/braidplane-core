package credentialssvc

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/stretchr/testify/require"
)

func realCredentialTestCipher(t *testing.T, current string) *crypto.CredentialKeyring {
	t.Helper()
	ring, err := crypto.NewCredentialKeyring(current, map[string][]byte{
		"old": bytes.Repeat([]byte{2}, 32), "current": bytes.Repeat([]byte{3}, 32),
	}, nil)
	require.NoError(t, err)
	return ring
}

// This repository returns the selected stored row so swaps exercise service context construction.
type encryptionStaticRepo struct {
	*credentialRepo
	record repocredentials.CredentialRecord
}

func (r *encryptionStaticRepo) FindStaticSecretByDataSource(context.Context, postgres.DBTX, repocredentials.ResolveRuntimeCredentialParams) (repocredentials.CredentialRecord, error) {
	return r.record, nil
}

func (r *encryptionStaticRepo) FindStaticSecretByEnvironmentSecret(context.Context, postgres.DBTX, repocredentials.ResolveRuntimeCredentialParams) (repocredentials.CredentialRecord, error) {
	return r.record, nil
}

func TestEncryptionStaticResolutionBindsStoredTenantAndRecord(t *testing.T) {
	t.Parallel()
	for _, owner := range []CredentialOwner{testOwner(), testEnvironmentSecretOwner()} {
		t.Run(owner.Kind.String(), func(t *testing.T) {
			repo := &encryptionStaticRepo{credentialRepo: newCredentialRepo()}
			kind := StaticCredentialTypePostgres
			if owner.Kind == CredentialOwnerKindEnvironmentSecret {
				kind = StaticCredentialTypeEnvironmentSecret
			}
			svc, err := New(testutil.FakeTransactionManager{}, repo, realCredentialTestCipher(t, "current"), nil, testutil.FakeClock{NowFn: time.Now}, false)
			require.NoError(t, err)
			plaintext := []byte(`{"value":"synthetic-static-secret"}`)
			_, err = svc.CreateStaticSecret(t.Context(), CreateStaticSecretParams{Owner: owner, CredentialType: kind, PlaintextJSON: plaintext})
			require.NoError(t, err)
			repo.record = repo.staticSecrets[0]
			got, err := svc.ResolveRuntimeCredential(t.Context(), ResolveRuntimeCredentialParams{Owner: owner})
			require.NoError(t, err)
			require.Equal(t, plaintext, got.PlaintextJSON)
			for _, change := range []func(*repocredentials.CredentialRecord){
				func(r *repocredentials.CredentialRecord) { r.OrgSlug = "other-tenant" },
				func(r *repocredentials.CredentialRecord) { r.Slug = "other-credential" },
			} {
				repo.record = repo.staticSecrets[0]
				change(&repo.record)
				got, err = svc.ResolveRuntimeCredential(t.Context(), ResolveRuntimeCredentialParams{Owner: owner})
				require.ErrorIs(t, err, crypto.ErrCredentialCiphertext)
				require.Empty(t, got.PlaintextJSON)
			}
		})
	}
}

func TestEncryptionWebhookResolutionBindsRecordAndPurpose(t *testing.T) {
	t.Parallel()
	repo := newCredentialRepo()
	ring := realCredentialTestCipher(t, "current")
	svc, err := New(testutil.FakeTransactionManager{}, repo, ring, nil, testutil.FakeClock{NowFn: time.Now}, false)
	require.NoError(t, err)
	params := PipelineWebhookSecretParams{OrgSlug: "acme", ProjectSlug: "project", EnvironmentSlug: "prod", TriggerSlug: "incoming"}
	issued, err := svc.IssuePipelineWebhookSecret(t.Context(), params)
	require.NoError(t, err)
	repo.resolvedWebhookCredential = repo.staticSecrets[0]
	resolve := ResolvePipelineWebhookSecretParams{OrgSlug: params.OrgSlug, ProjectSlug: params.ProjectSlug, EnvironmentSlug: params.EnvironmentSlug, TriggerSlug: params.TriggerSlug, KeyVersion: issued.KeyVersion}
	got, err := svc.ResolvePipelineWebhookSecret(t.Context(), resolve)
	require.NoError(t, err)
	require.Equal(t, issued.Value, string(got))
	repo.resolvedWebhookCredential.Slug = "other-credential"
	got, err = svc.ResolvePipelineWebhookSecret(t.Context(), resolve)
	require.ErrorIs(t, err, crypto.ErrCredentialCiphertext)
	require.Empty(t, got)
	repo.resolvedWebhookCredential = repo.staticSecrets[0]
	repo.resolvedWebhookCredential.EncryptedPayload, err = ring.Seal(credentialScope(params.OrgSlug, repo.resolvedWebhookCredential.Slug, "static-secret"), []byte(issued.Value))
	require.NoError(t, err)
	got, err = svc.ResolvePipelineWebhookSecret(t.Context(), resolve)
	require.ErrorIs(t, err, crypto.ErrCredentialCiphertext)
	require.Empty(t, got)
}

func TestEncryptionOAuthRefreshKeepsStoredSlugAndWritesCurrentKey(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	repo := newCredentialRepo()
	provider := &fakeProviderAdapter{refresh: OAuthTokenSet{AccessToken: "synthetic-new-access", RefreshToken: "synthetic-new-refresh", ExpiresAt: now.Add(time.Hour)}}
	svc, err := New(testutil.FakeTransactionManager{}, repo, realCredentialTestCipher(t, "old"), ProviderRegistry{"fakecrm": provider}, testutil.FakeClock{NowFn: func() time.Time { return now }}, false)
	require.NoError(t, err)
	internal := svc.(*service)
	grant, err := internal.grantFromTokenSet(testOwner(), "fakecrm", OAuthTokenSet{AccessToken: "synthetic-old-access", RefreshToken: "synthetic-old-refresh", ExpiresAt: now.Add(-time.Minute)}, now, slug.Slug("stored-grant"))
	require.NoError(t, err)
	repo.grant = toRepoCredentialGrant(grant)
	internal.enc = realCredentialTestCipher(t, "current")
	got, err := svc.ResolveRuntimeCredential(t.Context(), ResolveRuntimeCredentialParams{Owner: testOwner()})
	require.NoError(t, err)
	require.Contains(t, string(got.PlaintextJSON), "synthetic-new-access")
	require.Equal(t, slug.Slug("stored-grant"), repo.grant.Slug)
	require.Equal(t, 1, provider.refreshCalls)
	currentOnly, err := crypto.NewCredentialKeyring("current", map[string][]byte{"current": bytes.Repeat([]byte{3}, 32)}, nil)
	require.NoError(t, err)
	_, err = currentOnly.Open(credentialScope(testOwner().OrgSlug, repo.grant.Slug, "oauth-token-set"), repo.grant.EncryptedTokenSet)
	require.NoError(t, err)
	internal.enc = currentOnly
	got, err = svc.ResolveRuntimeCredential(t.Context(), ResolveRuntimeCredentialParams{Owner: testOwner()})
	require.NoError(t, err)
	require.Contains(t, string(got.PlaintextJSON), "synthetic-new-access")
	require.Equal(t, 1, provider.refreshCalls)
}

func TestEncryptionHTTPOAuthSwapsFailBeforeProviderUse(t *testing.T) {
	calls := 0
	svc, repo, app := newHTTPTestService(t, func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }, "authorization_code")
	svc.enc = realCredentialTestCipher(t, "current")
	owner := testOwner()
	require.NoError(t, svc.ConfigureHTTPOAuthApp(t.Context(), ConfigureHTTPOAuthAppParams{Owner: owner, App: app}))
	other := owner
	other.DataSourceSlug = "other-source"
	repo.httpApps[toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: other})] = repo.httpApps[toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: owner})]
	_, err := svc.ConnectHTTPOAuth(t.Context(), StartOAuthInstallParams{Owner: other, Provider: "http"})
	require.ErrorIs(t, err, crypto.ErrCredentialCiphertext)
	require.Zero(t, calls)

	first, err := svc.StartOAuthInstall(t.Context(), StartOAuthInstallParams{Owner: owner, Provider: "http"})
	require.NoError(t, err)
	_, err = svc.StartOAuthInstall(t.Context(), StartOAuthInstallParams{Owner: owner, Provider: "http"})
	require.NoError(t, err)
	require.Len(t, repo.installs, 2)
	repo.installs[0].EncryptedPKCEVerifier = repo.installs[1].EncryptedPKCEVerifier
	_, err = svc.CompleteOAuthCallback(t.Context(), CompleteOAuthCallbackParams{Provider: "http", State: first.State, Code: "synthetic-code", Issuer: app.Config.Issuer})
	require.ErrorIs(t, err, crypto.ErrCredentialCiphertext)
	require.Zero(t, calls)
	require.Empty(t, repo.grants)
}
