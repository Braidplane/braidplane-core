package frontend

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestODCSCatalogInspection(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"InvalidFromPlan", "InvalidFromPlanHTMX", "InvalidToPlan", "IncompletePlan", "InvalidCompare", "ProjectionInputs", "InvalidProjectionInput", "UnselectedCoverage", "Document", "Coverage", "InspectionDenied", "CoveragePagination", "Supported", "Unsupported", "Details", "Consumers", "InvalidSection", "IntegrityMismatch", "ForbiddenDocument", "OtherTenant", "PermissionDenied", "RevisionDenied", "Latest", "Pagination"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile("../pkg/odcs/testdata/supported.json")
			require.NoError(t, err)
			if scenario == "Unsupported" {
				raw = []byte(strings.Replace(string(raw), `"logicalType": "integer"`, `"logicalType": "date"`, 1))
			}
			recorded, err := odcs.Record(raw, "orders")
			require.NoError(t, err)
			if scenario == "IntegrityMismatch" {
				recorded.Digest = "sha256:mismatch"
			}
			if scenario == "ForbiddenDocument" {
				raw = []byte(strings.Replace(string(raw), `"version": "1"`, `"version": "1", "customProperties": [{"property": "password", "value": "PRIVATE-MUST-NOT-RENDER"}]`, 1))
			}
			if scenario == "Document" {
				raw = []byte(strings.Replace(string(raw), "Order interchange", "<script>window.authored = true</script> ${UNRESOLVED}", 1))
				recorded, err = odcs.Record(raw, "orders")
				require.NoError(t, err)
			}
			var calls atomic.Int32
			var impactCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if scenario == "PermissionDenied" && strings.HasSuffix(req.URL.Path, "/roles/admin") {
					_, _ = w.Write([]byte(`{"slug":"admin","permissions":[]}`))
					return
				}
				if handleTestAdminRolePermissions(w, req) {
					return
				}
				switch req.URL.Path {
				case "/api/organizations":
					_, _ = w.Write([]byte(`{"organizations":[{"slug":"acme-hq","name":"Acme","member_role":"admin"}]}`))
				case "/api/organizations/acme-hq/projects/ops/contracts/orders/revisions/rev-one":
					calls.Add(1)
					if scenario == "RevisionDenied" {
						w.WriteHeader(403)
						_, _ = w.Write([]byte(`{"error":{"message":"PRIVATE-MUST-NOT-RENDER"}}`))
						return
					}
					_ = json.NewEncoder(w).Encode(api.ODCSRevision{Slug: "rev-one", Digest: recorded.Digest, DocumentVersion: "1", DeclaredStatus: "active", Document: raw, SchemaObjects: []api.ODCSSchemaObject{{Id: "summary"}, {Id: "order"}}})
				case "/api/organizations/acme-hq/projects/ops/contracts/orders/revisions/rev-one/inspection":
					if scenario == "InspectionDenied" {
						w.WriteHeader(403)
						_, _ = w.Write([]byte(`{}`))
						return
					}
					object := req.URL.Query().Get("schema_object_id")
					if scenario == "UnselectedCoverage" {
						assert.Empty(t, object)
					} else {
						assert.Equal(t, "order", object)
					}
					projection, err := odcs.Convert(raw, object)
					assert.NoError(t, err)
					var coverage []api.ODCSDeclarationCoverage
					encoded, err := json.Marshal(projection.Coverage)
					assert.NoError(t, err)
					assert.NoError(t, json.Unmarshal(encoded, &coverage))
					if scenario == "CoveragePagination" {
						coverage = make([]api.ODCSDeclarationCoverage, 101)
						coverage[100].Path = "$.last"
					}
					_ = json.NewEncoder(w).Encode(api.ODCSRevisionInspection{Revision: api.ODCSRevision{Slug: "rev-one", Digest: recorded.Digest, Document: raw}, CompilerId: odcs.CompilerID, SchemaObjectId: object, DocumentValid: projection.DocumentValid, CoverageComplete: projection.CoverageComplete, ProjectionAvailable: projection.Shape != nil, Coverage: coverage})
				case "/api/organizations/acme-hq/projects/ops/contracts/orders/impact":
					impactCalls.Add(1)
					var input api.ODCSRevisionImpactRequest
					assert.NoError(t, json.NewDecoder(req.Body).Decode(&input))
					assert.Equal(t, "rev-one", input.FromRevisionSlug)
					if scenario == "ProjectionInputs" {
						assert.Equal(t, "rev-two", input.ToRevisionSlug)
						if !assert.NotNil(t, input.FromPlan) || !assert.NotNil(t, input.ToPlan) {
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						assert.Equal(t, new(0), input.FromPlan.ProjectionInput)
						assert.Equal(t, new(0), input.ToPlan.ProjectionInput)
					} else {
						assert.Equal(t, "rev-one", input.ToRevisionSlug)
					}
					_ = json.NewEncoder(w).Encode(api.ODCSRevisionImpact{Consumers: []api.ODCSConsumerImpact{{PipelineSlug: "orders-sync", StepSlug: "fetch-orders", Port: "output", Origin: "deployed", RevisionSlug: "rev-one", SchemaObjectId: "order"}}})
				case "/api/organizations/acme-hq/projects/ops/contracts":
					calls.Add(1)
					assert.Equal(t, "page-two", req.URL.Query().Get("cursor"))
					assert.Equal(t, "20", req.URL.Query().Get("limit"))
					_, _ = w.Write([]byte(`{"contracts":[{"slug":"orders","name":"Orders","status":"active"}],"has_more":false}`))
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			store := session.NewStore()
			router, err := NewRouter(server.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			cookie := getAuthenticatedSessionCookie(t, app)
			path := "/orgs/acme-hq/projects/ops/contracts?selector=1&contract=orders&revision=rev-one"
			if scenario == "OtherTenant" {
				path = strings.Replace(path, "acme-hq", "other-tenant", 1)
			}
			if scenario == "Latest" {
				path = strings.Replace(path, "rev-one", "latest", 1)
			}
			if scenario == "Pagination" {
				path = "/orgs/acme-hq/projects/ops/contracts?selector=1&cursor=page-two"
			}
			if scenario == "Details" || scenario == "Consumers" {
				path += "&section=" + strings.ToLower(scenario)
			}
			if scenario == "UnselectedCoverage" {
				path += "&section=coverage"
			}
			if scenario == "Document" {
				path += "&section=document&object=order"
			}
			if scenario == "Coverage" || scenario == "InspectionDenied" {
				path += "&section=coverage&object=order"
			}
			if scenario == "CoveragePagination" {
				path += "&section=coverage&object=order&page=1"
			}
			if strings.HasPrefix(scenario, "InvalidFromPlan") || scenario == "InvalidToPlan" || scenario == "IncompletePlan" || scenario == "InvalidCompare" || scenario == "ProjectionInputs" || scenario == "InvalidProjectionInput" {
				fromPlan, toPlan := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
				switch scenario {
				case "InvalidFromPlan", "InvalidFromPlanHTMX":
					fromPlan = "invalid" + strings.Repeat("a", 64)
				case "InvalidToPlan":
					toPlan = "sha256:" + strings.Repeat("G", 64)
				case "IncompletePlan":
					toPlan = ""
				}
				path += "&section=impact&compare_revision=rev-two&from_pipeline=sync&to_pipeline=sync&from_plan=" + fromPlan + "&to_plan=" + toPlan
				if scenario == "ProjectionInputs" {
					path += "&from_projection_input=0&to_projection_input=0"
				}
				if scenario == "InvalidProjectionInput" {
					path += "&from_projection_input=-1"
				}
			}
			if scenario == "InvalidCompare" {
				path = strings.Replace(path, "compare_revision=rev-two", "compare_revision=latest", 1)
			}
			if scenario == "InvalidSection" {
				path += "&section=unknown"
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
			req.Header.Set("Cookie", cookie)
			if scenario == "InvalidFromPlanHTMX" {
				req.Header.Set("HX-Request", "true")
			}
			response, err := app.Test(req)
			require.NoError(t, err)
			defer func() { require.NoError(t, response.Body.Close()) }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			html := string(body)
			require.NotContains(t, html, "PRIVATE-MUST-NOT-RENDER")
			switch scenario {
			case "InvalidFromPlan", "InvalidFromPlanHTMX", "InvalidToPlan", "IncompletePlan", "InvalidCompare", "InvalidProjectionInput":
				status := http.StatusBadRequest
				if scenario == "InvalidFromPlanHTMX" {
					status = http.StatusOK
				}
				require.Equal(t, status, response.StatusCode)
				require.Zero(t, impactCalls.Load())
				require.Contains(t, html, `role="alert"`)
				switch scenario {
				case "InvalidCompare":
					require.Contains(t, html, "Enter an exact revision slug")
				case "InvalidProjectionInput":
					require.Contains(t, html, "Select a projection input index from 0 to 63")
				default:
					require.Contains(t, html, "Provide both pipeline slugs")
				}
			case "Document":
				require.Equal(t, 200, response.StatusCode)
				require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
				require.Contains(t, html, `hx-history="false"`)
				require.Contains(t, html, `\u003cscript\u003ewindow.authored = true\u003c/script\u003e`)
				require.Contains(t, html, "${UNRESOLVED}")
				require.NotContains(t, html, "<script>window.authored")
			case "UnselectedCoverage":
				require.Equal(t, 200, response.StatusCode)
				require.NotContains(t, html, "compiled_runtime_constraint")
				require.Contains(t, html, "unresolved")
			case "Coverage":
				require.Equal(t, 200, response.StatusCode)
				require.Contains(t, html, odcs.CompilerID)
				require.Contains(t, html, "compiled_runtime_constraint")
				require.NotContains(t, html, "Next declarations")
				require.NotContains(t, html, "data-odcs-select")
			case "CoveragePagination":
				require.Contains(t, html, "$.last")
				require.Contains(t, html, "Previous declarations")
				require.NotContains(t, html, "Next declarations")
			case "InspectionDenied":
				require.Equal(t, 403, response.StatusCode)
				require.Contains(t, html, "Document inspection could not be loaded")
				require.NotContains(t, html, "Order interchange")
			case "OtherTenant", "PermissionDenied":
				require.Equal(t, 403, response.StatusCode)
				require.Zero(t, calls.Load())
			case "Latest", "InvalidSection":
				require.Equal(t, 400, response.StatusCode)
				require.Zero(t, calls.Load())
			case "IntegrityMismatch", "ForbiddenDocument":
				require.Contains(t, html, "Revision integrity could not be confirmed")
				require.NotContains(t, html, "data-odcs-select")
			case "RevisionDenied":
				require.Equal(t, 403, response.StatusCode)
				require.Contains(t, html, "Revision could not be loaded")
				require.NotContains(t, html, "data-odcs-select")
			case "Pagination":
				require.Contains(t, html, "Orders")
				require.EqualValues(t, 1, calls.Load())
			case "Details":
				require.Equal(t, 200, response.StatusCode)
				require.Contains(t, html, recorded.Digest)
				require.Contains(t, html, "Quality, SLA, governance")
				require.NotContains(t, html, "data-odcs-select")
				require.NotContains(t, html, "orders-sync")
			case "ProjectionInputs":
				require.Equal(t, 200, response.StatusCode)
				require.Contains(t, html, "Compare revisions")
				require.EqualValues(t, 1, impactCalls.Load())
			case "Consumers":
				require.Equal(t, 200, response.StatusCode)
				require.Contains(t, html, "orders-sync")
				require.NotContains(t, html, recorded.Digest)
				require.NotContains(t, html, "data-odcs-select")
				require.EqualValues(t, 1, impactCalls.Load())
			default:
				require.Equal(t, 200, response.StatusCode)
				require.Contains(t, html, "rev-one")
				require.NotContains(t, html, "orders-sync")
				require.NotContains(t, html, "Quality, SLA, governance")
				if scenario == "Supported" {
					require.Contains(t, html, `data-object="order"`)
					require.Contains(t, html, `<td>true</td>`)
				} else {
					require.Contains(t, html, "Unsupported port shape")
				}
			}
			if scenario != "Consumers" && scenario != "ProjectionInputs" {
				require.Zero(t, impactCalls.Load())
			}
		})
	}
}
