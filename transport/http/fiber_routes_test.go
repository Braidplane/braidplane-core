package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

type migrationRouteServer struct{ api.ServerInterface }

func TestPublicGeneratedRoutesExcludeManagedEndpoints(t *testing.T) {
	t.Parallel()
	app := fiber.New()
	api.RegisterHandlers(app, migrationRouteServer{})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/admin/identity"},
		{http.MethodGet, "/admin/demos"},
		{http.MethodPost, "/admin/demos"},
		{http.MethodGet, "/admin/demos/demo-one"},
		{http.MethodPost, "/admin/demos/demo-one/actions"},
		{http.MethodGet, "/organizations/acme/projects/demo/settlement-review-reset"},
		{http.MethodPost, "/organizations/acme/projects/demo/settlement-review-reset"},
	} {
		response, err := app.Test(httptest.NewRequestWithContext(t.Context(), route.method, route.path, nil))
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, response.StatusCode)
		require.NoError(t, response.Body.Close())
	}
}

func (migrationRouteServer) FormatPipelineSpec(c fiber.Ctx, slug api.SlugParam) error {
	return c.JSON([]string{"FormatPipelineSpec", slug})
}

func (migrationRouteServer) DeployPipeline(c fiber.Ctx, slug api.SlugParam, project api.ProjectSlugParam, environment api.EnvironmentSlugParam, pipeline api.PipelineSlugParam) error {
	return c.JSON([]string{"DeployPipeline", slug, project, environment, pipeline})
}

func (migrationRouteServer) ExecutePipeline(c fiber.Ctx, slug api.SlugParam, project api.ProjectSlugParam, environment api.EnvironmentSlugParam, pipeline api.PipelineSlugParam) error {
	return c.JSON([]string{"ExecutePipeline", slug, project, environment, pipeline})
}

func (migrationRouteServer) RevalidatePipeline(c fiber.Ctx, slug api.SlugParam, project api.ProjectSlugParam, environment api.EnvironmentSlugParam, pipeline api.PipelineSlugParam) error {
	return c.JSON([]string{"RevalidatePipeline", slug, project, environment, pipeline})
}

func (migrationRouteServer) PublishPipelineDraft(c fiber.Ctx, slug api.SlugParam, project api.ProjectSlugParam, pipeline api.PipelineSlugParam) error {
	return c.JSON([]string{"PublishPipelineDraft", slug, project, pipeline})
}

func (migrationRouteServer) RefreshPipelineDraftProposal(c fiber.Ctx, slug api.SlugParam, project api.ProjectSlugParam, pipeline api.PipelineSlugParam) error {
	return c.JSON([]string{"RefreshPipelineDraftProposal", slug, project, pipeline})
}

func (migrationRouteServer) ReconcilePipelines(c fiber.Ctx, slug api.SlugParam, project api.ProjectSlugParam) error {
	return c.JSON([]string{"ReconcilePipelines", slug, project})
}

func (migrationRouteServer) TestDataSourceConnection(c fiber.Ctx, slug api.SlugParam) error {
	return c.JSON([]string{"TestDataSourceConnection", slug})
}

func (migrationRouteServer) GetDataSource(c fiber.Ctx, slug api.SlugParam, datasource api.DataSourceSlugParam) error {
	return c.JSON([]string{"GetDataSource", slug, datasource})
}

func TestGeneratedFiberRoutesPreserveActionSuffixesAndParameters(t *testing.T) {
	t.Parallel()
	app := fiber.New()
	api.RegisterHandlers(app, migrationRouteServer{})
	env := "/organizations/acme/projects/default/environments/prod/pipelines/sync"
	project := "/organizations/acme/projects/default/pipelines/sync"
	tests := []struct {
		path, operation string
		params          []string
	}{
		{"/organizations/acme/projects/default/pipelines:reconcile", "ReconcilePipelines", []string{"acme", "default"}},
		{"/organizations/acme/pipeline-spec:format", "FormatPipelineSpec", []string{"acme"}},
		{env + ":deploy", "DeployPipeline", []string{"acme", "default", "prod", "sync"}},
		{env + ":execute", "ExecutePipeline", []string{"acme", "default", "prod", "sync"}},
		{env + ":revalidate", "RevalidatePipeline", []string{"acme", "default", "prod", "sync"}},
		{project + "/draft:publish", "PublishPipelineDraft", []string{"acme", "default", "sync"}},
		{project + "/draft:refresh-proposal", "RefreshPipelineDraftProposal", []string{"acme", "default", "sync"}},
	}
	for _, tt := range tests {
		t.Run(tt.operation, func(t *testing.T) {
			response, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodPost, tt.path, nil))
			require.NoError(t, err)
			defer func() { require.NoError(t, response.Body.Close()) }()
			require.Equal(t, http.StatusOK, response.StatusCode)
			var got []string
			require.NoError(t, json.NewDecoder(response.Body).Decode(&got))
			require.Equal(t, append([]string{tt.operation}, tt.params...), got)
			for _, path := range []string{strings.ReplaceAll(tt.path, ":", "-"), tt.path + "-extra"} {
				response, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, nil))
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Equal(t, http.StatusNotFound, response.StatusCode, path)
			}
		})
	}
}

func TestGeneratedFiberStaticAndDynamicRoutes(t *testing.T) {
	t.Parallel()
	app := fiber.New()
	api.RegisterHandlers(app, migrationRouteServer{})
	for _, tt := range []struct {
		method, operation string
		params            []string
	}{
		{http.MethodPost, "TestDataSourceConnection", []string{"acme"}},
		{http.MethodGet, "GetDataSource", []string{"acme", "test-connection"}},
	} {
		response, err := app.Test(httptest.NewRequestWithContext(context.Background(), tt.method, "/organizations/acme/data-sources/test-connection", nil))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode)
		var got []string
		require.NoError(t, json.NewDecoder(response.Body).Decode(&got))
		require.NoError(t, response.Body.Close())
		require.Equal(t, append([]string{tt.operation}, tt.params...), got)
	}
}
