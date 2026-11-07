package views

import (
	"net/url"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

type PipelineSyncView struct {
	Org, Project, Pipeline, Environment string
	Backend                             *api.ProjectPipelineBackend
	Report                              *api.PipelineReconcileResponse
	Error                               string
}

func PipelineSyncHref(org, project, environment, pipeline string) string {
	values := url.Values{}
	if environment != "" {
		values.Set("environment_slug", environment)
	}
	if pipeline != "" {
		values.Set("pipeline_slug", pipeline)
	}
	return "/orgs/" + org + "/projects/" + project + "/pipeline-sync?" + values.Encode()
}

func PipelineDeployHref(org, project, environment, pipeline, hash string) string {
	values := url.Values{"spec_hash": {hash}}
	if environment != "" {
		values.Set("environment_slug", environment)
	}
	return "/orgs/" + org + "/projects/" + project + "/pipelines/" + pipeline + "/deploy?" + values.Encode()
}

func pipelineSyncCanApply(report *api.PipelineReconcileResponse) bool {
	return report != nil && report.DryRun && report.Source.DataSourceSlug != nil && report.Source.Repo != nil && report.Source.Ref != nil && report.Source.Commit != nil && *report.Source.Commit != ""
}

type PipelineDeployView struct {
	Compiled                            *api.PipelineCompiledPlan
	CompiledDigest, ResolutionDigest    string
	ProjectionJSON                      string
	ResourceScopesJSON                  string
	ExpectedRevision                    int64
	Org, Project, Pipeline, Environment string
	Version                             *api.PipelineVersion
	Environments                        []api.ProjectEnvironment
	Protected                           bool
	Current                             *api.PipelineDeployment
	Deployed                            *api.PipelineDeployment
	Error                               string
	CanDeploy                           bool
	CanCompile                          bool
}
