package datasourcessvc

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
)

// Re-exported from repository layer — single source of truth.
var (
	ErrDataSourceNotFound             = repods.ErrDataSourceNotFound
	ErrDataSourceUsedAsProjectBackend = repods.ErrDataSourceUsedAsProjectBackend
	ErrConnectionTestConflict         = repods.ErrConnectionTestConflict
	ErrSlugTaken                      = repods.ErrSlugTaken
)

// Service-only errors.
var (
	ErrInvalidInput                   = xerror.ValidationFailed()
	ErrInvalidDataSourceType          = xerror.InvalidInput(xerror.DataSource)
	ErrConnectionFailed               = xerror.ConnectionFailed(xerror.DataSource)
	ErrSchemaIntrospectionUnsupported = xerror.DataSourceSchemaIntrospectionUnsupported()
	ErrInvalidSchemaURL               = xerror.InvalidSchemaURL()
	ErrSchemaFetchFailed              = xerror.SchemaFetchFailed()
	ErrInvalidOpenAPI                 = xerror.InvalidOpenAPI()
	ErrDataSourceReadOnly             = xerror.DataSourceReadOnly()
)
