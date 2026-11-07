package queriessvc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestWithIncrementalParamValuesExposesBoundFields(t *testing.T) {
	t.Parallel()

	params, err := withIncrementalParamValues(
		map[string]any{"user_param": "kept"},
		&pipelinerun.IncrementalCheckpointInput{
			LowerBound: json.RawMessage(`{"value":"2026-05-24T10:00:00Z","cursor":"cursor-1"}`),
			UpperBound: json.RawMessage(`"cursor-2"`),
		},
	)

	require.NoError(t, err)
	require.Equal(t, "kept", params["user_param"])
	require.Equal(t, "2026-05-24T10:00:00Z", params["runtime.incremental.lower_bound.value"])
	require.Equal(t, "cursor-1", params["runtime.incremental.lower_bound.cursor"])
	require.Equal(t, "cursor-2", params["runtime.incremental.upper_bound.value"])
	require.Equal(t, "cursor-2", params["runtime.incremental.upper_bound.cursor"])
}

func TestIncrementalObservationFromSQLWatermarkUsesMaxField(t *testing.T) {
	t.Parallel()

	checkpoint := incrementalObservationTestCheckpoint("watermark")
	checkpoint.Lookup.IncrementalField = "updated_at"
	checkpoint.LowerBound = json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`)
	execResult := result.NewSQLResult("postgres", &result.SQLResult{
		Rows: []map[string]any{
			{"id": int64(2), "updated_at": "2026-05-24T10:05:00Z"},
			{"id": int64(3), "updated_at": "2026-05-24T10:10:00Z"},
			{"id": int64(1), "updated_at": "2026-05-24T10:01:00Z"},
		},
		ColumnNames: []string{"id", "updated_at"},
		RowCount:    3,
	}, 12, nil)

	observation, err := incrementalObservationFromResult(
		execResult,
		checkpoint,
		time.Date(2026, time.May, 24, 10, 11, 0, 0, time.UTC),
	)

	require.NoError(t, err)
	require.Equal(t, slug.Slug("fetch-users"), observation.SourceStepSlug)
	require.JSONEq(t, `{"value":"2026-05-24T10:00:00Z"}`, string(observation.ObservedLowerBound))
	require.JSONEq(t, `{"value":"2026-05-24T10:10:00Z"}`, string(observation.ObservedUpperBound))
	require.JSONEq(t, `{"record_count":3}`, string(observation.DisplayMetadata))
}

func TestIncrementalObservationFromWatermarkComparesNumericStringsAsNumbers(t *testing.T) {
	t.Parallel()

	checkpoint := incrementalObservationTestCheckpoint("watermark")
	checkpoint.Lookup.IncrementalField = "updated_id"
	checkpoint.LowerBound = json.RawMessage(`{"value":"8"}`)
	execResult := result.NewSQLResult("mysql", &result.SQLResult{
		Rows: []map[string]any{
			{"id": int64(10), "updated_id": "10"},
			{"id": int64(9), "updated_id": "9"},
		},
		ColumnNames: []string{"id", "updated_id"},
		RowCount:    2,
	}, 12, nil)

	observation, err := incrementalObservationFromResult(execResult, checkpoint, time.Now())

	require.NoError(t, err)
	require.JSONEq(t, `{"value":"10"}`, string(observation.ObservedUpperBound))
}

func TestIncrementalObservationFromWatermarkWithoutRowsKeepsLowerBound(t *testing.T) {
	t.Parallel()

	checkpoint := incrementalObservationTestCheckpoint("watermark")
	checkpoint.Lookup.IncrementalField = "updated_at"
	checkpoint.LowerBound = json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`)
	execResult := result.NewSQLResult("postgres", &result.SQLResult{
		Rows:        []map[string]any{},
		ColumnNames: []string{"id", "updated_at"},
		RowCount:    0,
	}, 12, nil)

	observation, err := incrementalObservationFromResult(execResult, checkpoint, time.Now())

	require.NoError(t, err)
	require.JSONEq(t, `{"value":"2026-05-24T10:00:00Z"}`, string(observation.ObservedUpperBound))
	require.JSONEq(t, `{"record_count":0}`, string(observation.DisplayMetadata))
}

func TestIncrementalObservationFromMongoWatermarkUsesMaxField(t *testing.T) {
	t.Parallel()

	count := 3
	checkpoint := incrementalObservationTestCheckpoint("watermark")
	checkpoint.Lookup.IncrementalField = "updated_at"
	checkpoint.LowerBound = json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`)
	execResult := result.NewMongoDBResult(&result.MongoDBResult{
		OperationType: "find",
		Documents: []map[string]any{
			{"id": "2", "updated_at": "2026-05-24T10:05:00Z"},
			{"id": "3", "updated_at": "2026-05-24T10:10:00Z"},
			{"id": "1", "updated_at": "2026-05-24T10:01:00Z"},
		},
		DocumentCount: &count,
	}, 12, nil)

	observation, err := incrementalObservationFromResult(
		execResult,
		checkpoint,
		time.Date(2026, time.May, 24, 10, 11, 0, 0, time.UTC),
	)

	require.NoError(t, err)
	require.JSONEq(t, `{"value":"2026-05-24T10:10:00Z"}`, string(observation.ObservedUpperBound))
	require.JSONEq(t, `{"record_count":3}`, string(observation.DisplayMetadata))
}

func TestIncrementalObservationFromHTTPCursorUsesConfiguredResponseField(t *testing.T) {
	t.Parallel()

	checkpoint := incrementalObservationTestCheckpoint("cursor")
	checkpoint.Lookup.IncrementalCursor = "paging.next_page_token"
	checkpoint.LowerBound = json.RawMessage(`{"cursor":"cursor-1"}`)
	execResult := result.NewHTTPResult(&result.HTTPResult{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       json.RawMessage(`{"items":[],"paging":{"next_page_token":"cursor-2"}}`),
	}, 12, nil)

	observation, err := incrementalObservationFromResult(execResult, checkpoint, time.Now())

	require.NoError(t, err)
	require.JSONEq(t, `{"cursor":"cursor-1"}`, string(observation.ObservedLowerBound))
	require.JSONEq(t, `{"cursor":"cursor-2"}`, string(observation.ObservedUpperBound))
}

func TestIncrementalObservationFromHTTPCursorWithoutNextCursorKeepsLowerBound(t *testing.T) {
	t.Parallel()

	checkpoint := incrementalObservationTestCheckpoint("cursor")
	checkpoint.Lookup.IncrementalCursor = "paging.next_page_token"
	checkpoint.LowerBound = json.RawMessage(`{"cursor":"cursor-1"}`)
	execResult := result.NewHTTPResult(&result.HTTPResult{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       json.RawMessage(`{"items":[],"paging":{}}`),
	}, 12, nil)

	observation, err := incrementalObservationFromResult(execResult, checkpoint, time.Now())

	require.NoError(t, err)
	require.JSONEq(t, `{"cursor":"cursor-1"}`, string(observation.ObservedUpperBound))
}

func TestIncrementalObservationFromObjectRowsCursorUsesNextCursor(t *testing.T) {
	t.Parallel()

	nextCursor := "cursor-2"
	checkpoint := incrementalObservationTestCheckpoint("cursor")
	checkpoint.Lookup.IncrementalCursor = "after"
	checkpoint.LowerBound = json.RawMessage(`{"cursor":"cursor-1"}`)
	execResult := result.NewObjectRowsResult("hubspot", &result.ObjectRowsResult{
		ObjectType: "contacts",
		Operation:  "list",
		Rows:       []map[string]any{{"id": "201"}},
		RowCount:   1,
		NextCursor: &nextCursor,
		HasMore:    true,
	}, 12, nil)

	observation, err := incrementalObservationFromResult(execResult, checkpoint, time.Now())

	require.NoError(t, err)
	require.JSONEq(t, `{"cursor":"cursor-2"}`, string(observation.ObservedUpperBound))
	require.JSONEq(t, `{"record_count":1}`, string(observation.DisplayMetadata))
}

func incrementalObservationTestCheckpoint(kind string) *pipelinerun.IncrementalCheckpointInput {
	return &pipelinerun.IncrementalCheckpointInput{
		Lookup: pipelinerun.IncrementalCheckpointLookup{
			PipelineSlug:        "users-sync",
			SourceStepSlug:      "fetch-users",
			CheckpointNamespace: "normal",
			CheckpointKind:      kind,
		},
	}
}

func TestWithIncrementalParamValuesPreservesExactNumbers(t *testing.T) {
	t.Parallel()
	params, err := withIncrementalParamValues(nil, &pipelinerun.IncrementalCheckpointInput{LowerBound: json.RawMessage(`{"value":9007199254740993}`), UpperBound: json.RawMessage(`{"value":9007199254740993.0001}`)})
	require.NoError(t, err)
	require.Equal(t, json.Number("9007199254740993"), params["runtime.incremental.lower_bound.value"])
	require.Equal(t, json.Number("9007199254740993.0001"), params["runtime.incremental.upper_bound.value"])
	typed, err := buildTypedParams(nil, params)
	require.NoError(t, err)
	require.Equal(t, json.Number("9007199254740993"), typed["runtime.incremental.lower_bound.value"].Value)
	require.Equal(t, json.Number("9007199254740993.0001"), typed["runtime.incremental.upper_bound.value"].Value)
}
