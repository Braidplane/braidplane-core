package pipelinesinkeffects

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	// ErrPipelineSinkEffectNotFound indicates that no effect exists in the requested tenant scope.
	ErrPipelineSinkEffectNotFound = xerror.NotFound(xerror.PipelineSinkEffect).Detail("pipeline sink effect not found")

	// ErrProjectEnvironmentNotFound indicates that the requested tenant environment is unavailable.
	ErrProjectEnvironmentNotFound = xerror.NotFound(xerror.ProjectEnvironment)
)
