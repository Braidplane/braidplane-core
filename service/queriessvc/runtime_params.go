package queriessvc

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func validateRuntimeExecutionData(ctx context.Context, data json.RawMessage, inline bool) error {
	text := string(data)
	if inline {
		for name := range pipelineruntime.LiteralBindingsFrom(ctx) {
			text = strings.ReplaceAll(text, "{{"+name+"}}", "bound literal")
		}
	}
	if err := validateRuntimeQueryContext(json.RawMessage(text), pipelineruntime.IsSinkExecution(ctx), pipelineruntime.IsIncrementalExecution(ctx)); err != nil {
		return err
	}
	var value any
	if json.Unmarshal([]byte(text), &value) != nil {
		value = text
	}
	snapshot, _ := pipelineruntime.ExecutionContextFrom(ctx)
	return pipelineruntime.ValidateExecutionAvailability(value, snapshot)
}

func validateRuntimeQueryData(data json.RawMessage) error {
	// Saved definitions can be used as sources or sinks; dispatch checks the actual context.
	return validateRuntimeQueryContext(data, true, true)
}

func validateRuntimeQueryContext(data json.RawMessage, sink, incremental bool) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		// Prepared SQL statements are plain text; saved operation envelopes are JSON.
		value = string(data)
	}
	if object, ok := value.(map[string]any); ok {
		if statement, ok := object["statement"].(string); ok {
			if err := pipelineruntime.ValidateOperationReferences(statement, sink, incremental); err != nil {
				return err
			}
		}
	}
	return pipelineruntime.ValidateRuntimeReferences(value, false, sink, incremental)
}

const (
	reservedRuntimeParamNamespace = pipelineruntime.Namespace
	runtimeRunDateParamName       = pipelineruntime.RunDateParamName
	runtimeRunDateTimeParamName   = pipelineruntime.RunDateTimeParamName
)

func validateReservedRuntimeParamNamespace(params []*QueryParameter) error {
	for _, param := range params {
		if param == nil {
			continue
		}

		if strings.HasPrefix(param.Name, reservedRuntimeParamNamespace) {
			return ErrInvalidInput.Detailf(
				"query parameter name %q uses reserved namespace %q",
				param.Name,
				reservedRuntimeParamNamespace,
			)
		}
	}

	return nil
}

func withReservedRuntimeParams(
	ctx context.Context,
	params map[string]types.TypedParam,
	now time.Time,
) (map[string]types.TypedParam, error) {
	merged := make(map[string]types.TypedParam, len(params)+2)
	for key, value := range params {
		merged[key] = value
	}

	runtimeNow := now.UTC()
	merged[runtimeRunDateParamName] = types.TypedParam{
		Value:    runtimeNow.Format("2006-01-02"),
		DataType: ParameterDataTypeDate.String(),
	}
	merged[runtimeRunDateTimeParamName] = types.TypedParam{
		Value:    runtimeNow.Format(time.RFC3339),
		DataType: ParameterDataTypeText.String(),
	}

	snapshot, pipeline := pipelineruntime.ExecutionContextFrom(ctx)
	for _, name := range []string{pipelineruntime.RunSlugParamName, "runtime.previous_success.exists", "runtime.previous_success.run_slug", "runtime.previous_success.started_at", "runtime.previous_success.finished_at"} {
		delete(merged, name)
		if pipeline && snapshot.RunSlug != "" {
			value, err := pipelineruntime.ResolveExecutionValue("{{"+name+"}}", snapshot)
			if err != nil {
				return nil, err
			}
			dataType := ParameterDataTypeText.String()
			if name == "runtime.previous_success.exists" {
				dataType = ParameterDataTypeBoolean.String()
			}
			merged[name] = types.TypedParam{Value: value, DataType: dataType}
		}
	}
	return merged, nil
}
