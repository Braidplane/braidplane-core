package queries

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	// ErrQueryNotFound is returned when a query does not exist.
	ErrQueryNotFound = xerror.NotFound(xerror.Query)

	// ErrSlugTaken is returned when the query slug is already in use within the organization.
	ErrSlugTaken = xerror.AlreadyExists(xerror.Query)

	// ErrDataSourceNotFound is returned when the referenced data source does not exist.
	ErrDataSourceNotFound = xerror.NotFound(xerror.DataSource)

	// ErrQueryNotDeleted is returned when a query must be soft-deleted before the operation can proceed.
	ErrQueryNotDeleted = xerror.New(
		xerror.KindAlreadyExists,
		"QUERY_NOT_DELETED",
		"query must be deleted before purge",
	)

	// ErrInvalidQueryStatement is returned when the query statement is malformed.
	ErrInvalidQueryStatement = xerror.InvalidInput(xerror.Query)

	// ErrParameterValidationFailed is returned when query parameters fail validation.
	ErrParameterValidationFailed = xerror.ParameterValidationFailed()

	// ErrExecutionTimeout is returned when query execution exceeds the timeout.
	ErrExecutionTimeout = xerror.Timeout(xerror.Query)
)
