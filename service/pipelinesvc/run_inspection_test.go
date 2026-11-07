package pipelinesvc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	spec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/stretchr/testify/require"
)

func TestRunInspectionUsesOnlyExactStoredResolution(t *testing.T) {
	pipeline := &spec.Pipeline{Metadata: spec.Metadata{Slug: "sync", Name: "Sync"}, Steps: []spec.Step{{Kind: "query", Slug: "fetch", Query: &spec.QueryRef{DataSourceSlug: "logical", OperationType: "sql", OperationRef: "orders"}}}}
	authored, err := json.Marshal(pipeline)
	require.NoError(t, err)
	raw, compiledID, err := contractplan.Compile(contractplan.Input{Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/sync.json"}, Authored: authored, Operations: []contractplan.Operation{{Step: "fetch", Role: "query", DataSource: "logical", Slug: "orders", Revision: "old-revision", Content: json.RawMessage(`{"data":{"statement":"{\"sql\":\"SELECT private_column\"}"},"parameters":[]}`)}}})
	require.NoError(t, err)
	compiled, err := contractplan.Read(raw, compiledID)
	require.NoError(t, err)
	executable, err := compiled.RuntimePipeline()
	require.NoError(t, err)
	executable.Steps[0].Query.DataSourceSlug = "original-target"
	resolution, digest, err := deploymentplan.Encode(deploymentplan.Plan{Organization: "acme", Project: "etl", Environment: "dev", TemplateDigest: compiledID, CompiledDigest: compiledID, CompiledPlan: raw, Pipeline: executable, Targets: []deploymentplan.Target{{Step: "fetch", Role: "query", Reference: "logical", DataSource: "original-target", ConfigDigest: "sha256:" + strings.Repeat("b", 64), BindingRevision: "old-binding"}}})
	require.NoError(t, err)
	inspection := projectRunComposition(resolution, digest, "acme", "etl", "dev", "sync", compiled.AuthoredSpecHash)
	require.Equal(t, "available", inspection.Status)
	require.Equal(t, compiledID, inspection.CompiledPlanDigest)
	require.Equal(t, "old-revision", inspection.Operations[0].Revision)
	require.Equal(t, slug.Slug("original-target"), inspection.Targets[0].DataSource)
	encoded, err := json.Marshal(inspection)
	require.NoError(t, err)
	for _, private := range []string{"private_column", "statement", "parameters", "ConfigDigest", "Inputs", "Content"} {
		require.NotContains(t, string(encoded), private)
	}
	for _, scope := range [][5]string{{"other", "etl", "dev", "sync", compiled.AuthoredSpecHash}, {"acme", "other", "dev", "sync", compiled.AuthoredSpecHash}, {"acme", "etl", "other", "sync", compiled.AuthoredSpecHash}, {"acme", "etl", "dev", "other", compiled.AuthoredSpecHash}, {"acme", "etl", "dev", "sync", "different-authored-hash"}} {
		result := projectRunComposition(resolution, digest, slug.Slug(scope[0]), slug.Slug(scope[1]), slug.Slug(scope[2]), slug.Slug(scope[3]), scope[4])
		require.Equal(t, "unsupported", result.Status)
		require.Empty(t, result.Operations)
		require.Empty(t, result.Targets)
	}
	require.Equal(t, "unsupported", projectRunComposition([]byte("corrupt"), digest, "acme", "etl", "dev", "sync", compiled.AuthoredSpecHash).Status)
}
