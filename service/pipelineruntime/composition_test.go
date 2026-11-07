package pipelineruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/stretchr/testify/require"
)

type compositionDataSourceService struct {
	datasourcessvc.Service
	schema datasourcessvc.SchemaResult
}

func TestCompositionProducerAcceptsJSONDatetime(t *testing.T) {
	contract := &payload.Contract{Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject, Fields: []payload.ContractField{{
		Name: "items[].updated_at", Type: model.ContractFieldTypeValueDatetime, Required: true,
	}}}
	for _, tc := range []struct {
		document string
		valid    bool
	}{
		{`{"items":[{"updated_at":"2026-11-01T00:00:01Z"},{"updated_at":"2026-11-01T01:00:02.123+01:00"}]}`, true},
		{`{"items":[{"updated_at":"2026-11-01T00:00:01Z"},{"updated_at":"invalid"}]}`, false},
	} {
		err := validateCompositionProducerPayload(&payload.Payload{Representation: model.ContractRepresentationJSON, Document: json.RawMessage(tc.document)}, contract)
		if tc.valid {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrQueryOutputContractSchemaMismatch)
		}
	}
}

func (s compositionDataSourceService) FindBySlug(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error) {
	return &datasourcessvc.DataSource{Type: model.DataSourceTypePostgres, Schema: s.schema}, nil
}

func TestCompositionPrevalidationKeepsUnknownProviderChecksLocal(t *testing.T) {
	requirement := &payload.Contract{Representation: model.ContractRepresentationRows, Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}
	p := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}, Steps: []pipelinespec.Step{
		{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "db", OperationType: "sql", OperationRef: "orders"}},
		{Kind: "transform", Slug: "audit", Transform: &pipelinespec.TransformSpec{SQL: "SELECT id FROM orders", OutputKind: model.TransformOutputKindRows, Inputs: map[string]pipelinespec.StepInput{"orders": {FromStep: "fetch"}}, InputContracts: map[string]*pipelinespec.InputContractAttachment{"orders": {Contract: requirement}}, OutputContract: requirement}},
	}}
	runner := &Runner{dataSourceService: compositionDataSourceService{}}
	require.NoError(t, runner.PreValidateTransforms(t.Context(), "acme", p))
	require.Nil(t, p.Steps[0].Query.OutputContract, "consumer requirements must not become producer promises")
	require.Error(t, validateCompositionProducerPayload(&payload.Payload{Representation: model.ContractRepresentationRows, Rows: []map[string]any{{"id": "wrong"}}}, requirement))
	// A known provider contradiction is a compile failure, even without a producer promise.
	runner.dataSourceService = compositionDataSourceService{schema: datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{Type: "postgres", Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{Name: "orders", Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "text"}}}}}}}
	require.Error(t, runner.PreValidateTransforms(t.Context(), "acme", p))
}

func TestCompositionEventRuntimeAdmissionKeepsLimitsAndContractChecks(t *testing.T) {
	contract := &payload.Contract{Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject, Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}
	// This is the immutable shape produced by resolving an exact event output reference.
	p := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{{Kind: "input", Slug: "event", Input: &pipelinespec.InputSpec{OutputContract: contract}}}}
	for _, tc := range []struct {
		document string
		valid    bool
	}{{`{"id":1}`, true}, {`{}`, false}, {`{"id":"wrong"}`, false}, {`[{"id":1}]`, false}} {
		input, err := pipelinerun.NewVerifiedJSONEventInput("event", json.RawMessage(tc.document))
		require.NoError(t, err)
		_, summary, err := prepareEventInput(p, input)
		if tc.valid {
			require.NoError(t, err)
			require.NotNil(t, summary)
		} else {
			require.Error(t, err)
		}
	}
	_, _, err := prepareEventInput(p, nil)
	require.Error(t, err)
	_, err = pipelinerun.NewVerifiedJSONEventInput("event", json.RawMessage(`{"large":"`+strings.Repeat("x", pipelinerun.MaxEventInputBytes)+`"}`))
	require.Error(t, err)
}

func TestCompositionPrevalidationDefersQueryAliasesToRuntime(t *testing.T) {
	requirement := &payload.Contract{Representation: model.ContractRepresentationRows, Fields: []payload.ContractField{{Name: "order_id", Type: model.ContractFieldTypeValueInteger, Required: true}}}
	p := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}, Steps: []pipelinespec.Step{
		{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "db", OperationType: "sql", OperationInline: map[string]any{"statement": "SELECT id AS order_id FROM orders"}}},
		{Kind: "transform", Slug: "audit", Transform: &pipelinespec.TransformSpec{SQL: "SELECT order_id FROM orders", OutputKind: model.TransformOutputKindRows, Inputs: map[string]pipelinespec.StepInput{"orders": {FromStep: "fetch"}}, InputContracts: map[string]*pipelinespec.InputContractAttachment{"orders": {Contract: requirement}}, OutputContract: requirement}},
	}}
	runner := &Runner{dataSourceService: compositionDataSourceService{schema: datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{Type: "postgres", Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{Name: "orders", Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "integer"}}}}}}}}
	require.NoError(t, runner.PreValidateTransforms(t.Context(), "acme", p))
	require.Nil(t, p.Steps[0].Query.OutputContract, "an alias requirement stays on this consumer")
	runtime := newSQLTransformRuntime(transformsql.Limits{})
	_, err := runtime.Execute(t.Context(), "acme", p.Steps[1].Transform, map[string]*payload.Payload{"orders": {Representation: model.ContractRepresentationRows, Rows: []map[string]any{{"order_id": int64(1)}}, ColumnNames: []string{"order_id"}, RowCount: 1}})
	require.NoError(t, err)
	_, err = runtime.Execute(t.Context(), "acme", p.Steps[1].Transform, map[string]*payload.Payload{"orders": {Representation: model.ContractRepresentationRows, Rows: []map[string]any{{"id": int64(1)}}, ColumnNames: []string{"id"}, RowCount: 1}})
	require.Error(t, err)
}
