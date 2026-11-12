package validate

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/stretchr/testify/require"
)

func TestSQLStreamOutputFacets(t *testing.T) {
	t.Parallel()
	for _, vendor := range []string{"sql", "postgres", "mysql", "bigquery", "snowflake"} {
		query := &spec.QueryRef{OperationType: vendor, OperationInline: map[string]any{"type": vendor, "format": "file", "sql": "SELECT 1"}}
		representation, _ := queryOutputFacets(query)
		require.Equal(t, model.ContractRepresentationFile, representation)
		query.OperationInline["format"] = "rows"
		representation, _ = queryOutputFacets(query)
		require.Equal(t, model.ContractRepresentationRows, representation)
		query.OperationInline = nil
		query.OperationRef = "saved-query"
		representation, _ = queryOutputFacets(query)
		require.Equal(t, model.ContractRepresentationRows, representation)
	}
}

func TestSQLFileOutputRefusesRowTransforms(t *testing.T) {
	t.Parallel()
	pipeline := compositionPipeline()
	query := pipeline.Steps[0].Query
	query.OperationRef = ""
	query.OperationInline = map[string]any{"type": "postgres", "format": "file", "sql": "SELECT id,amount FROM orders"}
	query.OutputContract = &payload.Contract{Representation: model.ContractRepresentationFile}
	require.ErrorContains(t, ValidateStepInputCompatibility(pipeline), "contract_representation_mismatch")
}
