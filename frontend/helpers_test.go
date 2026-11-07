package frontend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

var csrfTokenTestMu sync.Mutex

func registerTestSessionRoute(app *fiber.App, store *session.Store) {
	app.Get("/_test/session", func(c fiber.Ctx) error {
		testSlug, err := slug.New("test-user")
		if err != nil {
			return err
		}
		return reqctx.SetUserSlug(c, store, testSlug)
	})

	app.Get("/_test/session/active-org/:slug", func(c fiber.Ctx) error {
		orgSlug, err := slug.New(c.Params("slug"))
		if err != nil {
			return err
		}

		return reqctx.SetActiveOrganizationSlug(c, store, orgSlug)
	})

	app.Get("/_test/session/active-org", func(c fiber.Ctx) error {
		return c.SendString(reqctx.GetActiveOrganizationSlug(c, store).String())
	})

	app.Get("/_test/session/user", func(c fiber.Ctx) error {
		return c.SendString(reqctx.GetUserSlug(c, store).String())
	})
}

func getAuthenticatedSessionCookie(t *testing.T, app *fiber.App) string {
	t.Helper()

	seedReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_test/session", nil)
	seedResp, err := app.Test(seedReq)
	require.NoError(t, err)
	defer func() { _ = seedResp.Body.Close() }()

	require.Equal(t, http.StatusOK, seedResp.StatusCode)
	cookies := seedResp.Header.Values("Set-Cookie")
	require.NotEmpty(t, cookies)

	return strings.SplitN(cookies[0], ";", 2)[0]
}

func addCSRFToken(t *testing.T, app *fiber.App, req *http.Request) {
	t.Helper()

	csrfTokenTestMu.Lock()
	defer csrfTokenTestMu.Unlock()

	tokenReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/auth/error", nil)
	if cookie := req.Header.Get("Cookie"); cookie != "" {
		tokenReq.Header.Set("Cookie", cookie)
	}
	tokenResp, err := app.Test(tokenReq)
	require.NoError(t, err)
	defer func() { _ = tokenResp.Body.Close() }()

	body, err := io.ReadAll(tokenResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, tokenResp.StatusCode)

	meta := regexp.MustCompile(`name="csrf-token"\s+content="([^"]+)"`).FindStringSubmatch(string(body))
	require.Len(t, meta, 2, "expected rendered page to contain a CSRF token")
	req.Header.Set("X-CSRF-Token", meta[1])
	cookies := tokenResp.Header.Values("Set-Cookie")
	if len(cookies) == 0 {
		return
	}

	cookieParts := make([]string, 0, len(cookies)+1)
	if existing := strings.TrimSpace(req.Header.Get("Cookie")); existing != "" {
		cookieParts = append(cookieParts, existing)
	}
	for _, cookie := range cookies {
		cookieParts = append(cookieParts, strings.SplitN(cookie, ";", 2)[0])
	}
	req.Header.Set("Cookie", strings.Join(cookieParts, "; "))
}

func getActiveOrganizationSlugFromSession(t *testing.T, app *fiber.App, authCookie string) string {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_test/session/active-org", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	return strings.TrimSpace(string(body))
}

func setActiveOrganizationSlugInSession(t *testing.T, app *fiber.App, authCookie, orgSlug string) {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_test/session/active-org/"+orgSlug, nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func getUserSlugFromSession(t *testing.T, app *fiber.App, authCookie string) string {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_test/session/user", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	return strings.TrimSpace(string(body))
}

func handleTestAdminRolePermissions(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || r.URL.Path != "/api/organizations/acme-hq/roles/admin" {
		return false
	}

	writeJSON(
		w,
		`{"slug":"admin","name":"Admin","permissions":[{"resource":"*","action":"*"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
	)
	return true
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}
