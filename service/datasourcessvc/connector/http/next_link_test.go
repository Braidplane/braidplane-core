package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nextLinkOperation = `{"method":"GET","path":"/items","query_params":{"window":"fixed"},"pagination":{"next_link_path":"next","records_path":"items","max_pages":3,"timeout_seconds":2}}`

func TestNextLinkPagination(t *testing.T) {
	for _, kind := range []string{"relative", "absolute", "query", "redirect"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer fixture", r.Header.Get("Authorization"))
				assert.Equal(t, "fixed", r.URL.Query().Get("window"))
				if kind == "redirect" && r.URL.Path == "/items" {
					http.Redirect(w, r, "/nested/items?window=fixed", http.StatusFound)
					return
				}
				calls++
				w.Header().Set("Content-Type", "application/json")
				if calls == 1 {
					link := "page2?window=fixed"
					switch kind {
					case "absolute":
						link = server.URL + "/page2?window=fixed"
					case "query":
						link = "?page=2&window=fixed"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"items": []int{1}, "next": link})
				} else {
					if kind == "redirect" {
						assert.Equal(t, "/nested/page2", r.URL.Path)
					}
					_, _ = fmt.Fprint(w, `{"items":[2],"next":null}`)
				}
			}))
			defer server.Close()
			ctx := transportsecurity.WithPlaintext(context.Background())
			connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{BearerToken: "fixture"})
			require.NoError(t, err)
			execution, err := connector.Execute(ctx, []byte(nextLinkOperation), nil)
			require.NoError(t, err)
			require.NoError(t, execution.Error())
			response, ok := execution.HTTP()
			require.True(t, ok)
			require.JSONEq(t, `{"items":[1,2],"next":null}`, string(response.Body))
			require.Equal(t, 2, calls)
		})
	}
}

func TestNextLinkRejectsIncompleteDrains(t *testing.T) {
	for _, link := range []string{"/items?window=fixed", "http://127.0.0.1/private", "https://other.invalid/items", "//other.invalid/items", "http://user:pass@example.invalid/items", "file:///etc/passwd", "#fragment", ":bad"} {
		t.Run(link, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []int{1}, "next": link})
			}))
			defer server.Close()
			ctx := transportsecurity.WithPlaintext(context.Background())
			connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			execution, err := connector.Execute(ctx, []byte(nextLinkOperation), nil)
			require.NoError(t, err)
			require.Error(t, execution.Error())
			response, _ := execution.HTTP()
			require.Nil(t, response)
			require.Equal(t, 1, calls)
		})
	}
}

func TestNextLinkLimits(t *testing.T) {
	for _, limits := range []types.ExecutionLimits{{MaxResultRows: 1}, {HTTPMaxResponseBytes: 50}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"items":[1],"next":"?page=2"}`)
		}))
		ctx := transportsecurity.WithPlaintext(context.Background())
		connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
		require.NoError(t, err)
		execution, err := connector.Execute(ctx, []byte(nextLinkOperation), nil, limits)
		require.NoError(t, err)
		require.Error(t, execution.Error())
		response, _ := execution.HTTP()
		require.Nil(t, response)
		server.Close()
	}
}

func TestNextLinkPreservesOAuthAndEncryptionBoundary(t *testing.T) {
	for _, destination := range []string{"oauth path escape", "downgrade"} {
		t.Run(destination, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, "/v1/items", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				link := "/outside"
				if destination == "downgrade" {
					link = "http://" + r.Host + "/v1/items"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []int{1}, "next": link})
			}))
			defer server.Close()
			connector := oauthTestConnector(t, server, "/health")
			execution, err := connector.Execute(t.Context(), []byte(`{"method":"GET","path":"/items","pagination":{"next_link_path":"next","records_path":"items","max_pages":3,"timeout_seconds":2}}`), nil)
			require.NoError(t, err)
			require.Error(t, execution.Error())
			require.Equal(t, 1, calls)
		})
	}
}
