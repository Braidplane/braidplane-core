package views

import (
	"net/url"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

type QueryPipelineSetupData struct {
	Org, DataSource, Project, Name, Slug, QueryRevision, Error string
	Query                                                      *api.Query
	Projects                                                   []api.Project
	Permissions                                                OrganizationPermissions
	BackendConfigured, ProjectAvailable                        bool
	ParameterValues                                            map[string]string
}

type PipelineCreationProgressData struct {
	Org, Project, Environment, Error, VersionError string
	Creation                                       *api.PipelineCreationProposal
	VersionAvailable, CanRetry                     bool
}

type PipelineCreationListData struct {
	Org, Project, Environment, Error string
	Page                             *api.PipelineCreationProposalListResponse
}

func PipelineCreationListHref(org, project, environment, cursor string) string {
	path := "/orgs/" + url.PathEscape(org) + "/projects/" + url.PathEscape(project) + "/pipeline-creations"
	values := url.Values{}
	if environment != "" {
		values.Set("environment_slug", environment)
	}
	if cursor != "" {
		values.Set("cursor", cursor)
	}
	if len(values) > 0 {
		path += "?" + values.Encode()
	}
	return path
}

func QueryPipelineSetupHref(org, source, query, project string) string {
	path := "/orgs/" + url.PathEscape(org) + "/data-sources/" + url.PathEscape(source) + "/queries/" + url.PathEscape(query) + "/pipeline"
	if project != "" {
		path += "?" + url.Values{"project": {project}}.Encode()
	}
	return path
}

func PipelineCreationProgressHref(org, project, creation, environment string) string {
	path := "/orgs/" + url.PathEscape(org) + "/projects/" + url.PathEscape(project) + "/pipeline-creations/" + url.PathEscape(creation)
	if environment != "" {
		path += "?" + url.Values{"environment_slug": {environment}}.Encode()
	}
	return path
}

func queryPipelineParameterValue(data QueryPipelineSetupData, parameter api.QueryParameter) string {
	if value, present := data.ParameterValues[parameter.Name]; present {
		return value
	}
	return valueOrEmpty(parameter.DefaultValue)
}

func pipelineCreationStatusLabel(status api.PipelineCreationProposalStatus) string {
	switch status {
	case api.CreationPublishQueued:
		return "Queued"
	case api.CreationBranchPushed:
		return "Branch pushed"
	case api.CreationProposalOpen:
		return "Opened for review"
	case api.CreationPublishFailed:
		return "Could not open proposal"
	default:
		return "Unavailable"
	}
}
