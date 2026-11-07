package views

import (
	"context"
	"fmt"
	"net/url"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

type (
	recencyContextKey                 struct{}
	operationalCapabilitiesContextKey struct{}
)

func WithRecencyInspection(ctx context.Context, inspection *api.SuccessfulRunRecency) context.Context {
	return context.WithValue(ctx, recencyContextKey{}, inspection)
}

func RecencyInspectable(ctx context.Context) bool {
	value, _ := ctx.Value(recencyContextKey{}).(*api.SuccessfulRunRecency)
	return value != nil && (value.CapabilityEnabled || value.Configured)
}

func WithOperationalCapabilities(ctx context.Context, value *api.PipelineOperationalCapabilities) context.Context {
	return context.WithValue(ctx, operationalCapabilitiesContextKey{}, value)
}

func RecencyAvailable(ctx context.Context) bool {
	value, _ := ctx.Value(operationalCapabilitiesContextKey{}).(*api.PipelineOperationalCapabilities)
	return value != nil && value.SuccessfulRunRecency
}

func RecencyHref(org, project, environment, pipeline string) string {
	return fmt.Sprintf("/orgs/%s/projects/%s/pipelines/%s/environments/%s/recency", org, project, pipeline, environment)
}

func RecencyAlertsHref(org, project, environment, pipeline string) string {
	q := url.Values{"tab": {"policies"}, "notification_type": {string(api.NotificationTypePipelineFreshnessSLABreached)}, "project_slug": {project}, "environment_slug": {environment}, "pipeline_slug": {pipeline}}
	return "/orgs/" + org + "/alerts?" + q.Encode()
}

func RecencyNumber(v *int64) string {
	if v == nil {
		return "Unavailable"
	}
	return fmt.Sprint(*v)
}

func RecencyText(v *string) string {
	if v == nil {
		return "Unavailable"
	}
	return *v
}

func RecencyTime(v *api.Timestamp) string {
	if v == nil {
		return "Unavailable"
	}
	return v.UTC().Format("2006-01-02 15:04:05 UTC")
}

func RecencyAgeInput(v *int64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(*v)
}

func RecencyUnavailableLabel(reason string) string {
	switch reason {
	case "capability_disabled":
		return "Monitoring is unavailable on this server. Retained evidence remains readable."
	case "not_configured":
		return "No expectation is configured for this target."
	case "expectation_disabled":
		return "This expectation is disabled."
	case "environment_unavailable":
		return "The environment is unavailable."
	case "deployment_unavailable":
		return "There is no current deployment."
	default:
		return "Current recency state is unavailable."
	}
}

func RecencyResolutionLabel(reason *string) string {
	if reason == nil {
		return "Open episode"
	}
	if *reason == "qualifying_success" {
		return "Recovered after a qualifying success"
	}
	return "Retired: " + *reason
}
