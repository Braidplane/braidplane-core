package pipelineruntime

import (
	"context"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelineruntimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// PrepareCheckpointBootstrap resolves the same source identities as execution without reading providers or running sinks.
func (r *Runner) PrepareCheckpointBootstrap(ctx context.Context, req RunRequest) (lookups []pipelinerun.IncrementalCheckpointLookup, prepareErr error) {
	pipeline, err := clonePipeline(req.Pipeline)
	if err != nil {
		return nil, err
	}
	if err := pipelineruntimevars.ResolvePipelineInput(pipeline, req.RuntimeInput); err != nil {
		return nil, err
	}
	redactor, err := r.resolveEnvironmentSecrets(ctx, req.OrgSlug, req.EnvironmentSlug, pipeline)
	defer func() { prepareErr = redactor.RedactError(prepareErr) }()
	if err != nil {
		return nil, err
	}
	if err := pipelinevalidate.Validate(pipeline); err != nil {
		return nil, err
	}
	lookups = []pipelinerun.IncrementalCheckpointLookup{}
	for _, step := range pipeline.Steps {
		if step.Query == nil || step.Query.Incremental == nil {
			continue
		}
		ref, inline, usesInline, err := resolveQueryOperationForStep(step)
		if err != nil {
			return nil, err
		}
		lookup, _, err := incrementalCheckpointLookupForStep(pipeline.Metadata.Slug, req.PipelineVersionID, pipelinerun.DefaultIncrementalCheckpointNamespace, step, ref, inline, usesInline)
		if err != nil {
			return nil, err
		}
		lookups = append(lookups, lookup)
	}
	if len(lookups) == 0 {
		return nil, xerror.InvalidInput(xerror.Pipeline).Detail("pipeline has no incremental sources")
	}
	return lookups, nil
}
