package http

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
)

func TestReadinessRequiresReadAccessToEachResource(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/organizations/acme/projects/ops/readiness", "/organizations/acme/projects/ops/pipelines/settlement-review/readiness"} {
		for _, denied := range []model.Resource{model.ResourcePipelines, model.ResourceEnvironments, model.ResourceDataSources} {
			t.Run(path+"/"+denied.String(), func(t *testing.T) {
				app, store := newTestApp(t, testServerConfig{
					authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.Read, input.Action)
						assert.Equal(t, must.Slug("acme"), *input.OrgSlug)
						return input.Resource != denied, nil
					}},
				})
				req := jsonRequest(t, http.MethodGet, path, nil)
				resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
				defer func() { require.NoError(t, resp.Body.Close()) }()
				assert.Equal(t, http.StatusForbidden, resp.StatusCode)
			})
		}
	}
}

func TestReadinessReadsPersistedPipelineInRequestedProject(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/organizations/acme/projects/ops/readiness", "/organizations/acme/projects/ops/pipelines/settlement-review/readiness"} {
		t.Run(path, func(t *testing.T) {
			app, store := newTestApp(t, testServerConfig{
				projectsService: &fakeProjectsService{GetProjectFn: func(_ context.Context, org, project slug.Slug) (*model.Project, error) {
					assert.Equal(t, must.Slug("acme"), org)
					assert.Equal(t, must.Slug("ops"), project)
					return testProject, nil
				}},
				pipelinesService: &fakePipelinesService{
					ListFn: func(_ context.Context, _, _, _ slug.Slug, _ model.ListRequest) (model.ListResponse[*pipelinesvc.PipelineSummary], error) {
						return model.ListResponse[*pipelinesvc.PipelineSummary]{Items: []*pipelinesvc.PipelineSummary{{Slug: "settlement-review"}}}, nil
					},
					GetFn: func(_ context.Context, org, project, pipeline, member slug.Slug) (*pipelinesvc.PipelineDetail, error) {
						assert.Equal(t, must.Slug("acme"), org)
						assert.Equal(t, must.Slug("ops"), project)
						assert.Equal(t, must.Slug("settlement-review"), pipeline)
						assert.Empty(t, member)
						return &pipelinesvc.PipelineDetail{Spec: &pipelinespec.Pipeline{
							Metadata: pipelinespec.Metadata{Slug: pipeline},
							Steps:    []pipelinespec.Step{{Slug: "read", Kind: "query", Query: &pipelinespec.QueryRef{DataSourceSlug: "ledger", OperationType: "sql"}}},
						}}, nil
					},
				},
				dataSourceRefsService: &fakeDataSourceRefsService{GetDataSourceRefFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.DataSourceRef, error) {
					return nil, datasourcerefssvc.ErrDataSourceRefNotFound
				}},
				environmentsService: &fakeEnvironmentsService{ListProjectEnvironmentsFn: func(context.Context, slug.Slug, slug.Slug, model.ListRequest) (model.ListResponse[model.ProjectEnvironment], error) {
					return model.ListResponse[model.ProjectEnvironment]{Items: []model.ProjectEnvironment{{Environment: model.Environment{Slug: "dev"}}}}, nil
				}},
			})
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), jsonRequest(t, http.MethodGet, path, nil))
			defer func() { require.NoError(t, resp.Body.Close()) }()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			report := parseResponse[api.ReadinessReport](t, resp)
			require.Equal(t, api.ReadinessStatusBlocked, report.Status)
			require.Len(t, report.Environments, 1)
			env := report.Environments[0]
			require.Len(t, env.Diagnostics, 1)
			require.Equal(t, "missing_ref", env.Diagnostics[0].Code)
			if strings.Contains(path, "/pipelines/") {
				require.NotNil(t, env.Requirements)
				require.Len(t, *env.Requirements, 1)
				row := (*env.Requirements)[0]
				require.Equal(t, api.ReadinessDataSourceRef, row.Kind)
				require.Equal(t, "ledger", row.Key)
				require.Equal(t, api.ReadinessStatusBlocked, row.Status)
				require.Nil(t, row.Target)
				require.Equal(t, []api.ReadinessUsage{{StepSlug: "read", FieldPath: env.Diagnostics[0].FieldPath}}, row.Usages)
			} else {
				require.Nil(t, env.Requirements)
			}
		})
	}
}

func TestReadinessUnknownProject(t *testing.T) {
	t.Parallel()
	app, store := newTestApp(t, testServerConfig{
		projectsService: &fakeProjectsService{GetProjectFn: func(context.Context, slug.Slug, slug.Slug) (*model.Project, error) {
			return nil, projectssvc.ErrProjectNotFound
		}},
	})
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), jsonRequest(t, http.MethodGet, "/organizations/acme/projects/missing/readiness", nil))
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
