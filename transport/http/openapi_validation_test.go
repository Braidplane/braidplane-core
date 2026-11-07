package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
)

func TestOpenAPIRequestValidatorMountedRequests(t *testing.T) {
	t.Parallel()
	schema, err := openapi3.NewLoader().LoadFromData([]byte(`openapi: 3.0.4
info: {title: validation, version: "1"}
servers: [{url: /api}]
paths:
  /body/{slug}:
    post:
      security: [{BearerAuth: []}]
      parameters:
        - {in: path, name: slug, required: true, schema: {type: string}}
        - {in: query, name: count, required: true, schema: {type: integer}}
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name: {type: string}
      responses:
        "200": {description: ok}
components:
  securitySchemes:
    BearerAuth: {type: http, scheme: bearer}
`))
	require.NoError(t, err)
	validator, err := NewOpenAPIRequestValidator(schema)
	require.NoError(t, err)
	root := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	root.Use(requestid.New())
	api := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	api.Use(validator)
	api.Post("/body/:slug", func(c fiber.Ctx) error {
		var body struct {
			Name string `json:"name"`
		}
		require.NoError(t, c.Bind().Body(&body))
		require.Equal(t, "preserved", body.Name)
		require.Equal(t, "tenant", c.Params("slug"))
		return c.Send(c.Body())
	})
	root.Use("/api", api)
	tests := []struct {
		name, method, path, body string
		status                   int
	}{
		{"valid body preserved", http.MethodPost, "/api/body/tenant?count=2", `{"name":"preserved"}`, 200},
		{"malformed body", http.MethodPost, "/api/body/tenant?count=2", `{"name":`, 400},
		{"required body", http.MethodPost, "/api/body/tenant?count=2", "", 400},
		{"required field", http.MethodPost, "/api/body/tenant?count=2", `{}`, 400},
		{"required parameter", http.MethodPost, "/api/body/tenant", `{"name":"preserved"}`, 400},
		{"invalid parameter", http.MethodPost, "/api/body/tenant?count=no", `{"name":"preserved"}`, 400},
		{"unsupported method", http.MethodPut, "/api/body/tenant?count=2", `{}`, 400},
		{"unknown route", http.MethodPost, "/api/missing", `{}`, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Request-ID", "validation-request")
			resp, err := root.Test(req)
			require.NoError(t, err)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			require.Equal(t, tt.status, resp.StatusCode)
			data, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			if tt.status == 200 {
				require.Equal(t, tt.body, string(data))
				return
			}
			var response apierror.ErrorResponse
			require.NoError(t, json.Unmarshal(data, &response))
			require.Equal(t, "validation-request", response.Error.RequestID)
			require.NotEmpty(t, response.Error.Code)
		})
	}
}
