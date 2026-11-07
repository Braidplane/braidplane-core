package http

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOpenAPIGeneratesSafeHTTPDraft(t *testing.T) {
	t.Parallel()

	schema, err := parseOpenAPI([]byte(`{
  "openapi": "3.0.0",
  "info": {"title": "Example", "version": "1.0.0"},
  "servers": [{"url": "https://api.example.test/v1"}],
  "paths": {
    "/accounts/{account-id}": {
      "parameters": [{"name": "account-id", "in": "path", "required": true, "description": "Path account", "schema": {"type": "string"}}],
      "post": {
        "operationId": "createAccountRecord",
        "parameters": [
          {"name": "account-id", "in": "path", "required": true, "description": "Overridden account", "schema": {"type": "string", "format": "uuid"}},
          {"name": "page", "in": "query", "schema": {"type": "integer", "default": 10, "example": 99}},
          {"name": "X-Request-ID", "in": "header", "schema": {"type": "string"}}
        ],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "required": ["name", "profile"], "properties": {
          "name": {"type": "string", "example": "not copied"},
          "profile": {"type": "object", "required": ["active"], "properties": {"active": {"type": "boolean"}}}
		}}}}},
        "responses": {"200": {"description": "ok"}}
      }
    }
  }
}`))
	require.NoError(t, err)
	require.Len(t, schema.Operations, 1)
	op := schema.Operations[0]
	require.Empty(t, op.Diagnostics)
	require.NotEmpty(t, op.Draft)
	require.NotContains(t, string(op.Draft), "not copied")
	require.NotContains(t, string(op.Draft), "99")

	var draft struct {
		Statement struct {
			Type        string         `json:"type"`
			Method      string         `json:"method"`
			Path        string         `json:"path"`
			QueryParams map[string]any `json:"query_params"`
			Headers     map[string]any `json:"headers"`
			Body        map[string]any `json:"body"`
		} `json:"statement"`
		Parameters []openAPIDraftParameter `json:"parameters"`
	}
	require.NoError(t, json.Unmarshal(op.Draft, &draft))
	require.Equal(t, "http", draft.Statement.Type)
	require.Equal(t, "POST", draft.Statement.Method)
	require.Equal(t, "/accounts/{{account_id}}", draft.Statement.Path)
	require.Equal(t, "{{page}}", draft.Statement.QueryParams["page"])
	require.Equal(t, "{{X_Request_ID}}", draft.Statement.Headers["X-Request-ID"])
	require.Equal(t, "{{body_name}}", draft.Statement.Body["name"])
	profile := draft.Statement.Body["profile"].(map[string]any)
	require.Equal(t, "{{body_profile_active}}", profile["active"])
	require.Equal(t, []openAPIDraftParameter{
		{Name: "X_Request_ID", DataType: "text"},
		{Name: "account_id", DataType: "uuid", IsRequired: true, Description: "Overridden account"},
		{Name: "page", DataType: "integer"},
		{Name: "body_name", DataType: "text", IsRequired: true},
		{Name: "body_profile_active", DataType: "boolean", IsRequired: true},
	}, draft.Parameters)
}

func TestParseOpenAPIDraftRejectsUnsafeOrUnsupportedShapes(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		path           string
		operation      string
		wantDiagnostic string
	}{
		"unsafe path":        {"/records?tenant=other", `"get": {"responses": {"200": {"description": "ok"}}}`, "safe relative path"},
		"credential header":  {"/records", `"get": {"parameters": [{"name": "X-API-Key", "in": "header", "schema": {"type": "string"}, "example": "provider-secret"}], "responses": {"200": {"description": "ok"}}}`, "credential-like parameter"},
		"cookie parameter":   {"/records", `"get": {"parameters": [{"name": "session", "in": "cookie", "schema": {"type": "string"}}], "responses": {"200": {"description": "ok"}}}`, "unsupported serialization"},
		"unsupported method": {"/records", `"options": {"responses": {"200": {"description": "ok"}}}`, "HTTP method is not supported"},
		"operation server":   {"/records", `"get": {"servers": [{"url": "https://other.example.test"}], "responses": {"200": {"description": "ok"}}}`, "server overrides"},
		"body key template":  {"/records", `"post": {"requestBody": {"content": {"application/json": {"schema": {"type": "object", "properties": {"{{undeclared}}": {"type": "string"}}}}}}, "responses": {"200": {"description": "ok"}}}`, "request body property names cannot contain template expressions"},
		"array body":         {"/records", `"post": {"requestBody": {"content": {"application/json": {"schema": {"type": "array", "items": {"type": "string"}}}}}, "responses": {"200": {"description": "ok"}}}`, "request body schema is unsupported"},
	} {
		t.Run(name, func(t *testing.T) {
			spec := []byte(`{"openapi":"3.0.0","info":{"title":"Example","version":"1.0.0"},"paths":{"` + testCase.path + `":{` + testCase.operation + `}}}`)
			schema, err := parseOpenAPI(spec)
			require.NoError(t, err)
			require.Len(t, schema.Operations, 1)
			require.Empty(t, schema.Operations[0].Draft)
			require.Contains(t, strings.Join(schema.Operations[0].Diagnostics, " "), testCase.wantDiagnostic)
			require.NotContains(t, strings.Join(schema.Operations[0].Diagnostics, " "), "provider-secret")
		})
	}
}

func TestDraftParameterNamesAreUniqueASCII(t *testing.T) {
	used := map[string]int{}
	var names []string
	for _, name := range []string{"id", "id_2", "id", "é", "1-user"} {
		names = append(names, draftParameterName(name, used))
	}
	require.Equal(t, []string{"id", "id_2", "id_3", "param_", "user"}, names)
}

func TestDraftPathDiagnostics(t *testing.T) {
	for _, path := range []string{"https://other.test/x", "//other.test/x", "/a/../b", "/%2e%2e/b", "/%252e%252e/b", "/a\\b", "/a%0Ab", "/a?x=1", "/a#b", "/a/{{injected}}"} {
		require.NotEmpty(t, draftPathDiagnostics(path), path)
	}
}
