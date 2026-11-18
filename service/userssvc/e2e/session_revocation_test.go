package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	sessionstorage "github.com/gofiber/storage/postgres/v3"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/abusecontrols"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/userssvc"
	transport "github.com/Braidplane/braidplane-core/transport/http"
)

type loginTestLimiter struct {
	db   postgres.DBTX
	repo abusecontrols.Repository
}

func (l loginTestLimiter) Attempt(ctx context.Context, budget httpsecurity.AbuseLimit) (httpsecurity.AbuseDecision, error) {
	return l.repo.Attempt(ctx, l.db, budget)
}

func TestPasswordSignInLimitsAreSharedByClientAcrossReplicas(t *testing.T) {
	ctx := t.Context()
	pool := e2edb.OpenIsolatedPostgresPool(t, "shared password sign-in limits")
	repo := users.NewRepository()
	hasher := crypto.NewArgon2()
	const (
		email    = "login-fixture@example.test"
		password = "Synthetic valid password for fixture"
	)
	subject := slug.Slug("login-fixture")
	hash, err := hasher.Hash(password)
	require.NoError(t, err)
	_, err = repo.CreateUser(ctx, pool, &users.CreateParams{
		Slug: subject, Email: email, FullName: "Sign-in fixture", Password: hash, Role: model.RoleUser, Locale: "en",
	})
	require.NoError(t, err)
	require.NoError(t, repo.MarkEmailAsVerified(ctx, pool, subject, time.Now()))
	mail := &testutil.FakeEmailProducer{}
	svc := userssvc.New(postgres.NewTransactionManager(pool), hasher, repo, mail, nil, "http://example.test",
		userssvc.WithMFAEmailBudget(abusecontrols.New(), 2, time.Minute))
	auth, err := authorization.New(svc, nil)
	require.NoError(t, err)

	replica := func() *fiber.App {
		store := session.NewStore()
		server := transport.NewServer(svc, nil, nil, store, auth, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "http://example.test", "", false, false, nil)
		app := fiber.New(fiber.Config{
			ErrorHandler: apierror.Middleware(),
			ProxyHeader:  fiber.HeaderXForwardedFor, TrustProxy: true, EnableIPValidation: true,
			TrustProxyConfig: fiber.TrustProxyConfig{Proxies: []string{"0.0.0.0"}},
		})
		limiter := loginTestLimiter{db: pool, repo: abusecontrols.New()}
		app.Use(transport.NewBackendSecurityMiddlewareForApp(store, slog.New(slog.DiscardHandler), limiter, 2, time.Minute, 20, 40, 10, time.Minute))
		app.Post("/api/sessions", server.CreateSession)
		return app
	}
	signIn := func(app *fiber.App, client, account, secret string, want int) {
		t.Helper()
		body := fmt.Sprintf(`{"email":%q,"password":%q}`, account, secret)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/sessions", strings.NewReader(body))
		req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		req.Header.Set(fiber.HeaderXForwardedFor, client)
		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { require.NoError(t, resp.Body.Close()) }()
		require.Equal(t, want, resp.StatusCode)
		switch want {
		case http.StatusOK:
			require.NotEmpty(t, resp.Cookies())
		case http.StatusAccepted:
			require.Empty(t, resp.Cookies())
		case http.StatusTooManyRequests:
			require.NotEmpty(t, resp.Header.Get(fiber.HeaderRetryAfter))
			require.Empty(t, resp.Cookies())
		}
	}
	first, second := replica(), replica()
	signIn(first, "192.0.2.1", email, "Wrong fixture password", http.StatusUnauthorized)
	signIn(second, "192.0.2.1", email, "Wrong fixture password", http.StatusUnauthorized)
	signIn(first, "192.0.2.1", "another-fixture@example.test", "Wrong fixture password", http.StatusTooManyRequests)
	signIn(second, "192.0.2.2", email, password, http.StatusOK)

	_, err = pool.Exec(ctx, "UPDATE users SET mfa_email_enabled=true WHERE slug=$1", subject)
	require.NoError(t, err)
	signIn(first, "192.0.2.3", email, "Wrong fixture password", http.StatusUnauthorized)
	signIn(first, "192.0.2.3", email, password, http.StatusAccepted)
	signIn(second, "192.0.2.4", email, password, http.StatusAccepted)
	signIn(second, "192.0.2.5", email, password, http.StatusTooManyRequests)
	require.Equal(t, 2, mail.EnqueueSessionMFACalls)
}

func TestEmailVerificationCodeExpiresAndIsConsumedOnce(t *testing.T) {
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "email verification code lifecycle")
	repo := users.NewRepository()
	subject := slug.Slug(fmt.Sprintf("verify-once-%d", time.Now().UnixNano()))
	_, err := repo.CreateUser(ctx, pool, &users.CreateParams{
		Slug: subject, Email: subject.String() + "@example.test", FullName: "Verification Fixture", Role: model.RoleUser, Locale: "en",
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "DELETE FROM users WHERE slug=$1", subject)
		require.NoError(t, err)
	})
	const code = "synthetic-email-verification-code"
	hash := sha256.Sum256([]byte(code))
	var databaseNow time.Time
	require.NoError(t, pool.QueryRow(ctx, "SELECT NOW()::timestamp").Scan(&databaseNow))
	params := users.CreateEmailVerificationTokenParams{
		Slug: subject, EmailVerificationTokenHash: hex.EncodeToString(hash[:]),
		EmailVerificationTokenExpiresAt: databaseNow.Add(-time.Minute),
	}
	svc := userssvc.New(postgres.NewTransactionManager(pool), nil, repo, nil, nil, "http://example.test")
	require.NoError(t, repo.CreateEmailVerificationToken(ctx, pool, params))
	require.ErrorIs(t, svc.VerifyEmail(ctx, code), userssvc.ErrInvalidToken)
	params.EmailVerificationTokenExpiresAt = databaseNow.Add(time.Minute)
	require.NoError(t, repo.CreateEmailVerificationToken(ctx, pool, params))
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- svc.VerifyEmail(ctx, code)
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first != nil {
		first, second = second, first
	}
	require.NoError(t, first)
	require.ErrorIs(t, second, userssvc.ErrInvalidToken)
}

func TestCredentialRecoveryRevokesStolenSessionsAcrossReplicas(t *testing.T) {
	for _, mutation := range []string{"reset", "change"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			pool := e2edb.OpenPostgresPool(t, "cross-replica credential recovery")
			repo := users.NewRepository()
			hasher := crypto.NewArgon2()
			// Existing accounts can still sign in and replace a password on the blocklist.
			password, err := hasher.Hash("password")
			require.NoError(t, err)
			subject := slug.Slug(fmt.Sprintf("revoke-%s-%d", mutation, time.Now().UnixNano()))
			email := subject.String() + "@example.com"
			_, err = repo.CreateUser(ctx, pool, &users.CreateParams{Slug: subject, Email: email, FullName: "Recovery Test", Password: password, Role: model.RoleUser, Locale: "en"})
			require.NoError(t, err)
			require.NoError(t, repo.MarkEmailAsVerified(ctx, pool, subject, time.Now()))
			t.Cleanup(func() { _, err := pool.Exec(ctx, "DELETE FROM users WHERE slug=$1", subject); require.NoError(t, err) })
			svc := userssvc.New(postgres.NewTransactionManager(pool), hasher, repo, nil, nil, "http://example.test")
			auth, err := authorization.New(svc, nil)
			require.NoError(t, err)
			replica := func() *fiber.App {
				store := session.NewStore(session.Config{Storage: sessionstorage.New(sessionstorage.Config{DB: pool, Table: "user_sessions"})})
				server := transport.NewServer(svc, nil, nil, store, auth, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "http://example.test", "", false, false, nil)
				app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
				app.Use(transport.NewSessionAuthenticationMiddleware(svc, store))
				app.Post("/sessions", server.CreateSession)
				app.Post("/sessions/mfa/verify", server.VerifySessionMFA)
				app.Post("/auth/password/reset", server.ResetPassword)
				app.Put("/users/profile/password", server.UpdateUserPassword)
				app.Patch("/users/profile", server.PatchUsersProfile)
				app.Get("/users/profile", server.GetUsersProfile)
				return app
			}
			a, b := replica(), replica()
			request := func(app *fiber.App, method, path, body string, cookie *http.Cookie, want int) []*http.Cookie {
				req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				if cookie != nil {
					req.AddCookie(cookie)
				}
				resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
				require.NoError(t, err)
				require.Equal(t, want, resp.StatusCode)
				defer func() { require.NoError(t, resp.Body.Close()) }()
				return resp.Cookies()
			}
			login := func(app *fiber.App) *http.Cookie {
				resp := request(app, http.MethodPost, "/sessions", fmt.Sprintf(`{"email":%q,"password":"password"}`, email), nil, 200)
				require.NotEmpty(t, resp)
				cookie := resp[0]
				t.Cleanup(func() {
					_, err := pool.Exec(ctx, "DELETE FROM user_sessions WHERE k=$1", cookie.Value)
					require.NoError(t, err)
				})
				return cookie
			}
			current, stolen := login(a), login(b)
			request(b, http.MethodGet, "/users/profile", "", stolen, 200)
			request(a, http.MethodPatch, "/users/profile", `{"mfa_email_enabled":true}`, current, 200)
			codeHash, err := hasher.Hash("123456")
			require.NoError(t, err)
			challenge := sha256.Sum256([]byte("pending-" + subject.String()))
			require.NoError(t, repo.CreateMFAChallenge(ctx, pool, subject, hex.EncodeToString(challenge[:]), codeHash, time.Now().Add(time.Minute)))
			if mutation == "reset" {
				token := "reset-" + subject.String()
				digest := sha256.Sum256([]byte(token))
				require.NoError(t, repo.CreatePasswordResetToken(ctx, pool, users.CreatePasswordResetTokenParams{Slug: subject, PasswordResetTokenHash: hex.EncodeToString(digest[:]), PasswordResetTokenExpiresAt: time.Now().UTC().Add(-time.Hour)}))
				request(a, http.MethodPost, "/auth/password/reset", fmt.Sprintf(`{"token":%q,"password":"replacement-password"}`, token), nil, 400)
				require.NoError(t, repo.CreatePasswordResetToken(ctx, pool, users.CreatePasswordResetTokenParams{Slug: subject, PasswordResetTokenHash: hex.EncodeToString(digest[:]), PasswordResetTokenExpiresAt: time.Now().Add(time.Minute)}))
				request(a, http.MethodPost, "/auth/password/reset", fmt.Sprintf(`{"token":%q,"password":"password123"}`, token), nil, 400)
				request(b, http.MethodGet, "/users/profile", "", stolen, 200)
				request(a, http.MethodPost, "/auth/password/reset", fmt.Sprintf(`{"token":%q,"password":"replacement-password"}`, token), nil, 204)
				request(b, http.MethodPost, "/auth/password/reset", fmt.Sprintf(`{"token":%q,"password":"another-password"}`, token), nil, 400)
			} else {
				request(a, http.MethodPut, "/users/profile/password", `{"current_password":"password","new_password":"password123"}`, current, 400)
				request(b, http.MethodGet, "/users/profile", "", stolen, 200)
				request(a, http.MethodPut, "/users/profile/password", `{"current_password":"password","new_password":"replacement-password"}`, current, 204)
			}
			for _, cookie := range []*http.Cookie{current, stolen} {
				request(b, http.MethodGet, "/users/profile", "", cookie, 401)
				request(a, http.MethodPatch, "/users/profile", `{"mfa_email_enabled":false}`, cookie, 401)
			}
			request(b, http.MethodPost, "/sessions/mfa/verify", fmt.Sprintf(`{"challenge_token":%q,"code":"123456"}`, "pending-"+subject.String()), nil, 400)
			request(b, http.MethodPost, "/sessions", fmt.Sprintf(`{"email":%q,"password":"password"}`, email), nil, 401)
			user, err := repo.FindBySlug(ctx, pool, subject)
			require.NoError(t, err)
			require.EqualValues(t, 2, user.AuthenticationVersion)
			require.True(t, user.MFAEmailEnabled)
			local, err := repo.FindBySlugWithPassword(ctx, pool, subject)
			require.NoError(t, err)
			matches, err := hasher.CompareHash("replacement-password", local.Password)
			require.NoError(t, err)
			require.True(t, matches)
		})
	}
}
