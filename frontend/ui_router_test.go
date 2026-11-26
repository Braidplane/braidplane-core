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

	router, err := newHandlerTestRouter("http://localhost:3001", nil, session.NewStore())
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

	router, err := newHandlerTestRouter(
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
	require.Empty(t, resp.Header.Get(fiber.HeaderContentSecurityPolicy))
}

func TestCommunityHeadRequiresItsOwnMapping(t *testing.T) {
	app := fiber.New()
	handler := func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) }
	app.Get("/host-health", handler)
	finish := checkCommunityRoutes(app)
	app.Head("/host-health", handler)
	require.PanicsWithValue(t, "missing authorization classification for HEAD /host-health", finish)
}

func TestCommunityRoutesRequireAnAuthorizationMapping(t *testing.T) {
	app := fiber.New()
	app.Head("/host-health", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) })
	finish := checkCommunityRoutes(app)
	require.NotPanics(t, finish)
	app.Get("/future-action", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) })
	require.PanicsWithValue(t, "missing authorization classification for GET /future-action", finish)
}
