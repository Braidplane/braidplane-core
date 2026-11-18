package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
)

var (
	authInspectionEnabled          bool
	authInspectionChallengeExpired bool
	googleSignupInspectionEnabled  bool
)

func registerAuthInspectionFixture(app *fiber.App, store *session.Store) {
	app.Post("/_test/auth/mfa", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		authInspectionEnabled = true
		return c.SendStatus(http.StatusNoContent)
	})
	app.Post("/_test/auth/mfa/expire", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		authInspectionChallengeExpired = true
		return c.SendStatus(http.StatusNoContent)
	})
	app.Post("/_test/auth/google-signup", func(c fiber.Ctx) error {
		sess, err := store.Get(c)
		if err != nil {
			return xerror.Wrapf("get synthetic Google signup session: %w", err)
		}
		defer sess.Release()
		expires := time.Now().Add(10 * time.Minute)
		if c.Query("expired") == "true" {
			expires = time.Now().Add(-time.Minute)
		}
		sess.Set(reqctx.SessionGoogleSignupKey, "synthetic-verified-identity")
		sess.Set(reqctx.SessionGoogleSignupExpiresKey, expires.Unix())
		if err := sess.Save(); err != nil {
			return xerror.Wrapf("save synthetic Google signup session: %w", err)
		}
		mu.Lock()
		googleSignupInspectionEnabled = true
		mu.Unlock()
		return c.SendStatus(http.StatusNoContent)
	})
}

func handleAuthInspectionAPI(w http.ResponseWriter, r *http.Request) bool {
	if googleSignupInspectionEnabled && r.URL.Path == "/api/auth/methods" && r.Method == http.MethodGet {
		_, _ = w.Write([]byte(`{"google_sign_in_enabled":true}`))
		return true
	}
	if !authInspectionEnabled || (r.URL.Path != "/api/sessions" && r.URL.Path != "/api/sessions/mfa/verify") {
		return false
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return true
	}
	if r.URL.Path == "/api/sessions" {
		authInspectionChallengeExpired = false
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"challenge_token":"synthetic-challenge","expires_at":"2099-01-01T00:00:00Z"}`))
		return true
	}
	var input struct {
		ChallengeToken string `json:"challenge_token"`
		Code           string `json:"code"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&input) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return true
	}
	if authInspectionChallengeExpired {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"INVALID_TOKEN","message":"invalid token"}}`))
		return true
	}
	if input.ChallengeToken != "synthetic-challenge" || input.Code != "123456" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"INVALID_MFA_CODE","message":"Invalid or expired code."}}`))
		return true
	}
	_, _ = w.Write([]byte(`{"authentication_version":1,"slug":"test-user","full_name":"Synthetic MFA User","email":"mfa@example.test","role":"user","locale":"en-US","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`))
	return true
}
