// This fixture runs production SSR against synthetic API replies and in-memory state.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/gofiber/fiber/v3/middleware/static"
)

func main() {
	var mu sync.Mutex
	var source map[string]any
	queries := []map[string]any{}
	mode := ""
	origin := ""
	pending := func() {
		source["is_active"] = false
		source["credential"] = map[string]any{"kind": "oauth2_grant", "status": "pending", "updated_at": "2026-10-15T00:00:00Z", "oauth": map[string]any{"provider": "http"}}
	}
	activate := func() {
		source["is_active"] = true
		source["last_tested_at"] = "2026-10-15T00:00:00Z"
		source["credential"].(map[string]any)["status"] = "active"
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		encode := func(value any) {
			if err := json.NewEncoder(w).Encode(value); err != nil {
				panic(err)
			}
		}
		switch r.URL.Path {
		case "/api/users/profile":
			encode(map[string]string{"slug": "test-user"})
		case "/api/organizations":
			encode(map[string]any{"organizations": []any{map[string]any{"name": "Acme", "slug": "acme", "member_role": "admin"}}})
		case "/api/organizations/acme":
			encode(map[string]any{"name": "Acme", "slug": "acme", "member_role": "admin"})
		case "/api/organizations/acme/roles/admin":
			encode(map[string]any{"slug": "admin", "permissions": []any{map[string]any{"resource": "*", "action": "*"}}})
		case "/api/organizations/acme/projects":
			encode(map[string]any{"projects": []any{}})
		case "/api/organizations/acme/environments":
			encode(map[string]any{"environments": []any{}})
		case "/api/organizations/acme/provider-connectors/stripe/data-source-installs":
			var input struct {
				Type     string           `json:"type"`
				Name     string           `json:"name"`
				Slug     string           `json:"slug"`
				Config   api.StripeConfig `json:"config"`
				ReadOnly bool             `json:"read_only"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&input) != nil || input.Type != "stripe" || !input.ReadOnly || input.Config.Mode != api.StripeConfigModeLive || input.Slug != "stripe-oauth" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			source = map[string]any{"slug": input.Slug, "name": input.Name, "type": input.Type, "config": input.Config, "read_only": true}
			pending()
			source["credential"].(map[string]any)["oauth"] = map[string]any{"provider": "stripe_live"}
			w.WriteHeader(http.StatusCreated)
			encode(map[string]any{"data_source": source, "install": map[string]any{"authorization_url": origin + "/_test/consent"}})
		case "/api/organizations/acme/provider-connectors/stripe/installs":
			var input api.StartProviderConnectorInstallRequest
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&input) != nil || input.DataSourceSlug != "stripe-oauth" || input.Intent != api.RotateGrant {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			encode(map[string]any{"authorization_url": origin + "/_test/consent"})
		case "/api/organizations/acme/provider-connectors/hubspot/data-source-installs":
			w.WriteHeader(http.StatusCreated)
			encode(map[string]any{"data_source": map[string]any{"slug": "hubspot-crm", "name": "HubSpot CRM", "type": "hubspot"}, "install": map[string]any{"authorization_url": origin + "/_test/consent"}})
		case "/api/organizations/acme/data-sources/test-connection":
			var input struct {
				Type   string `json:"type"`
				Config struct {
					Mode       string            `json:"mode"`
					HealthPath string            `json:"health_path"`
					WifObjects []json.RawMessage `json:"wif_objects"`
				} `json:"config"`
				Credentials struct {
					APIKey   string `json:"api_key"`
					AuthType string `json:"auth_type"`
					Username string `json:"username"`
				} `json:"credentials"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&input); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if input.Type == "stripe" && (input.Config.Mode != "test" || input.Credentials.APIKey != "rk_test_synthetic") {
				w.WriteHeader(http.StatusUnprocessableEntity)
				encode(map[string]any{"error": map[string]any{"message": "Check the Stripe restricted key and mode."}})
				return
			}
			if input.Type == "snowflake" {
				if input.Credentials.AuthType != "password" || len(input.Config.WifObjects) != 0 {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if input.Credentials.Username != "synthetic-reader" {
					w.WriteHeader(http.StatusUnprocessableEntity)
					encode(map[string]any{"error": map[string]any{"message": "Check the Snowflake username and password."}})
					return
				}
			}
			if input.Config.HealthPath == "/offline" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				encode(map[string]any{"error": map[string]any{"code": "fixture_unavailable", "message": "Health endpoint is unavailable."}})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/organizations/acme/data-sources":
			if r.Method == http.MethodPost {
				var input struct {
					Name        string         `json:"name"`
					Slug        string         `json:"slug"`
					Type        string         `json:"type"`
					Config      map[string]any `json:"config"`
					Credentials struct {
						APIKey string `json:"api_key"`
						OAuth  struct {
							GrantType string `json:"grant_type"`
						} `json:"oauth"`
					} `json:"credentials"`
				}
				r.Body = http.MaxBytesReader(w, r.Body, 16384)
				if json.NewDecoder(r.Body).Decode(&input) != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if input.Type == "stripe" && (input.Config["mode"] != "test" || input.Credentials.APIKey != "rk_test_synthetic") {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				source = map[string]any{"slug": input.Slug, "name": input.Name, "type": input.Type, "config": input.Config, "read_only": true, "created_at": "2026-10-15T00:00:00Z", "updated_at": "2026-10-15T00:00:00Z"}
				if input.Type == "git" || input.Type == "snowflake" || input.Type == "stripe" {
					source["is_active"] = true
					w.WriteHeader(http.StatusCreated)
					encode(source)
					return
				}
				source["schema_status"] = "ready"
				source["schema"] = map[string]any{"schema_type": "http", "operations": []any{
					map[string]any{"id": "getUser", "method": "GET", "path": "/users/{id}", "draft": map[string]any{"statement": map[string]any{"type": "http", "method": "GET", "path": "/users/{{path_id}}"}, "parameters": []any{map[string]any{"name": "path_id", "data_type": "text", "is_required": true}}}},
					map[string]any{"id": "upload", "method": "POST", "path": "/upload", "diagnostics": []string{"Multipart request bodies require manual authoring."}},
				}}
				mode = input.Credentials.OAuth.GrantType
				if mode == "" {
					source["is_active"] = true
				} else {
					pending()
				}
				w.WriteHeader(http.StatusCreated)
				encode(source)
			} else {
				items := []any{}
				if source != nil {
					items = append(items, source)
				}
				encode(map[string]any{"data_sources": items, "has_more": false})
			}
		case "/api/organizations/acme/data-sources/ssh-repository",
			"/api/organizations/acme/data-sources/ssh-repository/credentials":
			if source == nil || source["type"] != "git" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/credentials") {
				if r.Method != http.MethodPatch {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				var input api.UpdateDataSourceCredentialsJSONRequestBody
				if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&input); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				credentials, err := input.Credentials.AsGitCredentials()
				if err != nil || credentials.AuthType != "ssh" || credentials.SshPrivateKey == nil || credentials.SshKnownHosts == nil {
					w.WriteHeader(http.StatusUnprocessableEntity)
					return
				}
			}
			encode(source)
		case "/api/organizations/acme/data-sources/stripe-revenue", "/api/organizations/acme/data-sources/stripe-oauth":
			if source == nil || source["type"] != "stripe" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			encode(source)
		case "/api/organizations/acme/data-sources/snowflake-password":
			if source == nil || source["type"] != "snowflake" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			encode(source)
		case "/api/organizations/acme/data-sources/http-app":
			if source == nil {
				w.WriteHeader(404)
				return
			}
			if r.Method == http.MethodPatch {
				var input struct {
					Name   string         `json:"name"`
					Config map[string]any `json:"config"`
				}
				r.Body = http.MaxBytesReader(w, r.Body, 16384)
				if json.NewDecoder(r.Body).Decode(&input) != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if input.Config["health_path"] == "/unavailable" {
					w.WriteHeader(http.StatusUnprocessableEntity)
					encode(map[string]any{"error": map[string]any{"message": "Choose an available health path."}})
					return
				}
				source["name"] = input.Name
				source["config"] = input.Config
			}
			encode(source)
		case "/api/organizations/acme/data-sources/http-app/schema":
			encode(map[string]any{"status": "not_imported", "schema_type": "http"})
		case "/api/organizations/acme/data-sources/http-app/queries":
			if r.Method == http.MethodPost {
				var query map[string]any
				r.Body = http.MaxBytesReader(w, r.Body, 16384)
				if json.NewDecoder(r.Body).Decode(&query) != nil {
					w.WriteHeader(400)
					return
				}
				query["slug"] = "get-user"
				query["data_source_slug"] = "http-app"
				queries = append(queries, query)
				w.WriteHeader(http.StatusCreated)
				encode(query)
			} else {
				encode(map[string]any{"queries": queries, "has_more": false})
			}
		case "/api/organizations/acme/data-sources/http-app/queries/get-user":
			if len(queries) == 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			query := queries[len(queries)-1]
			if r.Method == http.MethodPut {
				var update map[string]any
				r.Body = http.MaxBytesReader(w, r.Body, 16384)
				if json.NewDecoder(r.Body).Decode(&update) != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				for key, value := range update {
					query[key] = value
				}
			}
			encode(query)
		case "/api/organizations/acme/data-sources/http-app/queries/get-user/execute":
			var input struct {
				Parameters map[string]any `json:"parameters"`
			}
			r.Body = http.MaxBytesReader(w, r.Body, 16384)
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if input.Parameters["path_id"] == "missing" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				encode(map[string]any{"error": map[string]any{
					"message": "The customer was not found.", "detailed_error": "The synthetic HTTP endpoint returned 404.",
				}})
				return
			}
			encode(map[string]any{"type": "http", "result_type": "http", "success": true, "duration_ms": 5, "payload": map[string]any{
				"status_code": 200, "body": map[string]string{"name": "Synthetic customer"},
			}})
		case "/api/organizations/acme/data-sources/http-app/credentials":
			r.Body = http.MaxBytesReader(w, r.Body, 16384)
			var input struct {
				Credentials struct {
					OAuth struct {
						GrantType string `json:"grant_type"`
					} `json:"oauth"`
				} `json:"credentials"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				w.WriteHeader(400)
				return
			}
			mode = input.Credentials.OAuth.GrantType
			pending()
			encode(source)
		case "/api/organizations/acme/data-sources/http-app/http-oauth/connect":
			if mode == "authorization_code" {
				encode(map[string]any{"data_source": source, "install": map[string]any{"authorization_url": origin + "/_test/consent"}})
			} else {
				activate()
				encode(map[string]any{"data_source": source})
			}
		default:
			if strings.HasSuffix(r.URL.Path, "/schema/status") {
				encode(map[string]any{"status": "not_imported"})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()
	store := session.NewStore()
	router, err := frontend.NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
	if err != nil {
		panic(err)
	}
	app := fiber.New()
	app.Get("/_test/session", func(c fiber.Ctx) error { return reqctx.SetUserSlug(c, store, slug.Slug("test-user")) })
	app.Post("/_test/schema-status", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		status := c.Query("status")
		if source == nil || (status != "importing" && status != "ready") {
			return c.SendStatus(http.StatusBadRequest)
		}
		source["schema_status"] = status
		return c.SendStatus(http.StatusNoContent)
	})
	app.Get("/_test/consent", func(c fiber.Ctx) error {
		return c.Type("html").SendString(`<h1>Synthetic OAuth consent</h1><p>UI fixture only. No provider credentials or tokens.</p><form method="post" action="/_test/authorize"><button>Allow access</button></form>`)
	})
	app.Post("/_test/authorize", func(c fiber.Ctx) error {
		mu.Lock()
		activate()
		slug := source["slug"].(string)
		mu.Unlock()
		return c.Redirect().Status(http.StatusSeeOther).To("/orgs/acme/data-sources/" + slug + "?section=settings&provider_oauth=success")
	})
	app.Get("/assets/icons/*", static.New("brand/icons"))
	app.Get("/assets/*", static.New("frontend/assets"))
	router.RegisterRoutes(app, frontend.CommunityPresentation{})
	listener, err := new(net.ListenConfig).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	origin = "http://" + listener.Addr().String()
	fmt.Println(origin)
	if err := app.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
		panic(err)
	}
}
