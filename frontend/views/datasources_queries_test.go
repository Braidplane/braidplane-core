package views

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestDataSourceOverviewOffersFirstResultForReadySupportedConnection(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, tc := range []struct {
		name        string
		typeName    api.DataSourceType
		active      bool
		permissions OrganizationPermissions
		want        bool
	}{
		{"ready", api.DataSourceTypePostgres, true, NewOrganizationPermissions("queries:read"), true},
		{"inactive", api.DataSourceTypePostgres, false, NewOrganizationPermissions("queries:read"), false},
		{"S3", api.DataSourceTypeS3, true, NewOrganizationPermissions("queries:read"), true},
		{"unsupported", api.DataSourceType("unsupported"), true, NewOrganizationPermissions("queries:read"), false},
		{"restricted", api.DataSourceTypePostgres, true, DefaultOrganizationPermissions(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			ds := &api.DataSource{Slug: "warehouse", Name: "Warehouse", Type: tc.typeName, IsActive: tc.active, LastTestedAt: &now}
			require.NoError(t, DataSourceDetailPanelContent("acme", "dev", ds, DefaultDataSourceSchemaPanelData(), nil, DataSourceUpdateFormData{}, tc.permissions, DataSourceOperationsPanelData{}).Render(context.Background(), &out))
			if tc.want {
				require.Contains(t, out.String(), "Get a result from this connection")
				require.Contains(t, out.String(), html.EscapeString(DataSourceQueriesHref("acme", "dev", "warehouse")))
			} else {
				require.NotContains(t, out.String(), "Get a result from this connection")
			}
		})
	}
}

func TestDataSourcesPageContentKeepsQuickLookReadOnly(t *testing.T) {
	t.Parallel()

	warehouse := api.DataSource{
		IsActive: true,
		Name:     "Warehouse",
		Slug:     "warehouse",
		Type:     api.DataSourceTypePostgres,
	}

	var out bytes.Buffer
	require.NoError(t, DataSourcesPageContent(
		[]api.DataSource{warehouse},
		[]api.DataSource{warehouse},
		false,
		"",
		"",
		"acme",
		"",
		"",
		&warehouse,
		DefaultDataSourceSchemaPanelData(),
		nil,
		DataSourceUpdateFormData{},
		DataSourceCreateFormData{},
		NewOrganizationPermissions("datasources:delete"),
		"",
		"",
		DataSourcePageFeedback{},
	).Render(context.Background(), &out))

	html := out.String()
	requireDataSourceIconMarker(t, html, "postgres")
	require.Contains(t, html, `data-entity-preview`)
	require.GreaterOrEqual(t, strings.Count(html, `data-data-source-icon-type="postgres"`), 1)
	require.Contains(t, html, DataSourceIconAssetPath("postgres")+`?v=`)
	require.Contains(t, html, ">Open full page</a>")
	panelIndex := strings.Index(html, `id="data-source-side-panel"`)
	require.NotEqual(t, -1, panelIndex)
	require.NotContains(t, html[panelIndex:], "<form")
	require.NotContains(t, html, ">Delete</button>")
}

func TestWorkspaceDisclosurePrecedesConsent(t *testing.T) {
	t.Parallel()
	const privacyURL = "https://docs.example.test/legal/google-workspace-2026-10-12/privacy/"
	for _, readOnly := range []bool{true, false} {
		var create bytes.Buffer
		require.NoError(t, DataSourcesPageContent(nil, nil, false, "", "", "acme", "dev", "", nil,
			DefaultDataSourceSchemaPanelData(), nil, DataSourceUpdateFormData{},
			DataSourceCreateFormData{Type: "google_workspace", WorkspaceReadOnly: readOnly, WorkspacePrivacyURL: privacyURL},
			NewOrganizationPermissions("datasources:create"), "", "", DataSourcePageFeedback{},
		).Render(t.Context(), &create))
		var reconnect bytes.Buffer
		require.NoError(t, WorkspaceSelection("acme", &api.DataSource{Slug: "sheet", ReadOnly: readOnly}, privacyURL).Render(t.Context(), &reconnect))
		for _, html := range []string{create.String(), reconnect.String()} {
			notice := strings.Index(html, `href="`+privacyURL+`"`)
			consent := strings.Index(html, ">Connect Google</button>")
			if consent == -1 {
				consent = strings.Index(html, ">Connect / reconnect Google</button>")
			}
			require.NotEqual(t, -1, notice)
			require.Greater(t, consent, notice, "disclosure must precede consent")
			require.Contains(t, html, "files you explicitly share with Braidplane and files Braidplane creates")
			require.NotContains(t, html, "all files available to your account")
			require.Contains(t, html, "scheduled workflows")
			require.Contains(t, html, "does not erase retained history or delivered copies")
			require.Contains(t, html, "/orgs/acme/data-sources/")
		}
	}
}

func TestDataSourcesPageContentRendersManagedGitHubRepositoryPicker(t *testing.T) {
	t.Parallel()

	form := DataSourceCreateFormData{
		Type: "github", GitHubConnectionAvailable: true, GitHubReadOnly: true,
		GitHubRepositories: []api.GitHubAppRepository{{Id: 42, FullName: "acme/pipelines", OwnerLogin: "acme", Name: "pipelines"}},
	}
	var out bytes.Buffer
	require.NoError(t, DataSourcesPageContent(nil, nil, false, "", "", "acme", "dev", "", nil,
		DefaultDataSourceSchemaPanelData(), nil, DataSourceUpdateFormData{}, form,
		NewOrganizationPermissions("datasources:create"), "", "", DataSourcePageFeedback{},
	).Render(context.Background(), &out))

	html := out.String()
	require.Contains(t, html, `name="github_repository_id"`)
	require.Contains(t, html, `value="42"`)
	require.Contains(t, html, `acme/pipelines`)
	require.NotContains(t, html, `data-ds-fields="github" name="git_repo_url"`)
	require.NotContains(t, html, `data-ds-fields="github" name="git_token"`)
}

func TestDataSourceOAuthFeedbackUsesSafeFixedCopy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		outcome string
		kind    string
		message string
	}{
		{outcome: "success", kind: "success", message: "HubSpot connected successfully."},
		{outcome: "denied", kind: "warning", message: "HubSpot connection was canceled."},
		{outcome: "expired", kind: "warning", message: "HubSpot authorization expired."},
		{outcome: "error", kind: "error", message: "HubSpot could not be connected."},
		{outcome: "provider-secret-detail", kind: "", message: ""},
	}

	for _, test := range tests {
		t.Run(test.outcome, func(t *testing.T) {
			t.Parallel()

			feedback := DataSourceOAuthFeedback(test.outcome)
			require.Equal(t, test.kind, feedback.Kind)
			require.Contains(t, feedback.Message, test.message)
			require.NotContains(t, feedback.Message, "provider-secret-detail")
		})
	}
}

func TestDataSourceHubSpotOAuthActionUsesCredentialLifecycle(t *testing.T) {
	t.Parallel()

	unknownInactive := api.DataSource{}
	unknownActive := api.DataSource{IsActive: true}
	static := api.DataSource{Credential: &api.CredentialMetadata{
		Kind:   api.StaticSecret,
		Status: api.CredentialStatusActive,
	}}
	pending := api.DataSource{Credential: &api.CredentialMetadata{
		Kind:   api.Oauth2Grant,
		Status: api.CredentialStatusPending,
	}}
	active := api.DataSource{Credential: &api.CredentialMetadata{
		Kind:   api.Oauth2Grant,
		Status: api.CredentialStatusActive,
	}}

	require.Equal(t, api.RotateGrant, DataSourceProviderOAuthIntent(unknownInactive))
	require.Equal(t, "Connect HubSpot with OAuth", DataSourceProviderOAuthActionLabel(unknownInactive))
	require.Equal(t, api.RotateGrant, DataSourceProviderOAuthIntent(unknownActive))
	require.Equal(t, "Connect HubSpot with OAuth", DataSourceProviderOAuthActionLabel(unknownActive))
	require.Equal(t, api.RotateGrant, DataSourceProviderOAuthIntent(static))
	require.Equal(t, "Switch to OAuth", DataSourceProviderOAuthActionLabel(static))
	require.Equal(t, api.CreateGrant, DataSourceProviderOAuthIntent(pending))
	require.Equal(t, "Retry HubSpot connection", DataSourceProviderOAuthActionLabel(pending))
	require.Equal(t, api.RotateGrant, DataSourceProviderOAuthIntent(active))
	require.Equal(t, "Reconnect HubSpot", DataSourceProviderOAuthActionLabel(active))
}

func TestDataSourceDetailPanelAllowsCreateOnlyUserToRetryPendingHubSpotOAuth(t *testing.T) {
	t.Parallel()

	pending := api.DataSource{
		Name: "HubSpot CRM",
		Slug: "hubspot-crm",
		Type: api.DataSourceTypeHubspot,
		Credential: &api.CredentialMetadata{
			Kind:   api.Oauth2Grant,
			Status: api.CredentialStatusPending,
		},
	}

	var out bytes.Buffer
	require.NoError(t, DataSourceDetailPanelContent(
		"acme",
		"development",
		&pending,
		DefaultDataSourceSchemaPanelData(),
		nil,
		DataSourceUpdateFormData{},
		NewOrganizationPermissions("datasources:create"),
		DataSourceOperationsPanelData{},
	).Render(WithEntityPageURL(context.Background(), "/?section=settings"), &out))

	html := out.String()
	require.Contains(t, html, "Retry HubSpot connection")
	require.NotContains(t, html, "Read-only settings")
	require.NotContains(t, html, ">Test connection<")
}

func TestDataSourceDetailPanelRequiresUpdateToSwitchHubSpotCredentials(t *testing.T) {
	t.Parallel()

	dataSource := api.DataSource{
		IsActive: true,
		Name:     "HubSpot CRM",
		Slug:     "hubspot-crm",
		Type:     api.DataSourceTypeHubspot,
		Credential: &api.CredentialMetadata{
			Kind:   api.StaticSecret,
			Status: api.CredentialStatusActive,
		},
	}
	render := func(t *testing.T, permissions OrganizationPermissions) string {
		t.Helper()
		var out bytes.Buffer
		require.NoError(t, DataSourceDetailPanelContent(
			"acme",
			"development",
			&dataSource,
			DefaultDataSourceSchemaPanelData(),
			nil,
			DataSourceUpdateFormData{},
			permissions,
			DataSourceOperationsPanelData{},
		).Render(WithEntityPageURL(context.Background(), "/?section=settings"), &out))
		return out.String()
	}

	createOnlyHTML := render(t, NewOrganizationPermissions("datasources:create"))
	require.NotContains(t, createOnlyHTML, "Switch to OAuth")
	require.NotContains(t, createOnlyHTML, ">Test connection<")

	updateHTML := render(t, NewOrganizationPermissions("datasources:update"))
	require.Contains(t, updateHTML, "Switch to OAuth")
	require.Contains(t, updateHTML, ">Recheck access<")
}

func TestDataSourceDetailPanelRequiresRepositorySelectionBeforeRecheck(t *testing.T) {
	t.Parallel()

	dataSource := api.DataSource{
		Name: "GitLab reports",
		Slug: "gitlab-reports",
		Type: api.DataSourceTypeGitlab,
		Credential: &api.CredentialMetadata{
			Kind:   api.Oauth2Grant,
			Status: api.CredentialStatusActive,
		},
	}
	form := DataSourceUpdateFormData{
		Name:                     dataSource.Name,
		Type:                     string(dataSource.Type),
		ProviderSelectionPending: true,
		ProviderRepositories: []api.ProviderRepository{{
			Id: "42", FullName: "acme/reports",
		}},
	}

	var out bytes.Buffer
	require.NoError(t, DataSourceDetailPanelContent(
		"acme",
		"development",
		&dataSource,
		DefaultDataSourceSchemaPanelData(),
		nil,
		form,
		NewOrganizationPermissions("datasources:update"),
		DataSourceOperationsPanelData{},
	).Render(WithEntityPageURL(context.Background(), "/?section=settings"), &out))

	html := out.String()
	require.Contains(t, html, "Repository selection required")
	require.Contains(t, html, `name="repository_id"`)
	require.Contains(t, html, "acme/reports")
	require.NotContains(t, html, ">Test connection<")
	require.NotContains(t, html, ">Recheck access<")
	require.NotContains(t, html, `name="git_repo_url"`)
	require.NotContains(t, html, "Rotate credentials")
}

func TestDataSourceConnectionUpdateFormsRenderConnectorSpecificControls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		dsType     api.DataSourceType
		formData   DataSourceUpdateFormData
		present    []string
		notPresent []string
	}{
		{
			name:   "BigQuery",
			dsType: api.DataSourceTypeBigquery,
			formData: DataSourceUpdateFormData{
				Type: "bigquery", ProjectID: "analytics-prod", Location: "EU",
				DefaultDataset: "analytics", TimeoutSeconds: "30", MaximumBytesBilled: "1000000",
			},
			present: []string{
				`name="project_id"`, `name="location"`, `name="default_dataset"`,
				`name="timeout_seconds"`, `name="maximum_bytes_billed"`,
			},
			notPresent: []string{`name="account"`, `name="warehouse"`, `name="connect_timeout"`},
		},
		{
			name:   "Snowflake",
			dsType: api.DataSourceTypeSnowflake,
			formData: DataSourceUpdateFormData{
				Type: "snowflake", Account: "acme", Warehouse: "WH", Database: "DB",
				Schema: "PUBLIC", ConnectTimeout: "15",
			},
			present: []string{
				`name="account"`, `name="warehouse"`, `name="database"`, `name="schema"`,
				`name="role"`, `name="connect_timeout"`,
			},
			notPresent: []string{
				`name="project_id"`, `name="location"`, `name="default_dataset"`,
				`name="timeout_seconds"`, `name="maximum_bytes_billed"`,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dataSource := api.DataSource{
				Name: "Warehouse", Slug: "warehouse", Type: test.dsType, IsActive: true,
			}
			var out bytes.Buffer
			require.NoError(t, DataSourceDetailPanelContent(
				"acme", "development", &dataSource, DefaultDataSourceSchemaPanelData(), nil,
				test.formData, NewOrganizationPermissions("datasources:update"),
				DataSourceOperationsPanelData{},
			).Render(WithEntityPageURL(context.Background(), "/?section=settings"), &out))
			html := out.String()
			for _, marker := range test.present {
				require.Contains(t, html, marker)
			}
			for _, marker := range test.notPresent {
				require.NotContains(t, html, marker)
			}
		})
	}
}

func TestDataSourceOperationalHelpers(t *testing.T) {
	t.Parallel()

	sampleDataSources := func() []api.DataSource {
		testError := "timeout"
		testedAt := time.Date(2026, 6, 7, 12, 30, 0, 0, time.UTC)
		return []api.DataSource{
			{
				IsActive:     true,
				LastTestedAt: &testedAt,
				Name:         "Warehouse",
				ReadOnly:     false,
				Slug:         "warehouse",
				Type:         api.DataSourceTypePostgres,
			},
			{
				IsActive:  true,
				Name:      "CRM",
				ReadOnly:  true,
				Slug:      "crm",
				TestError: &testError,
				Type:      api.DataSourceTypeHubspot,
			},
			{
				IsActive: false,
				Name:     "Archive",
				ReadOnly: true,
				Slug:     "archive",
				Type:     api.DataSourceTypeGit,
			},
		}
	}

	tests := []struct {
		name   string
		assert func(t *testing.T)
	}{
		{
			name: "ReadyCount",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, 1, DataSourceReadyCount(sampleDataSources()))
			},
		},
		{
			name: "AttentionCount",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, 2, DataSourceAttentionCount(sampleDataSources()))
			},
		},
		{
			name: "ReadWriteCount",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, 1, DataSourceReadWriteCount(sampleDataSources()))
			},
		},
		{
			name: "ReadyStatusLabel",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "ready", DataSourceStatusLabel(sampleDataSources()[0]))
			},
		},
		{
			name: "FailedStatusLabel",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "test failed", DataSourceStatusLabel(sampleDataSources()[1]))
			},
		},
		{
			name: "InactiveStatusLabel",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "inactive", DataSourceStatusLabel(sampleDataSources()[2]))
			},
		},
		{
			name: "ReadWriteCapabilityLabel",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "read/write", DataSourceCapabilityLabel(sampleDataSources()[0]))
			},
		},
		{
			name: "ReadOnlyCapabilityLabel",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "read only", DataSourceCapabilityLabel(sampleDataSources()[1]))
			},
		},
		{
			name: "LastTestLabel",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "last test 2026-06-07T12:30:00Z", DataSourceLastTestLabel(sampleDataSources()[0]))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.assert(t)
		})
	}
}

func TestDataSourceIconAssetPathUsesResourceBackedConnectorMarks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		dataSourceType string
		want           string
	}{
		{
			name:           "Postgres",
			dataSourceType: " postgres ",
			want:           "/assets/icons/data-sources/vendors/brand/postgres.svg",
		},
		{
			name:           "MySQL",
			dataSourceType: string(api.DataSourceTypeMysql),
			want:           "/assets/icons/data-sources/vendors/brand/mysql.svg",
		},
		{
			name:           "Google Workspace",
			dataSourceType: string(api.DataSourceTypeGoogleWorkspace),
			want:           "/assets/icons/data-sources/vendors/brand/google-drive.png",
		},
		{
			name:           "BigQuery",
			dataSourceType: string(api.DataSourceTypeBigquery),
			want:           "/assets/icons/data-sources/vendors/brand/google-bigquery.svg",
		},
		{
			name:           "Snowflake",
			dataSourceType: string(api.DataSourceTypeSnowflake),
			want:           "/assets/icons/data-sources/vendors/brand/snowflake.svg",
		},
		{
			name:           "MongoDB",
			dataSourceType: string(api.DataSourceTypeMongodb),
			want:           "/assets/icons/data-sources/vendors/brand/mongodb.png",
		},
		{
			name:           "HubSpot",
			dataSourceType: string(api.DataSourceTypeHubspot),
			want:           "/assets/icons/data-sources/vendors/brand/hubspot.svg",
		},
		{
			name:           "Git",
			dataSourceType: string(api.DataSourceTypeGit),
			want:           "/assets/icons/data-sources/vendors/brand/git.svg",
		},
		{
			name:           "GitHub",
			dataSourceType: string(api.DataSourceTypeGithub),
			want:           "/assets/icons/data-sources/vendors/brand/github.svg",
		},
		{
			name:           "GitLabFallsBackToLocalGlyph",
			dataSourceType: string(api.DataSourceTypeGitlab),
			want:           "",
		},
		{
			name:           "S3FallsBackToLocalGlyph",
			dataSourceType: string(api.DataSourceTypeS3),
			want:           "",
		},
		{
			name:           "HTTPProtocolFallback",
			dataSourceType: string(api.DataSourceTypeHttp),
			want:           "",
		},
		{
			name:           "FTPProtocolFallback",
			dataSourceType: string(api.DataSourceTypeFtp),
			want:           "",
		},
		{
			name:           "UnknownFallback",
			dataSourceType: "unknown",
			want:           "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, DataSourceIconAssetPath(tt.dataSourceType))
		})
	}
}

func TestDataSourceIconClassKeepsDataSourceMarksUnframed(t *testing.T) {
	t.Parallel()

	for _, dataSourceType := range []string{
		string(api.DataSourceTypePostgres),
		string(api.DataSourceTypeHttp),
		string(api.DataSourceTypeFtp),
		"",
	} {
		iconClass := DataSourceIconClass(dataSourceType, "")

		require.Contains(t, iconClass, "bg-transparent")
		require.NotContains(t, iconClass, "border")
		require.NotContains(t, iconClass, "rounded")
		require.NotContains(t, iconClass, "bg-white")
		require.NotContains(t, iconClass, "shadow-sm")
	}
}

func TestDataSourceIconRendersUnboxedProtocolFallbackLabels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		dataSourceType string
		label          string
	}{
		{name: "HTTP", dataSourceType: string(api.DataSourceTypeHttp), label: "HTTP"},
		{name: "FTP", dataSourceType: string(api.DataSourceTypeFtp), label: "FTP"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			require.NoError(t, DataSourceIcon(tt.dataSourceType, "sm").Render(context.Background(), &out))
			html := out.String()

			requireDataSourceIconMarker(t, html, tt.dataSourceType)
			require.Contains(t, html, ">"+tt.label+"</text>")
			require.NotContains(t, html, "<rect")
			require.NotContains(t, html, `width="16"`)
			require.NotContains(t, html, `height="16"`)
			require.NotContains(t, html, "border")
			require.NotContains(t, html, "rounded")
			require.NotContains(t, html, "bg-white")
		})
	}
}

func TestDataSourceIconUnknownFallbackMatchesShellGlyph(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	require.NoError(t, DataSourceIcon("", "sm").Render(context.Background(), &out))
	html := out.String()

	requireDataSourceIconMarker(t, html, dataSourceIconTypeUnknown)
	require.Contains(t, html, `d="M12 16v2.5a2.5 2.5 0 002.5 2.5H18"`)
	require.Contains(t, html, `cx="20" cy="21" r="1.5"`)
}

func TestGitLabDataSourceIconUsesGenericFallback(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	require.NoError(t, DataSourceIcon(string(api.DataSourceTypeGitlab), "sm").Render(t.Context(), &out))
	requireDataSourceIconMarker(t, out.String(), string(api.DataSourceTypeGitlab))
	require.Contains(t, out.String(), `d="M12 16v2.5a2.5 2.5 0 002.5 2.5H18"`)
	require.NotContains(t, out.String(), "gitlab.svg")
}

func TestDataSourceIconAssetsUseReviewedFiles(t *testing.T) {
	t.Parallel()

	files, err := os.ReadDir("../../brand/icons/data-sources/vendors/brand")
	require.NoError(t, err)
	var names []string
	for _, file := range files {
		names = append(names, file.Name())
	}
	require.ElementsMatch(t, []string{"git.svg", "mongodb.png", "postgres.svg", "mysql.svg", "google-drive.png", "google-bigquery.svg", "snowflake.svg", "hubspot.svg", "github.svg"}, names)
	metadata, err := os.ReadFile("../../brand/icons/data-sources/vendors/metadata.json")
	require.NoError(t, err)
	var marks []struct {
		File   string `json:"file"`
		Format string `json:"format"`
		SHA256 string `json:"sha256"`
	}
	require.NoError(t, json.Unmarshal(metadata, &marks))
	require.Len(t, marks, 9)
	for _, mark := range marks {
		content, readErr := os.ReadFile("../../brand/icons/data-sources/vendors/brand/" + mark.File + "." + mark.Format)
		require.NoError(t, readErr)
		digest := sha256.Sum256(content)
		require.Equal(t, mark.SHA256, hex.EncodeToString(digest[:]))
	}
	for _, sourceType := range []string{"git", "mongodb"} {
		var out bytes.Buffer
		require.NoError(t, DataSourceIcon(sourceType, "sm").Render(t.Context(), &out))
		requireDataSourceIconMarker(t, out.String(), sourceType)
		if sourceType == "mongodb" {
			require.Contains(t, out.String(), "®")
		} else {
			require.Contains(t, out.String(), "™")
		}
	}
}

func TestUnbrandedConnectorTypesRenderGenericConnectorGlyph(t *testing.T) {
	t.Parallel()
	for _, sourceType := range []string{"s3", "gitlab", "unknown"} {
		var out bytes.Buffer
		require.NoError(t, DataSourceIcon(sourceType, "sm").Render(t.Context(), &out))
		requireDataSourceIconMarker(t, out.String(), sourceType)
		require.Contains(t, out.String(), `d="M12 16v2.5a2.5 2.5 0 002.5 2.5H18"`)
		require.NotContains(t, out.String(), "<img")
	}
}

func requireDataSourceIconMarker(t *testing.T, html, dataSourceType string) {
	t.Helper()

	require.Contains(t, html, `data-data-source-icon`)
	require.Contains(t, html, `data-data-source-icon-type="`+dataSourceType+`"`)
	if assetPath := DataSourceIconAssetPath(dataSourceType); assetPath != "" {
		require.Contains(t, html, `src="`+assetPath+`?v=`)
	}
}

func TestDataSourceNavigationHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "ListCursorPreservesEnvironmentAndEncodesCursor",
			got:  DataSourcesListCursorHref(" acme ", " prod ", "next:page/2"),
			want: "/orgs/acme/data-sources?cursor=next%3Apage%2F2&environment_slug=prod",
		},
		{
			name: "QueriesLinkPreservesEnvironmentAndSource",
			got:  DataSourceQueriesHref(" acme ", " dev ", "repo source"),
			want: "/orgs/acme/queries?ds=repo+source&environment_slug=dev",
		},
		{
			name: "CreatePreservesEnvironment",
			got:  DataSourceCreateHref(" acme ", " dev "),
			want: "/orgs/acme/data-sources?environment_slug=dev",
		},
		{
			name: "CreateTestPreservesEnvironment",
			got:  DataSourceCreateTestHref(" acme ", " dev "),
			want: "/orgs/acme/data-sources/test-connection?environment_slug=dev",
		},
		{
			name: "StoredTestPreservesEnvironment",
			got:  DataSourceTestHref(" acme ", " dev ", "repo-source"),
			want: "/orgs/acme/data-sources/repo-source/test-connection?environment_slug=dev",
		},
		{
			name: "RefreshPreservesEnvironment",
			got:  DataSourceRefreshHref(" acme ", " dev ", "repo-source"),
			want: "/orgs/acme/data-sources/repo-source/schema/refresh?environment_slug=dev",
		},
		{
			name: "UpdatePreservesEnvironment",
			got:  DataSourceUpdateHref(" acme ", " dev ", "repo-source"),
			want: "/orgs/acme/data-sources/repo-source/update?environment_slug=dev",
		},
		{
			name: "UpdateCredentialPreservesEnvironment",
			got:  DataSourceUpdateCredentialHref(" acme ", " dev ", "repo-source"),
			want: "/orgs/acme/data-sources/repo-source/credentials/update?environment_slug=dev",
		},
		{
			name: "DeletePreservesEnvironment",
			got:  DataSourceDeleteHref(" acme ", " dev ", "repo-source"),
			want: "/orgs/acme/data-sources/repo-source/delete?environment_slug=dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.got)
		})
	}

	require.True(t, DataSourceTypeIsGitLike(" github "))
	require.True(t, DataSourceTypeIsGitLike("gitlab"))
	require.False(t, DataSourceTypeIsGitLike("postgres"))
}

func TestQueryOperationalHelpers(t *testing.T) {
	t.Parallel()

	sampleQuery := func() api.Query {
		description := "List active customers"
		return api.Query{
			DataSourceSlug: "warehouse",
			Description:    &description,
			Name:           "Customers",
			Parameters: []api.QueryParameter{
				{Name: "account_id", DataType: api.QueryParameterDataTypeText},
				{Name: "limit", DataType: api.QueryParameterDataTypeInteger},
			},
			QueryStatement: api.QueryStatement{},
			RowLimit:       100,
			Slug:           "customers",
			TimeoutSeconds: 30,
		}
	}
	sampleQueries := func() []api.Query {
		deletedAt := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
		active := sampleQuery()
		deleted := active
		deleted.DeletedAt = &deletedAt
		return []api.Query{active, deleted}
	}
	sampleDataSources := func() []api.DataSource {
		return []api.DataSource{{
			IsActive: true,
			Name:     "Warehouse",
			Slug:     "warehouse",
			Type:     api.DataSourceTypePostgres,
		}}
	}

	tests := []struct {
		name   string
		assert func(t *testing.T)
	}{
		{
			name: "ActiveCount",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, 1, QueryActiveCount(sampleQueries()))
			},
		},
		{
			name: "DeletedCount",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, 1, QueryDeletedCount(sampleQueries()))
			},
		},
		{
			name: "ParameterTotal",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, 4, QueryParameterTotal(sampleQueries()))
			},
		},
		{
			name: "SourceName",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "Warehouse", QuerySourceName(sampleDataSources(), "warehouse"))
			},
		},
		{
			name: "SourceStatusLabel",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "untested", QuerySourceStatusLabel(sampleDataSources(), "warehouse"))
			},
		},
		{
			name: "DescriptionLabel",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "List active customers", QueryDescriptionLabel(sampleQuery()))
			},
		},
		{
			name: "MissingDescriptionLabel",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "No description", QueryDescriptionLabel(api.Query{}))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.assert(t)
		})
	}
}

func TestQueryNavigationHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "CursorHrefEncodesSourceAndCursor",
			got:  QueriesListCursorHref(" acme ", "source/one", false, "cursor:2"),
			want: "/orgs/acme/queries?cursor=cursor%3A2&ds=source%2Fone",
		},
		{
			name: "DeletedCursorHrefPreservesLifecycleFilter",
			got:  QueriesListCursorHref(" acme ", "source", true, "next"),
			want: "/orgs/acme/queries?cursor=next&deleted=true&ds=source",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.got)
		})
	}
}
