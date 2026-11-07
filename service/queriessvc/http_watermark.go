package queriessvc

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
)

func observeHTTPTimestampWatermark(response *result.HTTPResult, checkpoint *pipelinerun.IncrementalCheckpointInput) (json.RawMessage, int, error) {
	invalid := ErrInvalidInput.Detail("HTTP watermark requires a successful JSON response with an RFC3339 timestamp at every configured field path").WithPublicDetail("HTTP watermark requires a successful JSON response with an RFC3339 timestamp at every configured field path")
	if response == nil || response.StatusCode < 200 || response.StatusCode >= 300 || response.File != nil {
		return nil, 0, invalid
	}
	rows, _, err := payload.ParseJSONBodyToRowsPreserveNumbers(response.Body)
	if err != nil || checkpoint.Lookup.IncrementalField == "" {
		return nil, 0, invalid
	}
	var maximum time.Time
	count := 0
	for _, row := range rows {
		values, _, missing, err := payload.ResolveFieldPathValues(row, checkpoint.Lookup.IncrementalField)
		if err != nil || missing {
			return nil, 0, invalid
		}
		for _, value := range values {
			parsed, ok := pipelinerun.HTTPWatermarkTime(value)
			if !ok {
				return nil, 0, invalid
			}
			if count == 0 || parsed.After(maximum) {
				maximum = parsed
			}
			count++
		}
	}
	// An explicit backfill bound never bypasses response validation.
	if len(checkpoint.UpperBound) > 0 && strings.TrimSpace(string(checkpoint.UpperBound)) != "null" {
		var bound struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(checkpoint.UpperBound, &bound) != nil {
			return nil, 0, invalid
		}
		upper, ok := pipelinerun.HTTPWatermarkTime(bound.Value)
		if !ok || (count > 0 && maximum.After(upper)) {
			return nil, 0, invalid
		}
		return cloneRawJSON(checkpoint.UpperBound), count, nil
	}
	if count == 0 {
		return cloneRawJSON(checkpoint.LowerBound), 0, nil
	}
	if len(checkpoint.LowerBound) > 0 && strings.TrimSpace(string(checkpoint.LowerBound)) != "null" {
		var lower struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(checkpoint.LowerBound, &lower) != nil {
			return nil, 0, invalid
		}
		previous, ok := pipelinerun.HTTPWatermarkTime(lower.Value)
		if !ok {
			return nil, 0, invalid
		}
		if maximum.Before(previous) {
			return cloneRawJSON(checkpoint.LowerBound), count, nil
		}
	}
	bound, err := marshalIncrementalBound(map[string]any{"value": maximum.UTC().Format(time.RFC3339Nano)})
	return bound, count, err
}
