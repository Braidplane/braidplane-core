package http

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
)

// TestFormatFromURL tests URL format detection from file extensions.
func TestFormatFromURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		url             string
		expected        OpenAPIFormat
		wantErrContains string
	}{
		{
			name:            "JSONExtension",
			url:             "https://example.com/api/spec.json",
			expected:        OpenAPIFormatJSON,
			wantErrContains: "",
		},
		{
			name:            "YAMLExtension",
			url:             "https://example.com/api/spec.yaml",
			expected:        OpenAPIFormatYAML,
			wantErrContains: "",
		},
		{
			name:            "YMLExtension",
			url:             "https://example.com/api/spec.yml",
			expected:        OpenAPIFormatYAML,
			wantErrContains: "",
		},
		{
			name:            "JSONWithQueryParams",
			url:             "https://example.com/api/spec.json?version=3.0.0",
			expected:        OpenAPIFormatJSON,
			wantErrContains: "",
		},
		{
			name:            "NoExtension",
			url:             "https://example.com/api/spec",
			expected:        "",
			wantErrContains: "no file extension",
		},
		{
			name:            "WrongExtension",
			url:             "https://example.com/api/spec.xml",
			expected:        "",
			wantErrContains: "unsupported",
		},
		{
			name:            "CasedExtensionJSON",
			url:             "https://example.com/api/spec.JSON",
			expected:        OpenAPIFormatJSON,
			wantErrContains: "",
		},
		{
			name:            "CasedExtensionYAML",
			url:             "https://example.com/api/spec.YAML",
			expected:        OpenAPIFormatYAML,
			wantErrContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			format, err := formatFromURL(tt.url)
			if tt.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErrContains)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expected, format)
		})
	}
}

func TestParseOpenAPIWithQuotaCountsOperationSummaries(t *testing.T) {
	t.Parallel()

	ctx := schemaquota.WithQuota(transportsecurity.WithPlaintext(context.Background()), 1, 18)
	_, err := parseOpenAPIWithQuota(ctx, []byte(`{
  "openapi": "3.0.0",
  "info": {"title": "Quota", "version": "1.0.0"},
  "paths": {
    "/users": {
      "get": {
        "operationId": "getUsers",
        "responses": {"200": {"description": "A response summary that exceeds the remaining quota."}}
      }
    }
  }
}`))

	require.ErrorContains(t, err, "schema result exceeds configured byte limit")
}

// TestParseOpenAPI_WithRealSchemaYAML tests parseOpenAPI with the actual Braidplane API schema in YAML
// format.
func TestParseOpenAPI_WithRealSchemaYAML(t *testing.T) {
	t.Parallel()

	// Load the real schema from api-schema/schema.yml
	schemaPath := filepath.Join("..", "..", "..", "..", "api-schema", "schema.yml")
	schemaYAML, err := os.ReadFile(schemaPath)
	require.NoError(t, err, "failed to read api-schema/schema.yml")

	// parseOpenAPI should handle the real schema without errors
	httpSchema, err := parseOpenAPI(schemaYAML)
	require.NoError(t, err, "failed to parse real schema")
	require.NotNil(t, httpSchema)

	// The real schema should have operations
	require.NotEmpty(t, httpSchema.Operations, "expected real schema to have operations")

	// Build operation map for lookups
	operationsByID := make(map[string]*Operation)
	operationsByPath := make(map[string][]Operation)
	for i, op := range httpSchema.Operations {
		if op.OperationID != "" {
			operationsByID[op.OperationID] = &httpSchema.Operations[i]
		}
		operationsByPath[op.Path] = append(operationsByPath[op.Path], op)
	}

	// Verify operation structure
	for _, op := range httpSchema.Operations {
		assert.NotEmpty(t, op.OperationID, "operation should have an operationId")
		assert.NotEmpty(t, op.Path, "operation should have a path")
		assert.NotEmpty(t, op.Method, "operation should have an HTTP method")
		assert.Contains(
			t,
			[]string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS", "HEAD"},
			op.Method,
		)
	}

	// Verify Braidplane-specific resources are present in the schema
	// Organizations
	organizationOps := []string{
		"createOrganization",
		"listOrganizations",
		"getOrganization",
		"updateOrganization",
	}
	for _, opID := range organizationOps {
		assert.NotNil(t, operationsByID[opID], "expected to find organization operation: %s", opID)
	}

	// Roles
	roleOps := []string{
		"createRole",
		"listRoles",
		"getRole",
		"updateRole",
		"deleteRole",
	}
	for _, opID := range roleOps {
		assert.NotNil(t, operationsByID[opID], "expected to find role operation: %s", opID)
	}

	// Members
	memberOps := []string{
		"createMember",
		"listMembers",
		"updateMember",
		"removeMember",
	}
	for _, opID := range memberOps {
		assert.NotNil(t, operationsByID[opID], "expected to find member operation: %s", opID)
	}

	// Data sources
	dsOps := []string{
		"createDataSource",
		"listDataSources",
		"getDataSource",
		"updateDataSource",
	}
	for _, opID := range dsOps {
		assert.NotNil(t, operationsByID[opID], "expected to find data source operation: %s", opID)
	}
}

// TestParseOpenAPI_InvalidFormat tests that parseOpenAPI rejects invalid input.
func TestParseOpenAPI_InvalidFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		input           []byte
		wantErrContains string
	}{
		{
			name:            "MalformedJSON",
			input:           []byte(`{invalid json`),
			wantErrContains: "json",
		},
		{
			name:            "EmptyString",
			input:           []byte(``),
			wantErrContains: "info section",
		},
		{
			name:            "MalformedYAML",
			input:           []byte(`{invalid: yaml: format`),
			wantErrContains: "yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseOpenAPI(tt.input)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErrContains)
		})
	}
}

// TestParseOpenAPI_MissingRequiredFields tests that parseOpenAPI validates required fields.
func TestParseOpenAPI_MissingRequiredFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		spec            map[string]any
		wantErrContains string
	}{
		{
			name: "MissingInfo",
			spec: map[string]any{
				"openapi": "3.0.0",
				"paths":   map[string]any{},
			},
			wantErrContains: "info",
		},
		{
			name: "ValidMinimalSpec",
			spec: map[string]any{
				"openapi": "3.0.0",
				"info": map[string]any{
					"title":   "Test API",
					"version": "1.0.0",
				},
				"paths": map[string]any{},
			},
			wantErrContains: "",
		},
		{
			name: "ValidSpecWithOperations",
			spec: map[string]any{
				"openapi": "3.0.0",
				"info": map[string]any{
					"title":   "Test API",
					"version": "1.0.0",
				},
				"paths": map[string]any{
					"/users": map[string]any{
						"get": map[string]any{
							"operationId": "getUsers",
							"summary":     "List users",
							"responses": map[string]any{
								"200": map[string]any{
									"description": "Success",
									"content": map[string]any{
										"application/json": map[string]any{
											"schema": map[string]any{
												"type": "array",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			wantErrContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			specJSON, err := json.Marshal(tt.spec)
			require.NoError(t, err)

			schema, err := parseOpenAPI(specJSON)
			if tt.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErrContains)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, schema)
		})
	}
}

func TestParseOpenAPI_IncludesOperationResponseMetadata(t *testing.T) {
	t.Parallel()

	spec := map[string]any{
		"openapi": "3.0.0",
		"info": map[string]any{
			"title":   "Response Metadata API",
			"version": "1.0.0",
		},
		"paths": map[string]any{
			"/users": map[string]any{
				"get": map[string]any{
					"operationId": "listUsers",
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Success",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"type": "array",
										"items": map[string]any{
											"type": "object",
										},
									},
								},
							},
						},
						"400": map[string]any{
							"description": "Bad request",
						},
					},
				},
			},
			"/users/{id}": map[string]any{
				"delete": map[string]any{
					"operationId": "deleteUser",
					"responses": map[string]any{
						"204": map[string]any{
							"description": "Deleted",
						},
					},
				},
			},
			"/reports": map[string]any{
				"get": map[string]any{
					"operationId": "getReport",
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Report",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"type": "object",
									},
								},
								"text/csv": map[string]any{},
							},
						},
					},
				},
			},
			"/health": map[string]any{
				"get": map[string]any{
					"operationId": "healthCheck",
					"responses": map[string]any{
						"200": map[string]any{
							"description": "OK",
							"content": map[string]any{
								"application/json": map[string]any{},
							},
						},
					},
				},
			},
		},
	}

	specJSON, err := json.Marshal(spec)
	require.NoError(t, err)

	httpSchema, err := parseOpenAPI(specJSON)
	require.NoError(t, err)
	require.NotNil(t, httpSchema)

	operationsByID := make(map[string]Operation, len(httpSchema.Operations))
	for _, op := range httpSchema.Operations {
		operationsByID[op.OperationID] = op
	}

	require.Contains(t, operationsByID, "listUsers")
	require.Contains(t, operationsByID, "deleteUser")
	require.Contains(t, operationsByID, "getReport")
	require.Contains(t, operationsByID, "healthCheck")

	assertResponseSummaryHasStatusCodes(t, operationsByID["listUsers"].Responses, "200", "400")
	assertResponseSummaryHasStatusCodes(t, operationsByID["deleteUser"].Responses, "204")
	assertResponseSummaryHasStatusCodes(t, operationsByID["getReport"].Responses, "200")
	assertResponseSummaryHasStatusCodes(t, operationsByID["healthCheck"].Responses, "200")

	listUsersResponses := decodeOperationMetadataJSON(t, operationsByID["listUsers"].Responses)
	listUsers200 := asObjectMap(t, listUsersResponses["200"])
	listUsers200Content := asObjectMap(t, listUsers200["content"])
	listUsersJSON := asObjectMap(t, listUsers200Content["application/json"])
	listUsersSchemaSummary := asObjectMap(t, listUsersJSON["schema_summary"])
	assert.Equal(t, "array", listUsersSchemaSummary["type"])

	deleteUserResponses := decodeOperationMetadataJSON(t, operationsByID["deleteUser"].Responses)
	deleteUser204 := asObjectMap(t, deleteUserResponses["204"])
	_, hasContent := deleteUser204["content"]
	assert.False(t, hasContent)

	healthCheckResponses := decodeOperationMetadataJSON(t, operationsByID["healthCheck"].Responses)
	healthCheck200 := asObjectMap(t, healthCheckResponses["200"])
	healthContent := asObjectMap(t, healthCheck200["content"])
	healthJSON := asObjectMap(t, healthContent["application/json"])
	_, hasSchemaSummary := healthJSON["schema_summary"]
	assert.False(t, hasSchemaSummary)
}

func TestParseOpenAPI_ExpandsRequestAndResponseFieldsForReferencedSchemas(t *testing.T) {
	t.Parallel()

	schemaPath := filepath.Join("..", "..", "..", "..", "api-schema", "schema.yml")
	schemaYAML, err := os.ReadFile(schemaPath)
	require.NoError(t, err, "failed to read api-schema/schema.yml")

	httpSchema, err := parseOpenAPI(schemaYAML)
	require.NoError(t, err)
	require.NotNil(t, httpSchema)

	var createDataSourceOp *Operation
	for i := range httpSchema.Operations {
		if httpSchema.Operations[i].OperationID == "createDataSource" {
			createDataSourceOp = &httpSchema.Operations[i]
			break
		}
	}
	require.NotNil(t, createDataSourceOp, "expected createDataSource operation in parsed schema")

	// Introspection should expose request schema fields, not just operation metadata.
	require.NotEmpty(t, createDataSourceOp.RequestBody)

	requestBody := decodeOperationMetadataJSON(t, createDataSourceOp.RequestBody)
	requestContent := asObjectMap(t, requestBody["content"])
	requestJSON := asObjectMap(t, requestContent["application/json"])
	requestSchema := asObjectMap(t, requestJSON["schema_summary"])
	assert.NotContains(t, requestSchema, "$ref")

	requestProperties := asObjectMap(t, requestSchema["properties"])
	assert.Contains(t, requestProperties, "name")
	assert.Contains(t, requestProperties, "type")
	assert.Contains(t, requestProperties, "config")
	assert.Contains(t, requestProperties, "credentials")

	// Response schema should also expose DataSource fields.
	responses := decodeOperationMetadataJSON(t, createDataSourceOp.Responses)
	response201 := asObjectMap(t, responses["201"])
	response201Content := asObjectMap(t, response201["content"])
	responseJSON := asObjectMap(t, response201Content["application/json"])
	responseSchema := asObjectMap(t, responseJSON["schema_summary"])
	assert.NotContains(t, responseSchema, "$ref")

	responseProperties := asObjectMap(t, responseSchema["properties"])
	assert.Contains(t, responseProperties, "slug")
	assert.Contains(t, responseProperties, "name")
	assert.Contains(t, responseProperties, "type")
	assert.Contains(t, responseProperties, "config")
	assert.Contains(t, responseProperties, "is_active")
}

func decodeOperationMetadataJSON(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	require.NotEmpty(t, raw)

	decoded := make(map[string]any)
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return decoded
}

func assertResponseSummaryHasStatusCodes(t *testing.T, raw json.RawMessage, statusCodes ...string) {
	t.Helper()
	decoded := decodeOperationMetadataJSON(t, raw)
	for _, statusCode := range statusCodes {
		require.Contains(t, decoded, statusCode)
	}
}

func asObjectMap(t *testing.T, value any) map[string]any {
	t.Helper()
	objectMap, ok := value.(map[string]any)
	require.True(t, ok)
	return objectMap
}

// TestGenerateOperationID tests the operationId generation fallback.
func TestGenerateOperationID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method   string
		path     string
		expected string
	}{
		{"GET", "/users", "getUsers"},
		{"POST", "/users", "postUsers"},
		{"PUT", "/users/{id}", "putUsersId"},
		{"DELETE", "/users/{id}", "deleteUsersId"},
		{"GET", "/users/{id}/posts/{postId}", "getUsersIdPostsPostId"},
		{"PATCH", "/users/{userId}/comments/{commentId}", "patchUsersUserIdCommentsCommentId"},
		{"GET", "/", "get"},
		{"POST", "/api/v1/users", "postApiV1Users"},
	}

	for _, tt := range tests {
		t.Run(tt.method+"_"+tt.path, func(t *testing.T) {
			t.Parallel()

			result := generateOperationID(tt.method, tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestParseOpenAPI_OperationID tests that operationId is used from spec or generated.
func TestParseOpenAPI_OperationID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		spec          map[string]any
		expectedOpIDs []string
	}{
		{
			name: "ExplicitOperationID",
			spec: map[string]any{
				"openapi": "3.0.0",
				"info": map[string]any{
					"title":   "Test",
					"version": "1.0.0",
				},
				"paths": map[string]any{
					"/users": map[string]any{
						"get": map[string]any{
							"operationId": "listAllUsers",
						},
					},
				},
			},
			expectedOpIDs: []string{"listAllUsers"},
		},
		{
			name: "GeneratedOperationID",
			spec: map[string]any{
				"openapi": "3.0.0",
				"info": map[string]any{
					"title":   "Test",
					"version": "1.0.0",
				},
				"paths": map[string]any{
					"/users": map[string]any{
						"post": map[string]any{
							"summary": "Create user",
						},
					},
				},
			},
			expectedOpIDs: []string{"postUsers"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			specJSON, err := json.Marshal(tt.spec)
			require.NoError(t, err)

			schema, err := parseOpenAPI(specJSON)
			require.NoError(t, err)

			if len(tt.expectedOpIDs) > 0 {
				require.NotEmpty(t, schema.Operations)
				assert.Equal(t, tt.expectedOpIDs[0], schema.Operations[0].OperationID)
			}
		})
	}
}

// TestParseOpenAPI_EmptySpec tests that empty specs are handled gracefully.
func TestParseOpenAPI_EmptySpec(t *testing.T) {
	t.Parallel()

	spec := map[string]any{
		"openapi": "3.0.0",
		"info": map[string]any{
			"title":   "Empty API",
			"version": "1.0.0",
		},
		"paths": map[string]any{},
	}

	specJSON, err := json.Marshal(spec)
	require.NoError(t, err)

	schema, err := parseOpenAPI(specJSON)
	require.NoError(t, err)
	assert.Empty(t, schema.Operations)
}

// TestParseOpenAPI_MultipleHTTPMethods tests that all HTTP methods are parsed.
func TestParseOpenAPI_MultipleHTTPMethods(t *testing.T) {
	t.Parallel()

	spec := map[string]any{
		"openapi": "3.0.0",
		"info": map[string]any{
			"title":   "Test",
			"version": "1.0.0",
		},
		"paths": map[string]any{
			"/users": map[string]any{
				"get": map[string]any{
					"operationId": "getUsers",
				},
				"post": map[string]any{
					"operationId": "createUser",
				},
				"put": map[string]any{
					"operationId": "updateUsers",
				},
				"delete": map[string]any{
					"operationId": "deleteUsers",
				},
				"patch": map[string]any{
					"operationId": "patchUsers",
				},
			},
		},
	}

	specJSON, err := json.Marshal(spec)
	require.NoError(t, err)

	schema, err := parseOpenAPI(specJSON)
	require.NoError(t, err)

	methods := make(map[string]bool)
	for _, op := range schema.Operations {
		methods[op.Method] = true
	}

	assert.True(t, methods["GET"])
	assert.True(t, methods["POST"])
	assert.True(t, methods["PUT"])
	assert.True(t, methods["DELETE"])
	assert.True(t, methods["PATCH"])
	assert.Len(t, schema.Operations, 5)
}
