package frontend

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestRouter_HandleCreateRoleGrantSubmitUsesBodySubject(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/organizations/acme-hq/authorization/grants", r.URL.Path)
		var payload api.NewRoleGrant
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal(t, api.NewRoleGrantSubjectKindApiKey, payload.SubjectKind)
		assert.Equal(t, "submitted-key", payload.SubjectSlug)
		assert.Equal(t, "editor", payload.RoleSlug)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"slug":"new-grant","subject_kind":"api_key","subject_slug":"submitted-key","role_slug":"editor","scope":"organization"}`))
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := newHandlerTestRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	path := "/organizations/acme-hq/grants?subject_kind=member&subject_slug=filtered-user"
	form := strings.NewReader("subject_kind=api_key&subject_slug=submitted-key&role_slug=editor&scope=organization")
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, path, resp.Header.Get("Location"))
}
