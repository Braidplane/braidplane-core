package queriessvc

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	repoq "github.com/Braidplane/braidplane-core/postgres/queries"
)

// Re-exported from repository layer — single source of truth.
var (
	ErrQueryNotFound   = repoq.ErrQueryNotFound
	ErrQueryNotDeleted = repoq.ErrQueryNotDeleted
	ErrSlugTaken       = repoq.ErrSlugTaken
)

// Service-only errors.
var (
	ErrOperationSnapshotRequired = xerror.New(xerror.KindInvalidInput, "OPERATION_SNAPSHOT_REQUIRED", "saved operation contracts require an immutable compiled snapshot")
	ErrInvalidInput              = xerror.ValidationFailed()
	ErrParameterValidationFailed = xerror.ParameterValidationFailed()
	ErrQueryExecutionFailed      = xerror.Unavailable(xerror.Query)
	ErrQueryTimeout              = xerror.Timeout(xerror.Query)
	ErrDataSourceUnavailable     = xerror.Unavailable(xerror.DataSource)
	ErrUnsupportedDataSourceType = xerror.Unsupported(xerror.DataSource)
)
