package views

import (
	"net/url"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/runlineage"
)

type PipelineRunPreview struct {
	Runs               []api.PipelineRunMetadata
	SelectedSlug       string
	LatestSlug         string
	Run                *api.PipelineRunRecord
	Lineage            runlineage.View
	Message            string
	HistoryUnavailable bool
}

func pipelineRunSelectionHref(base, run string) string {
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	query := parsed.Query()
	query.Set("section", "runs")
	query.Set("run_slug", run)
	query.Del("lineage_step")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func pipelineRunVersionLabel(hash string, deployments []PipelineDeploymentBadge, environment string, unavailable bool) string {
	if hash == "" {
		return "Executed version not recorded"
	}
	short := shortPipelineVersionSpecHash(&hash)
	if unavailable {
		return "Executed " + short + " · deployment unavailable"
	}
	for _, deployment := range deployments {
		if deployment.EnvironmentSlug == environment {
			if deployment.FullSpecHash == hash {
				return "Executed " + short + " · current deployment"
			}
			return "Executed " + short + " · deployed now " + deployment.ShortSpecHash
		}
	}
	return "Executed " + short + " · no current deployment"
}
