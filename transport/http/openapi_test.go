package http

import (
	"io"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
)

func TestPublicOpenAPIDocumentExcludesPrivateAdministration(t *testing.T) {
	raw, err := os.ReadFile("../../api-schema/schema.yml")
	require.NoError(t, err)
	internal, err := openapi3.NewLoader().LoadFromData(raw)
	require.NoError(t, err)
	public, err := PublicOpenAPIDocument(internal)
	require.NoError(t, err)
	for _, private := range []string{"/admin/", "AdminDemo", "AdminIdentity", "current global super_admin authorization", "tester email addresses are returned", "settlement-review-reset", "SettlementReviewResetStatus"} {
		require.NotContains(t, string(public), private)
	}
	projected, err := openapi3.NewLoader().LoadFromData(public)
	require.NoError(t, err)
	require.NotNil(t, projected.Paths.Value("/organizations"))
	require.Nil(t, projected.Paths.Value("/admin/demos"))
	require.Nil(t, internal.Paths.Value("/admin/demos"))
	require.NotContains(t, internal.Components.Schemas, "AdminDemo")
	require.Nil(t, projected.Paths.Value("/organizations/{slug}/projects/{project_slug}/settlement-review-reset"))
	require.Nil(t, internal.Paths.Value("/organizations/{slug}/projects/{project_slug}/settlement-review-reset"))
	require.NotContains(t, internal.Components.Schemas, "SettlementReviewResetStatus")
}

// TestRegisterOpenAPIRouteServesExactDocument verifies programmatic consumers
// receive the application-owned OpenAPI JSON without authentication middleware.
func TestRegisterOpenAPIRouteServesExactDocument(t *testing.T) {
	t.Parallel()

	document := []byte(`{"openapi":"3.0.4","info":{"title":"Braidplane API"}}`)
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	RegisterOpenAPIRoute(app, document)

	request := httptest.NewRequestWithContext(t.Context(), "GET", "/openapi.json", nil)
	response, err := app.Test(request)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, response.Body.Close())
	})

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusOK, response.StatusCode)
	require.Equal(t, fiber.MIMEApplicationJSON, response.Header.Get(fiber.HeaderContentType))
	require.Equal(t, document, body)
}
