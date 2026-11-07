package pipelineruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/richcontractfixture"
	"github.com/stretchr/testify/require"
)

func TestRichSelectedPortsPersistFailuresBeforeDownstreamEffects(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"Query", "Event", "NamedConsumer", "SavedQuery", "SavedSink", "Sink"} {
		t.Run(boundary, func(t *testing.T) {
			for _, value := range []struct{ name, from, to string }{
				{name: "Valid"},
				{"Nested", `"label":"synthetic"`, `"label":42`},
				{"Nullability", `"label":"synthetic"`, `"label":null`},
				{"Temporal", `2026-10-20T12:00:00.000Z`, `2026-10-20T12:00:00.000`},
				{"Vector", `[0.5,1,2]`, `[0.5,1]`},
				{"VectorPrecision", `[0.5,1,2]`, `[0.1,1,2]`},
				{"OptionalParentAbsent", `,"context":{"label":"synthetic"}`, ``},
				{"OptionalParentNull", `{"label":"synthetic"}`, `null`},
			} {
				t.Run(value.name, func(t *testing.T) {
					req := checkTestRequest(t)
					req.Source.AttemptNumber = 2
					p := req.Pipeline
					representation, root := model.ContractRepresentationRows, model.ContractRootShapeUnknown
					if boundary == "Event" {
						representation, root = model.ContractRepresentationJSON, model.ContractRootShapeObject
					}
					revision, selection, err := richcontractfixture.Selection(representation, root)
					require.NoError(t, err)
					projection, err := odcs.CompileProjection(*selection)
					require.NoError(t, err)
					require.Empty(t, projection.Diagnostics)
					ref := richcontractfixture.Reference(representation, root)
					pin, err := contractplan.CapturePort(ref, revision, projection.Plan)
					require.NoError(t, err)
					p.Steps[0].Query.OutputContract = payload.CloneContract(pin.Shape)
					phase, origin, step := "producer_output", "step", slug.Slug("fetch")
					var operations []contractplan.Operation
					switch boundary {
					case "Query":
						p.Steps[0].Query.OutputContract = nil
						p.Steps[0].Query.OutputContractRef = ref
					case "Event":
						p.Steps[0] = pipelinespec.Step{Kind: "input", Slug: "fetch", Input: &pipelinespec.InputSpec{OutputContractRef: ref}}
						phase = "event_admission"
						p.Steps[1].Transform.InputContracts["orders"].Contract = payload.CloneContract(p.Steps[1].Transform.InputContracts["orders"].Contract)
						p.Steps[1].Transform.InputContracts["orders"].Contract.Representation = model.ContractRepresentationJSON
						p.Steps[1].Transform.InputContracts["orders"].Contract.RootShape = model.ContractRootShapeObject
					case "NamedConsumer":
						p.Steps[1].Transform.InputContracts["orders"] = &pipelinespec.InputContractAttachment{ContractRef: ref}
						phase, step = "normalized_input", "project"
					case "SavedQuery", "SavedSink":
						origin = "operation"
						content := contractplan.OperationContent{Data: json.RawMessage(`{"statement":"{\"sql\":\"SELECT captured\"}"}`)}
						role, reference := "query", slug.Slug("read-orders")
						if boundary == "SavedQuery" {
							p.Steps[0].Query.OutputContract = nil
							p.Steps[0].Query.OperationInline = nil
							p.Steps[0].Query.OperationRef = reference
							content.OutputContractRef = ref
						} else {
							phase, step, role, reference = "pre_sink", "deliver", "sink", "write-orders"
							p.Steps = []pipelinespec.Step{p.Steps[0], p.Steps[2]}
							p.Steps[1].Sink.Input = &pipelinespec.StepInput{FromStep: "fetch"}
							p.Steps[1].Sink.OperationInline = nil
							p.Steps[1].Sink.OperationRef = reference
							content.ExpectedInputContractRef = ref
						}
						pin.StepSlug, pin.Port = step, "output"
						if role == "sink" {
							pin.Port = "input"
						}
						content.ContractPins = []model.PinnedODCSPort{pin}
						encoded, err := json.Marshal(content)
						require.NoError(t, err)
						operations = []contractplan.Operation{{Step: step, Role: role, DataSource: "db", Slug: reference, Revision: "operation-v1", Content: encoded}}
					case "Sink":
						p.Steps = []pipelinespec.Step{p.Steps[0], p.Steps[2]}
						p.Steps[1].Sink.Input = &pipelinespec.StepInput{FromStep: "fetch"}
						p.Steps[1].Sink.InputContract = nil
						p.Steps[1].Sink.InputContractRef = ref
						phase, step = "pre_sink", "deliver"
					}
					pins, err := contractplan.CapturePipelinePins(p, projection.Plan, func(*pipelinespec.ODCSContractRef) (*model.ODCSRevision, error) { return revision, nil })
					require.NoError(t, err)
					authored, err := json.Marshal(p)
					require.NoError(t, err)
					raw, digest, err := contractplan.Compile(contractplan.Input{Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/rich.json"}, Authored: authored, Pins: pins, Operations: operations, Projection: selection})
					require.NoError(t, err)
					req.Pipeline = nil
					req.CompiledPlan, req.CompiledPlanDigest = raw, digest
					data := richcontractfixture.ValidJSON
					if value.from != "" {
						data = strings.Replace(data, value.from, value.to, 1)
					}
					var row map[string]any
					require.NoError(t, jsonvalue.Unmarshal([]byte(data), &row))
					calls := 0
					store, saved := durableCheckStore(t, nil)
					runner := checkTestRunner(t, store, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error { calls++; return nil }})
					execute := func() (*result.ExecutionResult, error) {
						return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{row}, ColumnNames: payload.CollectColumnNames([]map[string]any{row}), RowCount: 1}, 0, nil), nil
					}
					runner.executor = capturedTestExecutor{queryExecutor: adaptTestQueryExecutor(&testutil.FakeQueryExecutor{ExecuteInlineFn: func(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error) {
						return execute()
					}}), execute: func(contractplan.OperationContent) (*result.ExecutionResult, error) { return execute() }}
					if boundary == "Event" {
						req.EventInput, err = pipelinerun.NewVerifiedJSONEventInput("fetch", json.RawMessage(data))
						require.NoError(t, err)
					}
					run, err := runner.Run(t.Context(), req)
					valid := strings.HasPrefix(value.name, "Optional") || value.name == "Valid"
					if valid {
						require.NoError(t, err)
						require.Equal(t, 1, calls)
					} else {
						require.Error(t, err)
						require.Zero(t, calls)
					}
					if boundary != "Event" || valid {
						require.NotNil(t, run)
					}
					var durable pipelinerun.RunTimeline
					require.NotEmpty(t, *saved)
					require.NoError(t, json.Unmarshal((*saved)[len(*saved)-1], &durable))
					found := false
					for _, check := range durable.StructuralChecks {
						if check.Step != step || check.Origin != origin || check.Phase != phase {
							continue
						}
						found = true
						require.Equal(t, digest, check.CompiledPlanDigest)
						require.Equal(t, 2, check.AttemptNumber)
						require.NotEmpty(t, check.ProjectionDigest)
						require.Contains(t, check.Declaration, "orders/v1/order")
						if valid {
							require.Equal(t, pipelinerun.CheckStatusPassed, check.Status)
						} else {
							require.Equal(t, pipelinerun.CheckStatusFailed, check.Status)
							require.Equal(t, "contract_violation", check.ReasonCode)
						}
					}
					require.True(t, found)
					checks, err := json.Marshal(durable.StructuralChecks)
					require.NoError(t, err)
					require.NotContains(t, string(checks), "synthetic")
					require.NotContains(t, string(checks), "2026-10-20T12:00")
				})
			}
		})
	}
}

func TestRichJournalRejectsDecimalCoercionAndPreservesHistoricalChecks(t *testing.T) {
	t.Parallel()
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprintf("Strict=%t", strict), func(t *testing.T) {
			req := checkTestRequest(t)
			store, saved := durableCheckStore(t, nil)
			runner := checkTestRunner(t, store, &fakeSinkRuntime{})
			precision, scale := 20, 2
			contract := &payload.Contract{Name: "decimal", Representation: model.ContractRepresentationRows, Fields: []payload.ContractField{{Name: "amount", Type: model.ContractFieldTypeValueDecimal, Required: true, Precision: &precision, Scale: &scale}}}
			structure := &payload.Structure{Contract: "orders", Revision: "v1", Object: "order", Representation: model.ContractRepresentationRows, Root: &payload.StructuralType{Kind: payload.StructuralObject, Additional: payload.StructuralOpen, Properties: []payload.StructuralProperty{{Name: "amount", Required: true, Type: &payload.StructuralType{Kind: payload.StructuralDecimal, Precision: 20, Scale: 2}}}}}
			journal := &checkJournal{runner: runner, request: req, pipeline: req.Pipeline, started: runner.clock.Now(), timeline: &pipelinerun.RunTimeline{RunSlug: req.RunSlug, Status: "running", StructuralChecks: []pipelinerun.StructuralCheck{{Step: "fetch", Origin: "step", Phase: "producer_output"}}}, structures: map[int]*payload.Structure{0: structure}, contracts: map[int]*payload.Contract{0: contract}, strictProjections: strict}
			source := pipelinerun.NewStepResult(result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"amount": "0.10"}}, ColumnNames: []string{"amount"}, RowCount: 1}, 0, nil))
			err := journal.evaluate(t.Context(), "fetch", "step", "producer_output", "", source, func() error { return nil })
			var durable pipelinerun.RunTimeline
			require.NotEmpty(t, *saved)
			require.NoError(t, json.Unmarshal((*saved)[len(*saved)-1], &durable))
			if strict {
				require.Error(t, err)
				require.Equal(t, pipelinerun.CheckStatusFailed, durable.StructuralChecks[0].Status)
			} else {
				require.NoError(t, err)
				require.Equal(t, pipelinerun.CheckStatusPassed, durable.StructuralChecks[0].Status)
			}
		})
	}
}
