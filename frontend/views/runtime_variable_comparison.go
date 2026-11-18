package views

import (
	"net/url"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

type RuntimeVariableComparisonView struct {
	Organization string
	Comparison   *api.RuntimeVariableComparison
	Permissions  OrganizationPermissions
	Protected    []string
	Project      string
	Pipeline     string
	SpecHash     string
	Form         *api.RuntimeVariableDefinition
	Editing      bool
	Unsaved      bool
}

func RuntimeVariableComparisonHref(org, project, pipeline, specHash string) string {
	path := "/organizations/" + org + "/runtime-variables"
	if project == "" {
		return path
	}
	return path + "?" + url.Values{
		"project": {project}, "pipeline": {pipeline}, "spec_hash": {specHash},
	}.Encode()
}

func (v RuntimeVariableComparisonView) Href() string {
	return RuntimeVariableComparisonHref(v.Organization, v.Project, v.Pipeline, v.SpecHash)
}

func (v RuntimeVariableComparisonView) EditHref(key string) string {
	u, _ := url.Parse(v.Href())
	q := u.Query()
	q.Set("define", "1")
	if key != "" {
		q.Set("key", key)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
