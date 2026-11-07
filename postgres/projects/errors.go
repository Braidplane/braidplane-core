package projects

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	ErrNotFound                             = xerror.NotFound(xerror.Project)
	ErrSlugTaken                            = xerror.AlreadyExists(xerror.Project)
	ErrInvalidSortBy                        = xerror.InvalidInput(xerror.Project)
	ErrPipelineBackendDataSourceUnavailable = xerror.Unavailable(xerror.DataSource)
	ErrPipelineBackendTaken                 = xerror.AlreadyExists(xerror.Project)
)
