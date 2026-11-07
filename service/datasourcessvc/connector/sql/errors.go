package sql

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	// ErrConnectionFailed indicates that a connection to the SQL data source could not be
	// established.
	ErrConnectionFailed = xerror.ConnectionFailed(xerror.DataSource)

	// ErrQueryExecutionFailed indicates that a SQL query failed during execution.
	ErrQueryExecutionFailed = xerror.Unavailable(xerror.Query)

	// ErrSchemaIntrospection indicates that SQL schema retrieval failed.
	ErrSchemaIntrospection = xerror.Unavailable(xerror.SchemaMetadata)

	// ErrUnknownConnector indicates that the SQL connector type is not registered.
	ErrUnknownConnector = xerror.Unsupported(xerror.DataSource)

	// ErrInvalidConfig indicates that the SQL connector configuration is invalid or incomplete.
	ErrInvalidConfig = xerror.InvalidInput(xerror.DataSource)
)
