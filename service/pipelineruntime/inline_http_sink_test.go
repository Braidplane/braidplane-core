package pipelineruntime

import (
	"context"
	"fmt"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/stretchr/testify/require"
)

func TestSinkRuntime_InlineHTTPBindsRowsAndRecordsDelivery(t *testing.T) {
	t.Parallel()
	input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassIdempotentWithKey)
	input.SinkBackend = model.SinkBackendHTTP
	input.SinkOperationType = catalog.OperationTypeHTTP
	input.SinkOperationRef = ""
	input.SinkOperationInline = map[string]any{"type": "http", "method": "POST", "path": "/records", "body": "{{input}}"}
	ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{Claimed: true}}
	calls := 0
	executor := &inlineSinkExecutor{inline: func(_ context.Context, org, source slug.Slug, operation, values map[string]any) (*result.ExecutionResult, error) {
		calls++
		require.Equal(t, input.OrgSlug, org)
		require.Equal(t, input.SinkDataSourceSlug, source)
		require.Equal(t, input.SinkOperationInline, operation)
		require.NotEmpty(t, values[sinkbinding.Input])
		require.Equal(t, "rows", values[sinkbinding.InternalInputRepresentation])
		return result.NewHTTPResult(&result.HTTPResult{StatusCode: 201}, 0, nil), nil
	}}
	require.NoError(t, NewSinkRuntime(executor, ledger).Execute(t.Context(), input))
	require.Equal(t, 1, calls)
	require.Equal(t, "confirmed", input.DeliveryOutcome)
	require.Equal(t, 1, ledger.markCalls)
}

func TestSinkRuntime_InlineHTTPFailurePreservesUnknownDelivery(t *testing.T) {
	t.Parallel()
	failure := fmt.Errorf("synthetic connection failure")
	input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassIdempotentWithKey)
	input.SinkBackend = model.SinkBackendHTTP
	input.SinkOperationType = catalog.OperationTypeHTTP
	input.SinkOperationRef = ""
	input.SinkOperationInline = map[string]any{"type": "http", "method": "POST", "path": "/records", "body": "{{input}}"}
	ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{Claimed: true}}
	executor := &inlineSinkExecutor{inline: func(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error) {
		return nil, failure
	}}
	require.ErrorIs(t, NewSinkRuntime(executor, ledger).Execute(t.Context(), input), failure)
	require.Equal(t, "unknown", input.DeliveryOutcome)
	require.Zero(t, ledger.markCalls)
}

func TestSinkRuntime_InlineHTTPRequiresExecutorAndExclusiveOperation(t *testing.T) {
	t.Parallel()
	input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassIdempotentWithKey)
	input.SinkBackend = model.SinkBackendHTTP
	input.SinkOperationType = catalog.OperationTypeHTTP
	input.SinkOperationRef = ""
	input.SinkOperationInline = map[string]any{"type": "http", "method": "POST", "path": "/records", "body": "{{input}}"}
	runtime := NewSinkRuntime(&fakeSinkQueryExecutor{})
	err := runtime.Execute(t.Context(), input)
	require.ErrorIs(t, err, errSinkNotConfigured)
	require.ErrorContains(t, err, "inline sink executor is required")
	input.SinkOperationRef = "saved-operation"
	err = runtime.Execute(t.Context(), input)
	require.ErrorIs(t, err, errSinkNotConfigured)
	require.ErrorContains(t, err, "exactly one saved or inline operation")
}
