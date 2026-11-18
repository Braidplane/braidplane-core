package readinesssvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/environmentssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

func TestPagesIncludesLastPage(t *testing.T) {
	cursor := "next-page"
	items, err := pages(func(req model.ListRequest) (model.ListResponse[string], error) {
		if req.Cursor == "" {
			return model.ListResponse[string]{Items: []string{"first"}, HasMore: true, NextCursor: &cursor}, nil
		}
		require.Equal(t, cursor, req.Cursor)
		return model.ListResponse[string]{Items: []string{"last"}}, nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"first", "last"}, items)
}

type loadPipelines struct {
	pipelinesvc.Service
	t *testing.T
}

func (p loadPipelines) Get(_ context.Context, org, project, pipeline, member slug.Slug) (*pipelinesvc.PipelineDetail, error) {
	require.Equal(p.t, []slug.Slug{"tenant", "project", "selected", ""}, []slug.Slug{org, project, pipeline, member})
	return &pipelinesvc.PipelineDetail{Spec: &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: pipeline},
		Steps: []pipelinespec.Step{{Slug: "read", Kind: "query", Query: &pipelinespec.QueryRef{
			DataSourceSlug: "ledger", OperationType: "sql", Parameters: map[string]any{"batch": "{{runtime.env.batch}}"},
		}}},
	}}, nil
}

type loadEnvironments struct {
	environmentssvc.Service
	t *testing.T
}

func (e loadEnvironments) ListProjectEnvironments(_ context.Context, org, project slug.Slug, _ model.ListRequest) (model.ListResponse[model.ProjectEnvironment], error) {
	require.Equal(e.t, []slug.Slug{"tenant", "project"}, []slug.Slug{org, project})
	return model.ListResponse[model.ProjectEnvironment]{Items: []model.ProjectEnvironment{
		{Environment: model.Environment{Slug: "dev"}}, {Environment: model.Environment{Slug: "prod"}},
	}}, nil
}

func (e loadEnvironments) CompareRuntimeVariables(_ context.Context, org slug.Slug, _ []string) (model.RuntimeVariableComparison, error) {
	require.Equal(e.t, slug.Slug("tenant"), org)
	return model.CompareRuntimeVariables([]slug.Slug{"prod", "dev", "disabled"}, nil, []model.RuntimeVariable{
		{EnvironmentSlug: "dev", Key: "batch", ValueType: model.RuntimeVariableValueTypeText, Value: "synthetic"},
	}, []string{"batch"}), nil
}

type loadRefs struct {
	datasourcerefssvc.Service
	t            *testing.T
	bindingError error
	environments []slug.Slug
	refReads     int
}

func (r *loadRefs) GetDataSourceRef(_ context.Context, org, project, pipeline, ref slug.Slug) (*model.DataSourceRef, error) {
	require.Equal(r.t, []slug.Slug{"tenant", "project", "selected", "ledger"}, []slug.Slug{org, project, pipeline, ref})
	r.refReads++
	return &model.DataSourceRef{Slug: ref, PipelineSlug: pipeline, DataSourceType: model.DataSourceTypePostgres, RequiredCapabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource}}, nil
}

func (r *loadRefs) GetEnvironmentBinding(_ context.Context, org, project, pipeline, env, ref slug.Slug) (*model.DataSourceBinding, error) {
	require.Equal(r.t, []slug.Slug{"tenant", "project", "selected", "ledger"}, []slug.Slug{org, project, pipeline, ref})
	r.environments = append(r.environments, env)
	if env == "prod" && r.bindingError != nil {
		return nil, r.bindingError
	}
	return &model.DataSourceBinding{EnvironmentSlug: env, DataSourceRefSlug: ref, PipelineSlug: pipeline, DataSourceType: model.DataSourceTypePostgres, DataSourceActive: true}, nil
}

func TestLoadSelectedPipeline(t *testing.T) {
	for _, tc := range []struct {
		name         string
		bindingError error
	}{
		{name: "enabled environments"}, {name: "later binding lookup fails", bindingError: xerror.Unexpected().Detail("binding read unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs := &loadRefs{t: t, bindingError: tc.bindingError}
			report, err := Load(t.Context(), loadPipelines{t: t}, loadEnvironments{t: t}, refs, "tenant", "project", "selected")
			require.Equal(t, 1, refs.refReads)
			require.Equal(t, []slug.Slug{"dev", "prod"}, refs.environments)
			if tc.bindingError != nil {
				require.ErrorIs(t, err, tc.bindingError)
				require.Equal(t, Report{}, report)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "blocked", report.Status)
			require.Len(t, report.Environments, 2)
			require.Equal(t, slug.Slug("dev"), report.Environments[0].EnvironmentSlug)
			require.Equal(t, "ready", report.Environments[0].Status)
			prod := report.Environments[1]
			require.Equal(t, slug.Slug("prod"), prod.EnvironmentSlug)
			require.Len(t, prod.Diagnostics, 1)
			require.Equal(t, "missing_variable", prod.Diagnostics[0].Code)
			require.Equal(t, "batch", prod.Diagnostics[0].RequirementKey)
		})
	}
}

type pipelineWithoutVariables struct{ pipelinesvc.Service }

func (pipelineWithoutVariables) Get(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*pipelinesvc.PipelineDetail, error) {
	return &pipelinesvc.PipelineDetail{Spec: &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "selected"}}}, nil
}

type environmentsWithoutComparison struct{ loadEnvironments }

func (e environmentsWithoutComparison) CompareRuntimeVariables(context.Context, slug.Slug, []string) (model.RuntimeVariableComparison, error) {
	return model.RuntimeVariableComparison{}, xerror.Unexpected().Detail("unexpected comparison")
}

func TestLoadWithoutVariables(t *testing.T) {
	report, err := Load(t.Context(), pipelineWithoutVariables{}, environmentsWithoutComparison{loadEnvironments{t: t}}, &loadRefs{t: t}, "tenant", "project", "selected")
	require.NoError(t, err)
	require.Equal(t, "ready", report.Status)
}
