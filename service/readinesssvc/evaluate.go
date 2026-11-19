// Package readinesssvc evaluates persisted pipeline setup without running connectors.
package readinesssvc

import (
	"slices"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type PipelineInput struct {
	Spec     *pipelinespec.Pipeline
	Refs     []model.DataSourceRef
	Bindings []model.DataSourceBinding
}

type EnvironmentInput struct {
	Slug      slug.Slug
	Variables map[string]Variable
}

type Report struct {
	Status       string              `json:"status"`
	Environments []EnvironmentResult `json:"environments"`
}

type EnvironmentResult struct {
	EnvironmentSlug slug.Slug     `json:"environment_slug"`
	Status          string        `json:"status"`
	Diagnostics     []Diagnostic  `json:"diagnostics"`
	Requirements    []Requirement `json:"requirements,omitzero"`
}

type Requirement struct {
	Kind       string                        `json:"kind"`
	Key        string                        `json:"key"`
	Capability model.DataSourceRefCapability `json:"capability,omitempty"`
	Status     string                        `json:"status"`
	Usages     []Usage                       `json:"usages"`
	Target     *Target                       `json:"target,omitempty"`
	Variable   *Variable                     `json:"variable,omitempty"`
}

type Usage struct {
	StepSlug  slug.Slug `json:"step_slug"`
	FieldPath string    `json:"field_path"`
}

type Target struct {
	Slug               slug.Slug            `json:"slug"`
	Name               string               `json:"name"`
	Type               model.DataSourceType `json:"type"`
	Active             bool                 `json:"active"`
	ReadOnly           bool                 `json:"read_only"`
	SupportsCapability bool                 `json:"supports_capability"`
}

type Variable struct {
	Source       model.RuntimeVariableSource `json:"source"`
	Present      bool                        `json:"present"`
	TypeMismatch bool                        `json:"type_mismatch"`
}

type Diagnostic struct {
	Severity        string    `json:"severity"`
	Code            string    `json:"code"`
	EnvironmentSlug slug.Slug `json:"environment_slug"`
	PipelineSlug    slug.Slug `json:"pipeline_slug"`
	StepSlug        slug.Slug `json:"step_slug"`
	RequirementKey  string    `json:"requirement_key"`
	FieldPath       string    `json:"field_path"`
	NextAction      string    `json:"next_action"`
}

func Evaluate(pipelines []PipelineInput, environments []EnvironmentInput) Report {
	return evaluate(pipelines, environments, false)
}

func evaluate(pipelines []PipelineInput, environments []EnvironmentInput, includeRequirements bool) Report {
	report := Report{Status: "ready", Environments: make([]EnvironmentResult, len(environments))}
	if len(environments) == 0 {
		report.Status = "warning"
	} else if len(pipelines) == 0 {
		report.Status = "informational"
	}
	for i, env := range environments {
		report.Environments[i] = EnvironmentResult{EnvironmentSlug: env.Slug, Status: report.Status, Diagnostics: []Diagnostic{}}
		if includeRequirements {
			report.Environments[i].Requirements = []Requirement{}
		}
	}
	for _, pipeline := range pipelines {
		requirements := runtime.ExtractRequirements(pipeline.Spec)
		refs := make(map[slug.Slug]model.DataSourceRef, len(pipeline.Refs))
		for _, ref := range pipeline.Refs {
			refs[ref.Slug] = ref
		}
		type bindingKey struct{ env, ref slug.Slug }
		bindings := make(map[bindingKey]model.DataSourceBinding, len(pipeline.Bindings))
		for _, b := range pipeline.Bindings {
			bindings[bindingKey{b.EnvironmentSlug, b.DataSourceRefSlug}] = b
		}
		operations := make(map[slug.Slug]string, len(pipeline.Spec.Steps))
		for _, step := range pipeline.Spec.Steps {
			switch {
			case step.Query != nil:
				operations[step.Slug] = strings.TrimSpace(step.Query.OperationType)
			case step.Sink != nil:
				operations[step.Slug] = strings.TrimSpace(step.Sink.OperationType)
			}
		}
		for i, env := range environments {
			result := &report.Environments[i]
			add := func(key string, usage runtime.RequirementUsage, code, action string) {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{
					Severity: "error", Code: code, NextAction: action,
					EnvironmentSlug: env.Slug, PipelineSlug: requirements.Pipeline.Slug,
					StepSlug: usage.StepSlug, RequirementKey: key, FieldPath: usage.FieldPath,
				})
				result.Status = "blocked"
				report.Status = "blocked"
			}
			for _, req := range requirements.DataSources {
				before := len(result.Diagnostics)
				ref, exists := refs[req.Ref]
				binding, bound := bindings[bindingKey{env.Slug, req.Ref}]
				for _, usage := range req.Usages {
					problem := func(code, action string) { add(string(req.Ref), usage, code, action) }
					if !exists {
						problem("missing_ref", "create_ref")
						continue
					}
					checkBinding(ref, binding, bound, req.Capability, operations[usage.StepSlug], problem)
				}
				if includeRequirements {
					row := Requirement{Kind: "data_source_ref", Key: req.Ref.String(), Capability: req.Capability}
					if bound {
						descriptor, _ := catalog.DescriptorFor(binding.DataSourceType)
						row.Target = &Target{
							Slug: binding.DataSourceSlug, Name: binding.DataSourceName, Type: binding.DataSourceType,
							Active: binding.DataSourceActive, ReadOnly: binding.DataSourceReadOnly,
							SupportsCapability: supportsCapability(descriptor, req.Capability),
						}
					}
					result.addRequirement(row, req.Usages, before)
				}
			}
			for _, req := range requirements.EnvironmentVariables {
				before := len(result.Diagnostics)
				variable, ok := env.Variables[req.Key]
				if !ok {
					variable.Source = model.RuntimeVariableMissing
				}
				if !variable.Present || variable.TypeMismatch {
					code := "missing_variable"
					if includeRequirements && variable.TypeMismatch {
						code = "variable_type_mismatch"
					}
					for _, usage := range req.Usages {
						add(req.Key, usage, code, "set_environment_variable")
					}
				}
				if includeRequirements {
					result.addRequirement(Requirement{Kind: "runtime_variable", Key: req.Key, Variable: &variable}, req.Usages, before)
				}
			}
		}
	}
	return report
}

func (r *EnvironmentResult) addRequirement(row Requirement, usages []runtime.RequirementUsage, previousDiagnostics int) {
	row.Status = "ready"
	if len(r.Diagnostics) > previousDiagnostics {
		row.Status = "blocked"
	}
	row.Usages = make([]Usage, len(usages))
	for i, u := range usages {
		row.Usages[i] = Usage{StepSlug: u.StepSlug, FieldPath: u.FieldPath}
	}
	r.Requirements = append(r.Requirements, row)
}

func supportsCapability(d catalog.Descriptor, capability model.DataSourceRefCapability) bool {
	if capability == model.DataSourceRefCapabilityPipelineSink {
		return d.SupportsPipelineSink
	}
	return d.SupportsPipelineSource
}

func checkBinding(ref model.DataSourceRef, binding model.DataSourceBinding, bound bool, capability model.DataSourceRefCapability, operationType string, problem func(code, action string)) {
	if !slices.Contains(ref.RequiredCapabilities, capability) {
		problem("missing_capability", "update_ref_capabilities")
	}
	refType, knownRef := catalog.DescriptorFor(ref.DataSourceType)
	incompatibleRef := ref.DataSourceType != model.DataSourceTypeUnknown && (!knownRef || refType.QueryOperationType != operationType)
	if incompatibleRef {
		problem("invalid_ref_type", "update_ref_type")
	}
	if !bound {
		problem("missing_binding", "configure_binding")
		return
	}
	if !binding.DataSourceActive {
		problem("inactive_target", "activate_target")
	}
	sink := capability == model.DataSourceRefCapabilityPipelineSink
	if sink && binding.DataSourceReadOnly {
		problem("read_only_sink", "bind_writable_target")
	}
	descriptor, known := catalog.DescriptorFor(binding.DataSourceType)
	if !known || (ref.DataSourceType != model.DataSourceTypeUnknown && binding.DataSourceType != ref.DataSourceType) || (!incompatibleRef && descriptor.QueryOperationType != operationType) {
		problem("invalid_target_type", "bind_compatible_target")
	}
	if known && !supportsCapability(descriptor, capability) {
		problem("missing_capability", "bind_capable_target")
	}
}
