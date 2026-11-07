package pipelineruntime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	resultpkg "github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
)

func TestRunnerRunProvidesValidatedEventInputToSinkWithoutCheckpointingValues(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{
		Name:           "event",
		Representation: model.ContractRepresentationJSON,
		RootShape:      model.ContractRootShapeObject,
		Fields: []payload.ContractField{{
			Name:     "id",
			Type:     model.ContractFieldTypeValueString,
			Required: true,
		}},
	}
	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: must.Slug("event-pipeline"), Name: "Event Pipeline"},
		Steps: []pipelinespec.Step{
			{
				Kind:  "input",
				Slug:  must.Slug("event"),
				Input: &pipelinespec.InputSpec{OutputContract: contract},
			},
			{
				Kind: "sink",
				Slug: must.Slug("send-event"),
				Sink: &pipelinespec.SinkSpec{
					Input:            &pipelinespec.StepInput{FromStep: must.Slug("event")},
					InputContract:    contract,
					DataSourceSlug:   must.Slug("events-http"),
					OperationType:    "http",
					OperationRef:     must.Slug("send-event"),
					IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
				},
			},
		},
	}
	eventInput, err := pipelinerun.NewVerifiedJSONEventInput(
		must.Slug("event"),
		json.RawMessage(`{"id":"evt-1","secret":"raw-value"}`),
	)
	require.NoError(t, err)

	store := &noopRunStore{
		timelines:   make(map[string]pipelinerun.RunTimeline),
		checkpoints: make(map[string]pipelinerun.ReplayCheckpoint),
	}
	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{},
		&testutil.FakeClock{NowFn: func() time.Time { return time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC) }},
		store,
		runnerTestDataSourceLookup{},
	)
	sinkCalls := 0
	runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
		sinkCalls++
		actual, ok := input.SourceResult.Payload()
		require.True(t, ok)
		require.JSONEq(t, `{"id":"evt-1","secret":"raw-value"}`, string(actual.Document))
		return nil
	}}

	result, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		ProjectSlug:     must.Slug("default"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "event-run",
		Pipeline:        pipeline,
		EventInput:      eventInput,
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", result.Metadata.Status)
	require.Equal(t, resultpkg.ExecutionResultTypeUnknown, result.StepResults[must.Slug("event")].Type())
	require.Empty(t, store.checkpoints)
	require.Len(t, result.Timeline.Snapshots, 2)
	summary := result.Timeline.Snapshots[0].Input.EventInput
	require.Equal(t, []string{"event"}, summary.Keys)
	require.NotContains(t, string(mustJSONBytes(t, result.Timeline)), "raw-value")
	require.Equal(t, 1, sinkCalls)

	incompatibleInput, err := pipelinerun.NewVerifiedJSONEventInput(
		must.Slug("event"),
		json.RawMessage(`{"id":42}`),
	)
	require.NoError(t, err)
	_, err = runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		ProjectSlug:     must.Slug("default"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "rejected-event-run",
		Pipeline:        pipeline,
		EventInput:      incompatibleInput,
	})
	require.ErrorContains(t, err, "type_mismatch")
	require.Equal(t, 1, sinkCalls)
}

func TestPrepareEventInputRejectsMissingUnsupportedAndIncompatibleInput(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{
		Name:           "event",
		Representation: model.ContractRepresentationJSON,
		RootShape:      model.ContractRootShapeObject,
		Fields:         []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueString, Required: true}},
	}
	pipeline := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{{
		Kind: "input", Slug: must.Slug("event"), Input: &pipelinespec.InputSpec{OutputContract: contract},
	}}}

	_, _, err := prepareEventInput(pipeline, nil)
	require.ErrorContains(t, err, "is required")

	input, err := pipelinerun.NewVerifiedJSONEventInput(must.Slug("other-event"), json.RawMessage(`{"id":"evt-1"}`))
	require.NoError(t, err)
	_, _, err = prepareEventInput(pipeline, input)
	require.ErrorContains(t, err, "does not match pipeline input")

	input, err = pipelinerun.NewVerifiedJSONEventInput(must.Slug("event"), json.RawMessage(`{"other":true}`))
	require.NoError(t, err)
	_, _, err = prepareEventInput(pipeline, input)
	require.ErrorContains(t, err, "incompatible")

	input, err = pipelinerun.NewVerifiedJSONEventInput(must.Slug("event"), json.RawMessage(`{"id":42}`))
	require.NoError(t, err)
	_, _, err = prepareEventInput(pipeline, input)
	require.ErrorContains(t, err, "type_mismatch")

	input, err = pipelinerun.NewVerifiedJSONEventInput(must.Slug("event"), json.RawMessage(`[{"id":"evt-1"}]`))
	require.NoError(t, err)
	_, _, err = prepareEventInput(pipeline, input)
	require.ErrorContains(t, err, "root_shape_mismatch")

	_, _, err = prepareEventInput(&pipelinespec.Pipeline{}, input)
	require.ErrorContains(t, err, "does not declare an input step")
}

func mustJSONBytes(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}
