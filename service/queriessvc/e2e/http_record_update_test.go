package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testHTTPRecordUpdateCredentials(t *testing.T, stack *testStack) {
	org, _, user := createOrgUserAndPostgresDataSource(t, stack, "Company mirror")
	actor := model.UserEventActor(user)
	for _, auth := range []string{"static", "oauth"} {
		t.Run(auth, func(t *testing.T) {
			var mu sync.Mutex
			var companyName string
			tokenCalls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/token":
					tokenCalls++
					_, _ = fmt.Fprint(w, `{"access_token":"fixture-oauth","refresh_token":"fixture-refresh","token_type":"Bearer","expires_in":3600,"scope":"crm.objects.companies.write crm.objects.companies.read"}`)
				case "/api/health":
					_, _ = fmt.Fprint(w, `{}`)
				case "/api/crm/v3/objects/companies/8713":
					assert.Equal(t, "Bearer fixture-"+auth, r.Header.Get("Authorization"))
					switch r.Method {
					case http.MethodGet:
						assert.Equal(t, "name", r.URL.Query().Get("properties"))
					case http.MethodPatch:
						var update struct {
							Properties map[string]string `json:"properties"`
						}
						assert.NoError(t, json.NewDecoder(r.Body).Decode(&update))
						assert.Equal(t, map[string]string{"name": "Synthetic company"}, update.Properties)
						companyName = update.Properties["name"]
					default:
						t.Errorf("unexpected method %s", r.Method)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "8713", "properties": map[string]string{"name": companyName, "domain": "owned-by-provider"}})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			previous := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			defer func() { http.DefaultTransport = previous }()
			credentials := map[string]any{"bearer_token": "fixture-static"}
			if auth == "oauth" {
				credentials = map[string]any{"oauth": map[string]any{"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "client_id": "fixture-app", "client_secret": "fixture-secret", "grant_type": "authorization_code", "scopes": []string{"crm.objects.companies.write", "crm.objects.companies.read"}}}
			}
			ds := must.Slug("company-" + auth)
			_, err := stack.datasourcesSvc.Create(t.Context(), org, actor, &datasourcessvc.CreateParams{Name: "Company " + auth, Slug: ds, Type: model.DataSourceTypeHTTP, ConfigJSON: json.RawMessage(fmt.Sprintf(`{"base_url":%q,"health_path":"health","allow_private_hosts":true}`, server.URL+"/api")), Credentials: credentials})
			require.NoError(t, err)
			if auth == "oauth" {
				connection, err := stack.datasourcesSvc.ConnectHTTPOAuth(t.Context(), org, ds, actor, false)
				require.NoError(t, err)
				authURL, err := url.Parse(connection.Install.AuthorizationURL)
				require.NoError(t, err)
				_, err = stack.datasourcesSvc.CompleteProviderOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "http", State: authURL.Query().Get("state"), Code: "fixture-code", Issuer: server.URL, Actor: actor})
				require.NoError(t, err)
			}
			operation := map[string]any{"type": "http", "method": "PATCH", "path": "/crm/v3/objects/companies/{{record_id}}", "body": map[string]any{"properties": "{{input}}"}}
			statement, err := json.Marshal(operation)
			require.NoError(t, err)
			data, err := json.Marshal(map[string]any{"statement": string(statement)})
			require.NoError(t, err)
			for _, step := range []struct {
				reconcile bool
				status    int
			}{{true, 409}, {false, 200}, {true, 200}} {
				ctx := sinkbinding.WithHTTPRecordUpdate(sinkbinding.WithSinkExecution(t.Context()), "8713", map[string]string{"name": "Synthetic company"}, step.reconcile, sinkbinding.RecordReadback{IdentityField: "id", FieldsObject: "properties", SelectParameter: "properties"})
				result, _, err := stack.queriesSvc.ExecuteSnapshot(ctx, org, ds, contractplan.OperationContent{Data: data}, nil, nil)
				require.NoError(t, err)
				reply, ok := result.HTTP()
				require.True(t, ok)
				require.Equal(t, step.status, reply.StatusCode)
				require.Empty(t, reply.Body)
				require.Empty(t, reply.Headers)
			}
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, "Synthetic company", companyName)
			if auth == "oauth" {
				require.Equal(t, 1, tokenCalls, "all operations should reuse the established grant")
			}
		})
	}
}
