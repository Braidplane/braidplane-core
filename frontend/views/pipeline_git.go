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
	return pipelineDeploySelectionHref(org, project, environment, pipeline, hash, "", "")
}

func pipelineDeploySelectionHref(org, project, environment, pipeline, hash, compiled, resolution string) string {
	values := url.Values{"spec_hash": {hash}}
	if environment != "" {
		values.Set("environment_slug", environment)
	}
	if compiled != "" {
		values.Set("compiled_plan_digest", compiled)
	}
	if resolution != "" {
		values.Set("resolution_digest", resolution)
	}
	return "/orgs/" + org + "/projects/" + project + "/pipelines/" + pipeline + "/deploy?" + values.Encode()
}

func pipelineSyncCanApply(report *api.PipelineReconcileResponse) bool {
	return report != nil && report.DryRun && report.Source.DataSourceSlug != nil && report.Source.Repo != nil && report.Source.Ref != nil && report.Source.Commit != nil && *report.Source.Commit != ""
}

func pipelineSyncFailureGuidance(stage api.PipelineReconcileFailureStage) string {
	switch stage {
	case api.PipelineReconcileFailureStageLoad, api.PipelineReconcileFailureStageMarshal:
		return "Check the committed pipeline definition for syntax and validation errors, then preview again."
	case api.PipelineReconcileFailureStageRead, api.PipelineReconcileFailureStageSource:
		return "Check that the committed file and configured Git source are available, then preview again."
	default:
		return "This change could not be saved. Preview again to check the current state before retrying."
	}
}

type PipelineDeployView struct {
	Compiled                            *api.PipelineCompiledPlan
	CompiledDigest, ResolutionDigest    string
	ProjectionJSON                      string
	ResourceScopesJSON                  string
	InvalidCompileField                 string
	ExpectedRevision                    int64
	Org, Project, Pipeline, Environment string
	Version                             *api.PipelineVersion
	Environments                        []api.ProjectEnvironment
	TargetEnabled                       bool
	Protected                           bool
	Current                             *api.PipelineDeployment
	Deployed                            *api.PipelineDeployment
	Error                               string
	CanDeploy                           bool
	CanCompile                          bool
	ReviewDeployment                    bool
	ReviewTarget                        bool
	RollbackSelectionCleared            bool
}
