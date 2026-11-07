package run

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestResolveParams_ResolvesNestedRefs(t *testing.T) {
	t.Parallel()

	completed := map[slug.Slug]*StepResult{
		must.Slug("fetch-users"): newStepResult(result.NewSQLResult(
			"postgres",
			&result.SQLResult{
				Rows:        []map[string]any{{"id": 1}},
				ColumnNames: []string{"id"},
				RowCount:    1,
			},
			5,
			nil,
		)),
	}

	params := map[string]any{
		"payload": map[string]any{
			"rows": "$steps.fetch-users.rows",
			"meta": map[string]any{
				"row_count": "$steps.fetch-users.row_count",
			},
			"collection": []any{
				"literal",
				"$steps.fetch-users.column_names",
			},
		},
	}

	resolved, err := resolveParams(params, completed)
	require.NoError(t, err)

	payload, ok := resolved["payload"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []map[string]any{{"id": 1}}, payload["rows"])

	meta, ok := payload["meta"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, 1, meta["row_count"])

	collection, ok := payload["collection"].([]any)
	require.True(t, ok)
	require.Equal(t, "literal", collection[0])
	require.Equal(t, []string{"id"}, collection[1])
}

func TestResolveParams_NestedErrorContainsPath(t *testing.T) {
	t.Parallel()

	params := map[string]any{
		"payload": map[string]any{
			"items": []any{"$steps.unknown.rows"},
		},
	}

	_, err := resolveParams(params, map[slug.Slug]*StepResult{})
	require.Error(t, err)
	require.ErrorContains(t, err, "parameter \"payload\".items[0]")
	require.ErrorIs(t, err, ErrRefUnknownStep)
}

func TestResolveParams_PreservesRuntimeVariableLiteralStepLikeStrings(t *testing.T) {
	t.Parallel()

	resolved, err := resolveParams(
		map[string]any{
			"path": pipelineruntime.LiteralString("$steps.archive/path"),
			"embedded_path": pipelineruntime.LiteralString(
				"$steps.archive/orders",
			),
			"nested": map[string]any{
				"path": pipelineruntime.LiteralString("$steps.fetch-users.rows"),
			},
		},
		map[slug.Slug]*StepResult{},
	)
	require.NoError(t, err)
	require.Equal(t, "$steps.archive/path", resolved["path"])
	require.Equal(t, "$steps.archive/orders", resolved["embedded_path"])
	require.Equal(t, "$steps.fetch-users.rows", resolved["nested"].(map[string]any)["path"])
}
