//go:build community

package frontend

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/require"
)

func TestCommunityPublicRoutes(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"google_sign_in_enabled":false}`))
	}))
	t.Cleanup(backend.Close)
	router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/", http.StatusSeeOther},
		{"/auth/login", http.StatusOK},
		{"/pricing", http.StatusNotFound},
		{"/privacy", http.StatusNotFound},
		{"/solutions", http.StatusNotFound},
		{"/solutions/settlement-review", http.StatusNotFound},
		{"/robots.txt", http.StatusNotFound},
		{"/sitemap.xml", http.StatusNotFound},
	} {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil))
			require.NoError(t, err)
			require.Equal(t, tc.status, resp.StatusCode)
			if tc.path == "/" {
				require.Equal(t, "/auth/login", resp.Header.Get("Location"))
			}
			require.NoError(t, resp.Body.Close())
		})
	}
}

func TestCommunityOmitsSettlementReviewExercise(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"google_sign_in_enabled":false}`))
	}))
	t.Cleanup(backend.Close)
	router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})
	token, cookies := frontendCSRFToken(t, app, "/auth/login")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		form := url.Values{"_csrf": {token}}
		req := httptest.NewRequestWithContext(t.Context(), method, "/orgs/acme/projects/demo/settlement-review", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	}
}
