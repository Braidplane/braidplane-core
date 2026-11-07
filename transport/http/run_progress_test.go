package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/stretchr/testify/require"
)

func TestRunProgressAuthorizesExactScopeAndReturnsBoundedCursor(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		t.Run(map[bool]string{true: "allowed", false: "denied"}[allowed], func(t *testing.T) {
			called := false
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, model.Read, input.Action)
					require.Equal(t, model.ResourcePipelines, input.Resource)
					return allowed, nil
				}},
				pipelinesService: &fakePipelinesService{GetRunProgressFn: func(_ context.Context, org, project, env slug.Slug, run string, after int64, limit int) (*pipelinesvc.RunProgress, error) {
					called = true
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("ops"), project)
					require.Equal(t, slug.Slug("dev"), env)
					require.Equal(t, "exact-attempt", run)
					require.EqualValues(t, 4, after)
					require.Equal(t, 2, limit)
					return &pipelinesvc.RunProgress{Metadata: pipelinesvc.PipelineRunMetadata{RunSlug: run}, Revision: 7, AfterEvent: 4, NextAfterEvent: 5, EventCount: 5, Events: []pipelinerun.TimelineEvent{{At: time.Now(), Type: "step_started", Step: "fetch", Status: "running"}}, WorkerLiveness: pipelinerun.WorkerLivenessUnavailable}, nil
				}},
			})
			response := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodGet, "/organizations/acme/projects/ops/environments/dev/runs/exact-attempt/progress?after_event=4&limit=2", nil))
			defer func() { _ = response.Body.Close() }()
			if !allowed {
				require.Equal(t, http.StatusForbidden, response.StatusCode)
				require.False(t, called)
				return
			}
			require.True(t, called)
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
			var result api.PipelineRunProgress
			require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
			require.Equal(t, "exact-attempt", result.Metadata.RunSlug)
			require.EqualValues(t, 7, result.Revision)
			require.EqualValues(t, 5, result.NextAfterEvent)
			require.Len(t, result.Events, 1)
			require.Equal(t, "fetch", *result.Events[0].Step)
		})
	}
}
