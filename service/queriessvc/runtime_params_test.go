package queriessvc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	runtimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/require"
)

func TestRuntimeQueryValidation(t *testing.T) {
	for _, expression := range []string{
		"{{runtime.unknown}}", "{{runtime.run.datetim}}", "{{runtime.previous_success.unknown}}",
		"{{runtime.sink.unknown}}", "{{runtime.incremental.invalid}}", "{{runtime.run.date",
		"{{runtime.run.date | add_days:1}}", "{{runtime.env.date | format:date}}",
	} {
		t.Run(expression, func(t *testing.T) {
			inline := map[string]any{"sql": "select " + expression}
			encoded, err := json.Marshal(inline)
			require.NoError(t, err)
			require.ErrorContains(t, validateRuntimeQueryData(encoded), "runtime date expression")
			_, err = prepareInlineExecution(t.Context(), inline)
			require.ErrorContains(t, err, "runtime date expression")
		})
	}
	for _, name := range []string{"runtime.env.customer", "runtime.input.customer", "runtime.secrets.API_TOKEN", "runtime.incremental.lower_bound.provider_field", "runtime.run.slug", "runtime.run.date"} {
		inline := map[string]any{"path": "{{" + name + "}}"}
		ctx := runtimevars.WithExecutionContext(runtimevars.WithSinkExecution(runtimevars.WithIncrementalExecution(t.Context())), runtimevars.ExecutionContext{RunSlug: "run-one"})
		prepared, err := prepareInlineExecution(ctx, inline)
		require.NoError(t, err)
		require.NoError(t, validateRuntimeQueryData(prepared.statementJSON))
		require.ErrorContains(t, validateQueryParams(prepared.statementJSON, nil), name)
		require.NoError(t, validateQueryParams(prepared.statementJSON, map[string]types.TypedParam{name: {Value: "value"}}))
	}
}

func TestLiteralBindingsRequireExecutionContext(t *testing.T) {
	inline := map[string]any{"path": "exports/{{runtime.literal.value_0}}"}
	_, err := prepareInlineExecution(t.Context(), inline)
	require.ErrorContains(t, err, "unknown runtime parameter runtime.literal.value_0")
	ctx := runtimevars.WithLiteralBindings(t.Context(), map[string]any{"runtime.literal.value_0": "{{runtime.unknown}}"})
	prepared, err := prepareInlineExecution(ctx, inline)
	require.NoError(t, err)
	require.NoError(t, validateRuntimeExecutionData(ctx, prepared.statementJSON, true))
	typed, err := buildTypedParams(nil, map[string]any{"value": runtimevars.LiteralString("{{runtime.unknown}}")})
	require.NoError(t, err)
	require.Equal(t, "{{runtime.unknown}}", typed["value"].Value)
}

func TestSavedDefinitionCannotUseInternalLiteralBindings(t *testing.T) {
	ctx := runtimevars.WithLiteralBindings(t.Context(), map[string]any{"runtime.literal.value_0": "synthetic-secret"})
	_, err := (&service{}).executePrepared(ctx, "org", "source", nil, &preparedExecution{statementJSON: json.RawMessage(`{"sql":"SELECT {{runtime.literal.value_0}}"}`)}, model.SystemEventActor())
	require.ErrorContains(t, err, "unknown runtime parameter runtime.literal.value_0")
}

func TestEncodedUnknownRuntimeReferenceRejectedBeforeSaving(t *testing.T) {
	data, err := json.Marshal(map[string]any{"statement": `{"sql":"SELECT \u007b\u007bruntime.unknown\u007d\u007d"}`})
	require.NoError(t, err)
	require.ErrorContains(t, validateRuntimeQueryData(data), "unknown runtime parameter runtime.unknown")
}

func TestProvidedParametersCannotInventExecutionContext(t *testing.T) {
	for _, inline := range []bool{false, true} {
		for _, name := range []string{"runtime.run.slug", "runtime.incremental.lower_bound.cursor"} {
			statement, err := json.Marshal(map[string]any{"sql": "SELECT {{" + name + "}}"})
			require.NoError(t, err)
			_, err = (&service{}).executePrepared(t.Context(), "org", "source", map[string]any{name: "invented"}, &preparedExecution{inline: inline, statementJSON: statement}, model.SystemEventActor())
			require.ErrorContains(t, err, name)
		}
	}
}

func TestPipelineMetadataUsesTrustedContextForSavedAndInlineQueries(t *testing.T) {
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, prior := range []*runtimevars.SuccessfulRun{nil, {RunSlug: "prior", StartedAt: at.Add(-time.Hour), FinishedAt: at}} {
		snapshot := runtimevars.ExecutionContext{RunSlug: "current", At: at, PreviousSuccess: prior}
		ctx := runtimevars.WithExecutionContext(t.Context(), snapshot)
		for _, inline := range []bool{false, true} {
			statement := json.RawMessage(`{"sql":"SELECT {{runtime.run.slug}}, {{runtime.previous_success.exists}}, {{runtime.previous_success.finished_at}}"}`)
			require.NoError(t, validateRuntimeQueryData(statement))
			require.NoError(t, validateRuntimeExecutionData(ctx, statement, inline))
			require.ErrorContains(t, validateRuntimeExecutionData(t.Context(), statement, inline), "requires pipeline execution")
			typed, err := withReservedRuntimeParams(ctx, map[string]types.TypedParam{runtimevars.RunSlugParamName: {Value: "invented"}}, at)
			require.NoError(t, err)
			require.Equal(t, "current", typed[runtimevars.RunSlugParamName].Value)
			require.Equal(t, prior != nil, typed["runtime.previous_success.exists"].Value)
			require.Equal(t, ParameterDataTypeBoolean.String(), typed["runtime.previous_success.exists"].DataType)
			if prior == nil {
				require.Nil(t, typed["runtime.previous_success.finished_at"].Value)
			} else {
				require.Equal(t, at.Format(time.RFC3339), typed["runtime.previous_success.finished_at"].Value)
			}
			require.NoError(t, validateQueryParams(statement, typed))
		}
	}
	standalone, err := withReservedRuntimeParams(t.Context(), map[string]types.TypedParam{runtimevars.RunSlugParamName: {Value: "invented"}, "runtime.previous_success.exists": {Value: true}}, at)
	require.NoError(t, err)
	require.NotContains(t, standalone, runtimevars.RunSlugParamName)
	require.NotContains(t, standalone, "runtime.previous_success.exists")
}
