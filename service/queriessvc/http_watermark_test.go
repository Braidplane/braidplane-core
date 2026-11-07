package queriessvc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/stretchr/testify/require"
)

func TestHTTPWatermarkObservation(t *testing.T) {
	for _, tt := range []struct {
		name, body, field, upper, want string
		invalid                        bool
	}{
		{name: "array", body: `[{"updated_at":"2026-05-24T12:00:00+02:00"},{"updated_at":"2026-05-24T10:00:00.123456789Z"}]`, field: "updated_at", want: `{"value":"2026-05-24T10:00:00.123456789Z"}`},
		{name: "envelope", body: `{"items":[{"updated_at":"2026-05-24T10:00:00Z"},{"updated_at":"2026-05-24T10:00:00Z"}],"next":null}`, field: "items[].updated_at", want: `{"value":"2026-05-24T10:00:00Z"}`},
		{name: "empty", body: `{"items":[]}`, field: "items[].updated_at", want: `{"value":"2026-05-24T09:00:00Z"}`},
		{name: "empty array", body: `[]`, field: "updated_at", want: `{"value":"2026-05-24T09:00:00Z"}`},
		{name: "overlap only", body: `[{"updated_at":"2026-05-24T08:58:00Z"}]`, field: "updated_at", want: `{"value":"2026-05-24T09:00:00Z"}`},
		{name: "missing", body: `{"items":[{"updated_at":"2026-05-24T10:00:00Z"},{}]}`, field: "items[].updated_at", invalid: true},
		{name: "missing array", body: `{}`, field: "items[].updated_at", invalid: true},
		{name: "null", body: `[{"updated_at":null}]`, field: "updated_at", invalid: true},
		{name: "invalid", body: `[{"updated_at":"secret-invalid-time"}]`, field: "updated_at", invalid: true},
		{name: "precision loss", body: `[{"updated_at":"2026-05-24T10:00:00.1234567891Z"}]`, field: "updated_at", invalid: true},
		{name: "single digit hour precision", body: `[{"updated_at":"2026-05-24T1:00:00.1234567891Z"}]`, field: "updated_at", invalid: true},
		{name: "invalid zone hour", body: `[{"updated_at":"2026-05-24T10:00:00+24:00"}]`, field: "updated_at", invalid: true},
		{name: "invalid zone minute", body: `[{"updated_at":"2026-05-24T10:00:00+00:60"}]`, field: "updated_at", invalid: true},
		{name: "scalar", body: `"2026-05-24T10:00:00Z"`, field: "updated_at", invalid: true},
		{name: "backfill invalid", body: `[{"updated_at":"bad"}]`, field: "updated_at", upper: `{"value":"2026-05-24T11:00:00Z"}`, invalid: true},
		{name: "upper precision", body: `[]`, field: "updated_at", upper: `{"value":"2026-05-24T11:00:00.1234567891Z"}`, invalid: true},
		{name: "outside upper", body: `[{"updated_at":"2026-05-24T12:00:00Z"}]`, field: "updated_at", upper: `{"value":"2026-05-24T11:00:00Z"}`, invalid: true},
		{name: "backfill", body: `[{"updated_at":"2026-05-24T10:00:00Z"}]`, field: "updated_at", upper: `{"value":"2026-05-24T11:00:00Z"}`, want: `{"value":"2026-05-24T11:00:00Z"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkpoint := incrementalObservationTestCheckpoint("watermark")
			checkpoint.Lookup.IncrementalField = tt.field
			checkpoint.LowerBound = json.RawMessage(`{"value":"2026-05-24T09:00:00Z"}`)
			checkpoint.UpperBound = json.RawMessage(tt.upper)
			observation, err := incrementalObservationFromResult(result.NewHTTPResult(&result.HTTPResult{StatusCode: 200, Body: json.RawMessage(tt.body)}, 0, nil), checkpoint, time.Now())
			if tt.invalid {
				require.Error(t, err)
				require.Nil(t, observation)
				require.NotContains(t, err.Error(), "secret-invalid-time")
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(observation.ObservedUpperBound))
		})
	}
}

func TestHTTPWatermarkLookbackPreservesCommittedBoundary(t *testing.T) {
	checkpoint := incrementalObservationTestCheckpoint("watermark")
	checkpoint.Lookup.OperationType = "http"
	checkpoint.Lookup.IncrementalLookback = "5m"
	checkpoint.LowerBound = json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`)
	values, err := withIncrementalParamValues(nil, checkpoint)
	require.NoError(t, err)
	require.Equal(t, "2026-05-24T09:55:00Z", values["runtime.incremental.lower_bound.value"])
	require.JSONEq(t, `{"value":"2026-05-24T10:00:00Z"}`, string(checkpoint.LowerBound))
	checkpoint.LowerBound = json.RawMessage(`{"value":"bad"}`)
	_, err = withIncrementalParamValues(nil, checkpoint)
	require.Error(t, err)
}

func TestHTTPWatermarkInitialNullCheckpoint(t *testing.T) {
	checkpoint := incrementalObservationTestCheckpoint("watermark")
	checkpoint.Lookup.OperationType = "http"
	checkpoint.Lookup.IncrementalField = "items[].updated_at"
	checkpoint.Lookup.IncrementalLookback = "5m"
	checkpoint.LowerBound = json.RawMessage(`null`)
	checkpoint.UpperBound = json.RawMessage(`null`)
	values, err := withIncrementalParamValues(nil, checkpoint)
	require.NoError(t, err)
	require.Nil(t, values["runtime.incremental.lower_bound.value"])
	observation, err := incrementalObservationFromResult(result.NewHTTPResult(&result.HTTPResult{StatusCode: 200, Body: json.RawMessage(`{"items":[{"updated_at":"2026-05-24T10:00:00Z"}]}`)}, 0, nil), checkpoint, time.Now())
	require.NoError(t, err)
	require.JSONEq(t, `{"value":"2026-05-24T10:00:00Z"}`, string(observation.ObservedUpperBound))
}
