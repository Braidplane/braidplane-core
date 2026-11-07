package pipelinesvc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	drift "github.com/Braidplane/braidplane-core/pkg/pipeline/drift"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/richcontractfixture"
	"github.com/Braidplane/braidplane-core/pkg/testutil/supplierfixture"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/stretchr/testify/require"
)

const testODCSDocument = `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","status":"active","schema":[{"id":"order","name":"Orders","properties":[{"name":"id","logicalType":"integer","required":true}]}]}`

type fakeODCSRevisionRepository struct {
	odcscontracts.Repository
	get func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.ODCSRevision, error)
}

func TestODCSPinBudgetRejectsAggregateWithoutPartialSnapshot(t *testing.T) {
	raw := strings.Replace(testODCSDocument, `"required":true`, `"required":true,"description":"`+strings.Repeat("x", odcs.MaxDocumentBytes/2)+`"`, 1)
	revision, err := odcs.Record([]byte(raw), "orders")
	require.NoError(t, err)
	store := &pipelineStore{txm: fakePipelineStoreTxManager{}, odcs: fakeODCSRevisionRepository{get: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.ODCSRevision, error) {
		return revision, nil
	}}}
	pipeline := testODCSQueryPipeline()
	for i := range 20 {
		step := testODCSQueryPipeline().Steps[0]
		step.Slug = slug.Slug(fmt.Sprintf("fetch-%d", i))
		pipeline.Steps = append(pipeline.Steps, step)
	}
	resolved, pins, err := store.ResolvePipelineContracts(t.Context(), "acme", "etl", pipeline)
	require.ErrorIs(t, err, ErrODCSContractReference)
	require.Nil(t, resolved)
	require.Nil(t, pins)
	var referenceError *odcsContractReferenceError
	require.True(t, xerror.As(err, &referenceError))
	require.Equal(t, "ODCS_PROJECTION_LIMIT_EXCEEDED", referenceError.diagnostics[0].Code)
}

func (r fakeODCSRevisionRepository) GetRevision(ctx context.Context, tx postgres.DBTX, org, project, contract, revision slug.Slug) (*model.ODCSRevision, error) {
	return r.get(ctx, tx, org, project, contract, revision)
}

func testODCSReference() *pipelinespec.ODCSContractRef {
	return &pipelinespec.ODCSContractRef{ContractSlug: must.Slug("orders"), RevisionSlug: must.Slug("v1"), SchemaObjectID: "order", Representation: model.ContractRepresentationRows}
}

func testODCSQueryPipeline() *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: must.Slug("orders-sync"), Name: "Orders"}, Steps: []pipelinespec.Step{{Kind: "query", Slug: must.Slug("fetch"), Query: &pipelinespec.QueryRef{DataSourceSlug: must.Slug("db"), OperationType: "sql", OperationRef: must.Slug("orders"), OutputContractRef: testODCSReference()}}}}
}

func TestResolvePipelineContractsPinsEveryPortAndEnforcesReloadedShape(t *testing.T) {
	t.Parallel()
	revision, err := odcs.Record(json.RawMessage(testODCSDocument), "orders")
	require.NoError(t, err)
	calls := 0
	store := &pipelineStore{txm: fakePipelineStoreTxManager{}, odcs: fakeODCSRevisionRepository{get: func(_ context.Context, _ postgres.DBTX, org, project, contract, selected slug.Slug) (*model.ODCSRevision, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("default"), project)
		require.Equal(t, slug.Slug("orders"), contract)
		require.Equal(t, slug.Slug("v1"), selected)
		calls++
		return revision, nil
	}}}
	authored := testODCSQueryPipeline()
	authored.Steps = append(authored.Steps,
		pipelinespec.Step{Kind: "transform", Slug: must.Slug("convert"), Transform: &pipelinespec.TransformSpec{SQL: "select id from input_rows", OutputKind: model.TransformOutputKindRows, Inputs: map[string]pipelinespec.StepInput{"input_rows": {FromStep: must.Slug("fetch")}}, InputContractRef: testODCSReference(), OutputContractRef: testODCSReference()}},
		pipelinespec.Step{Kind: "sink", Slug: must.Slug("deliver"), Sink: &pipelinespec.SinkSpec{IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent, Input: &pipelinespec.StepInput{FromStep: must.Slug("convert")}, DataSourceSlug: must.Slug("db"), OperationType: "sql", OperationRef: must.Slug("save"), InputContractRef: testODCSReference()}})
	resolved, pins, err := store.ResolvePipelineContracts(context.Background(), "acme", "default", authored)
	require.NoError(t, err)
	require.Len(t, pins, 4)
	require.Equal(t, 4, calls)
	require.NotNil(t, authored.Steps[0].Query.OutputContractRef)
	require.Nil(t, authored.Steps[0].Query.OutputContract)
	require.NoError(t, pipelinevalidate.Validate(resolved))
	require.NoError(t, pipelinevalidate.ValidateStepInputCompatibility(resolved))
	for _, pin := range pins {
		require.Equal(t, revision.Digest, pin.Digest)
		require.Equal(t, odcs.CompilerID, pin.CompilerID)
		require.NotEmpty(t, pin.Coverage)
	}
	specJSON, err := json.Marshal(authored)
	require.NoError(t, err)
	pinJSON, err := json.Marshal(pins)
	require.NoError(t, err)
	reloaded, err := pipelineFromVersion(&pipelinesrepo.PipelineVersion{SpecJSONB: specJSON, ResolvedContracts: pinJSON})
	require.NoError(t, err)
	require.Equal(t, resolved, reloaded)
	require.Equal(t, 4, calls)
	contract := reloaded.Steps[1].Transform.OutputContract
	require.NoError(t, transformsql.ValidateOutputContract(&payload.Output{OutputKind: model.TransformOutputKindRows, Rows: []map[string]any{{"id": int64(1)}}}, contract))
	require.Error(t, transformsql.ValidateOutputContract(&payload.Output{OutputKind: model.TransformOutputKindRows, Rows: []map[string]any{{"id": "wrong"}}}, contract))
}

func TestRichODCSApplyPinsCoverageAndReloadsWithoutCatalog(t *testing.T) {
	raw := strings.Replace(testODCSDocument, `"status":"active"`, `"status":"active","slaProperties":[{"property":"latency","value":1}],"team":{"name":"Operations"}`, 1)
	revision, err := odcs.Record([]byte(raw), "orders")
	require.NoError(t, err)
	store := &pipelineStore{txm: fakePipelineStoreTxManager{}, odcs: fakeODCSRevisionRepository{get: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.ODCSRevision, error) {
		return revision, nil
	}}}
	authored := testODCSQueryPipeline()
	resolved, pins, err := store.ResolvePipelineContracts(t.Context(), "acme", "etl", authored)
	require.NoError(t, err)
	again, repeatedPins, err := store.ResolvePipelineContracts(t.Context(), "acme", "etl", authored)
	require.NoError(t, err)
	require.Equal(t, resolved, again)
	require.Equal(t, pins, repeatedPins)
	require.Equal(t, odcs.CompilerID, pins[0].CompilerID)
	var unsupported bool
	for _, entry := range pins[0].Coverage {
		if entry.Path == "$.slaProperties" {
			unsupported = entry.State == "unsupported"
		}
	}
	require.True(t, unsupported)
	specJSON, err := json.Marshal(authored)
	require.NoError(t, err)
	pinJSON, err := json.Marshal(pins)
	require.NoError(t, err)
	var persisted []model.PinnedODCSPort
	require.NoError(t, json.Unmarshal(pinJSON, &persisted))
	require.Equal(t, pins, persisted)
	store.odcs = nil
	reloaded, err := pipelineFromVersion(&pipelinesrepo.PipelineVersion{SpecJSONB: specJSON, ResolvedContracts: pinJSON})
	require.NoError(t, err)
	require.Equal(t, resolved, reloaded)
	require.Error(t, payload.ValidateContractFieldValues(reloaded.Steps[0].Query.OutputContract, []map[string]any{{"id": nil}}))
}

func TestResolvePipelineContractsRejectsUnavailableOrUnsupportedObjects(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"missing revision", "missing object", "unsupported", "ambiguous", "digest mismatch", "inline and ref"} {
		t.Run(name, func(t *testing.T) {
			pipeline := testODCSQueryPipeline()
			document := json.RawMessage(testODCSDocument)
			if name == "unsupported" {
				document = json.RawMessage(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","status":"active","schema":[{"id":"order","name":"Orders","properties":[{"name":"id","logicalType":"date"}]}]}`)
			}
			if name == "ambiguous" {
				document = json.RawMessage(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","status":"active","schema":[{"id":"order","name":"Orders","properties":[{"name":"id","logicalType":"integer"},{"name":"id","logicalType":"string"}]}]}`)
			}
			revision, err := odcs.Record(document, "orders")
			require.NoError(t, err)
			if name == "missing object" {
				pipeline.Steps[0].Query.OutputContractRef.SchemaObjectID = "absent"
			}
			if name == "digest mismatch" {
				revision.Digest = "invalid"
			}
			if name == "inline and ref" {
				pipeline.Steps[0].Query.OutputContract = &payload.Contract{}
			}
			store := &pipelineStore{txm: fakePipelineStoreTxManager{}, odcs: fakeODCSRevisionRepository{get: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.ODCSRevision, error) {
				if name == "missing revision" {
					return nil, odcscontracts.ErrNotFound
				}
				return revision, nil
			}}}
			_, _, err = store.ResolvePipelineContracts(context.Background(), "acme", "default", pipeline)
			if name == "digest mismatch" {
				require.ErrorIs(t, err, xerror.Unexpected())
			} else {
				require.ErrorIs(t, err, ErrODCSContractReference)
			}
		})
	}
}

func TestPipelineFromVersionRequiresExactCompletePins(t *testing.T) {
	t.Parallel()
	pipeline := testODCSQueryPipeline()
	specJSON, err := json.Marshal(pipeline)
	require.NoError(t, err)
	_, err = pipelineFromVersion(&pipelinesrepo.PipelineVersion{SpecJSONB: specJSON})
	require.ErrorContains(t, err, "missing a pinned")
	revision, err := odcs.Record(json.RawMessage(testODCSDocument), "orders")
	require.NoError(t, err)
	conversion, err := odcs.Convert(revision.Document, "order")
	require.NoError(t, err)
	conversion.Shape.Representation = model.ContractRepresentationRows
	pin := model.PinnedODCSPort{StepSlug: "fetch", Port: "output", ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order", Digest: revision.Digest, Shape: conversion.Shape, CompilerID: conversion.CompilerID}
	_, err = pipelineFromVersion(&pipelinesrepo.PipelineVersion{SpecJSONB: specJSON, ResolvedContracts: mustJSON(t, []model.PinnedODCSPort{pin})})
	require.NoError(t, err)
	for _, name := range []string{"revision", "digest", "duplicate", "facet", "compiler", "missing compiler"} {
		t.Run(name, func(t *testing.T) {
			changed := pin
			if name == "compiler" {
				changed.CompilerID = "unsupported/future"
			}
			if name == "missing compiler" {
				changed.CompilerID = ""
			}
			if name == "revision" {
				changed.RevisionSlug = "v2"
			}
			if name == "digest" {
				changed.Digest = "invalid"
			}
			if name == "facet" {
				shapeCopy := *pin.Shape
				shapeCopy.Representation = model.ContractRepresentationCSV
				changed.Shape = &shapeCopy
			}
			pins := []model.PinnedODCSPort{changed}
			if name == "duplicate" {
				pins = append(pins, pin)
			}
			pinJSON, err := json.Marshal(pins)
			require.NoError(t, err)
			_, err = pipelineFromVersion(&pipelinesrepo.PipelineVersion{SpecJSONB: specJSON, ResolvedContracts: pinJSON})
			if name == "compiler" || name == "missing compiler" {
				require.ErrorContains(t, err, "unsupported flat compiler")
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestResolvedODCSContractsRejectIncompatibleEdgesAndMultiInput(t *testing.T) {
	t.Parallel()
	revision, err := odcs.Record(json.RawMessage(testODCSDocument), "orders")
	require.NoError(t, err)
	store := &pipelineStore{txm: fakePipelineStoreTxManager{}, odcs: fakeODCSRevisionRepository{get: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.ODCSRevision, error) {
		return revision, nil
	}}}
	for _, multipleInputs := range []bool{false, true} {
		t.Run(map[bool]string{false: "IncompatibleEdge", true: "CombinedInput"}[multipleInputs], func(t *testing.T) {
			pipeline := testODCSQueryPipeline()
			transform := &pipelinespec.TransformSpec{SQL: "select id from orders", OutputKind: model.TransformOutputKindRows, Inputs: map[string]pipelinespec.StepInput{"orders": {FromStep: "fetch"}}}
			if multipleInputs {
				pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "query", Slug: "other", Query: &pipelinespec.QueryRef{DataSourceSlug: "db", OperationType: "sql", OperationRef: "orders", OutputContractRef: testODCSReference()}})
				transform.Inputs["other"] = pipelinespec.StepInput{FromStep: "other"}
				transform.InputContractRef = testODCSReference()
			} else {
				transform.InputContract = &payload.Contract{Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueString, Required: true}}}
			}
			pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "transform", Slug: "convert", Transform: transform})
			resolved, _, err := store.ResolvePipelineContracts(context.Background(), "acme", "default", pipeline)
			require.NoError(t, err)
			if multipleInputs {
				// A relation namespace is not inferred from an ODCS object identity.
				require.ErrorContains(t, pipelinevalidate.Validate(resolved), "must be prefixed")
			} else {
				require.NoError(t, pipelinevalidate.Validate(resolved))
				require.ErrorIs(t, pipelinevalidate.ValidateStepInputCompatibility(resolved), pipelinevalidate.ErrContractMismatch)
			}
		})
	}
}

func TestODCSVersionDispatchesPinnedContractWithoutCatalog(t *testing.T) {
	t.Parallel()
	authored := testODCSQueryPipeline()
	conversion, err := odcs.Convert(json.RawMessage(testODCSDocument), "order")
	require.NoError(t, err)
	conversion.Shape.Representation = model.ContractRepresentationRows
	pins := []model.PinnedODCSPort{{StepSlug: "fetch", Port: "output", ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order", Digest: conversion.Digest, Shape: conversion.Shape, CompilerID: conversion.CompilerID}}
	version := &pipelinesrepo.PipelineVersion{ID: 42, SpecJSONB: mustJSON(t, authored), ResolvedContracts: mustJSON(t, pins)}
	stop := xerror.Unexpected().Detail("stop after observing runtime dispatch")
	runtime := &fakePipelineRuntime{executeFn: func(_ context.Context, req pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error) {
		require.Equal(t, int64(42), req.PipelineVersionID)
		require.Nil(t, req.Pipeline.Steps[0].Query.OutputContractRef)
		require.Equal(t, conversion.Shape, req.Pipeline.Steps[0].Query.OutputContract)
		return nil, stop
	}}
	svc := &service{runtime: runtime, admission: testAdmissionService()}
	_, err = svc.executeResolvedVersion(context.Background(), "acme", "default", "dev", version, nil, nil, model.SystemEventActor(), pipelinerun.RunSourceSummary{}, nil, false, nil, pipelinerun.IncrementalRunOptions{}, nil)
	require.ErrorIs(t, err, stop)
	require.Equal(t, 1, runtime.executeCalls)
}

func TestRevalidateODCSReferenceRequiresCatalogRead(t *testing.T) {
	t.Parallel()
	pipeline := testODCSQueryPipeline()
	store := &fakeServicePipelineStore{findSpecForUpdateFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
		return &pipelinesrepo.PipelineSpec{SpecJSONB: mustJSON(t, pipeline)}, nil
	}, resolveContractsFn: func(context.Context, slug.Slug, slug.Slug, *pipelinespec.Pipeline) (*pipelinespec.Pipeline, []model.PinnedODCSPort, error) {
		t.Fatal("catalog resolution must not happen without read permission")
		return nil, nil, nil
	}}
	svc := newCRUDTestService(t, store).(*service)
	svc.authorizationService = allowAuthorizationService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
		return input.Resource != model.ResourceProjects, nil
	}}
	_, err := svc.RevalidatePipeline(context.Background(), "acme", "default", "dev", ValidationHistoryPrincipal{UserSlug: "alice"}, pipeline.Metadata.Slug, "alice")
	require.ErrorIs(t, err, xerror.Forbidden(xerror.PipelineValidationResult))
}

func TestValidateODCSReferencesReturnsActionableSafeDiagnostics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, document, object, code, path string
		missing                            bool
	}{
		{name: "unsupported", document: `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","properties":[{"name":"private-value","logicalType":"date"}]}]}`, code: "ODCS_TYPE_UNSUPPORTED", path: "$.schema[0].properties[0].logicalType"},
		{name: "missing object", document: testODCSDocument, object: "absent", code: "ODCS_OBJECT_NOT_FOUND", path: "$.schema"},
		{name: "unavailable revision", missing: true, code: "ODCS_REFERENCE_UNAVAILABLE", path: "$"},
		{name: "invalid document", document: `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","properties":[{"name":"id","logicalType":"private-value"}]}]}`, code: "ODCS_SCHEMA_INVALID", path: "$.schema[0].properties[0].logicalType"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := testODCSQueryPipeline()
			if tc.object != "" {
				pipeline.Steps[0].Query.OutputContractRef.SchemaObjectID = tc.object
			}
			store := &pipelineStore{txm: fakePipelineStoreTxManager{}, odcs: fakeODCSRevisionRepository{get: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.ODCSRevision, error) {
				if tc.missing {
					return nil, odcscontracts.ErrNotFound
				}
				if tc.name == "invalid document" {
					// Revisions recorded before normative admission remain readable.
					return &model.ODCSRevision{Document: json.RawMessage(tc.document)}, nil
				}
				return odcs.Record(json.RawMessage(tc.document), "orders")
			}}}
			svc := newCRUDTestService(t, &fakeServicePipelineStore{resolveContractsFn: store.ResolvePipelineContracts})
			result, err := svc.Validate(context.Background(), "acme", "default", "dev", "alice", pipeline)
			require.NoError(t, err)
			require.False(t, result.Valid)
			require.NotEmpty(t, result.Diagnostics)
			require.Equal(t, tc.code, result.Diagnostics[0].Code)
			require.Equal(t, "steps[0].query.output_contract_ref", result.Diagnostics[0].Field)
			require.Equal(t, "fetch", *result.Diagnostics[0].StepSlug)
			require.Contains(t, result.Diagnostics[0].Message, tc.path+": ")
			for _, finding := range result.Diagnostics {
				require.NotContains(t, finding.Message, "private-value")
			}
		})
	}
}

func TestODCSConversionDiagnosticsIdentifyEveryPort(t *testing.T) {
	t.Parallel()
	p := testODCSQueryPipeline()
	p.Steps = append(p.Steps,
		pipelinespec.Step{Kind: "transform", Slug: "convert", Transform: &pipelinespec.TransformSpec{}},
		pipelinespec.Step{Kind: "sink", Slug: "deliver", Sink: &pipelinespec.SinkSpec{}},
	)
	want := []string{"steps[0].query.output_contract_ref", "steps[1].transform.input_contract_ref", "steps[1].transform.output_contract_ref", "steps[2].sink.input_contract_ref"}
	for i, port := range pipelineODCSPorts(p) {
		err := port.referenceError([]odcs.Diagnostic{{Code: "mapping_unsupported", Path: "$.tenant", Message: "Use the executable subset."}, {Code: "type_unsupported", Path: "$.schema[0].properties[0].logicalType", Message: "Use a supported type."}})
		require.ErrorIs(t, err, ErrODCSContractReference)
		var referenceError *odcsContractReferenceError
		require.True(t, xerror.As(err, &referenceError))
		require.Len(t, referenceError.diagnostics, 2)
		for _, diagnostic := range referenceError.diagnostics {
			require.Equal(t, want[i], diagnostic.Field)
			require.Equal(t, port.step.String(), *diagnostic.StepSlug)
		}
	}
}

func TestContractImpactComparesResolvedCandidateWithPinnedCurrentShape(t *testing.T) {
	current := testODCSQueryPipeline()
	revision, err := odcs.Record([]byte(testODCSDocument), "orders")
	require.NoError(t, err)
	conversion, err := odcs.Convert(revision.Document, "order")
	require.NoError(t, err)
	conversion.Shape.Representation = model.ContractRepresentationRows
	raw, err := json.Marshal(current)
	require.NoError(t, err)
	pins, err := json.Marshal([]model.PinnedODCSPort{{StepSlug: "fetch", Port: "output", ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order", Digest: revision.Digest, Shape: conversion.Shape, CompilerID: conversion.CompilerID}})
	require.NoError(t, err)
	candidate := testODCSQueryPipeline()
	candidate.Steps[0].Query.OutputContractRef = nil
	candidate.Steps[0].Query.OutputContract = &payload.Contract{Name: "order", Representation: model.ContractRepresentationRows, Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueString, Required: true}}}
	store := &fakeServicePipelineStore{
		getFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinespec.Pipeline, error) {
			return current, nil
		},
		findSpecFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
			return &pipelinesrepo.PipelineSpec{SpecHash: "current"}, nil
		},
		findVersionBySpecHashFn: func(_ context.Context, org, project, pipeline slug.Slug, hash string) (*pipelinesrepo.PipelineVersion, error) {
			require.Equal(t, "current", hash)
			return &pipelinesrepo.PipelineVersion{SpecJSONB: raw, ResolvedContracts: pins}, nil
		},
	}
	svc := &service{pipelineStore: store}
	report, hasReport, err := contractImpactReport(svc, t.Context(), "acme", "etl", candidate)
	require.NoError(t, err)
	require.True(t, hasReport)
	require.Equal(t, drift.ReportSeverityBreaking, report.Severity)
	require.Equal(t, "step[fetch].query.output_contract.id.type", report.Changes[0].Path)
	store.getFn = func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinespec.Pipeline, error) {
		return pipelineFromVersion(&pipelinesrepo.PipelineVersion{SpecJSONB: raw, ResolvedContracts: pins})
	}
	inline, hasReport, err := contractImpactReport(svc, t.Context(), "acme", "etl", candidate)
	require.NoError(t, err)
	require.True(t, hasReport)
	require.Equal(t, report, inline)
	store.getFn = func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinespec.Pipeline, error) {
		return current, nil
	}
	store.resolveContractsFn = func(context.Context, slug.Slug, slug.Slug, *pipelinespec.Pipeline) (*pipelinespec.Pipeline, []model.PinnedODCSPort, error) {
		return candidate, nil, nil
	}
	validationService := newValidationTestService(t, map[slug.Slug]datasourcessvc.SchemaResult{
		"db": datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{SchemaTypeValue: datasourcessvc.SchemaTypeSQL, Type: "postgres", Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{Name: "orders", Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "text"}}}}}},
	}).(*service)
	validationService.pipelineStore = store
	authored := testODCSQueryPipeline()
	authored.Steps[0].Query.OutputContractRef.RevisionSlug = "v2"
	validation, err := validationService.Validate(t.Context(), "acme", "etl", "", "alice", authored)
	require.NoError(t, err)
	require.False(t, validation.Valid)
	require.Equal(t, "PIPELINE_CONTRACT_IMPACT_BREAKING", validation.Diagnostics[0].Code)
}

func TestCompositionReferencesPinEventAndIndependentConsumers(t *testing.T) {
	raw := strings.Replace(testODCSDocument, `"required":true}`, `"required":true},{"name":"amount","logicalType":"integer","required":true}`, 1)
	revision, err := odcs.Record([]byte(raw), "orders")
	require.NoError(t, err)
	calls := 0
	store := &pipelineStore{txm: fakePipelineStoreTxManager{}, odcs: fakeODCSRevisionRepository{get: func(_ context.Context, _ postgres.DBTX, org, project, contract, selected slug.Slug) (*model.ODCSRevision, error) {
		calls++
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("etl"), project)
		return revision, nil
	}}}
	reference := func() *pipelinespec.ODCSContractRef {
		r := testODCSReference()
		r.Representation = model.ContractRepresentationJSON
		r.RootShape = model.ContractRootShapeObject
		return r
	}
	authored := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}, Steps: []pipelinespec.Step{
		{Kind: "input", Slug: "event", Input: &pipelinespec.InputSpec{OutputContractRef: reference()}},
		{Kind: "transform", Slug: "settlement", Transform: &pipelinespec.TransformSpec{SQL: "SELECT id, amount FROM orders", OutputKind: model.TransformOutputKindRows, Inputs: map[string]pipelinespec.StepInput{"orders": {FromStep: "event"}}, InputContracts: map[string]*pipelinespec.InputContractAttachment{"orders": {ContractRef: reference()}}}},
		{Kind: "transform", Slug: "audit", Transform: &pipelinespec.TransformSpec{SQL: "SELECT id FROM orders", OutputKind: model.TransformOutputKindRows, Inputs: map[string]pipelinespec.StepInput{"orders": {FromStep: "event"}}, InputContracts: map[string]*pipelinespec.InputContractAttachment{"orders": {Contract: &payload.Contract{Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject, Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}}}}},
	}}
	resolved, pins, err := store.ResolvePipelineContracts(t.Context(), "acme", "etl", authored)
	require.NoError(t, err)
	require.Len(t, pins, 2)
	require.Equal(t, "output", pins[0].Port)
	require.Equal(t, "input:orders", pins[1].Port)
	require.NoError(t, pipelinevalidate.ValidateStepInputCompatibility(resolved))
	require.NotNil(t, authored.Steps[0].Input.OutputContractRef)
	version := &pipelinesrepo.PipelineVersion{SpecJSONB: mustJSON(t, authored), ResolvedContracts: mustJSON(t, pins)}
	store.odcs = nil
	reloaded, err := pipelineFromVersion(version)
	require.NoError(t, err)
	require.Equal(t, resolved, reloaded)
	require.Equal(t, 2, calls)
	encoded, id, err := contractplan.Compile(contractplan.Input{Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/orders.json"}, Authored: version.SpecJSONB, Pins: pins})
	require.NoError(t, err)
	plan, err := contractplan.Read(encoded, id)
	require.NoError(t, err)
	require.Len(t, plan.Pins, 2)
	pins[1].Port = "input:renamed"
	version.ResolvedContracts = mustJSON(t, pins)
	_, err = pipelineFromVersion(version)
	require.Error(t, err)
}

func TestSupplierOfferDecimalPinsResolveAndReloadWithoutCatalog(t *testing.T) {
	t.Parallel()
	document, err := supplierfixture.Files.ReadFile("data/supplier-offer.odcs.json")
	require.NoError(t, err)
	revision, err := odcs.Record(document, "supplier-offer")
	require.NoError(t, err)
	pipeline, ref, err := supplierfixture.PinnedPipeline()
	require.NoError(t, err)
	calls := 0
	store := &pipelineStore{txm: fakePipelineStoreTxManager{}, odcs: fakeODCSRevisionRepository{get: func(_ context.Context, _ postgres.DBTX, org, project, contract, selected slug.Slug) (*model.ODCSRevision, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("etl"), project)
		require.Equal(t, ref.ContractSlug, contract)
		require.Equal(t, ref.RevisionSlug, selected)
		calls++
		return revision, nil
	}}}
	resolved, pins, err := store.ResolvePipelineContracts(t.Context(), "acme", "etl", pipeline)
	require.NoError(t, err)
	require.Len(t, pins, 22)
	require.Equal(t, 22, calls)
	require.NoError(t, pipelinevalidate.Validate(resolved))
	require.NoError(t, pipelinevalidate.ValidateStepInputCompatibility(resolved))
	for _, pin := range pins {
		require.Equal(t, revision.Digest, pin.Digest)
		require.Equal(t, odcs.DecimalCompilerID, pin.CompilerID)
		fields := pin.Shape.Fields
		require.Equal(t, model.ContractFieldTypeValueDecimal, fields[5].Type)
		require.Equal(t, 18, *fields[5].Precision)
		require.Equal(t, 2, *fields[5].Scale)
		require.Equal(t, 4, *fields[6].Scale)
	}
	authored, err := json.Marshal(pipeline)
	require.NoError(t, err)
	encoded, err := json.Marshal(pins)
	require.NoError(t, err)
	store.odcs = nil
	reloaded, err := pipelineFromVersion(&pipelinesrepo.PipelineVersion{SpecJSONB: authored, ResolvedContracts: encoded})
	require.NoError(t, err)
	require.Equal(t, resolved, reloaded)
	require.Equal(t, 22, calls)
	pins[0].CompilerID = "unknown"
	changed, err := json.Marshal(pins)
	require.NoError(t, err)
	_, err = pipelineFromVersion(&pipelinesrepo.PipelineVersion{SpecJSONB: authored, ResolvedContracts: changed})
	require.ErrorContains(t, err, "unsupported flat compiler")
}

func TestRevisionOwnedDecimalLoadsForPlainQuery(t *testing.T) {
	document, err := supplierfixture.Files.ReadFile("data/supplier-offer.odcs.json")
	require.NoError(t, err)
	revision, err := odcs.Record(document, "supplier-offer")
	require.NoError(t, err)
	pipeline := testODCSQueryPipeline()
	pipeline.Steps[0].Query.OutputContractRef = &pipelinespec.ODCSContractRef{ContractSlug: "supplier-offer", RevisionSlug: "v1", SchemaObjectID: "offer"}
	require.False(t, pipeline.UsesContractComposition())
	store := &pipelineStore{txm: fakePipelineStoreTxManager{}, odcs: fakeODCSRevisionRepository{get: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.ODCSRevision, error) {
		return revision, nil
	}}}
	_, pins, err := store.ResolvePipelineContracts(t.Context(), "acme", "etl", pipeline)
	require.NoError(t, err)
	version := &pipelinesrepo.PipelineVersion{SpecJSONB: mustJSON(t, pipeline), ResolvedContracts: mustJSON(t, pins)}
	_, err = pipelineFromVersion(version)
	require.NoError(t, err)
}

func TestRichAuthoringRetainsReferencesButCannotUseFlatExecution(t *testing.T) {
	t.Parallel()
	revision, _, err := richcontractfixture.Selection(model.ContractRepresentationRows, model.ContractRootShapeUnknown)
	require.NoError(t, err)
	store := &pipelineStore{txm: fakePipelineStoreTxManager{}, odcs: fakeODCSRevisionRepository{get: func(_ context.Context, _ postgres.DBTX, org, project, contract, selected slug.Slug) (*model.ODCSRevision, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("etl"), project)
		if contract != "orders" || selected != "v1" {
			return nil, odcscontracts.ErrNotFound
		}
		return revision, nil
	}}}
	pipeline := testODCSQueryPipeline()
	pipeline.Steps[0].Query.OutputContractRef = richcontractfixture.Reference(model.ContractRepresentationRows, model.ContractRootShapeUnknown)
	_, _, err = store.ResolvePipelineContracts(t.Context(), "acme", "etl", pipeline)
	require.ErrorIs(t, err, ErrODCSContractReference)
	retained, pins, err := store.resolvePipelineContracts(t.Context(), "acme", "etl", pipeline, true)
	require.NoError(t, err)
	require.Empty(t, pins)
	require.NotNil(t, retained.Steps[0].Query.OutputContractRef)
	require.Nil(t, retained.Steps[0].Query.OutputContract)
	raw, err := json.Marshal(retained)
	require.NoError(t, err)
	_, err = pipelineFromVersion(&pipelinesrepo.PipelineVersion{SpecJSONB: raw, ResolvedContracts: json.RawMessage(`[]`)})
	require.ErrorContains(t, err, "missing a pinned ODCS contract")
	pipeline.Steps[0].Query.OutputContractRef.SchemaObjectID = "missing"
	_, _, err = store.resolvePipelineContracts(t.Context(), "acme", "etl", pipeline, true)
	require.ErrorIs(t, err, ErrODCSContractReference)
	pipeline.Steps[0].Query.OutputContractRef.SchemaObjectID = "order"
	pipeline.Steps[0].Query.OutputContractRef.RevisionSlug = "missing"
	_, _, err = store.resolvePipelineContracts(t.Context(), "acme", "etl", pipeline, true)
	require.ErrorIs(t, err, ErrODCSContractReference)
	_, _, err = store.resolvePipelineContracts(t.Context(), "acme", "etl", pipeline, true)
	require.ErrorIs(t, err, ErrODCSContractReference)
}
