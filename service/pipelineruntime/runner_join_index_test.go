package pipelineruntime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/stretchr/testify/require"
)

func TestRunnerSettlementJoinPreservesMultiplicityAndMissingLedger(t *testing.T) {
	t.Parallel()
	settlements := []map[string]any{{"ref": "capture", "gross": 10000}, {"ref": "refund", "gross": -1500}, {"ref": "unknown", "gross": 2500}}
	ledger := []map[string]any{{"ref": "capture", "gross": 10000}, {"ref": "refund", "gross": -2000}, {"ref": "refund", "gross": -1900}}
	// Keep both inputs above the index's small-relation cutoff.
	for range 3 {
		settlements = append(settlements, map[string]any{"ref": "padding", "gross": 0})
		ledger = append(ledger, map[string]any{"ref": "other", "gross": 0})
	}
	schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL, Type: "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{Name: "ledger", Columns: []datasourcessvc.SchemaColumn{{Name: "ref", Type: "varchar"}, {Name: "gross", Type: "integer"}}}}},
	}
	calls := 0
	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(ctx context.Context, org, source, query slug.Slug, params map[string]any) (*result.ExecutionResult, error) {
			calls++
			rows := settlements
			if query == s("ledger") {
				rows = ledger
			}
			return result.NewSQLResult("postgres", &result.SQLResult{Rows: rows, ColumnNames: []string{"ref", "gross"}, RowCount: len(rows)}, 1, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time { return time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC) }},
		NewNoopRunStore(),
		preValidationMockService{schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("synthetic-ledger"): schema}},
	)
	inputContract := &payload.Contract{Fields: []payload.ContractField{
		runtimeField("settlements.ref", model.ContractFieldTypeValueString),
		runtimeField("settlements.gross", model.ContractFieldTypeValueInteger),
		runtimeField("ledger.ref", model.ContractFieldTypeValueString),
		runtimeField("ledger.gross", model.ContractFieldTypeValueInteger),
	}}
	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "settlement-join", Name: "Synthetic settlement comparison"},
		Steps: []pipelinespec.Step{
			{Kind: "query", Slug: "settlements", Query: &pipelinespec.QueryRef{DataSourceSlug: "synthetic-ledger", OperationType: "sql", OperationRef: "settlements"}},
			{Kind: "query", Slug: "ledger", Query: &pipelinespec.QueryRef{DataSourceSlug: "synthetic-ledger", OperationType: "sql", OperationRef: "ledger"}},
			{Kind: "transform", Slug: "compare", Transform: &pipelinespec.TransformSpec{
				Inputs:        map[string]pipelinespec.StepInput{"settlements": {FromStep: s("settlements")}, "ledger": {FromStep: s("ledger")}},
				SQL:           "SELECT s.ref AS ref, l.gross AS expected, s.gross - l.gross AS difference FROM settlements s LEFT JOIN ledger l ON s.ref = l.ref WHERE s.ref != 'padding'",
				InputContract: inputContract,
				OutputKind:    model.TransformOutputKindRows,
				OutputContract: &payload.Contract{Fields: []payload.ContractField{
					{Name: "ref", Type: model.ContractFieldTypeValueString, Nullable: true},
					{Name: "expected", Type: model.ContractFieldTypeValueInteger, Nullable: true},
					{Name: "difference", Type: model.ContractFieldTypeValueNumber, Nullable: true},
				}},
			}},
		},
	}
	run, err := runner.Run(context.Background(), RunRequest{OrgSlug: s("synthetic"), EnvironmentSlug: s("test"), RunSlug: "settlement-join-run", Pipeline: pipeline})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, "succeeded", run.Metadata.Status)
	output, ok := run.StepResults[s("compare")].Transform()
	require.True(t, ok)
	require.Equal(t, 4, output.RowCount)
	encoded, err := json.Marshal(output.Rows)
	require.NoError(t, err)
	require.JSONEq(t, `[{"ref":"capture","expected":10000,"difference":0},{"ref":"refund","expected":-2000,"difference":500},{"ref":"refund","expected":-1900,"difference":400},{"ref":"unknown","expected":null,"difference":null}]`, string(encoded))
}
