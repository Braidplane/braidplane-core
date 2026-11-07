package deploymentplan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/stretchr/testify/require"
)

func testPlan(t *testing.T) Plan {
	t.Helper()
	portable := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "sync", Name: "Sync"}, Steps: []pipelinespec.Step{{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "logical", OperationType: "sql", OperationRef: "orders"}}}}
	authored, err := json.Marshal(portable)
	require.NoError(t, err)
	raw, id, err := contractplan.Compile(contractplan.Input{Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/sync.json"}, Authored: authored, Operations: []contractplan.Operation{{Step: "fetch", Role: "query", DataSource: "logical", Slug: "orders", Revision: "v1", Content: json.RawMessage(`{"data":{"statement":"{\"sql\":\"SELECT 1\"}"},"parameters":[]}`)}}})
	require.NoError(t, err)
	compiled, err := contractplan.Read(raw, id)
	require.NoError(t, err)
	executable, err := compiled.RuntimePipeline()
	require.NoError(t, err)
	executable.Steps[0].Query.DataSourceSlug = "physical"
	return Plan{Organization: "acme", Project: "etl", Environment: "dev", TemplateDigest: id, CompiledDigest: id, CompiledPlan: raw, Pipeline: executable, Targets: []Target{{Step: "fetch", Role: "query", Reference: "logical", DataSource: "physical", ConfigDigest: "sha256:" + strings.Repeat("b", 64), BindingRevision: "revision-one"}}}
}

func TestResolutionRestoresLiteralRuntimeSyntax(t *testing.T) {
	plan := testPlan(t)
	plan.Pipeline.Steps[0].Query.Parameters = map[string]any{
		"as_of": pipelineruntime.LiteralString("{{runtime.run.date | add_days:-1}}"),
		"mixed": pipelineruntime.LiteralTemplate{Text: "x{{customer}}/{{runtime.run.date}}", Ranges: [][2]int{{1, 13}}},
	}
	raw, id, err := Encode(plan)
	require.NoError(t, err)
	loaded, err := Read(raw, id)
	require.NoError(t, err)
	require.Equal(t, pipelineruntime.LiteralString("{{runtime.run.date | add_days:-1}}"), loaded.Pipeline.Steps[0].Query.Parameters["as_of"])
	require.NoError(t, pipelineruntime.ResolvePipelineExecution(loaded.Pipeline, pipelineruntime.ExecutionContext{At: time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC)}))
	require.Equal(t, "x{{customer}}/2024-03-31", pipelineruntime.LiteralValue(loaded.Pipeline.Steps[0].Query.Parameters["mixed"]))
}

func TestResolutionIdentityAndReload(t *testing.T) {
	original := testPlan(t)
	raw, id, err := Encode(original)
	require.NoError(t, err)
	loaded, err := Read(raw, id)
	require.NoError(t, err)
	require.True(t, loaded.Matches("acme", "etl", "dev", "sync"))
	require.False(t, loaded.Matches("other", "etl", "dev", "sync"))
	for name, change := range map[string]func(*Plan){
		"environment":      func(p *Plan) { p.Environment = "prod" },
		"binding revision": func(p *Plan) { p.Targets[0].BindingRevision = "revision-two" },
		"target": func(p *Plan) {
			p.Targets[0].DataSource = "replacement"
			p.Pipeline.Steps[0].Query.DataSourceSlug = "replacement"
		},
		"configuration": func(p *Plan) { p.Targets[0].ConfigDigest = "sha256:" + strings.Repeat("c", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			p := testPlan(t)
			change(&p)
			_, changed, err := Encode(p)
			require.NoError(t, err)
			require.NotEqual(t, id, changed)
		})
	}
	_, err = Read(append(raw, ' '), id)
	require.ErrorIs(t, err, ErrInvalid)
	original.CompiledPlan = nil
	require.Equal(t, "physical", loaded.Pipeline.Steps[0].Query.DataSourceSlug.String())
}

func TestResolutionRejectsIncompleteTargetsAndCorruptCompiledMaterial(t *testing.T) {
	for name, change := range map[string]func(*Plan){
		"missing":                      func(p *Plan) { p.Targets = nil },
		"extra":                        func(p *Plan) { p.Targets = append(p.Targets, p.Targets[0]) },
		"source mismatch":              func(p *Plan) { p.Targets[0].DataSource = "other" },
		"reference mismatch":           func(p *Plan) { p.Targets[0].Reference = "other" },
		"missing revision":             func(p *Plan) { p.Targets[0].BindingRevision = "" },
		"invalid configuration digest": func(p *Plan) { p.Targets[0].ConfigDigest = "invalid" },
		"compiled corruption":          func(p *Plan) { p.CompiledPlan = json.RawMessage(`{}`) },
	} {
		t.Run(name, func(t *testing.T) {
			p := testPlan(t)
			change(&p)
			_, _, err := Encode(p)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestAttemptDispatchCannotSubstituteTargets(t *testing.T) {
	p := testPlan(t)
	ctx := WithDispatch(context.Background(), p.Organization, p.Targets)
	got, err := DispatchConfig(ctx, "acme", "physical")
	require.NoError(t, err)
	require.Equal(t, p.Targets[0].ConfigDigest, got)
	_, err = DispatchConfig(ctx, "acme", "logical")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = DispatchConfig(ctx, "other", "physical")
	require.ErrorIs(t, err, ErrInvalid)
}
