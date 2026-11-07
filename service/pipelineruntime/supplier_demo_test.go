package pipelineruntime

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/supplierfixture"
	"github.com/stretchr/testify/require"
)

func TestSupplierPriceDeskUnionAllBaselineRepeatAndCedarUpdate(t *testing.T) {
	t.Parallel()
	for _, pinned := range []bool{false, true} {
		t.Run(fmt.Sprintf("pinned=%t", pinned), func(t *testing.T) {
			read := func(name string) []byte {
				data, err := supplierfixture.Files.ReadFile("data/" + name)
				require.NoError(t, err)
				return data
			}
			pipeline, err := validate.Load("supplier-price-desk.pipeline.yaml", read("supplier-price-desk.pipeline.yaml"))
			require.NoError(t, err)
			// Environment binding is resolved before the runner in product execution.
			pipeline.Steps[len(pipeline.Steps)-1].Sink.Config["org_slug"] = "acme"
			var compiled []byte
			var planDigest string
			if pinned {
				var refErr error
				pipeline, _, refErr = supplierfixture.PinnedPipeline()
				require.NoError(t, refErr)
				pipeline.Steps[len(pipeline.Steps)-1].Sink.Config["org_slug"] = "acme"
				ref := pipeline.Steps[1].Transform.OutputContractRef
				conversion, err := odcs.Convert(read("supplier-offer.odcs.json"), ref.SchemaObjectID)
				require.NoError(t, err)
				require.Empty(t, conversion.Diagnostics)
				require.NotNil(t, conversion.Shape)
				conversion.Shape.Representation = ref.Representation
				planInput := contractplan.Input{Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/supplier-price-desk.pipeline.yaml"}}
				for _, step := range pipeline.Steps {
					pin := func(port string) {
						planInput.Pins = append(planInput.Pins, model.PinnedODCSPort{StepSlug: step.Slug, Port: port, ContractSlug: ref.ContractSlug, RevisionSlug: ref.RevisionSlug, SchemaObjectID: ref.SchemaObjectID, Digest: conversion.Digest, CompilerID: conversion.CompilerID, Shape: payload.CloneContract(conversion.Shape), Coverage: conversion.Coverage})
					}
					if q := step.Query; q != nil {
						q.OperationRef = ""
						q.OperationInline = map[string]any{"statement": `{"type":"s3","operation":"read","key":"synthetic.csv","format":"csv"}`}
					}
					if transform := step.Transform; transform != nil {
						pin("output")
						for name := range transform.InputContracts {
							pin("input:" + name)
						}
					}
					if sink := step.Sink; sink != nil {
						pin("input")
						sink.OperationRef = ""
						sink.OperationInline = map[string]any{"statement": `{"type":"postgres","sql":"INSERT captured offers"}`}
					}
				}
				planInput.Authored, err = json.Marshal(pipeline)
				require.NoError(t, err)
				compiled, planDigest, err = contractplan.Compile(planInput)
				require.NoError(t, err)
				loaded, err := contractplan.Read(compiled, planDigest)
				require.NoError(t, err)
				require.Len(t, loaded.Pins, 22)
			}
			variant := "baseline"
			execute := func(_ context.Context, _, source, _ slug.Slug, _ map[string]any) (*result.ExecutionResult, error) {
				provider := strings.TrimPrefix(source.String(), "prices-")
				name := "sources/" + provider + ".csv"
				if provider == "cedar" && variant == "update" {
					name = "updates/cedar.csv"
				}
				records, err := csv.NewReader(bytes.NewReader(read(name))).ReadAll()
				require.NoError(t, err)
				rows := make([]map[string]any, 0, len(records)-1)
				for _, record := range records[1:] {
					row := make(map[string]any, len(record))
					for i, value := range record {
						row[records[0][i]] = value
					}
					rows = append(rows, row)
				}
				return result.NewS3Result(&result.FileResult{Rows: rows, ColumnNames: records[0], RowCount: len(rows), Format: "csv"}, 0, nil), nil
			}
			executor := adaptTestQueryExecutor(&testutil.FakeQueryExecutor{
				ExecuteFn: execute,
				ExecuteInlineFn: func(ctx context.Context, org, source slug.Slug, _, params map[string]any) (*result.ExecutionResult, error) {
					return execute(ctx, org, source, "", params)
				},
			})
			destination := make(map[string]map[string]any)
			writes := 0
			runner, err := NewRunner(executor, clock.New(), NewNoopRunStore(), compositionDataSourceService{}, &fakeSinkRuntime{executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
				writes++
				require.Equal(t, slug.Slug("combine-offers"), input.SourceStep)
				require.Equal(t, slug.Slug("pricing-database"), input.SinkDataSourceSlug)
				output, ok := input.SourceResult.Transform()
				require.True(t, ok)
				require.Len(t, output.Rows, 30)
				for _, row := range output.Rows {
					destination[fmt.Sprint(row["provider"], "/", row["product_code"])] = maps.Clone(row)
				}
				return nil
			}})
			require.NoError(t, err)
			for i, snapshot := range []string{"baseline", "baseline", "update"} {
				variant = snapshot
				run, err := runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "local", RunSlug: fmt.Sprintf("supplier-demo-%d", i), Pipeline: pipeline, CompiledPlan: compiled, CompiledPlanDigest: planDigest})
				require.NoError(t, err)
				require.Equal(t, "succeeded", run.Metadata.Status)
				require.Len(t, run.Timeline.Snapshots, 22)
				if pinned {
					combineChecks := 0
					for _, check := range run.Timeline.StructuralChecks {
						require.Equal(t, planDigest, check.CompiledPlanDigest)
						require.Equal(t, pipelinerun.CheckStatusPassed, check.Status)
						if check.Step == "combine-offers" {
							combineChecks++
						}
					}
					require.Equal(t, 11, combineChecks)
				}
				require.Equal(t, i+1, writes, "one sink dispatch per successful run")
				require.Len(t, destination, 30)
				var expected []map[string]any
				decoder := json.NewDecoder(bytes.NewReader(read("expected-" + snapshot + ".json")))
				decoder.UseNumber()
				require.NoError(t, decoder.Decode(&expected))
				for _, row := range expected {
					actual := destination[fmt.Sprint(row["provider"], "/", row["product_code"])]
					require.Len(t, actual, len(row))
					for field, value := range row {
						require.Equal(t, fmt.Sprint(value), fmt.Sprint(actual[field]), "%s/%s %s", row["provider"], row["product_code"], field)
					}
				}
			}
			require.Equal(t, "2.2000", fmt.Sprint(destination["cedar/000104"]["unit_price"]))
			require.Equal(t, "1.5000", fmt.Sprint(destination["packworks/000104"]["unit_price"]))
		})
	}
}
