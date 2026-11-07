package pipelineruntime

import (
	"context"
	"fmt"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestSinkRuntime_InlineS3BindsPayloadAndRecordsDelivery(t *testing.T) {
	t.Parallel()
	input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassDeduplicatedByLedger)
	input.SinkBackend = model.SinkBackendS3
	input.SinkOperationType = "s3"
	input.SinkOperationRef = ""
	input.SinkOperationInline = map[string]any{"type": "s3", "operation": "write", "key": "output.csv", "format": "csv"}
	input.SinkConfig = map[string]any{"key": "output.csv", "format": "csv", "overwrite": true}
	ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{Claimed: true}}
	executor := &inlineSinkExecutor{inline: func(_ context.Context, org, source slug.Slug, operation, values map[string]any) (*result.ExecutionResult, error) {
		require.Equal(t, input.OrgSlug, org)
		require.Equal(t, input.SinkDataSourceSlug, source)
		require.Equal(t, input.SinkOperationInline, operation)
		require.NotEmpty(t, values[sinkbinding.Input])
		require.Equal(t, "output.csv", values["key"])
		require.Equal(t, "csv", values["format"])
		require.Equal(t, true, values["overwrite"])
		return result.NewS3Result(&result.FileResult{Operation: "write", TargetPath: "output.csv"}, 0, nil), nil
	}}
	require.NoError(t, NewSinkRuntime(executor, ledger).Execute(t.Context(), input))
	require.Equal(t, "confirmed", input.DeliveryOutcome)
	require.Equal(t, 1, ledger.markCalls)
	require.NotNil(t, input.SinkResult)
}

func TestSinkRuntime_InlineS3RequiresExecutorAndExclusiveOperation(t *testing.T) {
	t.Parallel()
	input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassDeduplicatedByLedger)
	input.SinkBackend = model.SinkBackendS3
	input.SinkOperationRef = ""
	input.SinkOperationInline = map[string]any{"type": "s3", "operation": "write", "key": "output.csv"}
	input.SinkConfig = map[string]any{"key": "output.csv"}
	runtime := NewSinkRuntime(&fakeSinkQueryExecutor{})
	err := runtime.Execute(t.Context(), input)
	require.ErrorIs(t, err, errSinkNotConfigured)
	require.ErrorContains(t, err, "inline sink executor is required")
	input.SinkOperationRef = "saved-operation"
	err = runtime.Execute(t.Context(), input)
	require.ErrorIs(t, err, errSinkNotConfigured)
	require.ErrorContains(t, err, "exactly one saved or inline operation")
}

func TestSinkRuntime_InlineS3FailurePreservesUnknownDelivery(t *testing.T) {
	t.Parallel()
	failure := fmt.Errorf("synthetic provider failure")
	input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassDeduplicatedByLedger)
	input.SinkBackend = model.SinkBackendS3
	input.SinkOperationRef = ""
	input.SinkOperationInline = map[string]any{"type": "s3", "operation": "write", "key": "output.csv"}
	input.SinkConfig = map[string]any{"key": "output.csv"}
	ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{Claimed: true}}
	executor := &inlineSinkExecutor{inline: func(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error) {
		return nil, failure
	}}
	require.ErrorIs(t, NewSinkRuntime(executor, ledger).Execute(t.Context(), input), failure)
	require.Equal(t, "unknown", input.DeliveryOutcome)
	require.Zero(t, ledger.markCalls)
}

func TestSinkRuntime_InvalidS3ConfigDoesNotClaimEffect(t *testing.T) {
	for _, config := range []map[string]any{nil, {"key": " "}, {"key": "out.json", "metadata": map[string]any{"private-endpoint": 1}}} {
		input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassUnsafe)
		input.SinkBackend = model.SinkBackendS3
		input.SinkOperationRef = ""
		input.SinkOperationInline = map[string]any{"operation": "write", "key": "private-object"}
		input.SinkConfig = config
		ledger := &fakeSinkEffectLedger{}
		executor := &inlineSinkExecutor{inline: func(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error) {
			t.Fatal("invalid configuration must not dispatch")
			return nil, xerror.Unexpected().Detail("invalid configuration reached connector")
		}}
		for range 2 {
			err := NewSinkRuntime(executor, ledger).Execute(t.Context(), input)
			require.ErrorIs(t, err, xerror.InvalidInput(xerror.Pipeline))
			require.Equal(t, "failed", input.DeliveryOutcome)
			require.Empty(t, input.ReplayDecision)
			require.Zero(t, ledger.beginCalls)
			require.Zero(t, ledger.markCalls)
			code := sinkFailureReasonCode(err, pipelinespec.Step{}, failureKindPlatform)
			require.Equal(t, pipelinerun.FailureReasonCodeSinkContractMismatch, code)
			require.Contains(t, dataSourceFailureSummary(err, code), "sink.config.")
			require.NotContains(t, dataSourceFailureSummary(err, code), "private-")
		}
	}
}
