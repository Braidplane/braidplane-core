package readinesssvc

import (
	"context"
	"slices"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
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
	for i, s := range slugs {
		detail, err := pipelines.Get(ctx, org, project, s, "")
		if err != nil {
			return Report{}, err
		}
		if detail == nil || detail.Spec == nil {
			return Report{}, xerror.Unexpected().Detail("missing persisted pipeline spec")
		}
		inputs[i].Spec = detail.Spec
	}
	envs, err := loadSetup(ctx, environments, refs, org, project, pipeline, inputs)
	if err != nil {
		return Report{}, err
	}
	return Evaluate(inputs, envs), nil
}

// LoadSpecs evaluates incoming pipeline specs against the project's current setup.
func LoadSpecs(ctx context.Context, environments environmentssvc.Service, refs datasourcerefssvc.Service, org, project slug.Slug, specs []*pipelinespec.Pipeline) ([]Report, error) {
	reports := make([]Report, len(specs))
	if len(specs) == 0 {
		return reports, nil
	}
	inputs := make([]PipelineInput, len(specs))
	for i, spec := range specs {
		if spec == nil {
			return nil, xerror.Unexpected().Detail("missing incoming pipeline spec")
		}
		inputs[i].Spec = spec
	}
	envs, err := loadSetup(ctx, environments, refs, org, project, "", inputs)
	if err != nil {
		return nil, err
	}
	for i := range specs {
		reports[i] = Evaluate(inputs[i:i+1], envs)
	}
	return reports, nil
}

func loadSetup(ctx context.Context, environments environmentssvc.Service, refs datasourcerefssvc.Service, org, project, pipeline slug.Slug, inputs []PipelineInput) ([]EnvironmentInput, error) {
	enabled, err := pages(func(req model.ListRequest) (model.ListResponse[model.ProjectEnvironment], error) {
		return environments.ListProjectEnvironments(ctx, org, project, req)
	})
	if err != nil {
		return nil, err
	}
	envs := make([]EnvironmentInput, len(enabled))
	vars := make(map[slug.Slug]map[string]bool, len(enabled))
	for i, e := range enabled {
		envs[i] = EnvironmentInput{Slug: e.Environment.Slug, Variables: map[string]bool{}}
		vars[e.Environment.Slug] = envs[i].Variables
	}
	if len(inputs) == 0 || len(envs) == 0 {
		return envs, nil
	}
	if pipeline == "" {
		bySlug := make(map[slug.Slug][]*PipelineInput, len(inputs))
		for i := range inputs {
			s := inputs[i].Spec.Metadata.Slug
			bySlug[s] = append(bySlug[s], &inputs[i])
		}
		err = loadProjectRefs(ctx, refs, org, project, bySlug, envs)
	} else {
		err = loadPipelineRefs(ctx, refs, org, project, pipeline, &inputs[0], envs)
	}
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(inputs, func(p PipelineInput) bool {
		return len(runtime.ExtractRequirements(p.Spec).EnvironmentVariables) > 0
	}) {
		return envs, nil
	}
	comparison, err := environments.CompareRuntimeVariables(ctx, org, nil)
	if err != nil {
		return nil, err
	}
	for _, row := range comparison.Rows {
		for i, cell := range row.Cells {
			if keys := vars[comparison.Environments[i]]; keys != nil {
				keys[row.Key] = cell.Variable != nil && !cell.TypeMismatch
			}
		}
	}
	return envs, nil
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

func loadProjectRefs(ctx context.Context, refs datasourcerefssvc.Service, org, project slug.Slug, bySlug map[slug.Slug][]*PipelineInput, envs []EnvironmentInput) error {
	logical, err := pages(func(req model.ListRequest) (model.ListResponse[model.DataSourceRef], error) {
		return refs.ListDataSourceRefs(ctx, org, project, req)
	})
	if err != nil {
		return err
	}
	for _, r := range logical {
		for _, p := range bySlug[r.PipelineSlug] {
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
			for _, p := range bySlug[b.PipelineSlug] {
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
