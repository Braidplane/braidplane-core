package google_signup_fixture

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/proxy"
	"github.com/gofiber/fiber/v3/middleware/session"
	sessionstore "github.com/gofiber/storage/postgres/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/frontend"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/testutil/containerfixture"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/oidc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
	transporthttp "github.com/Braidplane/braidplane-core/transport/http"
)

const clientID = "google-signup-browser"

const fixtureShutdownTimeout = 5 * time.Second

func TestMain(m *testing.M) {
	if os.Getenv("BRAIDPLANE_GOOGLE_SIGNUP_FIXTURE") != "1" {
		os.Exit(m.Run())
	}
	os.Exit(containerfixture.Run(m, "database"))
}

// TestFixtureServer is intentionally a test binary: containerfixture owns its disposable
// PostgreSQL instance and the fixture is never part of a production executable.
func TestFixtureServer(t *testing.T) {
	if os.Getenv("BRAIDPLANE_GOOGLE_SIGNUP_FIXTURE") != "1" {
		return
	}
	pool := e2edb.OpenPostgresPool(t, "Google signup browser fixture")
	ctx := t.Context()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(signals)
	issuer, closeIssuer := newIssuer(t)
	defer closeIssuer()
	appListener := listen(t, "127.0.0.1:0")
	appURL := "http://" + appListener.Addr().String()
	callbackURL := appURL + "/api/auth/google/callback"
	oidcService, err := oidc.New(issuer.url, clientID, "fixture-secret", callbackURL)
	if err != nil {
		t.Fatal(err)
	}

	storage := sessionstore.New(sessionstore.Config{DB: pool, Table: "user_sessions"})
	defer func() { _ = storage.Close() }()
	store := session.NewStore(session.Config{Storage: storage, IdleTimeout: 24 * time.Hour, CookieHTTPOnly: true, CookieSameSite: "Lax", CookiePath: "/"})
	queueClient, err := queue.NewEnqueueOnly(pool)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := crypto.NewAES256([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	emails, err := workerproducer.NewEmailProducer(queue.NewEnqueuer(queueClient), cipher)
	if err != nil {
		t.Fatal(err)
	}
	usersService := userssvc.New(postgres.NewTransactionManager(pool), crypto.NewArgon2WithParams(1024, 1, 1, 16, 32), users.NewRepository(), emails, nil, appURL)

	// The disposable servers must not retain browser/proxy keepalive connections during teardown.
	backend := fiber.New(fiber.Config{DisableKeepalive: true})
	// Only these workspace-supporting reads are synthetic. Google auth handlers,
	// sessions, users service/repository, and durable agreement enqueue are production code.
	backend.Get("/api/organizations", func(c fiber.Ctx) error { return c.JSON(fiber.Map{"organizations": []any{}}) })
	backend.Get("/api/users/profile", func(c fiber.Ctx) error { return c.JSON(fiber.Map{"full_name": "Google Browser Test"}) })
	api.RegisterHandlers(backend.Group("/api"), transporthttp.NewServer(usersService, nil, oidcService, store, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, appURL, callbackURL, true, true, nil))
	backendListener := listen(t, "127.0.0.1:0")
	go func() { _ = backend.Listener(backendListener, fiber.ListenConfig{DisableStartupMessage: true}) }()

	router, err := frontend.NewRouter(appURL, slog.New(slog.DiscardHandler), store)
	if err != nil {
		t.Fatal(err)
	}
	policy := proxy.DefaultSecurityPolicy()
	policy.AllowPrivateIPs = true
	previousPolicy := proxy.WithSecurityPolicy(policy)
	defer proxy.WithSecurityPolicy(previousPolicy)
	// Idle browser preconnections must expire before the five-second shutdown deadline.
	app := fiber.New(fiber.Config{ReadTimeout: 2 * time.Second, DisableKeepalive: true})
	app.Use("/api", func(c fiber.Ctx) error { return proxy.Do(c, "http://"+backendListener.Addr().String()+c.OriginalURL()) })
	app.Get("/_test/assert", fixtureAssertions(pool, issuer))
	router.RegisterRoutes(app, frontend.CommunityPresentation{})
	go func() { _ = app.Listener(appListener, fiber.ListenConfig{DisableStartupMessage: true}) }()
	fmt.Println(appURL)

	select {
	case <-signals:
	case <-ctx.Done():
	}
	shutdownFixtureApp(t, app)
	shutdownFixtureApp(t, backend)
}

func shutdownFixtureApp(t *testing.T, app *fiber.App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), fixtureShutdownTimeout)
	defer cancel()
	if err := app.ShutdownWithContext(ctx); err != nil {
		t.Errorf("fixture shutdown: %v", err)
	}
}

type issuerFixture struct {
	url                     string
	signer                  jose.Signer
	key                     *ecdsa.PrivateKey
	mu                      sync.Mutex
	authorization, exchange int
	codes                   map[string]bool
}

func newIssuer(t *testing.T) (*issuerFixture, func()) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &issuerFixture{signer: signer, key: key, codes: map[string]bool{}}
	listener := listen(t, "localhost:0")
	issuer.url = "http://localhost:" + fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", issuer.discovery)
	mux.HandleFunc("/keys", issuer.keys)
	mux.HandleFunc("/authorize", issuer.authorize)
	mux.HandleFunc("/token", issuer.token)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	return issuer, func() {
		ctx, cancel := context.WithTimeout(context.Background(), fixtureShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("issuer shutdown: %v", err)
		}
		_ = server.Close()
	}
}

func (i *issuerFixture) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, fiber.Map{"issuer": i.url, "authorization_endpoint": i.url + "/authorize", "token_endpoint": i.url + "/token", "jwks_uri": i.url + "/keys", "id_token_signing_alg_values_supported": []string{"ES256"}})
}

func (i *issuerFixture) keys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &i.key.PublicKey, Algorithm: "ES256", Use: "sig"}}})
}

func (i *issuerFixture) authorize(w http.ResponseWriter, r *http.Request) {
	redirect, err := url.Parse(r.URL.Query().Get("redirect_uri"))
	if err != nil || redirect.Host == "" {
		http.Error(w, "redirect", 400)
		return
	}
	i.mu.Lock()
	i.authorization++
	code := fmt.Sprintf("browser-code-%d", i.authorization)
	i.codes[code] = false
	i.mu.Unlock()
	query := redirect.Query()
	query.Set("code", code)
	query.Set("state", r.URL.Query().Get("state"))
	redirect.RawQuery = query.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (i *issuerFixture) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "authorization_code" {
		http.Error(w, "grant", 400)
		return
	}
	code := r.Form.Get("code")
	i.mu.Lock()
	used, ok := i.codes[code]
	if !ok || used {
		i.mu.Unlock()
		http.Error(w, "code", 400)
		return
	}
	i.codes[code] = true
	i.exchange++
	sequence := i.exchange
	i.mu.Unlock()
	claims := jwt.Claims{Issuer: i.url, Subject: fmt.Sprintf("google-browser-%d", sequence), Audience: jwt.Audience{clientID}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))}
	token, err := jwt.Signed(i.signer).Claims(claims).Claims(map[string]any{"email": fmt.Sprintf("google-browser-%d@example.test", sequence), "email_verified": true, "name": "Google Browser Test", "locale": "en"}).Serialize()
	if err != nil {
		http.Error(w, "token", 500)
		return
	}
	writeJSON(w, fiber.Map{"access_token": "fixture-access", "token_type": "Bearer", "id_token": token})
}

func fixtureAssertions(pool *pgxpool.Pool, issuer *issuerFixture) fiber.Handler {
	return func(c fiber.Ctx) error {
		email := strings.TrimSpace(c.Query("email"))
		var created time.Time
		err := pool.QueryRow(c.Context(), `SELECT created_at FROM users WHERE email=$1`, email).Scan(&created)
		var jobs int
		if err == nil {
			err = pool.QueryRow(c.Context(), `SELECT count(*) FROM river_job WHERE kind='email_send' AND args->>'type'='message'`).Scan(&jobs)
		}
		issuer.mu.Lock()
		authorization, exchange := issuer.authorization, issuer.exchange
		issuer.mu.Unlock()
		return c.JSON(fiber.Map{"authorization": authorization, "exchange": exchange, "created_at": created, "message_jobs": jobs, "error": errorString(err)})
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, "fixture response encoding failed", http.StatusInternalServerError)
	}
}

func listen(t *testing.T, address string) net.Listener {
	t.Helper()
	listener, err := new(net.ListenConfig).Listen(context.Background(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	return listener
}
