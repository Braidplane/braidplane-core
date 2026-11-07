package queriessvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	runtimeIncrementalLowerBoundParamName = reservedRuntimeParamNamespace + "incremental.lower_bound"
	runtimeIncrementalUpperBoundParamName = reservedRuntimeParamNamespace + "incremental.upper_bound"
)

func (s *service) ExecuteIncremental(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	paramValues map[string]any,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	ctx = pipelineruntime.WithIncrementalExecution(ctx)
	preparedResult, err := s.prepareSavedQueryExecution(ctx, orgSlug, dsSlug, querySlug)
	if err != nil {
		return nil, nil, xerror.Wrapf("execute incremental query: %w", err)
	}

	paramValues, err = withIncrementalParamValues(paramValues, checkpoint)
	if err != nil {
		return nil, nil, err
	}
	execResult, err := s.executePrepared(
		ctx,
		orgSlug,
		dsSlug,
		paramValues,
		preparedResult,
		model.SystemEventActor(),
	)
	if err != nil {
		return nil, nil, xerror.Wrapf("execute incremental query: %w", err)
	}

	observation, err := incrementalObservationFromResult(execResult, checkpoint, s.clock.Now())
	if err != nil {
		return nil, nil, xerror.Wrapf("observe incremental query checkpoint: %w", err)
	}

	return execResult, observation, nil
}

func (s *service) ExecuteInlineIncremental(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	operationInline map[string]any,
	paramValues map[string]any,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	ctx = pipelineruntime.WithIncrementalExecution(ctx)
	preparedResult, err := prepareInlineExecution(ctx, operationInline)
	if err != nil {
		return nil, nil, xerror.Wrapf("execute inline incremental query: %w", err)
	}

	paramValues, err = withIncrementalParamValues(paramValues, checkpoint)
	if err != nil {
		return nil, nil, err
	}
	execResult, err := s.executePrepared(
		ctx,
		orgSlug,
		dsSlug,
		paramValues,
		preparedResult,
		model.SystemEventActor(),
	)
	if err != nil {
		return nil, nil, xerror.Wrapf("execute inline incremental query: %w", err)
	}

	observation, err := incrementalObservationFromResult(execResult, checkpoint, s.clock.Now())
	if err != nil {
		return nil, nil, xerror.Wrapf("observe inline incremental query checkpoint: %w", err)
	}

	return execResult, observation, nil
}

func withIncrementalParamValues(
	paramValues map[string]any,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (map[string]any, error) {
	merged := make(map[string]any, len(paramValues)+8)
	for key, value := range paramValues {
		merged[key] = value
	}

	if checkpoint == nil {
		addIncrementalBoundParams(merged, runtimeIncrementalLowerBoundParamName, nil)
		addIncrementalBoundParams(merged, runtimeIncrementalUpperBoundParamName, nil)
		return merged, nil
	}

	addIncrementalBoundParams(merged, runtimeIncrementalLowerBoundParamName, checkpoint.LowerBound)
	addIncrementalBoundParams(merged, runtimeIncrementalUpperBoundParamName, checkpoint.UpperBound)
	if checkpoint.Lookup.OperationType == "http" && checkpoint.Lookup.CheckpointKind == "watermark" && checkpoint.Lookup.IncrementalLookback != "" && merged[runtimeIncrementalLowerBoundParamName+".value"] != nil {
		duration, err := time.ParseDuration(checkpoint.Lookup.IncrementalLookback)
		timestamp, ok := pipelinerun.HTTPWatermarkTime(merged[runtimeIncrementalLowerBoundParamName+".value"])
		if err != nil || duration <= 0 || !ok {
			return nil, ErrInvalidInput.With(err).WithPublicDetail("HTTP timestamp lookback requires a positive duration and an RFC3339 lower bound")
		}
		shifted := map[string]any{"value": timestamp.Add(-duration).UTC().Format(time.RFC3339Nano)}
		raw, err := json.Marshal(shifted)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		addIncrementalBoundParams(merged, runtimeIncrementalLowerBoundParamName, raw)
	}
	return merged, nil
}

func addIncrementalBoundParams(params map[string]any, prefix string, raw json.RawMessage) {
	if len(raw) == 0 {
		params[prefix] = nil
		params[prefix+".value"] = nil
		params[prefix+".cursor"] = nil
		return
	}

	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		params[prefix] = string(raw)
		params[prefix+".value"] = string(raw)
		params[prefix+".cursor"] = string(raw)
		return
	}

	params[prefix] = decoded
	switch value := decoded.(type) {
	case map[string]any:
		for field, fieldValue := range value {
			params[prefix+"."+field] = fieldValue
		}
		if _, ok := params[prefix+".value"]; !ok {
			params[prefix+".value"] = nil
		}
		if _, ok := params[prefix+".cursor"]; !ok {
			params[prefix+".cursor"] = nil
		}
	default:
		params[prefix+".value"] = value
		params[prefix+".cursor"] = value
	}
}

func incrementalObservationFromResult(
	execResult *result.ExecutionResult,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
	observedAt time.Time,
) (*pipelinerun.IncrementalCheckpointObservation, error) {
	if checkpoint == nil {
		return nil, ErrInvalidInput.Detail("incremental checkpoint input is required")
	}
	if execResult == nil {
		return nil, ErrInvalidInput.Detail("incremental checkpoint requires execution result")
	}
	if execResult.Error() != nil {
		return nil, execResult.Error()
	}

	upperBound := cloneRawJSON(checkpoint.UpperBound)
	recordCount := incrementalResultRecordCount(execResult)
	httpResult, isHTTP := execResult.HTTP()
	if isHTTP && checkpoint.Lookup.CheckpointKind == "watermark" {
		var err error
		upperBound, recordCount, err = observeHTTPTimestampWatermark(httpResult, checkpoint)
		if err != nil {
			return nil, err
		}
	} else if len(upperBound) == 0 {
		var err error
		switch checkpoint.Lookup.CheckpointKind {
		case "watermark":
			upperBound, err = observeWatermarkUpperBound(execResult, checkpoint)
		case "cursor":
			upperBound, err = observeCursorUpperBound(execResult, checkpoint)
		default:
			return nil, ErrInvalidInput.Detailf(
				"unsupported incremental checkpoint kind %q",
				checkpoint.Lookup.CheckpointKind,
			)
		}
		if err != nil {
			return nil, err
		}
	}

	displayMetadata, err := json.Marshal(map[string]any{
		"record_count": recordCount,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &pipelinerun.IncrementalCheckpointObservation{
		SourceStepSlug:      checkpoint.Lookup.SourceStepSlug,
		CheckpointNamespace: checkpoint.Lookup.CheckpointNamespace,
		CheckpointKind:      checkpoint.Lookup.CheckpointKind,
		ObservedLowerBound:  cloneRawJSON(checkpoint.LowerBound),
		ObservedUpperBound:  upperBound,
		DisplayMetadata:     displayMetadata,
		ObservedAt:          observedAt.UTC(),
	}, nil
}

func observeWatermarkUpperBound(
	execResult *result.ExecutionResult,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (json.RawMessage, error) {
	field := strings.TrimSpace(checkpoint.Lookup.IncrementalField)
	if field == "" {
		return nil, ErrInvalidInput.Detail("watermark incremental checkpoint requires field")
	}

	rows := incrementalResultRows(execResult)
	var maxValue any
	hasMaxValue := false
	for _, row := range rows {
		value, ok := row[field]
		if !ok || value == nil {
			continue
		}
		if !hasMaxValue || compareIncrementalValues(value, maxValue) > 0 {
			maxValue = value
			hasMaxValue = true
		}
	}

	if !hasMaxValue {
		return cloneRawJSON(checkpoint.LowerBound), nil
	}

	return marshalIncrementalBound(map[string]any{"value": maxValue})
}

func observeCursorUpperBound(
	execResult *result.ExecutionResult,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (json.RawMessage, error) {
	cursorField := strings.TrimSpace(checkpoint.Lookup.IncrementalCursor)
	if cursorField == "" {
		return nil, ErrInvalidInput.Detail("cursor incremental checkpoint requires cursor")
	}
	if execResult == nil {
		return nil, ErrInvalidInput.Detail("cursor incremental checkpoint requires execution result")
	}

	if objectRowsResult, ok := execResult.ObjectRows(); ok && objectRowsResult != nil {
		if objectRowsResult.NextCursor == nil || *objectRowsResult.NextCursor == "" {
			return cloneRawJSON(checkpoint.LowerBound), nil
		}
		return marshalIncrementalBound(map[string]any{"cursor": *objectRowsResult.NextCursor})
	}

	if httpResult, ok := execResult.HTTP(); ok && httpResult != nil {
		var body any
		if err := json.Unmarshal(httpResult.Body, &body); err != nil {
			return nil, xerror.Unexpected().Detailf("decode HTTP cursor response: %w", err)
		}
		cursorValue, ok := lookupIncrementalPath(body, cursorField)
		if !ok || cursorValue == nil || fmt.Sprint(cursorValue) == "" {
			return cloneRawJSON(checkpoint.LowerBound), nil
		}
		return marshalIncrementalBound(map[string]any{"cursor": cursorValue})
	}

	return nil, ErrInvalidInput.Detailf(
		"cursor incremental checkpoint is not supported for result type %s",
		execResult.Type(),
	)
}

func incrementalResultRows(execResult *result.ExecutionResult) []map[string]any {
	if execResult == nil {
		return nil
	}
	if sqlResult, ok := execResult.SQL(); ok && sqlResult != nil {
		return sqlResult.Rows
	}
	if mongoResult, ok := execResult.MongoDB(); ok && mongoResult != nil {
		return mongoResult.Documents
	}
	if objectRowsResult, ok := execResult.ObjectRows(); ok && objectRowsResult != nil {
		return objectRowsResult.Rows
	}
	if fileResult, ok := execResult.File(); ok && fileResult != nil {
		return fileResult.Rows
	}
	return nil
}

func incrementalResultRecordCount(execResult *result.ExecutionResult) int {
	if execResult == nil {
		return 0
	}
	if sqlResult, ok := execResult.SQL(); ok && sqlResult != nil {
		return sqlResult.RowCount
	}
	if mongoResult, ok := execResult.MongoDB(); ok && mongoResult != nil && mongoResult.DocumentCount != nil {
		return *mongoResult.DocumentCount
	}
	if objectRowsResult, ok := execResult.ObjectRows(); ok && objectRowsResult != nil {
		return objectRowsResult.RowCount
	}
	if fileResult, ok := execResult.File(); ok && fileResult != nil {
		return fileResult.RowCount
	}
	return 0
}

func lookupIncrementalPath(value any, path string) (any, bool) {
	current := value
	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func compareIncrementalValues(left, right any) int {
	if leftTime, leftOK := incrementalTime(left); leftOK {
		if rightTime, rightOK := incrementalTime(right); rightOK {
			return leftTime.Compare(rightTime)
		}
	}

	if leftFloat, leftOK := incrementalFloat(left); leftOK {
		if rightFloat, rightOK := incrementalFloat(right); rightOK {
			switch {
			case leftFloat < rightFloat:
				return -1
			case leftFloat > rightFloat:
				return 1
			default:
				return 0
			}
		}
	}

	return strings.Compare(fmt.Sprint(left), fmt.Sprint(right))
}

func incrementalTime(value any) (time.Time, bool) {
	switch v := value.(type) {
	case time.Time:
		return v, true
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, v)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func incrementalFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		parsed, err := strconv.ParseFloat(string(v), 64)
		return parsed, err == nil
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func marshalIncrementalBound(value any) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return data, nil
}

func cloneRawJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}
