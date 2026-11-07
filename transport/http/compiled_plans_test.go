package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/richcontractfixture"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/contractplansvc"
	"github.com/stretchr/testify/require"
)

type fakeCompiledPlans struct {
	scopes     func([]contractplansvc.OperationScope)
	projection func(*odcs.ProjectionRequest)
	call       func(slug.Slug, slug.Slug, slug.Slug, string, slug.Slug) (*contractplansvc.Summary, error)
}

func (f fakeCompiledPlans) CompileVersion(_ context.Context, org, project, pipeline slug.Slug, hash string, environment slug.Slug, projection *odcs.ProjectionRequest, scopes []contractplansvc.OperationScope) (*contractplansvc.Summary, error) {
	if f.scopes != nil {
		f.scopes(scopes)
	}
	if f.projection != nil {
		f.projection(projection)
	}
	return f.call(org, project, pipeline, hash, environment)
}

func (f fakeCompiledPlans) Get(_ context.Context, org, project, pipeline slug.Slug, digest string) (*contractplansvc.Summary, error) {
	return f.call(org, project, pipeline, digest, "")
}

func compiledSnapshot(t *testing.T) *contractplansvc.Summary {
	t.Helper()
	return &contractplansvc.Summary{PipelineSlug: "sync", Digest: "sha256:" + strings.Repeat("a", 64), SpecHash: strings.Repeat("b", 64), Status: "available", AdmissionEnabled: true, Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/retained.json"}, Operations: []contractplansvc.OperationIdentity{{Step: "fetch", Role: "query", Operation: "orders", Revision: "retained", Digest: "sha256:" + strings.Repeat("c", 64)}}}
}

func TestCompiledPlanRoutesScopePermissionsAndSafeSummary(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"compile", "retained", "missing", "forbidden", "unsupported", "invented source", "projection"} {
		t.Run(scenario, func(t *testing.T) {
			stored := compiledSnapshot(t)
			called := false
			isCompile := scenario == "compile" || scenario == "forbidden" || scenario == "invented source" || scenario == "projection"
			service := fakeCompiledPlans{call: func(org, project, pipeline slug.Slug, identity string, environment slug.Slug) (*contractplansvc.Summary, error) {
				called = true
				require.Equal(t, slug.Slug("acme"), org)
				require.Equal(t, slug.Slug("etl"), project)
				require.Equal(t, slug.Slug("sync"), pipeline)
				if isCompile {
					require.Equal(t, stored.SpecHash, identity)
					require.Equal(t, slug.Slug("dev"), environment)
				} else {
					require.Equal(t, stored.Digest, identity)
				}
				if scenario == "missing" {
					return nil, compiledplans.ErrNotFound
				}
				if scenario == "unsupported" {
					stored.Status = "unsupported"
				}
				stored.AdmissionEnabled = true
				return stored, nil
			}}
			if scenario == "projection" {
				service.projection = func(projection *odcs.ProjectionRequest) {
					require.Len(t, projection.Inputs, 1)
					require.Empty(t, projection.Inputs[0].Document)
					require.Equal(t, "orders", projection.Inputs[0].Contract)
					require.Equal(t, "mapping/v1", projection.Inputs[0].Mapping.ID)
					require.Equal(t, "private-resolution", projection.Inputs[0].Variables["TABLE"].Value)
				}
			}
			app, store := newTestApp(t, testServerConfig{compiledPlansService: service, authService: fakeAuthService{CanFn: func(_ context.Context, _ authorization.Input) (bool, error) { return scenario != "forbidden", nil }}})
			method, path := http.MethodGet, "/organizations/acme/projects/etl/pipelines/sync/compiled-plans/"+stored.Digest
			if isCompile {
				method, path = http.MethodPost, "/organizations/acme/projects/etl/pipelines/sync/versions/"+stored.SpecHash+"/compiled-plans?environment_slug=dev"
			}
			var body any
			if scenario == "invented source" {
				body = map[string]any{"source": map[string]string{"commit": strings.Repeat("c", 40), "path": "invented.json"}}
			}
			if scenario == "projection" {
				body = map[string]any{"projection": map[string]any{"inputs": []any{map[string]any{"contract_slug": "orders", "revision_slug": "v1", "schema_object_id": "row", "digest": "sha256:" + strings.Repeat("d", 64), "mapping": map[string]any{"id": "mapping/v1", "representation": "json", "root_shape": "object", "types": []string{"object", "integer"}}, "variables": map[string]any{"TABLE": map[string]string{"value": "private-resolution", "revision": "v1"}}}}}}
			}
			response := authenticatedRequest(t, app, store, "test-user", jsonRequest(t, method, path, body))
			defer func() { _ = response.Body.Close() }()
			require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
			switch scenario {
			case "forbidden":
				require.Equal(t, http.StatusForbidden, response.StatusCode)
				require.False(t, called)
			case "invented source":
				require.Equal(t, http.StatusBadRequest, response.StatusCode)
				require.False(t, called)
			case "missing":
				require.Equal(t, http.StatusNotFound, response.StatusCode)
			default:
				require.Equal(t, http.StatusOK, response.StatusCode)
				raw, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				for _, absent := range []string{"sensitive-body", "sensitive-default", "authored", "parameters", "statement", "materialization", "private-resolution"} {
					require.NotContains(t, string(raw), absent)
				}
				var summary api.PipelineCompiledPlan
				require.NoError(t, json.Unmarshal(raw, &summary))
				require.Equal(t, stored.Digest, summary.Digest)
				if scenario == "unsupported" {
					require.Equal(t, api.PipelineCompiledPlanStatusUnsupported, summary.Status)
				} else {
					require.Equal(t, api.PipelineCompiledPlanStatusAvailable, summary.Status)
					require.Equal(t, "pipelines/retained.json", *summary.SourcePath)
					require.Equal(t, "retained", summary.Operations[0].Revision)
				}
			}
		})
	}
}

func TestCompiledPlanResourceScopeSelections(t *testing.T) {
	t.Parallel()
	const selection = `[{"step":"fetch","role":"query","scope":{"policy_id":"s3/relative-key/v1","data_source":"logical","operation":"orders","contract":"orders","revision":"v1","object_id":"row","representation":"rows","s3":{"endpoint":"https://storage.example","region":"test","bucket":"fixed-bucket","prefix":"imports","dynamic_key":true,"force_path_style":false}}}]`
	for _, scenario := range []string{"valid", "null", "missing prefix", "missing dynamic flag", "missing addressing flag", "unknown", "unknown nested", "latest", "bad slug", "unsupported", "endpoint credentials", "too many", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			raw := selection
			switch scenario {
			case "null":
				raw = "null"
			case "missing prefix":
				raw = strings.Replace(raw, `"prefix":"imports",`, "", 1)
			case "missing dynamic flag":
				raw = strings.Replace(raw, `"dynamic_key":true,`, "", 1)
			case "missing addressing flag":
				raw = strings.Replace(raw, `,"force_path_style":false`, "", 1)
			case "unknown":
				raw = strings.Replace(raw, `"step":`, `"credential":"private","step":`, 1)
			case "unknown nested":
				raw = strings.Replace(raw, `"endpoint":`, `"token":"private","endpoint":`, 1)
			case "latest":
				raw = strings.Replace(raw, `"v1"`, `"latest"`, 1)
			case "bad slug":
				raw = strings.Replace(raw, `"logical"`, `"INVALID SOURCE"`, 1)
			case "unsupported":
				raw = strings.Replace(raw, `"s3/relative-key/v1"`, `"other"`, 1)
			case "endpoint credentials":
				raw = strings.Replace(raw, `https://storage.example`, `https://user:secret@storage.example`, 1)
			case "too many":
				raw = "[" + strings.Repeat(selection[1:len(selection)-1]+",", 128) + selection[1:len(selection)-1] + "]"
			}
			called := false
			stored := compiledSnapshot(t)
			service := fakeCompiledPlans{scopes: func(scopes []contractplansvc.OperationScope) {
				require.Len(t, scopes, 1)
				require.Equal(t, "logical", scopes[0].Scope.DataSource.String())
				require.True(t, scopes[0].Scope.S3.DynamicKey)
			}, call: func(org, project, pipeline slug.Slug, hash string, env slug.Slug) (*contractplansvc.Summary, error) {
				called = true
				stored.Operations[0].ResourceScope = &resourcescope.Identity{PolicyID: "s3/relative-key/v1", DataSource: "logical", Operation: "orders", Contract: "orders", Revision: "v1", ObjectID: "row", Representation: "rows"}
				return stored, nil
			}}
			app, store := newTestApp(t, testServerConfig{compiledPlansService: service, authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return scenario != "denied", nil }}})
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme/projects/etl/pipelines/sync/versions/"+stored.SpecHash+"/compiled-plans?environment_slug=dev", strings.NewReader(`{"resource_scopes":`+raw+`}`))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			response := authenticatedRequest(t, app, store, "test-user", request)
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			if scenario == "valid" {
				require.Equal(t, http.StatusOK, response.StatusCode, string(body))
				require.True(t, called)
				var summary api.PipelineCompiledPlan
				require.NoError(t, json.Unmarshal(body, &summary))
				require.Equal(t, "s3/relative-key/v1", summary.Operations[0].ResourceScope.PolicyId)
				for _, absent := range []string{"storage.example", "fixed-bucket", "imports", "dynamic_key", "force_path_style"} {
					require.NotContains(t, string(body), absent)
				}
			} else {
				require.False(t, called)
				if scenario == "denied" {
					require.Equal(t, http.StatusForbidden, response.StatusCode)
				} else {
					require.Equal(t, http.StatusBadRequest, response.StatusCode, string(body))
				}
			}
		})
	}
}

func TestRichCompilationSelectionRetainsExecutablePortIdentity(t *testing.T) {
	t.Parallel()
	revision, selection, err := richcontractfixture.Selection(model.ContractRepresentationRows, model.ContractRootShapeUnknown)
	require.NoError(t, err)
	summary := compiledSnapshot(t)
	var retained *odcs.ProjectionRequest
	service := fakeCompiledPlans{projection: func(projection *odcs.ProjectionRequest) {
		require.Len(t, projection.Inputs, 2)
		for i := range projection.Inputs {
			require.Empty(t, projection.Inputs[i].Document)
			projection.Inputs[i].Document = revision.Document
		}
		retained = projection
	}, call: func(org, project, pipeline slug.Slug, identity string, environment slug.Slug) (*contractplansvc.Summary, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("etl"), project)
		projected, err := odcs.CompileProjection(*retained)
		require.NoError(t, err)
		require.Empty(t, projected.Diagnostics)
		pin, err := contractplan.CapturePort(richcontractfixture.Reference(model.ContractRepresentationRows, model.ContractRootShapeUnknown), revision, projected.Plan)
		require.NoError(t, err)
		summary.Pins = []contractplansvc.ContractPin{{Step: "fetch", Port: "output", Origin: "step", Contract: pin.ContractSlug, Revision: pin.RevisionSlug, Object: pin.SchemaObjectID, Digest: pin.Digest, Compiler: pin.CompilerID, Representation: "rows"}}
		require.Equal(t, []string{"region"}, projected.Plan.Relationships[0].Pairs[0].From.IDs)
		require.Equal(t, []string{"id"}, projected.Plan.Relationships[0].Pairs[1].From.IDs)
		return summary, nil
	}}
	inputs := []any{}
	for _, input := range selection.Inputs {
		types := []string{}
		for kind, enabled := range input.Mapping.Types {
			if enabled {
				types = append(types, kind.String())
			}
		}
		inputs = append(inputs, map[string]any{"contract_slug": input.Contract, "revision_slug": input.Revision, "schema_object_id": input.ObjectID, "digest": input.Digest, "mapping": map[string]any{"id": input.Mapping.ID, "representation": "rows", "types": types}, "variables": map[string]any{"LABEL_TYPE": map[string]string{"value": "string", "revision": "variables-v1"}}})
	}
	body := map[string]any{"projection": map[string]any{"inputs": inputs, "relationships": []any{map[string]any{"input": 0, "target": 1, "path": "$.schema[0].relationships[0]"}}}}
	app, store := newTestApp(t, testServerConfig{compiledPlansService: service, authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return true, nil }}})
	response := authenticatedRequest(t, app, store, "test-user", jsonRequest(t, http.MethodPost, "/organizations/acme/projects/etl/pipelines/sync/versions/"+summary.SpecHash+"/compiled-plans", body))
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, string(raw))
	require.Contains(t, string(raw), odcs.ProjectionCompilerID)
	require.Contains(t, string(raw), revision.Digest)
	require.NotContains(t, string(raw), "LABEL_TYPE")
	require.NotContains(t, string(raw), "variables-v1")
	require.NotContains(t, string(raw), "embedding")
}
