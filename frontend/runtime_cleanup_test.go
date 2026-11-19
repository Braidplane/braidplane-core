package frontend

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeCleanupPages(t *testing.T) {
	const org = "/api/organizations/acme-hq"
	const project = org + "/projects/default"
	const spec = `{"metadata":{"slug":"daily","name":"Daily"},"steps":[{"kind":"query","slug":"read","name":"Read","query":{"data_source_ref":"db","operation_type":"sql","operation_ref":"read-orders","parameters":{"region":"{{runtime.env.region}}"}}}]}`
	const pipeline = `{"slug":"daily","current_version_spec_hash":"hash","sync_status":"synced","has_pending_spec":false,"spec":` + spec + `}`
	for _, tc := range []struct {
		name, path, mode string
		want, absent     []string
	}{
		{"project refs and bindings", "/orgs/acme-hq/projects/default/environments", "", []string{"Logical ref · daily / old", "Binding · prod / daily / old"}, []string{"Logical ref · daily / db", "Binding · prod / daily / db"}},
		{"organization variables", "/organizations/acme-hq/runtime-variables", "", []string{"Unused by current organization pipeline specs", "Missing required value"}, nil},
		{"bound operations", "/orgs/acme-hq/queries?ds=warehouse-prod", "", []string{"<li class=\"break-words\">old-operation</li>"}, []string{"<li class=\"break-words\">read-orders</li>"}},
		{"unresolved operation binding", "/orgs/acme-hq/queries?ds=warehouse-prod", "unbound", []string{"Usage is unknown"}, []string{"<li class=\"break-words\">read-orders</li>"}},
		{"malformed spec", "/organizations/acme-hq/runtime-variables", "malformed", []string{"Usage is unknown"}, []string{"Unused by current organization pipeline specs"}},
		{"inaccessible spec", "/orgs/acme-hq/projects/default/environments", "denied", []string{"Usage is unknown"}, []string{"Logical ref ·"}},
		{"another member has a draft", "/organizations/acme-hq/runtime-variables", "other-draft", []string{"Usage is unknown"}, []string{"Unused by current organization pipeline specs"}},
		{"unavailable ref inventory", "/orgs/acme-hq/projects/default/environments", "refs-denied", []string{"Usage is unknown"}, []string{"Logical ref ·"}},
		{"pending creation", "/orgs/acme-hq/projects/default/environments", "pending", []string{"Usage is unknown"}, []string{"Binding ·"}},
		{"incomplete listing", "/organizations/acme-hq/runtime-variables", "cursor", []string{"Usage is unknown"}, []string{"Unused by current organization pipeline specs"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if handleTestAdminRolePermissions(w, r) {
					return
				}
				switch r.URL.Path {
				case org + "/projects":
					writeJSON(w, `{"projects":[{"slug":"default"},{"slug":"west"}]}`)
				case org + "/projects/west/pipelines":
					writeJSON(w, `{"pipelines":[{"slug":"west-orders","current_version_spec_hash":"hash"}]}`)
				case org + "/projects/west/pipelines/west-orders":
					writeJSON(w, strings.NewReplacer("daily", "west-orders", "env.region", "env.west", "read-orders", "west-operation").Replace(pipeline))
				case org + "/projects/west/pipeline-creations":
					writeJSON(w, `{"pipeline_creations":[]}`)
				case project + "/pipelines":
					if tc.mode == "cursor" {
						writeJSON(w, `{"pipelines":[],"has_more":true}`)
						return
					}
					writeJSON(w, `{"pipelines":[{"slug":"daily","current_version_spec_hash":"hash"}]}`)
				case project + "/pipelines/daily":
					switch tc.mode {
					case "denied":
						w.WriteHeader(http.StatusForbidden)
					case "other-draft":
						writeJSON(w, strings.Replace(pipeline, `"has_pending_spec":false`, `"has_pending_spec":true`, 1))
					case "malformed":
						writeJSON(w, strings.Replace(pipeline, `"kind":"query"`, `"kind":"unknown"`, 1))
					default:
						writeJSON(w, pipeline)
					}
				case project + "/pipeline-creations":
					if tc.mode == "pending" {
						writeJSON(w, `{"pipeline_creations":[{"pipeline_slug":"proposed"}]}`)
						return
					}
					writeJSON(w, `{"pipeline_creations":[]}`)
				case project + "/data-source-refs":
					if tc.mode == "refs-denied" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					writeJSON(w, `{"data_source_refs":[{"slug":"db","pipeline_slug":"daily"},{"slug":"old","pipeline_slug":"daily"}]}`)
				case project + "/environments/prod/data-source-bindings":
					if tc.mode == "unbound" {
						writeJSON(w, `{"data_source_bindings":[]}`)
						return
					}
					writeJSON(w, `{"data_source_bindings":[{"data_source_ref_slug":"db","pipeline_slug":"daily","environment_slug":"prod","data_source_slug":"warehouse-prod"},{"data_source_ref_slug":"old","pipeline_slug":"daily","environment_slug":"prod","data_source_slug":"warehouse-prod"}]}`)
				case org + "/runtime-variable-comparison":
					writeJSON(w, `{"environments":["prod"],"rows":[{"key":"region","cells":[{"source":"missing","blocked":true}]},{"key":"west","cells":[{"source":"override"}]},{"key":"old","cells":[{"source":"override"}]}]}`)
				case org + "/data-sources/warehouse-prod/queries":
					writeJSON(w, `{"queries":[{"slug":"old-operation"},{"slug":"read-orders"}]}`)
				default:
					writeProjectBindingReloadFixture(w, r)
				}
			}))
			defer server.Close()
			app, cookie := setupBindingActionTestApp(t, server.URL)
			sep := "?"
			if strings.Contains(tc.path, "?") {
				sep = "&"
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path+sep+"cleanup=1", nil)
			req.Header.Set("Cookie", cookie)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			if tc.mode == "refs-denied" {
				require.Equal(t, http.StatusForbidden, resp.StatusCode)
			} else {
				require.Equal(t, http.StatusOK, resp.StatusCode)
			}
			page := string(raw)
			for _, value := range tc.want {
				if !strings.Contains(page, value) {
					t.Errorf("missing %q", value)
				}
			}
			for _, value := range tc.absent {
				if strings.Contains(page, value) {
					t.Errorf("unexpected %q", value)
				}
			}
			if tc.name == "organization variables" {
				rows := strings.Split(page, "<tr>")
				for _, row := range rows {
					row, _, _ = strings.Cut(row, "</tr>")
					switch {
					case strings.Contains(row, ">old</span>"):
						require.Contains(t, row, "Unused by current organization pipeline specs")
					case strings.Contains(row, ">west</span>"), strings.Contains(row, ">region</span>"):
						require.NotContains(t, row, "Unused by current organization pipeline specs")
					}
				}
				require.Contains(t, page, ">west</span>")
				require.Equal(t, 1, strings.Count(page, "Unused by current organization pipeline specs"))
			}
		})
	}
}
