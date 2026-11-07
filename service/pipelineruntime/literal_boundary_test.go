package pipelineruntime

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/model"
	runtimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	"github.com/stretchr/testify/require"
)

func TestRunnerBindsInlineSuppliedTextWithoutEvaluatingIt(t *testing.T) {
	for _, supplied := range []string{"{{runtime.run.date}}", "{{runtime.run.date | offset:1h}}", "{{customer}}", "{{runtime .broken", "'); DROP TABLE users; --"} {
		called := false
		executor := &testutil.FakeQueryExecutor{ExecuteInlineFn: func(ctx context.Context, _, _ slug.Slug, operation, values map[string]any) (*result.ExecutionResult, error) {
			called = true
			for key, value := range values {
				values[key] = runtimevars.LiteralValue(value)
			}
			for key, value := range runtimevars.LiteralBindingsFrom(ctx) {
				values[key] = value
			}
			statement, args, err := param.BindParams(operation["sql"].(string), values, "?")
			require.NoError(t, err)
			require.Equal(t, "SELECT ?", statement)
			require.Equal(t, []any{supplied}, args)
			return successfulSQLResult(1), nil
		}}
		runner := mustNewRunner(t, executor, &testutil.FakeClock{NowFn: func() time.Time { return testExecutionContext().At }})
		pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "literal", Name: "Literal"}, Steps: []pipelinespec.Step{{Kind: "query", Slug: "read", Query: &pipelinespec.QueryRef{
			DataSourceSlug: "db", OperationType: "sql", OperationInline: map[string]any{"sql": "SELECT {{runtime.input.value}}"},
		}}}}
		_, err := runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", RunSlug: "literal-inline", Pipeline: pipeline, RuntimeInput: map[string]any{"value": supplied}})
		require.NoError(t, err)
		require.True(t, called)
	}
}

func TestRunnerPreservesMixedSinkLiteralsAndAuthoredMetadata(t *testing.T) {
	for _, supplied := range []string{"{{runtime.run.date}}", "{{runtime.run.date | offset:1h}}", "{{customer}}", "{{runtime .broken"} {
		pipeline := incrementalCheckpointPipeline()
		pipeline.Steps[0].Query.Incremental = nil
		pipeline.Steps[1].Sink.Config = map[string]any{"key": "exports/{{runtime.env.prefix}}/{{runtime.run.date}}/{{runtime.sink.replay_identity}}"}
		require.NoError(t, runtimevars.ResolvePipeline(pipeline, []model.RuntimeVariable{{Key: "prefix", ValueType: model.RuntimeVariableValueTypeText, Value: supplied}}))
		called := false
		runner, err := NewRunner(adaptTestQueryExecutor(&testutil.FakeQueryExecutor{ExecuteFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, map[string]any) (*result.ExecutionResult, error) {
			return successfulSQLResult(1), nil
		}}),
			&testutil.FakeClock{NowFn: func() time.Time { return testExecutionContext().At }}, &fakeRunStore{}, incrementalCheckpointDataSourceService{},
			&fakeSinkRuntime{executeFn: func(ctx context.Context, input *SinkRuntimeInput) error {
				called = true
				values := map[string]any{"runtime.sink.replay_identity": input.ReplayIdentity}
				for key, value := range runtimevars.LiteralBindingsFrom(ctx) {
					values[key] = value
				}
				key, err := param.SubstituteString(input.SinkConfig["key"].(string), values)
				require.NoError(t, err)
				require.Equal(t, "exports/"+supplied+"/2026-10-22/literal-sink", key)
				return nil
			}})
		require.NoError(t, err)
		_, err = runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", RunSlug: "literal-sink", Pipeline: pipeline})
		require.NoError(t, err)
		require.True(t, called)
	}
}

func TestRunnerDoesNotExposeLaterStepLiteralBindingsToSources(t *testing.T) {
	pipeline := incrementalCheckpointPipeline()
	pipeline.Steps[0].Query.Incremental = nil
	pipeline.Steps[1].Sink.Config = map[string]any{"key": runtimevars.LiteralString("future-step-value/{{runtime.run.date}}")}
	sourceCalled, sinkCalled := false, false
	runner, err := NewRunner(adaptTestQueryExecutor(&testutil.FakeQueryExecutor{ExecuteFn: func(ctx context.Context, _, _, _ slug.Slug, _ map[string]any) (*result.ExecutionResult, error) {
		sourceCalled = true
		require.Empty(t, runtimevars.LiteralBindingsFrom(ctx))
		return successfulSQLResult(1), nil
	}}), &testutil.FakeClock{NowFn: func() time.Time { return testExecutionContext().At }}, &fakeRunStore{}, incrementalCheckpointDataSourceService{}, &fakeSinkRuntime{executeFn: func(ctx context.Context, _ *SinkRuntimeInput) error {
		sinkCalled = true
		require.Equal(t, map[string]any{"runtime.literal.value_0": "future-step-value/{{runtime.run.date}}"}, runtimevars.LiteralBindingsFrom(ctx))
		return nil
	}})
	require.NoError(t, err)
	_, err = runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", RunSlug: "step-literals", Pipeline: pipeline})
	require.NoError(t, err)
	require.True(t, sourceCalled)
	require.True(t, sinkCalled)
}
