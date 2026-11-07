package views

import (
	"context"
	"net/url"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

type PipelineBindingSetupView struct {
	Org, Project, Pipeline, Environment, SpecHash, ReturnHref, Error string
	Definition                                                       *api.Pipeline
	Bindings                                                         PipelineBindingContext
	Permissions                                                      OrganizationPermissions
	ValidationResults                                                []api.PipelineValidationResultRecord
	ValidationUnavailable                                            bool
}

func PipelineBindingSetupHref(org, project, environment, pipeline, hash, returnHref string) string {
	u := &url.URL{Path: "/orgs/" + org + "/projects/" + project + "/pipelines/" + pipeline + "/bindings"}
	values := url.Values{}
	if environment != "" {
		values.Set("environment_slug", environment)
	}
	if hash != "" {
		values.Set("spec_hash", hash)
	}
	if returnHref != "" {
		values.Set("binding_return", returnHref)
	}
	u.RawQuery = values.Encode()
	return u.String()
}

func pipelineBindingCellID(row DataSourceBindingRow, cell DataSourceBindingCell) string {
	return DataSourceBindingFormID("binding-cell", row.PipelineSlug, row.RefSlug, cell.EnvironmentSlug)
}

func pipelineBindingAttentionRows(rows []DataSourceBindingRow, environment string) []DataSourceBindingRow {
	var attention []DataSourceBindingRow
	for _, row := range rows {
		for _, cell := range row.Cells {
			if cell.EnvironmentSlug == environment && cell.State != bindingCellStateBound {
				row.Cells = []DataSourceBindingCell{cell}
				attention = append(attention, row)
			}
		}
	}
	return attention
}

func pipelineBindingEnvironmentEnabled(bindings PipelineBindingContext, environment string) bool {
	for _, target := range bindings.Environments {
		if target.EnvironmentSlug == environment {
			return true
		}
	}
	return false
}

func pipelineBindingSetupAction(ctx context.Context, action string) string {
	current := entityPageURL(ctx)
	if !strings.HasSuffix(current.Path, "/bindings") && current.Query().Get("binding_setup") != "1" {
		return action
	}
	u, _ := url.Parse(action)
	values := u.Query()
	values.Set("binding_setup", "1")
	for _, key := range []string{"spec_hash", "binding_return"} {
		if value := current.Query().Get(key); value != "" {
			values.Set(key, value)
		}
	}
	u.RawQuery = values.Encode()
	return u.String()
}
