package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

type executionControlTestService struct {
	pipelinesvc.Service
	set func(pipelinesvc.ExecutionScope, int64, bool, model.EventActor) (*pipelinesvc.ExecutionControl, error)
}

func (s executionControlTestService) SetExecutionControl(_ context.Context, scope pipelinesvc.ExecutionScope, revision int64, paused bool, actor model.EventActor) (*pipelinesvc.ExecutionControl, error) {
	return s.set(scope, revision, paused, actor)
}

func TestSetPipelineExecutionControlAuthorizesReadAndUpdateOnTarget(t *testing.T) {
	for _, test := range []struct {
		name   string
		denied model.Action
		status int
	}{
		{"read denied", model.Read, http.StatusForbidden},
		{"update denied", model.Update, http.StatusForbidden},
		{"authorized", model.Action(0), http.StatusOK},
		{"missing desired state", model.Action(0), http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			var checks []model.Action
			called := false
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, "setPipelineExecutionControl", input.Operation)
					require.Equal(t, model.ResourcePipelines, input.Resource)
					require.Equal(t, model.AuthorizationTarget{Kind: "deployment_targets", ProjectSlug: "etl", EnvironmentSlug: "prod", Slug: "orders"}, input.Target)
					checks = append(checks, input.Action)
					return input.Action != test.denied, nil
				}},
				pipelinesService: executionControlTestService{set: func(scope pipelinesvc.ExecutionScope, revision int64, paused bool, actor model.EventActor) (*pipelinesvc.ExecutionControl, error) {
					called = true
					require.Equal(t, pipelinesvc.ExecutionScope{Org: "acme", Project: "etl", Environment: "prod", Pipeline: "orders"}, scope)
					require.EqualValues(t, 9, revision)
					require.True(t, paused)
					require.Equal(t, model.UserEventActor("operator"), actor)
					return &pipelinesvc.ExecutionControl{Paused: true, Revision: 10, UncertainCount: 1}, nil
				}},
			})
			request := map[string]any{"paused": true, "expected_revision": 9}
			if test.name == "missing desired state" {
				delete(request, "paused")
			}
			req := jsonRequest(t, http.MethodPut, "/organizations/acme/projects/etl/environments/prod/pipelines/orders/execution-control", request)
			resp := authenticatedRequest(t, app, store, slug.Slug("operator"), req)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, test.status, resp.StatusCode)
			require.Equal(t, test.status == http.StatusOK, called)
			if test.denied == model.Read {
				require.Equal(t, []model.Action{model.Read}, checks)
			} else {
				require.Equal(t, []model.Action{model.Read, model.Update}, checks)
			}
			if called {
				var state api.PipelineExecutionControl
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&state))
				require.True(t, state.Drained)
				require.EqualValues(t, 1, state.UncertainCount)
			}
		})
	}
}
