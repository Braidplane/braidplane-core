package datasources

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	// ErrDataSourceNotFound indicates that the data source does not exist.
	ErrDataSourceNotFound = xerror.NotFound(xerror.DataSource)

	// ErrDataSourceUsedAsProjectBackend indicates the data source is still referenced by a project backend.
	ErrDataSourceUsedAsProjectBackend = xerror.InvalidInput(xerror.DataSource).
						Detail("data source is used as a project pipeline backend")

	// ErrSlugTaken indicates that the slug is already in use within the organization.
	ErrSlugTaken = xerror.AlreadyExists(xerror.DataSource)

	// ErrConnectionTestConflict indicates that the tested data source changed before the outcome was stored.
	ErrConnectionTestConflict = xerror.New(
		xerror.KindAlreadyExists,
		"DATA_SOURCE_CONNECTION_TEST_CONFLICT",
		"data source changed during connection test",
	)

	// ErrSchemaMetadataNotFound indicates that cached schema metadata does not exist.
	ErrSchemaMetadataNotFound = xerror.NotFound(xerror.SchemaMetadata)

	// ErrSchemaImportAttemptNotCurrent indicates that a queued import attempt is stale.
	ErrSchemaImportAttemptNotCurrent = xerror.New(
		xerror.KindNotFound,
		"DATA_SOURCE_SCHEMA_IMPORT_ATTEMPT_NOT_CURRENT",
		"data source schema import attempt not current",
	)
)
