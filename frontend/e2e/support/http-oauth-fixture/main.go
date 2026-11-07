// This fixture runs production SSR against synthetic API replies; it does not implement the OAuth protocol or persistence.
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
	queries := []any{}
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
		case "/api/organizations/acme/provider-connectors/hubspot/data-source-installs":
			w.WriteHeader(http.StatusCreated)
			encode(map[string]any{"data_source": map[string]any{"slug": "hubspot-crm", "name": "HubSpot CRM", "type": "hubspot"}, "install": map[string]any{"authorization_url": origin + "/_test/consent"}})
		case "/api/organizations/acme/data-sources/test-connection":
			w.WriteHeader(http.StatusNoContent)
		case "/api/organizations/acme/data-sources":
			if r.Method == http.MethodPost {
				var input struct {
					Name        string         `json:"name"`
					Slug        string         `json:"slug"`
					Config      map[string]any `json:"config"`
					Credentials struct {
						OAuth struct {
							GrantType string `json:"grant_type"`
						} `json:"oauth"`
					} `json:"credentials"`
				}
				r.Body = http.MaxBytesReader(w, r.Body, 16384)
				if json.NewDecoder(r.Body).Decode(&input) != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				source = map[string]any{"slug": input.Slug, "name": input.Name, "type": "http", "config": input.Config, "read_only": true, "created_at": "2026-10-15T00:00:00Z", "updated_at": "2026-10-15T00:00:00Z"}
				source["schema_status"] = "ready"
				source["schema"] = map[string]any{"schema_type": "http", "operations": []any{
					map[string]any{"id": "getUser", "method": "GET", "path": "/users/{id}", "draft": map[string]any{"statement": map[string]any{"type": "http", "method": "GET", "path": "/users/{{path_id}}"}, "parameters": []any{map[string]any{"name": "path_id", "data_type": "text", "is_required": true}}}},
					map[string]any{"id": "upload", "method": "POST", "path": "/upload", "diagnostics": []string{"Multipart request bodies require manual authoring."}},
				}}
				mode = input.Credentials.OAuth.GrantType
				pending()
				w.WriteHeader(http.StatusCreated)
				encode(source)
			} else {
				items := []any{}
				if source != nil {
					items = append(items, source)
				}
				encode(map[string]any{"data_sources": items, "has_more": false})
			}
		case "/api/organizations/acme/data-sources/http-app":
			if source == nil {
				w.WriteHeader(404)
			} else {
				encode(source)
			}
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
	app.Get("/_test/consent", func(c fiber.Ctx) error {
		return c.Type("html").SendString(`<h1>Synthetic OAuth consent</h1><p>UI fixture only. No provider credentials or tokens.</p><form method="post" action="/_test/authorize"><button>Allow access</button></form>`)
	})
	app.Post("/_test/authorize", func(c fiber.Ctx) error {
		mu.Lock()
		activate()
		mu.Unlock()
		return c.Redirect().Status(http.StatusSeeOther).To("/orgs/acme/data-sources/http-app?section=settings&provider_oauth=success")
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
