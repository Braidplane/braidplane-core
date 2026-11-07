package views

import (
	"bytes"
	"context"
	"testing"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestDataSourceReadinessMatchesInventoryEvidence(t *testing.T) {
	tested := time.Now()
	failure := "Connection refused"
	blank := " \t "
	for _, tt := range []struct {
		name       string
		active     bool
		tested     *time.Time
		problem    *string
		failed     bool
		credential *api.CredentialMetadata
		want       string
	}{
		{name: "tested", active: true, tested: &tested, want: "ready"},
		{name: "untested", active: true, want: "untested"},
		{name: "blank error", active: true, tested: &tested, problem: &blank, want: "ready"},
		{name: "inactive", want: "inactive"},
		{name: "failed inactive", problem: &failure, failed: true, want: "test failed"},
		{name: "pending", credential: &api.CredentialMetadata{Status: api.CredentialStatusPending}, want: "authorization pending"},
		{name: "pending with prior failure", active: true, tested: &tested, problem: &failure, failed: true, credential: &api.CredentialMetadata{Status: api.CredentialStatusPending}, want: "authorization pending"},
		{name: "revoked", active: true, tested: &tested, credential: &api.CredentialMetadata{Status: api.CredentialStatusRevoked}, want: "authorization unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ds := api.DataSource{IsActive: tt.active, LastTestedAt: tt.tested, TestError: tt.problem, Credential: tt.credential}
			require.Equal(t, tt.want, DataSourceStatusLabel(ds))
			ready := tt.want == "ready"
			require.Equal(t, ready, DataSourceMatchesListFilter(ds, DataSourceListFilterReady))
			require.Equal(t, !ready, DataSourceMatchesListFilter(ds, DataSourceListFilterAttention))
			require.Equal(t, ready, DataSourceReadyCount([]api.DataSource{ds}) == 1)
			require.Equal(t, !ready, DataSourceAttentionCount([]api.DataSource{ds}) == 1)
			// Stored test evidence remains searchable regardless of current authorization.
			require.Equal(t, tt.failed, DataSourceMatchesListFilter(ds, DataSourceListFilterFailed))
			require.Equal(t, tt.tested == nil, DataSourceMatchesListFilter(ds, DataSourceListFilterUntested))
		})
	}
}

func TestDataSourceQuickLookRecoveryPreservesCatalogContext(t *testing.T) {
	failure := "Safe connection failure"
	ds := &api.DataSource{Name: "Reports", Slug: "reports", Type: api.DataSourceTypeGitlab, TestError: &failure}
	ctx := WithEntityPageURL(context.Background(), "/orgs/acme/data-sources?filter=attention&cursor=next&selected=reports")
	var out bytes.Buffer
	require.NoError(t, DataSourceDetailPanelSlot("acme", "dev", ds, DataSourceSchemaPanelData{Status: api.DataSourceSchemaStatusFailed}, nil, DataSourceUpdateFormData{ProviderSelectionPending: true}, NewOrganizationPermissions("datasources:update"), "", "").Render(ctx, &out))
	html := out.String()
	require.Contains(t, html, "Choose repository")
	require.Contains(t, html, "Review schema")
	require.Contains(t, html, "section=settings")
	require.Contains(t, html, "section=schema")
	require.Contains(t, html, "list_return=")
	require.Contains(t, html, "filter%3Dattention")
	require.Contains(t, html, "cursor%3Dnext")
	require.NotContains(t, html, "<form")
	require.NotContains(t, html, "<select")
}

func TestDataSourceOperationsPreserveProofTargetAndPermissions(t *testing.T) {
	ds := &api.DataSource{Name: "Reports", Slug: "reports", Type: api.DataSourceTypeGitlab}
	ctx := WithEntityPageURL(context.Background(), "/orgs/acme/data-sources/reports?section=operations")
	var out bytes.Buffer
	require.NoError(t, DataSourceDetailPanelContent("acme", "dev", ds, DefaultDataSourceSchemaPanelData(), nil, DataSourceUpdateFormData{}, NewOrganizationPermissions("pipelines:read"), DataSourceOperationsPanelData{}).Render(ctx, &out))
	require.Contains(t, out.String(), "/orgs/acme/data-sources/reports/proof?environment_slug=dev")
	require.Contains(t, out.String(), "Query access required")
	require.NotContains(t, out.String(), ">Open queries</a>")
}

func TestDataSourceQuickLookIgnoresWhitespaceTestErrors(t *testing.T) {
	blank := " \t "
	tested := time.Now()
	for _, credential := range []*api.CredentialMetadata{nil, {Status: api.CredentialStatusPending}} {
		ds := &api.DataSource{Name: "Warehouse", Slug: "warehouse", Type: api.DataSourceTypePostgres, IsActive: true, LastTestedAt: &tested, TestError: &blank, Credential: credential}
		var out bytes.Buffer
		require.NoError(t, DataSourceDetailPanelSlot("acme", "dev", ds, DefaultDataSourceSchemaPanelData(), nil, DataSourceUpdateFormData{}, OrganizationPermissions{}, "", "").Render(context.Background(), &out))
		require.NotContains(t, out.String(), "Last test failed")
		require.NotContains(t, out.String(), "Review connection settings")
	}
}

func TestSchemaBrowserPollsOnlyWhileImporting(t *testing.T) {
	for _, status := range []api.DataSourceSchemaStatus{api.DataSourceSchemaStatusNotImported, api.DataSourceSchemaStatusImporting, api.DataSourceSchemaStatusReady, api.DataSourceSchemaStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			source := &api.DataSource{Name: "Warehouse", Slug: "warehouse", Type: api.DataSourceTypePostgres}
			panel := DataSourceSchemaPanelData{Status: status, Title: "Schema import completed", Lines: []string{"No tables discovered."}}
			if status == api.DataSourceSchemaStatusFailed {
				panel.Error = "Schema access denied."
			}
			ctx := WithEntityPageURL(context.Background(), "/orgs/acme/data-sources/warehouse?section=schema&environment_slug=dev")
			var out bytes.Buffer
			require.NoError(t, DataSourceDetailPanelContent("acme", "dev", source, panel, nil, DataSourceUpdateFormData{}, NewOrganizationPermissions("datasources:update"), DataSourceOperationsPanelData{}).Render(ctx, &out))
			html := out.String()
			if status == api.DataSourceSchemaStatusImporting {
				require.Contains(t, html, `hx-get="/orgs/acme/data-sources/warehouse?environment_slug=dev&amp;section=schema"`)
				require.Contains(t, html, `hx-trigger="every 2s"`)
				require.Contains(t, html, `hx-select="#data-source-schema-browser"`)
				require.Contains(t, html, "This panel updates automatically.")
				require.Contains(t, html, `disabled>Importing…</button>`)
				require.NotContains(t, html, "No tables discovered.")
				require.NotContains(t, html, "Schema import completed")
			} else {
				require.NotContains(t, html, `hx-trigger="every 2s"`)
				if status == api.DataSourceSchemaStatusFailed {
					require.Contains(t, html, "Schema access denied.")
					require.Contains(t, html, "Retry import")
				}
			}
		})
	}
}
