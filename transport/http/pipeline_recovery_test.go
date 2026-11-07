package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/stretchr/testify/require"
)

func TestRecoverPipelineRunEnforcesExecuteAndRuntimeScope(t *testing.T) {
	t.Parallel()
	for _, allowed := range []bool{true, false} {
		t.Run(map[bool]string{true: "allowed", false: "denied"}[allowed], func(t *testing.T) {
			called := false
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, model.Execute, input.Action)
					return allowed, nil
				}},
				pipelinesService: &fakePipelinesService{recoverRunFn: func(_ context.Context, org, project, environment, run slug.Slug, confirmation *slug.Slug, actor model.EventActor, _ pipelinerun.RunSourceSummary) (*pipelinesvc.PipelineRunDetail, error) {
					called = true
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("ops"), project)
					require.Equal(t, slug.Slug("dev"), environment)
					require.Equal(t, slug.Slug("original"), run)
					require.Equal(t, slug.Slug("dev"), *confirmation)
					require.Equal(t, model.EventActorTypeUser, actor.Type)
					return &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{RunSlug: "recovery", Status: "failed"}}, xerror.ReplayUnsafe()
				}},
			})
			request := jsonRequest(t, http.MethodPost, "/organizations/acme/projects/ops/environments/dev/runs/original:recover", map[string]any{"protected_environment_confirmation": "dev"})
			response := authenticatedRequest(t, app, store, must.Slug("alice"), request)
			defer func() { _ = response.Body.Close() }()
			if !allowed {
				require.Equal(t, http.StatusForbidden, response.StatusCode)
				require.False(t, called)
				return
			}
			require.True(t, called)
			require.Equal(t, http.StatusOK, response.StatusCode)
			var record api.PipelineRunRecord
			require.NoError(t, json.NewDecoder(response.Body).Decode(&record))
			require.Equal(t, "recovery", record.Metadata.RunSlug)
			require.Equal(t, api.PipelineRunMetadataStatusFailed, record.Metadata.Status)
		})
	}
}

func TestRunTimelinePreservesSafeSinkDeliveryDiagnostics(t *testing.T) {
	t.Parallel()
	timeline := toAPIPipelineRunTimeline(pipelinerun.RunTimeline{Snapshots: []pipelinerun.StepSnapshot{{Step: "write", Kind: "sink", Input: pipelinerun.StepInputSnapshot{
		SinkReplayStrategy: "naturally_idempotent", SinkReplayMethod: "deterministic_external_write", SinkReplayDecision: "skip_ledger_succeeded", SinkEffectState: pipelinerun.SinkEffectStateSucceeded, SinkDeliveryOutcome: "confirmed",
	}}}})
	input := timeline.Snapshots[0].Input
	require.Equal(t, "confirmed", string(*input.SinkDeliveryOutcome))
	require.Equal(t, "succeeded", string(*input.SinkEffectState))
	require.Equal(t, "naturally_idempotent", string(*input.SinkReplayStrategy))
	require.Equal(t, "skip_ledger_succeeded", *input.SinkReplayDecision)
	require.Equal(t, "deterministic_external_write", *input.SinkReplayMethod)
}
