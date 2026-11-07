package odcscontractsvc

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	internalmodel "github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	drift "github.com/Braidplane/braidplane-core/pkg/pipeline/drift"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	spec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/stretchr/testify/require"
)

type impactPlans struct {
	find func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string) (*compiledplans.Snapshot, error)
}

func (p impactPlans) Find(ctx context.Context, db postgres.DBTX, org, project, pipeline slug.Slug, digest string) (*compiledplans.Snapshot, error) {
	return p.find(ctx, db, org, project, pipeline, digest)
}

func TestSelectedProjectionImpactLoadsExactScopedPlans(t *testing.T) {
	before := impactRevision(t, "v1", impactDocument)
	after := impactRevision(t, "v2", strings.Replace(impactDocument, `"name":"id"`, `"name":"renamed"`, 1))
	makePlan := func(revision *internalmodel.ODCSRevision) *compiledplans.Snapshot {
		raw, digest, err := contractplan.Compile(contractplan.Input{Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/sync.json"}, Authored: json.RawMessage(`{"metadata":{"slug":"sync","name":"Sync"},"steps":[{"kind":"query","slug":"fetch","query":{"data_source_ref":"db","operation_type":"sql","operation_ref":"orders"}}]}`), Operations: []contractplan.Operation{{Step: "fetch", Role: "query", DataSource: "db", Slug: "orders", Revision: "operation-v1", Content: json.RawMessage(`{"data":{"statement":"{\"sql\":\"SELECT private_field\"}"},"parameters":[]}`)}}, Projection: &odcs.ProjectionRequest{Inputs: []odcs.ProjectionInput{{Contract: "orders", Revision: revision.Slug.String(), ObjectID: "order", Digest: revision.Digest, Document: revision.Document, Mapping: odcs.ProjectionMapping{ID: "synthetic/json/v1", Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject, Types: map[payload.StructuralKind]bool{payload.StructuralInteger: true, payload.StructuralObject: true}}}}}})
		require.NoError(t, err)
		plan, err := contractplan.Read(raw, digest)
		require.NoError(t, err)
		return &compiledplans.Snapshot{PipelineSlug: "sync", Digest: digest, FormatVersion: plan.FormatVersion, AuthoredSpecHash: plan.AuthoredSpecHash, Materialization: raw}
	}
	old, current := makePlan(before), makePlan(after)
	plans := impactPlans{find: func(_ context.Context, _ postgres.DBTX, org, project, pipeline slug.Slug, digest string) (*compiledplans.Snapshot, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("etl"), project)
		if pipeline != "sync" {
			return nil, compiledplans.ErrNotFound
		}
		if digest == old.Digest {
			return old, nil
		}
		if digest == current.Digest {
			return current, nil
		}
		return nil, compiledplans.ErrNotFound
	}}
	repo := &impactRepository{fakeRepository: fakeRepository{get: func(_, _, _, revision slug.Slug) (*internalmodel.ODCSRevision, error) {
		if revision == "v1" {
			return before, nil
		}
		return after, nil
	}}}
	svc := New(testutil.FakeTransactionManager{}, repo, projects.NewRepository(), plans)
	request := RevisionImpactRequest{From: "v1", To: "v2", FromPlan: &ImpactPlanSelection{Pipeline: "sync", Digest: old.Digest}, ToPlan: &ImpactPlanSelection{Pipeline: "sync", Digest: current.Digest}}
	result, err := svc.PreviewRevisionImpact(t.Context(), "acme", "etl", "orders", request)
	require.NoError(t, err)
	require.Equal(t, "selected_projection", result.Objects[0].Scope)
	require.Equal(t, "data_path_changed", result.Objects[0].Changes[0].Kind)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private_field")

	pipeline := spec.Pipeline{Metadata: spec.Metadata{Slug: "other"}, Steps: []spec.Step{{Slug: "fetch", Kind: "query", Query: &spec.QueryRef{OutputContractRef: &spec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order"}}}}}
	consumerSpec, err := json.Marshal(pipeline)
	require.NoError(t, err)
	repo.snapshots = []odcscontracts.ConsumerSnapshot{{Origin: "candidate", Spec: consumerSpec, Pins: []byte(`[]`)}}
	result, err = svc.PreviewRevisionImpact(t.Context(), "acme", "etl", "orders", request)
	require.NoError(t, err)
	require.Equal(t, "unsupported", result.Consumers[0].Comparison)
	require.Equal(t, "projection_not_selected", result.Consumers[0].Diagnostics[0].Code)
	require.Equal(t, drift.ReportSeverityWarning, result.Consumers[0].Impact.Severity)

	pipeline.Metadata.Slug = "sync"
	repo.snapshots[0].Spec, err = json.Marshal(pipeline)
	require.NoError(t, err)
	repo.snapshots[0].Origin = "deployed"
	_, err = svc.PreviewRevisionImpact(t.Context(), "acme", "etl", "orders", request)
	require.Error(t, err, "selected projections must reject missing deployed pins")
	conversion, err := odcs.Convert(before.Document, "order")
	require.NoError(t, err)
	pin := internalmodel.PinnedODCSPort{StepSlug: "fetch", Port: "output", ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order", Digest: "wrong", Shape: conversion.Shape}
	repo.snapshots[0].Pins, err = json.Marshal([]internalmodel.PinnedODCSPort{pin})
	require.NoError(t, err)
	_, err = svc.PreviewRevisionImpact(t.Context(), "acme", "etl", "orders", request)
	require.Error(t, err, "selected projections must reject mismatched deployed pins")
	pin.Digest = before.Digest
	repo.snapshots[0].Pins, err = json.Marshal([]internalmodel.PinnedODCSPort{pin})
	require.NoError(t, err)
	result, err = svc.PreviewRevisionImpact(t.Context(), "acme", "etl", "orders", request)
	require.NoError(t, err)
	require.Equal(t, "supported", result.Consumers[0].Comparison)
	require.NotNil(t, result.Consumers[0].Impact)
	request.ToPlan = &ImpactPlanSelection{Pipeline: "other", Digest: current.Digest}
	_, err = svc.PreviewRevisionImpact(t.Context(), "acme", "etl", "orders", request)
	require.ErrorIs(t, err, ErrImpactSelection)
	request.ToPlan = request.FromPlan
	_, err = svc.PreviewRevisionImpact(t.Context(), "acme", "etl", "orders", request)
	require.ErrorIs(t, err, ErrImpactSelection, "a plan for v1 cannot replace the selected v2 projection")
	request.ToPlan = nil
	_, err = svc.PreviewRevisionImpact(t.Context(), "acme", "etl", "orders", request)
	require.Error(t, err)
}

func impactProjection() *odcs.ProjectedInput {
	return &odcs.ProjectedInput{Contract: "orders", ObjectID: "row", Revision: "v1", Digest: "original", Mapping: odcs.ProjectionMapping{ID: "recorded-mapping"}, Structure: &payload.Structure{Contract: "orders", Object: "row", Revision: "v1", Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject, Root: &payload.StructuralType{Kind: payload.StructuralObject, Additional: payload.StructuralOpen, Properties: []payload.StructuralProperty{{ID: "order-id", Name: "a.b", Required: true, Type: &payload.StructuralType{Kind: payload.StructuralString}}}}}}
}

func TestSelectedProjectionImpactPreservesPathIdentityAndSafeChanges(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		change     func(*odcs.ProjectedInput)
		severity   drift.ReportSeverity
	}{
		{"stable ID rename", "data_path_changed", func(p *odcs.ProjectedInput) { p.Structure.Root.Properties[0].Name = "renamed" }, drift.ReportSeverityBreaking},
		{"removed", "declaration_removed", func(p *odcs.ProjectedInput) { p.Structure.Root.Properties = nil }, drift.ReportSeverityBreaking},
		{"type", "structural_constraint_changed", func(p *odcs.ProjectedInput) { p.Structure.Root.Properties[0].Type.Kind = payload.StructuralBoolean }, drift.ReportSeverityBreaking},
		{"required", "structural_constraint_changed", func(p *odcs.ProjectedInput) { p.Structure.Root.Properties[0].Required = false }, drift.ReportSeverityBreaking},
		{"representation", "representation_change", func(p *odcs.ProjectedInput) {
			p.Structure.Representation, p.Structure.RootShape = model.ContractRepresentationRows, model.ContractRootShapeUnknown
		}, drift.ReportSeverityBreaking},
		{"mapping", "mapping_change", func(p *odcs.ProjectedInput) { p.Mapping.ID = "different-mapping" }, drift.ReportSeverityWarning},
		{"resolution", "resolution_change", func(p *odcs.ProjectedInput) {
			p.Resolutions = []odcs.ProjectionResolution{{Value: "private-resolved-value", Revision: "new"}}
		}, drift.ReportSeverityWarning},
		{"description", "descriptive_change", func(p *odcs.ProjectedInput) { p.Digest = "changed-document" }, drift.ReportSeveritySafe},
		{"closed root", "root_constraint_changed", func(p *odcs.ProjectedInput) { p.Structure.Root.Additional = payload.StructuralClosed }, drift.ReportSeveritySafe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, after := impactProjection(), impactProjection()
			tc.change(after)
			result, err := compareProjectedObject("row", before, after)
			require.NoError(t, err)
			i := slices.IndexFunc(result.Changes, func(c ImpactChange) bool { return c.Kind == tc.kind })
			require.NotEqual(t, -1, i)
			require.Equal(t, tc.severity, result.Changes[i].Severity)
			if tc.kind == "data_path_changed" {
				require.Equal(t, `$["a.b"]`, result.Changes[i].Before)
				require.Equal(t, "order-id", result.Changes[i].AfterPropertyID)
			}
			raw, err := json.Marshal(result)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "private-resolved-value")
		})
	}
}

func TestSelectedProjectionImpactScopesIDsAndIDLelessPaths(t *testing.T) {
	before, after := impactProjection(), impactProjection()
	before.Structure.Root.Properties[0].ID, after.Structure.Root.Properties[0].ID = "", ""
	after.Structure.Root.Properties[0].Name = "different"
	result, err := compareProjectedObject("row", before, after)
	require.NoError(t, err)
	require.Len(t, result.Changes, 2)
	require.Equal(t, "declaration_removed", result.Changes[0].Kind)
	require.Equal(t, "declaration_added", result.Changes[1].Kind)
	result, err = compareProjectedObject("row", before, nil)
	require.NoError(t, err)
	require.Equal(t, "unsupported", result.Comparison)
	require.Empty(t, result.Changes)
	parent := func(id, child string) payload.StructuralProperty {
		return payload.StructuralProperty{ID: id, Name: id, Type: &payload.StructuralType{Kind: payload.StructuralObject, Additional: payload.StructuralOpen, Properties: []payload.StructuralProperty{{ID: "shared-id", Name: child, Type: &payload.StructuralType{Kind: payload.StructuralString}}}}}
	}
	before, after = impactProjection(), impactProjection()
	before.Structure.Root.Properties = []payload.StructuralProperty{parent("left", "value"), parent("right", "value")}
	after.Structure.Root.Properties = []payload.StructuralProperty{parent("right", "value"), parent("left", "renamed")}
	result, err = compareProjectedObject("row", before, after)
	require.NoError(t, err)
	paths := []string{}
	for _, change := range result.Changes {
		if change.Kind == "data_path_changed" {
			paths = append(paths, change.Path)
		}
	}
	require.Equal(t, []string{`$["left"]["renamed"]`}, paths)
}

func TestSelectedProjectionImpactReportsRootArrayItemChanges(t *testing.T) {
	before, after := impactProjection(), impactProjection()
	for _, projection := range []*odcs.ProjectedInput{before, after} {
		projection.Structure.RootShape = model.ContractRootShapeArray
		projection.Structure.Root = &payload.StructuralType{Kind: payload.StructuralArray, Items: &payload.StructuralType{Kind: payload.StructuralString}}
	}
	after.Structure.Root.Items.Kind = payload.StructuralBoolean
	result, err := compareProjectedObject("row", before, after)
	require.NoError(t, err)
	require.Len(t, result.Changes, 1)
	require.Equal(t, "root_constraint_changed", result.Changes[0].Kind)
	require.Equal(t, drift.ReportSeverityBreaking, result.Changes[0].Severity)
}

func TestSelectedProjectionInputDisambiguatesMappings(t *testing.T) {
	revision := impactRevision(t, "v1", impactDocument)
	input := odcs.ProjectionInput{Contract: "orders", Revision: "v1", ObjectID: "order", Digest: revision.Digest, Document: revision.Document, Mapping: odcs.ProjectionMapping{ID: "mapping/one", Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject, Types: map[payload.StructuralKind]bool{payload.StructuralObject: true, payload.StructuralInteger: true}}}
	other := input
	other.Mapping.ID = "mapping/two"
	raw, digest, err := contractplan.Compile(contractplan.Input{
		Source:     contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/sync.json"},
		Authored:   json.RawMessage(`{"metadata":{"slug":"sync","name":"Sync"},"steps":[{"kind":"query","slug":"fetch","query":{"data_source_ref":"db","operation_type":"sql","operation_ref":"orders"}}]}`),
		Operations: []contractplan.Operation{{Step: "fetch", Role: "query", DataSource: "db", Slug: "orders", Revision: "v1", Content: json.RawMessage(`{"data":{"statement":"{\"sql\":\"SELECT id\"}"},"parameters":[]}`)}},
		Projection: &odcs.ProjectionRequest{Inputs: []odcs.ProjectionInput{input, other}},
	})
	require.NoError(t, err)
	plan, err := contractplan.Read(raw, digest)
	require.NoError(t, err)
	stored := &compiledplans.Snapshot{PipelineSlug: "sync", Digest: digest, FormatVersion: plan.FormatVersion, AuthoredSpecHash: plan.AuthoredSpecHash, Materialization: raw}
	svc := &service{plans: impactPlans{find: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string) (*compiledplans.Snapshot, error) {
		return stored, nil
	}}}
	selection := &ImpactPlanSelection{Pipeline: "sync", Digest: digest}
	_, err = svc.selectedImpactInputs(t.Context(), nil, "acme", "etl", "orders", revision, selection)
	require.ErrorIs(t, err, ErrImpactSelection, "omission cannot choose the first mapping")
	for _, index := range []int{0, 1} {
		selection.ProjectionInput = &index
		inputs, err := svc.selectedImpactInputs(t.Context(), nil, "acme", "etl", "orders", revision, selection)
		require.NoError(t, err)
		require.Len(t, inputs, 1)
		require.Equal(t, plan.Projection.Inputs[index].Mapping.ID, inputs["order"].Mapping.ID)
	}
	for _, index := range []int{-1, 2} {
		selection.ProjectionInput = &index
		_, err := svc.selectedImpactInputs(t.Context(), nil, "acme", "etl", "orders", revision, selection)
		require.ErrorIs(t, err, ErrImpactSelection)
	}
}
