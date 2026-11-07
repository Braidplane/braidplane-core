package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

func TestObservationFailuresKeepTheirPublicStatusAndSafeCode(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{proofreceiptsvc.ErrObservationAuditUnavailable, http.StatusServiceUnavailable, "PROOF_OBSERVATION_AUDIT_UNAVAILABLE"},
		{proofevidencesvc.ErrAdmissionLimit, http.StatusServiceUnavailable, proofevidencesvc.ErrAdmissionLimit.Code()},
		{proofevidence.ErrAuthorizationDenied, http.StatusForbidden, proofevidence.ErrAuthorizationDenied.Code()},
	} {
		var mapped *apierror.Error
		require.True(t, xerror.As(mapProofObservationError(test.err), &mapped))
		require.Equal(t, test.status, mapped.HTTPStatus)
		require.Equal(t, test.code, mapped.Code)
		require.Empty(t, mapped.DetailedError)
	}
}

func TestObservationControlsRequireUserPermissionAndFeature(t *testing.T) {
	t.Parallel()
	for _, route := range []struct {
		method, path string
		action       model.Action
		resource     model.Resource
	}{
		{http.MethodGet, "proof-observation-policy", model.Read, model.ResourceEnvironments},
		{http.MethodPut, "proof-observation-policy", model.Update, model.ResourceEnvironments},
		{http.MethodPost, "proof-observation-previews", model.Execute, model.ResourcePipelines},
		{http.MethodPost, "proof-observations", model.Execute, model.ResourcePipelines},
		{http.MethodGet, "proof-observations/observation", model.Read, model.ResourcePipelines},
		{http.MethodPost, "proof-observations/observation/cancel", model.Execute, model.ResourcePipelines},
		{http.MethodPost, "proof-receipts", model.Execute, model.ResourcePipelines},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			for _, mode := range []string{"denied", "machine", "audit-unavailable", "disabled"} {
				t.Run(mode, func(t *testing.T) {
					calls := 0
					cfg := testServerConfig{proofObservationService: &proofreceiptsvc.LiveCompiler{}, authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						calls++
						require.Equal(t, route.action, input.Action)
						require.Equal(t, route.resource, input.Resource)
						return false, nil
					}}}
					var captured *eventssvc.CreateEventParams
					cfg.eventsService = &fakeEventsService{CreateEventFn: func(ctx context.Context, params eventssvc.CreateEventParams) (*model.Event, error) {
						deadline, ok := ctx.Deadline()
						require.True(t, ok)
						require.LessOrEqual(t, time.Until(deadline), 2*time.Second)
						captured = &params
						if mode == "audit-unavailable" {
							return nil, xerror.Unexpected().Detail("private-audit-failure")
						}
						return &model.Event{Slug: params.Slug}, nil
					}}
					if mode == "disabled" {
						cfg.proofObservationService = nil
					}
					if mode == "machine" {
						cfg.middleware = []fiber.Handler{apiKeyPrincipalMiddleware(reqctx.APIKeyPrincipal{Slug: "machine", OrgSlug: "acme", Role: "admin"})}
					}
					app, store := newTestApp(t, cfg)
					req, err := http.NewRequestWithContext(t.Context(), route.method, "/organizations/acme/projects/default/environments/prod/"+route.path, strings.NewReader(`{"intent_token":"private-denied-input"}`))
					require.NoError(t, err)
					req.Header.Set("Content-Type", "application/json")
					resp := authenticatedRequest(t, app, store, "test-user", req)
					defer func() { _ = resp.Body.Close() }()
					if mode == "disabled" {
						require.Equal(t, http.StatusNotFound, resp.StatusCode)
					} else {
						require.Equal(t, http.StatusForbidden, resp.StatusCode)
					}
					body, err := io.ReadAll(resp.Body)
					require.NoError(t, err)
					require.NotContains(t, string(body), "private-")
					if mode == "disabled" {
						require.Nil(t, captured)
					} else {
						require.NotNil(t, captured)
						require.Equal(t, model.EventClassSecurityAudit, captured.EventClass)
						require.Equal(t, model.EventActionDecline, captured.Action)
						require.Equal(t, "acme", captured.OrgSlug.String())
						require.Equal(t, map[string]any{"project_slug": "default", "environment_slug": "prod", "accepted": false, "admission_limit_reached": false, "requested_action": route.action.String(), "resource": route.resource.String()}, captured.Metadata)
						expectedActor := model.UserEventActor("test-user")
						if mode == "machine" {
							expectedActor = model.APIKeyEventActor("machine")
						}
						require.Equal(t, expectedActor.Type, captured.ActorType)
						require.Equal(t, expectedActor.Slug, captured.ActorSlug)
						require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
					}
					if mode == "denied" || mode == "audit-unavailable" {
						require.Equal(t, 1, calls)
					} else {
						require.Zero(t, calls)
					}
				})
			}
		})
	}
}

func TestObservationIntentAlsoRequiresPipelineAndDataSourceRead(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"proof-observation-previews", "proof-observations", "proof-receipts"} {
		for _, denied := range []model.Resource{model.ResourcePipelines, model.ResourceDataSources} {
			var captured *eventssvc.CreateEventParams
			app, store := newTestApp(t, testServerConfig{eventsService: &fakeEventsService{CreateEventFn: func(_ context.Context, params eventssvc.CreateEventParams) (*model.Event, error) {
				captured = &params
				return &model.Event{Slug: params.Slug}, nil
			}}, proofObservationService: &proofreceiptsvc.LiveCompiler{}, authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				return input.Action != model.Read || input.Resource != denied, nil
			}}})
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme/projects/default/environments/prod/"+path, strings.NewReader(`{"intent_token":"opaque","request_digest":"digest"}`))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			resp := authenticatedRequest(t, app, store, "test-user", req)
			require.Equal(t, http.StatusForbidden, resp.StatusCode)
			require.NoError(t, resp.Body.Close())
			require.NotNil(t, captured)
			require.Equal(t, model.Read.String(), captured.Metadata["requested_action"])
			require.Equal(t, denied.String(), captured.Metadata["resource"])
		}
	}
}

func TestProofObservationSchemaRejectsMixedOrMissingSelectors(t *testing.T) {
	t.Parallel()
	schema, err := openapi3.NewLoader().LoadFromFile("../../api-schema/schema.yml")
	require.NoError(t, err)
	target := schema.Components.Schemas["ProofObservationTarget"].Value
	for _, test := range []struct {
		value map[string]any
		valid bool
	}{
		{map[string]any{"connector_type": "hubspot", "account_id": float64(1)}, true},
		{map[string]any{"connector_type": "postgres", "host": "database.example.com", "port": float64(5432), "database": "analytics", "schema": "public", "table": "orders"}, true},
		{map[string]any{"connector_type": "mysql", "host": "database.example.com", "port": float64(3306), "database": "analytics", "schema": "analytics", "table": "orders"}, true},
		{map[string]any{"connector_type": "mongodb", "host": "database.example.com", "port": float64(27017), "database": "analytics", "collection": "orders"}, true},
		{map[string]any{"connector_type": "mongodb", "host": "database.example.com", "port": float64(27017), "database": "analytics", "collection": "orders", "table": "orders"}, false},
		{map[string]any{"connector_type": "mongodb", "host": "database.example.com", "port": float64(27017), "database": "analytics"}, false},
		{map[string]any{"connector_type": "s3", "endpoint": "", "region": "eu-west-1", "bucket": "fixture", "key": "orders.json"}, true},
		{map[string]any{"connector_type": "s3", "endpoint": "", "region": "eu-west-1", "bucket": "fixture"}, false},
		{map[string]any{"connector_type": "s3", "endpoint": "", "region": "eu-west-1", "bucket": "fixture", "key": "orders.json", "collection": "orders"}, false},
		{map[string]any{"connector_type": "postgres", "host": "database.example.com", "port": float64(5432), "database": "analytics", "schema": "public"}, false},
		{map[string]any{"connector_type": "postgres", "host": "database.example.com", "port": float64(65536), "database": "analytics", "schema": "public", "table": "orders"}, false},
		{map[string]any{"connector_type": "postgres", "host": "database.example.com", "port": float64(5432), "database": "analytics", "schema": "public", "table": "orders", "account_id": float64(1)}, false},
		{map[string]any{"connector_type": "bigquery", "project_id": "project", "dataset_id": "dataset", "table_id": "table"}, true},
		{map[string]any{"connector_type": "google_workspace", "resource": "spreadsheet", "spreadsheet_id": "book", "worksheet_id": float64(0), "range": "A1:B2"}, true},
		{map[string]any{"connector_type": "google_workspace", "resource": "file", "file_id": "folder", "operation": "create"}, true},
		{map[string]any{"connector_type": "google_workspace"}, false},
		{map[string]any{"connector_type": "google_workspace", "resource": "spreadsheet", "spreadsheet_id": "book", "range": "A1:B2"}, false},
		{map[string]any{"connector_type": "google_workspace", "resource": "spreadsheet", "spreadsheet_id": "book", "worksheet_id": float64(-1), "range": "A1:B2"}, false},
		{map[string]any{"connector_type": "google_workspace", "resource": "file", "file_id": "folder"}, false},
		{map[string]any{"connector_type": "google_workspace", "resource": "file", "file_id": "folder", "operation": "create", "range": "A1:B2"}, false},
		{map[string]any{"connector_type": "google_workspace", "resource": "file", "file_id": "folder", "operation": "create", "account_id": float64(1)}, false},
		{map[string]any{"connector_type": "hubspot", "account_id": float64(1), "resource": "file"}, false},
		{map[string]any{"connector_type": "bigquery", "project_id": "p", "dataset_id": "d", "table_id": "t", "file_id": "file"}, false},
		{map[string]any{"connector_type": "hubspot"}, false},
		{map[string]any{"connector_type": "hubspot", "account_id": float64(1), "table_id": "table"}, false},
		{map[string]any{"connector_type": "bigquery", "project_id": "project", "dataset_id": "dataset"}, false},
		{map[string]any{"connector_type": "bigquery", "project_id": "project", "dataset_id": "dataset", "table_id": "table", "account_id": float64(1)}, false},
	} {
		err := target.VisitJSON(test.value)
		if test.valid {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}

func TestWorkspaceObservationTargetPlans(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, target, operation string
		resource                any
		routes                  int
	}{
		{"Worksheet", `{"connector_type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"selected-book","worksheet_id":0,"range":"B2:C3"}`, "google_workspace.worksheet_metadata", map[string]any{"spreadsheet_id": "selected-book", "worksheet_id": float64(0), "range": "B2:C3"}, 2},
		{"DocExport", `{"connector_type":"google_workspace","resource":"document","file_id":"doc","operation":"export"}`, "google_workspace.file_metadata", map[string]any{"file_id": "doc", "operation": "export", "resource": "document"}, 1},
		{"SlidesUpdate", `{"connector_type":"google_workspace","resource":"presentation","file_id":"slides","operation":"update"}`, "google_workspace.file_metadata", map[string]any{"file_id": "slides", "operation": "update", "resource": "presentation"}, 1},
		{"FileRead", `{"connector_type":"google_workspace","resource":"file","file_id":"selected-file","operation":"read"}`, "google_workspace.file_metadata", map[string]any{"file_id": "selected-file", "operation": "read"}, 1},
		{"FileUpdate", `{"connector_type":"google_workspace","resource":"file","file_id":"selected-file","operation":"update"}`, "google_workspace.file_metadata", map[string]any{"file_id": "selected-file", "operation": "update"}, 1},
		{"FolderCreate", `{"connector_type":"google_workspace","resource":"file","file_id":"selected-folder","operation":"create"}`, "google_workspace.file_metadata", map[string]any{"file_id": "selected-folder", "operation": "create"}, 1},
		{"FolderList", `{"connector_type":"google_workspace","resource":"file","file_id":"selected-folder","operation":"list"}`, "google_workspace.file_metadata", map[string]any{"file_id": "selected-folder", "operation": "list"}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var target api.ProofObservationTarget
			require.NoError(t, json.Unmarshal([]byte(test.target), &target))
			plan, err := proofObservationTargetPlan(target)
			require.NoError(t, err)
			require.Equal(t, model.DataSourceTypeGoogleWorkspace, plan.ConnectorType)
			require.Equal(t, test.operation, plan.Operation)
			var resource any
			require.NoError(t, json.Unmarshal(plan.Resource, &resource))
			require.Equal(t, test.resource, resource)
			require.Len(t, plan.Routes, test.routes)
			for _, route := range plan.Routes {
				require.Equal(t, http.MethodGet, route.Method)
				require.NotContains(t, route.URL, "/values")
				require.NotContains(t, route.URL, "alt=media")
				require.NotContains(t, route.URL, "/upload/")
			}
		})
	}
}

func TestObservationControlsRejectMalformedInputsBeforeServiceAccess(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, method, path, body string
	}{
		{"PolicyJSON", http.MethodPut, "proof-observation-policy", `{`},
		{"PolicyLimits", http.MethodPut, "proof-observation-policy", `{"enabled":true,"timeout_seconds":0}`},
		{"PreviewJSON", http.MethodPost, "proof-observation-previews", `{`},
		{"CandidateSlug", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"bad slug","step_slug":"step"}`},
		{"StepSlug", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"bad slug"}`},
		{"Unsupported", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"postgres"}}`},
		{"HubSpotSelector", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"hubspot"}}`},
		{"HubSpotIdentity", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"hubspot","account_id":-1}}`},
		{"BigQuerySelector", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"bigquery"}}`},
		{"BigQueryIdentity", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"bigquery","project_id":"","dataset_id":"dataset","table_id":"table"}}`},
		{"WorkspaceResource", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"google_workspace"}}`},
		{"WorkspaceUnknownResource", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"google_workspace","resource":"document"}}`},
		{"WorkspaceSheetMissing", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","range":"A1:B2"}}`},
		{"WorkspaceSheetRange", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","worksheet_id":0,"range":"Sheet1!A1:B2"}}`},
		{"WorkspaceSheetNegative", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","worksheet_id":-1,"range":"A1:B2"}}`},
		{"WorkspaceSheetMixed", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","worksheet_id":0,"range":"A1:B2","file_id":"file"}}`},
		{"WorkspaceFileMissing", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"google_workspace","resource":"file","file_id":"file"}}`},
		{"WorkspaceFileInvalid", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"google_workspace","resource":"file","file_id":"file/other","operation":"read"}}`},
		{"WorkspaceFileOperation", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"google_workspace","resource":"file","file_id":"file","operation":"delete"}}`},
		{"WorkspaceFileMixed", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"google_workspace","resource":"file","file_id":"file","operation":"read","worksheet_id":0}}`},
		{"WorkspaceUnrelated", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"google_workspace","resource":"file","file_id":"file","operation":"read","account_id":1}}`},
		{"HubSpotWorkspaceMixed", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"hubspot","account_id":1,"file_id":"file"}}`},
		{"BigQueryWorkspaceMixed", http.MethodPost, "proof-observation-previews", `{"candidate_slug":"candidate","step_slug":"step","target":{"connector_type":"bigquery","project_id":"p","dataset_id":"d","table_id":"t","range":"A1:B2"}}`},
		{"ConfirmJSON", http.MethodPost, "proof-observations", `{`},
		{"StatusSlug", http.MethodGet, "proof-observations/bad%20slug", ""},
		{"CancelSlug", http.MethodPost, "proof-observations/bad%20slug/cancel", ""},
		{"ReceiptJSON", http.MethodPost, "proof-receipts", `{`},
		{"ReceiptCandidate", http.MethodPost, "proof-receipts", `{"candidate_slug":"bad slug","observation_slugs":["job"]}`},
		{"ReceiptJob", http.MethodPost, "proof-receipts", `{"candidate_slug":"candidate","observation_slugs":["bad slug"]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, store := newTestApp(t, testServerConfig{proofObservationService: &proofreceiptsvc.LiveCompiler{}, authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return true, nil }}})
			req, err := http.NewRequestWithContext(t.Context(), test.method, "/organizations/acme/projects/default/environments/prod/"+test.path, strings.NewReader(test.body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			response := authenticatedRequest(t, app, store, "test-user", req)
			defer func() { require.NoError(t, response.Body.Close()) }()
			require.Equal(t, http.StatusBadRequest, response.StatusCode)
			require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		})
	}
}

func TestNativeObservationTargetsRetainConnectionAndResourceIdentity(t *testing.T) {
	for _, kind := range []string{"postgres", "mysql"} {
		raw := `{"connector_type":"` + kind + `","host":"database.example.com","port":5432,"database":"analytics","schema":"analytics","table":"orders"}`
		var target api.ProofObservationTarget
		require.NoError(t, json.Unmarshal([]byte(raw), &target))
		plan, err := proofObservationTargetPlan(target)
		require.NoError(t, err)
		require.Equal(t, kind+".table_metadata", plan.Operation)
		require.Equal(t, kind, plan.Target.Scheme)
		require.Equal(t, "database.example.com", plan.Target.Host)
		require.Len(t, plan.Routes, 1)
	}
}
