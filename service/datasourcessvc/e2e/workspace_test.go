package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/testutil/datasourcee2e"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/stretchr/testify/require"
)

type workspaceOAuthStub struct{ dataSourceOAuthProviderStub }

func (*workspaceOAuthStub) OAuthScopePlanForInstall(p credentialssvc.OAuthScopePlanParams) credentialssvc.OAuthScopePlan {
	scopes := []string{"openid", "email", "https://www.googleapis.com/auth/drive.file"}
	if p.IncludeWriteScopes {
		scopes[2] = "https://www.googleapis.com/auth/drive.file"
	}
	return credentialssvc.OAuthScopePlan{Required: scopes}
}

func (p *workspaceOAuthStub) ExchangeCode(_ context.Context, params credentialssvc.ExchangeCodeParams) (credentialssvc.OAuthTokenSet, error) {
	tokens := p.exchange
	tokens.Scopes = params.RequestedScopes
	return tokens, nil
}

func TestWorkspaceAccountConsentAtomicity(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "Workspace onboarding and consent atomicity")
	for _, mode := range []string{"selected_files"} {
		t.Run(mode, func(t *testing.T) {
			org := datasourcee2e.UniqueSlug(t, "workspace-org")
			source := datasourcee2e.UniqueSlug(t, "workspace-source")
			datasourcee2e.SeedOrganization(t, pool, org)
			provider := &workspaceOAuthStub{dataSourceOAuthProviderStub{authorizationURL: "https://google.test/consent", exchange: credentialssvc.OAuthTokenSet{AccessToken: "first", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour), Account: credentialssvc.ProviderAccount{ID: "account-1"}}}}
			stack := datasourcee2e.NewStack(t, pool, credentialssvc.ProviderRegistry{"google_workspace": provider})
			fail := false
			tests := 0
			svc := stack.ServiceWithConfig(t, datasourcee2e.ServiceConfig{ConnectionTester: datasourcee2e.ConnectionTestExecutorFunc(func(_ context.Context, request datasourcessvc.ConnectionTestRequest) error {
				tests++
				require.Equal(t, org, request.OrganizationSlug)
				if fail {
					return datasourcessvc.ErrConnectionFailed
				}
				return nil
			})})
			pending, err := svc.StartProviderDataSourceInstall(t.Context(), org, model.SystemEventActor(), "google_workspace", &datasourcessvc.StartProviderDataSourceInstallParams{Name: "Workspace", Slug: source, Type: model.DataSourceTypeGoogleWorkspace, ConfigJSON: []byte(`{"access_mode":"` + mode + `"}`)})
			require.NoError(t, err)
			require.False(t, pending.DataSource.IsActive)
			_, err = svc.CompleteProviderOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "google_workspace", State: pending.Install.State, Code: "first"})
			require.NoError(t, err)
			require.Equal(t, 1, tests)
			connected, err := svc.FindBySlug(t.Context(), org, source)
			require.NoError(t, err)
			require.True(t, connected.IsActive)
			require.Equal(t, credentialssvc.CredentialStatusActive, connected.Credential.Status)
			require.NotNil(t, connected.LastTestedAt)
			require.JSONEq(t, `{"access_mode":"`+mode+`"}`, string(connected.Config))
			otherMode := "selected_files"
			owner := credentialssvc.CredentialOwner{OrgSlug: org, DataSourceSlug: source}
			install, err := stack.Credentials.StartOAuthInstall(t.Context(), credentialssvc.StartOAuthInstallParams{Owner: owner, Provider: "google_workspace", Intent: credentialssvc.OAuthInstallIntentRotate, AccessMode: otherMode})
			require.NoError(t, err)
			before := stack.FindDataSource(t, org, source)
			oldCredential := before.Credential.Slug
			fail = true
			provider.exchange.Account.ID = "account-2"
			_, err = svc.CompleteProviderOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "google_workspace", State: install.State, Code: "failed"})
			require.ErrorIs(t, err, datasourcessvc.ErrConnectionFailed)
			after := stack.FindDataSource(t, org, source)
			require.True(t, after.IsActive)
			require.Equal(t, oldCredential, after.Credential.Slug)
			require.Equal(t, before.Config, after.Config)
			require.Equal(t, before.ConnectionTestRevision, after.ConnectionTestRevision)
			fail = false
			_, err = svc.CompleteProviderOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "google_workspace", State: install.State, Code: "retry"})
			require.NoError(t, err)
			after = stack.FindDataSource(t, org, source)
			require.True(t, after.IsActive)
			require.NotEqual(t, oldCredential, after.Credential.Slug)
			require.JSONEq(t, `{"access_mode":"`+otherMode+`"}`, string(after.Config))
			stale, err := stack.Credentials.StartOAuthInstall(t.Context(), credentialssvc.StartOAuthInstallParams{Owner: owner, Provider: "google_workspace", Intent: credentialssvc.OAuthInstallIntentRotate, AccessMode: otherMode})
			require.NoError(t, err)
			_, err = svc.Update(t.Context(), org, source, model.SystemEventActor(), &datasourcessvc.UpdateParams{Name: "Renamed"})
			require.NoError(t, err)
			_, err = svc.CompleteProviderOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "google_workspace", State: stale.State, Code: "stale"})
			require.ErrorIs(t, err, datasourcessvc.ErrConnectionTestConflict)
		})
	}
}
