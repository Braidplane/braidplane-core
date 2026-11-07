package views

import (
	"context"
	"net/url"
	"strconv"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

type runProgressKey struct{}

type RunProgressInspection struct {
	Evidence        *api.PipelineRunProgress
	Poll            bool
	VersionRecorded bool
}

func WithRunProgress(ctx context.Context, inspection RunProgressInspection) context.Context {
	return context.WithValue(ctx, runProgressKey{}, inspection)
}

func runProgressInspection(ctx context.Context) RunProgressInspection {
	inspection, _ := ctx.Value(runProgressKey{}).(RunProgressInspection)
	return inspection
}

func runProgressHref(ctx context.Context, detailHref string, evidence *api.PipelineRunProgress) string {
	href, _ := url.Parse(runSectionHref(ctx, detailHref, runDetailSection(ctx)))
	query := href.Query()
	query.Set("progress_poll", "1")
	if evidence != nil {
		query.Set("progress_revision", strconv.FormatInt(evidence.Revision, 10))
		query.Set("progress_events", strconv.FormatInt(evidence.EventCount, 10))
		query.Set("progress_status", string(evidence.Metadata.Status))
	}
	href.RawQuery = query.Encode()
	return href.String()
}

func runProgressLivenessLabel(evidence *api.PipelineRunProgress) string {
	switch evidence.WorkerLiveness {
	case "recent":
		return "Worker reporting"
	case "stale":
		return "Worker reporting overdue; liveness unknown"
	case "terminal":
		return "Attempt finished"
	default:
		return "Worker telemetry unavailable"
	}
}

func runProgressDuration(evidence *api.PipelineRunProgress) string {
	duration := evidence.Metadata.DurationMs
	if evidence.Metadata.Status == "queued" || evidence.Metadata.Status == "running" {
		duration = max(int64(0), evidence.ServerTime.Sub(evidence.Metadata.StartedAt).Milliseconds())
	}
	return runDurationLabel(duration)
}
