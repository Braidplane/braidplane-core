package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/accountclosures"
	"github.com/Braidplane/braidplane-core/postgres/apikeys"
	"github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/postgres/pats"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/accountclosuresvc"
	"github.com/Braidplane/braidplane-core/service/apikeyssvc"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/patssvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
	transport "github.com/Braidplane/braidplane-core/transport/http"
	sessionstore "github.com/gofiber/storage/postgres/v3"
)

func TestAccountDeletionSessionOwnershipAndRevocation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "account deletion HTTP")
	storage := sessionstore.New(sessionstore.Config{DB: pool, Table: "user_sessions"})
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	prefix := fmt.Sprintf("account-http-%d", time.Now().UnixNano())
	alice, bob, org := prefix+"-alice", prefix+"-bob", prefix+"-org"
	for _, user := range []string{alice, bob} {
		_, err := pool.Exec(ctx, `INSERT INTO users (slug, email, full_name, role, verified_at) VALUES ($1, $2, 'Test Person', 'user', NOW())`, user, user+"@example.com")
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		for _, query := range []string{
			`DELETE FROM account_deletion_requests WHERE user_id IN (SELECT id FROM users WHERE slug IN ($1, $2))`,
		} {
			_, cleanupErr := pool.Exec(context.Background(), query, alice, bob)
			require.NoError(t, cleanupErr)
		}
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, org)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM river_job WHERE args->>'organization_slug' = $1`, org)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = $1`, org)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM users WHERE slug IN ($1, $2)`, alice, bob)
		require.NoError(t, cleanupErr)
	})
	_, err := pool.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, 'Test Organization')`, org)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO roles (organization_id, slug, name) SELECT id, 'admin', 'Admin' FROM organizations WHERE slug = $1`, org)
	require.NoError(t, err)
	addAdmin := func(user string) {
		_, err := pool.Exec(ctx, `INSERT INTO organization_members (organization_id, user_id, role_id)
			SELECT o.id, u.id, r.id FROM organizations o JOIN roles r ON r.organization_id = o.id AND r.slug = 'admin'
			CROSS JOIN users u WHERE o.slug = $1 AND u.slug = $2`, org, user)
		require.NoError(t, err)
	}
	addAdmin(alice)
	txm := postgres.NewTransactionManager(pool)
	userService := userssvc.New(txm, &testutil.FakeHasher{}, users.NewRepository(), nil, nil, "")
	auth, err := authorization.New(userService, nil)
	require.NoError(t, err)
	queueClient, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	closure, err := accountclosuresvc.New(txm, accountclosures.Repository{}, eventtest.NewProducer(eventssvc.New(txm, events.New(), nil)), queueClient)
	require.NoError(t, err)
	store := session.NewStore(session.Config{Storage: storage})
	srv := transport.NewServer(
		userService, closure, nil, store, auth,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, "", "", false, false,
		nil,
	)
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Post("/_test/login/:user", func(c fiber.Ctx) error {
		if err := reqctx.SetUserSlug(c, store, slug.Slug(c.Params("user"))); err != nil {
			return err
		}
		return c.SendStatus(fiber.StatusOK)
	})
	// Throttle behavior has hermetic coverage; do not share minute-long IP budgets with other DB tests.
	app.Use(transport.NewBackendSecurityMiddlewareForApp(store, slog.New(slog.DiscardHandler), httpsecurity.NewMemoryAbuseLimiter(), 100, time.Nanosecond, 0, 0, 0, time.Minute))
	patService := patssvc.New(txm, pats.NewRepository())
	app.Use(transport.NewBearerAuthMiddleware(apikeyssvc.New(txm, apikeys.NewRepository(), nil, nil), patService))
	api.RegisterHandlers(app, srv)
	type responseSnapshot struct {
		StatusCode int
		Header     http.Header
		Body       []byte
		cookies    []*http.Cookie
	}
	request := func(method, path, body string, cookies []*http.Cookie, csrf string) responseSnapshot {
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set(httpsecurity.CSRFHeader, csrf)
		}
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		response, err := app.Test(req)
		require.NoError(t, err)
		data, err := io.ReadAll(response.Body)
		require.NoError(t, response.Body.Close())
		require.NoError(t, err)
		return responseSnapshot{StatusCode: response.StatusCode, Header: response.Header, Body: data, cookies: response.Cookies()}
	}
	login := func(user string) ([]*http.Cookie, string) {
		response := request(http.MethodPost, "/_test/login/"+user, "", nil, "")
		require.Equal(t, http.StatusOK, response.StatusCode)
		cookies := response.cookies
		profile := request(http.MethodGet, "/users/profile", "", cookies, "")
		require.Equal(t, http.StatusOK, profile.StatusCode)
		token := profile.Header.Get(httpsecurity.CSRFHeader)
		require.NotEmpty(t, token)
		return append(cookies, profile.cookies...), token
	}
	aliceCookies, csrf := login(alice)
	bobCookies, _ := login(bob)
	require.Equal(t, http.StatusNotFound, request(http.MethodPost, "/users/profile/exports", `{}`, aliceCookies, csrf).StatusCode)
	const body = `{"confirmation":"DELETE MY ACCOUNT"}`
	denied := request(http.MethodPost, "/users/profile/deletion", body, aliceCookies, "")
	require.Equal(t, http.StatusForbidden, denied.StatusCode)
	blocked := request(http.MethodPost, "/users/profile/deletion", body, aliceCookies, csrf)
	require.Equal(t, http.StatusConflict, blocked.StatusCode)
	var pending api.AccountDeletionRequest
	require.NoError(t, json.Unmarshal(blocked.Body, &pending))
	require.Equal(t, "ownership_required", string(pending.State))
	require.Equal(t, []api.Slug{org}, pending.OrganizationRemovalRequired)
	require.Empty(t, pending.OwnershipRequired)
	require.Nil(t, pending.CompletedAt)
	addAdmin(bob)
	accepted := request(http.MethodPost, "/users/profile/deletion", body, aliceCookies, csrf)
	require.Equal(t, http.StatusOK, accepted.StatusCode)
	require.Equal(t, "no-store", accepted.Header.Get("Cache-Control"))
	var result api.AccountDeletionRequest
	require.NoError(t, json.Unmarshal(accepted.Body, &result))
	require.Equal(t, "completed", string(result.State))
	require.NotNil(t, result.CompletedAt)
	require.Empty(t, result.OwnershipRequired)
	require.Equal(t, http.StatusForbidden, request(http.MethodGet, "/users/profile", "", aliceCookies, "").StatusCode)
	require.Equal(t, http.StatusForbidden, request(http.MethodPost, "/users/profile/deletion", body, aliceCookies, csrf).StatusCode)
	require.Equal(t, http.StatusOK, request(http.MethodGet, "/users/profile", "", bobCookies, "").StatusCode)
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM account_deletion_requests WHERE slug = $1`, result.Slug)
		require.NoError(t, cleanupErr)
	})
	var bobActive bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT deleted_at IS NULL FROM users WHERE slug = $1`, bob).Scan(&bobActive))
	require.True(t, bobActive)
}
