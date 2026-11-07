package datasourcessvc

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

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

type workspaceTransport func(*http.Request) (*http.Response, error)

func (f workspaceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestWorkspaceDiscoveryUsesOrganizationCredentialAndTracing(t *testing.T) {
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	calls := 0
	http.DefaultTransport = workspaceTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "Bearer scoped-token", r.Header.Get("Authorization"))
		require.Equal(t, "continuation", r.URL.Query().Get("pageToken"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"files":[{"id":"book","name":"Budget"}],"nextPageToken":"more"}`)), Header: make(http.Header)}, nil
	})
	org, source := slug.Slug("acme"), slug.Slug("sheet")
	repo := testutil.NewFakeDataSourceRepo()
	repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, gotOrg, gotSource slug.Slug) (*repods.DataSource, error) {
		require.Equal(t, org, gotOrg)
		require.Equal(t, source, gotSource)
		return &repods.DataSource{Slug: source, Type: model.DataSourceTypeGoogleWorkspace, Config: []byte(`{"access_mode":"selected_files"}`)}, nil
	}
	svc := newTestService(repo, testutil.NewFakeSchemaMetadataRepo(), testutil.NewFakeEncryptor(), connector.NewConnectorWithTracing(connector.NewConnector)).(*service)
	creds := newFakeCredentialService()
	resolved := 0
	creds.resolveRuntimeFn = func(_ context.Context, p credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		resolved++
		require.Equal(t, org, p.Owner.OrgSlug)
		require.Equal(t, source, p.Owner.DataSourceSlug)
		return credentialssvc.RuntimeCredential{Kind: credentialssvc.CredentialKindOAuth2Grant, PlaintextJSON: []byte(`{"access_token":"scoped-token","granted_scopes":["https://www.googleapis.com/auth/drive.file"]}`)}, nil
	}
	svc.credentials = creds
	_, err := svc.ListGoogleWorkspaceFiles(t.Context(), org, source, workspace.ListOptions{PageToken: strings.Repeat("x", 2049)})
	require.Error(t, err)
	require.Zero(t, resolved)
	found, err := svc.ListGoogleWorkspaceFiles(t.Context(), org, source, workspace.ListOptions{PageToken: "continuation"})
	require.NoError(t, err)
	require.Len(t, found.Files, 1)
	require.Equal(t, "book", found.Files[0].FileID)
	require.Equal(t, "more", found.NextPageToken)
	require.Equal(t, 1, resolved)
	require.Equal(t, 1, calls)
	creds.resolveRuntimeFn = func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
		return credentialssvc.RuntimeCredential{}, ErrConnectionFailed
	}
	_, err = svc.ListGoogleWorkspaceFiles(t.Context(), org, source, workspace.ListOptions{PageToken: "continuation"})
	require.ErrorIs(t, err, ErrConnectionFailed)
	require.Equal(t, 1, calls)
}

func TestWorkspaceIntrospectionThroughProductionTracingFactory(t *testing.T) {
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	http.DefaultTransport = workspaceTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"files":[{"id":"book","name":"Budget","mimeType":"application/vnd.google-apps.spreadsheet","isAppAuthorized":true}]}`)), Header: make(http.Header)}, nil
	})
	request := SchemaIntrospectionRequest{DataSourceType: model.DataSourceTypeGoogleWorkspace, ConfigJSON: []byte(`{}`), CredentialsJSON: []byte(`{"access_token":"synthetic","granted_scopes":["https://www.googleapis.com/auth/drive.file"]}`), MaxResultBytes: 1024, MaxResultItems: 1}
	encoded, err := ExecuteSchemaIntrospection(t.Context(), connector.NewConnectorWithTracing(connector.NewConnector), request)
	require.NoError(t, err)
	schema, err := decodeSchemaJSON(encoded)
	require.NoError(t, err)
	metadata, ok := schema.(Schema[FileSchemaPayload])
	require.True(t, ok)
	require.Equal(t, "book", metadata.Payload.Files[0].ResourceID)
	require.Equal(t, "Budget", metadata.Payload.Files[0].Name)
	request.MaxResultBytes = 10
	_, err = ExecuteSchemaIntrospection(t.Context(), connector.NewConnectorWithTracing(connector.NewConnector), request)
	require.Error(t, err)
}
