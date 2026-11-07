package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/compress"
	"github.com/gofiber/fiber/v3/middleware/proxy"
	recovermiddleware "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/gofiber/fiber/v3/middleware/static"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/propagation"

	frontendconfig "github.com/Braidplane/braidplane-core/config/frontend"
	"github.com/Braidplane/braidplane-core/frontend"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/dbpool"
	"github.com/Braidplane/braidplane-core/pkg/lifecycle"
	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/tracing"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	sessionstore "github.com/gofiber/storage/postgres/v3"
)

func apiProxyTarget(baseURL, originalURL string) string {
	return strings.TrimRight(baseURL, "/") + originalURL
}

// providerCallbackProxyTarget maps the canonical public callback to the backend API mount.
func providerCallbackProxyTarget(baseURL, originalURL string) string {
	return strings.TrimRight(baseURL, "/") + "/api" + originalURL
}

func registerBackendProxyRoutes(app *fiber.App, backendAPIBaseURL string) {
	// Both proxy routes pin their destination to the validated operator-configured
	// backend. Private addresses are required for local and internal deployments;
	// request paths cannot select an upstream and redirects are not followed.
	policy := proxy.DefaultSecurityPolicy()
	policy.AllowPrivateIPs = true
	proxy.WithSecurityPolicy(policy)
	app.Use("/api", func(c fiber.Ctx) error {
		injectAPIProxyHeaders(c.Context(), c)
		return proxy.Do(c, apiProxyTarget(backendAPIBaseURL, c.OriginalURL()))
	})
	app.Get("/provider-connectors/:provider/callback", func(c fiber.Ctx) error {
		injectAPIProxyHeaders(c.Context(), c)
		return proxy.Do(c, providerCallbackProxyTarget(backendAPIBaseURL, c.OriginalURL()))
	})
}

func apiProxyCorrelationHeaders() []string {
	return []string{
		fiber.HeaderXRequestID,
		"traceparent",
		"tracestate",
		"baggage",
	}
}

// injectAPIProxyHeaders replaces caller-controlled client identity before proxying and preserves request correlation headers.
func injectAPIProxyHeaders(ctx context.Context, c fiber.Ctx) {
	// c.IP() has already applied Fiber's trusted-proxy policy on the frontend edge.
	c.Request().Header.Set(fiber.HeaderXForwardedFor, c.IP())

	if requestID := reqctx.GetRequestID(ctx); requestID != nil {
		c.Request().Header.Set(fiber.HeaderXRequestID, *requestID)
	}

	carrier := propagation.MapCarrier{}
	propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	).Inject(ctx, carrier)

	for _, header := range apiProxyCorrelationHeaders() {
		if value := carrier.Get(header); value != "" {
			c.Request().Header.Set(header, value)
		}
	}
}

func validateBackendProxyTarget(backendAPIBaseURL, frontendAddress string) error {
	backendHostPort, err := normalizedURLHostPort(backendAPIBaseURL)
	if err != nil {
		return xerror.ValidationFailed().Detailf(
			"backend_api_url %q is invalid: %w",
			backendAPIBaseURL,
			err,
		)
	}

	frontendHostPort, err := normalizedAddressHostPort(frontendAddress)
	if err != nil {
		return xerror.Unexpected().Detailf(
			"frontend server address %q is invalid: %w",
			frontendAddress,
			err,
		)
	}

	if backendHostPort == frontendHostPort {
		return xerror.ValidationFailed().Detailf(
			"backend_api_url %q points to the standalone frontend server at %q; set it to the backend API origin instead",
			backendAPIBaseURL,
			frontendAddress,
		)
	}

	return nil
}

func normalizedURLHostPort(rawURL string) (string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", xerror.ValidationFailed().Detailf("parse backend API URL: %w", err)
	}

	if parsedURL.Host == "" {
		return "", xerror.ValidationFailed().Detail("missing URL host")
	}

	port := parsedURL.Port()
	if port == "" {
		switch strings.ToLower(parsedURL.Scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", xerror.ValidationFailed().Detail("missing URL port")
		}
	}

	return net.JoinHostPort(normalizeLoopbackHost(parsedURL.Hostname()), port), nil
}

func normalizedAddressHostPort(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", xerror.ValidationFailed().Detailf("parse frontend address: %w", err)
	}

	return net.JoinHostPort(normalizeLoopbackHost(host), port), nil
}

func normalizeLoopbackHost(host string) string {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "", "0.0.0.0", "127.0.0.1", "::", "::1", "localhost":
		return "localhost"
	default:
		return host
	}
}

const frontendSessionExpiration = 24 * time.Hour

func frontendSessionConfig(cfg *frontendconfig.Config) session.Config {
	return session.Config{
		IdleTimeout:    frontendSessionExpiration,
		CookieSecure:   cfg.Session.CookieSecure,
		CookieHTTPOnly: true,
		CookieSameSite: "Lax",
		CookiePath:     "/",
	}
}

// Run starts the shared frontend server with the distribution's required route presentation.
func Run(newRoutePresentation func(string) (frontend.RoutePresentation, error)) error {
	if newRoutePresentation == nil {
		return xerror.Unexpected().Detail("frontend route presentation factory is required")
	}
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config.toml"
	}

	cfg, err := frontendconfig.Load(configPath)
	if err != nil {
		return xerror.Wrapf("failed to load config: %w", err)
	}

	return RunWithConfig(cfg, newRoutePresentation, func(*frontend.Router) {})
}

// RunWithConfig starts a distribution using its validated configuration and presentation policy.
func RunWithConfig(cfg *frontendconfig.Config, newRoutePresentation func(string) (frontend.RoutePresentation, error), configure func(*frontend.Router)) error {
	if cfg == nil || newRoutePresentation == nil || configure == nil {
		return xerror.Unexpected().Detail("frontend configuration and presentation are required")
	}

	frontendLogger, frontendCloser, err := logging.Setup(logging.Config{
		Level:            cfg.Logging.Level,
		FilePath:         cfg.Logging.File,
		RotationInterval: cfg.Logging.RotationInterval.TimeDuration(),
		MaxFileSizeMB:    cfg.Logging.MaxFileSizeMB,
		Environment:      cfg.Logging.Environment,
		OTLPLogsEndpoint: cfg.Logging.OTLPLogsEndpoint,
	}, "frontend")
	if err != nil {
		return xerror.Wrapf("failed to setup frontend logger: %w", err)
	}
	defer func() {
		if closeErr := frontendCloser.Close(); closeErr != nil {
			frontendLogger.Error("failed to close frontend logger", slog.Any("error", closeErr))
		}
	}()

	traceCloser, err := tracing.Setup(context.Background(), tracing.Config{
		Component:              "frontend",
		Environment:            cfg.Logging.Environment,
		OTLPTracesEndpoint:     cfg.Tracing.OTLPTracesEndpoint,
		SamplingRatio:          cfg.Tracing.SamplingRatio,
		InstallGlobalProviders: true,
	})
	if err != nil {
		return xerror.Wrapf("failed to setup frontend tracing: %w", err)
	}
	defer func() {
		if closeErr := traceCloser.Close(); closeErr != nil {
			frontendLogger.Error("failed to close frontend tracer", slog.Any("error", closeErr))
		}
	}()

	metricsMeter, err := metrics.New(metrics.Config{
		Enabled:     cfg.Metrics.Enabled,
		Path:        cfg.Metrics.Path,
		ServiceName: "frontend",
		Environment: cfg.Logging.Environment,
	})
	if err != nil {
		return xerror.Wrapf("failed to setup frontend metrics: %w", err)
	}
	restoreMetrics := metrics.SetProcessMeter(metricsMeter)
	defer restoreMetrics()

	frontendLogger.Info("configuration loaded")

	launchCtx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	pool, err := dbpool.Open(launchCtx, cfg.Database.ConnectionString())
	if err != nil {
		return xerror.Wrapf("failed to initialize database pool: %w", err)
	}
	defer pool.Close()
	metricsMeter.RegisterPostgresPool(func() metrics.PostgresPoolStats {
		return pool.Stat()
	})

	sessionStore := sessionstore.New(sessionstore.Config{DB: pool, Table: cfg.Session.TableName})
	defer func() {
		if err := sessionStore.Close(); err != nil {
			frontendLogger.Error("failed to close session store", slog.Any("error", err))
		}
	}()

	sessionConfig := frontendSessionConfig(cfg)
	sessionConfig.Storage = sessionStore
	store := session.NewStore(sessionConfig)

	backendAPIBaseURL := strings.TrimRight(cfg.BackendAPIURL, "/")
	if err := validateBackendProxyTarget(backendAPIBaseURL, cfg.Server.Address()); err != nil {
		return err
	}

	frontendRouter, err := frontend.NewRouter(
		backendAPIBaseURL,
		frontendLogger,
		store,
	)
	if err != nil {
		return xerror.Wrapf("failed to initialize frontend router: %w", err)
	}
	presentation, err := newRoutePresentation(backendAPIBaseURL)
	if err != nil {
		return xerror.Wrapf("failed to initialize frontend route presentation: %w", err)
	}
	frontendRouter.SetApplicationURL(cfg.FrontendURL)
	frontendRouter.SetDocumentationURL(cfg.DocumentationURL)
	frontendRouter.SetSelfServiceRegistrationEnabled(cfg.Auth.SelfServiceRegistrationEnabled)
	frontendRouter.SetAllowPrivateDataSourceHosts(cfg.Security.AllowPrivateDataSourceHosts)
	configure(frontendRouter)

	if cfg.Server.StaticCacheMaxAge > uint(math.MaxInt) {
		return xerror.Unexpected().Detailf("frontend static cache max age %d exceeds int range", cfg.Server.StaticCacheMaxAge)
	}

	app := fiber.New(fiber.Config{
		ErrorHandler:       apierror.Middleware(),
		ProxyHeader:        cfg.Server.ProxyHeader,
		TrustProxy:         len(cfg.Server.TrustedProxies) > 0,
		TrustProxyConfig:   fiber.TrustProxyConfig{Proxies: cfg.Server.TrustedProxies},
		EnableIPValidation: cfg.Server.ProxyHeader != "",
	})
	app.Use(
		recovermiddleware.New(recovermiddleware.Config{EnableStackTrace: true}),
		compress.New(),
		requestid.New(requestid.Config{Generator: uuid.NewString}),
	)
	app.Use(func(c fiber.Ctx) error {
		requestID := reqctx.FiberRequestID(c)
		if requestID != "" {
			c.SetContext(reqctx.WithRequestID(c.Context(), requestID))
		}

		return c.Next()
	})
	drainLifecycle := lifecycle.NewDrainLifecycle()
	backendReadinessProbe := newFrontendBackendReadinessProbe(backendAPIBaseURL)
	registerFrontendProbeRoutes(app, frontendProbeRoutesConfig{
		DatabaseCheck: pool.Ping,
		BackendCheck:  backendReadinessProbe.Check,
		Lifecycle:     drainLifecycle,
	})
	app.Use(tracing.HTTPMiddleware("frontend"))
	app.Use(metricsMeter.HTTPMiddleware())
	app.Use(logging.HTTPMiddleware(frontendLogger, cfg.Logging.LogHTTPRequests))
	registerBackendProxyRoutes(app, backendAPIBaseURL)

	registerStaticAssetRoutes(app, int(cfg.Server.StaticCacheMaxAge))
	metricsMeter.RegisterRoutes(app)
	frontendRouter.RegisterRoutes(app, presentation)

	frontendLogger.Info("starting frontend server", slog.String("address", cfg.Server.Address()))

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- app.Listen(cfg.Server.Address())
	}()

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	select {
	case err := <-serverErrors:
		return xerror.Unexpected().With(err)
	case sig := <-shutdown:
		frontendLogger.Info("shutdown signal received", slog.String("signal", sig.String()))

		if drainLifecycle.BeginDrain() {
			frontendLogger.Info(
				"readiness drain started",
				slog.Duration("duration", cfg.Server.DrainTimeout.TimeDuration()),
			)
		}
		if err := drainLifecycle.WaitDrainWindow(
			context.Background(),
			cfg.Server.DrainTimeout.TimeDuration(),
		); err != nil {
			frontendLogger.Error("readiness drain wait failed", slog.Any("error", err))
			return xerror.Unexpected().With(err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout.TimeDuration())
		defer cancel()

		if err := app.ShutdownWithContext(ctx); err != nil {
			frontendLogger.Error("graceful shutdown failed", slog.Any("error", err))
			return xerror.Unexpected().With(err)
		}

		frontendLogger.Info("server shutdown complete")
	}

	return nil
}

// registerStaticAssetRoutes serves canonical shared icons before app-only frontend assets.
func registerStaticAssetRoutes(app *fiber.App, staticCacheMaxAge int) {
	registerStaticAssetRoutesFromRoots(app, staticCacheMaxAge, "./brand/icons", "./frontend/assets")
}

// registerStaticAssetRoutesFromRoots mounts static assets from explicit roots for runtime and test use.
func registerStaticAssetRoutesFromRoots(app *fiber.App, staticCacheMaxAge int, iconRoot, frontendAssetRoot string) {
	app.Get("/favicon.ico", func(c fiber.Ctx) error {
		return c.SendFile(filepath.Join(iconRoot, "app", "favicon.ico"))
	})
	app.Get("/apple-touch-icon.png", func(c fiber.Ctx) error {
		return c.SendFile(filepath.Join(iconRoot, "app", "apple-touch-icon.png"))
	})
	app.Use("/assets", staticAssetCachePolicy(staticCacheMaxAge, iconRoot, frontendAssetRoot))
	app.Get("/assets/icons/*", static.New(iconRoot, static.Config{MaxAge: staticCacheMaxAge}))
	app.Get("/assets/*", static.New(frontendAssetRoot, static.Config{MaxAge: staticCacheMaxAge, ByteRange: true}))
}

// staticAssetCachePolicy makes only explicitly content-versioned successful assets immutable and prevents cookies from entering the CDN surface.
func staticAssetCachePolicy(staticCacheMaxAge int, iconRoot, frontendAssetRoot string) fiber.Handler {
	return func(c fiber.Ctx) error {
		err := c.Next()

		c.Response().Header.Del(fiber.HeaderSetCookie)
		cacheControl := "no-store"
		if err == nil && staticCacheMaxAge > 0 && isCacheableStaticAssetStatus(c.Response().StatusCode()) && hasMatchingStaticAssetVersion(c, iconRoot, frontendAssetRoot) {
			cacheControl = "public, max-age=" + strconv.Itoa(staticCacheMaxAge) + ", immutable"
		}
		c.Set(fiber.HeaderCacheControl, cacheControl)

		return err
	}
}

// hasMatchingStaticAssetVersion accepts only the requested file's single SHA-256 prefix emitted by the frontend asset helper.
func hasMatchingStaticAssetVersion(c fiber.Ctx, iconRoot, frontendAssetRoot string) bool {
	args := c.RequestCtx().QueryArgs()
	if args.Len() != 1 {
		return false
	}

	content, ok := readStaticAsset(c.Path(), iconRoot, frontendAssetRoot)
	if !ok {
		return false
	}

	sum := sha256.Sum256(content)
	expectedVersion := hex.EncodeToString(sum[:])[:12]
	return string(args.Peek("v")) == expectedVersion
}

// readStaticAsset reads an asset from the same confined root used by Fiber's static routes.
func readStaticAsset(requestPath, iconRoot, frontendAssetRoot string) ([]byte, bool) {
	relPath := strings.TrimPrefix(requestPath, "/assets/")
	if relPath == requestPath || relPath == "" {
		return nil, false
	}

	root := frontendAssetRoot
	if strings.HasPrefix(relPath, "icons/") {
		root = iconRoot
		relPath = strings.TrimPrefix(relPath, "icons/")
	}

	relPath = filepath.FromSlash(relPath)
	if !filepath.IsLocal(relPath) {
		return nil, false
	}

	assetRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, false
	}
	defer func() {
		_ = assetRoot.Close()
	}()

	content, err := assetRoot.ReadFile(relPath)
	return content, err == nil
}

// isCacheableStaticAssetStatus restricts immutable caching to successful file responses and conditional revalidation hits.
func isCacheableStaticAssetStatus(status int) bool {
	return status == http.StatusOK || status == http.StatusPartialContent || status == http.StatusNotModified
}
