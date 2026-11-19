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
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/snowflake"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

func checkOperation(step pipelinespec.Step, binding model.DataSourceBinding, query *queriessvc.Query, graph operationGraph, problem func(code, action string)) {
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
		SQL       string `json:"sql"`
	}
	if !ok || descriptor.QueryOperationType != strings.TrimSpace(operationType) || err != nil ||
		json.Unmarshal(raw, &statement) != nil || statement == nil ||
		(statement.Type != "" && statement.Type != target.String() && statement.Type != descriptor.QueryOperationType) {
		problem("incompatible_operation_type", "select_compatible_operation")
		return
	}
	if !operationSupportsRole(descriptor, raw, statement.Operation, statement.SQL, sink) ||
		!sink && binding.DataSourceReadOnly && !queriessvc.DirectOperationIsReadOnly(target, raw) {
		problem("missing_operation_capability", "select_compatible_operation")
	}
	if runtime.ValidateOperationReferences(string(raw), sink, incremental) != nil ||
		missingOperationParameter(raw, parameters, query.Parameters, sink) {
		problem("missing_operation_parameter", "configure_operation_parameters")
	}
	if !operationContractsCompatible(step, query, graph) {
		problem("incompatible_operation_contract", "update_operation_contract")
	}
}

func operationSupportsRole(d catalog.Descriptor, raw json.RawMessage, operation, sql string, sink bool) bool {
	if sink && !d.SupportsPipelineSink || !sink && !d.SupportsPipelineSource {
		return false
	}
	operation = strings.TrimSpace(operation)
	switch d.QueryOperationType {
	case catalog.OperationTypeSnowflake:
		return !sink || snowflake.SupportsPipelineSinkSQL(sql)
	case catalog.OperationTypeGoogleWorkspace:
		statement, err := googleworkspace.DecodeStatement(raw)
		return err == nil && statement.IsRead() != sink
	case catalog.OperationTypeBigQuery:
		if operation == "" {
			operation = "query"
		}
	case catalog.OperationTypeStripe, catalog.OperationTypeHubSpot, catalog.OperationTypeGit, catalog.OperationTypeFTP, catalog.OperationTypeS3:
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
			sink && slices.Contains(sinkbinding.ReservedNames(), name) {
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

func operationContractsCompatible(step pipelinespec.Step, query *queriessvc.Query, graph operationGraph) bool {
	if q := step.Query; q != nil {
		output := contractMetadata(query.OutputContract, query.OutputContractRef)
		if payload.ValidateContractConjunction(output, contractMetadata(q.OutputContract, q.OutputContractRef)) != nil {
			return false
		}
		if output == nil {
			return true
		}
		for _, consumer := range graph.consumers[step.Slug] {
			if !knownEdgeCompatible(step, consumer) {
				return false
			}
		}
		return true
	}
	sink := *step.Sink
	expected := contractMetadata(query.ExpectedInputContract, query.ExpectedInputContractRef)
	if payload.ValidateContractConjunction(expected, contractMetadata(sink.InputContract, sink.InputContractRef)) != nil {
		return false
	}
	if expected == nil || sink.Input == nil {
		return true
	}
	sink.InputContract, sink.InputContractRef = query.ExpectedInputContract, query.ExpectedInputContractRef
	step.Sink = &sink
	return knownEdgeCompatible(graph.steps[sink.Input.FromStep], step)
}

// Compare explicit facets on an unresolved ODCS reference without assuming its fields are empty.
func contractMetadata(inline *payload.Contract, ref *pipelinespec.ODCSContractRef) *payload.Contract {
	if inline != nil || ref == nil || (ref.Representation == model.ContractRepresentationUnknown && ref.RootShape == model.ContractRootShapeUnknown) {
		return inline
	}
	return &payload.Contract{Representation: ref.Representation, RootShape: ref.RootShape}
}

func knownEdgeCompatible(upstream, downstream pipelinespec.Step) bool {
	var output, input *payload.Contract
	var outputRef, inputRef *pipelinespec.ODCSContractRef
	switch {
	case upstream.Query != nil:
		output, outputRef = upstream.Query.OutputContract, upstream.Query.OutputContractRef
	case upstream.Transform != nil:
		output, outputRef = upstream.Transform.OutputContract, upstream.Transform.OutputContractRef
	case upstream.Input != nil:
		output, outputRef = upstream.Input.OutputContract, upstream.Input.OutputContractRef
	}
	switch {
	case downstream.Sink != nil:
		input, inputRef = downstream.Sink.InputContract, downstream.Sink.InputContractRef
	case downstream.Transform != nil:
		input, inputRef = downstream.Transform.InputContract, downstream.Transform.InputContractRef
	}
	if output == nil || input == nil {
		return payload.ValidateContractConjunction(contractMetadata(output, outputRef), contractMetadata(input, inputRef)) == nil
	}
	return pipelinevalidate.EvaluateEdgeDiagnosticsForSteps(upstream, downstream).Compatible
}

type operationGraph struct {
	steps     map[slug.Slug]pipelinespec.Step
	consumers map[slug.Slug][]pipelinespec.Step
}
