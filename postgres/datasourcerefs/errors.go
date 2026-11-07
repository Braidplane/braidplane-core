package datasourcerefs

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	ErrDataSourceRefNotFound        = xerror.NotFound(xerror.DataSourceRef)
	ErrDataSourceRefSlugTaken       = xerror.AlreadyExists(xerror.DataSourceRef)
	ErrDataSourceBindingNotFound    = xerror.NotFound(xerror.DataSourceBinding)
	ErrProjectEnvironmentNotEnabled = xerror.NotFound(xerror.ProjectEnvironment)
)
