package pipelineruntime

import (
	"context"
	"time"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	executionruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func (r *Runner) executionSnapshot(ctx context.Context, req RunRequest, at time.Time) (*executionruntime.ExecutionContext, error) {
	if req.executionContext != nil {
		return req.executionContext, nil
	}
	// A repeated delivery and a new retry attempt both retain the original metadata.
	sourceRun := req.RunSlug
	required := false
	if req.Source.RetryOfRunSlug != nil {
		sourceRun = req.Source.RetryOfRunSlug.String()
		required = true
	}
	if sourceRun != "" {
		timeline, err := r.store.Get(ctx, req.OrgSlug, req.ProjectSlug, req.EnvironmentSlug, sourceRun)
		if err != nil && !xerror.Is(err, pipelinerun.ErrTimelineNotFound) {
			return nil, err
		}
		if timeline != nil && timeline.ExecutionContext != nil {
			return timeline.ExecutionContext, nil
		}
		if required {
			return nil, xerror.InvalidInput(xerror.PipelineRun).Detail("retry requires retained execution context; start a new run for legacy history")
		}
	}
	at = at.UTC()
	prior, err := r.store.PreviousSuccess(ctx, req.OrgSlug, req.ProjectSlug, req.EnvironmentSlug, req.Pipeline.Metadata.Slug, req.PipelineVersionID, at)
	if err != nil && !xerror.Is(err, pipelinerun.ErrTimelineNotFound) {
		return nil, xerror.Wrapf("resolve previous successful run: %w", err)
	}
	return &executionruntime.ExecutionContext{At: at, PreviousSuccess: prior}, nil
}
