package http

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

func TestScheduleAndWebhookMutationsRequireExecutionPermission(t *testing.T) {
	for _, mutation := range []struct {
		method  string
		kind    string
		enabled bool
	}{
		{http.MethodPost, "schedule", true},
		{http.MethodPost, "schedule", false},
		{http.MethodPut, "schedule", true},
		{http.MethodPut, "schedule", false},
		{http.MethodPost, "webhook", true},
		{http.MethodPost, "webhook", false},
		{http.MethodPut, "webhook", true},
		{http.MethodPut, "webhook", false},
		{http.MethodPost, "ScHeDuLe", true},
		{http.MethodPut, "WeBhOoK", true},
	} {
		for _, principal := range []string{"user", "api-key"} {
			for _, permission := range []string{"allowed", "execution-denied", "management-denied", "execution-error"} {
				t.Run(fmt.Sprintf("%s/%s/enabled=%t/%s/%s", mutation.method, mutation.kind, mutation.enabled, principal, permission), func(t *testing.T) {
					called := false
					trigger := testHTTPPipelineTrigger()
					trigger.PipelineSlug = "replacement"
					cfg := testServerConfig{
						pipelineTriggersService: &fakePipelineTriggersService{
							CreatePipelineTriggerFn: func(_ context.Context, _, _, _ slug.Slug, input model.NewPipelineTrigger, _ model.EventActor) (*model.PipelineTrigger, error) {
								called = true
								require.Equal(t, slug.Slug("replacement"), input.PipelineSlug)
								require.Equal(t, mutation.enabled, input.Enabled)
								return trigger, nil
							},
							UpdatePipelineTriggerFn: func(_ context.Context, _, _, _, _ slug.Slug, input model.UpdatePipelineTrigger, _ model.EventActor) (*model.PipelineTrigger, error) {
								called = true
								require.Equal(t, slug.Slug("replacement"), input.PipelineSlug)
								require.Equal(t, mutation.enabled, input.Enabled)
								return trigger, nil
							},
						},
						authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
							require.Equal(t, slug.Slug("acme"), *input.OrgSlug)
							if principal == "api-key" {
								require.Equal(t, slug.Slug("trigger-manager"), input.APIKeySlug)
							} else {
								require.Equal(t, slug.Slug("alice"), input.UserSlug)
							}
							if input.Resource == model.ResourcePipelineTriggers {
								expected := model.Create
								if mutation.method == http.MethodPut {
									expected = model.Update
								}
								require.Equal(t, expected, input.Action)
								return permission != "management-denied", nil
							}
							require.Equal(t, model.ResourcePipelines, input.Resource)
							require.Equal(t, model.Execute, input.Action)
							if permission == "execution-error" {
								return false, xerror.Unexpected().Detail("synthetic authorization failure")
							}
							return permission == "allowed", nil
						}},
					}
					if principal == "api-key" {
						cfg.middleware = []fiber.Handler{apiKeyPrincipalMiddleware(reqctx.APIKeyPrincipal{Slug: "trigger-manager", OrgSlug: "acme", Role: "custom"})}
					}
					app, store := newTestApp(t, cfg)
					body := map[string]any{
						"name": "Trigger", "type": mutation.kind, "enabled": mutation.enabled,
						"pipeline_slug": "replacement", "version_policy": testHTTPTriggerLatestPolicyAPI(),
						"runtime_input_keys": []string{}, "retry_policy": testHTTPTriggerNoRetryPolicyAPI(),
					}
					if strings.EqualFold(mutation.kind, "schedule") {
						body["schedule"] = map[string]any{"cron": "0 2 * * *", "timezone": "UTC", "misfire_policy": "skip"}
					} else {
						body["webhook"] = map[string]any{"input_mappings": []any{}}
					}
					path := "/organizations/acme/projects/operations/environments/prod/pipeline-triggers"
					if mutation.method == http.MethodPut {
						path += "/existing"
					}
					request := jsonRequest(t, mutation.method, path, body)
					var response *http.Response
					if principal == "api-key" {
						var err error
						response, err = app.Test(request)
						require.NoError(t, err)
					} else {
						response = authenticatedRequest(t, app, store, "alice", request)
					}
					defer func() { require.NoError(t, response.Body.Close()) }()
					status := http.StatusForbidden
					switch permission {
					case "allowed":
						status = http.StatusCreated
						if mutation.method == http.MethodPut {
							status = http.StatusOK
						}
					case "execution-error":
						status = http.StatusInternalServerError
					}
					require.Equal(t, status, response.StatusCode)
					require.Equal(t, permission == "allowed", called)
				})
			}
		}
	}
}
