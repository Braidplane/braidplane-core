package pipelinesvc

import (
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestValidateBackfillWindows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, kind, lower, upper, operation string
		unsafe, extra, missing, wantError   bool
	}{
		{name: "HTTP nanoseconds", operation: "http", kind: "watermark", lower: `{"value":"2026-01-01T00:00:00.123456788Z"}`, upper: `{"value":"2026-01-01T00:00:00.123456789Z"}`},
		{name: "HTTP offsets", operation: "http", kind: "watermark", lower: `{"value":"2026-01-01T00:00:00+02:00"}`, upper: `{"value":"2026-01-01T00:00:00Z"}`},
		{name: "HTTP equal instants", operation: "http", kind: "watermark", lower: `{"value":"2026-01-01T02:00:00+02:00"}`, upper: `{"value":"2026-01-01T00:00:00Z"}`, wantError: true},
		{name: "HTTP reversed instants", operation: "http", kind: "watermark", lower: `{"value":"2026-01-01T00:00:00Z"}`, upper: `{"value":"2026-01-01T01:00:00+02:00"}`, wantError: true},
		{name: "HTTP text", operation: "http", kind: "watermark", lower: `{"value":"a"}`, upper: `{"value":"z"}`, wantError: true},
		{name: "HTTP number", operation: "http", kind: "watermark", lower: `{"value":1}`, upper: `{"value":5}`, wantError: true},
		{name: "HTTP lower precision", operation: "http", kind: "watermark", lower: `{"value":"2026-01-01T00:00:00.1234567891Z"}`, upper: `{"value":"2026-01-02T00:00:00Z"}`, wantError: true},
		{name: "HTTP upper precision", operation: "http", kind: "watermark", lower: `{"value":"2026-01-01T00:00:00Z"}`, upper: `{"value":"2026-01-02T00:00:00.1234567891Z"}`, wantError: true},
		{name: "HTTP cursor", operation: "http", kind: "cursor", lower: `{"cursor":"z"}`, upper: `{"cursor":"a"}`},
		{name: "SQL text", operation: "sql", kind: "watermark", lower: `{"value":"a"}`, upper: `{"value":"z"}`},
		{name: "hostile exponent", kind: "watermark", lower: `{"value":1e100000000}`, upper: `{"value":2e100000000}`, wantError: true},
		{name: "numeric strings", kind: "watermark", lower: `{"value":"9"}`, upper: `{"value":"10"}`},
		{name: "reversed numeric strings", kind: "watermark", lower: `{"value":"10"}`, upper: `{"value":"9"}`, wantError: true},
		{name: "numeric watermark", kind: "watermark", lower: `{"value":1}`, upper: `{"value":5}`},
		{name: "timestamp offsets", kind: "watermark", lower: `{"value":"2026-01-01T00:00:00+02:00"}`, upper: `{"value":"2026-01-01T00:00:00Z"}`},
		{name: "opaque cursors", kind: "cursor", lower: `{"cursor":"z"}`, upper: `{"cursor":"a"}`},
		{name: "reversed", kind: "watermark", lower: `{"value":5}`, upper: `{"value":1}`, wantError: true},
		{name: "equal", kind: "watermark", lower: `{"value":1}`, upper: `{"value":1}`, wantError: true},
		{name: "type mismatch", kind: "watermark", lower: `{"value":1}`, upper: `{"value":"5"}`, wantError: true},
		{name: "missing lower", kind: "watermark", upper: `{"value":5}`, wantError: true},
		{name: "null upper", kind: "watermark", lower: `{"value":1}`, upper: `null`, wantError: true},
		{name: "cursor wrong envelope", kind: "cursor", lower: `{"value":"a"}`, upper: `{"cursor":"b"}`, wantError: true},
		{name: "extra envelope fields", kind: "watermark", lower: `{"value":1,"extra":2}`, upper: `{"value":5}`, wantError: true},
		{name: "unsafe sink", kind: "watermark", lower: `{"value":1}`, upper: `{"value":5}`, unsafe: true, wantError: true},
		{name: "unknown source", kind: "watermark", lower: `{"value":1}`, upper: `{"value":5}`, extra: true, wantError: true},
		{name: "missing source", kind: "watermark", missing: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, err := model.IncrementalCheckpointKindString(tc.kind)
			require.NoError(t, err)
			pipeline := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{
				{Slug: "source", Query: &pipelinespec.QueryRef{OperationType: tc.operation, Incremental: &pipelinespec.IncrementalSourceContract{CheckpointKind: &kind}}},
				{Slug: "sink", Sink: &pipelinespec.SinkSpec{IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent}},
			}}
			if tc.unsafe {
				pipeline.Steps[1].Sink.IdempotencyClass = pipelinespec.SinkIdempotencyClassUnsafe
			}
			windows := map[slug.Slug]pipelinerun.IncrementalSourceWindow{"source": {LowerBound: json.RawMessage(tc.lower), UpperBound: json.RawMessage(tc.upper)}}
			if tc.extra {
				windows["unknown"] = windows["source"]
			}
			if tc.missing {
				delete(windows, "source")
			}
			err = validateBackfillWindows(pipeline, windows)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestHTTPCheckpointBoundDiagnostic(t *testing.T) {
	_, err := checkpointBoundValue(json.RawMessage(`{"value":"private-invalid-date"}`), "watermark", "http")
	require.ErrorIs(t, err, xerror.InvalidInput(xerror.PipelineRun))
	require.Contains(t, xerror.PublicDetail(err), "RFC3339")
	require.Contains(t, xerror.PublicDetail(err), "nine fractional second digits")
	require.NotContains(t, err.Error()+xerror.PublicDetail(err), "private-invalid-date")
}
