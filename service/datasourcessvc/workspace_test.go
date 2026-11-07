package datasourcessvc

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceVerificationDoesNotChangeConnectionAndRejectsStaleRequests(t *testing.T) {
	for _, failure := range []string{"", "inaccessible", "stale", "changed"} {
		t.Run(failure, func(t *testing.T) {
			org, source := slug.Slug("acme"), slug.Slug("workspace")
			ds := &repods.DataSource{Slug: source, Type: model.DataSourceTypeGoogleWorkspace, Config: []byte(`{"access_mode":"selected_files"}`), ConnectionTestRevision: 7, ReadOnly: true}
			repo := testutil.NewFakeDataSourceRepo()
			repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, o, d slug.Slug) (*repods.DataSource, error) {
				require.Equal(t, org, o)
				require.Equal(t, source, d)
				return ds, nil
			}
			writes := 0
			repo.UpdateFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *repods.UpdateParams) (*repods.DataSource, error) {
				writes++
				return ds, nil
			}
			svc := newTestService(repo, testutil.NewFakeSchemaMetadataRepo(), testutil.NewFakeEncryptor(), nil).(*service)
			creds := newFakeCredentialService()
			creds.credentials[source] = []byte(`{"access_token":"synthetic","granted_scopes":["https://www.googleapis.com/auth/drive.file"]}`)
			svc.credentials = creds
			original := http.DefaultTransport
			defer func() { http.DefaultTransport = original }()
			calls := 0
			http.DefaultTransport = workspaceTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if failure == "changed" {
					ds.ConnectionTestRevision++
				}
				status := 200
				if failure == "inaccessible" {
					status = 403
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"chosen","name":"Fixture","mimeType":"application/json","isAppAuthorized":true}`))}, nil
			})
			revision := int64(7)
			if failure == "stale" {
				revision = 6
			}
			found, err := svc.VerifyGoogleWorkspaceFiles(t.Context(), org, source, model.SystemEventActor(), WorkspaceVerificationParams{FileIDs: []string{"chosen"}, ExpectedRevision: revision})
			if failure == "" {
				require.NoError(t, err)
				require.Equal(t, "chosen", found.Files[0].FileID)
			} else {
				require.Error(t, err)
			}
			if failure == "stale" {
				require.Zero(t, calls)
			}
			require.Zero(t, writes)
			require.JSONEq(t, `{"access_mode":"selected_files"}`, string(ds.Config))
		})
	}
}

func TestWorkspaceConsentFailurePreservesUsableConfig(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "inaccessible new account", true: "stale callback"}[stale], func(t *testing.T) {
			org, source := slug.Slug("acme"), slug.Slug("sheet")
			ds := &repods.DataSource{Slug: source, Type: model.DataSourceTypeGoogleWorkspace, Config: []byte(`{"access_mode":"selected_files"}`), ConnectionTestRevision: 3, IsActive: true}
			repo := testutil.NewFakeDataSourceRepo()
			repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) { return ds, nil }
			svc := newTestService(repo, testutil.NewFakeSchemaMetadataRepo(), testutil.NewFakeEncryptor(), func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
				return &testutil.FakeSQLConnector{TestConnectionFn: func(context.Context) error { return ErrConnectionFailed }}, nil
			}).(*service)
			rev := int64(3)
			if stale {
				rev = 2
			}
			err := svc.validateWorkspaceConsent(t.Context(), credentialssvc.CredentialInstall{Owner: credentialssvc.CredentialOwner{OrgSlug: org, DataSourceSlug: source}, ExpectedSourceRevision: &rev, RequestedScopes: []string{"https://www.googleapis.com/auth/drive.file"}}, credentialssvc.OAuthTokenSet{AccessToken: "new-account"})
			require.Error(t, err)
			require.True(t, ds.IsActive)
			require.JSONEq(t, `{"access_mode":"selected_files"}`, string(ds.Config))
		})
	}
}

func TestWorkspacePendingContractIsSeparateFromExecutableConfig(t *testing.T) {
	require.NoError(t, decodeWorkspacePending([]byte(`{}`)))
	require.NoError(t, decodeWorkspacePending([]byte(`{"access_mode":"selected_files"}`)))
	for _, raw := range []string{`null`, `{"spreadsheet_id":"book"}`, `{"access_mode":"invalid"}`, `{"access_token":"token"}`, `{} {}`} {
		require.Error(t, decodeWorkspacePending([]byte(raw)))
	}
}

type workspaceConsentTransactionManager struct {
	testutil.FakeTransactionManager
	inTransaction bool
}

func (m *workspaceConsentTransactionManager) RunInTx(ctx context.Context, fn func(context.Context, postgres.DBTX) error) error {
	m.inTransaction = true
	defer func() { m.inTransaction = false }()
	return fn(ctx, nil)
}

func TestWorkspaceConsentVerifiesOutsideTransactionAndRechecksRevision(t *testing.T) {
	for _, test := range []struct {
		name     string
		changed  bool
		readOnly bool
	}{
		{name: "read-only", readOnly: true},
		{name: "read/write"},
		{name: "changed during verification", changed: true, readOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			org, source := slug.Slug("acme"), slug.Slug("sheet")
			ds := &repods.DataSource{Name: "Test Workspace", Slug: source, Type: model.DataSourceTypeGoogleWorkspace, Config: []byte(`{"access_mode":"selected_files"}`), ConnectionTestRevision: 7, IsActive: true, ReadOnly: test.readOnly}
			repo := testutil.NewFakeDataSourceRepo()
			repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) { return ds, nil }
			txm := &workspaceConsentTransactionManager{}
			tests, writes := 0, 0
			factory := func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
				return &testutil.FakeSQLConnector{TestConnectionFn: func(context.Context) error {
					tests++
					require.False(t, txm.inTransaction)
					if test.changed {
						ds.ConnectionTestRevision++
					}
					return nil
				}}, nil
			}
			repo.UpdateFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug, params *repods.UpdateParams) (*repods.DataSource, error) {
				require.True(t, txm.inTransaction)
				require.Equal(t, 1, tests)
				require.Equal(t, ds.Name, params.Name)
				require.Equal(t, ds.ReadOnly, params.ReadOnly)
				writes++
				ds.Config = params.ConfigJSON
				ds.ConnectionTestRevision++
				return ds, nil
			}
			repo.MarkTestSuccessFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
				return ds, nil
			}
			schema := testutil.NewFakeSchemaMetadataRepo()
			schema.ResetImportBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.SchemaImportState, error) {
				return &repods.SchemaImportState{}, nil
			}
			svc := newTestService(repo, schema, testutil.NewFakeEncryptor(), factory).(*service)
			svc.txm = txm
			activations := 0
			svc.admission = &activationPolicy{activate: func(_ context.Context, gotOrg, gotSource slug.Slug, at time.Time) error {
				require.True(t, txm.inTransaction)
				require.Equal(t, org, gotOrg)
				require.Equal(t, source, gotSource)
				require.False(t, at.IsZero())
				activations++
				return nil
			}}

			revision := int64(7)
			install := credentialssvc.CredentialInstall{Owner: credentialssvc.CredentialOwner{OrgSlug: org, DataSourceSlug: source}, ExpectedSourceRevision: &revision, RequestedScopes: []string{"https://www.googleapis.com/auth/drive.file"}}
			tokens := credentialssvc.OAuthTokenSet{AccessToken: "candidate"}
			require.NoError(t, svc.validateWorkspaceConsent(t.Context(), install, tokens))
			require.Zero(t, writes)
			err := svc.finalizeWorkspaceConsent(t.Context(), install, tokens)
			if test.changed {
				require.ErrorIs(t, err, ErrConnectionTestConflict)
				require.Zero(t, writes)
				require.Zero(t, activations)
			} else {
				require.NoError(t, err)
				require.Equal(t, 1, writes)
				require.Equal(t, 1, activations)
				require.JSONEq(t, `{"access_mode":"selected_files"}`, string(ds.Config))
			}
			require.Equal(t, 1, tests)
		})
	}
}

func TestWorkspaceDiscoveryRequiresUsableGrant(t *testing.T) {
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return &repods.DataSource{Type: model.DataSourceTypeGoogleWorkspace, Config: []byte(`{"access_mode":"selected_files"}`)}, nil
	}
	svc := newTestService(repo, testutil.NewFakeSchemaMetadataRepo(), testutil.NewFakeEncryptor(), nil).(*service)
	creds := newFakeCredentialService()
	calls := 0
	creds.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		calls++
		return credentialssvc.RuntimeCredential{}, ErrConnectionFailed
	}
	svc.credentials = creds
	_, err := svc.ListGoogleWorkspaceFiles(t.Context(), slug.Slug("acme"), slug.Slug("sheet"), workspace.ListOptions{})
	require.ErrorIs(t, err, ErrConnectionFailed)
	require.Equal(t, 1, calls)
}

func TestPickerConfigurationRequiresActiveSelectedFileGrantWithoutResolvingTokens(t *testing.T) {
	for _, failure := range []string{"", "legacy config", "legacy grant", "mixed grant", "revoked", "inactive", "missing settings", "wrong type"} {
		t.Run(failure, func(t *testing.T) {
			org, source := slug.Slug("acme"), slug.Slug("workspace")
			ds := &repods.DataSource{Slug: source, Type: model.DataSourceTypeGoogleWorkspace, Config: []byte(`{"access_mode":"selected_files"}`), IsActive: true, ConnectionTestRevision: 7, Credential: &repods.CredentialMetadata{Kind: "oauth2_grant", Status: "active", Provider: "google_workspace", Scopes: []string{"openid", "email", workspace.FileScope}, Account: repods.ProviderAccount{ID: "connected", Email: "connected@example.com"}}}
			switch failure {
			case "legacy config":
				ds.Config = []byte(`{"access_mode":"all_files"}`)
			case "legacy grant":
				ds.Credential.Scopes = []string{"openid", "email", "https://www.googleapis.com/auth/drive"}
			case "mixed grant":
				ds.Credential.Scopes = append(ds.Credential.Scopes, "https://www.googleapis.com/auth/drive.readonly")
			case "revoked":
				ds.Credential.Status = "revoked"
			case "inactive":
				ds.IsActive = false
			case "wrong type":
				ds.Type = model.DataSourceTypeBigQuery
			}
			repo := testutil.NewFakeDataSourceRepo()
			repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, gotOrg, gotSource slug.Slug) (*repods.DataSource, error) {
				require.Equal(t, org, gotOrg)
				require.Equal(t, source, gotSource)
				return ds, nil
			}
			svc := newTestService(repo, testutil.NewFakeSchemaMetadataRepo(), testutil.NewFakeEncryptor(), nil).(*service)
			svc.googleWorkspacePicker = WorkspacePickerSettings{ClientID: "client", DeveloperKey: "restricted-public-key", AppID: "123"}
			if failure == "missing settings" {
				svc.googleWorkspacePicker = WorkspacePickerSettings{}
			}
			creds := newFakeCredentialService()
			creds.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
				t.Fatal("Picker config resolved backend tokens")
				return credentialssvc.RuntimeCredential{}, nil
			}
			svc.credentials = creds
			result, err := svc.GetGoogleWorkspacePicker(t.Context(), org, source)
			if failure != "" {
				require.Error(t, err)
				require.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.Equal(t, "connected@example.com", result.AccountEmail)
				require.EqualValues(t, 7, result.ExpectedRevision)
				require.Equal(t, svc.googleWorkspacePicker, result.WorkspacePickerSettings)
			}
		})
	}
}
