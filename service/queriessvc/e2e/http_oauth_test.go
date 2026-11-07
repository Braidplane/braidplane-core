package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRenewsHTTPOAuthForSavedQueriesAndPipelineReads(t *testing.T, stack *testStack) {
	for _, grantType := range []string{"client_credentials", "authorization_code"} {
		t.Run(grantType, func(t *testing.T) {
			var generations, reads atomic.Int64
			provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/token":
					r.Body = http.MaxBytesReader(w, r.Body, 4096)
					assert.NoError(t, r.ParseForm())
					id, secret, ok := r.BasicAuth()
					assert.True(t, ok)
					assert.Equal(t, "fixture-app", id)
					assert.Equal(t, "fixture-app-secret", secret)
					previous := generations.Load()
					switch {
					case grantType == "client_credentials":
						assert.Equal(t, grantType, r.Form.Get("grant_type"))
					case previous == 0:
						assert.Equal(t, "authorization_code", r.Form.Get("grant_type"))
						assert.NotEmpty(t, r.Form.Get("code_verifier"))
					default:
						assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
						assert.Equal(t, fmt.Sprintf("refresh-%d", previous), r.Form.Get("refresh_token"))
					}
					current := generations.Add(1)
					_, _ = fmt.Fprintf(w, `{"access_token":"access-%d","refresh_token":"refresh-%d","token_type":"Bearer","expires_in":3600,"scope":"read"}`, current, current)
				case "/v1/health", "/v1/records":
					current := generations.Load()
					if r.Header.Get("Authorization") != fmt.Sprintf("Bearer access-%d", current) {
						t.Error("resource request used a stale or missing token")
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					assert.Empty(t, r.Header.Get("Cookie"))
					assert.Empty(t, r.URL.RawQuery)
					if r.URL.Path == "/v1/health" {
						_, _ = fmt.Fprint(w, `{"ok":true}`)
						return
					}
					reads.Add(1)
					_, _ = fmt.Fprintf(w, `[{"id":1,"generation":%d}]`, current)
				default:
					t.Errorf("unexpected provider path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer provider.Close()
			previousTransport := http.DefaultTransport
			http.DefaultTransport = provider.Client().Transport
			t.Cleanup(func() { http.DefaultTransport = previousTransport })

			suffix := time.Now().UnixNano()
			user, err := stack.usersService.CreateUser(t.Context(), model.NewUser{FullName: "HTTP OAuth execution fixture", Email: fmt.Sprintf("http-oauth-%d@example.com", suffix), Password: "password123", Role: model.RoleUser, Locale: "en"})
			require.NoError(t, err)
			org, err := stack.orgService.CreateOrganization(t.Context(), user.Slug, model.UserEventActor(user.Slug), model.NewOrganization{Name: "HTTP OAuth execution", Slug: must.Slug(fmt.Sprintf("http-oauth-org-%d", suffix))})
			require.NoError(t, err)
			createPipelineWorkspaceProject(t, stack, t.Context(), org.Slug, user.Slug)
			source := must.Slug(fmt.Sprintf("http-oauth-source-%d", suffix))
			config, err := json.Marshal(map[string]any{"base_url": provider.URL + "/v1", "health_path": "health", "allow_private_hosts": true})
			require.NoError(t, err)
			created, err := stack.datasourcesSvc.Create(t.Context(), org.Slug, model.UserEventActor(user.Slug), &datasourcessvc.CreateParams{Name: "HTTP OAuth", Slug: source, Type: model.DataSourceTypeHTTP, ConfigJSON: config, ReadOnly: true, Credentials: map[string]any{"oauth": map[string]any{"issuer": provider.URL, "authorization_endpoint": provider.URL + "/authorize", "token_endpoint": provider.URL + "/token", "client_id": "fixture-app", "client_secret": "fixture-app-secret", "grant_type": grantType, "scopes": []string{"read"}}}})
			require.NoError(t, err)
			require.False(t, created.IsActive)
			connection, err := stack.datasourcesSvc.ConnectHTTPOAuth(t.Context(), org.Slug, source, model.UserEventActor(user.Slug))
			require.NoError(t, err)
			if grantType == "authorization_code" {
				require.NotNil(t, connection.Install)
				authorization, err := url.Parse(connection.Install.AuthorizationURL)
				require.NoError(t, err)
				_, err = stack.datasourcesSvc.CompleteProviderOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "http", State: authorization.Query().Get("state"), Code: "fixture-code", Issuer: provider.URL, Actor: model.UserEventActor(user.Slug)})
				require.NoError(t, err)
			} else {
				require.Nil(t, connection.Install)
				require.True(t, connection.DataSource.IsActive)
			}
			require.EqualValues(t, 1, generations.Load())
			query := createQuery(t, stack, org.Slug, source, "OAuth records", map[string]any{"type": "http", "method": "GET", "path": "records"}, 30, nil)
			execute := func(expected int64) {
				result, err := stack.queriesSvc.Execute(t.Context(), org.Slug, source, query, model.UserEventActor(user.Slug), map[string]any{})
				require.NoError(t, err)
				require.True(t, result.IsSuccess())
				response, ok := result.HTTP()
				require.True(t, ok)
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.JSONEq(t, fmt.Sprintf(`[{"id":1,"generation":%d}]`, expected), string(response.Body))
				serialized, err := json.Marshal(result)
				require.NoError(t, err)
				require.NotContains(t, string(serialized), "fixture-app-secret")
				require.NotContains(t, string(serialized), "refresh-")
				require.NotContains(t, string(serialized), "access-")
			}
			expire := func() {
				changed, err := stack.pool.Exec(t.Context(), `UPDATE credential_oauth_grants g SET access_token_expires_at=NOW()-INTERVAL '1 minute' FROM data_sources ds JOIN organizations o ON o.id=ds.organization_id WHERE g.credential_id=ds.credential_id AND o.slug=$1 AND ds.slug=$2`, org.Slug, source)
				require.NoError(t, err)
				require.EqualValues(t, 1, changed.RowsAffected())
			}
			execute(1)
			expire()
			execute(2)
			require.EqualValues(t, 2, generations.Load())
			expire()
			pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: must.Slug(fmt.Sprintf("http-oauth-pipeline-%d", suffix)), Name: "HTTP OAuth read"}, Steps: []pipelinespec.Step{{Kind: "query", Slug: must.Slug("read-records"), Query: &pipelinespec.QueryRef{DataSourceSlug: source, OperationType: "http", OperationRef: query}}}}
			result, err := newE2EPipelineRunner(t, stack).Run(t.Context(), newPipelineRunRequest(t, stack, org.Slug, fmt.Sprintf("http-oauth-run-%d", suffix), pipeline))
			require.NoError(t, err)
			require.Equal(t, "succeeded", result.Metadata.Status)
			require.EqualValues(t, 3, generations.Load())
			require.EqualValues(t, 3, reads.Load())
			serialized, err := json.Marshal(result)
			require.NoError(t, err)
			require.NotContains(t, string(serialized), "fixture-app-secret")
			require.NotContains(t, string(serialized), "refresh-")
			require.NotContains(t, string(serialized), "access-")
		})
	}
}
