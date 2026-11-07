package pipelineruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/decimal"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestSupplierCombineTenBranchesOneSinkAndRequiredSourceFailure(t *testing.T) {
	for _, failSource := range []bool{false, true} {
		t.Run(fmt.Sprintf("source_failure=%t", failSource), func(t *testing.T) {
			pipeline, _ := supplierCombinePipeline()
			executor := adaptTestQueryExecutor(&testutil.FakeQueryExecutor{ExecuteFn: func(_ context.Context, _, _, operation slug.Slug, _ map[string]any) (*result.ExecutionResult, error) {
				if failSource && operation == "read-4" {
					return nil, xerror.ConnectionFailed(xerror.DataSource)
				}
				rows := make([]map[string]any, 3)
				for i := range rows {
					rows[i] = map[string]any{"provider": operation.String(), "product": fmt.Sprintf("product-%d", i), "price_text": fmt.Sprintf("%d.10", i+1)}
				}
				return result.NewSQLResult("postgres", &result.SQLResult{Rows: rows, ColumnNames: []string{"provider", "product", "price_text"}, RowCount: 3}, 0, nil), nil
			}})
			sinkCalls := 0
			sink := &fakeSinkRuntime{executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
				sinkCalls++
				require.Equal(t, slug.Slug("combine"), input.SourceStep)
				output, ok := input.SourceResult.Transform()
				require.True(t, ok)
				require.Len(t, output.Rows, 30)
				seen := make(map[string]bool)
				for _, row := range output.Rows {
					key := fmt.Sprint(row["provider"], "/", row["product"])
					require.False(t, seen[key])
					seen[key] = true
					product := row["product"].(string)
					require.Equal(t, map[string]string{"product-0": "1.10", "product-1": "2.10", "product-2": "3.10"}[product], row["unit_price"].(decimal.Value).String())
				}
				return nil
			}}
			runner, err := NewRunner(executor, clock.New(), NewNoopRunStore(), compositionDataSourceService{}, sink)
			require.NoError(t, err)
			run, err := runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "staging", RunSlug: "combine-suppliers", Pipeline: pipeline})
			if failSource {
				require.Error(t, err)
				require.Zero(t, sinkCalls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "succeeded", run.Metadata.Status)
			require.Equal(t, 1, sinkCalls)
			require.Len(t, run.Timeline.Snapshots, 22)
		})
	}
}

func TestSupplierCombinePinnedSharedContractsSurviveReloadAndChecks(t *testing.T) {
	pipeline, shape := supplierCombinePipeline()
	ref := &pipelinespec.ODCSContractRef{ContractSlug: "supplier-offer", RevisionSlug: "revision-one", SchemaObjectID: "offer", Representation: model.ContractRepresentationRows}
	input := contractplan.Input{Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/supplier-offers.json"}}
	for _, step := range pipeline.Steps {
		if step.Query != nil {
			step.Query.OperationRef = ""
			step.Query.OperationInline = map[string]any{"statement": `{"sql":"SELECT captured supplier rows"}`}
		}
		pin := func(port string) {
			input.Pins = append(input.Pins, model.PinnedODCSPort{StepSlug: step.Slug, Port: port, ContractSlug: ref.ContractSlug, RevisionSlug: ref.RevisionSlug, SchemaObjectID: ref.SchemaObjectID, Digest: "sha256:" + strings.Repeat("b", 64), CompilerID: odcs.CompilerID, Shape: shape})
		}
		if step.Transform != nil {
			step.Transform.OutputContract, step.Transform.OutputContractRef = nil, ref
			pin("output")
			for name, attachment := range step.Transform.InputContracts {
				attachment.Contract, attachment.ContractRef = nil, ref
				pin("input:" + name)
			}
		}
		if step.Sink != nil {
			step.Sink.InputContract, step.Sink.InputContractRef = nil, ref
			step.Sink.OperationRef = ""
			step.Sink.OperationInline = map[string]any{"statement": `{"sql":"INSERT captured offers"}`}
			pin("input")
		}
	}
	var err error
	input.Authored, err = json.Marshal(pipeline)
	require.NoError(t, err)
	raw, digest, err := contractplan.Compile(input)
	require.NoError(t, err)
	plan, err := contractplan.Read(raw, digest)
	require.NoError(t, err)
	require.Len(t, plan.Pins, 22)
	for _, pin := range plan.Pins {
		require.Equal(t, ref.ContractSlug, pin.ContractSlug)
		require.Equal(t, ref.RevisionSlug, pin.RevisionSlug)
		require.Equal(t, odcs.CompilerID, pin.CompilerID)
	}
	// Mutating the caller's revision/shape cannot change the stored plan.
	ref.RevisionSlug = "revision-two"
	shape.Fields[0].Type = model.ContractFieldTypeValueInteger
	plan, err = contractplan.Read(raw, digest)
	require.NoError(t, err)
	require.Equal(t, slug.Slug("revision-one"), plan.Pins[0].RevisionSlug)
	executor := adaptTestQueryExecutor(&testutil.FakeQueryExecutor{ExecuteInlineFn: func(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error) {
		return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"provider": "synthetic", "product": "one", "price_text": "1.10"}}, ColumnNames: []string{"provider", "product", "price_text"}, RowCount: 1}, 0, nil), nil
	}})
	sinkCalls := 0
	runner, err := NewRunner(executor, clock.New(), NewNoopRunStore(), compositionDataSourceService{}, &fakeSinkRuntime{executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
		sinkCalls++
		output, ok := input.SourceResult.Transform()
		require.True(t, ok)
		require.Len(t, output.Rows, 10)
		return nil
	}})
	require.NoError(t, err)
	run, err := runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "staging", RunSlug: "pinned-combine", CompiledPlan: raw, CompiledPlanDigest: digest})
	require.NoError(t, err)
	require.Equal(t, 1, sinkCalls)
	combineChecks := 0
	for _, check := range run.Timeline.StructuralChecks {
		require.Equal(t, digest, check.CompiledPlanDigest)
		require.Equal(t, pipelinerun.CheckStatusPassed, check.Status)
		if check.Step == "combine" {
			combineChecks++
		}
	}
	require.Equal(t, 11, combineChecks)
}

func supplierCombinePipeline() (*pipelinespec.Pipeline, *payload.Contract) {
	precision, scale := 18, 2
	canonical := &payload.Contract{Name: "supplier-offer", Representation: model.ContractRepresentationRows, Fields: []payload.ContractField{
		{Name: "provider", Type: model.ContractFieldTypeValueString, Required: true},
		{Name: "product", Type: model.ContractFieldTypeValueString, Required: true},
		{Name: "unit_price", Type: model.ContractFieldTypeValueDecimal, Required: true, Precision: &precision, Scale: &scale},
	}}
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "supplier-offers", Name: "Supplier offers"}}
	combine := &pipelinespec.TransformSpec{OutputKind: model.TransformOutputKindRows, Inputs: make(map[string]pipelinespec.StepInput), InputContracts: make(map[string]*pipelinespec.InputContractAttachment), OutputContract: canonical}
	branches := make([]string, 10)
	for i := range branches {
		name := fmt.Sprintf("supplier_%d", i)
		read := slug.Slug(fmt.Sprintf("read-%d", i))
		normalize := slug.Slug(fmt.Sprintf("normalize-%d", i))
		pipeline.Steps = append(pipeline.Steps,
			pipelinespec.Step{Kind: "query", Slug: read, Query: &pipelinespec.QueryRef{DataSourceSlug: "supplier-files", OperationType: "sql", OperationRef: read, OutputContract: &payload.Contract{Name: "raw-offer", Representation: model.ContractRepresentationRows, Fields: []payload.ContractField{{Name: "provider", Type: model.ContractFieldTypeValueString, Required: true}, {Name: "product", Type: model.ContractFieldTypeValueString, Required: true}, {Name: "price_text", Type: model.ContractFieldTypeValueString, Required: true}}}}},
			pipelinespec.Step{Kind: "transform", Slug: normalize, Transform: &pipelinespec.TransformSpec{Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: read}}, SQL: "SELECT provider, product, CAST(price_text AS DECIMAL(18,2)) AS unit_price FROM input", OutputKind: model.TransformOutputKindRows, OutputContract: canonical}},
		)
		combine.Inputs[name] = pipelinespec.StepInput{FromStep: normalize}
		combine.InputContracts[name] = &pipelinespec.InputContractAttachment{Contract: canonical}
		branches[i] = "SELECT provider, product, unit_price FROM " + name
	}
	combine.SQL = strings.Join(branches, " UNION ALL ")
	pipeline.Steps = append(pipeline.Steps,
		pipelinespec.Step{Kind: "transform", Slug: "combine", Transform: combine},
		pipelinespec.Step{Kind: "sink", Slug: "write-offers", Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: "combine"}, DataSourceSlug: "offers-db", OperationType: "sql", OperationRef: "upsert-offers", InputContract: canonical, IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent}},
	)
	return pipeline, canonical
}
