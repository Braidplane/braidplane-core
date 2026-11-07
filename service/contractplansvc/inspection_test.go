package contractplansvc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/stretchr/testify/require"
)

func TestSummaryContainsOnlyValidatedIdentityMetadata(t *testing.T) {
	t.Parallel()
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "sync", Name: "Sensitive authored title"}, Steps: []pipelinespec.Step{{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "db", OperationType: "sql", OperationRef: "orders"}}}}
	authored, err := json.Marshal(pipeline)
	require.NoError(t, err)
	raw, digest, err := contractplan.Compile(contractplan.Input{Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/retained.json"}, Authored: authored, Operations: []contractplan.Operation{{Step: "fetch", Role: "query", DataSource: "db", Slug: "orders", Revision: "retained", Content: json.RawMessage(`{"data":{"statement":"sensitive-body"},"parameters":[{"slug":"secret","name":"secret","data_type":"text","default":"sensitive-default"}]}`)}}})
	require.NoError(t, err)
	plan, err := contractplan.Read(raw, digest)
	require.NoError(t, err)
	stored := &compiledplans.Snapshot{PipelineSlug: "sync", Digest: digest, AuthoredSpecHash: plan.AuthoredSpecHash, FormatVersion: plan.FormatVersion, Materialization: raw}
	summary := summarize(stored)
	require.Equal(t, "available", summary.Status)
	require.True(t, summary.AdmissionEnabled)
	require.Equal(t, "retained", summary.Operations[0].Revision)
	require.Equal(t, "pipelines/retained.json", summary.Source.Path)
	encoded, err := json.Marshal(summary)
	require.NoError(t, err)
	for _, absent := range []string{"Sensitive authored title", "sensitive-body", "sensitive-default", "statement", "parameters", "Materialization"} {
		require.NotContains(t, string(encoded), absent)
	}
	stored.Materialization = []byte("unsupported")
	require.Equal(t, "unsupported", summarize(stored).Status)
}
