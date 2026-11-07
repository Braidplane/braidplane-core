package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	drift "github.com/Braidplane/braidplane-core/pkg/pipeline/drift"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/odcscontractsvc"
	"github.com/stretchr/testify/require"
)

type fakeODCSService struct {
	inspect func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (*odcscontractsvc.Inspection, error)
	impact  func(context.Context, slug.Slug, slug.Slug, slug.Slug, odcscontractsvc.RevisionImpactRequest) (*odcscontractsvc.RevisionImpact, error)
	odcscontractsvc.Service
	create   func(context.Context, slug.Slug, slug.Slug, model.NewODCSContract) (*model.ODCSContract, error)
	revision func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, json.RawMessage) (*model.ODCSRevision, error)
	list     func(context.Context, slug.Slug, slug.Slug, model.ListRequest) (model.ListResponse[model.ODCSContract], error)
}

func (f fakeODCSService) InspectRevision(ctx context.Context, org, project, contract, revision slug.Slug, object string) (*odcscontractsvc.Inspection, error) {
	return f.inspect(ctx, org, project, contract, revision, object)
}

func (f fakeODCSService) CreateContract(ctx context.Context, org, project slug.Slug, input model.NewODCSContract) (*model.ODCSContract, error) {
	return f.create(ctx, org, project, input)
}

func (f fakeODCSService) CreateRevision(ctx context.Context, org, project, identity, revision slug.Slug, raw json.RawMessage) (*model.ODCSRevision, error) {
	return f.revision(ctx, org, project, identity, revision, raw)
}

func (f fakeODCSService) ListContracts(ctx context.Context, org, project slug.Slug, req model.ListRequest) (model.ListResponse[model.ODCSContract], error) {
	return f.list(ctx, org, project, req)
}

func TestODCSPermissions(t *testing.T) {
	base := "/organizations/acme/projects/etl/contracts"
	for _, tc := range []struct {
		method, path string
		action       model.Action
	}{
		{http.MethodGet, base, model.Read},
		{http.MethodPost, base + "/import", model.Create},
		{http.MethodGet, base + "/orders/revisions/v1/artifact?source_repo=example&commit=abc&path=contracts/a.yaml", model.Read},
		{http.MethodPost, base, model.Create},
		{http.MethodGet, base + "/orders", model.Read},
		{http.MethodPut, base + "/orders", model.Update},
		{http.MethodGet, base + "/orders/revisions", model.Read},
		{http.MethodPost, base + "/orders/revisions", model.Create},
		{http.MethodGet, base + "/orders/revisions/v1", model.Read},
		{http.MethodGet, base + "/orders/revisions/v1/inspection?schema_object_id=order", model.Read},
		{http.MethodPost, base + "/orders/impact", model.Read},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			app, store := newTestApp(t, testServerConfig{authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.Equal(t, model.ResourceProjects, input.Resource)
				require.Equal(t, tc.action, input.Action)
				return false, nil
			}}})
			response := authenticatedRequest(t, app, store, slug.Slug("johndoe"), jsonRequest(t, tc.method, tc.path, nil))
			t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
			require.Equal(t, http.StatusForbidden, response.StatusCode)
		})
	}
}

func TestODCSCreateAndSafeFailures(t *testing.T) {
	base := "/organizations/acme/projects/etl/contracts"
	for _, tc := range []struct {
		name, path, body string
		status           int
		serviceErr       error
	}{
		{"generated", base, `{"name":"Orders","odcs_id":"orders"}`, 201, nil},
		{"empty explicit slug", base, `{"slug":"","name":"Orders","odcs_id":"orders"}`, 400, nil},
		{"created", base, `{"slug":"orders","name":"Orders","odcs_id":"orders"}`, 201, nil},
		{"duplicate", base, `{"slug":"orders","name":"Orders","odcs_id":"orders"}`, 409, odcscontracts.ErrConflict},
		{"wrong project", base, `{"slug":"orders","name":"Orders","odcs_id":"orders"}`, 404, odcscontracts.ErrNotFound},
		{"invalid slug", base, `{"slug":"INVALID","name":"Orders","odcs_id":"orders"}`, 400, nil},
		{"duplicate key", base, `{"slug":"orders","slug":"other","name":"Orders","odcs_id":"orders"}`, 400, nil},
		{"unknown metadata", base, `{"slug":"orders","name":"Orders","odcs_id":"orders","extra":"private-value"}`, 400, nil},
		{"trailing value", base, `{"slug":"orders","name":"Orders","odcs_id":"orders"} {}`, 400, nil},
		{"invalid project path", "/organizations/acme/projects/INVALID/contracts", `{}`, 400, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, store := newTestApp(t, testServerConfig{odcsContractsService: fakeODCSService{create: func(_ context.Context, org, project slug.Slug, input model.NewODCSContract) (*model.ODCSContract, error) {
				require.Equal(t, slug.Slug("acme"), org)
				require.Equal(t, slug.Slug("etl"), project)
				return &model.ODCSContract{Slug: input.Slug, Name: input.Name, ODCSID: input.ODCSID, Status: "active"}, tc.serviceErr
			}}})
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, tc.path, strings.NewReader(tc.body))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			response := authenticatedRequest(t, app, store, slug.Slug("johndoe"), request)
			defer func() { require.NoError(t, response.Body.Close()) }()
			require.Equal(t, tc.status, response.StatusCode)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NotContains(t, string(body), "private-value")
		})
	}
}

func TestODCSRevisionRoundTrip(t *testing.T) {
	app, store := newTestApp(t, testServerConfig{odcsContractsService: fakeODCSService{revision: func(_ context.Context, org, project, identity, revisionSlug slug.Slug, raw json.RawMessage) (*model.ODCSRevision, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("etl"), project)
		require.Equal(t, slug.Slug("orders"), identity)
		revision, err := odcs.Record(raw, "orders")
		if err != nil {
			return nil, err
		}
		revision.Slug = revisionSlug
		return revision, nil
	}}})
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme/projects/etl/contracts/orders/revisions", strings.NewReader(`{"slug":"v1","document":{"kind":"DataContract","apiVersion":"v3.2.0","id":"orders","version":"1","schema":[{"id":"orders","name":"Orders","properties":[{"name":"amount","enum":[{"value":9007199254740993.30}]}]}]}}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response := authenticatedRequest(t, app, store, slug.Slug("johndoe"), request)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, 201, response.StatusCode)
	var actual api.ODCSRevision
	require.NoError(t, json.NewDecoder(response.Body).Decode(&actual))
	require.Contains(t, string(actual.Document), "9007199254740993.30")
	require.Equal(t, api.ODCSRevisionStatusRecorded, actual.Status)
	require.Len(t, actual.Digest, 71)
}

func TestODCSListPaginationMapping(t *testing.T) {
	app, store := newTestApp(t, testServerConfig{odcsContractsService: fakeODCSService{list: func(_ context.Context, _, _ slug.Slug, req model.ListRequest) (model.ListResponse[model.ODCSContract], error) {
		require.Equal(t, 1, req.Limit)
		require.Equal(t, "asc", req.Order)
		require.Equal(t, "prev", req.Direction)
		require.Equal(t, "cursor", req.Cursor)
		next := "next"
		return model.ListResponse[model.ODCSContract]{Items: []model.ODCSContract{}, HasMore: true, NextCursor: &next}, nil
	}}})
	response := authenticatedRequest(t, app, store, slug.Slug("johndoe"), jsonRequest(t, http.MethodGet, "/organizations/acme/projects/etl/contracts?limit=1&order=asc&direction=prev&cursor=cursor", nil))
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, 200, response.StatusCode)
	var actual api.ODCSContractListResponse
	require.NoError(t, json.NewDecoder(response.Body).Decode(&actual))
	require.Empty(t, actual.Contracts)
	require.True(t, actual.HasMore)
	require.Equal(t, "next", *actual.NextCursor)
}

func (f fakeODCSService) PreviewRevisionImpact(ctx context.Context, org, project, contract slug.Slug, request odcscontractsvc.RevisionImpactRequest) (*odcscontractsvc.RevisionImpact, error) {
	return f.impact(ctx, org, project, contract, request)
}

func TestODCSImpactPreviewValidatesRequestAndMapsIdentities(t *testing.T) {
	calls := 0
	app, store := newTestApp(t, testServerConfig{odcsContractsService: fakeODCSService{impact: func(_ context.Context, org, project, contract slug.Slug, request odcscontractsvc.RevisionImpactRequest) (*odcscontractsvc.RevisionImpact, error) {
		calls++
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("etl"), project)
		require.Equal(t, slug.Slug("orders"), contract)
		require.Equal(t, slug.Slug("v1"), request.From)
		require.Equal(t, slug.Slug("v2"), request.To)
		return &odcscontractsvc.RevisionImpact{FromDigest: "sha256:old", ToDigest: "sha256:new", Objects: []odcscontractsvc.ObjectImpact{{SchemaObjectID: "order", Comparison: "supported", Changes: []odcscontractsvc.ImpactChange{{Change: drift.Change{Kind: "type_change", Path: "schema_object[order].id.type", Before: "integer", After: "string", Severity: drift.ReportSeverityBreaking}, BeforePropertyID: "order-id", AfterPropertyID: "order-id"}}}}}, nil
	}}})
	for _, body := range []string{`{"from_revision_slug":"v1","to_revision_slug":"v2"}`, `{"from_revision_slug":"v1","to_revision_slug":"bad slug"}`, `{"from_revision_slug":"v1","to_revision_slug":"v2","extra":true}`, `{"from_revision_slug":"v1","from_revision_slug":"v2","to_revision_slug":"v2"}`} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme/projects/etl/contracts/orders/impact", strings.NewReader(body))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		response := authenticatedRequest(t, app, store, "johndoe", request)
		if calls == 1 && body == `{"from_revision_slug":"v1","to_revision_slug":"v2"}` {
			require.Equal(t, 200, response.StatusCode)
			var result api.ODCSRevisionImpact
			require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
			require.Equal(t, "unknown", string(result.ExternalConsumers))
			require.Len(t, result.Objects, 1)
			require.Equal(t, "order-id", *result.Objects[0].Changes[0].BeforePropertyId)
			require.Equal(t, api.DriftImpactReportSeverityBreaking, result.Objects[0].Changes[0].Severity)
		} else {
			require.Equal(t, 400, response.StatusCode)
		}
		require.NoError(t, response.Body.Close())
	}
	require.Equal(t, 1, calls)
}

func TestODCSSelectedImpactRequiresPipelineReadAndPreservesPlanSelection(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		calls := 0
		app, store := newTestApp(t, testServerConfig{
			authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				return input.Resource == model.ResourceProjects || allowed, nil
			}},
			odcsContractsService: fakeODCSService{impact: func(_ context.Context, org, project, contract slug.Slug, input odcscontractsvc.RevisionImpactRequest) (*odcscontractsvc.RevisionImpact, error) {
				calls++
				require.Equal(t, slug.Slug("sync"), input.FromPlan.Pipeline)
				require.Equal(t, "sha256:"+strings.Repeat("a", 64), input.FromPlan.Digest)
				return &odcscontractsvc.RevisionImpact{FromPlan: input.FromPlan, ToPlan: input.ToPlan, Consumers: []odcscontractsvc.ConsumerImpact{{Port: "input:orders"}}}, nil
			}},
		})
		body := map[string]any{"from_revision_slug": "v1", "to_revision_slug": "v2", "from_plan": map[string]string{"pipeline_slug": "sync", "digest": "sha256:" + strings.Repeat("a", 64)}, "to_plan": map[string]string{"pipeline_slug": "sync", "digest": "sha256:" + strings.Repeat("b", 64)}}
		response := authenticatedRequest(t, app, store, "johndoe", jsonRequest(t, http.MethodPost, "/organizations/acme/projects/etl/contracts/orders/impact", body))
		if allowed {
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
			var result api.ODCSRevisionImpact
			require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
			require.Equal(t, "sync", result.FromPlan.PipelineSlug)
			require.Equal(t, "input:orders", result.Consumers[0].Port)
			require.Equal(t, 1, calls)
		} else {
			require.Equal(t, http.StatusForbidden, response.StatusCode)
			require.Zero(t, calls)
		}
		require.NoError(t, response.Body.Close())
		delete(body, "to_plan")
		response = authenticatedRequest(t, app, store, "johndoe", jsonRequest(t, http.MethodPost, "/organizations/acme/projects/etl/contracts/orders/impact", body))
		require.Equal(t, http.StatusBadRequest, response.StatusCode)
		require.NoError(t, response.Body.Close())
	}
}

func TestODCSInspectionScopeAndCoverage(t *testing.T) {
	app, store := newTestApp(t, testServerConfig{odcsContractsService: fakeODCSService{inspect: func(_ context.Context, org, project, contract, revision slug.Slug, object string) (*odcscontractsvc.Inspection, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("etl"), project)
		require.Equal(t, slug.Slug("orders"), contract)
		require.Equal(t, slug.Slug("v1"), revision)
		require.Equal(t, "order", object)
		if project != "etl" {
			return nil, odcscontracts.ErrNotFound
		}
		return &odcscontractsvc.Inspection{Revision: &model.ODCSRevision{Slug: revision, Document: json.RawMessage(`{"description":"<script>authored text</script>","schema":[]}`)}, Projection: &odcs.ConversionResult{CompilerID: odcs.CompilerID, CoverageComplete: true, DocumentValid: true, SchemaObjectID: object, Coverage: []model.ODCSDeclarationCoverage{{Path: "$.quality", State: "unsupported", Reason: "no_runtime_evaluator", Applicability: "document"}}}}, nil
	}}})
	response := authenticatedRequest(t, app, store, "johndoe", jsonRequest(t, http.MethodGet, "/organizations/acme/projects/etl/contracts/orders/revisions/v1/inspection?schema_object_id=order", nil))
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, 200, response.StatusCode)
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	var result api.ODCSRevisionInspection
	require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
	require.True(t, result.CoverageComplete)
	require.False(t, result.ProjectionAvailable)
	require.Nil(t, result.Shape)
	require.Equal(t, api.ODCSDeclarationCoverageStateUnsupported, result.Coverage[0].State)
	require.Contains(t, string(result.Revision.Document), "authored text")
}

func TestODCSInspectionExposesExactSupportedMappingShape(t *testing.T) {
	p, s := 18, 2
	shape := &payload.Contract{Name: "product", Fields: []payload.ContractField{
		{Name: "sku", Type: model.ContractFieldTypeValueString, Required: true},
		{
			Name: "unit_price", Type: model.ContractFieldTypeValueDecimal, Precision: &p, Scale: &s, Required: true,
			Validations: &payload.ContractFieldValidations{Min: json.Number("0.01")},
		},
	}}
	app, store := newTestApp(t, testServerConfig{odcsContractsService: fakeODCSService{inspect: func(_ context.Context, org, project, contract, revision slug.Slug, object string) (*odcscontractsvc.Inspection, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("etl"), project)
		require.Equal(t, slug.Slug("catalog"), contract)
		require.Equal(t, slug.Slug("v1"), revision)
		require.Equal(t, "product", object)
		return &odcscontractsvc.Inspection{
			Revision:   &model.ODCSRevision{Slug: revision, Digest: "sha256:exact-revision", Document: json.RawMessage(`{}`)},
			Projection: &odcs.ConversionResult{CompilerID: odcs.DecimalCompilerID, SchemaObjectID: object, DocumentValid: true, Shape: shape},
		}, nil
	}}})
	response := authenticatedRequest(t, app, store, "johndoe", jsonRequest(t, http.MethodGet,
		"/organizations/acme/projects/etl/contracts/catalog/revisions/v1/inspection?schema_object_id=product", nil))
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var result api.ODCSRevisionInspection
	require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
	require.True(t, result.ProjectionAvailable)
	require.Equal(t, "sha256:exact-revision", result.Revision.Digest)
	require.Equal(t, odcs.DecimalCompilerID, result.CompilerId)
	require.NotNil(t, result.Shape)
	require.Nil(t, result.Shape.Representation, "wire representation is selected by the pipeline reference")
	require.Equal(t, pipelineSchemaContractToAPI(shape), result.Shape)
	require.True(t, (*result.Shape.Fields)[0].Required)
	require.Equal(t, &p, (*result.Shape.Fields)[1].Precision)
	require.NotNil(t, (*result.Shape.Fields)[1].Validations.Min)
}

func TestODCSInspectionRequiresAuthentication(t *testing.T) {
	app, _ := newTestApp(t, testServerConfig{})
	response, err := app.Test(jsonRequest(t, http.MethodGet, "/organizations/acme/projects/etl/contracts/orders/revisions/v1/inspection", nil))
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
}
