package runtime

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// Requirements describes the configuration referenced by a canonical pipeline spec.
type Requirements struct {
	Pipeline             pipelinespec.Metadata
	DataSources          []DataSourceRequirement
	EnvironmentVariables []EnvironmentVariableRequirement
	SavedOperations      []SavedOperationRequirement
	InlineOperations     []RequirementUsage
}

// RequirementUsage identifies a field by its JSON Pointer from the root of the spec.
type RequirementUsage struct {
	StepSlug  slug.Slug
	StepKind  string
	FieldPath string
}

type DataSourceRequirement struct {
	Ref        slug.Slug
	Capability model.DataSourceRefCapability
	Usages     []RequirementUsage
}

type EnvironmentVariableRequirement struct {
	Key    string
	Usages []RequirementUsage
}

type SavedOperationRequirement struct {
	DataSourceRef slug.Slug
	Ref           slug.Slug
	Usages        []RequirementUsage
}

// ExtractRequirements reads authored references without resolving or copying field values.
// Data sources are grouped by ref and capability, and saved operations by data source and operation ref.
func ExtractRequirements(pipeline *pipelinespec.Pipeline) Requirements {
	if pipeline == nil {
		return Requirements{}
	}
	result := Requirements{Pipeline: pipeline.Metadata}
	type sourceKey struct {
		ref        slug.Slug
		capability model.DataSourceRefCapability
	}
	type operationKey struct {
		source, ref slug.Slug
	}
	sources := map[sourceKey]int{}
	operations := map[operationKey]int{}
	recordConnector := func(usage RequirementUsage, source, operation slug.Slug, capability model.DataSourceRefCapability, inline bool) {
		if source != "" {
			key := sourceKey{source, capability}
			index, ok := sources[key]
			if !ok {
				index = len(result.DataSources)
				sources[key] = index
				result.DataSources = append(result.DataSources, DataSourceRequirement{Ref: source, Capability: capability})
			}
			result.DataSources[index].Usages = append(result.DataSources[index].Usages, usage.at("data_source_ref"))
		}
		if operation != "" {
			key := operationKey{source, operation}
			index, ok := operations[key]
			if !ok {
				index = len(result.SavedOperations)
				operations[key] = index
				result.SavedOperations = append(result.SavedOperations, SavedOperationRequirement{DataSourceRef: source, Ref: operation})
			}
			result.SavedOperations[index].Usages = append(result.SavedOperations[index].Usages, usage.at("operation_ref"))
		}
		if inline {
			result.InlineOperations = append(result.InlineOperations, usage.at("operation_inline"))
		}
	}
	for i, step := range pipeline.Steps {
		usage := stepRequirementUsage(i, step)
		if q := step.Query; q != nil {
			recordConnector(usage.at("query"), q.DataSourceSlug, q.OperationRef, model.DataSourceRefCapabilityPipelineSource, len(q.OperationInline) > 0)
		}
		if s := step.Sink; s != nil {
			recordConnector(usage.at("sink"), s.DataSourceSlug, s.OperationRef, model.DataSourceRefCapabilityPipelineSink, len(s.OperationInline) > 0)
		}
	}

	variables := map[string][]RequirementUsage{}
	visitEnvironmentReferences(pipeline, func(key string, usage RequirementUsage) {
		if runtimeInputKeyPattern.MatchString(key) {
			variables[key] = append(variables[key], usage)
		}
	})
	for _, key := range slices.Sorted(maps.Keys(variables)) {
		result.EnvironmentVariables = append(result.EnvironmentVariables, EnvironmentVariableRequirement{Key: key, Usages: variables[key]})
	}
	return result
}

func stepRequirementUsage(index int, step pipelinespec.Step) RequirementUsage {
	return RequirementUsage{StepSlug: step.Slug, StepKind: step.Kind, FieldPath: "/steps/" + strconv.Itoa(index)}
}

var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

func (u RequirementUsage) at(field string) RequirementUsage {
	u.FieldPath += "/" + pointerEscaper.Replace(field)
	return u
}

func visitEnvironmentReferences(pipeline *pipelinespec.Pipeline, visit func(string, RequirementUsage)) {
	if pipeline == nil {
		return
	}
	for i, step := range pipeline.Steps {
		usage := stepRequirementUsage(i, step)
		if q := step.Query; q != nil {
			query := usage.at("query")
			visitEnvironmentValue(q.Parameters, query.at("parameters"), visit)
			visitEnvironmentValue(q.OperationInline, query.at("operation_inline"), visit)
			if q.Incremental != nil {
				visitEnvironmentValue(q.Incremental.LookbackWindow, query.at("incremental").at("lookback_window"), visit)
			}
		}
		if s := step.Sink; s != nil {
			sink := usage.at("sink")
			visitEnvironmentValue(s.OperationInline, sink.at("operation_inline"), visit)
			visitEnvironmentValue(s.Config, sink.at("config"), visit)
		}
	}
}

func visitEnvironmentValue(value any, usage RequirementUsage, visit func(string, RequirementUsage)) {
	switch value := value.(type) {
	case string:
		seen := map[string]bool{}
		for _, match := range runtimeReference.FindAllStringSubmatch(value, -1) {
			name, _, _ := strings.Cut(match[1], "|")
			if key, ok := strings.CutPrefix(strings.TrimSpace(name), EnvNamespace); ok && !seen[key] {
				seen[key] = true
				visit(key, usage)
			}
		}
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(value)) {
			visitEnvironmentValue(value[key], usage.at(key), visit)
		}
	case []any:
		for index, item := range value {
			visitEnvironmentValue(item, usage.at(strconv.Itoa(index)), visit)
		}
	}
}
