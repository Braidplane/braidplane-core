package drift

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	spec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/stretchr/testify/require"
)

func TestPortImpactTraversesRealEdgesAndDoesNotMutatePipeline(t *testing.T) {
	pipeline := contractImpactPipeline()
	pipeline.Steps = append(pipeline.Steps, spec.Step{Slug: "store", Kind: "sink", Sink: &spec.SinkSpec{Input: &spec.StepInput{FromStep: "normalize-users"}}})
	before := pipeline.Steps[0].Query.OutputContract
	after := &payload.Contract{Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueString}}}
	report := PortImpact(pipeline, "fetch-users", "output", before, after)
	require.Equal(t, ReportSeverityBreaking, report.Severity)
	require.Equal(t, 1, countImpacts(report.Impacts, "sink", ""))
	require.Equal(t, 1, countImpacts(report.Impacts, "edge", "normalize-users->store"))
	require.Same(t, before, pipeline.Steps[0].Query.OutputContract)
	require.Nil(t, PortImpact(pipeline, "fetch-users", "output", before, before))
}

func TestSinkInputPortImpactMatchesInlineBreakingChange(t *testing.T) {
	pipeline := &spec.Pipeline{Steps: []spec.Step{{Slug: "store", Kind: "sink", Sink: &spec.SinkSpec{}}}}
	before := &payload.Contract{Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger}}}
	after := &payload.Contract{Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueString}}}
	report := PortImpact(pipeline, "store", "input", before, after)
	require.NotNil(t, report)
	require.Equal(t, ReportSeverityBreaking, report.Severity)
	require.Equal(t, "step[store].sink.input_contract.id.type", report.Changes[0].Path)
}

func TestNamedAndEventPortImpactPreservesIndependentAttachments(t *testing.T) {
	before := &payload.Contract{Representation: model.ContractRepresentationRows, Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}
	after := payload.CloneContract(before)
	after.Fields[0].Type = model.ContractFieldTypeValueString
	pipeline := &spec.Pipeline{Metadata: spec.Metadata{Slug: "orders"}, Steps: []spec.Step{{Kind: "transform", Slug: "audit", Transform: &spec.TransformSpec{InputContracts: map[string]*spec.InputContractAttachment{"orders": {Contract: before}}}}}}
	report := PortImpact(pipeline, "audit", "input:orders", before, after)
	require.NotNil(t, report)
	require.Equal(t, ReportSeverityBreaking, report.Severity)
	require.Contains(t, report.Changes[0].Path, "input_contracts.orders.contract.id.type")
	require.Same(t, before, pipeline.Steps[0].Transform.InputContracts["orders"].Contract)
	pipeline.Steps = []spec.Step{{Kind: "input", Slug: "event", Input: &spec.InputSpec{OutputContract: before}}}
	report = PortImpact(pipeline, "event", "output", before, after)
	require.NotNil(t, report)
	require.Same(t, before, pipeline.Steps[0].Input.OutputContract)
}
