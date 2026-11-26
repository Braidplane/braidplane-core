package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// The resource family can differ from the object being accessed. In particular,
// a pipeline's reference to a shared data source must never select that source.
func TestRESTPermissionTargets(t *testing.T) {
	const source = "/organizations/:slug/data-sources/:ds_slug"
	const project = "/organizations/:slug/projects/:project_slug"
	const environment = project + "/environments/:environment_slug"
	for _, tc := range []struct {
		method, route, request string
		resource               model.Resource
		target                 model.AuthorizationTarget
	}{
		{"GET", source + "/queries/:query_slug/executions/:execution_slug", "/organizations/acme/data-sources/warehouse/queries/count/executions/attempt", model.ResourceQueries, model.AuthorizationTarget{Kind: "query_executions", Slug: "attempt", DataSourceSlug: "warehouse", ParentSlug: "count"}},
		{"GET", environment + "/pipelines/:pipeline_slug/deployment", "/organizations/acme/projects/demo/environments/dev/pipelines/new-one/deployment", model.ResourcePipelines, model.AuthorizationTarget{Kind: "deployment_targets", Slug: "new-one", ProjectSlug: "demo", EnvironmentSlug: "dev"}},
		{"GET", environment + "/pipeline-validation-results/:validation_slug", "/organizations/acme/projects/demo/environments/dev/pipeline-validation-results/result", model.ResourceDataSources, model.AuthorizationTarget{}},
		{"PUT", project + "/pipelines/:pipeline_slug/environments/:environment_slug/data-source-bindings/:data_source_ref_slug", "/organizations/acme/projects/demo/pipelines/daily/environments/dev/data-source-bindings/input", model.ResourceDataSources, model.AuthorizationTarget{Kind: "data_source_bindings_upsert", Slug: "input", ProjectSlug: "demo", EnvironmentSlug: "dev", ParentSlug: "daily"}},
		{"GET", environment + "/pipeline-triggers/:pipeline_trigger_slug/deliveries/:delivery_id", "/organizations/acme/projects/demo/environments/dev/pipeline-triggers/trigger/deliveries/id_1.2", model.ResourcePipelineTriggers, model.AuthorizationTarget{Kind: "webhook_deliveries", Slug: "id_1.2", ProjectSlug: "demo", EnvironmentSlug: "dev", ParentSlug: "trigger"}},
		{"PUT", "/organizations/:slug/environments/:environment_slug/secrets/:environment_secret_key", "/organizations/acme/environments/dev/secrets/API_TOKEN", model.ResourceEnvironments, model.AuthorizationTarget{Kind: "environment_secrets_upsert", Slug: "API_TOKEN", EnvironmentSlug: "dev"}},
		{"DELETE", "/organizations/:slug/runtime-variable-definitions/:runtime_variable_key", "/organizations/acme/runtime-variable-definitions/TABLE_NAME", model.ResourceEnvironments, model.AuthorizationTarget{Kind: "runtime_variable_definitions", Slug: "TABLE_NAME"}},
		{"POST", "/organizations/:slug/roles/:role_slug/restore", "/organizations/acme/roles/editor/restore", model.ResourceRoles, model.AuthorizationTarget{Kind: "deleted_roles", Slug: "editor"}},
	} {
		t.Run(tc.method+tc.request, func(t *testing.T) {
			app := fiber.New()
			app.Add([]string{tc.method}, tc.route, func(c fiber.Ctx) error {
				require.Equal(t, tc.target, routeAuthorizationTarget(c, tc.resource))
				return c.SendStatus(http.StatusNoContent)
			})
			resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), tc.method, tc.request, nil))
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, http.StatusNoContent, resp.StatusCode)
		})
	}
}

// Exercise the generated route, the transport's authorization input and the
// shared selector rules together without executing a data operation.
func TestRouteInputsMatchRegisteredSelectors(t *testing.T) {
	org := slug.Slug("acme")
	for _, tc := range []struct {
		route, operation string
		target           model.AuthorizationTarget
		binding          bool
	}{
		{"/organizations/acme/projects/demo/pipelines", "listPipelines", model.AuthorizationTarget{Kind: "projects", Slug: "demo", ProjectSlug: "demo"}, false},
		{"/organizations/acme/projects/demo/pipelines/daily", "getPipeline", model.AuthorizationTarget{Kind: "pipelines", Slug: "daily", ProjectSlug: "demo"}, false},
		{"/organizations/acme/projects/demo/data-source-refs", "listDataSourceRefs", model.AuthorizationTarget{Kind: "projects", Slug: "demo", ProjectSlug: "demo"}, true},
		{"/organizations/acme/data-sources/shared", "getDataSource", model.AuthorizationTarget{Kind: "datasources", Slug: "shared"}, false},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			called := false
			svc, err := authorization.New(nil, func(_ context.Context, tenant, user, key slug.Slug, target model.AuthorizationTarget) (model.AuthorizationFacts, error) {
				require.Equal(t, org, tenant)
				require.Equal(t, tc.target, target)
				return model.AuthorizationFacts{Valid: true, OrganizationID: "1", TargetID: target.Kind + ":2", ProjectID: "3", Bindings: []model.AuthorizationBinding{{Scope: "organization", ScopeID: "1", Permissions: []string{"*:*"}}}}, nil
			})
			require.NoError(t, err)
			app, store := newTestApp(t, testServerConfig{authService: fakeAuthService{CanFn: func(ctx context.Context, input authorization.Input) (bool, error) {
				called = true
				require.Equal(t, tc.operation, input.Operation)
				require.Equal(t, tc.binding, input.ProjectDataSourceBindings)
				allowed, err := svc.Can(ctx, input)
				require.NoError(t, err)
				require.True(t, allowed)
				return false, nil
			}}})
			response := authenticatedRequest(t, app, store, "alice", httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.route, nil))
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusForbidden, response.StatusCode)
			require.True(t, called)
		})
	}
}
