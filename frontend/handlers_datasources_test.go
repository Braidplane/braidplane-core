package frontend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
)

func setupAuthenticatedDatasourceTestApp(t *testing.T) (*fiber.App, string) {
	t.Helper()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	setActiveOrganizationSlugInSession(t, app, authCookie, "acme")
	return app, authCookie
}

func TestRenderTemplPreservesListReturnAfterAction(t *testing.T) {
	t.Parallel()
	listURL := "/orgs/acme/data-sources?cursor=next&environment_slug=dev&filter=attention&selected=warehouse"
	for _, tt := range []struct {
		name string
		host string
		want string
	}{
		{"same host", "example.com", listURL},
		{"external host", "other.example", views.DataSourcesListHref("acme", "dev", "warehouse")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			app := fiber.New()
			app.Post("/orgs/acme/data-sources/warehouse/test", func(c fiber.Ctx) error {
				return renderTempl(c, views.DataSourceDetailPageContent(
					"acme", "dev", &api.DataSource{Name: "Warehouse", Slug: "warehouse", Type: api.DataSourceTypePostgres},
					views.DefaultDataSourceSchemaPanelData(), nil, views.DataSourceUpdateFormData{},
					views.OrganizationPermissions{}, views.DataSourceOperationsPanelData{}, "", "",
				))
			})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com/orgs/acme/data-sources/warehouse/test", nil)
			req.Header.Set("HX-Current-URL", "http://"+tt.host+"/orgs/acme/data-sources/warehouse?list_return="+url.QueryEscape(listURL))
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Contains(t, string(body), `href="`+strings.ReplaceAll(tt.want, "&", "&amp;")+`">Back to list</a>`)
		})
	}
}

func TestRouter_ListAllDataSourcesViaAPI_ReturnsErrorOnRepeatedCursor(t *testing.T) {
	t.Parallel()

	requests := 0
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assert.Equal(t, "/api/organizations/acme-hq/data-sources", r.URL.Path)
		assert.Equal(t, "100", r.URL.Query().Get("limit"))
		requests++
		if requests == 2 {
			assert.Equal(t, "stuck", r.URL.Query().Get("cursor"))
			assert.Equal(t, "next", r.URL.Query().Get("direction"))
		}
		_, _ = w.Write([]byte(`{"data_sources":[],"has_more":true,"next_cursor":"stuck","prev_cursor":null}`))
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	dataSources, status, errorResponse, err := router.listAllDataSourcesViaAPI(
		t.Context(),
		"",
		"acme-hq",
	)

	require.Error(t, err)
	require.Nil(t, dataSources)
	require.Nil(t, errorResponse)
	require.Equal(t, http.StatusInternalServerError, status)
	require.Contains(t, err.Error(), "repeated cursor")
	require.Equal(t, 2, requests)
}

func withDatasourceFormContext(t *testing.T, form url.Values, fn func(fiber.Ctx)) {
	t.Helper()

	app := fiber.New()
	app.Post("/", func(c fiber.Ctx) error {
		fn(c)
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, fiber.StatusNoContent, resp.StatusCode)
}

func bigQueryDataSourceFormValues() url.Values {
	form := url.Values{}
	form.Set("name", "Analytics Warehouse")
	form.Set("type", "bigquery")
	form.Set("project_id", "analytics-prod")
	form.Set("location", "EU")
	form.Set("default_dataset", "warehouse")
	form.Set("timeout_seconds", "30")
	form.Set("maximum_bytes_billed", "1000000000")
	return form
}

func snowflakeDataSourceFormValues() url.Values {
	form := url.Values{}
	form.Set("name", "Snowflake Warehouse")
	form.Set("type", "snowflake")
	form.Set("account", "acme.eu-central-1")
	form.Set("warehouse", "BRAIDPLANE_WH")
	form.Set("database", "ANALYTICS")
	form.Set("schema", "PUBLIC")
	form.Set("role", "BRAIDPLANE_READER")
	form.Set("connect_timeout", "15")
	form.Set("snowflake_auth_type", "password")
	form.Set("username", "braidplane_service")
	form.Set("password", "write-only-secret")
	return form
}

func gitDataSourceFormValues() url.Values {
	form := url.Values{}
	form.Set("name", "Automation Repo")
	form.Set("type", "git")
	form.Set("git_auth_type", gitAuthTypeSSH)
	form.Set("git_repo_url", "git@github.com:acme/automation.git")
	form.Set("git_default_branch", "main")
	form.Set("git_base_path", "pipelines")
	form.Set("git_username", "git")
	form.Set("git_token", "git-redacted-token-value")
	form.Set("git_ssh_private_key", "git-redacted-private-key-value")
	form.Set("git_ssh_known_hosts", "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample")
	form.Set("git_ssh_passphrase", "git-redacted-passphrase-value")
	return form
}

func TestDataSourceUpdateFormDataFromDataSource_DerivesGitAuthTypeFromRepoURL(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name         string
		dataType     api.DataSourceType
		repoURL      string
		wantAuthType string
		wantSelected string
	}{
		{
			name:         "GenericGitSCPSSH",
			dataType:     api.DataSourceTypeGit,
			repoURL:      "git@github.com:acme/automation.git",
			wantAuthType: gitAuthTypeSSH,
			wantSelected: `<option value="ssh" selected>SSH</option>`,
		},
		{
			name:         "GitHubHTTPS",
			dataType:     api.DataSourceTypeGithub,
			repoURL:      "https://github.com/acme/automation.git",
			wantAuthType: gitAuthTypeHTTPS,
			wantSelected: `<option value="https" selected>HTTPS</option>`,
		},
		{
			name:         "GitLabSSHURL",
			dataType:     api.DataSourceTypeGitlab,
			repoURL:      "ssh://git@gitlab.com/acme/automation.git",
			wantAuthType: gitAuthTypeSSH,
			wantSelected: `<option value="ssh" selected>SSH</option>`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var config api.DataSource_Config
			require.NoError(t, config.FromGitConfig(api.GitConfig{RepoUrl: tt.repoURL}))
			dataSource := &api.DataSource{
				Name:     "Automation Repo",
				Slug:     "automation-repo",
				Type:     tt.dataType,
				Config:   config,
				IsActive: true,
			}

			formData := dataSourceUpdateFormDataFromDataSource(dataSource)
			require.Equal(t, tt.repoURL, formData.GitRepoURL)
			require.Equal(t, tt.wantAuthType, formData.GitAuthType)

			var out strings.Builder
			err := views.DataSourceDetailPageContent(
				"acme",
				"dev",
				dataSource,
				views.DefaultDataSourceSchemaPanelData(),
				nil,
				formData,
				views.NewOrganizationPermissions("datasources:update"), views.DataSourceOperationsPanelData{},
				"",
				"",
			).Render(views.WithEntityPageURL(context.Background(), "/?section=settings"), &out)
			require.NoError(t, err)

			html := out.String()
			require.Contains(t, html, tt.wantSelected)
			require.Contains(t, html, `name="git_repo_url" value="`+tt.repoURL+`"`)
			require.NotContains(t, html, `type="hidden" name="git_repo_url"`)
		})
	}
}

func TestDataSourceUpdateFormDataFromDataSource_DetectsPendingProviderSelection(t *testing.T) {
	t.Parallel()

	var pendingConfig api.DataSource_Config
	require.NoError(t, pendingConfig.FromGitConfig(api.GitConfig{RepoUrl: ""}))
	pending := dataSourceUpdateFormDataFromDataSource(&api.DataSource{
		Type:   api.DataSourceTypeGitlab,
		Config: pendingConfig,
	})
	require.True(t, pending.ProviderSelectionPending)
	require.False(t, pending.ManagedProvider)

	var managedConfig api.DataSource_Config
	require.NoError(t, managedConfig.FromProviderGitConfig(api.ProviderGitConfig{
		Repository: api.ProviderRepository{
			Id: "42", Name: "reports", FullName: "acme/reports", WebUrl: "https://gitlab.com/acme/reports",
		},
	}))
	managed := dataSourceUpdateFormDataFromDataSource(&api.DataSource{
		Type:   api.DataSourceTypeGitlab,
		Config: managedConfig,
	})
	require.False(t, managed.ProviderSelectionPending)
	require.True(t, managed.ManagedProvider)
}

func s3DataSourceFormValues() url.Values {
	form := url.Values{}
	form.Set("name", "Object Store")
	form.Set("type", "s3")
	form.Set("endpoint", "http://localhost:39000")
	form.Set("region", "us-east-1")
	form.Set("bucket", "braidplane-fixtures")
	form.Set("prefix", "exports/")
	form.Set("force_path_style", "on")
	form.Set("allow_private_hosts", "on")
	form.Set("timeout_seconds", "15")
	form.Set("access_key_id", "access-key")
	form.Set("secret_access_key", "secret-key")
	form.Set("session_token", "session-token")
	return form
}

func databaseDataSourceFormValues(dsType api.DataSourceType) url.Values {
	form := url.Values{}
	form.Set("name", "Operational Database")
	form.Set("type", string(dsType))
	form.Set("host", "db.example.com")
	form.Set("database", "app")
	form.Set("username", "app-user")
	form.Set("password", "secret")
	form.Set("connect_timeout", "15")
	form.Set(databaseAllowPrivateHostsField, "on")
	switch dsType {
	case api.DataSourceTypeMysql:
		form.Set("port", "3306")
	case api.DataSourceTypeMongodb:
		form.Set("port", "27017")
		form.Set("auth_database", "admin")
	default:
		form.Set("port", "5432")
	}
	return form
}

func TestSnowflakeDataSourceForms(t *testing.T) {
	t.Parallel()

	withDatasourceFormContext(t, snowflakeDataSourceFormValues(), func(c fiber.Ctx) {
		payload, errMsg := buildCreateDataSourcePayloadWithPrivateHosts(c, false)
		require.Empty(t, errMsg)

		value, err := payload.ValueByDiscriminator()
		require.NoError(t, err)
		request, ok := value.(api.CreateSnowflakeDataSourceRequest)
		require.True(t, ok)
		require.Equal(t, "acme.eu-central-1", request.Config.Account)
		require.Equal(t, "BRAIDPLANE_WH", request.Config.Warehouse)
		require.Equal(t, "ANALYTICS", request.Config.Database)
		require.Equal(t, "PUBLIC", request.Config.Schema)
		require.Equal(t, "BRAIDPLANE_READER", *request.Config.Role)
		require.Equal(t, 15, *request.Config.ConnectTimeout)
		credentials, err := request.Credentials.AsSnowflakePasswordCredentials()
		require.NoError(t, err)
		require.NotNil(t, credentials.Username)
		require.NotNil(t, credentials.Password)
		require.Equal(t, "braidplane_service", *credentials.Username)
		require.Equal(t, "write-only-secret", *credentials.Password)
		require.Equal(t, api.SnowflakePasswordCredentialsAuthTypePassword, credentials.AuthType)

		connectionPayload, err := toTestDataSourceConnectionPayload(payload)
		require.NoError(t, err)
		connectionValue, err := connectionPayload.ValueByDiscriminator()
		require.NoError(t, err)
		_, ok = connectionValue.(api.TestSnowflakeDataSourceConnectionRequest)
		require.True(t, ok)
	})
}

func TestSnowflakeWorkloadIdentityFormOmitsStaticSecrets(t *testing.T) {
	t.Parallel()

	form := snowflakeDataSourceFormValues()
	form.Set("snowflake_auth_type", "workload_identity")
	form.Del("username")
	form.Del("password")

	withDatasourceFormContext(t, form, func(c fiber.Ctx) {
		payload, errMsg := buildCreateDataSourcePayloadWithPrivateHosts(c, false)
		require.Empty(t, errMsg)
		value, err := payload.ValueByDiscriminator()
		require.NoError(t, err)
		request, ok := value.(api.CreateSnowflakeDataSourceRequest)
		require.True(t, ok)
		credentials, err := request.Credentials.AsSnowflakeWorkloadIdentityCredentials()
		require.NoError(t, err)
		require.Equal(t, api.SnowflakeWorkloadIdentityCredentialsAuthTypeWorkloadIdentity, credentials.AuthType)
	})
}

func TestManagedGitLabFormUsesStableProjectIdentity(t *testing.T) {
	t.Parallel()

	form := url.Values{
		"name":               {"Pipelines"},
		"type":               {"gitlab"},
		"gitlab_project_id":  {"42"},
		"gitlab_access_mode": {"read"},
		"git_base_path":      {"data"},
	}
	withDatasourceFormContext(t, form, func(c fiber.Ctx) {
		payload, errMsg := buildCreateDataSourcePayloadWithPrivateHosts(c, false)
		require.Empty(t, errMsg)
		value, err := payload.ValueByDiscriminator()
		require.NoError(t, err)
		request, ok := value.(api.CreateGitLabDataSourceRequest)
		require.True(t, ok)
		require.Equal(t, "42", request.Config.ProjectId)
		require.Equal(t, "data", *request.Config.BasePath)
		require.True(t, *request.ReadOnly)
	})
}

func TestBigQueryDataSourceForms(t *testing.T) {
	t.Parallel()
	form := bigQueryDataSourceFormValues()
	withDatasourceFormContext(t, form, func(c fiber.Ctx) {
		_, errMsg := buildCreateDataSourcePayloadWithPrivateHosts(c, false)
		require.Equal(t, "BigQuery data sources must use Google OAuth.", errMsg)
		_, errMsg = buildUpdateDataSourceCredentialsPayloadForDataSource(c, &api.DataSource{Type: api.DataSourceTypeBigquery})
		require.Equal(t, "BigQuery credentials are managed through Google OAuth.", errMsg)
	})
}

func TestS3DataSourceUpdateForms(t *testing.T) {
	t.Parallel()

	decodePayload := func(t *testing.T, payload any) map[string]any {
		t.Helper()

		body, err := json.Marshal(payload)
		require.NoError(t, err)

		var captured map[string]any
		require.NoError(t, json.Unmarshal(body, &captured))
		return captured
	}

	assertS3Config := func(t *testing.T, captured map[string]any, wantAllowPrivateHosts bool) {
		t.Helper()

		config, ok := captured["config"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "http://localhost:39000", config["endpoint"])
		require.Equal(t, "us-east-1", config["region"])
		require.Equal(t, "braidplane-fixtures", config["bucket"])
		require.Equal(t, "exports/", config["prefix"])
		require.Equal(t, true, config["force_path_style"])
		require.Equal(t, wantAllowPrivateHosts, config["allow_private_hosts"])
		require.InDelta(t, 15, config["timeout_seconds"], 0.000001)
	}

	assertS3Credentials := func(t *testing.T, captured map[string]any) {
		t.Helper()

		credentials, ok := captured["credentials"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "access-key", credentials["access_key_id"])
		require.Equal(t, "secret-key", credentials["secret_access_key"])
		require.Equal(t, "session-token", credentials["session_token"])
	}

	t.Run("UpdatePayloadSendsConfig", func(t *testing.T) {
		t.Parallel()

		withDatasourceFormContext(t, s3DataSourceFormValues(), func(c fiber.Ctx) {
			payload, errMsg := buildUpdateDataSourcePayloadWithPrivateHosts(c, true)
			require.Empty(t, errMsg)

			captured := decodePayload(t, payload)
			require.Equal(t, "Object Store", captured["name"])
			assertS3Config(t, captured, true)
		})
	})

	t.Run("UpdatePayloadOmitsPrivateHostsWhenDeploymentPolicyDisabled", func(t *testing.T) {
		t.Parallel()

		withDatasourceFormContext(t, s3DataSourceFormValues(), func(c fiber.Ctx) {
			payload, errMsg := buildUpdateDataSourcePayloadWithPrivateHosts(c, false)
			require.Empty(t, errMsg)

			config, ok := decodePayload(t, payload)["config"].(map[string]any)
			require.True(t, ok)
			require.NotContains(t, config, "allow_private_hosts")
		})
	})

	t.Run("CredentialsPayloadSendsCredentials", func(t *testing.T) {
		t.Parallel()

		withDatasourceFormContext(t, s3DataSourceFormValues(), func(c fiber.Ctx) {
			payload, errMsg := buildUpdateDataSourceCredentialsPayloadForDataSource(c, &api.DataSource{Type: api.DataSourceTypeS3})
			require.Empty(t, errMsg)

			assertS3Credentials(t, decodePayload(t, payload))
		})
	})

	t.Run("PrefillAndRenderDetailForms", func(t *testing.T) {
		t.Parallel()

		endpoint := "http://localhost:39000"
		prefix := "exports/"
		forcePathStyle := true
		allowPrivateHosts := true
		timeoutSeconds := 15
		var config api.DataSource_Config
		require.NoError(t, config.FromS3Config(api.S3Config{
			Endpoint:          &endpoint,
			Region:            "us-east-1",
			Bucket:            "braidplane-fixtures",
			Prefix:            &prefix,
			ForcePathStyle:    &forcePathStyle,
			AllowPrivateHosts: &allowPrivateHosts,
			TimeoutSeconds:    &timeoutSeconds,
		}))
		dataSource := &api.DataSource{
			Name:     "Object Store",
			Slug:     "object-store",
			Type:     api.DataSourceTypeS3,
			Config:   config,
			IsActive: true,
		}

		formData := dataSourceUpdateFormDataFromDataSource(dataSource)
		formData.AllowPrivateDataSourceHosts = true
		require.Equal(t, "http://localhost:39000", formData.S3Endpoint)
		require.Equal(t, "us-east-1", formData.S3Region)
		require.Equal(t, "braidplane-fixtures", formData.S3Bucket)
		require.Equal(t, "exports/", formData.S3Prefix)
		require.True(t, formData.S3ForcePathStyle)
		require.True(t, formData.S3AllowPrivateHosts)
		require.Equal(t, "15", formData.TimeoutSeconds)

		var out strings.Builder
		err := views.DataSourceDetailPageContent(
			"acme",
			"dev",
			dataSource,
			views.DefaultDataSourceSchemaPanelData(),
			nil,
			formData,
			views.NewOrganizationPermissions("datasources:update"), views.DataSourceOperationsPanelData{},
			"",
			"",
		).Render(views.WithEntityPageURL(context.Background(), "/?section=settings"), &out)
		require.NoError(t, err)

		body := out.String()
		require.Contains(t, body, `name="endpoint"`)
		require.Contains(t, body, `value="http://localhost:39000"`)
		require.Contains(t, body, `name="region"`)
		require.Contains(t, body, `value="us-east-1"`)
		require.Contains(t, body, `name="bucket"`)
		require.Contains(t, body, `value="braidplane-fixtures"`)
		require.Contains(t, body, `name="prefix"`)
		require.Contains(t, body, `value="exports/"`)
		require.Contains(t, body, `name="force_path_style"`)
		require.Contains(t, body, `name="allow_private_hosts"`)
		require.Contains(t, body, `name="access_key_id"`)
		require.Contains(t, body, `name="secret_access_key"`)
		require.Contains(t, body, `name="session_token"`)
		require.NotContains(t, body, `name="host"`)
		require.NotContains(t, body, `name="port"`)
		require.NotContains(t, body, `name="database"`)
	})
}

func TestHubSpotDataSourceAccessModeForms(t *testing.T) {
	t.Parallel()

	decodePayload := func(t *testing.T, payload any) map[string]any {
		t.Helper()

		body, err := json.Marshal(payload)
		require.NoError(t, err)

		var captured map[string]any
		require.NoError(t, json.Unmarshal(body, &captured))
		return captured
	}

	for _, tt := range []struct {
		name       string
		accessMode string
		wantRead   bool
	}{
		{name: "DefaultsToReadOnly", wantRead: true},
		{name: "EnablesWritesExplicitly", accessMode: "write", wantRead: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			form := url.Values{}
			form.Set("name", "HubSpot CRM")
			form.Set("type", "hubspot")
			form.Set("timeout_seconds", "15")
			form.Set("private_app_token", "pat-redacted")
			if tt.accessMode != "" {
				form.Set("hubspot_access_mode", tt.accessMode)
			}

			withDatasourceFormContext(t, form, func(c fiber.Ctx) {
				updatePayload, errMsg := buildUpdateDataSourcePayloadWithPrivateHosts(c, false)
				require.Empty(t, errMsg)
				require.Equal(t, tt.wantRead, decodePayload(t, updatePayload)["read_only"])

				createPayload, err := buildCreateDataSourceRequest(
					"HubSpot CRM",
					"hubspot-crm",
					api.DataSourceTypeHubspot,
					c,
					false,
				)
				require.NoError(t, err)
				require.Equal(t, tt.wantRead, decodePayload(t, createPayload)["read_only"])
				fields := collectFormFields(c)
				require.Equal(t, tt.wantRead, fields.HubSpotReadOnly)

				var out strings.Builder
				require.NoError(t, views.DataSourceFields(fields, "").Render(context.Background(), &out))
				selectedValue := "write"
				if tt.wantRead {
					selectedValue = "read"
				}
				require.Contains(t, out.String(), `<option value="`+selectedValue+`" selected>`)
			})
		})
	}

	for _, readOnly := range []bool{true, false} {
		t.Run(fmt.Sprintf("RendersPersistedReadOnlyValue/%t", readOnly), func(t *testing.T) {
			t.Parallel()

			timeoutSeconds := 15
			var config api.DataSource_Config
			require.NoError(t, config.FromHubSpotConfig(api.HubSpotConfig{TimeoutSeconds: &timeoutSeconds}))
			dataSource := &api.DataSource{
				Name:       "HubSpot CRM",
				Slug:       "hubspot-crm",
				Type:       api.DataSourceTypeHubspot,
				Config:     config,
				ReadOnly:   readOnly,
				IsActive:   true,
				Credential: &api.CredentialMetadata{Kind: api.Oauth2Grant, Status: api.CredentialStatusActive},
			}

			formData := dataSourceUpdateFormDataFromDataSource(dataSource)
			require.Equal(t, readOnly, formData.HubSpotReadOnly)

			var out strings.Builder
			err := views.DataSourceDetailPageContent(
				"acme",
				"dev",
				dataSource,
				views.DefaultDataSourceSchemaPanelData(),
				nil,
				formData,
				views.NewOrganizationPermissions("datasources:update"), views.DataSourceOperationsPanelData{},
				"",
				"",
			).Render(views.WithEntityPageURL(context.Background(), "/?section=settings"), &out)
			require.NoError(t, err)

			html := out.String()
			require.Contains(t, html, `name="hubspot_access_mode"`)
			selectedValue := "write"
			if readOnly {
				selectedValue = "read"
			}
			require.Contains(t, html, `<option value="`+selectedValue+`" selected>`)
			require.Contains(t, html, "Reconnect HubSpot after saving")
		})
	}
}

func TestPostgresSSLModePayloads(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"", "disable", "allow", "prefer", "require", "verify-ca", "verify-full", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			form := databaseDataSourceFormValues(api.DataSourceTypePostgres)
			form.Set("sslmode", mode)
			withDatasourceFormContext(t, form, func(c fiber.Ctx) {
				create, message := buildCreateDataSourcePayloadWithPrivateHosts(c, true)
				update, updateMessage := buildUpdateDataSourcePayloadWithPrivateHosts(c, true)
				if mode == "invalid" {
					require.Equal(t, "Validation: invalid PostgreSQL SSL mode.", message)
					require.Equal(t, message, updateMessage)
					return
				}
				require.Empty(t, message)
				require.Empty(t, updateMessage)
				require.Equal(t, mode, dataSourceCreateFormDataFromRequest(c).SSLMode)
				draftTest, err := toTestDataSourceConnectionPayload(create)
				require.NoError(t, err)
				standalone, err := buildCreateDataSourceRequest("Database", "database", api.DataSourceTypePostgres, c, true)
				require.NoError(t, err)
				for _, payload := range []any{create, draftTest, update, standalone} {
					body, err := json.Marshal(payload)
					require.NoError(t, err)
					var decoded struct {
						Config map[string]any `json:"config"`
					}
					require.NoError(t, json.Unmarshal(body, &decoded))
					if mode == "" {
						require.NotContains(t, decoded.Config, "ssl_mode")
					} else {
						require.Equal(t, mode, decoded.Config["ssl_mode"])
					}
				}
				stored := &api.DataSource{Type: api.DataSourceTypePostgres}
				config, err := update.Config.AsPostgresConfig()
				require.NoError(t, err)
				require.NoError(t, stored.Config.FromPostgresConfig(config))
				require.Equal(t, mode, dataSourceUpdateFormDataFromDataSource(stored).SSLMode)
			})
		})
	}
}

func TestDatabaseDataSourcePrivateHostPayloads(t *testing.T) {
	t.Parallel()

	decodePayload := func(t *testing.T, payload any) map[string]any {
		t.Helper()

		body, err := json.Marshal(payload)
		require.NoError(t, err)

		var captured map[string]any
		require.NoError(t, json.Unmarshal(body, &captured))
		return captured
	}

	tests := []struct {
		name   string
		dsType api.DataSourceType
	}{
		{name: "Postgres", dsType: api.DataSourceTypePostgres},
		{name: "MySQL", dsType: api.DataSourceTypeMysql},
		{name: "MongoDB", dsType: api.DataSourceTypeMongodb},
	}

	for _, tt := range tests {
		t.Run(tt.name+"CreatePayloadIncludesPrivateHostsWhenPolicyEnabled", func(t *testing.T) {
			t.Parallel()

			withDatasourceFormContext(t, databaseDataSourceFormValues(tt.dsType), func(c fiber.Ctx) {
				payload, errMsg := buildCreateDataSourcePayloadWithPrivateHosts(c, true)
				require.Empty(t, errMsg)

				config, ok := decodePayload(t, payload)["config"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, true, config["allow_private_hosts"])

				testPayload, err := toTestDataSourceConnectionPayload(payload)
				require.NoError(t, err)
				testConfig, ok := decodePayload(t, testPayload)["config"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, true, testConfig["allow_private_hosts"])
			})
		})

		t.Run(tt.name+"CreatePayloadOmitsPrivateHostsWhenPolicyDisabled", func(t *testing.T) {
			t.Parallel()

			withDatasourceFormContext(t, databaseDataSourceFormValues(tt.dsType), func(c fiber.Ctx) {
				payload, errMsg := buildCreateDataSourcePayloadWithPrivateHosts(c, false)
				require.Empty(t, errMsg)

				config, ok := decodePayload(t, payload)["config"].(map[string]any)
				require.True(t, ok)
				require.NotContains(t, config, "allow_private_hosts")
			})
		})

		t.Run(tt.name+"UpdatePayloadIncludesPrivateHostsWhenPolicyEnabled", func(t *testing.T) {
			t.Parallel()

			withDatasourceFormContext(t, databaseDataSourceFormValues(tt.dsType), func(c fiber.Ctx) {
				payload, errMsg := buildUpdateDataSourcePayloadWithPrivateHosts(c, true)
				require.Empty(t, errMsg)

				config, ok := decodePayload(t, payload)["config"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, true, config["allow_private_hosts"])
			})
		})

		t.Run(tt.name+"UpdatePayloadOmitsPrivateHostsWhenPolicyDisabled", func(t *testing.T) {
			t.Parallel()

			withDatasourceFormContext(t, databaseDataSourceFormValues(tt.dsType), func(c fiber.Ctx) {
				payload, errMsg := buildUpdateDataSourcePayloadWithPrivateHosts(c, false)
				require.Empty(t, errMsg)

				config, ok := decodePayload(t, payload)["config"].(map[string]any)
				require.True(t, ok)
				require.NotContains(t, config, "allow_private_hosts")
			})
		})
	}
}

func TestPrivateHostControlsRespectDeploymentPolicy(t *testing.T) {
	t.Parallel()

	renderListCreateForm := func(t *testing.T, enabled bool) string {
		t.Helper()

		formData := views.DataSourceCreateFormData{
			Type:                        "git",
			GitAuthType:                 gitAuthTypeHTTPS,
			GitRepoURL:                  "https://github.com/acme/repo.git",
			GitAllowPrivateHosts:        true,
			DatabaseAllowPrivateHosts:   true,
			HTTPAllowPrivateHosts:       true,
			AllowPrivateDataSourceHosts: enabled,
		}

		var out strings.Builder
		err := views.DataSourcesPageContent(
			nil,
			nil,
			false,
			"",
			"",
			"acme",
			"dev",
			"",
			nil,
			views.DefaultDataSourceSchemaPanelData(),
			nil,
			views.DataSourceUpdateFormData{},
			formData,
			views.NewOrganizationPermissions("datasources:create"),
			"",
			"",
			views.DataSourcePageFeedback{},
		).Render(context.Background(), &out)
		require.NoError(t, err)
		return out.String()
	}

	renderStandaloneFields := func(t *testing.T, dsType api.DataSourceType, enabled bool) string {
		t.Helper()

		fields := views.DataSourceFormFields{
			DSType:                      dsType,
			HTTPAllowPrivateHosts:       true,
			GitAllowPrivateHosts:        true,
			DatabaseAllowPrivateHosts:   true,
			AllowPrivateDataSourceHosts: enabled,
		}

		var out strings.Builder
		err := views.DataSourceFields(fields, "").Render(context.Background(), &out)
		require.NoError(t, err)
		return out.String()
	}

	renderDetailForm := func(t *testing.T, dsType api.DataSourceType, formData views.DataSourceUpdateFormData, enabled bool) string {
		t.Helper()

		formData.AllowPrivateDataSourceHosts = enabled
		dataSource := &api.DataSource{
			Name:     "Data Source",
			Slug:     "data-source",
			Type:     dsType,
			IsActive: true,
		}

		var out strings.Builder
		err := views.DataSourceDetailPageContent(
			"acme",
			"dev",
			dataSource,
			views.DefaultDataSourceSchemaPanelData(),
			nil,
			formData,
			views.NewOrganizationPermissions("datasources:update"), views.DataSourceOperationsPanelData{},
			"",
			"",
		).Render(views.WithEntityPageURL(context.Background(), "/?section=settings"), &out)
		require.NoError(t, err)
		return out.String()
	}

	listCreateCases := []struct {
		name        string
		enabled     bool
		contains    []string
		notContains []string
	}{
		{
			name:        "ListCreateGitPolicyDisabled",
			enabled:     false,
			notContains: []string{`name="git_allow_private_hosts"`},
		},
		{
			name:     "ListCreateGitPolicyEnabled",
			enabled:  true,
			contains: []string{`name="git_allow_private_hosts"`},
		},
		{
			name:        "ListCreateHTTPPolicyDisabled",
			enabled:     false,
			notContains: []string{`name="http_allow_private_hosts"`},
		},
		{
			name:     "ListCreateHTTPPolicyEnabled",
			enabled:  true,
			contains: []string{`name="http_allow_private_hosts"`},
		},
		{
			name:        "ListCreateDatabasePolicyDisabled",
			enabled:     false,
			notContains: []string{`name="database_allow_private_hosts"`},
		},
		{
			name:     "ListCreateDatabasePolicyEnabled",
			enabled:  true,
			contains: []string{`name="database_allow_private_hosts"`},
		},
	}
	for _, tt := range listCreateCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := renderListCreateForm(t, tt.enabled)
			for _, want := range tt.contains {
				require.Contains(t, body, want)
			}
			for _, unwanted := range tt.notContains {
				require.NotContains(t, body, unwanted)
			}
		})
	}

	standaloneFieldCases := []struct {
		name        string
		dsType      api.DataSourceType
		enabled     bool
		fieldName   string
		wantPresent bool
	}{
		{
			name:      "StandaloneFieldsHTTPPolicyDisabled",
			dsType:    api.DataSourceTypeHttp,
			enabled:   false,
			fieldName: `name="http_allow_private_hosts"`,
		},
		{
			name:        "StandaloneFieldsHTTPPolicyEnabled",
			dsType:      api.DataSourceTypeHttp,
			enabled:     true,
			fieldName:   `name="http_allow_private_hosts"`,
			wantPresent: true,
		},
		{
			name:      "StandaloneFieldsGitPolicyDisabled",
			dsType:    api.DataSourceTypeGit,
			enabled:   false,
			fieldName: `name="git_allow_private_hosts"`,
		},
		{
			name:        "StandaloneFieldsGitPolicyEnabled",
			dsType:      api.DataSourceTypeGit,
			enabled:     true,
			fieldName:   `name="git_allow_private_hosts"`,
			wantPresent: true,
		},
		{
			name:      "StandaloneFieldsPostgresPolicyDisabled",
			dsType:    api.DataSourceTypePostgres,
			enabled:   false,
			fieldName: `name="database_allow_private_hosts"`,
		},
		{
			name:        "StandaloneFieldsPostgresPolicyEnabled",
			dsType:      api.DataSourceTypePostgres,
			enabled:     true,
			fieldName:   `name="database_allow_private_hosts"`,
			wantPresent: true,
		},
		{
			name:      "StandaloneFieldsMySQLPolicyDisabled",
			dsType:    api.DataSourceTypeMysql,
			enabled:   false,
			fieldName: `name="database_allow_private_hosts"`,
		},
		{
			name:        "StandaloneFieldsMySQLPolicyEnabled",
			dsType:      api.DataSourceTypeMysql,
			enabled:     true,
			fieldName:   `name="database_allow_private_hosts"`,
			wantPresent: true,
		},
		{
			name:      "StandaloneFieldsMongoDBPolicyDisabled",
			dsType:    api.DataSourceTypeMongodb,
			enabled:   false,
			fieldName: `name="database_allow_private_hosts"`,
		},
		{
			name:        "StandaloneFieldsMongoDBPolicyEnabled",
			dsType:      api.DataSourceTypeMongodb,
			enabled:     true,
			fieldName:   `name="database_allow_private_hosts"`,
			wantPresent: true,
		},
	}
	for _, tt := range standaloneFieldCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := renderStandaloneFields(t, tt.dsType, tt.enabled)
			if tt.wantPresent {
				require.Contains(t, body, tt.fieldName)
				return
			}
			require.NotContains(t, body, tt.fieldName)
		})
	}

	detailCases := []struct {
		name        string
		dsType      api.DataSourceType
		formData    views.DataSourceUpdateFormData
		enabled     bool
		contains    []string
		notContains []string
	}{
		{
			name:   "DetailHTTPPolicyDisabled",
			dsType: api.DataSourceTypeHttp,
			formData: views.DataSourceUpdateFormData{
				Type:                  "http",
				Name:                  "Customer API",
				BaseURL:               "https://api.example.com",
				HTTPAllowPrivateHosts: true,
			},
			enabled:     false,
			notContains: []string{`name="http_allow_private_hosts"`},
		},
		{
			name:   "DetailHTTPPolicyEnabled",
			dsType: api.DataSourceTypeHttp,
			formData: views.DataSourceUpdateFormData{
				Type:                  "http",
				Name:                  "Customer API",
				BaseURL:               "https://api.example.com",
				HTTPAllowPrivateHosts: true,
			},
			enabled:  true,
			contains: []string{`name="http_allow_private_hosts"`, `name="http_allow_private_hosts" checked`},
		},
		{
			name:   "DetailS3PolicyDisabled",
			dsType: api.DataSourceTypeS3,
			formData: views.DataSourceUpdateFormData{
				Type:                "s3",
				Name:                "Object Store",
				S3Region:            "us-east-1",
				S3Bucket:            "fixtures",
				S3AllowPrivateHosts: true,
			},
			enabled:     false,
			notContains: []string{`name="allow_private_hosts"`},
		},
		{
			name:   "DetailS3PolicyEnabled",
			dsType: api.DataSourceTypeS3,
			formData: views.DataSourceUpdateFormData{
				Type:                "s3",
				Name:                "Object Store",
				S3Region:            "us-east-1",
				S3Bucket:            "fixtures",
				S3AllowPrivateHosts: true,
			},
			enabled:  true,
			contains: []string{`name="allow_private_hosts"`},
		},
		{
			name:   "DetailGitPolicyDisabled",
			dsType: api.DataSourceTypeGit,
			formData: views.DataSourceUpdateFormData{
				Type:                 "git",
				Name:                 "Repo",
				GitRepoURL:           "https://github.com/acme/repo.git",
				GitAllowPrivateHosts: true,
			},
			enabled:     false,
			notContains: []string{`name="git_allow_private_hosts"`},
		},
		{
			name:   "DetailGitPolicyEnabled",
			dsType: api.DataSourceTypeGit,
			formData: views.DataSourceUpdateFormData{
				Type:                 "git",
				Name:                 "Repo",
				GitRepoURL:           "https://github.com/acme/repo.git",
				GitAllowPrivateHosts: true,
			},
			enabled:  true,
			contains: []string{`name="git_allow_private_hosts"`},
		},
		{
			name:   "DetailPostgresPolicyDisabled",
			dsType: api.DataSourceTypePostgres,
			formData: views.DataSourceUpdateFormData{
				Type:                      "postgres",
				Name:                      "Main DB",
				Host:                      "db.example.com",
				Port:                      "5432",
				Database:                  "app",
				DatabaseAllowPrivateHosts: true,
			},
			enabled:     false,
			notContains: []string{`name="database_allow_private_hosts"`},
		},
		{
			name:   "DetailPostgresPolicyEnabled",
			dsType: api.DataSourceTypePostgres,
			formData: views.DataSourceUpdateFormData{
				Type:                      "postgres",
				Name:                      "Main DB",
				Host:                      "db.example.com",
				Port:                      "5432",
				Database:                  "app",
				DatabaseAllowPrivateHosts: true,
			},
			enabled:  true,
			contains: []string{`name="database_allow_private_hosts"`},
		},
		{
			name:   "DetailMySQLPolicyDisabled",
			dsType: api.DataSourceTypeMysql,
			formData: views.DataSourceUpdateFormData{
				Type:                      "mysql",
				Name:                      "Main DB",
				Host:                      "db.example.com",
				Port:                      "3306",
				Database:                  "app",
				DatabaseAllowPrivateHosts: true,
			},
			enabled:     false,
			notContains: []string{`name="database_allow_private_hosts"`},
		},
		{
			name:   "DetailMySQLPolicyEnabled",
			dsType: api.DataSourceTypeMysql,
			formData: views.DataSourceUpdateFormData{
				Type:                      "mysql",
				Name:                      "Main DB",
				Host:                      "db.example.com",
				Port:                      "3306",
				Database:                  "app",
				DatabaseAllowPrivateHosts: true,
			},
			enabled:  true,
			contains: []string{`name="database_allow_private_hosts"`},
		},
		{
			name:   "DetailMongoDBPolicyDisabled",
			dsType: api.DataSourceTypeMongodb,
			formData: views.DataSourceUpdateFormData{
				Type:                      "mongodb",
				Name:                      "Main DB",
				Host:                      "db.example.com",
				Port:                      "27017",
				Database:                  "app",
				DatabaseAllowPrivateHosts: true,
			},
			enabled:     false,
			notContains: []string{`name="database_allow_private_hosts"`},
		},
		{
			name:   "DetailMongoDBPolicyEnabled",
			dsType: api.DataSourceTypeMongodb,
			formData: views.DataSourceUpdateFormData{
				Type:                      "mongodb",
				Name:                      "Main DB",
				Host:                      "db.example.com",
				Port:                      "27017",
				Database:                  "app",
				DatabaseAllowPrivateHosts: true,
			},
			enabled:  true,
			contains: []string{`name="database_allow_private_hosts"`},
		},
	}
	for _, tt := range detailCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := renderDetailForm(t, tt.dsType, tt.formData, tt.enabled)
			for _, want := range tt.contains {
				require.Contains(t, body, want)
			}
			for _, unwanted := range tt.notContains {
				require.NotContains(t, body, unwanted)
			}
		})
	}
}

func TestCollectFormFieldsPreservesHTTPPrivateHostState(t *testing.T) {
	t.Parallel()

	form := url.Values{}
	form.Set("type", "http")
	form.Set("base_url", "https://api.example.com")
	form.Set("http_allow_private_hosts", "true")

	withDatasourceFormContext(t, form, func(c fiber.Ctx) {
		fields := collectFormFields(c)
		require.True(t, fields.HTTPAllowPrivateHosts)
	})
}

func TestCollectFormFieldsPreservesDatabasePrivateHostState(t *testing.T) {
	t.Parallel()

	form := url.Values{}
	form.Set("type", "postgres")
	form.Set("host", "db.example.com")
	form.Set(databaseAllowPrivateHostsField, "true")

	withDatasourceFormContext(t, form, func(c fiber.Ctx) {
		fields := collectFormFields(c)
		require.True(t, fields.DatabaseAllowPrivateHosts)
	})
}

func TestAllowPrivateHostsFormPointer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		form        url.Values
		policy      bool
		wantNil     bool
		wantPointer bool
	}{
		{
			name: "PolicyDisabledDropsSubmittedValue",
			form: url.Values{
				"http_allow_private_hosts": {"on"},
			},
			policy:  false,
			wantNil: true,
		},
		{
			name: "HTTPFieldSetsTrue",
			form: url.Values{
				"http_allow_private_hosts": {"on"},
			},
			policy:      true,
			wantPointer: true,
		},
		{
			name: "LegacyFallbackSetsTrue",
			form: url.Values{
				"allow_private_hosts": {"true"},
			},
			policy:      true,
			wantPointer: true,
		},
		{
			name:    "BlankInputReturnsNil",
			form:    url.Values{},
			policy:  true,
			wantNil: true,
		},
		{
			name: "HTTPFieldSetsFalse",
			form: url.Values{
				"http_allow_private_hosts": {"false"},
			},
			policy:      true,
			wantPointer: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			withDatasourceFormContext(t, tt.form, func(c fiber.Ctx) {
				got := allowPrivateHostsFormPointer(c, tt.policy)
				if tt.wantNil {
					require.Nil(t, got)
					return
				}
				require.NotNil(t, got)
				require.Equal(t, tt.wantPointer, *got)
			})
		})
	}
}

func TestPrivateHostsFormPointer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		form        url.Values
		policy      bool
		wantNil     bool
		wantPointer bool
	}{
		{
			name: "PolicyDisabledDropsSubmittedValue",
			form: url.Values{
				databaseAllowPrivateHostsField: {"on"},
			},
			policy:  false,
			wantNil: true,
		},
		{
			name: "FieldSetsTrue",
			form: url.Values{
				databaseAllowPrivateHostsField: {"on"},
			},
			policy:      true,
			wantPointer: true,
		},
		{
			name:        "BlankInputSetsFalseWhenPolicyEnabled",
			form:        url.Values{},
			policy:      true,
			wantPointer: false,
		},
		{
			name: "FieldSetsFalse",
			form: url.Values{
				databaseAllowPrivateHostsField: {"false"},
			},
			policy:      true,
			wantPointer: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			withDatasourceFormContext(t, tt.form, func(c fiber.Ctx) {
				got := privateHostsFormPointer(c, databaseAllowPrivateHostsField, tt.policy)
				if tt.wantNil {
					require.Nil(t, got)
					return
				}
				require.NotNil(t, got)
				require.Equal(t, tt.wantPointer, *got)
			})
		})
	}
}

func TestGitDataSourceFormsDoNotEchoCredentials(t *testing.T) {
	t.Parallel()

	const (
		gitToken      = "git-redacted-token-value"
		gitPrivateKey = "git-redacted-private-key-value"
		gitPassphrase = "git-redacted-passphrase-value"
	)

	assertNoGitSecrets := func(t *testing.T, body string) {
		t.Helper()

		require.Contains(t, body, `name="git_token"`)
		require.Contains(t, body, `name="git_ssh_private_key"`)
		require.Contains(t, body, `name="git_ssh_passphrase"`)
		require.NotContains(t, body, gitToken)
		require.NotContains(t, body, gitPrivateKey)
		require.NotContains(t, body, gitPassphrase)
	}

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "CreateStickyStateDropsGitSecrets",
			run: func(t *testing.T) {
				withDatasourceFormContext(t, gitDataSourceFormValues(), func(c fiber.Ctx) {
					formData := dataSourceCreateFormDataFromRequest(c)
					require.Empty(t, formData.GitToken)
					require.Empty(t, formData.GitPrivateKey)
					require.Empty(t, formData.GitPassphrase)
					require.NotEmpty(t, formData.GitKnownHosts)
				})
			},
		},
		{
			name: "TestConnectionStickyStateDropsGitSecrets",
			run: func(t *testing.T) {
				form := gitDataSourceFormValues()
				form.Set("git_token", " git-redacted-token-value ")
				form.Set("git_ssh_private_key", "git-redacted-private-key-value\n")
				form.Set("git_ssh_passphrase", " git-redacted-passphrase-value ")

				withDatasourceFormContext(t, form, func(c fiber.Ctx) {
					formData := (&Router{}).dataSourceCreateFormDataFromRequest(c)
					require.Empty(t, formData.GitToken)
					require.Empty(t, formData.GitPrivateKey)
					require.Empty(t, formData.GitPassphrase)
					require.NotEmpty(t, formData.GitKnownHosts)
				})
			},
		},
		{
			name: "FieldStickyStateDropsGitSecrets",
			run: func(t *testing.T) {
				withDatasourceFormContext(t, gitDataSourceFormValues(), func(c fiber.Ctx) {
					formData := collectFormFields(c)
					require.Empty(t, formData.GitToken)
					require.Empty(t, formData.GitSSHPrivateKey)
					require.Empty(t, formData.GitSSHPassphrase)
					require.NotEmpty(t, formData.GitSSHKnownHosts)
				})
			},
		},
		{
			name: "RenderedFieldsDoNotEchoGitSecrets",
			run: func(t *testing.T) {
				fields := views.DataSourceFormFields{
					DSType:           api.DataSourceTypeGit,
					GitAuthType:      gitAuthTypeSSH,
					GitRepoURL:       "git@github.com:acme/automation.git",
					GitToken:         gitToken,
					GitSSHPrivateKey: gitPrivateKey,
					GitSSHKnownHosts: "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample",
					GitSSHPassphrase: gitPassphrase,
				}

				var out strings.Builder
				err := views.DataSourceFields(fields, "Validation: repository URL is required.").
					Render(context.Background(), &out)
				require.NoError(t, err)

				assertNoGitSecrets(t, out.String())
			},
		},
		{
			name: "RenderedCreatePageDoNotEchoGitSecrets",
			run: func(t *testing.T) {
				fields := views.DataSourceFormFields{
					DSType:           api.DataSourceTypeGit,
					GitAuthType:      gitAuthTypeSSH,
					GitRepoURL:       "git@github.com:acme/automation.git",
					GitToken:         gitToken,
					GitSSHPrivateKey: gitPrivateKey,
					GitSSHKnownHosts: "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample",
					GitSSHPassphrase: gitPassphrase,
				}

				var out strings.Builder
				err := views.DataSourceNewPage(
					"Add data source",
					"acme",
					fields,
					"",
					"",
					"",
				).Render(context.Background(), &out)
				require.NoError(t, err)

				assertNoGitSecrets(t, out.String())
			},
		},
		{
			name: "RenderedListCreateFormDoNotEchoGitSecrets",
			run: func(t *testing.T) {
				formData := views.DataSourceCreateFormData{
					Name:                        "Automation Repo",
					Type:                        "git",
					GitAuthType:                 gitAuthTypeSSH,
					GitRepoURL:                  "git@github.com:acme/automation.git",
					GitToken:                    gitToken,
					GitPrivateKey:               gitPrivateKey,
					GitKnownHosts:               "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample",
					GitPassphrase:               gitPassphrase,
					AllowPrivateDataSourceHosts: true,
				}

				var out strings.Builder
				err := views.DataSourcesPageContent(
					nil,
					nil,
					false,
					"",
					"",
					"acme",
					"dev",
					"",
					nil,
					views.DefaultDataSourceSchemaPanelData(),
					nil,
					views.DataSourceUpdateFormData{},
					formData,
					views.NewOrganizationPermissions("datasources:create"),
					"",
					"",
					views.DataSourcePageFeedback{},
				).Render(context.Background(), &out)
				require.NoError(t, err)

				assertNoGitSecrets(t, out.String())
			},
		},
		{
			name: "RenderedDetailCredentialFormDoNotEchoGitSecrets",
			run: func(t *testing.T) {
				dataSource := &api.DataSource{
					Name:     "Automation Repo",
					Slug:     "automation-repo",
					Type:     api.DataSourceTypeGit,
					IsActive: true,
				}
				formData := views.DataSourceUpdateFormData{
					Name:                 "Automation Repo",
					Type:                 "git",
					GitAuthType:          gitAuthTypeSSH,
					GitRepoURL:           "git@github.com:acme/automation.git",
					GitPrivateKey:        gitPrivateKey,
					GitKnownHosts:        "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample",
					GitPassphrase:        gitPassphrase,
					GitAllowPrivateHosts: true,
				}

				var out strings.Builder
				err := views.DataSourceDetailPageContent(
					"acme",
					"dev",
					dataSource,
					views.DefaultDataSourceSchemaPanelData(),
					nil,
					formData,
					views.NewOrganizationPermissions("datasources:update"), views.DataSourceOperationsPanelData{},
					"",
					"",
				).Render(views.WithEntityPageURL(context.Background(), "/?section=settings"), &out)
				require.NoError(t, err)

				assertNoGitSecrets(t, out.String())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.run(t)
		})
	}
}

func TestGitDataSourceCredentialPayloadsPreserveSSHPrivateKeyNewline(t *testing.T) {
	t.Parallel()

	const (
		privateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\n"
		passphrase = " leading and trailing passphrase \n"
	)

	decodePayload := func(t *testing.T, payload any) map[string]any {
		t.Helper()

		body, err := json.Marshal(payload)
		require.NoError(t, err)

		var captured map[string]any
		require.NoError(t, json.Unmarshal(body, &captured))
		return captured
	}
	assertSSHCredentials := func(t *testing.T, captured map[string]any) {
		t.Helper()

		credentials, ok := captured["credentials"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, privateKey, credentials["ssh_private_key"])
		require.Equal(t, passphrase, credentials["ssh_passphrase"])
	}

	for _, dsType := range []string{"git"} {
		t.Run(dsType, func(t *testing.T) {
			t.Parallel()

			form := gitDataSourceFormValues()
			form.Set("type", dsType)
			form.Set("git_ssh_private_key", privateKey)
			form.Set("git_ssh_passphrase", passphrase)

			withDatasourceFormContext(t, form, func(c fiber.Ctx) {
				createPayload, errMsg := buildCreateDataSourcePayloadWithPrivateHosts(c, false)
				require.Empty(t, errMsg)
				assertSSHCredentials(t, decodePayload(t, createPayload))

				testPayload, err := toTestDataSourceConnectionPayload(createPayload)
				require.NoError(t, err)
				assertSSHCredentials(t, decodePayload(t, testPayload))

				credentialsPayload, errMsg := buildUpdateDataSourceCredentialsPayloadForType(
					c,
					api.DataSourceType(dsType),
					form.Get("git_repo_url"),
				)
				require.Empty(t, errMsg)
				assertSSHCredentials(t, decodePayload(t, credentialsPayload))
			})
		})
	}
}

// createDataSourcePanel returns the create form fragment that HTMX swaps on create/test submissions.
func createDataSourcePanel(t *testing.T, body string) string {
	t.Helper()

	idIndex := strings.Index(body, `id="create-data-source"`)
	require.NotEqual(t, -1, idIndex)

	start := strings.LastIndex(body[:idIndex], "<form")
	require.NotEqual(t, -1, start)

	end := strings.Index(body[idIndex:], "</form>")
	require.NotEqual(t, -1, end)

	return body[start : idIndex+end+len("</form>")]
}

func requireDataSourceListRedirect(
	t *testing.T,
	location string,
	organizationSlug string,
	environmentSlug string,
	dataSourceSlug string,
) {
	t.Helper()

	parsed, err := url.Parse(location)
	require.NoError(t, err)
	require.Equal(t, "/orgs/"+organizationSlug+"/data-sources", parsed.Path)

	query := parsed.Query()
	require.Equal(t, environmentSlug, query.Get("environment_slug"))
	require.Equal(t, dataSourceSlug, query.Get("selected"))
}

func TestValidatedDataSourceOAuthFeedbackRequiresActiveHubSpotGrantForSuccess(t *testing.T) {
	t.Parallel()

	activeOAuthCredential := &api.CredentialMetadata{
		Kind:   api.Oauth2Grant,
		Status: api.CredentialStatusActive,
	}
	for _, test := range []struct {
		name       string
		outcome    string
		dataSource *api.DataSource
		want       string
	}{
		{
			name:    "RejectsSuccessWithoutSelectedSource",
			outcome: "success",
		},
		{
			name:    "RejectsSuccessForAnotherConnector",
			outcome: "success",
			dataSource: &api.DataSource{
				Type:       api.DataSourceTypePostgres,
				IsActive:   true,
				Credential: activeOAuthCredential,
			},
		},
		{
			name:    "RejectsSuccessForPendingGrant",
			outcome: "success",
			dataSource: &api.DataSource{
				Type:     api.DataSourceTypeHubspot,
				IsActive: false,
				Credential: &api.CredentialMetadata{
					Kind:   api.Oauth2Grant,
					Status: api.CredentialStatusPending,
				},
			},
		},
		{
			name:    "RejectsSuccessForStaticCredential",
			outcome: "success",
			dataSource: &api.DataSource{
				Type:     api.DataSourceTypeHubspot,
				IsActive: true,
				Credential: &api.CredentialMetadata{
					Kind:   api.StaticSecret,
					Status: api.CredentialStatusActive,
				},
			},
		},
		{
			name:    "AcceptsSuccessForActiveHubSpotGrant",
			outcome: "success",
			dataSource: &api.DataSource{
				Type:       api.DataSourceTypeHubspot,
				IsActive:   true,
				Credential: activeOAuthCredential,
			},
			want: "HubSpot connected successfully.",
		},
		{
			name:    "RequestsGitLabProjectSelectionAfterAuthorization",
			outcome: "select_repository",
			dataSource: &api.DataSource{
				Slug:       "pipelines",
				Type:       api.DataSourceTypeGitlab,
				IsActive:   false,
				Credential: activeOAuthCredential,
			},
			want: "GitLab authorization succeeded. Choose the project for this data source.",
		},
		{
			name:    "PreservesDeniedFeedback",
			outcome: "denied",
			want:    "HubSpot connection was canceled. You can try again when you are ready.",
		},
		{
			name:    "PreservesExpiredFeedback",
			outcome: "expired",
			want:    "HubSpot authorization expired. Start the connection again to continue.",
		},
		{
			name:    "PreservesErrorFeedback",
			outcome: "error",
			want:    "HubSpot could not be connected. Check the data source and try again.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			feedback := validatedDataSourceOAuthFeedback(test.outcome, test.dataSource)

			assert.Equal(t, test.want, feedback.Message)
		})
	}
}

func TestRouter_HandleDataSourcesPage_RendersList(t *testing.T) {
	t.Parallel()

	var schemaCalls atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources/prod-db":
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources/prod-db/schema":
			schemaCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write(
				[]byte(
					`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects/default/environments":
			_, _ = w.Write(
				[]byte(
					`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":"Developer target","is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"},{"environment_slug":"prod","environment_name":"Production","environment_description":"Customer target","is_default":false,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/data-sources?selected=prod-db&provider_oauth=success", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Connector inventory")
	require.Contains(t, string(body), "Acme HQ")
	require.Contains(t, string(body), `<span class="sr-only">Switch organization: </span>`)
	require.Contains(t, string(body), `href="/organizations/acme-hq"`)
	require.Contains(t, string(body), `aria-label="Breadcrumb"`)
	require.Contains(t, string(body), `href="/dashboard"`)
	require.Contains(t, string(body), "Production DB")
	require.NotContains(t, string(body), "HubSpot connected successfully.")
	require.NotContains(t, string(body), `data-provider-oauth-feedback`)
	require.Contains(t, string(body), `name="hubspot_auth_type"`)
	require.Contains(t, string(body), `<option value="oauth2" selected>OAuth 2.0 (recommended)</option>`)
	require.Contains(t, string(body), `data-provider="hubspot" data-provider-auth-fields="private_app"`)
	require.Contains(t, string(body), `>Connect HubSpot</button>`)
	require.NotContains(t, string(body), `name="gitlab_auth_type"`)
	require.Contains(t, string(body), `>Manage provider connections</a>`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/data-sources?environment_slug=dev&amp;selected=prod-db"`)
	require.Contains(t, string(body), `id="data-sources-browser"`)
	require.Contains(t, string(body), `hx-target="#data-source-side-panel"`)
	require.Contains(t, string(body), `hx-swap="outerHTML show:none focus-scroll:false"`)
	require.Contains(t, string(body), `data-preserve-scroll`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/data-sources/prod-db?environment_slug=dev&amp;list_return=`)
	require.NotContains(t, string(body), "/orgs/acme-hq/queries?ds=prod-db&amp;environment_slug=dev")
	require.Contains(t, string(body), "Open full page")
	require.NotContains(t, string(body), "Schema browser")
	require.NotContains(t, string(body), "Save connection settings")
	require.Contains(t, string(body), `name="environment_slug" value="dev"`)
	require.NotContains(t, string(body), `hx-confirm="Delete data source &#34;Production DB (prod-db)&#34;? It will no longer be available for workflows."`)
	require.NotContains(t, string(body), `data-destructive-confirm="Delete data source &#34;Production DB (prod-db)&#34;? It will no longer be available for workflows."`)
	require.NotContains(t, string(body), `hx-confirm="Rotate credentials for data source &#34;Production DB (prod-db)&#34;? The current credentials will be replaced."`)
	require.NotContains(t, string(body), `data-destructive-confirm="Rotate credentials for data source &#34;Production DB (prod-db)&#34;? The current credentials will be replaced."`)
	require.NotContains(t, string(body), ">Details<")
	require.Contains(t, string(body), `id="environment-switcher"`)
	require.Contains(t, string(body), `option selected value="/orgs/acme-hq/data-sources?environment_slug=dev&amp;selected=prod-db"`)
	require.NotContains(t, string(body), ">No runtime<")
	require.Equal(t, int32(0), schemaCalls.Load())
}

func TestRouter_HandleDataSourcesPage_FiltersQueryCoverage(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"read_only":false,"last_tested_at":"2026-03-01T00:00:00Z","test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},{"slug":"archive-export","name":"Archive export","type":"s3","config":{"type":"s3"},"is_active":false,"read_only":true,"last_tested_at":null,"test_error":"missing credentials","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources/prod-db/queries":
			_, _ = w.Write(
				[]byte(
					`{"queries":[{"name":"Recent orders","slug":"recent-orders","data_source_slug":"prod-db","description":"recent orders","parameters":[],"query_statement":{"sql":"select * from orders"},"row_limit":100,"timeout_seconds":30,"created_at":"2026-03-03T08:15:00Z","updated_at":"2026-03-03T08:15:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources/archive-export/queries":
			_, _ = w.Write([]byte(`{"queries":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
		case "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write(
				[]byte(
					`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects/default/environments":
			_, _ = w.Write(
				[]byte(
					`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":"Developer target","is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/data-sources?filter=without_queries", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Sources without queries")
	require.Contains(t, html, "1 shown")
	require.Contains(t, html, "Archive export")
	require.NotContains(t, html, "Production DB")
	require.Contains(t, html, "Inventory view")
	require.Contains(t, html, `method="get" action="/orgs/acme-hq/data-sources"`)
	require.Contains(t, html, `hx-target="#data-sources-browser"`)
	require.Contains(t, html, `hx-swap="outerHTML show:none focus-scroll:false"`)
	require.Contains(t, html, `data-preserve-scroll`)
	require.Contains(t, html, `name="environment_slug" value="dev"`)
	require.Contains(t, html, `<option selected value="without_queries">No queries</option>`)
	require.Contains(t, html, `href="/orgs/acme-hq/data-sources?environment_slug=dev"`)
	require.Contains(t, html, "filters active")
	require.Contains(t, html, `href="/orgs/acme-hq/data-sources?environment_slug=dev&amp;filter=without_queries&amp;selected=archive-export"`)
}

func TestRouter_HandleDataSourcesPage_RendersSelectedLoadFailureInSidePanel(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources/missing-source":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"UPSTREAM_FAILED","message":"data source details unavailable","detailed_error":"backend could not load the selected data source"}}`,
				),
			)
		case "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write(
				[]byte(
					`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects/default/environments":
			_, _ = w.Write(
				[]byte(
					`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":"Developer target","is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	var logBuffer bytes.Buffer
	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.NewJSONHandler(&logBuffer, nil)), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/data-sources?selected=missing-source", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyText := string(body)
	sidePanelIndex := strings.Index(bodyText, `id="data-source-side-panel"`)
	require.NotEqual(t, -1, sidePanelIndex)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, bodyText[sidePanelIndex:], "data source details unavailable")
	require.NotContains(t, bodyText[sidePanelIndex:], "Select a data source")
	require.Contains(t, logBuffer.String(), `"braidplane.event":"frontend.backend_api.rendered_failure"`)
	require.Contains(t, logBuffer.String(), `"action":"get data source"`)
	require.Contains(t, logBuffer.String(), `"error.category":"upstream_response"`)
	require.Contains(t, logBuffer.String(), `"upstream.status_code":502`)
	require.Contains(t, logBuffer.String(), `"organization.slug":"acme-hq"`)
	require.Contains(t, logBuffer.String(), `"data_source.slug":"missing-source"`)
}

func TestRouter_HandleDataSourcesPage_LogsRenderedListFailureWithCorrelation(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/organizations/acme-hq/data-sources":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"code":"UPSTREAM_FAILED","message":"secret-list-message","detailed_error":"token=secret-list-token"}}`))
		case "/api/organizations":
			_, _ = w.Write([]byte(`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write([]byte(`{"projects":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	traceID, err := trace.TraceIDFromHex("4db57c86403bd979b96119f226f33455")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID})

	var logBuffer bytes.Buffer
	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.NewJSONHandler(&logBuffer, nil)), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	app.Use(func(c fiber.Ctx) error {
		ctx := reqctx.WithRequestID(c.Context(), "req-rendered-list-failure")
		c.SetContext(trace.ContextWithSpanContext(ctx, spanContext))
		return c.Next()
	})
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/orgs/acme-hq/data-sources", nil)
	req.Header.Set("Cookie", authCookie)
	req.Header.Set(fiber.HeaderXRequestID, "req-rendered-list-failure")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)

	require.Contains(t, logBuffer.String(), `"braidplane.event":"frontend.backend_api.rendered_failure"`)
	require.Contains(t, logBuffer.String(), `"action":"list data sources"`)
	require.Contains(t, logBuffer.String(), `"error.category":"upstream_response"`)
	require.Contains(t, logBuffer.String(), `"upstream.status_code":502`)
	require.Contains(t, logBuffer.String(), `"request_id":"req-rendered-list-failure"`)
	require.Contains(t, logBuffer.String(), `"trace_id":"4db57c86403bd979b96119f226f33455"`)
	require.NotContains(t, logBuffer.String(), "secret-list-message")
	require.NotContains(t, logBuffer.String(), "secret-list-token")
}

func TestRouter_HandleDataSourcesPage_LogsFilteredQueryInventoryFailure(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write([]byte(`{"data_sources":[{"slug":"hubspot-crm","name":"HubSpot CRM","type":"hubspot","config":{"type":"hubspot"},"is_active":true,"read_only":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
		case "/api/organizations/acme-hq/data-sources/hubspot-crm/queries":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"code":"UPSTREAM_FAILED","message":"query inventory unavailable","detailed_error":"access_token=secret-query-token"}}`))
		case "/api/organizations":
			_, _ = w.Write([]byte(`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write([]byte(`{"projects":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	var logBuffer bytes.Buffer
	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.NewJSONHandler(&logBuffer, nil)), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/orgs/acme-hq/data-sources?filter=with_queries", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)

	require.Contains(t, logBuffer.String(), `"braidplane.event":"frontend.backend_api.rendered_failure"`)
	require.Contains(t, logBuffer.String(), `"action":"list queries"`)
	require.Contains(t, logBuffer.String(), `"error.category":"upstream_response"`)
	require.Contains(t, logBuffer.String(), `"upstream.status_code":502`)
	require.Contains(t, logBuffer.String(), `"diagnostic.code":"UPSTREAM_FAILED"`)
	require.NotContains(t, logBuffer.String(), "query inventory unavailable")
	require.NotContains(t, logBuffer.String(), "secret-query-token")
}

func TestRouter_HandleDataSourcesPage_RendersHTTPOpenAPIImportFields(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write(
				[]byte(
					`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/data-sources", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyText := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, bodyText, `name="openapi_url"`)
	require.Contains(t, bodyText, `name="openapi_fetch_timeout_seconds"`)
	require.Contains(t, bodyText, "OpenAPI spec URL")
	require.Contains(t, bodyText, "OpenAPI fetch timeout")
	require.Contains(t, bodyText, "Import operations from an OpenAPI document")
}

func TestRouter_HandleDataSourceDetailPage_RendersSQLSchema(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"schema_status":"ready","schema":{"schema_type":"sql","type":"postgres","tables":[{"name":"users","columns":[{"name":"id","type":"int8"},{"name":"email","type":"text"}]},{"name":"organizations","columns":[]}]},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/data-sources/prod-db?section=schema&notice=Connection+test+passed.",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Data source details")
	require.Contains(t, string(body), "Production DB")
	require.Contains(t, string(body), "Connection test passed.")
	require.Contains(t, string(body), `aria-label="Breadcrumb"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/data-sources"`)
	require.Contains(t, string(body), `aria-current="page"`)
	require.Contains(t, string(body), "Schema type: SQL (postgres)")
	require.Contains(t, string(body), "table users")
	require.Contains(t, string(body), "email (text)")
}

func TestDataSourceSchemaPanelDataKeepsAPIStatusAuthoritative(t *testing.T) {
	t.Parallel()

	var schema api.IntrospectSchemaResponse
	require.NoError(t, schema.FromSQLSchemaResponse(api.SQLSchemaResponse{
		Type: api.SQLSchemaResponseTypePostgres,
		Tables: []api.TableInfo{{
			Name: "users",
			Columns: []api.ColumnInfo{{
				Name: "email",
				Type: "text",
			}},
		}},
	}))

	panel := dataSourceSchemaPanelData(&api.DataSource{
		Name:         "Production DB",
		Type:         api.DataSourceTypePostgres,
		SchemaStatus: api.DataSourceSchemaStatusImporting,
		Schema:       schema,
	}, "")

	assert.Equal(t, api.DataSourceSchemaStatusImporting, panel.Status)
	assert.Equal(t, "Schema type: SQL (postgres)", panel.Title)
	assert.Contains(t, panel.Lines, "  - email (text)")
}

func TestDataSourceReturnURLPreservesSectionAndCatalogContext(t *testing.T) {
	list := "/orgs/acme/data-sources?filter=active&cursor=next&selected=warehouse"
	for _, section := range []string{"overview", "schema", "operations", "settings"} {
		withDatasourceFormContext(t, url.Values{"environment_slug": {"dev"}}, func(c fiber.Ctx) {
			c.Request().URI().SetQueryString(url.Values{"section": {section}, "list_return": {list}}.Encode())
			got, err := url.Parse(dataSourceReturnURL(c, "acme", "warehouse"))
			require.NoError(t, err)
			require.Equal(t, "/orgs/acme/data-sources/warehouse", got.Path)
			require.Equal(t, section, got.Query().Get("section"))
			require.Equal(t, "dev", got.Query().Get("environment_slug"))
			require.Equal(t, list, got.Query().Get("list_return"))
			require.Empty(t, got.Fragment)
		})
	}
}

func TestRouter_HandleDataSourceDetailPage_RendersHubSpotSchema(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/hubspot-crm":
			_, _ = w.Write(
				[]byte(
					`{"slug":"hubspot-crm","name":"HubSpot CRM","type":"hubspot","config":{"timeout_seconds":15},"is_active":true,"read_only":true,"last_tested_at":null,"test_error":null,"schema_status":"ready","schema":{"schema_type":"object_api","type":"hubspot","objects":[{"name":"contacts","label":"Contacts","fields":[{"name":"hs_object_id","label":"HubSpot record ID","type":"string","provider_metadata":{"source":"record_id"}},{"name":"email","label":"Email","type":"string"},{"name":"firstname","label":"First name","type":"string"}],"operations":[{"name":"list","label":"List"},{"name":"search","label":"Search"}]},{"name":"companies","label":"Companies","fields":[],"operations":[{"name":"list","label":"List"},{"name":"search","label":"Search"}]}]},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/data-sources/hubspot-crm?section=schema", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "HubSpot CRM")
	require.Contains(t, string(body), "Schema type: Object API (hubspot)")
	require.Contains(t, string(body), "object contacts")
	require.Contains(t, string(body), "HubSpot record ID")
	require.Contains(t, string(body), "Email")
	require.Contains(t, string(body), "First name")
}

func TestRouter_HandleDataSourceDetailPage_RendersHTTPOpenAPIOperationMetadata(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/customer-api":
			_, _ = w.Write(
				[]byte(
					`{"slug":"customer-api","name":"Customer API","type":"http","config":{"base_url":"https://api.example.com","health_path":"/health","timeout_seconds":10,"openapi_url":"https://api.example.com/openapi.yaml","openapi_fetch_timeout_seconds":12},"is_active":true,"last_tested_at":null,"test_error":null,"schema_status":"ready","schema":{"schema_type":"http","type":"http","operations":[{"id":"listCustomers","path":"/customers","method":"GET","description":"List customers for the active account.","request_body":{"content":{"application/json":{"schema_summary":{"type":"object","properties":{"status":{"type":"string"}}}}}},"responses":{"200":{"description":"Customers returned","content":{"application/json":{"schema_summary":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"}}}}}}}}},{"id":"createCustomer","path":"/customers","method":"POST","description":"Create one customer record.","responses":{"201":{"description":"Customer created"}}}]}}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/data-sources/customer-api?section=schema", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyText := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, bodyText, "Schema type: HTTP")
	require.Contains(t, bodyText, "GET /customers")
	require.Contains(t, bodyText, "listCustomers")
	require.Contains(t, bodyText, "List customers for the active account.")
	require.Contains(t, bodyText, "request body")
	require.Contains(t, bodyText, "application/json")
	require.Contains(t, bodyText, "status")
	require.Contains(t, bodyText, "200")
	require.Contains(t, bodyText, "Customers returned")
	settingsReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/data-sources/customer-api?section=settings", nil)
	settingsReq.Header.Set("Cookie", req.Header.Get("Cookie"))
	settingsResp, err := app.Test(settingsReq)
	require.NoError(t, err)
	defer func() { _ = settingsResp.Body.Close() }()
	settingsBody, err := io.ReadAll(settingsResp.Body)
	require.NoError(t, err)
	bodyText = string(settingsBody)
	require.Contains(t, bodyText, `value="https://api.example.com/openapi.yaml"`)
	require.Contains(t, bodyText, `value="12"`)
	require.Contains(t, bodyText, "openapi_url")
}

func TestRouter_HandleDataSourceDetailPage_RendersColdCacheWithoutSchemaFetch(t *testing.T) {
	t.Parallel()

	var schemaCalls atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"schema_status":"not_imported","schema":{"schema_type":"sql","type":"postgres","tables":[]},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/schema":
			schemaCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/data-sources/prod-db?section=schema", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Production DB")
	require.Contains(t, string(body), "not imported")
	require.Contains(t, string(body), "Schema has not been imported yet.")
	require.Contains(t, string(body), "Import schema")
	require.NotContains(t, string(body), "shell-error-popup")
	require.Equal(t, int32(0), schemaCalls.Load())
}

func TestRouter_HandleRefreshDataSourceSchemaSubmit_RedirectsToDetail(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/schema":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write(
				[]byte(`{"data_source_slug":"prod-db","schema_status":"importing","enqueued":true,"job":{"kind":"data_source_schema_import","queue":"data_source_schema_import","state":"queued"}}`),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/schema/refresh",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/data-sources/prod-db", resp.Header.Get("Location"))
}

func TestRouter_HandleRefreshDataSourceSchemaSubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/schema":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write(
				[]byte(`{"data_source_slug":"prod-db","schema_status":"importing","enqueued":true,"job":{"kind":"data_source_schema_import","queue":"data_source_schema_import","state":"queued"}}`),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/schema/refresh",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/data-sources/prod-db", resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_DataSourceReturnToListRedirectsPreserveSelectionAndEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		requestPath  string
		apiMethod    string
		apiPath      string
		apiResponse  string
		formValues   url.Values
		redirectSlug string
	}{
		{
			name:        "RefreshSchema",
			requestPath: "/orgs/acme-hq/data-sources/prod-db/schema/refresh",
			apiMethod:   http.MethodPost,
			apiPath:     "/api/organizations/acme-hq/data-sources/prod-db/schema",
			apiResponse: `{"data_source_slug":"prod-db","schema_status":"importing","enqueued":true,"job":{"kind":"data_source_schema_import","queue":"data_source_schema_import","state":"queued"}}`,
			formValues: url.Values{
				"return_to":        {"list"},
				"environment_slug": {"dev"},
			},
			redirectSlug: "prod-db",
		},
		{
			name:        "UpdateSettings",
			requestPath: "/orgs/acme-hq/data-sources/prod-db/update",
			apiMethod:   http.MethodPatch,
			apiPath:     "/api/organizations/acme-hq/data-sources/prod-db",
			apiResponse: `{"slug":"prod-db","name":"Production DB Updated","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"app","connect_timeout":10},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
			formValues: url.Values{
				"return_to":        {"list"},
				"environment_slug": {"dev"},
				"type":             {"postgres"},
				"name":             {"Production DB Updated"},
				"host":             {"db.example.com"},
				"port":             {"5432"},
				"database":         {"app"},
				"connect_timeout":  {"10"},
			},
			redirectSlug: "prod-db",
		},
		{
			name:        "UpdateCredentials",
			requestPath: "/orgs/acme-hq/data-sources/prod-db/credentials/update",
			apiMethod:   http.MethodPatch,
			apiPath:     "/api/organizations/acme-hq/data-sources/prod-db/credentials",
			apiResponse: `{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
			formValues: url.Values{
				"return_to":        {"list"},
				"environment_slug": {"dev"},
				"type":             {"postgres"},
				"username":         {"new_user"},
				"password":         {"new_secret"},
			},
			redirectSlug: "prod-db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tt.name == "UpdateCredentials" &&
					r.Method == http.MethodGet &&
					r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db" {
					_, _ = w.Write(
						[]byte(
							`{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
						),
					)
					return
				}
				if r.Method == tt.apiMethod && r.URL.Path == tt.apiPath {
					if tt.name == "RefreshSchema" {
						w.WriteHeader(http.StatusAccepted)
					}
					_ = json.NewEncoder(w).Encode(json.RawMessage(tt.apiResponse))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer apiServer.Close()

			store := session.NewStore()
			router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)

			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})

			authCookie := getAuthenticatedSessionCookie(t, app)
			req := httptest.NewRequestWithContext(
				context.Background(),
				http.MethodPost,
				tt.requestPath,
				strings.NewReader(tt.formValues.Encode()),
			)

			req.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, req)
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusSeeOther, resp.StatusCode)
			require.Empty(t, resp.Header.Get("HX-Redirect"))
			requireDataSourceListRedirect(t, resp.Header.Get("Location"), "acme-hq", "dev", tt.redirectSlug)
		})
	}
}

func TestAppendDataSourceReturnQuery_UsesAmpersandForExistingQuery(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		"/orgs/acme-hq/data-sources?environment_slug=dev&selected=prod-db&error=failed+to+save",
		appendDataSourceReturnQuery(
			"/orgs/acme-hq/data-sources?environment_slug=dev&selected=prod-db",
			"error",
			"failed to save",
		),
	)
}

func TestRouter_HandleRefreshDataSourceSchemaSubmit_PreservesImportDiagnostic(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/customer-api/schema":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"SCHEMA_FETCH_FAILED","message":"schema fetch failed","detailed_error":"failed to fetch OpenAPI spec"}}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/customer-api/schema/refresh",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	location := resp.Header.Get("Location")
	require.Contains(t, location, "/orgs/acme-hq/data-sources/customer-api?schema_error=")
	require.Contains(t, location, "OpenAPI+import+failed")
	require.Contains(t, location, "failed+to+fetch+OpenAPI+spec")
	require.Contains(t, location, "SCHEMA_FETCH_FAILED")
}

func TestRouter_HandleDataSourceDetailPage_ShowsPersistedImportDiagnostic(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/customer-api":
			_, _ = w.Write(
				[]byte(
					`{"slug":"customer-api","name":"Customer API","type":"http","config":{"base_url":"https://api.example.com","openapi_url":"https://api.example.com/openapi.yaml"},"is_active":true,"last_tested_at":null,"test_error":null,"schema_status":"failed","schema_diagnostic":{"code":"SCHEMA_FETCH_FAILED","message":"failed to fetch OpenAPI spec"},"schema":{"schema_type":"http","type":"http","operations":[{"id":"listCustomers","path":"/customers","method":"GET"}]},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/data-sources/customer-api?section=schema",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyText := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, bodyText, "failed to fetch OpenAPI spec")
	require.Contains(t, bodyText, "SCHEMA_FETCH_FAILED")
}

func TestRouter_HandleRefreshDataSourceSchemaSubmit_HTMXShowsFailedSchemaState(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/customer-api/schema":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"SCHEMA_FETCH_FAILED","message":"schema fetch failed","detailed_error":"failed to fetch OpenAPI spec"}}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/customer-api":
			_, _ = w.Write(
				[]byte(
					`{"slug":"customer-api","name":"Customer API","type":"http","config":{"base_url":"https://api.example.com","openapi_url":"https://api.example.com/openapi.yaml"},"is_active":true,"last_tested_at":null,"test_error":null,"schema_status":"ready","schema":{"schema_type":"http","type":"http","operations":[{"id":"listCustomers","path":"/customers","method":"GET"}]},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("return_to", "list")
	form.Set("environment_slug", "dev")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/customer-api/schema/refresh",
		strings.NewReader(form.Encode()))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyText := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
	require.Contains(t, bodyText, `id="data-source-side-panel"`)
	require.Contains(t, bodyText, "OpenAPI import failed")
	require.Contains(t, bodyText, "failed to fetch OpenAPI spec")
	require.Contains(t, bodyText, "SCHEMA_FETCH_FAILED")
	require.Contains(t, bodyText, "failed")
	require.NotContains(t, bodyText, "Retry import")
}

func TestRouter_HandleCreateDataSourceSubmit_RedirectsToCreatedSource(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			bodyBytes, err := io.ReadAll(r.Body)
			assert.NoError(t, err)

			var payload map[string]any
			err = json.Unmarshal(bodyBytes, &payload)
			assert.NoError(t, err)
			assert.Equal(t, "Analytics DB", payload["name"])
			assert.Equal(t, "postgres", payload["type"])

			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(
				[]byte(
					`{"slug":"analytics-db","name":"Analytics DB","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"analytics"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("name", "Analytics DB")
	form.Set("type", "postgres")
	form.Set("host", "db.example.com")
	form.Set("port", "5432")
	form.Set("database", "analytics")
	form.Set("username", "analytics")
	form.Set("password", "secret")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/data-sources", resp.Header.Get("Location"))
}

func TestRouter_HandleCreateDataSourceSubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(
				[]byte(
					`{"slug":"analytics-db","name":"Analytics DB","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"analytics"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("name", "Analytics DB")
	form.Set("type", "postgres")
	form.Set("host", "db.example.com")
	form.Set("port", "5432")
	form.Set("database", "analytics")
	form.Set("username", "analytics")
	form.Set("password", "secret")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/data-sources", resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_HandleCreateDataSourceSubmit_HTMXBackendValidationShowsDetailedFormFeedback(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"VALIDATION_FAILED","message":"Request validation failed","detailed_error":"Config: input matches more than one oneOf schemas"}}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"UPSTREAM_FAILED","message":"reload failed","detailed_error":"token=secret-reload-token"}}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	var logBuffer bytes.Buffer
	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.NewJSONHandler(&logBuffer, nil)), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("name", "Git Repo")
	form.Set("type", "git")
	form.Set("git_repo_url", "https://github.com/acme/repo.git")
	form.Set("git_auth_type", gitAuthTypeHTTPS)

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	panel := createDataSourcePanel(t, string(body))

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, panel, "Request validation failed")
	require.Contains(t, panel, "Config: input matches more than one oneOf schemas")
	require.Contains(t, panel, `value="git" selected`)
	require.Contains(t, panel, `value="https://github.com/acme/repo.git"`)
	require.Contains(t, logBuffer.String(), `"action":"create data source"`)
	require.Contains(t, logBuffer.String(), `"upstream.status_code":400`)
	require.Contains(t, logBuffer.String(), `"action":"reload data sources after failed action"`)
	require.Contains(t, logBuffer.String(), `"upstream.status_code":502`)
	require.NotContains(t, logBuffer.String(), "secret-reload-token")
}

func TestRouter_HandleCreateDataSourceSubmit_HubSpotSendsHubSpotPayload(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			bodyBytes, err := io.ReadAll(r.Body)
			assert.NoError(t, err)

			var payload map[string]any
			err = json.Unmarshal(bodyBytes, &payload)
			assert.NoError(t, err)
			assert.Equal(t, "HubSpot CRM", payload["name"])
			assert.Equal(t, "hubspot", payload["type"])
			assert.Equal(t, true, payload["read_only"])

			config, ok := payload["config"].(map[string]any)
			if assert.True(t, ok) {
				assert.InDelta(t, 15, config["timeout_seconds"], 0.000001)
			}

			credentials, ok := payload["credentials"].(map[string]any)
			if assert.True(t, ok) {
				assert.Equal(t, "pat-na1-ui-token", credentials["private_app_token"])
			}

			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(
				[]byte(
					`{"slug":"hubspot-crm","name":"HubSpot CRM","type":"hubspot","config":{"timeout_seconds":15},"is_active":true,"read_only":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("name", "HubSpot CRM")
	form.Set("type", "hubspot")
	form.Set("timeout_seconds", "15")
	form.Set("private_app_token", "pat-na1-ui-token")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/data-sources", resp.Header.Get("Location"))
}

func TestRouter_HandleStartHubSpotDataSourceOAuthSubmit_RedirectsToProvider(t *testing.T) {
	t.Parallel()
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprintf("htmx=%t", htmx), func(t *testing.T) {
			const authorizationURL = "https://app.hubspot.com/oauth/authorize?client_id=client-id&state=opaque-state"
			var captured map[string]any
			var decodeErr error
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if handleTestAdminRolePermissions(w, r) {
					return
				}
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/provider-connectors/hubspot/data-source-installs":
					defer func() { _ = r.Body.Close() }()
					decodeErr = json.NewDecoder(r.Body).Decode(&captured)
					if decodeErr != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{
				"data_source":{"slug":"hubspot-crm","name":"HubSpot CRM","type":"hubspot","config":{"timeout_seconds":15},"is_active":false,"read_only":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},
				"install":{"authorization_url":"` + authorizationURL + `","expires_at":"2026-03-01T00:10:00Z","intent":"create_grant","provider":"hubspot","requested_scopes":["crm.objects.contacts.read"],"slug":"hubspot-install"}
			}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer apiServer.Close()

			store := session.NewStore()
			router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})

			form := url.Values{}
			form.Set("name", "HubSpot CRM")
			form.Set("slug", "hubspot-crm")
			form.Set("type", "hubspot")
			form.Set("hubspot_auth_type", "oauth2")
			form.Set("hubspot_access_mode", "write")
			form.Set("timeout_seconds", "15")
			req := httptest.NewRequestWithContext(
				context.Background(),
				http.MethodPost,
				"/orgs/acme-hq/data-sources/hubspot/oauth",
				strings.NewReader(form.Encode()),
			)
			req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			addCSRFToken(t, app, req)
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			if htmx {
				req.Header.Set("HX-Request", "true")
			}

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			if htmx {
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.Equal(t, authorizationURL, resp.Header.Get("HX-Redirect"))
			} else {
				require.Equal(t, http.StatusSeeOther, resp.StatusCode)
				require.Equal(t, authorizationURL, resp.Header.Get("Location"))
			}
			require.NoError(t, decodeErr)
			require.Equal(t, "HubSpot CRM", captured["name"])
			require.Equal(t, "hubspot-crm", captured["slug"])
			require.Equal(t, "hubspot", captured["type"])
			require.Equal(t, false, captured["read_only"])
			require.NotContains(t, captured, "credentials")
			config, ok := captured["config"].(map[string]any)
			require.True(t, ok)
			require.InDelta(t, 15, config["timeout_seconds"], 0.000001)
		})
	}
}

func TestRouter_HandleStartHubSpotDataSourceOAuthSubmit_RendersMissingNameFeedback(t *testing.T) {
	t.Parallel()
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprintf("htmx=%t", htmx), func(t *testing.T) {
			var installCalls atomic.Int32
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if handleTestAdminRolePermissions(w, r) {
					return
				}
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/provider-connectors/hubspot/data-source-installs":
					installCalls.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
					_, _ = w.Write([]byte(`{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
					_, _ = w.Write([]byte(`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
					_, _ = w.Write([]byte(`{"projects":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer apiServer.Close()

			store := session.NewStore()
			router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})

			form := url.Values{}
			form.Set("type", "hubspot")
			form.Set("hubspot_auth_type", "oauth2")
			form.Set("timeout_seconds", "15")
			req := httptest.NewRequestWithContext(
				context.Background(),
				http.MethodPost,
				"/orgs/acme-hq/data-sources/hubspot/oauth",
				strings.NewReader(form.Encode()),
			)
			req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			addCSRFToken(t, app, req)
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			if htmx {
				req.Header.Set("HX-Request", "true")
			}

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			bodyText := string(body)

			if htmx {
				require.Equal(t, http.StatusOK, resp.StatusCode)
			} else {
				require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			}
			require.Empty(t, resp.Header.Get("HX-Redirect"))
			require.Empty(t, resp.Header.Get("Location"))
			require.Contains(t, bodyText, validationNameRequiredMessage)
			require.Contains(t, bodyText, `name="name"`)
			require.Contains(t, bodyText, `aria-invalid="true"`)
			require.Contains(t, bodyText, `aria-errormessage="data-source-create-feedback"`)
			require.Contains(t, bodyText, `id="data-source-create-feedback"`)
			require.Contains(t, bodyText, `autofocus`)
			require.Contains(t, bodyText, `formnovalidate`)
			require.Contains(t, bodyText, `hx-target="#create-data-source"`)
			require.Zero(t, installCalls.Load())
		})
	}
}

func TestRouter_HandleStartHubSpotDataSourceOAuthSubmit_RejectsInvalidSlugBeforeAPICall(t *testing.T) {
	t.Parallel()
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprintf("htmx=%t", htmx), func(t *testing.T) {
			var installCalls atomic.Int32
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if handleTestAdminRolePermissions(w, r) {
					return
				}
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/provider-connectors/hubspot/data-source-installs":
					installCalls.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
					_, _ = w.Write([]byte(`{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
					_, _ = w.Write([]byte(`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
					_, _ = w.Write([]byte(`{"projects":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer apiServer.Close()

			store := session.NewStore()
			router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})

			form := url.Values{}
			form.Set("name", "HubSpot CRM")
			form.Set("slug", "Invalid HubSpot Slug")
			form.Set("type", "hubspot")
			form.Set("hubspot_auth_type", "oauth2")
			form.Set("timeout_seconds", "15")
			req := httptest.NewRequestWithContext(
				context.Background(),
				http.MethodPost,
				"/orgs/acme-hq/data-sources/hubspot/oauth",
				strings.NewReader(form.Encode()),
			)
			req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			addCSRFToken(t, app, req)
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			if htmx {
				req.Header.Set("HX-Request", "true")
			}

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			if htmx {
				require.Equal(t, http.StatusOK, resp.StatusCode)
			} else {
				require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			}
			require.Empty(t, resp.Header.Get("HX-Redirect"))
			require.Contains(t, string(body), `aria-errormessage="data-source-create-feedback"`)
			require.Contains(t, string(body), "Validation: slug is invalid.")
			require.Zero(t, installCalls.Load())
		})
	}
}

func TestRouter_HandleStartExistingHubSpotOAuthSubmit_ConvertsStaticCredential(t *testing.T) {
	t.Parallel()

	const authorizationURL = "https://app.hubspot.com/oauth/authorize?client_id=client-id&state=opaque-state"
	var captured api.StartProviderConnectorInstallRequest
	var decodeErr error
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/hubspot-crm":
			_, _ = w.Write([]byte(`{"slug":"hubspot-crm","name":"HubSpot CRM","type":"hubspot","config":{"timeout_seconds":15},"credential":{"kind":"static_secret","status":"active","updated_at":"2026-03-01T00:00:00Z"},"is_active":true,"read_only":true,"last_tested_at":"2026-03-01T00:00:00Z","test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/provider-connectors/hubspot/installs":
			defer func() { _ = r.Body.Close() }()
			decodeErr = json.NewDecoder(r.Body).Decode(&captured)
			if decodeErr != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"authorization_url":"` + authorizationURL + `","expires_at":"2026-03-01T00:10:00Z","intent":"rotate_grant","provider":"hubspot","requested_scopes":["crm.objects.contacts.read"],"slug":"hubspot-install"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/hubspot-crm/hubspot/oauth",
		nil,
	)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, decodeErr)
	require.Equal(t, authorizationURL, resp.Header.Get("HX-Redirect"))
	require.Equal(t, "hubspot-crm", captured.DataSourceSlug)
	require.Equal(t, api.RotateGrant, captured.Intent)
}

func TestRouter_HandleCreateDataSourceSubmit_HTTPSendsOpenAPIImportConfig(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			defer func() { _ = r.Body.Close() }()
			err := json.NewDecoder(r.Body).Decode(&captured)
			assert.NoError(t, err)

			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(
				[]byte(
					`{"slug":"customer-api","name":"Customer API","type":"http","config":{"base_url":"https://api.example.com","health_path":"/health","timeout_seconds":15,"openapi_url":"https://api.example.com/openapi.yaml","openapi_fetch_timeout_seconds":20},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("name", "Customer API")
	form.Set("type", "http")
	form.Set("base_url", "https://api.example.com")
	form.Set("health_path", "/health")
	form.Set("timeout_seconds", "15")
	form.Set("openapi_url", "https://api.example.com/openapi.yaml")
	form.Set("openapi_fetch_timeout_seconds", "20")
	form.Set("bearer_token", "test-token")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "Customer API", captured["name"])
	require.Equal(t, "http", captured["type"])

	config, ok := captured["config"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "https://api.example.com", config["base_url"])
	require.Equal(t, "/health", config["health_path"])
	require.InDelta(t, 15, config["timeout_seconds"], 0.000001)
	require.Equal(t, "https://api.example.com/openapi.yaml", config["openapi_url"])
	require.InDelta(t, 20, config["openapi_fetch_timeout_seconds"], 0.000001)

	credentials, ok := captured["credentials"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "test-token", credentials["bearer_token"])
}

func TestRouter_HandleCreateDataSourceSubmit_ValidationErrorPreservesFormValues(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("name", "Persist Me")
	form.Set("slug", "persist-me")
	form.Set("type", "postgres")
	form.Set("host", "db.internal")
	form.Set("port", "5432")
	form.Set("database", "analytics")
	form.Set("username", "analytics-user")
	// Intentionally omit password to trigger validation error.

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), `value="Persist Me"`)
	require.Contains(t, string(body), `value="persist-me"`)
	require.Contains(t, string(body), `value="db.internal"`)
	require.Contains(t, string(body), `value="analytics-user"`)
	require.Contains(t, string(body), `value="postgres" selected`)
}

func TestRouter_HandleCreateDataSourceSubmit_HTMXValidationErrorRendersCreatePanel(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyText := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, bodyText, `id="create-data-source"`)
	require.Contains(t, bodyText, "Validation: type is required.")
	require.Contains(t, bodyText, `data-ds-requires-type`)
	require.Contains(t, bodyText, `hx-target="#create-data-source"`)
}

func TestRouter_HandleDeleteDataSourceSubmit_RedirectsToList(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/data-sources/prod-db/delete", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/data-sources", resp.Header.Get("Location"))
}

func TestRouter_HandleDeleteDataSourceSubmit_PreservesEnvironmentRedirect(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	form := url.Values{}
	form.Set("environment_slug", "prod")
	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/delete?environment_slug=prod",
		strings.NewReader(form.Encode()),
	)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/data-sources?environment_slug=prod", resp.Header.Get("Location"))
}

func TestRouter_HandleDeleteDataSourceSubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/data-sources/prod-db/delete", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/data-sources", resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_HandleTestDataSourceConnectionSubmit_ShowsSuccessMessage(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/test-connection":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("name", "Analytics DB")
	form.Set("type", "postgres")
	form.Set("host", "db.example.com")
	form.Set("port", "5432")
	form.Set("database", "analytics")
	form.Set("username", "analytics")
	form.Set("password", "secret")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/test-connection",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Connection successful.")
	require.Contains(t, string(body), `value="Analytics DB"`)
}

func TestRouter_HandleTestDataSourceConnectionSubmit_ShowsFailureMessageAndPreservesValues(
	t *testing.T,
) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/test-connection":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"DATA_SOURCE_CONNECTION_FAILED","message":"data source connection failed","detailed_error":"dial tcp: connection refused"}}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("name", "Analytics DB")
	form.Set("slug", "analytics-db")
	form.Set("type", "postgres")
	form.Set("host", "db.example.com")
	form.Set("port", "5432")
	form.Set("database", "analytics")
	form.Set("username", "analytics")
	form.Set("password", "secret")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/test-connection",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(t, string(body), "dial tcp: connection refused")
	require.Contains(t, string(body), "shell-error-popup")
	require.Contains(t, string(body), `value="analytics-db"`)
	require.Contains(t, string(body), `value="db.example.com"`)
}

func TestRouter_HandleTestDataSourceConnectionSubmit_HTMXSuccessRendersForm(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/test-connection":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("name", "Analytics DB")
	form.Set("type", "postgres")
	form.Set("host", "db.example.com")
	form.Set("port", "5432")
	form.Set("database", "analytics")
	form.Set("username", "analytics")
	form.Set("password", "secret")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/test-connection",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Connection successful.")
	require.Contains(t, string(body), `id="create-data-source"`)
	require.Contains(t, string(body), `value="Analytics DB"`)
}

func TestRouter_HandleTestDataSourceConnectionSubmit_HTMXGitSSHSuccessDoesNotEchoDraftCredentials(
	t *testing.T,
) {
	t.Parallel()

	var captured map[string]any
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/test-connection":
			defer func() { _ = r.Body.Close() }()
			err := json.NewDecoder(r.Body).Decode(&captured)
			assert.NoError(t, err)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	privateKey := "git-private-key-line-one\ngit-private-key-line-two\n"
	passphrase := " git-passphrase "
	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("name", "Git Repo")
	form.Set("type", "git")
	form.Set("git_repo_url", "git@github.com:acme/repo.git")
	form.Set("git_auth_type", gitAuthTypeSSH)
	form.Set("git_ssh_private_key", privateKey)
	form.Set("git_ssh_known_hosts", "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample")
	form.Set("git_ssh_passphrase", passphrase)

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/test-connection",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	panel := createDataSourcePanel(t, string(body))

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, panel, "Connection successful.")
	require.NotContains(t, panel, privateKey)
	require.NotContains(t, panel, passphrase)
	require.Contains(t, panel, `id="data-source-create-git-private-key"`)
	require.Contains(t, panel, `id="data-source-create-git-passphrase"`)
	require.Contains(t, panel, `hx-preserve`)
	require.Contains(t, panel, `value="ssh" selected`)
	credentials, ok := captured["credentials"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, privateKey, credentials["ssh_private_key"])
	require.Equal(t, passphrase, credentials["ssh_passphrase"])
}

func TestRouter_HandleTestDataSourceConnectionSubmit_HTMXFailureRendersError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/test-connection":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"DATA_SOURCE_CONNECTION_FAILED","message":"data source connection failed","detailed_error":"dial tcp: connection refused"}}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("name", "Analytics DB")
	form.Set("type", "postgres")
	form.Set("host", "db.example.com")
	form.Set("port", "5432")
	form.Set("database", "analytics")
	form.Set("username", "analytics")
	form.Set("password", "secret")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/test-connection",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "dial tcp: connection refused")
	require.Contains(t, string(body), "shell-error-popup")
	require.Contains(t, string(body), `id="create-data-source"`)
	require.Contains(t, string(body), `value="Analytics DB"`)
}

func TestRouter_HandleTestDataSourceConnectionSubmit_HTMXGitFailureRendersFormFeedback(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/test-connection":
			defer func() { _ = r.Body.Close() }()
			err := json.NewDecoder(r.Body).Decode(&captured)
			assert.NoError(t, err)
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"DATA_SOURCE_CONNECTION_FAILED","message":"data source connection failed","detailed_error":"git ls-remote failed: repository authentication failed or remote is unavailable"}}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("name", "Git Repo")
	form.Set("type", "git")
	form.Set("git_repo_url", "https://github.com/acme/repo.git")
	form.Set("git_auth_type", gitAuthTypeHTTPS)

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/test-connection",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	panel := createDataSourcePanel(t, string(body))

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, panel, "data source connection failed")
	require.Contains(t, panel, "git ls-remote failed")
	require.Contains(t, panel, "repository authentication failed or remote is unavailable")
	require.Contains(t, panel, `value="git" selected`)
	require.Contains(t, panel, `value="https://github.com/acme/repo.git"`)
	require.Equal(t, "git", captured["type"])
	require.NotContains(t, captured, "name")
	require.NotContains(t, captured, "slug")
	require.NotContains(t, captured, "read_only")
}

func TestRouter_HandleTestDataSourceConnectionSubmit_HTTPSendsOpenAPIImportConfig(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/test-connection":
			defer func() { _ = r.Body.Close() }()
			err := json.NewDecoder(r.Body).Decode(&captured)
			assert.NoError(t, err)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("name", "Customer API")
	form.Set("type", "http")
	form.Set("base_url", "https://api.example.com")
	form.Set("health_path", "/health")
	form.Set("timeout_seconds", "15")
	form.Set("openapi_url", "https://api.example.com/openapi.yaml")
	form.Set("openapi_fetch_timeout_seconds", "20")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/test-connection",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "http", captured["type"])
	require.NotContains(t, captured, "name")
	require.NotContains(t, captured, "slug")
	require.NotContains(t, captured, "read_only")

	config, ok := captured["config"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "https://api.example.com/openapi.yaml", config["openapi_url"])
	require.InDelta(t, 20, config["openapi_fetch_timeout_seconds"], 0.000001)
}

func TestRouter_HandleTestExistingDataSourceConnectionSubmit_HTMXFullPageRedirectsWithNotice(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/test-connection":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/test-connection",
		strings.NewReader(form.Encode()))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(
		t,
		"/orgs/acme-hq/data-sources/prod-db?notice=Connection+test+passed.",
		resp.Header.Get("HX-Redirect"),
	)
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_HandleTestExistingDataSourceConnectionSubmit_HTMXRendersSidePanel(
	t *testing.T,
) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/repo-source/test-connection":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/repo-source":
			_, _ = w.Write(
				[]byte(
					`{"slug":"repo-source","name":"Seeded Git Repo","type":"git","config":{"repo_url":"ssh://admin@127.0.0.1:55593/seeded-datasource-repo-with-a-long-name","default_branch":"main","allow_private_hosts":true},"is_active":true,"last_tested_at":null,"test_error":null,"schema_status":"ready","schema":{"schema_type":"file","type":"git","files":[{"name":"orders.csv","fields":[{"name":"customer_email","type":"text"}]}]},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("return_to", "list")
	form.Set("environment_slug", "dev")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/repo-source/test-connection",
		strings.NewReader(form.Encode()))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyText := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
	require.Contains(t, bodyText, `id="data-source-side-panel"`)
	require.Contains(t, bodyText, "Connection test passed.")
	require.NotContains(t, bodyText, "Recheck access")
	require.NotContains(t, bodyText, "Schema type: File (git)")
	require.NotContains(t, bodyText, "customer_email")
	require.Contains(t, bodyText, "Open full page")
	require.Contains(t, bodyText, "Open full page")
}

func TestRouter_HandleTestExistingDataSourceConnectionSubmit_HTMXRendersFailureInSidePanel(
	t *testing.T,
) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/test-connection":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"DATA_SOURCE_CONNECTION_FAILED","message":"data source connection failed","detailed_error":"dial tcp: connection refused"}}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"schema_status":"ready","schema":{"schema_type":"sql","type":"postgres","tables":[{"name":"users","columns":[{"name":"email","type":"text"}]}]},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("return_to", "list")
	form.Set("environment_slug", "dev")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/test-connection",
		strings.NewReader(form.Encode()))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyText := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
	require.Contains(t, bodyText, `id="data-source-side-panel"`)
	require.Contains(t, bodyText, "data source connection failed: dial tcp: connection refused")
	require.Contains(t, bodyText, "dial tcp: connection refused")
	require.Contains(t, bodyText, "Production DB")
	require.NotContains(t, bodyText, "Save connection settings")
}

func TestRouter_HandleUpdateDataSourceSubmit_RedirectsToDetail(t *testing.T) {
	t.Parallel()

	var captured map[string]any

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			defer func() { _ = r.Body.Close() }()
			err := json.NewDecoder(r.Body).Decode(&captured)
			assert.NoError(t, err)
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB Updated","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"app","connect_timeout":10},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("type", "postgres")
	form.Set("name", "Production DB Updated")
	form.Set("host", "db.example.com")
	form.Set("port", "5432")
	form.Set("database", "app")
	form.Set("connect_timeout", "10")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/update",
		strings.NewReader(form.Encode()))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/data-sources/prod-db", resp.Header.Get("Location"))
	require.Equal(t, "Production DB Updated", captured["name"])

	config, ok := captured["config"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "db.example.com", config["host"])
	require.Equal(t, "app", config["database"])
}

func TestRouter_HandleUpdateDataSourceSubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB Updated","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"app","connect_timeout":10},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("type", "postgres")
	form.Set("name", "Production DB Updated")
	form.Set("host", "db.example.com")
	form.Set("port", "5432")
	form.Set("database", "app")
	form.Set("connect_timeout", "10")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/update",
		strings.NewReader(form.Encode()))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/data-sources/prod-db", resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_HandleUpdateDataSourceSubmit_HTTPSendsOpenAPIImportConfig(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/organizations/acme-hq/data-sources/customer-api":
			defer func() { _ = r.Body.Close() }()
			err := json.NewDecoder(r.Body).Decode(&captured)
			assert.NoError(t, err)
			_, _ = w.Write(
				[]byte(
					`{"slug":"customer-api","name":"Customer API Updated","type":"http","config":{"base_url":"https://api.example.com","health_path":"/health","timeout_seconds":15,"openapi_url":"https://api.example.com/openapi.yaml","openapi_fetch_timeout_seconds":20},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	router.SetAllowPrivateDataSourceHosts(true)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("type", "http")
	form.Set("name", "Customer API Updated")
	form.Set("base_url", "https://api.example.com")
	form.Set("health_path", "/health")
	form.Set("timeout_seconds", "15")
	form.Set("openapi_url", "https://api.example.com/openapi.yaml")
	form.Set("openapi_fetch_timeout_seconds", "20")
	form.Set("http_allow_private_hosts", "true")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/customer-api/update",
		strings.NewReader(form.Encode()))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/data-sources/customer-api", resp.Header.Get("Location"))
	require.Equal(t, "Customer API Updated", captured["name"])

	config, ok := captured["config"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "https://api.example.com", config["base_url"])
	require.Equal(t, "/health", config["health_path"])
	require.InDelta(t, 15, config["timeout_seconds"], 0.000001)
	require.Equal(t, "https://api.example.com/openapi.yaml", config["openapi_url"])
	require.InDelta(t, 20, config["openapi_fetch_timeout_seconds"], 0.000001)
	require.Equal(t, true, config["allow_private_hosts"])
}

func TestRouter_HandleUpdateDataSourceCredentialsSubmit_RedirectsToDetail(t *testing.T) {
	t.Parallel()

	var captured map[string]any

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/credentials":
			defer func() { _ = r.Body.Close() }()
			err := json.NewDecoder(r.Body).Decode(&captured)
			assert.NoError(t, err)
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("type", "postgres")
	form.Set("username", "new_user")
	form.Set("password", "new_secret")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/credentials/update",
		strings.NewReader(form.Encode()))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/data-sources/prod-db", resp.Header.Get("Location"))

	credentials, ok := captured["credentials"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "new_user", credentials["username"])
	require.Equal(t, "new_secret", credentials["password"])
}

func TestRouter_HandleUpdateDataSourceCredentialsSubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/credentials":
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("type", "postgres")
	form.Set("username", "new_user")
	form.Set("password", "new_secret")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/credentials/update",
		strings.NewReader(form.Encode()))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/data-sources/prod-db", resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_HandleUpdateDataSourceCredentialsSubmit_ValidatesGitAuthAgainstStoredRepoURL(t *testing.T) {
	t.Parallel()

	patchCalled := make(chan struct{}, 1)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/repo-source":
			_, _ = w.Write(
				[]byte(
					`{"slug":"repo-source","name":"Repo Source","type":"github","config":{"repo_url":"git@github.com:acme/repo.git"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/organizations/acme-hq/data-sources/repo-source/credentials":
			select {
			case patchCalled <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := url.Values{}
	form.Set("type", "github")
	form.Set("git_repo_url", "https://github.com/acme/repo.git")
	form.Set("git_auth_type", gitAuthTypeHTTPS)
	form.Set("git_token", "ghp_client_controlled_token")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/repo-source/credentials/update",
		strings.NewReader(form.Encode()))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Location"), "SSH+repository+URLs+require+SSH+authentication")
	select {
	case <-patchCalled:
		t.Fatal("credentials update API was called")
	default:
	}
}

func TestRouter_HandleDataSourceNewPage_RequiresAuth(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme/datasources/new", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Location"), "/auth/login")
}

func TestRouter_HandleDataSourceNewPage_RendersForm(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	setActiveOrganizationSlugInSession(t, app, authCookie, "acme")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme/datasources/new", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Add data source")
	require.Contains(t, string(body), "Import a connection URI")
	require.Contains(t, string(body), "parse-uri")
	require.Contains(t, string(body), `hx-select="#ds-fields"`)
	require.Contains(t, string(body), `hx-swap="outerHTML"`)
	require.Contains(t, string(body), "[name='database_allow_private_hosts']")
	require.Contains(t, string(body), "[name='http_allow_private_hosts']")
	require.Contains(t, string(body), "[name='git_allow_private_hosts']")
	require.Contains(t, string(body), `value="git"`)
	require.NotContains(t, string(body), `value="github"`)
	require.NotContains(t, string(body), `value="gitlab"`)
}

func TestRouter_HandleDataSourceCreateSubmit_BigQueryRedirectPreservesIdentity(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	authCookie := getAuthenticatedSessionCookie(t, app)
	setActiveOrganizationSlugInSession(t, app, authCookie, "acme")

	form := url.Values{"name": {"Analytics Warehouse"}, "slug": {"analytics-warehouse"}, "type": {"bigquery"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/organizations/acme/datasources", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/acme/data-sources?name=Analytics+Warehouse&slug=analytics-warehouse&type=bigquery", resp.Header.Get("Location"))
}

func TestRouter_HandleDataSourceParseURI_Postgres(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		formType   string
		formURI    string
		contains   []string
		wantStatus int
	}{
		{
			name:     "ParsesPostgresURI",
			formType: "postgres",
			formURI:  "postgres://app_user:s3cr3t@db.example.com:5432/production?sslmode=verify-ca",
			contains: []string{
				`value="db.example.com"`,
				`value="5432"`,
				`value="production"`,
				`value="app_user"`,
				`value="verify-ca" selected`,
				"URI parsed successfully",
			},
			wantStatus: http.StatusOK,
		},
		{
			name:     "ParsesMySQLURI",
			formType: "mysql",
			formURI:  "mysql://analytics:pass@mysql.local:3306/metrics",
			contains: []string{
				`value="mysql.local"`,
				`value="3306"`,
				`value="metrics"`,
				`value="analytics"`,
				"URI parsed successfully",
			},
			wantStatus: http.StatusOK,
		},
		{
			name:     "ParsesMongoDBURIWithAuthSource",
			formType: "mongodb",
			formURI:  "mongodb://user:pw@mongo.local:27017/appdb?authSource=admin",
			contains: []string{
				`value="mongo.local"`,
				`value="27017"`,
				`value="appdb"`,
				`value="user"`,
				`value="admin"`,
				"URI parsed successfully",
			},
			wantStatus: http.StatusOK,
		},
		{
			name:     "ParsesHTTPURI",
			formType: "http",
			formURI:  "https://api.example.com:8443/health",
			contains: []string{
				`value="https://api.example.com:8443"`,
				`value="/health"`,
				"URI parsed successfully",
			},
			wantStatus: http.StatusOK,
		},
		{
			name:     "MissingType_InferredFromURI",
			formType: "",
			formURI:  "postgres://user:pass@host/db",
			contains: []string{
				`value="host"`,
				`value="db"`,
				`value="user"`,
				"URI parsed successfully",
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "EmptyURI_ReturnsPrompt",
			formType:   "postgres",
			formURI:    "",
			contains:   []string{"Paste a URI to parse"},
			wantStatus: http.StatusOK,
		},
		{
			name:     "WrongScheme_AutoDetectsType",
			formType: "postgres",
			formURI:  "mysql://user:pass@host/db",
			contains: []string{
				`value="host"`,
				`value="db"`,
				`value="user"`,
				`value="3306"`,
				"Type inferred from URI scheme as",
				"mysql",
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "MalformedURI_ReturnsError",
			formType:   "postgres",
			formURI:    "not-a-valid-uri-at-all://[broken",
			contains:   []string{"Could not parse URI"},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			app, authCookie := setupAuthenticatedDatasourceTestApp(t)

			form := url.Values{}
			form.Set("type", tt.formType)
			form.Set("uri", tt.formURI)

			req := httptest.NewRequestWithContext(context.Background(),
				http.MethodPost,
				"/organizations/acme/datasources/new/parse-uri",
				strings.NewReader(form.Encode()))

			req.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, req)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			require.Equal(t, tt.wantStatus, resp.StatusCode)
			for _, want := range tt.contains {
				require.Contains(t, string(body), want)
			}
		})
	}
}

func TestRouter_HandleDataSourceFieldsPartial(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		dsType   string
		contains []string
	}{
		{
			name:   "PostgresFields",
			dsType: "postgres",
			contains: []string{
				`name="host"`,
				`name="port"`,
				`name="database"`,
				`name="username"`,
				`name="password"`,
			},
		},
		{
			name:     "MySQLFields",
			dsType:   "mysql",
			contains: []string{`name="host"`, `name="port"`, `name="database"`},
		},
		{
			name:     "MongoDBFields",
			dsType:   "mongodb",
			contains: []string{`name="host"`, `name="auth_database"`},
		},
		{
			name:   "HTTPFields",
			dsType: "http",
			contains: []string{
				`name="base_url"`,
				`name="health_path"`,
				`name="openapi_url"`,
				`name="openapi_fetch_timeout_seconds"`,
				`name="bearer_token"`,
			},
		},
		{
			name:   "GitFields",
			dsType: "git",
			contains: []string{
				`name="git_repo_url"`,
				`name="git_auth_type"`,
				`name="git_token"`,
				`name="git_ssh_known_hosts"`,
			},
		},
		{
			name:   "GitHubFields",
			dsType: "github",
			contains: []string{
				"Use the managed provider flow on the data sources page.",
			},
		},
		{
			name:   "GitLabFields",
			dsType: "gitlab",
			contains: []string{
				"Use the managed provider flow on the data sources page.",
			},
		},
		{name: "EmptyType_ShowsPrompt", dsType: "", contains: []string{"Select a datasource type"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			app, authCookie := setupAuthenticatedDatasourceTestApp(t)

			req := httptest.NewRequestWithContext(context.Background(),
				http.MethodGet,
				"/organizations/acme/datasources/new/fields?type="+tt.dsType,
				nil)

			req.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, req)

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			require.Equal(t, http.StatusOK, resp.StatusCode)
			for _, want := range tt.contains {
				require.Contains(t, string(body), want)
			}
		})
	}
}

func TestParseGitCredentialsForm_SSHRequiresKnownHosts(t *testing.T) {
	t.Parallel()

	form := url.Values{}
	form.Set("git_auth_type", gitAuthTypeSSH)
	form.Set("git_ssh_private_key", "-----BEGIN OPENSSH PRIVATE KEY-----")

	withDatasourceFormContext(t, form, func(c fiber.Ctx) {
		authType, authTypeErr := parseGitAuthTypeForm(c)
		require.Empty(t, authTypeErr)
		_, errMsg := parseGitCredentialsFormForAuthType(c, authType)
		require.Equal(
			t,
			"Validation: SSH known_hosts is required for git SSH authentication.",
			errMsg,
		)
	})
}

func TestParseGitConfigForm_RejectsUnsupportedURLScheme(t *testing.T) {
	t.Parallel()

	form := url.Values{}
	form.Set("git_repo_url", "ftp://example.com/acme/repo.git")

	withDatasourceFormContext(t, form, func(c fiber.Ctx) {
		_, errMsg := parseGitConfigFormWithPrivateHosts(c, false)
		require.Equal(t, "Validation: repository URL must use HTTPS or SSH.", errMsg)
	})
}

func TestParseGitConfigForm_RejectsEmptyURLHost(t *testing.T) {
	t.Parallel()

	form := url.Values{}
	form.Set("git_repo_url", "https:///acme/repo.git")

	withDatasourceFormContext(t, form, func(c fiber.Ctx) {
		_, errMsg := parseGitConfigFormWithPrivateHosts(c, false)
		require.Equal(t, "Validation: repository URL must include a valid hostname.", errMsg)
	})
}

func TestParseGitForm_RejectsAuthTypeMismatchedWithRepoURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		repoURL    string
		authType   string
		wantErrMsg string
	}{
		{
			name:       "SSHURLWithHTTPSAuth",
			repoURL:    "git@github.com:acme/repo.git",
			authType:   gitAuthTypeHTTPS,
			wantErrMsg: "Validation: SSH repository URLs require SSH authentication.",
		},
		{
			name:       "SSHSchemeURLWithHTTPSAuth",
			repoURL:    "ssh://git@github.com/acme/repo.git",
			authType:   gitAuthTypeHTTPS,
			wantErrMsg: "Validation: SSH repository URLs require SSH authentication.",
		},
		{
			name:       "HTTPSURLWithSSHAuth",
			repoURL:    "https://github.com/acme/repo.git",
			authType:   gitAuthTypeSSH,
			wantErrMsg: "Validation: HTTPS repository URLs require HTTPS authentication.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			form := url.Values{}
			form.Set("git_repo_url", tt.repoURL)
			form.Set("git_auth_type", tt.authType)

			withDatasourceFormContext(t, form, func(c fiber.Ctx) {
				_, _, errMsg := parseGitFormWithPrivateHosts(c, false)
				require.Equal(t, tt.wantErrMsg, errMsg)
			})
		})
	}
}

func TestBuildUpdateDataSourceCredentialsPayloadForType_RejectsGitAuthTypeMismatchedWithStoredRepoURL(t *testing.T) {
	t.Parallel()

	form := url.Values{}
	form.Set("type", "github")
	form.Set("git_repo_url", "https://github.com/acme/repo.git")
	form.Set("git_auth_type", gitAuthTypeHTTPS)

	withDatasourceFormContext(t, form, func(c fiber.Ctx) {
		_, errMsg := buildUpdateDataSourceCredentialsPayloadForType(
			c,
			api.DataSourceTypeGithub,
			"git@github.com:acme/repo.git",
		)
		require.Equal(t, "Validation: SSH repository URLs require SSH authentication.", errMsg)
	})
}

func TestParseGitCredentialsForm_SSHIncludesKnownHosts(t *testing.T) {
	t.Parallel()

	form := url.Values{}
	form.Set("git_auth_type", gitAuthTypeSSH)
	form.Set("git_ssh_private_key", "-----BEGIN OPENSSH PRIVATE KEY-----")
	form.Set("git_ssh_known_hosts", "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample")
	form.Set("git_ssh_passphrase", "correct horse battery staple")

	withDatasourceFormContext(t, form, func(c fiber.Ctx) {
		authType, authTypeErr := parseGitAuthTypeForm(c)
		require.Empty(t, authTypeErr)
		creds, errMsg := parseGitCredentialsFormForAuthType(c, authType)
		require.Empty(t, errMsg)
		require.NotNil(t, creds.SshPrivateKey)
		require.Equal(t, "-----BEGIN OPENSSH PRIVATE KEY-----", *creds.SshPrivateKey)
		require.NotNil(t, creds.SshKnownHosts)
		require.Equal(
			t,
			"github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample",
			*creds.SshKnownHosts,
		)
		require.NotNil(t, creds.SshPassphrase)
		require.Equal(t, "correct horse battery staple", *creds.SshPassphrase)
	})
}

func TestParseGitCredentialsForm_PreservesSecretBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		form url.Values
		run  func(t *testing.T, creds api.GitCredentials)
	}{
		{
			name: "HTTPS",
			form: func() url.Values {
				form := url.Values{}
				form.Set("git_auth_type", gitAuthTypeHTTPS)
				form.Set("git_username", " git ")
				form.Set("git_token", " token with surrounding space \n")
				return form
			}(),
			run: func(t *testing.T, creds api.GitCredentials) {
				t.Helper()
				require.NotNil(t, creds.Username)
				require.Equal(t, "git", *creds.Username)
				require.NotNil(t, creds.Token)
				require.Equal(t, " token with surrounding space \n", *creds.Token)
			},
		},
		{
			name: "SSH",
			form: func() url.Values {
				form := url.Values{}
				form.Set("git_auth_type", gitAuthTypeSSH)
				form.Set("git_ssh_private_key", "-----BEGIN OPENSSH PRIVATE KEY-----\nkey-body\n-----END OPENSSH PRIVATE KEY-----\n")
				form.Set("git_ssh_known_hosts", " github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample \n")
				form.Set("git_ssh_passphrase", " leading and trailing passphrase \n")
				return form
			}(),
			run: func(t *testing.T, creds api.GitCredentials) {
				t.Helper()
				require.NotNil(t, creds.SshPrivateKey)
				require.Equal(
					t,
					"-----BEGIN OPENSSH PRIVATE KEY-----\nkey-body\n-----END OPENSSH PRIVATE KEY-----\n",
					*creds.SshPrivateKey,
				)
				require.NotNil(t, creds.SshKnownHosts)
				require.Equal(
					t,
					"github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample",
					*creds.SshKnownHosts,
				)
				require.NotNil(t, creds.SshPassphrase)
				require.Equal(t, " leading and trailing passphrase \n", *creds.SshPassphrase)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			withDatasourceFormContext(t, tt.form, func(c fiber.Ctx) {
				authType, authTypeErr := parseGitAuthTypeForm(c)
				require.Empty(t, authTypeErr)
				creds, errMsg := parseGitCredentialsFormForAuthType(c, authType)
				require.Empty(t, errMsg)
				tt.run(t, creds)
			})
		})
	}
}

func TestMySQLCredentialParsersAllowPasswordlessAccounts(t *testing.T) {
	t.Parallel()
	for _, password := range []string{"", " \n", "ordinary-password"} {
		withDatasourceFormContext(t, url.Values{
			"host": {"mysql.example.com"}, "database": {"app"}, "username": {"reader"}, "password": {password},
		}, func(c fiber.Ctx) {
			_, credentials, errMsg := parseMySQLFormWithPrivateHosts(c, 3306, false)
			require.Empty(t, errMsg)
			require.Equal(t, password, credentials.Password)
			credentials, errMsg = parseMySQLCredentialsForm(c)
			require.Empty(t, errMsg)
			require.Equal(t, password, credentials.Password)
		})
	}
	withDatasourceFormContext(t, url.Values{"host": {"mysql.example.com"}, "database": {"app"}}, func(c fiber.Ctx) {
		_, _, errMsg := parseMySQLFormWithPrivateHosts(c, 3306, false)
		require.Contains(t, errMsg, "username")
		_, errMsg = parseMySQLCredentialsForm(c)
		require.Contains(t, errMsg, "username")
	})
}

func TestDataSourceCredentialParsersPreserveRawSecretValues(t *testing.T) {
	t.Parallel()

	const secretValue = " secret with surrounding whitespace \n"

	tests := []struct {
		name string
		form url.Values
		run  func(t *testing.T, c fiber.Ctx)
	}{
		{
			name: "PostgresPassword",
			form: func() url.Values {
				form := url.Values{}
				form.Set("host", "db.example.com")
				form.Set("database", "app")
				form.Set("username", "app")
				form.Set("password", secretValue)
				return form
			}(),
			run: func(t *testing.T, c fiber.Ctx) {
				t.Helper()
				_, createCreds, errMsg := parseSQLFormWithPrivateHosts(c, 5432, false)
				require.Empty(t, errMsg)
				require.Equal(t, secretValue, createCreds.Password)

				updateCreds, errMsg := parseSQLCredentialsForm(c)
				require.Empty(t, errMsg)
				require.Equal(t, secretValue, updateCreds.Password)
			},
		},
		{
			name: "MySQLPassword",
			form: func() url.Values {
				form := url.Values{}
				form.Set("host", "mysql.example.com")
				form.Set("database", "app")
				form.Set("username", "app")
				form.Set("password", secretValue)
				return form
			}(),
			run: func(t *testing.T, c fiber.Ctx) {
				t.Helper()
				_, createCreds, errMsg := parseMySQLFormWithPrivateHosts(c, 3306, false)
				require.Empty(t, errMsg)
				require.Equal(t, secretValue, createCreds.Password)

				updateCreds, errMsg := parseMySQLCredentialsForm(c)
				require.Empty(t, errMsg)
				require.Equal(t, secretValue, updateCreds.Password)
			},
		},
		{
			name: "MongoPassword",
			form: func() url.Values {
				form := url.Values{}
				form.Set("host", "mongo.example.com")
				form.Set("database", "app")
				form.Set("username", "app")
				form.Set("password", secretValue)
				return form
			}(),
			run: func(t *testing.T, c fiber.Ctx) {
				t.Helper()
				_, createCreds, errMsg := parseMongoFormWithPrivateHosts(c, 27017, false)
				require.Empty(t, errMsg)
				require.Equal(t, secretValue, createCreds.Password)

				updateCreds, errMsg := parseMongoCredentialsForm(c)
				require.Empty(t, errMsg)
				require.Equal(t, secretValue, updateCreds.Password)
			},
		},
		{
			name: "HTTPAPIKey",
			form: url.Values{"base_url": {"https://api.example.com"}, "api_key": {secretValue}, "api_key_header": {"Authorization"}},
			run: func(t *testing.T, c fiber.Ctx) {
				_, creds, errMsg := parseHTTPFormWithPrivateHosts(c, false)
				require.Empty(t, errMsg)
				require.Equal(t, secretValue, *creds.ApiKey)
				require.Equal(t, api.HTTPCredentialsApiKeyHeader("Authorization"), *creds.ApiKeyHeader)
				updated, message := parseHTTPCredentialsForm(c)
				require.Empty(t, message)
				require.Equal(t, creds, updated)
			},
		},
		{
			name: "HTTPSecretHeaders",
			form: url.Values{"base_url": {"https://api.example.com"}, "secret_header_name_0": {"Authorization"}, "secret_header_value_0": {secretValue}, "secret_header_name_1": {"X-Custom"}, "secret_header_value_1": {secretValue}},
			run: func(t *testing.T, c fiber.Ctx) {
				_, credentials, diagnostic := parseHTTPFormWithPrivateHosts(c, false)
				require.Empty(t, diagnostic)
				require.Equal(t, map[string]string{"Authorization": secretValue, "X-Custom": secretValue}, *credentials.SecretHeaders)
				updated, diagnostic := parseHTTPCredentialsForm(c)
				require.Empty(t, diagnostic)
				require.Equal(t, credentials, updated)
			},
		},
		{
			name: "HTTPCredentials",
			form: func() url.Values {
				form := url.Values{}
				form.Set("base_url", "https://api.example.com")
				form.Set("bearer_token", secretValue)
				form.Set("username", "api")
				form.Set("password", secretValue)
				return form
			}(),
			run: func(t *testing.T, c fiber.Ctx) {
				t.Helper()
				_, createCreds, errMsg := parseHTTPFormWithPrivateHosts(c, false)
				require.Empty(t, errMsg)
				require.NotNil(t, createCreds.BearerToken)
				require.Equal(t, secretValue, *createCreds.BearerToken)
				require.NotNil(t, createCreds.Password)
				require.Equal(t, secretValue, *createCreds.Password)

				updateCreds, errMsg := parseHTTPCredentialsForm(c)
				require.Empty(t, errMsg)
				require.NotNil(t, updateCreds.BearerToken)
				require.Equal(t, secretValue, *updateCreds.BearerToken)
				require.NotNil(t, updateCreds.Password)
				require.Equal(t, secretValue, *updateCreds.Password)
			},
		},
		{
			name: "HubSpotToken",
			form: func() url.Values {
				form := url.Values{}
				form.Set("private_app_token", secretValue)
				return form
			}(),
			run: func(t *testing.T, c fiber.Ctx) {
				t.Helper()
				creds, errMsg := parseHubSpotCredentialsForm(c)
				require.Empty(t, errMsg)
				require.Equal(t, secretValue, creds.PrivateAppToken)
			},
		},
		{
			name: "S3Secrets",
			form: func() url.Values {
				form := url.Values{}
				form.Set("access_key_id", "AKIAEXAMPLE")
				form.Set("secret_access_key", secretValue)
				form.Set("session_token", secretValue)
				return form
			}(),
			run: func(t *testing.T, c fiber.Ctx) {
				t.Helper()
				creds, errMsg := parseS3CredentialsForm(c)
				require.Empty(t, errMsg)
				require.NotNil(t, creds.SecretAccessKey)
				require.Equal(t, secretValue, *creds.SecretAccessKey)
				require.NotNil(t, creds.SessionToken)
				require.Equal(t, secretValue, *creds.SessionToken)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			withDatasourceFormContext(t, tt.form, func(c fiber.Ctx) {
				tt.run(t, c)
			})
		})
	}
}

func TestRouter_HandleCreateDataSourceParseURI_JSON(t *testing.T) {
	t.Parallel()

	app, authCookie := setupAuthenticatedDatasourceTestApp(t)

	form := url.Values{}
	form.Set("type", "postgres")
	form.Set("uri", "postgres://app_user:s3cr3t@db.example.com:5432/production?sslmode=verify-ca")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme/data-sources/parse-uri",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `"success":true`)
	require.Contains(t, string(body), `"type":"postgres"`)
	require.Contains(t, string(body), `"host":"db.example.com"`)
	require.Contains(t, string(body), `"port":"5432"`)
	require.Contains(t, string(body), `"database":"production"`)
	require.Contains(t, string(body), `"username":"app_user"`)
	require.Contains(t, string(body), `"sslmode":"verify-ca"`)
}

func TestRouter_HandleCreateDataSourceParseURI_JSON_InferTypeFromURI(t *testing.T) {
	t.Parallel()

	app, authCookie := setupAuthenticatedDatasourceTestApp(t)

	form := url.Values{}
	form.Set("type", "")
	form.Set("uri", "https://api.example.com:8443/health")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme/data-sources/parse-uri",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `"success":true`)
	require.Contains(t, string(body), `"type":"http"`)
	require.Contains(t, string(body), `"base_url":"https://api.example.com:8443"`)
	require.Contains(t, string(body), `"health_path":"/health"`)
}

func TestRouter_HandleCreateDataSourceParseURI_JSON_TypeMismatchFallsBackToURI(t *testing.T) {
	t.Parallel()

	app, authCookie := setupAuthenticatedDatasourceTestApp(t)

	form := url.Values{}
	form.Set("type", "postgres")
	form.Set(
		"uri",
		"mongodb+srv://myDatabaseUser:D1fficultP%40ssw0rd@cluster0.example.mongodb.net/?retryWrites=true&w=majority",
	)

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme/data-sources/parse-uri",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `"success":true`)
	require.Contains(t, string(body), `"type":"mongodb"`)
	require.Contains(t, string(body), `"host":"cluster0.example.mongodb.net"`)
	require.Contains(t, string(body), `"username":"myDatabaseUser"`)
	require.Contains(t, string(body), `"password":"D1fficultP@ssw0rd"`)
}

func TestRouter_HandleDataSourceCreateSubmit_RedirectsOnSuccess(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/organizations/acme/data-sources", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		ds := map[string]any{
			"name": "Prod DB",
			"slug": "prod-db",
			"type": "postgres",
			"config": map[string]any{
				"host":     "db.example.com",
				"port":     5432,
				"database": "prod",
			},
			"is_active":     false,
			"created_at":    "2026-03-01T00:00:00Z",
			"updated_at":    "2026-03-01T00:00:00Z",
			"schema_status": "not_imported",
			"schema": map[string]any{
				"schema_type": "sql",
				"type":        "postgres",
				"tables":      []any{},
			},
			"last_tested_at": nil,
			"test_error":     nil,
		}
		_ = json.NewEncoder(w).Encode(ds)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	form := url.Values{}
	form.Set("name", "Prod DB")
	form.Set("type", "postgres")
	form.Set("host", "db.example.com")
	form.Set("port", "5432")
	form.Set("database", "prod")
	form.Set("username", "app_user")
	form.Set("password", "secret")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme/datasources",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/acme/data-sources/prod-db", resp.Header.Get("Location"))
}

func TestRouter_HandleDataSourceCreateSubmit_HTTPSendsOpenAPIImportConfig(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	handlerErrs := make(chan error, 1)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			handlerErrs <- fmt.Errorf("method = %s, want %s", r.Method, http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/api/organizations/acme/data-sources" {
			handlerErrs <- fmt.Errorf("path = %s, want /api/organizations/acme/data-sources", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		defer func() { _ = r.Body.Close() }()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			handlerErrs <- fmt.Errorf("decode create data source payload: %w", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "Customer API",
			"slug": "customer-api",
			"type": "http",
			"config": map[string]any{
				"base_url":                      "https://api.example.com",
				"health_path":                   "/health",
				"openapi_url":                   "https://api.example.com/openapi.yaml",
				"openapi_fetch_timeout_seconds": 20,
			},
			"is_active":      true,
			"created_at":     "2026-03-01T00:00:00Z",
			"updated_at":     "2026-03-01T00:00:00Z",
			"last_tested_at": nil,
			"test_error":     nil,
		})
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	form := url.Values{}
	form.Set("name", "Customer API")
	form.Set("type", "http")
	form.Set("base_url", "https://api.example.com")
	form.Set("health_path", "/health")
	form.Set("openapi_url", "https://api.example.com/openapi.yaml")
	form.Set("openapi_fetch_timeout_seconds", "20")
	form.Set("bearer_token", "test-token")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme/datasources",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	select {
	case handlerErr := <-handlerErrs:
		require.NoError(t, handlerErr)
	default:
	}
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/acme/data-sources/customer-api", resp.Header.Get("Location"))
	require.Equal(t, "Customer API", captured["name"])
	require.Equal(t, "http", captured["type"])

	config, ok := captured["config"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "https://api.example.com", config["base_url"])
	require.Equal(t, "/health", config["health_path"])
	require.Equal(t, "https://api.example.com/openapi.yaml", config["openapi_url"])
	require.InDelta(t, 20, config["openapi_fetch_timeout_seconds"], 0.000001)

	credentials, ok := captured["credentials"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "test-token", credentials["bearer_token"])
}

func TestRouter_HandleDataSourceCreateSubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/organizations/acme/data-sources", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		ds := map[string]any{
			"name": "Prod DB",
			"slug": "prod-db",
			"type": "postgres",
			"config": map[string]any{
				"host":     "db.example.com",
				"port":     5432,
				"database": "prod",
			},
			"is_active":     false,
			"created_at":    "2026-03-01T00:00:00Z",
			"updated_at":    "2026-03-01T00:00:00Z",
			"schema_status": "not_imported",
			"schema": map[string]any{
				"schema_type": "sql",
				"type":        "postgres",
				"tables":      []any{},
			},
			"last_tested_at": nil,
			"test_error":     nil,
		}
		_ = json.NewEncoder(w).Encode(ds)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	form := url.Values{}
	form.Set("name", "Prod DB")
	form.Set("type", "postgres")
	form.Set("host", "db.example.com")
	form.Set("port", "5432")
	form.Set("database", "prod")
	form.Set("username", "app_user")
	form.Set("password", "secret")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme/datasources",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/orgs/acme/data-sources/prod-db", resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_HandleDataSourceCreateSubmit_ShowsErrorOnMissingName(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	form := url.Values{}
	form.Set("name", "")
	form.Set("type", "postgres")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme/datasources",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "Name is required")
}

func TestRouter_HandleDataSourceCreateSubmit_ShowsErrorOnMissingType(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	form := url.Values{}
	form.Set("name", "My DS")
	form.Set("type", "")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme/datasources",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "Select a datasource type")
}

func TestRouter_HandleDataSourceCreateSubmit_ShowsErrorOnAPIFailure(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write(
			[]byte(`{"error":{"message":"connection test failed","code":"connection_failed"}}`),
		)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	form := url.Values{}
	form.Set("name", "Prod DB")
	form.Set("slug", "saved-source")
	form.Set("type", "postgres")
	form.Set("host", "db.example.com")
	form.Set("port", "5432")
	form.Set("database", "prod")
	form.Set("username", "app_user")
	form.Set("password", "secret")

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme/datasources",
		strings.NewReader(form.Encode()))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(t, string(body), "connection test failed")
	require.Contains(t, string(body), `name="name"`)
	require.Contains(t, string(body), `value="Prod DB"`)
	require.Contains(t, string(body), `value="saved-source"`)
	require.NotContains(t, string(body), `value="secret"`)
}

func TestValidateGitRepoURLForFormRejectsEmbeddedCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		repoURL     string
		wantMessage string
	}{
		{
			name:        "HTTPSUserInfo",
			repoURL:     "https://alice:secret@example.com/acme/pipelines.git",
			wantMessage: "Validation: repository URL must not include embedded credentials.",
		},
		{
			name:        "SSHUserInfo",
			repoURL:     "ssh://alice:secret@example.com/acme/pipelines.git",
			wantMessage: "Validation: repository URL must not include embedded credentials.",
		},
		{
			name:        "HTTPSUsernameOnlyUserInfo",
			repoURL:     "https://alice@example.com/acme/pipelines.git",
			wantMessage: "Validation: repository URL must not include embedded credentials.",
		},
		{
			name:        "SCPStyleUserInfo",
			repoURL:     "alice:secret@example.com:acme/pipelines.git",
			wantMessage: "Validation: repository URL must not include embedded credentials.",
		},
		{
			name:    "SSHUsernameOnlyUserInfo",
			repoURL: "ssh://git@example.com/acme/pipelines.git",
		},
		{
			name:    "SCPStyleSSH",
			repoURL: "git@example.com:acme/pipelines.git",
		},
		{
			name:    "HTTPS",
			repoURL: "https://example.com/acme/pipelines.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := validateGitRepoURLForForm(tt.repoURL)
			require.Equal(t, tt.wantMessage, got)
		})
	}
}

func TestRouter_HandleStartBigQueryDataSourceOAuthSubmit_RedirectsToProvider(t *testing.T) {
	t.Parallel()

	const authorizationURL = "https://accounts.google.com/o/oauth2/v2/auth?client_id=client-id&state=opaque-state"
	var captured map[string]any
	var decodeErr error
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/provider-connectors/bigquery/data-source-installs":
			defer func() { _ = r.Body.Close() }()
			decodeErr = json.NewDecoder(r.Body).Decode(&captured)
			if decodeErr != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{
				"data_source":{"slug":"bigquery-crm","name":"BigQuery CRM","type":"bigquery","config":{"timeout_seconds":15},"is_active":false,"read_only":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},
				"install":{"authorization_url":"` + authorizationURL + `","expires_at":"2026-03-01T00:10:00Z","intent":"create_grant","provider":"bigquery","requested_scopes":["crm.objects.contacts.read"],"slug":"bigquery-install"}
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	form := url.Values{}
	form.Set("name", "BigQuery CRM")
	form.Set("slug", "bigquery-crm")
	form.Set("type", "bigquery")
	form.Set("bigquery_access_mode", "write")
	form.Set("timeout_seconds", "15")
	form.Set("project_id", "fixture")
	form.Set("default_dataset", "crm")
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/bigquery/oauth",
		strings.NewReader(form.Encode()),
	)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, decodeErr)
	require.Equal(t, authorizationURL, resp.Header.Get("HX-Redirect"))
	require.Equal(t, "BigQuery CRM", captured["name"])
	require.Equal(t, "bigquery-crm", captured["slug"])
	require.Equal(t, "bigquery", captured["type"])
	require.Equal(t, false, captured["read_only"])
	require.NotContains(t, captured, "credentials")
	config, ok := captured["config"].(map[string]any)
	require.True(t, ok)
	require.InDelta(t, 15, config["timeout_seconds"], 0.000001)
	require.Equal(t, "fixture", config["project_id"])
	require.Equal(t, "crm", config["default_dataset"])
}

func TestSnowflakeObjectSelectionRoundTrip(t *testing.T) {
	for _, sink := range []bool{false, true} {
		t.Run(fmt.Sprint(sink), func(t *testing.T) {
			app := fiber.New()
			app.Post("/", func(c fiber.Ctx) error {
				config, message := parseSnowflakeConfigForm(c)
				require.Empty(t, message)
				require.NotNil(t, config.WifObjects)
				require.Equal(t, `Orders.with "quotes"`, (*config.WifObjects)[0].Name)
				require.Equal(t, !sink, *snowflakeReadOnly(config))
				return c.JSON(config)
			})
			field := "snowflake_select_tables"
			if sink {
				field = "snowflake_insert_tables"
			}
			values := url.Values{"account": {"acme-account"}, "warehouse": {"WH"}, "database": {"DB"}, "schema": {"PUBLIC"}, "role": {"DEDICATED"}, field: {`Orders.with "quotes"`}}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(values.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			var config api.SnowflakeConfig
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&config))
			var union api.DataSource_Config
			require.NoError(t, union.FromSnowflakeConfig(config))
			form := dataSourceUpdateFormDataFromDataSource(&api.DataSource{Type: api.DataSourceTypeSnowflake, Config: union})
			if sink {
				require.Contains(t, form.SnowflakeInsertTables, `Orders.with "quotes"`)
			} else {
				require.Contains(t, form.SnowflakeSelectTables, `Orders.with "quotes"`)
			}
		})
	}
}

func TestSnowflakeOnboardingFragmentUsesBackendAuthenticationState(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusNotFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if !strings.HasSuffix(r.URL.Path, "/snowflake-onboarding") {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write([]byte(`{"identity_state":"pending","diagnostic":"Provisioning","sql":"customer SQL"}`))
				} else {
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			defer backend.Close()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			app := fiber.New()
			app.Get("/orgs/:slug/data-sources/:dsSlug/snowflake-onboarding", router.handleSnowflakeOnboarding)
			resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/orgs/acme/data-sources/warehouse/snowflake-onboarding", nil))
			require.NoError(t, err)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			switch status {
			case http.StatusOK:
				require.Contains(t, string(body), "Provisioning")
			case http.StatusBadRequest:
				require.Contains(t, string(body), "selected tables, then retry")
			case http.StatusNotFound:
				require.Empty(t, body)
			}
		})
	}
}

func TestManagedGitRepositoryListFailureIsNotMissingConnection(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if strings.HasSuffix(request.URL.Path, "/gitlab-connection/repositories") {
					w.WriteHeader(status)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer apiServer.Close()
			router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			app := fiber.New()
			var form views.DataSourceCreateFormData
			app.Get("/", func(c fiber.Ctx) error {
				router.loadManagedGitRepositories(c, "acme", &form)
				return c.SendStatus(http.StatusNoContent)
			})
			response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			require.False(t, form.GitLabConnectionAvailable)
			if status == http.StatusServiceUnavailable {
				require.Contains(t, form.GitLabRepositoryError, "Could not load")
			} else {
				require.Empty(t, form.GitLabRepositoryError)
			}
		})
	}
}

func TestConnectionChecksUseExecutionDeadline(t *testing.T) {
	for _, stored := range []bool{false, true} {
		t.Run(fmt.Sprintf("stored=%t", stored), func(t *testing.T) {
			for _, shorter := range []bool{false, true} {
				recorder := &deadlineRecordingRoundTripper{}
				client, err := api.NewClientWithResponses("https://example.com", api.WithHTTPClient(&http.Client{Transport: defaultTimeoutRoundTripper{base: recorder, timeout: frontendAPIDefaultTimeout}}))
				require.NoError(t, err)
				router := Router{apiClient: client}
				ctx := t.Context()
				if shorter {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, time.Second)
					defer cancel()
				}
				var status int
				if stored {
					status, _, err = router.testExistingDataSourceConnectionViaAPI(ctx, "", "acme", "warehouse")
				} else {
					status, _, err = router.testDataSourceConnectionViaAPI(ctx, "", "acme", api.TestDataSourceConnectionJSONRequestBody{})
				}
				require.NoError(t, err)
				require.Equal(t, http.StatusNoContent, status)
				require.True(t, recorder.hasDeadline)
				if shorter {
					require.LessOrEqual(t, time.Until(recorder.deadline), time.Second)
				} else {
					require.Greater(t, time.Until(recorder.deadline), frontendAPIDefaultTimeout)
					require.LessOrEqual(t, time.Until(recorder.deadline), frontendAPIDefaultExecutionTimeout)
				}
			}
		})
	}
}

func TestRouter_DataSourceOperationsLoadsOnlyScopedPermittedQueries(t *testing.T) {
	for _, tt := range []struct {
		name, section, want string
		queryStatus         int
		canRead             bool
		wantCalls           int32
	}{
		{"saved", "operations", "Read acceptance Sheet", http.StatusOK, true, 1},
		{"empty", "operations", "No saved queries", http.StatusOK, true, 1},
		{"failed", "operations", "Queries unavailable", http.StatusServiceUnavailable, true, 1},
		{"restricted", "operations", "Query access required", http.StatusOK, false, 0},
		{"overview", "overview", "Connection", http.StatusOK, true, 0},
		{"schema", "schema", "Schema browser", http.StatusOK, true, 0},
		{"settings", "settings", "Recheck access", http.StatusOK, true, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/organizations/acme-hq/roles/admin":
					if tt.canRead {
						handleTestAdminRolePermissions(w, r)
					} else {
						writeJSON(w, `{"slug":"admin","permissions":[{"resource":"datasources","action":"read"}]}`)
					}
				case "/api/organizations":
					writeJSON(w, `{"organizations":[{"slug":"acme-hq","name":"Acme","member_role":"admin"}]}`)
				case "/api/organizations/acme-hq/projects":
					writeJSON(w, `{"projects":[{"slug":"ops","name":"Ops"}]}`)
				case "/api/organizations/acme-hq/projects/ops/environments":
					writeJSON(w, `{"project_environments":[{"environment_slug":"dev","environment_name":"Development","is_default":true}]}`)
				case "/api/organizations/acme-hq/data-sources/workspace":
					writeJSON(w, `{"slug":"workspace","name":"Workspace","type":"google_workspace","config":{"access_mode":"selected_files"},"schema_status":"not_imported"}`)
				case "/api/organizations/acme-hq/data-sources/workspace/queries":
					calls.Add(1)
					assert.Equal(t, "20", r.URL.Query().Get("limit"))
					assert.Empty(t, r.URL.Query().Get("include_deleted"))
					if tt.queryStatus != http.StatusOK {
						w.WriteHeader(tt.queryStatus)
						return
					}
					if tt.name == "empty" {
						writeJSON(w, `{"queries":[],"has_more":false}`)
						return
					}
					writeJSON(w, `{"queries":[{"slug":"test-1098","name":"Read acceptance Sheet","statement":{"type":"google_workspace","resource":"spreadsheet","operation":"read","spreadsheet_id":"book","worksheet_id":12,"range":"A1:C3"}}],"has_more":true}`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer apiServer.Close()
			store := session.NewStore()
			router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			cookie := getAuthenticatedSessionCookie(t, app)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/orgs/acme-hq/data-sources/workspace?section="+tt.section+"&environment_slug=dev", nil)
			req.Header.Set("Cookie", cookie)
			// Allow headroom for loopback API calls while coverage tests share the CPU.
			resp, err := app.Test(req, fiber.TestConfig{Timeout: 3 * time.Second, FailOnTimeout: true})
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Contains(t, string(body), tt.want)
			require.Equal(t, tt.wantCalls, calls.Load())
			if tt.name == "saved" {
				require.Contains(t, string(body), "/orgs/acme-hq/data-sources/workspace/queries/test-1098?environment_slug=dev")
				require.Contains(t, string(body), "Showing the first page")
			}
		})
	}
}

func TestWorkspaceCachedSchemaDisplaysIdentityWithoutClaimingContentFields(t *testing.T) {
	var schema api.IntrospectSchemaResponse
	require.NoError(t, json.Unmarshal([]byte(`{"schema_type":"file","type":"google_workspace","files":[{"name":"crm.json","resource_id":"crm-json","mime_type":"application/json","fields":[]}]}`), &schema))
	panel := dataSourceSchemaPanelData(&api.DataSource{Type: api.DataSourceTypeGoogleWorkspace, SchemaStatus: api.DataSourceSchemaStatusReady, Schema: schema}, "")
	require.Empty(t, panel.Error)
	require.Contains(t, panel.Lines, "file crm.json")
	require.Contains(t, panel.Lines, "  ID: crm-json")
	require.Contains(t, panel.Lines, "  Type: application/json")
	require.NotContains(t, panel.Lines, "  (no fields)")
}

func TestWorkspaceConnectionInfoPreservesUnknownAccessMode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		want   string
	}{
		{name: "missing", config: `{}`, want: "unknown"},
		{name: "selected", config: `{"access_mode":"selected_files"}`, want: "selected_files"},
		{name: "legacy", config: `{"access_mode":"all_files"}`, want: "all_files"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ds api.DataSource
			require.NoError(t, json.Unmarshal([]byte(`{"type":"google_workspace","config":`+tc.config+`}`), &ds))
			info := extractConnectionInfo(&ds)
			require.Len(t, info.Fields, 1)
			assert.Equal(t, tc.want, info.Fields[0].Value)
		})
	}
}

func TestFileSchemaDistinguishesStructuredAndRawFiles(t *testing.T) {
	var schema api.IntrospectSchemaResponse
	require.NoError(t, json.Unmarshal([]byte(`{"schema_type":"file","type":"git","files":[{"name":"users.csv","fields":[{"name":"id","type":"string"}]},{"name":"nested/report.pdf","fields":[]}]}`), &schema))
	_, lines, diagnostic := formatSchemaForDisplay(&schema, &api.DataSource{Type: api.DataSourceTypeGit})
	require.Empty(t, diagnostic)
	require.Contains(t, lines, "  Structured file · inferred fields")
	require.Contains(t, lines, "  Raw file · format: file")
	require.Contains(t, lines, "file nested/report.pdf")
}

func TestFileSchemaDisplaysInferenceFailure(t *testing.T) {
	var schema api.IntrospectSchemaResponse
	require.NoError(t, json.Unmarshal([]byte(`{"schema_type":"file","type":"git","files":[{"name":"bad.json","fields":[]}],"diagnostics":[{"name":"bad.json","target":"bad.json","severity":"warning","code":"parse_failed","message":"Sample could not be parsed."}]}`), &schema))
	_, lines, diagnostic := formatSchemaForDisplay(&schema, &api.DataSource{Type: api.DataSourceTypeGit})
	require.Empty(t, diagnostic)
	require.Contains(t, lines, "  Fields not inferred · Sample could not be parsed.")
	require.NotContains(t, lines, "  Raw file · format: file")
}

func TestS3DataSourceCreateAndTestPayloadsKeepCredentialsWriteOnly(t *testing.T) {
	t.Parallel()
	for _, allowPrivate := range []bool{false, true} {
		t.Run(fmt.Sprint(allowPrivate), func(t *testing.T) {
			withDatasourceFormContext(t, s3DataSourceFormValues(), func(c fiber.Ctx) {
				payload, errMsg := buildCreateDataSourcePayloadWithPrivateHosts(c, allowPrivate)
				require.Empty(t, errMsg)
				created, err := payload.AsCreateS3DataSourceRequest()
				require.NoError(t, err)
				require.Equal(t, "braidplane-fixtures", created.Config.Bucket)
				require.Equal(t, "us-east-1", created.Config.Region)
				require.Equal(t, "exports/", *created.Config.Prefix)
				require.True(t, *created.Config.ForcePathStyle)
				if allowPrivate {
					require.True(t, *created.Config.AllowPrivateHosts)
				} else {
					require.Nil(t, created.Config.AllowPrivateHosts)
				}
				require.Equal(t, "secret-key", *created.Credentials.SecretAccessKey)
				testPayload, err := toTestDataSourceConnectionPayload(payload)
				require.NoError(t, err)
				tested, err := testPayload.AsTestS3DataSourceConnectionRequest()
				require.NoError(t, err)
				require.Equal(t, created.Config, tested.Config)
				require.Equal(t, created.Credentials, tested.Credentials)

				router := &Router{allowPrivateDataSourceHosts: allowPrivate}
				sticky := router.dataSourceCreateFormDataFromRequest(c)
				require.Equal(t, "http://localhost:39000", sticky.S3Endpoint)
				require.Equal(t, allowPrivate, sticky.S3AllowPrivateHosts)
				var out bytes.Buffer
				require.NoError(t, views.DataSourcesPageContent(nil, nil, false, "", "", "acme", "dev", "", nil,
					views.DefaultDataSourceSchemaPanelData(), nil, views.DataSourceUpdateFormData{}, sticky,
					views.NewOrganizationPermissions("datasources:create"), "", "", views.DataSourcePageFeedback{}).Render(context.Background(), &out))
				html := out.String()
				require.Contains(t, html, "S3-compatible storage")
				require.Contains(t, html, `name="bucket" value="braidplane-fixtures"`)
				require.Equal(t, allowPrivate, strings.Contains(html, `name="allow_private_hosts"`))
				for _, secret := range []string{"access-key", "secret-key", "session-token"} {
					require.NotContains(t, html, `value="`+secret+`"`)
				}
			})
		})
	}
}

func TestRouterLegacyS3CreateRedirectsToCanonicalConnectionForm(t *testing.T) {
	t.Parallel()
	app, cookie := setupAuthenticatedDatasourceTestApp(t)
	form := url.Values{"name": {"Settlement Files"}, "slug": {"settlement-files"}, "type": {"s3"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/organizations/acme/datasources", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", cookie)
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/acme/data-sources?name=Settlement+Files&slug=settlement-files&type=s3", resp.Header.Get("Location"))
}
