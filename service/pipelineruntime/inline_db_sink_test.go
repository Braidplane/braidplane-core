package pipelineruntime

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/writegate"
	"github.com/Braidplane/braidplane-core/pkg/decimal"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type inlineSinkExecutor struct {
	fakeSinkQueryExecutor
	inline func(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error)
}

func (e *inlineSinkExecutor) ExecuteInline(ctx context.Context, org, source slug.Slug, operation, values map[string]any) (*result.ExecutionResult, error) {
	if err := writegate.BeforeWrite(ctx); err != nil {
		return nil, err
	}
	return e.inline(ctx, org, source, operation, values)
}

func TestSinkRuntime_InlineDBSinkBindsNativeRows(t *testing.T) {
	t.Parallel()
	price, err := decimal.Parse("2.68")
	require.NoError(t, err)
	rows := []map[string]any{{"sku": "0001", "price": price}}
	operation := map[string]any{"type": "postgres", "sql": "INSERT INTO prices SELECT {{org_slug}}, sku, price FROM input"}
	var captured map[string]any
	calls := 0
	executor := &inlineSinkExecutor{inline: func(_ context.Context, org, source slug.Slug, actual, values map[string]any) (*result.ExecutionResult, error) {
		calls++
		require.Equal(t, must.Slug("acme"), org)
		require.Equal(t, must.Slug("acme-postgres"), source)
		require.Equal(t, operation, actual)
		captured = values
		return result.NewSQLResult("postgres", &result.SQLResult{RowCount: 1}, 0, nil), nil
	}}
	input := &SinkRuntimeInput{
		OrgSlug: must.Slug("acme"), SinkBackend: model.SinkBackendDB,
		SinkDataSourceSlug: must.Slug("acme-postgres"), SinkOperationType: "sql",
		SinkOperationInline: operation, SinkConfig: map[string]any{"org_slug": "acme"},
		SourceResult: pipelinerun.NewStepResult(result.NewSQLResult("postgres", &result.SQLResult{Rows: rows, ColumnNames: []string{"sku", "price"}, RowCount: 1}, 0, nil)),
	}
	ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{Claimed: true}}
	require.NoError(t, NewSinkRuntime(executor, ledger).Execute(t.Context(), input))
	require.Equal(t, 1, ledger.markCalls)
	require.Equal(t, pipelinerun.SinkEffectStateSucceeded, input.EffectState)
	require.Equal(t, 1, calls)
	require.Equal(t, "acme", captured["org_slug"])
	require.Equal(t, []string{"sku", "price"}, captured[sinkbinding.InternalInputColumns])
	require.Equal(t, rows, captured[sinkbinding.InternalInputRows])
	captured[sinkbinding.InternalInputRows].([]map[string]any)[0]["sku"] = "changed"
	require.Equal(t, "0001", rows[0]["sku"])
}

func TestSinkRuntime_InlineDBSinkRejectsAmbiguousTarget(t *testing.T) {
	t.Parallel()
	err := NewSinkRuntime(&fakeSinkQueryExecutor{}).Execute(t.Context(), &SinkRuntimeInput{
		SinkBackend: model.SinkBackendDB, SinkDataSourceSlug: must.Slug("acme-postgres"),
		SinkOperationRef: must.Slug("saved"), SinkOperationInline: map[string]any{"type": "postgres", "sql": "SELECT 1"},
	})
	require.ErrorIs(t, err, errSinkNotConfigured)
	require.ErrorContains(t, err, "exactly one")
}

func TestSinkRuntime_InlineDBSinkRequiresInlineExecutor(t *testing.T) {
	t.Parallel()
	err := NewSinkRuntime(&fakeSinkQueryExecutor{}).Execute(t.Context(), &SinkRuntimeInput{
		SinkBackend: model.SinkBackendDB, SinkDataSourceSlug: must.Slug("acme-postgres"),
		SinkOperationInline: map[string]any{"type": "postgres", "sql": "SELECT 1"},
		SourceResult:        pipelinerun.NewStepResult(result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": 1}}, ColumnNames: []string{"id"}, RowCount: 1}, 0, nil)),
	})
	require.ErrorIs(t, err, errSinkNotConfigured)
	require.ErrorContains(t, err, "inline sink executor")
}

func TestSinkRuntime_InlineDBSinkFailurePreservesUnknownEffect(t *testing.T) {
	t.Parallel()
	failure := fmt.Errorf("connector failed")
	executor := &inlineSinkExecutor{inline: func(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error) {
		return nil, failure
	}}
	ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{Claimed: true}}
	input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassNaturallyIdempotent)
	input.SinkBackend = model.SinkBackendDB
	input.SinkOperationRef = ""
	input.SinkOperationInline = map[string]any{"type": "postgres", "sql": "INSERT INTO prices SELECT id FROM input"}
	err := NewSinkRuntime(executor, ledger).Execute(t.Context(), input)
	require.ErrorIs(t, err, failure)
	require.Equal(t, "unknown", input.DeliveryOutcome)
	require.Equal(t, 0, ledger.markCalls)
}

func TestSinkRuntime_InlineDBSinkCannotWriteReadOnlySource(t *testing.T) {
	t.Parallel()
	err := NewSinkRuntime(&fakeSinkQueryExecutor{}).Execute(t.Context(), &SinkRuntimeInput{
		SinkBackend: model.SinkBackendDB, SinkDataSourceSlug: must.Slug("acme-postgres"),
		SinkDataSourceReadOnly: true, SinkOperationInline: map[string]any{"type": "postgres", "sql": "INSERT INTO prices SELECT id FROM input"},
	})
	require.ErrorIs(t, err, errSinkReadOnly)
}
