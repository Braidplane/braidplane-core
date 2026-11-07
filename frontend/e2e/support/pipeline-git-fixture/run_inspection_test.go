package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

func TestRunInspectionStatusOutlivesRequest(t *testing.T) {
	previousStatus, previousEnabled, previousImported, previousLarge := runInspectionStatus, runInspectionEnabled, imported, runInspectionLarge
	t.Cleanup(func() {
		runInspectionStatus, runInspectionEnabled, imported, runInspectionLarge = previousStatus, previousEnabled, previousImported, previousLarge
	})
	for _, status := range []string{"succeeded", "failed"} {
		t.Run(status, func(t *testing.T) {
			app := fiber.New()
			app.Use(func(c fiber.Ctx) error {
				err := c.Next()
				// Simulate reuse of Fiber's request path buffer after the handler returns.
				c.Path(strings.Repeat("x", len(c.Path())))
				return err
			})
			registerRunInspectionFixture(app)
			response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/_test/run/"+status, nil))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, status, runInspectionStatus)
		})
	}
}
