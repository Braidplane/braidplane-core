package pipelinetriggers

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var (
	// ErrPipelineTriggerNotFound indicates that the trigger does not exist in the requested environment.
	ErrPipelineTriggerNotFound = xerror.NotFound(xerror.PipelineTrigger)

	// ErrPipelineTriggerSlugTaken indicates that a trigger slug already exists in the requested environment.
	ErrPipelineTriggerSlugTaken = xerror.AlreadyExists(xerror.PipelineTrigger).Detail("pipeline trigger slug already exists")

	// ErrPipelineTriggerDependencyMissing indicates that trigger creation references a missing environment, pipeline, or version.
	ErrPipelineTriggerDependencyMissing = xerror.InvalidInput(xerror.PipelineTrigger).Detail("pipeline trigger dependency is missing")

	// ErrPipelineVersionNotFound indicates that a pinned pipeline version does not exist.
	ErrPipelineVersionNotFound = xerror.NotFound(xerror.PipelineVersion)

	// ErrPipelineVersionAmbiguous indicates that a pinned version hash prefix matched more than one version.
	ErrPipelineVersionAmbiguous = xerror.InvalidInput(xerror.PipelineVersion)
)
