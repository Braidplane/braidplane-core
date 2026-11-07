package pipelinetriggersvc

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
)

var (
	// ErrPipelineTriggerNotFound indicates that the trigger does not exist in the requested environment.
	ErrPipelineTriggerNotFound = pipelinetriggers.ErrPipelineTriggerNotFound

	// ErrPipelineTriggerSlugTaken indicates that a trigger slug already exists in the requested environment.
	ErrPipelineTriggerSlugTaken = pipelinetriggers.ErrPipelineTriggerSlugTaken

	// ErrInvalidPipelineTriggerInput indicates that the trigger definition is not launchable.
	ErrInvalidPipelineTriggerInput = xerror.ValidationFailed()

	// ErrProjectEnvironmentNotFound indicates that the target environment is not enabled for the project.
	ErrProjectEnvironmentNotFound = xerror.NotFound(xerror.ProjectEnvironment)

	// ErrPipelineNotFound indicates that the target pipeline does not exist in the project.
	ErrPipelineNotFound = xerror.NotFound(xerror.Pipeline)

	// ErrPipelineVersionNotFound indicates that the pinned target version does not exist.
	ErrPipelineVersionNotFound = xerror.NotFound(xerror.PipelineVersion)

	// ErrPipelineDeploymentNotFound indicates that no deployed version exists for enabled latest_deployed triggers.
	ErrPipelineDeploymentNotFound = xerror.NotFound(xerror.PipelineDeployment)
)
