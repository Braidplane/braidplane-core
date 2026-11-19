package readinesssvc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/environmentssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
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
			report, err := Load(t.Context(), loadPipelines{t: t}, loadEnvironments{t: t}, refs, nil, "tenant", "project", "selected")
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
			dev := report.Environments[0].Requirements
			require.Len(t, dev, 2)
			require.Equal(t, &Variable{Source: model.RuntimeVariableOverride, Present: true}, dev[1].Variable)
			encoded, err := json.Marshal(report)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "synthetic")
			prod := report.Environments[1]
			require.Equal(t, slug.Slug("prod"), prod.EnvironmentSlug)
			require.Len(t, prod.Diagnostics, 1)
			require.Equal(t, "missing_variable", prod.Diagnostics[0].Code)
			require.Equal(t, "batch", prod.Diagnostics[0].RequirementKey)
			require.Equal(t, &Variable{Source: model.RuntimeVariableMissing}, prod.Requirements[1].Variable)
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
	report, err := Load(t.Context(), pipelineWithoutVariables{}, environmentsWithoutComparison{loadEnvironments{t: t}}, &loadRefs{t: t}, nil, "tenant", "project", "selected")
	require.NoError(t, err)
	require.Equal(t, "ready", report.Status)
	encoded, err := json.Marshal(report.Environments[0])
	require.NoError(t, err)
	require.JSONEq(t, `{"environment_slug":"dev","status":"ready","diagnostics":[],"requirements":[]}`, string(encoded))
}

type projectRefs struct {
	datasourcerefssvc.Service
	t     *testing.T
	lists int
	envs  []slug.Slug
}

func (r *projectRefs) ListDataSourceRefs(_ context.Context, org, project slug.Slug, _ model.ListRequest) (model.ListResponse[model.DataSourceRef], error) {
	require.Equal(r.t, []slug.Slug{"tenant", "project"}, []slug.Slug{org, project})
	r.lists++
	return model.ListResponse[model.DataSourceRef]{Items: []model.DataSourceRef{{PipelineSlug: "selected", Slug: "ledger", DataSourceType: model.DataSourceTypePostgres, RequiredCapabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource}}}}, nil
}

func (r *projectRefs) ListEnvironmentBindings(_ context.Context, org, project, env slug.Slug, _ model.ListRequest) (model.ListResponse[model.DataSourceBinding], error) {
	require.Equal(r.t, []slug.Slug{"tenant", "project"}, []slug.Slug{org, project})
	r.envs = append(r.envs, env)
	var bindings []model.DataSourceBinding
	if env == "dev" {
		bindings = []model.DataSourceBinding{{PipelineSlug: "selected", EnvironmentSlug: env, DataSourceRefSlug: "ledger", DataSourceType: model.DataSourceTypePostgres, DataSourceActive: true}}
	}
	return model.ListResponse[model.DataSourceBinding]{Items: bindings}, nil
}

func TestLoadSpecsEvaluatesIncomingRequirementsInOrder(t *testing.T) {
	refs := &projectRefs{t: t}
	detail, err := (loadPipelines{t: t}).Get(t.Context(), "tenant", "project", "selected", "")
	require.NoError(t, err)
	first := detail.Spec
	second := *first
	second.Steps = nil
	third := *first
	third.Steps = []pipelinespec.Step{{Slug: "read", Kind: "query", Query: &pipelinespec.QueryRef{DataSourceSlug: "ledger", OperationType: "sql"}}}
	reports, err := LoadSpecs(t.Context(), loadEnvironments{t: t}, refs, nil, "tenant", "project", []*pipelinespec.Pipeline{first, &second, &third})
	require.NoError(t, err)
	require.Len(t, reports, 3)
	encoded, err := json.Marshal(reports)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"requirements"`)
	require.Equal(t, 1, refs.lists)
	require.Equal(t, []slug.Slug{"dev", "prod"}, refs.envs)
	require.Equal(t, "ready", reports[0].Environments[0].Status)
	prod := reports[0].Environments[1]
	require.Len(t, prod.Diagnostics, 2)
	require.Equal(t, "missing_binding", prod.Diagnostics[0].Code)
	require.Equal(t, "missing_variable", prod.Diagnostics[1].Code)
	require.Equal(t, "batch", prod.Diagnostics[1].RequirementKey)
	require.Equal(t, "ready", reports[1].Status)
	require.Equal(t, "ready", reports[2].Environments[0].Status)
	require.Len(t, reports[2].Environments[1].Diagnostics, 1)
}

func TestSavedOperationsUseConcreteEnvironmentBinding(t *testing.T) {
	source, sink := model.DataSourceRefCapabilityPipelineSource, model.DataSourceRefCapabilityPipelineSink
	p := PipelineInput{
		Spec: &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "pipeline"}, Steps: []pipelinespec.Step{
			{Slug: "source", Kind: "query", Query: &pipelinespec.QueryRef{OperationType: "sql", DataSourceSlug: "logical", OperationRef: "read"}},
			{Slug: "again", Kind: "query", Query: &pipelinespec.QueryRef{OperationType: "sql", DataSourceSlug: "logical", OperationRef: "read"}},
			{Slug: "sink", Kind: "sink", Sink: &pipelinespec.SinkSpec{OperationType: "sql", DataSourceSlug: "logical", OperationRef: "write"}},
		}},
		Refs: []model.DataSourceRef{{Slug: "logical", DataSourceType: model.DataSourceTypePostgres, RequiredCapabilities: []model.DataSourceRefCapability{source, sink}}},
	}
	envs := []EnvironmentInput{{Slug: "dev"}, {Slug: "prod"}}
	for _, env := range envs {
		p.Bindings = append(p.Bindings, model.DataSourceBinding{EnvironmentSlug: env.Slug, DataSourceRefSlug: "logical", DataSourceSlug: env.Slug, DataSourceType: model.DataSourceTypePostgres, DataSourceActive: true})
	}
	inputs := []PipelineInput{p, p}
	reads := map[OperationKey]int{}
	err := loadOperations(t.Context(), "tenant", inputs, func(_ context.Context, org, ds, op slug.Slug) (*queriessvc.Query, error) {
		require.Equal(t, slug.Slug("tenant"), org)
		reads[OperationKey{ds, op}]++
		if ds == "prod" {
			if op == "read" {
				return nil, queriessvc.ErrOperationWrongDataSource
			}
			return nil, queriessvc.ErrQueryNotFound
		}
		return &queriessvc.Query{DataSourceSlug: ds, Data: json.RawMessage(`{"statement":"{\"type\":\"postgres\",\"sql\":\"select 1\"}"}`)}, nil
	})
	require.NoError(t, err)
	require.Equal(t, map[OperationKey]int{{"dev", "read"}: 1, {"prod", "read"}: 1, {"dev", "write"}: 1, {"prod", "write"}: 1}, reads)
	report := evaluate(inputs[:1], envs, pipelineDetail)
	require.Equal(t, "ready", report.Environments[0].Status)
	prod := report.Environments[1]
	require.Equal(t, "blocked", prod.Status)
	require.Len(t, prod.Diagnostics, 3)
	for i, want := range []string{"operation_wrong_data_source", "operation_wrong_data_source", "missing_operation"} {
		require.Equal(t, want, prod.Diagnostics[i].Code)
	}
	require.Equal(t, "/steps/2/sink/operation_ref", prod.Diagnostics[2].FieldPath)
	row := prod.Requirements[len(prod.Requirements)-1]
	require.Equal(t, "saved_operation", row.Kind)
	require.Equal(t, "write", row.Key)
	require.Equal(t, slug.Slug("prod"), row.Target.Slug)
	require.Equal(t, "blocked", row.Status)
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "select 1")
}
