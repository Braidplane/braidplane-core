package httpsecurity

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/extractors"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextHelpersAndSafeMethod(t *testing.T) {
	t.Parallel()

	var nilCtx context.Context
	assert.Nil(t, WithCSRFToken(nilCtx, ""))
	assert.Empty(t, CSRFTokenFromContext(nilCtx))

	ctx := WithCSRFToken(context.Background(), "token-123")
	assert.Equal(t, "token-123", CSRFTokenFromContext(ctx))
	assert.Empty(t, CSRFTokenFromContext(WithCSRFToken(context.Background(), "")))

	assert.True(t, SafeMethod(http.MethodGet))
	assert.True(t, SafeMethod(" head "))
	assert.True(t, SafeMethod(http.MethodOptions))
	assert.True(t, SafeMethod(http.MethodTrace))
	assert.False(t, SafeMethod(http.MethodPost))
}

func TestEnsureExistingAndValidateCSRFToken(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	app := fiber.New()

	app.Get("/ensure", func(c fiber.Ctx) error {
		token, err := EnsureCSRFToken(c, store)
		if err != nil {
			return err
		}
		return c.SendString(token)
	})
	app.Get("/existing", func(c fiber.Ctx) error {
		token, err := ExistingCSRFToken(c, store)
		if err != nil {
			return err
		}
		return c.SendString(token)
	})
	app.Post("/validate", func(c fiber.Ctx) error {
		valid, err := ValidateCSRFToken(c, store)
		if err != nil {
			return err
		}
		if valid {
			return c.SendString("true")
		}
		return c.SendString("false")
	})

	token, sessionCookie := doRequestBodyAndCookie(
		t,
		app,
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ensure", nil),
	)
	require.Len(t, token, 64)

	assert.Equal(t, token, doRequestBody(t, app, requestWithCookie(http.MethodGet, "/existing", sessionCookie)))

	assert.Empty(t, doRequestBody(t, app, requestWithCookie(http.MethodGet, "/existing", "")))

	validateReq := requestWithCookie(http.MethodPost, "/validate", sessionCookie)
	validateReq.Header.Set(CSRFHeader, token)
	assert.Equal(t, "true", doRequestBody(t, app, validateReq))

	validateReq = requestWithCookie(http.MethodPost, "/validate", sessionCookie)
	validateReq.Header.Set(CSRFHeader, "wrong-token")
	assert.Equal(t, "false", doRequestBody(t, app, validateReq))

	validateReq = requestWithCookie(http.MethodPost, "/validate", sessionCookie)
	assert.Equal(t, "false", doRequestBody(t, app, validateReq))
}

func TestHasSessionLookup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		store      *session.Store
		req        *http.Request
		wantResult string
	}{
		{
			name:       "cookie lookup present",
			store:      session.NewStore(),
			req:        requestWithCookie(http.MethodGet, "/", "session_id=test-session"),
			wantResult: "true",
		},
		{
			name:       "header lookup present",
			store:      session.NewStore(session.Config{Extractor: extractors.FromHeader("X-Session-ID")}),
			req:        requestWithHeader(http.MethodGet, "/", "X-Session-ID", "header-session"),
			wantResult: "true",
		},
		{
			name:       "query lookup present",
			store:      session.NewStore(session.Config{Extractor: extractors.FromQuery("session_id")}),
			req:        httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?session_id=query-session", nil),
			wantResult: "true",
		},
		{
			name: "missing extractor",
			store: func() *session.Store {
				store := session.NewStore()
				store.Extractor = extractors.Extractor{}
				return store
			}(),
			req:        httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil),
			wantResult: "false",
		},
		{
			name:       "missing value",
			store:      session.NewStore(session.Config{Extractor: extractors.FromHeader("X-Session-ID")}),
			req:        httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil),
			wantResult: "false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			app.Get("/", func(c fiber.Ctx) error {
				if HasSessionLookup(c, tt.store) {
					return c.SendString("true")
				}
				return c.SendString("false")
			})

			assert.Equal(t, tt.wantResult, doRequestBody(t, app, tt.req))
		})
	}
}

func doRequest(t *testing.T, app *fiber.App, req *http.Request) *http.Response {
	t.Helper()

	resp, err := app.Test(req)
	require.NoError(t, err)
	return resp
}

func responseBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

func doRequestBody(t *testing.T, app *fiber.App, req *http.Request) string {
	t.Helper()

	resp := doRequest(t, app, req)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	return responseBody(t, resp)
}

func doRequestBodyAndCookie(t *testing.T, app *fiber.App, req *http.Request) (string, string) {
	t.Helper()

	resp := doRequest(t, app, req)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	return responseBody(t, resp), sessionCookieHeader(t, resp)
}

func sessionCookieHeader(t *testing.T, resp *http.Response) string {
	t.Helper()

	cookies := resp.Cookies()
	require.NotEmpty(t, cookies)
	return cookies[0].Name + "=" + cookies[0].Value
}

func requestWithCookie(method, path, cookie string) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	return req
}

func requestWithHeader(method, path, name, value string) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	req.Header.Set(name, value)
	return req
}
