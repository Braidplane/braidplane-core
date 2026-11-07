package datasourcessvc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
)

func TestWorkspaceRequiresProviderOnboarding(t *testing.T) {
	svc := &service{}
	raw := json.RawMessage(`{}`)
	require.NoError(t, svc.validateProviderDataSourceConfig(model.DataSourceTypeGoogleWorkspace, raw))
	require.Equal(t, googleworkspace.SelectedFiles, workspaceConsentMode(model.DataSourceTypeGoogleWorkspace, raw))
	require.Equal(t, googleworkspace.SelectedFiles, workspaceConsentMode(model.DataSourceTypeGoogleWorkspace, []byte(`{"access_mode":"selected_files"}`)))
	_, err := svc.Create(t.Context(), slug.Slug("acme"), model.EventActor{}, &CreateParams{Type: model.DataSourceTypeGoogleWorkspace})
	require.ErrorContains(t, err, "OAuth onboarding")
	_, _, err = connector.NormalizeCustomerConfig(model.DataSourceTypeGoogleWorkspace, []byte(`{"spreadsheet_id":"sheet-1","access_token":"secret"}`))
	require.Error(t, err)
}

func TestWorkspaceSchemaCacheRoundTrip(t *testing.T) {
	schema, err := emptySchemaForType(model.DataSourceTypeGoogleWorkspace)
	require.NoError(t, err)
	require.Equal(t, SchemaTypeFile, schema.SchemaType())
	raw, err := json.Marshal(schema)
	require.NoError(t, err)
	decoded, err := decodeSchemaJSON(raw)
	require.NoError(t, err)
	require.Equal(t, schema, decoded)
}

func TestWorkspaceUpdateKeepsVerifiedResourceAndConsent(t *testing.T) {
	const current = `{"access_mode":"selected_files"}`
	for _, tc := range []struct {
		name, stored, candidate string
		valid                   bool
	}{
		{"unchanged", current, current, true},
		{"omitted mode", current, `{}`, true},
		{"different spreadsheet", current, `{"spreadsheet_id":"sheet-2","access_mode":"selected_files"}`, false},
		{"different mode", current, `{"access_mode":"invalid"}`, false},
		{"malformed stored", `{`, current, false},
		{"invalid stored", `{"spreadsheet_id":""}`, current, false},
		{"malformed candidate", current, `{`, false},
		{"invalid candidate", current, `{"spreadsheet_id":""}`, false},
		{"unknown key", current, `{"spreadsheet_id":"sheet-1","access_token":"synthetic"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			persisted := false
			repo := testutil.NewFakeDataSourceRepo()
			ds := &repods.DataSource{Slug: slug.Slug("sheet"), Type: model.DataSourceTypeGoogleWorkspace, Config: json.RawMessage(tc.stored), ReadOnly: true, Credential: &repods.CredentialMetadata{Status: "active", Kind: "oauth2_grant"}}
			repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, org, source slug.Slug) (*repods.DataSource, error) {
				require.Equal(t, slug.Slug("acme"), org)
				require.Equal(t, ds.Slug, source)
				return ds, nil
			}
			repo.UpdateFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug, params *repods.UpdateParams) (*repods.DataSource, error) {
				persisted = true
				var cfg googleworkspace.Config
				require.NoError(t, json.Unmarshal(params.ConfigJSON, &cfg))
				require.Equal(t, googleworkspace.SelectedFiles, cfg.ConsentMode())
				result := *ds
				result.Config = params.ConfigJSON
				return &result, nil
			}
			svc := newTestService(repo, testutil.NewFakeSchemaMetadataRepo(), testutil.NewFakeEncryptor(), testutil.NewFakeConnectorFactory().NewConnectorFn).(*service)
			creds := newFakeCredentialService()
			creds.credentials[ds.Slug] = []byte(`{"access_token":"synthetic"}`)
			svc.credentials = creds
			_, err := svc.Update(t.Context(), slug.Slug("acme"), ds.Slug, model.SystemEventActor(), &UpdateParams{ConfigJSON: json.RawMessage(tc.candidate), ConfigFromCustomer: true})
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, tc.valid, persisted)
		})
	}
}
