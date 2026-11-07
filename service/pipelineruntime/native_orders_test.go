package pipelineruntime

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/stretchr/testify/require"
)

func TestNativeOrdersContractAndOracle(t *testing.T) {
	t.Parallel()
	readFixture := func(name string, target any) {
		t.Helper()
		raw, err := os.ReadFile("../../doc/etl-golden-path/native-orders/" + name)
		require.NoError(t, err)
		require.NoError(t, jsonvalue.Unmarshal(raw, target))
	}
	var rows, expected []map[string]any
	readFixture("source-rows.json", &rows)
	readFixture("expected-v2.json", &expected)
	jsonvalue.Normalize(rows)
	for _, negative := range []bool{false, true} {
		t.Run(map[bool]string{false: "signed projection matches v2", true: "retained arithmetic rejects integer promise"}[negative], func(t *testing.T) {
			var pipeline pipelinespec.Pipeline
			readFixture("orders.pipeline.json", &pipeline)
			pipeline.Steps[0].Query.DataSourceSlug = "orders-source"
			pipeline.Steps[2].Sink.DataSourceSlug = "orders-export"
			if negative {
				pipeline.Steps[1].Transform.SQL = strings.Replace(pipeline.Steps[1].Transform.SQL, "CAST(amount_cents - discount_cents AS SIGNED)", "amount_cents - discount_cents", 1)
			}
			require.NoError(t, pipelinevalidate.Validate(&pipeline), "offline validation lacks source schema types")
			queryCalls, sinkCalls := 0, 0
			store := &fakeRunStore{}
			runner := mustNewRunnerWithStore(&testutil.FakeQueryExecutor{ExecuteInlineFn: func(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error) {
				queryCalls++
				return result.NewSQLResult("postgres", &result.SQLResult{Rows: rows, RowCount: len(rows), ColumnNames: []string{"order_id", "customer_id", "status", "amount_cents", "discount_cents", "currency", "placed_at_utc", "note"}}, 0, nil), nil
			}}, clock.New(), store, compositionDataSourceService{})
			runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
				sinkCalls++
				output, ok := input.SourceResult.Payload()
				require.True(t, ok)
				encoded, err := json.Marshal(output.Rows)
				require.NoError(t, err)
				var actual []map[string]any
				require.NoError(t, jsonvalue.Unmarshal(encoded, &actual))
				require.Equal(t, expected, actual)
				return nil
			}}
			run, err := runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "dev", RunSlug: "native-orders", Pipeline: &pipeline})
			if negative {
				require.ErrorIs(t, err, transformsql.ErrContractType)
				require.ErrorContains(t, err, `"net_total_cents" expects integer but projection resolves to number`)
				require.NotNil(t, run)
				require.Equal(t, "failed", store.timelines["native-orders"].Status)
				require.Empty(t, run.Timeline.Snapshots)
				require.Zero(t, queryCalls)
				require.Zero(t, sinkCalls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "succeeded", run.Metadata.Status)
			require.Equal(t, 1, queryCalls)
			require.Equal(t, 1, sinkCalls)
		})
	}
}
