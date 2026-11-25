package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
)

func TestUnmappedCoreOperationCannotReachHandler(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Get("/future-action", func(c fiber.Ctx) error {
		return RequireMappedOperation(c, func(fiber.Ctx) error { t.Error("unmapped action reached handler"); return nil })
	})
	response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/future-action", nil))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusForbidden, response.StatusCode)
}
