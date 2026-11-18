package readinesssvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/environmentssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

// Load evaluates the current persisted specs. An empty pipeline selects the whole project.
func Load(ctx context.Context, pipelines pipelinesvc.Service, environments environmentssvc.Service, refs datasourcerefssvc.Service, org, project, pipeline slug.Slug) (Report, error) {
	slugs := []slug.Slug{pipeline}
	if pipeline == "" {
		summaries, err := pages(func(req model.ListRequest) (model.ListResponse[*pipelinesvc.PipelineSummary], error) {
			return pipelines.List(ctx, org, project, "", req)
		})
		if err != nil {
			return Report{}, err
		}
		slugs = make([]slug.Slug, len(summaries))
		for i, s := range summaries {
			slugs[i] = s.Slug
		}
	}
	inputs := make([]PipelineInput, len(slugs))
	bySlug := make(map[slug.Slug]*PipelineInput, len(slugs))
	for i, s := range slugs {
		detail, err := pipelines.Get(ctx, org, project, s, "")
		if err != nil {
			return Report{}, err
		}
		if detail == nil || detail.Spec == nil {
			return Report{}, xerror.Unexpected().Detail("missing persisted pipeline spec")
		}
		inputs[i].Spec = detail.Spec
		bySlug[s] = &inputs[i]
	}
	enabled, err := pages(func(req model.ListRequest) (model.ListResponse[model.ProjectEnvironment], error) {
		return environments.ListProjectEnvironments(ctx, org, project, req)
	})
	if err != nil {
		return Report{}, err
	}
	envs := make([]EnvironmentInput, len(enabled))
	vars := make(map[slug.Slug]map[string]bool, len(enabled))
	for i, e := range enabled {
		envs[i] = EnvironmentInput{Slug: e.Environment.Slug, Variables: map[string]bool{}}
		vars[e.Environment.Slug] = envs[i].Variables
	}
	if len(inputs) == 0 || len(envs) == 0 {
		return Evaluate(inputs, envs), nil
	}
	if pipeline == "" {
		err = loadProjectRefs(ctx, refs, org, project, bySlug, envs)
	} else {
		err = loadPipelineRefs(ctx, refs, org, project, pipeline, &inputs[0], envs)
	}
	if err != nil {
		return Report{}, err
	}
	comparison, err := environments.CompareRuntimeVariables(ctx, org, nil)
	if err != nil {
		return Report{}, err
	}
	for _, row := range comparison.Rows {
		for i, cell := range row.Cells {
			if keys := vars[comparison.Environments[i]]; keys != nil {
				keys[row.Key] = cell.Variable != nil && !cell.TypeMismatch
			}
		}
	}
	return Evaluate(inputs, envs), nil
}

func loadPipelineRefs(ctx context.Context, refs datasourcerefssvc.Service, org, project, pipeline slug.Slug, input *PipelineInput, envs []EnvironmentInput) error {
	seen := map[slug.Slug]bool{}
	for _, req := range runtime.ExtractRequirements(input.Spec).DataSources {
		if seen[req.Ref] {
			continue
		}
		seen[req.Ref] = true
		ref, err := refs.GetDataSourceRef(ctx, org, project, pipeline, req.Ref)
		if xerror.Is(err, datasourcerefssvc.ErrDataSourceRefNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		input.Refs = append(input.Refs, *ref)
		for _, env := range envs {
			binding, err := refs.GetEnvironmentBinding(ctx, org, project, pipeline, env.Slug, req.Ref)
			if xerror.Is(err, datasourcerefssvc.ErrDataSourceBindingNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			input.Bindings = append(input.Bindings, *binding)
		}
	}
	return nil
}

func loadProjectRefs(ctx context.Context, refs datasourcerefssvc.Service, org, project slug.Slug, bySlug map[slug.Slug]*PipelineInput, envs []EnvironmentInput) error {
	logical, err := pages(func(req model.ListRequest) (model.ListResponse[model.DataSourceRef], error) {
		return refs.ListDataSourceRefs(ctx, org, project, req)
	})
	if err != nil {
		return err
	}
	for _, r := range logical {
		if p := bySlug[r.PipelineSlug]; p != nil {
			p.Refs = append(p.Refs, r)
		}
	}
	for _, env := range envs {
		bindings, err := pages(func(req model.ListRequest) (model.ListResponse[model.DataSourceBinding], error) {
			return refs.ListEnvironmentBindings(ctx, org, project, env.Slug, req)
		})
		if err != nil {
			return err
		}
		for _, b := range bindings {
			if p := bySlug[b.PipelineSlug]; p != nil {
				p.Bindings = append(p.Bindings, b)
			}
		}
	}
	return nil
}

func pages[T any](list func(model.ListRequest) (model.ListResponse[T], error)) ([]T, error) {
	var items []T
	req := model.ListRequest{Limit: 100}
	for {
		page, err := list(req)
		if err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		if !page.HasMore {
			return items, nil
		}
		if page.NextCursor == nil || *page.NextCursor == req.Cursor {
			return nil, xerror.Unexpected().Detail("invalid readiness pagination cursor")
		}
		req.Cursor = *page.NextCursor
	}
}
