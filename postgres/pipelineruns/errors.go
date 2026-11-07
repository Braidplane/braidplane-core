package pipelineruns

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	ErrOrganizationNotFound = xerror.NotFound(xerror.Organization)
	ErrRunNotFound          = xerror.NotFound(xerror.PipelineRun)
	ErrRunAlreadyExists     = xerror.AlreadyExists(xerror.PipelineRun)
)
