package readinesssvc

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	"github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

func checkOperation(step pipelinespec.Step, binding model.DataSourceBinding, query *queriessvc.Query, steps map[slug.Slug]pipelinespec.Step, problem func(code, action string)) {
	sink := step.Sink != nil
	var operationType string
	var parameters map[string]any
	var incremental bool
	if sink {
		operationType, parameters = step.Sink.OperationType, step.Sink.Config
	} else {
		operationType, parameters = step.Query.OperationType, step.Query.Parameters
		incremental = step.Query.Incremental != nil
	}
	target := binding.DataSourceType
	descriptor, ok := catalog.DescriptorFor(target)
	raw, err := queriessvc.ExtractQueryStatement(query.Data)
	var statement *struct {
		Type      string `json:"type"`
		Operation string `json:"operation"`
	}
	if !ok || descriptor.QueryOperationType != strings.TrimSpace(operationType) || err != nil ||
		json.Unmarshal(raw, &statement) != nil || statement == nil ||
		(statement.Type != "" && statement.Type != target.String() && statement.Type != descriptor.QueryOperationType) {
		problem("incompatible_operation_type", "select_compatible_operation")
		return
	}
	if !operationSupportsRole(descriptor, raw, statement.Operation, sink) ||
		!sink && binding.DataSourceReadOnly && !queriessvc.DirectOperationIsReadOnly(target, raw) {
		problem("missing_operation_capability", "select_compatible_operation")
	}
	if runtime.ValidateOperationReferences(string(raw), sink, incremental) != nil ||
		missingOperationParameter(raw, parameters, query.Parameters, sink) {
		problem("missing_operation_parameter", "configure_operation_parameters")
	}
	if !operationContractsCompatible(step, query, steps) {
		problem("incompatible_operation_contract", "update_operation_contract")
	}
}

func operationSupportsRole(d catalog.Descriptor, raw json.RawMessage, operation string, sink bool) bool {
	if sink && !d.SupportsPipelineSink || !sink && !d.SupportsPipelineSource {
		return false
	}
	operation = strings.TrimSpace(operation)
	switch d.QueryOperationType {
	case catalog.OperationTypeGoogleWorkspace:
		statement, err := googleworkspace.DecodeStatement(raw)
		return err == nil && statement.IsRead() != sink
	case catalog.OperationTypeBigQuery:
		if operation == "" {
			operation = "query"
		}
	case catalog.OperationTypeHubSpot, catalog.OperationTypeGit, catalog.OperationTypeFTP, catalog.OperationTypeS3:
		operation = strings.ToLower(operation)
		if operation == "" {
			// The default is a read (HubSpot may infer either list or search).
			return !sink
		}
	case catalog.OperationTypeMongoDB:
		if _, dynamic := param.ExactParamName(operation); dynamic {
			// MongoDB substitutes parameters before decoding its operation.
			return true
		}
		operation = strings.ToLower(operation)
	default:
		// Authored SQL and generic HTTP requests can serve either pipeline role.
		return true
	}
	if sink {
		return slices.Contains(d.Capabilities.WriteOperations, operation)
	}
	return slices.Contains(d.Capabilities.ReadOperations, operation)
}

func missingOperationParameter(raw json.RawMessage, values map[string]any, parameters []*queriessvc.QueryParameter, sink bool) bool {
	for _, name := range param.ExtractParamNames(string(raw)) {
		if _, supplied := values[name]; supplied || strings.HasPrefix(name, runtime.Namespace) ||
			sink && (name == sinkbinding.Input || strings.HasPrefix(name, "__braidplane_")) {
			continue
		}
		if !slices.ContainsFunc(parameters, func(p *queriessvc.QueryParameter) bool {
			return p != nil && p.Name == name && p.DefaultValue != nil
		}) {
			return true
		}
	}
	return false
}

func operationContractsCompatible(step pipelinespec.Step, query *queriessvc.Query, steps map[slug.Slug]pipelinespec.Step) bool {
	if step.Query != nil {
		return payload.ValidateContractConjunction(query.OutputContract, step.Query.OutputContract) == nil
	}
	expected := query.ExpectedInputContract
	if payload.ValidateContractConjunction(expected, step.Sink.InputContract) != nil {
		return false
	}
	if expected == nil || step.Sink.Input == nil {
		return true
	}
	upstream := steps[step.Sink.Input.FromStep]
	var output *payload.Contract
	switch {
	case upstream.Query != nil:
		output = upstream.Query.OutputContract
	case upstream.Transform != nil:
		output = upstream.Transform.OutputContract
	case upstream.Input != nil:
		output = upstream.Input.OutputContract
	}
	if output == nil {
		return true
	}
	// The edge validator supplies default facets when a producer only declares fields.
	if output.Representation == model.ContractRepresentationUnknown {
		sink := *step.Sink
		sink.InputContract, sink.InputContractRef = expected, nil
		step.Sink = &sink
		return pipelinevalidate.EvaluateEdgeDiagnosticsForSteps(upstream, step).Compatible
	}
	edge := pipelinevalidate.EdgeLink{FromStep: upstream.Slug, ToStep: step.Slug}
	return pipelinevalidate.EvaluateEdgeCompatibility(edge, output, expected).Compatible
}
