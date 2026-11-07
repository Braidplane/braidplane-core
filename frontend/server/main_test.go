package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"

	frontendconfig "github.com/Braidplane/braidplane-core/config/frontend"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
)

func TestAPIProxyTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		baseURL     string
		originalURL string
		want        string
	}{
		{
			name:        "preserves api path",
			baseURL:     "http://localhost:3001",
			originalURL: "/api/organizations/acme-hq/pipelines",
			want:        "http://localhost:3001/api/organizations/acme-hq/pipelines",
		},
		{
			name:        "preserves query string",
			baseURL:     "http://localhost:3001/",
			originalURL: "/api/organizations?limit=20&cursor=next-1",
			want:        "http://localhost:3001/api/organizations?limit=20&cursor=next-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := apiProxyTarget(tt.baseURL, tt.originalURL)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestProviderCallbackProxyTarget verifies split frontend parity with the hosted edge callback route.
func TestProviderCallbackProxyTarget(t *testing.T) {
	t.Parallel()

	got := providerCallbackProxyTarget(
		"http://localhost:3001/",
		"/provider-connectors/hubspot/callback?code=test-code&state=test-state",
	)
	assert.Equal(
		t,
		"http://localhost:3001/api/provider-connectors/hubspot/callback?code=test-code&state=test-state",
		got,
	)
}

func TestValidateBackendProxyTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		backendAPIBaseURL string
		frontendAddress   string
		wantErrContains   string
	}{
		{
			name:              "allows backend on different port",
			backendAPIBaseURL: "http://localhost:3001",
			frontendAddress:   "0.0.0.0:3002",
		},
		{
			name:              "rejects identical frontend port",
			backendAPIBaseURL: "http://localhost:3002",
			frontendAddress:   "0.0.0.0:3002",
			wantErrContains:   "points to the standalone frontend server",
		},
		{
			name:              "rejects loopback alias of frontend port",
			backendAPIBaseURL: "http://127.0.0.1:3002",
			frontendAddress:   "0.0.0.0:3002",
			wantErrContains:   "points to the standalone frontend server",
		},
		{
			name:              "rejects invalid backend api url",
			backendAPIBaseURL: "localhost:3002",
			frontendAddress:   "0.0.0.0:3002",
			wantErrContains:   "backend_api_url",
		},
		{
			name:              "wraps backend api url parse errors",
			backendAPIBaseURL: "http://[::1",
			frontendAddress:   "0.0.0.0:3002",
			wantErrContains:   "parse backend API URL",
		},
		{
			name:              "wraps frontend address parse errors",
			backendAPIBaseURL: "http://localhost:3001",
			frontendAddress:   "localhost",
			wantErrContains:   "parse frontend address",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateBackendProxyTarget(tt.backendAPIBaseURL, tt.frontendAddress)
			if tt.wantErrContains == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrContains)
		})
	}
}

func TestFrontendSessionConfigScopesCookieToRoot(t *testing.T) {
	t.Parallel()

	cfg := &frontendconfig.Config{
		Session: frontendconfig.SessionConfig{
			CookieSecure: true,
		},
	}

	got := frontendSessionConfig(cfg)

	assert.Equal(t, "/", got.CookiePath)
	assert.Empty(t, got.CookieDomain)
	assert.Equal(t, "Lax", got.CookieSameSite)
	assert.True(t, got.CookieHTTPOnly)
	assert.True(t, got.CookieSecure)
	assert.Equal(t, 24*time.Hour, got.IdleTimeout)
}

func TestRegisterStaticAssetRoutesServesBrandIcons(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	registerStaticAssetRoutesFromRoots(app, 0, "../../brand/icons", "../../frontend/assets")

	resp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/assets/icons/app/favicon-16.png", nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get(fiber.HeaderContentType), "image/png")
}

func TestRegisterStaticAssetRoutesDoesNotServeExcludedVendorIcons(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	registerStaticAssetRoutesFromRoots(app, 0, "../../brand/icons", "../../frontend/assets")
	for _, path := range []string{
		"/assets/icons/data-sources/vendors/brand/gitlab.svg",
		"/assets/icons/data-sources/vendors/mono/gitlab.svg",
		"/assets/icons/data-sources/vendors/brand/dbt.png",
		"/assets/icons/data-sources/vendors/mono/mongodb.svg",
	} {
		resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	}
}

func TestRegisterStaticAssetRoutesServesReviewedMarksAndAttribution(t *testing.T) {
	t.Parallel()
	app := fiber.New()
	registerStaticAssetRoutesFromRoots(app, 0, "../../brand/icons", "../../frontend/assets")
	for _, path := range []string{
		"/assets/icons/data-sources/vendors/brand/git.svg",
		"/assets/icons/data-sources/vendors/brand/postgres.svg",
		"/assets/icons/data-sources/vendors/brand/mysql.svg",
		"/assets/icons/data-sources/vendors/brand/google-drive.png",
		"/assets/icons/data-sources/vendors/brand/google-bigquery.svg",
		"/assets/icons/data-sources/vendors/brand/snowflake.svg",
		"/assets/icons/data-sources/vendors/brand/hubspot.svg",
		"/assets/icons/data-sources/vendors/brand/github.svg",
		"/assets/icons/data-sources/vendors/brand/mongodb.png",
		"/assets/icons/data-sources/licenses/vendor-NOTICE.txt",
		"/assets/icons/data-sources/licenses/git-CC-BY-3.0.txt",
	} {
		resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	}
}

func TestRegisterStaticAssetRoutesServesPublicIconAliases(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	registerStaticAssetRoutesFromRoots(app, 0, "../../brand/icons", "../../frontend/assets")

	for _, test := range []struct {
		path         string
		contentTypes []string
	}{
		{path: "/favicon.ico", contentTypes: []string{"image/x-icon", "image/vnd.microsoft.icon"}},
		{path: "/apple-touch-icon.png", contentTypes: []string{"image/png"}},
	} {
		resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, test.path, nil))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, test.contentTypes, resp.Header.Get(fiber.HeaderContentType))
		require.NoError(t, resp.Body.Close())
	}
}

func TestStaticAssetCachePolicyRequiresContentVersion(t *testing.T) {
	t.Parallel()

	assetRoot := t.TempDir()
	assetContent := []byte("body{}")
	require.NoError(t, os.WriteFile(filepath.Join(assetRoot, "app.css"), assetContent, 0o600))
	assetSum := sha256.Sum256(assetContent)
	assetVersion := hex.EncodeToString(assetSum[:])[:12]

	tests := []struct {
		name       string
		requestURL string
		maxAge     int
		wantStatus int
		wantCache  string
	}{
		{
			name:       "VersionedAssetIsImmutable",
			requestURL: "/assets/app.css?v=" + assetVersion,
			maxAge:     31536000,
			wantStatus: http.StatusOK,
			wantCache:  "public, max-age=31536000, immutable",
		},
		{
			name:       "VersionedBrandAssetIsImmutable",
			requestURL: "/assets/icons/app.css?v=" + assetVersion,
			maxAge:     31536000,
			wantStatus: http.StatusOK,
			wantCache:  "public, max-age=31536000, immutable",
		},
		{
			name:       "UnversionedAssetIsNotStored",
			requestURL: "/assets/app.css",
			maxAge:     31536000,
			wantStatus: http.StatusOK,
			wantCache:  "no-store",
		},
		{
			name:       "WellFormedMismatchedVersionIsNotStored",
			requestURL: "/assets/app.css?v=0123456789ab",
			maxAge:     31536000,
			wantStatus: http.StatusOK,
			wantCache:  "no-store",
		},
		{
			name:       "InvalidVersionIsNotStored",
			requestURL: "/assets/app.css?v=not-a-digest",
			maxAge:     31536000,
			wantStatus: http.StatusOK,
			wantCache:  "no-store",
		},
		{
			name:       "AdditionalQueryInputIsNotStored",
			requestURL: "/assets/app.css?v=0123456789ab&preview=true",
			maxAge:     31536000,
			wantStatus: http.StatusOK,
			wantCache:  "no-store",
		},
		{
			name:       "DisabledCacheIsNotStored",
			requestURL: "/assets/app.css?v=" + assetVersion,
			maxAge:     0,
			wantStatus: http.StatusOK,
			wantCache:  "no-store",
		},
		{
			name:       "MissingAssetIsNotStored",
			requestURL: "/assets/missing.css?v=0123456789ab",
			maxAge:     31536000,
			wantStatus: http.StatusNotFound,
			wantCache:  "no-store",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			app.Use("/assets", func(c fiber.Ctx) error {
				c.Cookie(&fiber.Cookie{Name: "unexpected", Value: "must-not-leak"})
				return c.Next()
			})
			registerStaticAssetRoutesFromRoots(app, tt.maxAge, assetRoot, assetRoot)

			resp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, tt.requestURL, nil))
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, resp.Body.Close())
			})

			require.Equal(t, tt.wantStatus, resp.StatusCode)
			assert.Equal(t, tt.wantCache, resp.Header.Get(fiber.HeaderCacheControl))
			assert.Empty(t, resp.Header.Values(fiber.HeaderSetCookie))
		})
	}
}

func TestAPIProxyPreservesCorrelationHeaders(t *testing.T) {
	t.Parallel()

	headers := apiProxyCorrelationHeaders()

	for _, header := range []string{
		"X-Request-ID",
		"traceparent",
		"tracestate",
		"baggage",
	} {
		assert.Truef(t, containsHeader(headers, header), "apiProxyCorrelationHeaders() missing %q", header)
	}
}

func TestInjectAPIProxyHeaders_OverwritesForwardedClientIPAndPreservesCorrelation(t *testing.T) {
	t.Parallel()

	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	member, err := baggage.NewMember("braidplane.component", "frontend")
	require.NoError(t, err)
	bag, err := baggage.New(member)
	require.NoError(t, err)

	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	}))
	ctx = baggage.ContextWithBaggage(reqctx.WithRequestID(ctx, "req-123"), bag)

	app := fiber.New()
	app.Get("/api/test", func(c fiber.Ctx) error {
		c.SetContext(ctx)
		injectAPIProxyHeaders(ctx, c)

		assert.Equal(t, "req-123", string(c.Request().Header.Peek(fiber.HeaderXRequestID)))
		assert.Equal(
			t,
			"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			string(c.Request().Header.Peek("traceparent")),
		)
		assert.Equal(t, "braidplane.component=frontend", string(c.Request().Header.Peek("baggage")))
		assert.Equal(t, "0.0.0.0", string(c.Request().Header.Peek(fiber.HeaderXForwardedFor)))

		return c.SendStatus(http.StatusNoContent)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/test", nil)
	req.Header.Set(fiber.HeaderXForwardedFor, "198.18.12.34")
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestMetricsRouteStaysPublicBeforeAuthenticatedFrontendRoutes(t *testing.T) {
	t.Parallel()

	meter, err := metrics.New(metrics.Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "frontend",
		Environment: "test",
	})
	require.NoError(t, err)

	app := fiber.New()
	meter.RegisterRoutes(app)
	app.Use(func(c fiber.Ctx) error {
		return c.Redirect().Status(http.StatusSeeOther).To("/auth/login?return_to=" + c.Path())
	})

	resp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `service_name="frontend"`)
	require.Contains(t, string(body), "braidplane_target_info")
}

func containsHeader(headers []string, want string) bool {
	for _, header := range headers {
		if strings.EqualFold(header, want) {
			return true
		}
	}
	return false
}

func TestBackendProxyRoutesAllowConfiguredPrivateBackend(t *testing.T) {
	previousPolicy := proxy.WithSecurityPolicy(proxy.DefaultSecurityPolicy())
	t.Cleanup(func() { proxy.WithSecurityPolicy(previousPolicy) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "correlated", r.Header.Get("X-Request-ID"))
		assert.Equal(t, "session=opaque", r.Header.Get("Cookie"))
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			assert.Equal(t, "body available", string(body))
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Upstream-Path", r.URL.RequestURI())
		w.Header().Set("Set-Cookie", "session=rotated; HttpOnly")
		w.Header().Set("Location", "http://untrusted.example/redirect")
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, err := io.WriteString(w, "upstream response")
		assert.NoError(t, err)
	}))
	defer upstream.Close()
	app := fiber.New()
	registerBackendProxyRoutes(app, upstream.URL)
	for _, path := range []string{"/api/action?x=1", "/provider-connectors/google/callback?code=opaque"} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader("body available"))
		if strings.HasPrefix(path, "/provider-connectors/") {
			req.Method = http.MethodGet
		}
		req.Header.Set("X-Request-ID", "correlated")
		req.Header.Set("Cookie", "session=opaque")
		response, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusTemporaryRedirect, response.StatusCode)
		assert.Equal(t, "session=rotated; HttpOnly", response.Header.Get("Set-Cookie"))
		assert.Equal(t, "http://untrusted.example/redirect", response.Header.Get("Location"))
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		assert.Equal(t, "/api"+strings.TrimPrefix(path, "/api"), response.Header.Get("X-Upstream-Path"))
		assert.Equal(t, "upstream response", string(body))
	}
}

func TestStaticMediaSupportsSeeking(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "demo.mp4"), []byte("0123456789"), 0o600))
	app := fiber.New()
	registerStaticAssetRoutesFromRoots(app, 0, root, root)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/assets/demo.mp4", nil)
	req.Header.Set("Range", "bytes=2-5")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusPartialContent, resp.StatusCode)
	assert.Equal(t, "bytes 2-5/10", resp.Header.Get("Content-Range"))
	assert.Contains(t, resp.Header.Get("Content-Type"), "video/mp4")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "2345", string(body))
}

func TestRunRequiresRoutePresentationFactory(t *testing.T) {
	t.Parallel()
	require.ErrorContains(t, Run(nil), "frontend route presentation factory is required")
}
