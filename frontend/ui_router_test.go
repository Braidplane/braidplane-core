package frontend

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/require"
)

func TestNewRouter_RequiresLogger(t *testing.T) {
	t.Parallel()

	router, err := NewRouter("http://localhost:3001", nil, session.NewStore())
	require.Error(t, err)
	require.Nil(t, router)
	require.ErrorContains(t, err, "frontend logger is required")
}

func TestMountedFrontendRoutes_DoNotInterceptAPISessions(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	rootApp := fiber.New()
	frontendApp := fiber.New()
	apiApp := fiber.New()

	router.RegisterRoutes(frontendApp, CommunityPresentation{})
	apiApp.Post("/sessions", func(c fiber.Ctx) error {
		return c.SendStatus(http.StatusUnauthorized)
	})

	rootApp.Use("/api", apiApp)
	rootApp.Use("/", frontendApp)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/sessions", nil)
	resp, err := rootApp.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Empty(t, resp.Header.Get("Location"))
}
