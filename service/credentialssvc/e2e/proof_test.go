package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/stretchr/testify/require"
)

func TestProofCredentialRequiresExactActiveTenantVersion(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres Proof credential E2E test in short mode")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "Proof credential resolution")
	svc := newCredentialService(t, pool, time.Now().UTC(), &fakeProviderAdapter{})
	org := uniqueCredentialE2ESlug("proof-cred-org")
	ds := uniqueCredentialE2ESlug("proof-cred-ds")
	seedCredentialLifecycleDataSource(t, pool, org, ds)
	params := credentialssvc.CreateStaticSecretParams{
		Owner: credentialOwner(org, ds), CredentialType: credentialssvc.StaticCredentialTypeHubSpot,
		PlaintextJSON: []byte(`{"private_app_token":"proof-fixture"}`),
	}
	credential, err := svc.CreateStaticSecret(ctx, params)
	require.NoError(t, err)
	err = postgres.NewTransactionManager(pool).RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		_, err := repods.New().UpdateCredentials(ctx, tx, org, ds, &repods.UpdateCredentialsParams{CredentialSlug: credential.Slug})
		return err
	})
	require.NoError(t, err)
	runtime, err := svc.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{Owner: params.Owner})
	require.NoError(t, err)
	r := testutil.ProofEvidenceRequest()
	r.OrganizationSlug, r.Candidate.OrganizationSlug = org, org
	r.DataSourceSlug, r.CredentialDataSourceSlug = ds, ds
	r.ConnectorType, r.Operation = model.DataSourceTypeHubSpot, "hubspot.contact_metadata"
	r.Target.Host = "api.hubapi.com"
	r.Egress = []proofevidence.Endpoint{r.Target}
	r.CredentialVersion = runtime.Version
	grant := testutil.AdmitProofEvidence(t, r)
	resolved, err := svc.ResolveProofCredential(ctx, grant)
	require.NoError(t, err)
	require.Equal(t, runtime.Version, resolved.Version)
	require.JSONEq(t, string(params.PlaintextJSON), string(resolved.PlaintextJSON))
	for _, scenario := range []string{"OtherTenant", "OtherVersion"} {
		t.Run(scenario, func(t *testing.T) {
			request := r
			if scenario == "OtherTenant" {
				request.OrganizationSlug = uniqueCredentialE2ESlug("other-proof-org")
				request.Candidate.OrganizationSlug = request.OrganizationSlug
			} else {
				request.CredentialVersion = "another-version"
			}
			denied, err := svc.ResolveProofCredential(ctx, testutil.AdmitProofEvidence(t, request))
			require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
			require.Empty(t, denied.PlaintextJSON)
		})
	}
	replacement, err := svc.CreateStaticSecret(ctx, params)
	require.NoError(t, err)
	err = postgres.NewTransactionManager(pool).RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		_, err := repods.New().UpdateCredentials(ctx, tx, org, ds, &repods.UpdateCredentialsParams{CredentialSlug: replacement.Slug})
		return err
	})
	require.NoError(t, err)
	denied, err := svc.ResolveProofCredential(ctx, grant)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	require.Empty(t, denied.PlaintextJSON)
}

func TestOAuthRefreshInvalidatesAdmittedProofCredential(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires real Postgres")
	}
	for _, providerName := range []string{"bigquery", "hubspot"} {
		t.Run(providerName, func(t *testing.T) {
			ctx := t.Context()
			pool := e2edb.OpenPostgresPool(t, "OAuth refresh Proof invalidation")
			now := time.Now().UTC()
			provider := &fakeProviderAdapter{
				authURL:  "https://provider.example/oauth",
				exchange: credentialssvc.OAuthTokenSet{AccessToken: "initial-access", RefreshToken: "fixture-refresh", ExpiresAt: now.Add(time.Hour), Scopes: []string{"metadata.read"}},
				refresh:  credentialssvc.OAuthTokenSet{AccessToken: "refreshed-access", ExpiresAt: now.Add(2 * time.Hour), Scopes: []string{"metadata.read"}},
			}
			enc, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": []byte("0123456789abcdef0123456789abcdef")}, []byte("0123456789abcdef0123456789abcdef"))
			require.NoError(t, err)
			svc, err := credentialssvc.New(postgres.NewTransactionManager(pool), repocredentials.New(), enc, credentialssvc.ProviderRegistry{providerName: provider}, testutil.FakeClock{NowFn: func() time.Time { return now }}, false)
			require.NoError(t, err)
			org, ds := uniqueCredentialE2ESlug("proof-refresh-org"), uniqueCredentialE2ESlug("proof-refresh-ds")
			seedCredentialLifecycleDataSource(t, pool, org, ds)
			owner := credentialOwner(org, ds)
			install, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{Owner: owner, Provider: providerName, RequestedScopes: []string{"metadata.read"}, Intent: credentialssvc.OAuthInstallIntentRotate})
			require.NoError(t, err)
			completed, err := svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{Provider: providerName, State: install.State, Code: "fixture-code"})
			require.NoError(t, err)
			before, err := svc.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
			require.NoError(t, err)
			request := testutil.ProofEvidenceRequest()
			request.OrganizationSlug, request.Candidate.OrganizationSlug = org, org
			request.DataSourceSlug, request.CredentialDataSourceSlug = ds, ds
			if providerName == "hubspot" {
				request.ConnectorType, request.Operation = model.DataSourceTypeHubSpot, "hubspot.contact_metadata"
				request.Target.Host = "api.hubapi.com"
				request.Egress = []proofevidence.Endpoint{request.Target}
			}
			request.CredentialVersion = before.Version
			admitted := testutil.AdmitProofEvidence(t, request)
			_, err = svc.ResolveProofCredential(ctx, admitted)
			require.NoError(t, err)
			refreshed, err := svc.Refresh(ctx, credentialssvc.RefreshGrantParams{Owner: owner})
			require.NoError(t, err)
			require.Equal(t, completed.Credential.Slug, refreshed.Slug)
			after, err := svc.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
			require.NoError(t, err)
			require.NotEqual(t, before.Version, after.Version)
			require.ErrorIs(t, svc.CheckProofCredential(ctx, admitted), proofevidence.ErrAuthorizationDenied)
			denied, err := svc.ResolveProofCredential(ctx, admitted)
			require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
			require.Empty(t, denied.PlaintextJSON)
			require.Equal(t, before.Version, admitted.Request().CredentialVersion)
			request.CredentialVersion = after.Version
			resolved, err := svc.ResolveProofCredential(ctx, testutil.AdmitProofEvidence(t, request))
			require.NoError(t, err)
			require.Equal(t, after.Version, resolved.Version)
			require.Equal(t, 1, provider.refreshCalls, "Proof must never refresh credentials itself")
		})
	}
}
