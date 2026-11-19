package views

import (
	"context"
	"slices"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func readinessBadge(status api.ReadinessStatus) string {
	switch status {
	case api.ReadinessStatusReady:
		return badgeSuccessOutline
	case api.ReadinessStatusBlocked:
		return badgeErrorOutline
	case api.ReadinessStatusWarning:
		return badgeWarningOutline
	default:
		return badgeInfoOutline
	}
}

// Index by both key and spec location: a field can reference several variables,
// and one logical ref can serve as both a source and a sink.
type readinessLocation struct{ key, path string }

func readinessReasons(diagnostics []api.ReadinessDiagnostic) map[readinessLocation][]string {
	reasons := make(map[readinessLocation][]string)
	for _, d := range diagnostics {
		location := readinessLocation{d.RequirementKey, d.FieldPath}
		reasons[location] = append(reasons[location], readinessReason(d.Code, d.NextAction))
	}
	return reasons
}

func readinessReason(code, action string) string {
	switch code {
	case "missing_ref":
		return "Logical reference missing. Create it with braidplane data-source-ref create, then bind it in this environment."
	case "missing_binding":
		return "Binding missing"
	case "invalid_ref_type":
		return "Logical reference has an incompatible type. Correct it with braidplane data-source-ref update."
	case "invalid_target_type":
		return "Bound target has an incompatible type"
	case "inactive_target":
		return "Bound target is inactive"
	case "read_only_sink":
		return "Sink requires a writable target"
	case "missing_capability":
		if action == "update_ref_capabilities" {
			return "Logical reference lacks the required capability. Add it with braidplane data-source-ref update."
		}
		return "Bound target lacks the required capability"
	case "missing_variable":
		return "Runtime variable missing"
	case "variable_type_mismatch":
		return "Runtime variable type mismatch"
	case "missing_operation":
		return "Saved operation missing on the bound data source"
	case "operation_wrong_data_source":
		return "Saved operation belongs to a different data source"
	case "operation_unbound":
		return "Bind the data-source reference to check this saved operation"
	case "incompatible_operation_type":
		return "Choose a saved operation compatible with the bound connector"
	case "missing_operation_capability":
		return "Choose a saved operation that supports this step’s source or sink role"
	case "incompatible_operation_contract":
		return "Update the saved operation or pipeline so their contracts agree"
	case "missing_operation_parameter":
		return "Supply the required saved-operation parameters in the pipeline"
	default:
		return strings.ReplaceAll(code, "_", " ")
	}
}

func requirementReasons(req api.ReadinessRequirement, reasons map[readinessLocation][]string) []string {
	var result []string
	for _, usage := range req.Usages {
		for _, reason := range reasons[readinessLocation{req.Key, usage.FieldPath}] {
			if !slices.Contains(result, reason) {
				result = append(result, reason)
			}
		}
	}
	return result
}

func readinessVariablesHref(org, project string, pipeline *api.Pipeline) string {
	if pipeline.CurrentVersionSpecHash != nil && *pipeline.CurrentVersionSpecHash != "" {
		return RuntimeVariableComparisonHref(org, project, pipeline.Slug, *pipeline.CurrentVersionSpecHash)
	}
	return RuntimeVariableComparisonHref(org, "", "", "")
}

func readinessBindingsHref(ctx context.Context, org, project, environment, ref string, pipeline *api.Pipeline) string {
	if pipeline.CurrentVersionSpecHash == nil || *pipeline.CurrentVersionSpecHash == "" {
		return ProjectEnvironmentConfigurationHref(org, project, environment, environment) + "#project-binding-matrix"
	}
	detail := PipelineDetailHrefWithEnvironment(org, project, environment, pipeline.Slug)
	return PipelineBindingSetupHref(org, project, environment, pipeline.Slug, *pipeline.CurrentVersionSpecHash,
		entitySectionHref(ctx, detail, "readiness")) + "#" + DataSourceBindingFormID("binding-cell", pipeline.Slug, ref, environment)
}
