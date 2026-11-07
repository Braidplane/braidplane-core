package frontend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestAccountDeletionSubmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		confirmation string
		apiStatus    int
		apiBody      string
		status       int
		message      string
	}{
		{"Removed", "DELETE+MY+ACCOUNT", 200, `{"slug":"delete-test","state":"completed","requested_at":"2026-09-21T12:00:00Z","completed_at":"2026-09-21T12:00:01Z","ownership_required":[]}`, 200, "Account deleted"},
		{"Pending", "DELETE+MY+ACCOUNT", 202, `{"slug":"delete-test","state":"pending","requested_at":"2026-09-21T12:00:00Z","ownership_required":[],"organization_removal_required":[]}`, 200, "Account deletion pending"},
		{"LastOwner", "DELETE+MY+ACCOUNT", 409, `{"slug":"delete-test","state":"ownership_required","requested_at":"2026-09-21T12:00:00Z","ownership_required":["acme"]}`, 409, "Transfer ownership first"},
		{"SoleMember", "DELETE+MY+ACCOUNT", 409, `{"slug":"delete-test","state":"ownership_required","requested_at":"2026-09-21T12:00:00Z","ownership_required":[],"organization_removal_required":["solo"]}`, 409, "Delete your sole-member organizations first"},
		{"Failed", "DELETE+MY+ACCOUNT", 500, `{}`, 500, "Account removal not confirmed"},
		{"IncompleteReceipt", "DELETE+MY+ACCOUNT", 202, `{"slug":"delete-test","state":"requested","requested_at":"2026-09-21T12:00:00Z","ownership_required":[]}`, 502, "Account removal not confirmed"},
		{"ConfirmationMissing", "", 0, "", 400, "Type DELETE MY ACCOUNT"},
		{"CSRFMissing", "DELETE+MY+ACCOUNT", 0, "", 403, "Security check failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, "/api/users/profile/deletion", r.URL.Path, "no profile reload is allowed after revocation")
				assert.Equal(t, http.MethodPost, r.Method)
				assert.NotEmpty(t, r.Header.Get("Cookie"))
				assert.NotEmpty(t, r.Header.Get("X-CSRF-Token"))
				var body api.AccountDeletionConfirmation
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				assert.Equal(t, api.DELETEMYACCOUNT, body.Confirmation)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.apiStatus)
				_, err := io.WriteString(w, tc.apiBody)
				assert.NoError(t, err)
			}))
			defer backend.Close()
			store := session.NewStore()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			cookie := getAuthenticatedSessionCookie(t, app)
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/deletion", strings.NewReader("confirmation="+tc.confirmation))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Cookie", cookie)
			if tc.name != "CSRFMissing" {
				addCSRFToken(t, app, req)
			}
			response, err := app.Test(req)
			require.NoError(t, err)
			defer func() { require.NoError(t, response.Body.Close()) }()
			require.Equal(t, tc.status, response.StatusCode)
			if tc.name != "CSRFMissing" {
				require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
			}
			html, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Contains(t, string(html), tc.message)
			if tc.apiStatus == 0 {
				require.Zero(t, calls.Load())
			} else {
				require.Equal(t, int32(1), calls.Load())
			}
			if tc.name == "Removed" {
				require.Contains(t, string(html), "delete-test")
				require.Contains(t, string(html), "private account data have been deleted")
				require.NotContains(t, string(html), `id="shell-layout"`)
				followup := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/account", nil)
				followup.Header.Set("Cookie", cookie)
				loggedOut, err := app.Test(followup)
				require.NoError(t, err)
				defer func() { require.NoError(t, loggedOut.Body.Close()) }()
				require.Equal(t, http.StatusSeeOther, loggedOut.StatusCode)
				require.Contains(t, loggedOut.Header.Get("Location"), "/auth/login")
				require.Equal(t, int32(1), calls.Load())
			}
			if tc.name == "LastOwner" {
				require.Contains(t, string(html), "acme")
				require.Contains(t, string(html), "Your account is still active")
			}
		})
	}
}
