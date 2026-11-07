package apischema_test

import (
	"encoding/json"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/publicschema"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"
)

func TestOpenAPIDocumentsValidate(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromFile("schema.yml")
	require.NoError(t, err)
	require.NoError(t, document.Validate(t.Context()))
	for name, item := range document.Paths.Map() {
		require.NotEqual(t, true, item.Extensions["x-internal"], "managed path in public source: %s", name)
	}
	for name, schema := range document.Components.Schemas {
		require.NotEqual(t, true, schema.Value.Extensions["x-internal"], "managed schema in public source: %s", name)
	}
	public, err := publicschema.Project(document)
	require.NoError(t, err)
	projected, err := openapi3.NewLoader().LoadFromData(public)
	require.NoError(t, err)
	require.NoError(t, projected.Validate(t.Context()))

	operations := make(map[string]bool)
	for _, item := range projected.Paths.Map() {
		for _, operation := range item.Operations() {
			operations[operation.OperationID] = true
		}
	}
	allowlist, err := os.ReadFile("../cli/api/operations.txt")
	require.NoError(t, err)
	for _, line := range strings.Split(string(allowlist), "\n") {
		operation := strings.TrimSpace(line)
		if operation == "" || strings.HasPrefix(operation, "#") {
			continue
		}
		require.True(t, operations[operation], "CLI operation must belong to the public API: %s", operation)
	}
}

func TestErrorRequestIDAcceptsOpaqueValues(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromFile("schema.yml")
	require.NoError(t, err)
	schema := document.Components.Schemas["ErrorResponse"].Value
	require.Empty(t, document.Components.Schemas["Error"].Value.Properties["request_id"].Value.Format)
	require.NoError(t, schema.VisitJSON(map[string]any{
		"error": map[string]any{"code": "FORBIDDEN", "message": "not allowed", "request_id": "credential-validation"},
	}))
}

func TestCommunityUserProfileExcludesBillingAddress(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromFile("schema.yml")
	require.NoError(t, err)
	require.NotContains(t, document.Components.Schemas, "BillingAddress")
	for _, name := range []string{"User", "UpdateUserProfile"} {
		require.NotContains(t, document.Components.Schemas[name].Value.Properties, "billing_address")
	}
	require.Error(t, document.Components.Schemas["UpdateUserProfile"].Value.VisitJSON(map[string]any{
		"billing_address": map[string]any{"country": "PL"},
	}))
}

func TestDataSourceResponseConfigAllowsOverlappingConnectorShapes(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromFile("schema.yml")
	require.NoError(t, err)
	config := document.Components.Schemas["DataSource"].Value.Properties["config"].Value
	require.NoError(t, config.VisitJSON(map[string]any{
		"host": "database.example.test", "port": 5432, "database": "synthetic",
	}))
	require.Error(t, config.VisitJSON(map[string]any{"unexpected_field": true}))
}

func TestPinnedRevisionSchemasValidateAndRejectLatest(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromFile("schema.yml")
	require.NoError(t, err)
	for _, tc := range []struct{ schema, property string }{
		{"PipelineResourceScope", "revision"},
		{"ODCSProjectionInputSelection", "revision_slug"},
		{"PipelineODCSContractRef", "revision_slug"},
	} {
		t.Run(tc.schema, func(t *testing.T) {
			schema := document.Components.Schemas[tc.schema].Value
			require.NoError(t, schema.Validate(t.Context()))
			revision := schema.Properties[tc.property].Value
			require.NoError(t, revision.VisitJSON("revision-123"))
			require.Error(t, revision.VisitJSON("latest"))
		})
	}
}

func TestPipelineContractValidationScalarSchemasAcceptIntegers(t *testing.T) {
	t.Parallel()

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("schema.yml")
	require.NoError(t, err)

	tests := []struct {
		name       string
		schemaName string
		value      string
	}{
		{
			name:       "BoundInteger",
			schemaName: "PipelineSchemaContractValidationBound",
			value:      `1`,
		},
		{
			name:       "BoundDecimal",
			schemaName: "PipelineSchemaContractValidationBound",
			value:      `1.5`,
		},
		{
			name:       "BoundDatetime",
			schemaName: "PipelineSchemaContractValidationBound",
			value:      `"2020-01-01T00:00:00Z"`,
		},
		{
			name:       "LiteralInteger",
			schemaName: "PipelineSchemaContractValidationLiteral",
			value:      `1`,
		},
		{
			name:       "LiteralDecimal",
			schemaName: "PipelineSchemaContractValidationLiteral",
			value:      `1.5`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var value any
			decoder := json.NewDecoder(strings.NewReader(tt.value))
			decoder.UseNumber()
			require.NoError(t, decoder.Decode(&value))

			schemaRef := doc.Components.Schemas[tt.schemaName]
			require.NotNil(t, schemaRef)
			require.NoError(t, schemaRef.Value.VisitJSON(value))
		})
	}
}

func TestHubSpotPipelineSinkOperationPresentInOpenAPI(t *testing.T) {
	t.Parallel()

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("schema.yml")
	require.NoError(t, err)

	sinkSpec := doc.Components.Schemas["PipelineSinkSpec"]
	require.NotNil(t, sinkSpec)
	require.NotNil(t, sinkSpec.Value)
	operationType, ok := sinkSpec.Value.Properties["operation_type"]
	require.True(t, ok, "PipelineSinkSpec.operation_type must exist")
	require.NotNil(t, operationType.Value)
	require.Contains(t, operationType.Value.Enum, any("hubspot"))
}

func TestCommunityAccountContractsHaveNoConsentFields(t *testing.T) {
	t.Parallel()
	doc, err := openapi3.NewLoader().LoadFromFile("schema.yml")
	require.NoError(t, err)
	require.NoError(t, doc.Components.Schemas["MemberInvitationAcceptanceTokenRequest"].Value.VisitJSON(map[string]any{"token": "invitation-token"}))
	require.NotContains(t, doc.Components.Schemas, "TermsAcceptanceRequest")
	require.NotContains(t, doc.Components.Schemas["NewUser"].Value.Properties, "terms_acceptance")
}

func TestProviderOnboardingAcceptsEmptyHubSpotAndPendingWorkspaceConfigs(t *testing.T) {
	t.Parallel()
	doc, err := openapi3.NewLoader().LoadFromFile("schema.yml")
	require.NoError(t, err)
	request := doc.Components.Schemas["StartProviderDataSourceInstallRequest"].Value
	for _, payload := range []string{
		`{"name":"CRM","type":"hubspot","config":{}}`,
		`{"name":"Sheets","type":"google_workspace","config":{}}`,
		`{"name":"Sheets","type":"google_workspace","config":{"access_mode":"selected_files"}}`,
	} {
		var value any
		require.NoError(t, json.Unmarshal([]byte(payload), &value))
		require.NoError(t, request.VisitJSON(value), payload)
	}
	pending := doc.Components.Schemas["GoogleWorkspacePendingInstallConfig"].Value
	require.Error(t, pending.VisitJSON(map[string]any{"spreadsheet_id": "book"}))
	require.NoError(t, doc.Components.Schemas["GoogleWorkspaceConfig"].Value.VisitJSON(map[string]any{}))
}

// TestPublicAPIServersUseCommunityMount keeps API consumers on their own installation.
func TestPublicAPIServersUseCommunityMount(t *testing.T) {
	t.Parallel()

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("schema.yml")
	require.NoError(t, err)

	serverURLs := make([]string, 0, len(doc.Servers))
	for _, server := range doc.Servers {
		serverURLs = append(serverURLs, server.URL)
	}

	require.Equal(t, []string{
		"/api",
	}, serverURLs)

	publicDocument, err := json.Marshal(doc)
	require.NoError(t, err)

	var published struct {
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
	}
	require.NoError(t, json.Unmarshal(publicDocument, &published))
	require.Len(t, published.Servers, len(serverURLs))
	publishedServerURLs := make([]string, 0, len(published.Servers))
	for _, server := range published.Servers {
		publishedServerURLs = append(publishedServerURLs, server.URL)
	}
	require.Equal(t, serverURLs, publishedServerURLs)
}

func TestBigQueryDataSourceContractsPresentInOpenAPI(t *testing.T) {
	t.Parallel()

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("schema.yml")
	require.NoError(t, err)

	for _, schemaName := range []string{
		"BigQueryConfig",
		"BigQueryCredentials",
		"CreateBigQueryDataSourceRequest",
		"TestBigQueryDataSourceConnectionRequest",
		"BigQueryStatement",
	} {
		require.NotNilf(t, doc.Components.Schemas[schemaName], "missing schema %s", schemaName)
	}

	requireSchemaEnumContains(t, doc, "DataSourceType", "bigquery")
	requireSchemaEnumContains(t, doc, "SQLSchemaResponse", "bigquery")
	requireCompositionContains(t, doc, "DataSourceConfig", "BigQueryConfig")
	requireCompositionContains(t, doc, "DataSourceCredentials", "BigQueryCredentials")
	requireCompositionContains(t, doc, "CreateDataSourceRequest", "CreateBigQueryDataSourceRequest")
	requireDiscriminatorMapping(t, doc, "CreateDataSourceRequest", "bigquery", "CreateBigQueryDataSourceRequest")
	requireCompositionContains(t, doc, "TestDataSourceConnectionRequest", "TestBigQueryDataSourceConnectionRequest")
	requireDiscriminatorMapping(t, doc, "TestDataSourceConnectionRequest", "bigquery", "TestBigQueryDataSourceConnectionRequest")
	requireCompositionContains(t, doc, "QueryStatement", "BigQueryStatement")
	requireDiscriminatorMapping(t, doc, "QueryStatement", "bigquery", "BigQueryStatement")
}

func TestBigQueryCredentialsStayWriteOnlyInOpenAPI(t *testing.T) {
	t.Parallel()

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("schema.yml")
	require.NoError(t, err)

	credentials := doc.Components.Schemas["BigQueryCredentials"]
	require.NotNil(t, credentials)
	require.Contains(t, strings.ToLower(credentials.Value.Description), "write-only")
	require.Contains(t, strings.ToLower(credentials.Value.Description), "never returned")
	require.Contains(t, credentials.Value.Properties, "service_account_json")

	dataSource := doc.Components.Schemas["DataSource"]
	require.NotNil(t, dataSource)
	require.NotContains(t, dataSource.Value.Properties, "credentials")
	require.Contains(t, dataSource.Value.Properties, "credential")
}

func TestBigQueryStatementRequiresOperationSpecificFields(t *testing.T) {
	t.Parallel()

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("schema.yml")
	require.NoError(t, err)

	schemaRef := doc.Components.Schemas["BigQueryStatement"]
	require.NotNil(t, schemaRef)

	tests := []struct {
		name            string
		payload         string
		wantErrContains string
	}{
		{
			name:    "QueryValid",
			payload: `{"type":"bigquery","operation":"query","sql":"SELECT 1"}`,
		},
		{
			name:            "QueryMissingSQL",
			payload:         `{"type":"bigquery","operation":"query"}`,
			wantErrContains: "sql",
		},
		{
			name:    "InsertRowsValid",
			payload: `{"type":"bigquery","operation":"insert_rows","table":"orders","rows":"{{input}}"}`,
		},
		{
			name:            "InsertRowsMissingRows",
			payload:         `{"type":"bigquery","operation":"insert_rows","table":"orders"}`,
			wantErrContains: "rows",
		},
		{
			name:            "OperationOmitted",
			payload:         `{"type":"bigquery","sql":"SELECT 1"}`,
			wantErrContains: "operation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var value any
			decoder := json.NewDecoder(strings.NewReader(tt.payload))
			decoder.UseNumber()
			require.NoError(t, decoder.Decode(&value))

			err := schemaRef.Value.VisitJSON(value)
			if tt.wantErrContains != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestS3DataSourceContractsPresentInOpenAPI(t *testing.T) {
	t.Parallel()

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("schema.yml")
	require.NoError(t, err)

	for _, schemaName := range []string{
		"S3Config",
		"S3Credentials",
		"CreateS3DataSourceRequest",
		"TestS3DataSourceConnectionRequest",
		"S3Statement",
	} {
		require.NotNilf(t, doc.Components.Schemas[schemaName], "missing schema %s", schemaName)
	}

	requireSchemaEnumContains(t, doc, "DataSourceType", "s3")
	requireSchemaEnumContains(t, doc, "FileSchemaResponse", "s3")
	requireCompositionContains(t, doc, "DataSourceConfig", "S3Config")
	requireCompositionContains(t, doc, "DataSourceCredentials", "S3Credentials")
	requireCompositionContains(t, doc, "CreateDataSourceRequest", "CreateS3DataSourceRequest")
	requireDiscriminatorMapping(t, doc, "CreateDataSourceRequest", "s3", "CreateS3DataSourceRequest")
	requireCompositionContains(t, doc, "TestDataSourceConnectionRequest", "TestS3DataSourceConnectionRequest")
	requireDiscriminatorMapping(t, doc, "TestDataSourceConnectionRequest", "s3", "TestS3DataSourceConnectionRequest")
	requireCompositionContains(t, doc, "QueryStatement", "S3Statement")
	requireDiscriminatorMapping(t, doc, "QueryStatement", "s3", "S3Statement")
}

func TestS3CredentialsStayWriteOnlyInOpenAPI(t *testing.T) {
	t.Parallel()

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("schema.yml")
	require.NoError(t, err)

	credentials := doc.Components.Schemas["S3Credentials"]
	require.NotNil(t, credentials)
	require.Contains(t, strings.ToLower(credentials.Value.Description), "write-only")
	require.Contains(t, strings.ToLower(credentials.Value.Description), "never returned")
	require.Contains(t, credentials.Value.Properties, "access_key_id")
	require.Contains(t, credentials.Value.Properties, "secret_access_key")

	dataSource := doc.Components.Schemas["DataSource"]
	require.NotNil(t, dataSource)
	require.NotContains(t, dataSource.Value.Properties, "credentials")
	require.Contains(t, dataSource.Value.Properties, "credential")
}

func TestS3StatementRequiresOperationSpecificFields(t *testing.T) {
	t.Parallel()

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("schema.yml")
	require.NoError(t, err)

	schemaRef := doc.Components.Schemas["S3Statement"]
	require.NotNil(t, schemaRef)

	tests := []struct {
		name            string
		payload         string
		wantErrContains string
	}{
		{
			name:    "ReadValid",
			payload: `{"type":"s3","operation":"read","key":"exports/users.csv"}`,
		},
		{
			name:            "ReadMissingKey",
			payload:         `{"type":"s3","operation":"read"}`,
			wantErrContains: "key",
		},
		{
			name:    "WriteValid",
			payload: `{"type":"s3","operation":"write","key":"exports/users.csv","content":"{{input}}"}`,
		},
		{
			name:            "WriteMissingContent",
			payload:         `{"type":"s3","operation":"write","key":"exports/users.csv"}`,
			wantErrContains: "content",
		},
		{
			name:            "OperationOmitted",
			payload:         `{"type":"s3","key":"exports/users.csv"}`,
			wantErrContains: "operation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var value any
			decoder := json.NewDecoder(strings.NewReader(tt.payload))
			decoder.UseNumber()
			require.NoError(t, decoder.Decode(&value))

			err := schemaRef.Value.VisitJSON(value)
			if tt.wantErrContains != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
		})
	}
}

func requireSchemaEnumContains(t *testing.T, doc *openapi3.T, schemaName, value string) {
	t.Helper()

	schemaRef := doc.Components.Schemas[schemaName]
	require.NotNil(t, schemaRef)
	require.Truef(t, schemaEnumContains(schemaRef.Value, value), "%s enum does not contain %q", schemaName, value)
}

func schemaEnumContains(schema *openapi3.Schema, value string) bool {
	if schema == nil {
		return false
	}
	for _, enumValue := range schema.Enum {
		if enumValue == value {
			return true
		}
	}
	for _, property := range schema.Properties {
		if schemaEnumContains(property.Value, value) {
			return true
		}
	}
	for _, allOf := range schema.AllOf {
		if schemaEnumContains(allOf.Value, value) {
			return true
		}
	}
	return false
}

func requireCompositionContains(t *testing.T, doc *openapi3.T, schemaName, wantRefName string) {
	t.Helper()

	schemaRef := doc.Components.Schemas[schemaName]
	require.NotNil(t, schemaRef)

	refs := compositionRefNames(schemaRef.Value)
	require.Containsf(t, refs, wantRefName, "%s composition missing %s; got %v", schemaName, wantRefName, refs)
}

func compositionRefNames(schema *openapi3.Schema) []string {
	if schema == nil {
		return nil
	}

	refs := make([]string, 0, len(schema.OneOf)+len(schema.AnyOf))
	for _, candidates := range [][]*openapi3.SchemaRef{schema.OneOf, schema.AnyOf} {
		for _, candidate := range candidates {
			if candidate.Ref == "" {
				continue
			}
			refs = append(refs, path.Base(candidate.Ref))
		}
	}
	return refs
}

func requireDiscriminatorMapping(t *testing.T, doc *openapi3.T, schemaName, value, wantRefName string) {
	t.Helper()

	schemaRef := doc.Components.Schemas[schemaName]
	require.NotNil(t, schemaRef)
	require.NotNil(t, schemaRef.Value.Discriminator)

	ref, ok := schemaRef.Value.Discriminator.Mapping[value]
	require.Truef(t, ok, "%s discriminator missing value %q", schemaName, value)
	require.Equal(t, wantRefName, path.Base(ref.Ref))
}

func TestPipelineContractValidationDecimalSchemasUseDoublePrecision(t *testing.T) {
	t.Parallel()

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("schema.yml")
	require.NoError(t, err)

	for _, schemaName := range []string{
		"PipelineSchemaContractValidationBound",
		"PipelineSchemaContractValidationLiteral",
	} {
		t.Run(schemaName, func(t *testing.T) {
			t.Parallel()

			schemaRef := doc.Components.Schemas[schemaName]
			require.NotNil(t, schemaRef)

			var numberArm *openapi3.Schema
			for _, candidate := range schemaRef.Value.OneOf {
				if candidate.Value.Type != nil && candidate.Value.Type.Is("number") {
					numberArm = candidate.Value
					break
				}
			}

			require.NotNil(t, numberArm)
			require.Equal(t, "double", numberArm.Format)
		})
	}
}

func TestWorkspaceFileWriteContentContract(t *testing.T) {
	t.Parallel()
	doc, err := openapi3.NewLoader().LoadFromFile("schema.yml")
	require.NoError(t, err)
	schema := doc.Components.Schemas["GoogleWorkspaceStatement"].Value
	for _, tc := range []struct {
		name    string
		content any
		valid   bool
	}{
		{"binding", "{{input}}", true},
		{"object", map[string]any{"id": 1}, true},
		{"rows", []any{map[string]any{"id": 1}}, true},
		{"null", nil, false},
		{"boolean", true, false},
		{"number", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := schema.VisitJSON(map[string]any{"type": "google_workspace", "resource": "file", "operation": "create", "folder_id": "folder", "name": "crm.json", "format": "json", "content": tc.content})
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestODCSImportCommitPrecondition(t *testing.T) {
	t.Parallel()
	doc, err := openapi3.NewLoader().LoadFromFile("schema.yml")
	require.NoError(t, err)
	schema := doc.Paths.Value("/organizations/{slug}/projects/{project_slug}/contracts/import").Post.RequestBody.Value.Content["application/json"].Schema.Value
	for _, tc := range []struct {
		name   string
		apply  any
		commit string
		valid  bool
	}{
		{"initial preview", false, "", true},
		{"pinned preview", false, strings.Repeat("b", 40), true},
		{"malformed preview", false, "abc", false},
		{"exact apply", true, strings.Repeat("a", 40), true},
		{"empty apply", true, "", false},
		{"short apply", true, "abc", false},
		{"invalid apply", true, strings.Repeat("x", 40), false},
		{"wrong apply type", "true", strings.Repeat("a", 40), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := schema.VisitJSON(map[string]any{"apply": tc.apply, "expected_commit": tc.commit})
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestAuthenticationResponseAndPasswordSchemaContracts(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromFile("schema.yml")
	require.NoError(t, err)
	password := document.Components.Schemas["Password"].Value
	require.NoError(t, password.VisitJSON(strings.Repeat("p", 1024)))
	require.Error(t, password.VisitJSON(strings.Repeat("p", 1025)))
	body := map[string]any{
		"slug": "alice", "full_name": "Alice", "email": "alice@example.com", "role": "user", "locale": "en",
		"mfa_email_enabled": false, "has_local_password": true,
		"created_at": "2026-10-16T12:00:00Z", "updated_at": "2026-10-16T12:00:00Z",
	}
	require.NoError(t, document.Components.Schemas["User"].Value.VisitJSON(body))
	login := document.Components.Schemas["AuthenticatedUser"].Value
	require.Error(t, login.VisitJSON(body), "login requires the captured credential generation")
	body["authentication_version"] = float64(1)
	require.NoError(t, login.VisitJSON(body))
}

func TestCompositionAttachmentAPISchema(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("schema.yml")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, schema, value string
		valid               bool
	}{
		{"event reference", "PipelineInputSpec", `{"output_contract_ref":{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row","representation":"json","root_shape":"object"}}`, true},
		{"event conflict", "PipelineInputSpec", `{"output_contract":{"name":"","representation":"json","fields":[{"name":"id","type":"integer","required":true}]},"output_contract_ref":{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row","representation":"json","root_shape":"object"}}`, false},
		{"event missing root", "PipelineInputSpec", `{"output_contract_ref":{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row","representation":"json"}}`, false},
		{"named inline", "PipelineInputContractAttachment", `{"contract":{"name":"","representation":"rows","fields":[{"name":"id","type":"integer","required":true}]}}`, true},
		{"named reference", "PipelineInputContractAttachment", `{"contract_ref":{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row"}}`, true},
		{"named file reference", "PipelineInputContractAttachment", `{"contract_ref":{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row","representation":"file"}}`, false},
		{"named multipart reference", "PipelineInputContractAttachment", `{"contract_ref":{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row","representation":"multipart_form"}}`, false},
		{"named file inline", "PipelineInputContractAttachment", `{"contract":{"name":"","representation":"file","fields":[{"name":"id","type":"integer","required":true}]}}`, false},
		{"named conflict", "PipelineInputContractAttachment", `{"contract":{"name":"","fields":[{"name":"id","type":"integer","required":true}]},"contract_ref":{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row"}}`, false},
		{"empty attachment", "PipelineInputContractAttachment", `{}`, false},
		{"combined conflict", "PipelineTransformSpec", `{"inputs":{"orders":"fetch"},"sql":"SELECT id FROM orders","output_kind":"rows","input_contracts":{"orders":{"contract_ref":{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row"}}},"input_contract":{"name":"","fields":[{"name":"id","type":"integer","required":true}]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			require.NoError(t, json.Unmarshal([]byte(tc.value), &value))
			err := doc.Components.Schemas[tc.schema].Value.VisitJSON(value)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestHTTPPaginationTraversalPatterns(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("schema.yml")
	require.NoError(t, err)
	for _, tc := range []struct {
		options string
		valid   bool
	}{
		{`"next_link_path":"paging.next"`, true},
		{`"cursor_param":"after","next_cursor_path":"next"`, true},
		{`"page_param":"page","page_size_param":"size","page_size":1,"page_number_path":"page","total_pages_path":"total"`, true},
		{`"next_link_path":"next","cursor_param":"after","next_cursor_path":"cursor"`, false},
		{`"next_link_path":"next","page_param":"page"`, false},
		{`"next_link_path":"items[].next"`, false},
	} {
		t.Run(tc.options, func(t *testing.T) {
			var value any
			require.NoError(t, json.Unmarshal([]byte(`{"records_path":"items","max_pages":3,"timeout_seconds":5,`+tc.options+`}`), &value))
			err := doc.Components.Schemas["HTTPPagination"].Value.VisitJSON(value)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestCommunityHasNoBillingProfileAPI(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromFile("schema.yml")
	require.NoError(t, err)
	for _, path := range []string{"/users/profile/billing-profile", "/organizations/{slug}/billing-profile"} {
		require.Nil(t, document.Paths.Value(path))
	}
	for _, name := range []string{"BillingProfile", "BillingProfileAddress", "BillingProfileOwnerType", "UpsertBillingProfile"} {
		require.NotContains(t, document.Components.Schemas, name)
	}
}
