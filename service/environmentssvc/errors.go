package environmentssvc

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/environments"
)

var (
	ErrEnvironmentNotFound        = environments.ErrNotFound
	ErrSlugTaken                  = environments.ErrSlugTaken
	ErrInvalidEnvironmentSlug     = xerror.InvalidInput(xerror.Environment)
	ErrProjectEnvironmentNotFound = environments.ErrProjectEnvironmentNotFound
	ErrRuntimeVariableNotFound    = environments.ErrRuntimeVariableNotFound
	ErrInvalidRuntimeVariable     = xerror.InvalidInput(xerror.RuntimeVariable)
	ErrEnvironmentSecretNotFound  = environments.ErrEnvironmentSecretNotFound
	ErrInvalidEnvironmentSecret   = xerror.InvalidInput(xerror.EnvironmentSecret)
)
