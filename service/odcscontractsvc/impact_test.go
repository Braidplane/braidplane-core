package odcscontractsvc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	drift "github.com/Braidplane/braidplane-core/pkg/pipeline/drift"
	spec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/stretchr/testify/require"
)

const impactDocument = `{"kind":"DataContract","apiVersion":"v3.2.0","id":"orders","version":"1","schema":[{"id":"order","name":"Order","properties":[{"id":"order-id","name":"id","logicalType":"integer","required":true}]}]}`

func impactRevision(t *testing.T, revision slug.Slug, raw string) *model.ODCSRevision {
	t.Helper()
	value, err := odcs.Record([]byte(raw), "orders")
	require.NoError(t, err)
	value.Slug = revision
	return value
}

func TestObjectComparisonUsesNormalizedShapeAndStableIdentities(t *testing.T) {
	before := impactRevision(t, "v1", impactDocument)
	for _, tc := range []struct {
		name, raw, comparison string
		severity              drift.ReportSeverity
		changes               int
	}{
		{"type", strings.Replace(impactDocument, `"integer"`, `"string"`, 1), "supported", drift.ReportSeverityBreaking, 1},
		{"required", strings.Replace(impactDocument, `"required":true`, `"required":false`, 1), "supported", drift.ReportSeverityBreaking, 2},
		{"metadata", strings.Replace(impactDocument, `"version":"1"`, `"version":"2"`, 1), "supported", drift.ReportSeveritySafe, 0},
		{"rich metadata", strings.Replace(impactDocument, `"version":"1"`, `"version":"2","slaProperties":[{"property":"latency","value":1}]`, 1), "supported", drift.ReportSeveritySafe, 0},
		{"identity", strings.Replace(impactDocument, `"order-id"`, `"new-id"`, 1), "supported", drift.ReportSeverityWarning, 1},
		{"unsupported", strings.Replace(impactDocument, `"integer"`, `"date"`, 1), "unsupported", drift.ReportSeveritySafe, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after := impactRevision(t, "v2", tc.raw)
			result, err := compareObject(before, after, "order")
			require.NoError(t, err)
			require.Equal(t, tc.comparison, result.Comparison)
			require.Len(t, result.Changes, tc.changes)
			if tc.changes > 0 {
				severity := drift.ReportSeveritySafe
				for _, change := range result.Changes {
					severity = max(severity, change.Severity)
				}
				require.Equal(t, tc.severity, severity)
				require.Equal(t, "order-id", result.Changes[0].BeforePropertyID)
				require.Contains(t, result.Changes[0].Path, "schema_object[order]")
			}
			if tc.comparison == "unsupported" {
				require.NotEmpty(t, result.Diagnostics)
				require.Equal(t, slug.Slug("v2"), result.Diagnostics[0].RevisionSlug)
				if tc.name == "unsupported" {
					require.Equal(t, "order-id", result.Diagnostics[0].PropertyID)
				}
			}
		})
	}
}

type impactRepository struct {
	fakeRepository
	snapshots []odcscontracts.ConsumerSnapshot
	calls     int
}

func (r *impactRepository) ListConsumerSnapshots(_ context.Context, _ postgres.DBTX, org, project slug.Slug) ([]odcscontracts.ConsumerSnapshot, error) {
	if org != "acme" || project != "etl" {
		panic("consumer scope lost")
	}
	r.calls++
	return r.snapshots, nil
}

func TestRevisionImpactEnumeratesPortsAndUsesImmutableDeployedPin(t *testing.T) {
	before := impactRevision(t, "v1", impactDocument)
	after := impactRevision(t, "v2", strings.Replace(impactDocument, `"integer"`, `"string"`, 1))
	ref := &spec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order"}
	pipeline := &spec.Pipeline{Metadata: spec.Metadata{Slug: "sync"}, Steps: []spec.Step{
		{Slug: "fetch", Kind: "query", Query: &spec.QueryRef{OutputContractRef: ref}},
		{Slug: "normalize", Kind: "transform", Transform: &spec.TransformSpec{Inputs: map[string]spec.StepInput{"source": {FromStep: "fetch"}}, InputContractRef: ref, OutputContractRef: ref}},
		{Slug: "store", Kind: "sink", Sink: &spec.SinkSpec{Input: &spec.StepInput{FromStep: "normalize"}, InputContractRef: ref}},
	}}
	raw, err := json.Marshal(pipeline)
	require.NoError(t, err)
	conversion, err := odcs.Convert(before.Document, "order")
	require.NoError(t, err)
	pins := []model.PinnedODCSPort{}
	for _, port := range consumerPorts(pipeline) {
		pins = append(pins, model.PinnedODCSPort{StepSlug: port.step, Port: port.name, ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order", Digest: before.Digest, Shape: conversion.Shape})
	}
	pinJSON, err := json.Marshal(pins)
	require.NoError(t, err)
	repo := &impactRepository{fakeRepository: fakeRepository{get: func(org, project, contract, revision slug.Slug) (*model.ODCSRevision, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("etl"), project)
		require.Equal(t, slug.Slug("orders"), contract)
		if revision == "v1" {
			return before, nil
		}
		return after, nil
	}}, snapshots: []odcscontracts.ConsumerSnapshot{
		{Origin: "deployed", Environment: "staging", SpecHash: "one", Spec: raw, Pins: pinJSON},
		{Origin: "candidate", SpecHash: "two", Spec: raw, Pins: []byte(`[]`)},
		{Origin: "draft", Spec: raw, Pins: []byte(`[]`)},
	}}
	svc := New(testutil.FakeTransactionManager{}, repo, projects.NewRepository(), compiledplans.NewRepository())
	result, err := svc.PreviewRevisionImpact(t.Context(), "acme", "etl", "orders", RevisionImpactRequest{From: "v1", To: "v2"})
	require.NoError(t, err)
	require.Len(t, result.Consumers, 12)
	require.Len(t, result.Objects, 1)
	for _, consumer := range result.Consumers {
		require.Equal(t, "supported", consumer.Comparison)
		require.Equal(t, drift.ReportSeverityBreaking, consumer.Impact.Severity)
	}
	require.Equal(t, "staging", result.Consumers[0].Environment)
	pins[0].Digest = "wrong"
	repo.snapshots[0].Pins, err = json.Marshal(pins)
	require.NoError(t, err)
	_, err = svc.PreviewRevisionImpact(t.Context(), "acme", "etl", "orders", RevisionImpactRequest{From: "v1", To: "v2"})
	require.Error(t, err)
}

func TestRevisionImpactRemovedObjectAndRenamedProperty(t *testing.T) {
	before := impactRevision(t, "v1", impactDocument)
	renamed := impactRevision(t, "v2", strings.Replace(impactDocument, `"name":"id"`, `"name":"order_number"`, 1))
	result, err := compareObject(before, renamed, "order")
	require.NoError(t, err)
	require.Equal(t, "supported", result.Comparison)
	require.Len(t, result.Changes, 2)
	require.Equal(t, "order-id", result.Changes[0].BeforePropertyID)
	require.Equal(t, "order-id", result.Changes[1].AfterPropertyID)
	removed := impactRevision(t, "v3", strings.Replace(impactDocument, `"id":"order"`, `"id":"replacement"`, 1))
	result, err = compareObject(before, removed, "order")
	require.NoError(t, err)
	require.Equal(t, "supported", result.Comparison)
	require.Empty(t, result.Diagnostics)
	require.NotEmpty(t, result.Changes)
	require.Contains(t, result.Changes, ImpactChange{Change: drift.Change{Kind: "removal", Path: "schema_object[order].id", Before: "integer(required=true,nullable=false)", After: "<none>", Severity: drift.ReportSeverityBreaking, ReasonCode: "removal"}, BeforePropertyID: "order-id"})
}

func TestRevisionOwnedDecimalChangesReachCatalogAndEveryConsumer(t *testing.T) {
	document := `{"kind":"DataContract","apiVersion":"v3.2.0","id":"orders","version":"1","customProperties":[{"vendor":"braidplane","property":"decimalPhysicalTypes","value":{"PRICE":{"precision":18,"scale":2}}}],"schema":[{"id":"order","name":"Order","properties":[{"id":"price-id","name":"price","logicalType":"number","physicalType":"PRICE","logicalTypeOptions":{"minimum":0.1}}]}]}`
	before := impactRevision(t, "v1", document)
	after := impactRevision(t, "v2", strings.Replace(document, `"scale":2`, `"scale":4`, 1))
	ref := &spec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order"}
	pipeline := &spec.Pipeline{Metadata: spec.Metadata{Slug: "sync"}, Steps: []spec.Step{{Slug: "fetch", Kind: "query", Query: &spec.QueryRef{OutputContractRef: ref}}}}
	repo := &impactRepository{fakeRepository: fakeRepository{get: func(_, _, _, revision slug.Slug) (*model.ODCSRevision, error) {
		if revision == "v1" {
			return before, nil
		}
		return after, nil
	}}, snapshots: []odcscontracts.ConsumerSnapshot{{Origin: "candidate", Spec: mustImpactJSON(t, pipeline), Pins: []byte(`[]`)}, {Origin: "draft", Spec: mustImpactJSON(t, pipeline), Pins: []byte(`[]`)}}}
	svc := New(testutil.FakeTransactionManager{}, repo, projects.NewRepository(), compiledplans.NewRepository())
	result, err := svc.PreviewRevisionImpact(t.Context(), "acme", "etl", "orders", RevisionImpactRequest{From: "v1", To: "v2"})
	require.NoError(t, err)
	require.Len(t, result.Objects, 1)
	require.Equal(t, "supported", result.Objects[0].Comparison)
	require.NotEmpty(t, result.Objects[0].Changes)
	require.Len(t, result.Consumers, 2)
	for _, consumer := range result.Consumers {
		require.Equal(t, "supported", consumer.Comparison)
		require.Empty(t, consumer.Diagnostics)
		require.NotEmpty(t, consumer.Impact.Changes)
	}
}

func mustImpactJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}
