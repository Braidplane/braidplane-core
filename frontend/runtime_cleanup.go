package frontend

import (
	"context"
	"encoding/json"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
)

// Negative usage findings require a complete inventory of current specs.
func (r *Router) projectRequirements(ctx context.Context, cookie, org, project string) ([]runtime.Requirements, bool) {
	pipelines, _, _, err := r.listAllPipelinesViaAPI(ctx, cookie, org, project)
	if err != nil {
		return nil, false
	}
	details, unavailable, unauthorized := r.projectPipelineDetailsViaAPI(ctx, cookie, org, project, pipelines)
	if unavailable || unauthorized || len(details) != len(pipelines) {
		return nil, false
	}
	imported := map[string]bool{}
	var requirements []runtime.Requirements
	for i, p := range details {
		version := pipelines[i].CurrentVersionSpecHash
		if p.Slug != pipelines[i].Slug || p.Slug != p.Spec.Metadata.Slug || p.SyncStatus != api.Synced || p.HasDraft || p.Draft != nil {
			return nil, false
		}
		if p.HasPendingSpec == nil || *p.HasPendingSpec {
			return nil, false
		}
		if version == nil || *version == "" || p.CurrentVersionSpecHash == nil || *version != *p.CurrentVersionSpecHash {
			return nil, false
		}
		raw, err := json.Marshal(p.Spec)
		if err != nil {
			return nil, false
		}
		spec, err := validate.Load("pipeline.json", raw)
		if err != nil {
			return nil, false
		}
		imported[p.Slug] = true
		requirements = append(requirements, runtime.ExtractRequirements(spec))
	}
	params := &api.ListPipelineCreationProposalsParams{Limit: new(api.LimitParam(100))}
	seen := map[string]bool{}
	for {
		resp, err := r.apiClient.ListPipelineCreationProposalsWithResponse(ctx, org, project, params, frontendRequestEditor(ctx, cookie))
		if err != nil || resp.JSON200 == nil {
			return nil, false
		}
		page := resp.JSON200
		for _, proposal := range page.PipelineCreations {
			if !imported[proposal.PipelineSlug] && (proposal.ProposalState == nil || *proposal.ProposalState != "closed") {
				return nil, false
			}
		}
		if !page.HasMore {
			return requirements, true
		}
		if page.NextCursor == nil || *page.NextCursor == "" || seen[*page.NextCursor] {
			return nil, false
		}
		seen[*page.NextCursor] = true
		params.Cursor = page.NextCursor
	}
}

type logicalRef struct{ pipeline, ref string }

func projectCleanup(requirements []runtime.Requirements, refs []api.DataSourceRef, environments []api.ProjectEnvironment, bindings map[string][]api.DataSourceBinding) views.CleanupFindings {
	used := map[logicalRef]bool{}
	for _, pipeline := range requirements {
		for _, source := range pipeline.DataSources {
			used[logicalRef{pipeline.Pipeline.Slug.String(), source.Ref.String()}] = true
		}
	}
	findings := views.CleanupFindings{Known: true}
	for _, ref := range refs {
		if !used[logicalRef{ref.PipelineSlug, ref.Slug}] {
			findings.Items = append(findings.Items, "Logical ref · "+ref.PipelineSlug+" / "+ref.Slug)
		}
	}
	for _, environment := range environments {
		for _, binding := range bindings[environment.EnvironmentSlug] {
			if !used[logicalRef{binding.PipelineSlug, binding.DataSourceRefSlug}] {
				findings.Items = append(findings.Items, "Binding · "+binding.EnvironmentSlug+" / "+binding.PipelineSlug+" / "+binding.DataSourceRefSlug)
			}
		}
	}
	return findings
}

// Only fetch binding metadata for projects that reference an operation on this page.
func (r *Router) organizationUsage(ctx context.Context, cookie, org, source string, queries []api.Query) (map[string]bool, bool) {
	projects, _, _, err := r.listAllProjectsViaAPI(ctx, cookie, org)
	if err != nil {
		return nil, false
	}
	keys := map[string]bool{}
	candidates := map[string]bool{}
	for _, q := range queries {
		candidates[q.Slug] = true
	}
	for _, project := range projects {
		requirements, ok := r.projectRequirements(ctx, cookie, org, project.Slug)
		if !ok {
			return nil, false
		}
		operations := map[logicalRef][]string{}
		for _, pipeline := range requirements {
			if source == "" {
				for _, v := range pipeline.EnvironmentVariables {
					keys[v.Key] = true
				}
				continue
			}
			for _, op := range pipeline.SavedOperations {
				if candidates[op.Ref.String()] {
					ref := logicalRef{pipeline.Pipeline.Slug.String(), op.DataSourceRef.String()}
					operations[ref] = append(operations[ref], op.Ref.String())
				}
			}
		}
		if len(operations) == 0 {
			continue
		}
		envs, _, _, err := r.listProjectEnvironmentsViaAPI(ctx, cookie, org, project.Slug)
		if err != nil || len(envs.ProjectEnvironments) == 0 {
			return nil, false
		}
		for _, env := range envs.ProjectEnvironments {
			bindings, _, _, err := r.listDataSourceBindingsViaAPI(ctx, cookie, org, project.Slug, env.EnvironmentSlug)
			if err != nil {
				return nil, false
			}
			bound := map[logicalRef]bool{}
			for _, b := range bindings.DataSourceBindings {
				ref := logicalRef{b.PipelineSlug, b.DataSourceRefSlug}
				bound[ref] = true
				if b.DataSourceSlug == source {
					for _, op := range operations[ref] {
						keys[op] = true
					}
				}
			}
			for ref := range operations {
				if !bound[ref] {
					return nil, false
				}
			}
		}
	}
	return keys, true
}
