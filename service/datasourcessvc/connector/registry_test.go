package connector

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	hubspotclient "github.com/Braidplane/braidplane-core/pkg/hubspot/crm"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	bigqueryconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery"
	mysqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/mysql"
	postgresconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/postgres"
)

type dataSourceDocumentation struct {
	label string
	path  string
}

var dataSourceDocumentationByType = map[model.DataSourceType]dataSourceDocumentation{
	model.DataSourceTypeGoogleWorkspace: {"Google Workspace", "data-sources/GOOGLE_WORKSPACE.md"},
	model.DataSourceTypePostgres:        {"PostgreSQL", "data-sources/POSTGRES.md"},
	model.DataSourceTypeMySQL:           {"MySQL", "data-sources/MYSQL.md"},
	model.DataSourceTypeBigQuery:        {"BigQuery", "data-sources/BIGQUERY.md"},
	model.DataSourceTypeHTTP:            {"HTTP", "data-sources/HTTP.md"},
	model.DataSourceTypeMongoDB:         {"MongoDB", "data-sources/MONGODB.md"},
	model.DataSourceTypeHubSpot:         {"HubSpot", "data-sources/HUBSPOT.md"},
	model.DataSourceTypeFTP:             {"FTP", "data-sources/FTP.md"},
	model.DataSourceTypeGit:             {"Git", "data-sources/GIT.md"},
	model.DataSourceTypeGithub:          {"GitHub", "data-sources/GIT.md"},
	model.DataSourceTypeGitlab:          {"GitLab", "data-sources/GIT.md"},
	model.DataSourceTypeS3:              {"S3", "data-sources/S3.md"},
	model.DataSourceTypeSnowflake:       {"Snowflake", "data-sources/SNOWFLAKE.md"},
}

// TestConnectorDefinitionsCoverCatalogDescriptors keeps connector construction support
// aligned with the catalog descriptors used by service and pipeline runtime code.
func TestConnectorDefinitionsCoverCatalogDescriptors(t *testing.T) {
	t.Parallel()

	definitions := allConnectorDefinitions()
	byType := make(map[model.DataSourceType]connectorDefinition, len(definitions))
	for _, definition := range definitions {
		require.NotEqual(t, model.DataSourceTypeUnknown, definition.dataSourceType)
		require.NotEmptyf(t, definition.operationType, "%s operation type", definition.dataSourceType)
		require.NotNilf(t, definition.configDecoder, "%s config decoder", definition.dataSourceType)
		require.NotNilf(t, definition.credentialsDecoder, "%s credentials decoder", definition.dataSourceType)
		require.NotNilf(t, definition.prepare, "%s preparation hook", definition.dataSourceType)
		require.NotNilf(t, definition.privateHostCheck, "%s private host hook", definition.dataSourceType)
		require.NotNilf(t, definition.constructor, "%s constructor", definition.dataSourceType)

		_, ok := byType[definition.dataSourceType]
		require.Falsef(t, ok, "duplicate connector definition for %s", definition.dataSourceType)
		byType[definition.dataSourceType] = definition

		descriptor, ok := catalog.DescriptorFor(definition.dataSourceType)
		require.Truef(t, ok, "connector definition without catalog descriptor for %s", definition.dataSourceType)
		require.Equalf(
			t,
			descriptor.QueryOperationType,
			definition.operationType,
			"connector definition operation type drifted from catalog for %s",
			definition.dataSourceType,
		)
	}

	for _, descriptor := range catalog.AllDescriptors() {
		_, ok := byType[descriptor.Type]
		require.Truef(t, ok, "catalog descriptor without connector definition for %s", descriptor.Type)
	}
}

// TestDecodeConnectorPayloadsReturnsFalseForUnsupportedType locks down the
// registry lookup behavior used by unsupported data source validation paths.
func TestDecodeConnectorPayloadsReturnsFalseForUnsupportedType(t *testing.T) {
	t.Parallel()

	payloads, ok, err := DecodeConnectorPayloads(
		model.DataSourceTypeUnknown,
		[]byte(`{}`),
		[]byte(`{}`),
	)

	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, DecodedConnectorPayloads{}, payloads)
}

// TestDecodeConnectorPayloadsMapsMalformedJSONToValidationFailed keeps customer
// config and credential parse failures classified as invalid input.
func TestDecodeConnectorPayloadsMapsMalformedJSONToValidationFailed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		configJSON      []byte
		credentialsJSON []byte
	}{
		{
			name:            "Config",
			configJSON:      []byte(`{`),
			credentialsJSON: []byte(`{}`),
		},
		{
			name:            "Credentials",
			configJSON:      []byte(`{}`),
			credentialsJSON: []byte(`{`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			payloads, ok, err := DecodeConnectorPayloads(
				model.DataSourceTypePostgres,
				tt.configJSON,
				tt.credentialsJSON,
			)

			require.ErrorIs(t, err, xerror.ValidationFailed())
			require.True(t, ok)
			require.Equal(t, DecodedConnectorPayloads{}, payloads)
		})
	}
}

// TestDecodeConnectorPayloadsAppliesSQLPreparation verifies the registry owns
// the SQL credential merge and construction defaults previously embedded in the factory switch.
func TestDecodeConnectorPayloadsAppliesSQLPreparation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		dsType      model.DataSourceType
		configJSON  []byte
		credentials []byte
		assert      func(*testing.T, DecodedConnectorPayloads)
	}{
		{
			name:        "Postgres",
			dsType:      model.DataSourceTypePostgres,
			configJSON:  []byte(`{"host":"db.example.com","port":5432,"database":"analytics"}`),
			credentials: []byte(`{"username":"pg-user","password":"pg-pass"}`),
			assert: func(t *testing.T, payloads DecodedConnectorPayloads) {
				t.Helper()

				cfg, ok := payloads.Config.(postgresconnector.Config)
				require.True(t, ok)
				require.Equal(t, "pg-user", cfg.User)
				require.Equal(t, "pg-pass", cfg.Password)
				require.Equal(t, 5, cfg.ConnectTimeout)
			},
		},
		{
			name:        "MySQL",
			dsType:      model.DataSourceTypeMySQL,
			configJSON:  []byte(`{"host":"db.example.com","port":3306,"database":"analytics"}`),
			credentials: []byte(`{"username":"my-user","password":"my-pass"}`),
			assert: func(t *testing.T, payloads DecodedConnectorPayloads) {
				t.Helper()

				cfg, ok := payloads.Config.(mysqlconnector.Config)
				require.True(t, ok)
				require.Equal(t, "my-user", cfg.User)
				require.Equal(t, "my-pass", cfg.Password)
				require.Equal(t, 5, cfg.ConnectTimeout)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			payloads, ok, err := DecodeConnectorPayloads(tt.dsType, tt.configJSON, tt.credentials)

			require.NoError(t, err)
			require.True(t, ok)
			require.False(t, payloads.PrivateHostsRequested)
			tt.assert(t, payloads)
		})
	}
}

func TestDecodeMySQLCredentialsAcceptsExplicitEmptyPassword(t *testing.T) {
	t.Parallel()
	config := []byte(`{"host":"db.example.com","port":3306,"database":"analytics"}`)
	payloads, ok, err := DecodeConnectorPayloads(model.DataSourceTypeMySQL, config, []byte(`{"username":"reader","password":""}`))
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, payloads.Config.(mysqlconnector.Config).Password)
	require.NotNil(t, payloads.Credentials.(mysqlconnector.Credentials).Password)
	require.NoError(t, validate.Default().Struct(payloads.Config))
	require.NoError(t, validate.Default().Struct(payloads.Credentials))
	for _, credentials := range []string{`{"username":"reader"}`, `{"username":"reader","password":null}`} {
		_, _, err := DecodeConnectorPayloads(model.DataSourceTypeMySQL, config, []byte(credentials))
		require.ErrorIs(t, err, xerror.ValidationFailed())
	}
}

// TestDecodeConnectorPayloadsReportsCustomerPrivateHostOptIn preserves which
// connector configs are governed by customer private-host deployment policy.
func TestDecodeConnectorPayloadsReportsCustomerPrivateHostOptIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		dsType      model.DataSourceType
		configJSON  []byte
		credentials []byte
		want        bool
	}{
		{
			name:        "Postgres",
			dsType:      model.DataSourceTypePostgres,
			configJSON:  []byte(`{"host":"db.example.com","port":5432,"database":"analytics","allow_private_hosts":true}`),
			credentials: []byte(`{"username":"pg-user","password":"pg-pass"}`),
			want:        true,
		},
		{
			name:        "MySQL",
			dsType:      model.DataSourceTypeMySQL,
			configJSON:  []byte(`{"host":"db.example.com","port":3306,"database":"analytics","allow_private_hosts":true}`),
			credentials: []byte(`{"username":"my-user","password":"my-pass"}`),
			want:        true,
		},
		{
			name:        "MongoDB",
			dsType:      model.DataSourceTypeMongoDB,
			configJSON:  []byte(`{"host":"db.example.com","port":27017,"database":"analytics","allow_private_hosts":true}`),
			credentials: []byte(`{"username":"mongo-user","password":"mongo-pass"}`),
			want:        true,
		},
		{
			name:        "HTTP",
			dsType:      model.DataSourceTypeHTTP,
			configJSON:  []byte(`{"base_url":"https://api.example.com","allow_private_hosts":true}`),
			credentials: []byte(`{"bearer_token":"token-123"}`),
			want:        true,
		},
		{
			name:        "FTP",
			dsType:      model.DataSourceTypeFTP,
			configJSON:  []byte(`{"host":"ftp.example.com","port":21,"allow_private_hosts":true}`),
			credentials: []byte(`{"username":"ftp-user","password":"secret"}`),
			want:        true,
		},
		{
			name:        "Git",
			dsType:      model.DataSourceTypeGit,
			configJSON:  []byte(`{"repo_url":"https://github.com/acme/repo.git","allow_private_hosts":true}`),
			credentials: []byte(`{"auth_type":"https"}`),
			want:        true,
		},
		{
			name:        "GitHub",
			dsType:      model.DataSourceTypeGithub,
			configJSON:  []byte(`{"repo_url":"https://github.com/acme/repo.git","allow_private_hosts":true}`),
			credentials: []byte(`{"auth_type":"https"}`),
			want:        true,
		},
		{
			name:        "GitLab",
			dsType:      model.DataSourceTypeGitlab,
			configJSON:  []byte(`{"repo_url":"https://gitlab.com/acme/repo.git","allow_private_hosts":true}`),
			credentials: []byte(`{"auth_type":"https"}`),
			want:        true,
		},
		{
			name:        "S3",
			dsType:      model.DataSourceTypeS3,
			configJSON:  []byte(`{"endpoint":"https://s3.example.com","region":"us-east-1","bucket":"fixtures","allow_private_hosts":true}`),
			credentials: []byte(`{"access_key_id":"access-key","secret_access_key":"secret-key"}`),
			want:        true,
		},
		{
			name:        "BigQueryOperatorOverride",
			dsType:      model.DataSourceTypeBigQuery,
			configJSON:  []byte(`{"project_id":"analytics-prod","allow_private_hosts":true}`),
			credentials: []byte(`{}`),
			want:        false,
		},
		{
			name:        "HubSpotOperatorOverride",
			dsType:      model.DataSourceTypeHubSpot,
			configJSON:  []byte(`{"base_url":"https://api.hubapi.com","allow_private_hosts":true}`),
			credentials: []byte(`{}`),
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			payloads, ok, err := DecodeConnectorPayloads(tt.dsType, tt.configJSON, tt.credentials)

			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, tt.want, payloads.PrivateHostsRequested)
			if tt.dsType == model.DataSourceTypeBigQuery {
				_, ok = payloads.Config.(bigqueryconnector.Config)
				require.True(t, ok)
			}
			if tt.dsType == model.DataSourceTypeHubSpot {
				_, ok = payloads.Config.(hubspotclient.Config)
				require.True(t, ok)
			}
		})
	}
}

func TestNormalizeCustomerConfigRejectsInternalProviderOverrideKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		dsType     model.DataSourceType
		configJSON []byte
	}{
		{
			name:       "BigQueryBaseURL",
			dsType:     model.DataSourceTypeBigQuery,
			configJSON: []byte(`{"project_id":"analytics-prod","base_url":"https://bigquery.example.com"}`),
		},
		{
			name:       "BigQueryTokenURL",
			dsType:     model.DataSourceTypeBigQuery,
			configJSON: []byte(`{"project_id":"analytics-prod","token_url":"https://oauth.example.com/token"}`),
		},
		{
			name:       "BigQueryAllowPrivateHosts",
			dsType:     model.DataSourceTypeBigQuery,
			configJSON: []byte(`{"project_id":"analytics-prod","allow_private_hosts":true}`),
		},
		{
			name:       "HubSpotBaseURL",
			dsType:     model.DataSourceTypeHubSpot,
			configJSON: []byte(`{"base_url":"https://api.hubapi.com"}`),
		},
		{
			name:       "HubSpotAllowPrivateHosts",
			dsType:     model.DataSourceTypeHubSpot,
			configJSON: []byte(`{"allow_private_hosts":true}`),
		},
		{
			name:       "SnowflakeWIFTokenFile",
			dsType:     model.DataSourceTypeSnowflake,
			configJSON: []byte(`{"account":"acme","warehouse":"WH","database":"DB","schema":"PUBLIC","_wif_token_file":"/tmp/token"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			normalized, ok, err := NormalizeCustomerConfig(tt.dsType, tt.configJSON)

			require.True(t, ok)
			require.Nil(t, normalized)
			require.ErrorContains(t, err, "unknown field")
		})
	}
}

// TestCatalogOperationTypesAreSharedOnlyWhereExpected makes duplicated operation
// types an explicit review point instead of an accidental catalog collision.
func TestCatalogOperationTypesAreSharedOnlyWhereExpected(t *testing.T) {
	t.Parallel()

	allowedSharedOperationTypes := map[string][]model.DataSourceType{
		catalog.OperationTypeSQL: {
			model.DataSourceTypePostgres,
			model.DataSourceTypeMySQL,
		},
		catalog.OperationTypeGit: {
			model.DataSourceTypeGit,
			model.DataSourceTypeGithub,
			model.DataSourceTypeGitlab,
		},
	}

	byOperationType := make(map[string][]model.DataSourceType)
	for _, descriptor := range catalog.AllDescriptors() {
		byOperationType[descriptor.QueryOperationType] = append(
			byOperationType[descriptor.QueryOperationType],
			descriptor.Type,
		)
	}

	for operationType, dsTypes := range byOperationType {
		if len(dsTypes) == 1 {
			continue
		}
		require.ElementsMatchf(
			t,
			allowedSharedOperationTypes[operationType],
			dsTypes,
			"unexpected data source types share operation type %s",
			operationType,
		)
	}

	for operationType, expectedDSTypes := range allowedSharedOperationTypes {
		require.ElementsMatchf(
			t,
			expectedDSTypes,
			byOperationType[operationType],
			"expected operation type %s sharing changed",
			operationType,
		)
	}
}

// TestCatalogDataSourceTypesHaveDocumentation keeps supported connector docs
// close to the catalog and construction registry when a new type is introduced.
func TestCatalogDataSourceTypesHaveDocumentation(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	dataSourcesDocBytes, err := os.ReadFile(filepath.Join(root, "doc", "DATA_SOURCES.md"))
	require.NoError(t, err)
	dataSourcesDoc := string(dataSourcesDocBytes)

	for _, descriptor := range catalog.AllDescriptors() {
		documentation, ok := dataSourceDocumentationByType[descriptor.Type]
		require.Truef(t, ok, "missing documentation expectation for %s", descriptor.Type)

		docPath := filepath.Join(root, "doc", filepath.FromSlash(documentation.path))
		_, err := os.Stat(docPath)
		require.NoErrorf(t, err, "missing per-type data source doc for %s", descriptor.Type)

		link := fmt.Sprintf("[%s](%s)", documentation.label, documentation.path)
		require.Containsf(
			t,
			dataSourcesDoc,
			link,
			"doc/DATA_SOURCES.md must reference %s for %s",
			link,
			descriptor.Type,
		)
	}
}

// repositoryRoot returns the checkout root from this package's test working directory.
func repositoryRoot(t *testing.T) string {
	t.Helper()

	abs, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	return abs
}

func TestNormalizeSnowflakeConnectionTimeout(t *testing.T) {
	for _, value := range []string{"omitted", "1", "300", "0", "-1", "301"} {
		t.Run(value, func(t *testing.T) {
			raw := `{}`
			if value != "omitted" {
				raw = `{"connect_timeout":` + value + `}`
			}
			normalized, supported, err := NormalizeCustomerConfig(model.DataSourceTypeSnowflake, []byte(raw))
			require.True(t, supported)
			if value == "0" || value == "-1" || value == "301" {
				require.ErrorIs(t, err, xerror.ValidationFailed())
				return
			}
			require.NoError(t, err)
			if value == "omitted" {
				require.NotContains(t, string(normalized), "connect_timeout")
			} else {
				require.Contains(t, string(normalized), `"connect_timeout":`+value)
			}
		})
	}
}

func TestWorkspaceConstruction(t *testing.T) {
	_, err := NewConnector(t.Context(), model.DataSourceTypeGoogleWorkspace, []byte(`{"access_mode":"selected_files"}`), []byte(`{"access_token":"runtime-token","granted_scopes":["https://www.googleapis.com/auth/drive.file"]}`))
	require.NoError(t, err)
}
