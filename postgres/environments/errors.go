package environments

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	ErrNotFound                   = xerror.NotFound(xerror.Environment)
	ErrSlugTaken                  = xerror.AlreadyExists(xerror.Environment)
	ErrProjectEnvironmentNotFound = xerror.NotFound(xerror.ProjectEnvironment)
	ErrProjectEnvironmentTaken    = xerror.AlreadyExists(xerror.ProjectEnvironment)
	ErrRuntimeVariableNotFound    = xerror.NotFound(xerror.RuntimeVariable)
	ErrEnvironmentSecretNotFound  = xerror.NotFound(xerror.EnvironmentSecret)
)
