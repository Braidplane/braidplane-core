package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
)

func TestOpenAPIRequestValidatorPreservesCredentialDiagnostics(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../api-schema/schema.yml")
	require.NoError(t, err)
	schema, err := openapi3.NewLoader().LoadFromData(data)
	require.NoError(t, err)
	validator, err := NewOpenAPIRequestValidator(schema)
	require.NoError(t, err)
	app := fiber.New()
	app.Use(requestid.New(), validator)
	app.Post("/api/organizations/:slug/data-sources", func(c fiber.Ctx) error {
		t.Error("invalid credentials reached the handler")
		return c.SendStatus(http.StatusCreated)
	})
	for _, tt := range []struct{ credentials, diagnostic string }{
		{`{"auth_type":"workload_identity","username":"must-not-echo"}`, `property "username" is unsupported`},
		{`{"auth_type":"oauth2","username":"must-not-echo","password":"must-not-echo"}`, "auth_type"},
	} {
		body := `{"name":"invalid","slug":"invalid","type":"snowflake","config":{"account":"acme","warehouse":"wh","database":"db","schema":"public"},"credentials":` + tt.credentials + `}`
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/organizations/acme/data-sources", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", "credential-validation")
		response, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, response.StatusCode)
		var result apierror.ErrorResponse
		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
		require.NoError(t, response.Body.Close())
		require.Equal(t, apierror.CodeValidationFailed, result.Error.Code)
		require.Equal(t, "credential-validation", result.Error.RequestID)
		require.Contains(t, result.Error.Message, tt.diagnostic)
		require.NotContains(t, result.Error.Message, "must-not-echo")
		require.NotContains(t, result.Error.Message, "\nSchema:")
		require.NotContains(t, result.Error.Message, "\nValue:")
	}
}

func TestHTTPPaginationSchema(t *testing.T) {
	data, err := os.ReadFile("../../api-schema/schema.yml")
	require.NoError(t, err)
	schema, err := openapi3.NewLoader().LoadFromData(data)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, pagination string
		valid            bool
	}{
		{"page number", `{"page_param":"page","page_size_param":"pageSize","page_size":10,"total_pages_path":"totalPages","page_number_path":"page","records_path":"items","max_pages":3,"timeout_seconds":2}`, true},
		{"mixed", `{"cursor_param":"after","next_cursor_path":"next","page_param":"page","page_size_param":"pageSize","page_size":10,"total_pages_path":"totalPages","page_number_path":"page","records_path":"items","max_pages":3,"timeout_seconds":2}`, false},
		{"no explicit termination", `{"page_param":"page","page_size_param":"pageSize","page_size":10,"records_path":"items","max_pages":3,"timeout_seconds":2}`, false},
		{"invalid page size", `{"page_param":"page","page_size_param":"pageSize","page_size":0,"total_pages_path":"totalPages","page_number_path":"page","records_path":"items","max_pages":3,"timeout_seconds":2}`, false},
		{"supported", `{"cursor_param":"after","records_path":"data.items","next_cursor_path":"paging.next","max_pages":3,"timeout_seconds":2}`, true},
		{"array index", `{"cursor_param":"after","records_path":"data[0]","next_cursor_path":"paging.next","max_pages":3,"timeout_seconds":2}`, false},
		{"wildcard", `{"cursor_param":"after","records_path":"data.items","next_cursor_path":"paging.*","max_pages":3,"timeout_seconds":2}`, false},
		{"missing limits", `{"cursor_param":"after","records_path":"data.items","next_cursor_path":"paging.next"}`, false},
		{"invalid page limit", `{"cursor_param":"after","records_path":"data.items","next_cursor_path":"paging.next","max_pages":0,"timeout_seconds":2}`, false},
		{"invalid deadline", `{"cursor_param":"after","records_path":"data.items","next_cursor_path":"paging.next","max_pages":3,"timeout_seconds":301}`, false},
		{"unsupported option", `{"cursor_param":"after","records_path":"data.items","next_cursor_path":"paging.next","max_pages":3,"timeout_seconds":2,"next_url":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			require.NoError(t, json.Unmarshal([]byte(tc.pagination), &value))
			err := schema.Components.Schemas["HTTPPagination"].Value.VisitJSON(value)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestDeploymentResolutionRequestSchema(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../api-schema/schema.yml")
	require.NoError(t, err)
	schema, err := openapi3.NewLoader().LoadFromData(data)
	require.NoError(t, err)
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name   string
		fields map[string]any
		valid  bool
	}{
		{"new resolution", map[string]any{"compiled_plan_digest": digest, "resolution_inputs": map[string]any{"KIND": map[string]any{"value": "string", "revision": "v1"}}}, true},
		{"rollback", map[string]any{"resolution_digest": digest}, true},
		{"malformed compiled digest", map[string]any{"compiled_plan_digest": "sha256:bad"}, false},
		{"malformed resolution digest", map[string]any{"resolution_digest": "sha256:bad"}, false},
		{"rollback with inputs", map[string]any{"resolution_digest": digest, "resolution_inputs": map[string]any{}}, false},
		{"rollback with compiled plan", map[string]any{"resolution_digest": digest, "compiled_plan_digest": digest}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.fields["spec_hash"] = "version"
			err := schema.Components.Schemas["PipelineDeploymentRequest"].Value.VisitJSON(tc.fields)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
