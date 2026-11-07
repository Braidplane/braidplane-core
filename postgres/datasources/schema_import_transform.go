package datasources

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// schemaImportRow groups same-typed schema import columns before converting them to domain state.
type schemaImportRow struct {
	DataSourceSlug slug.Slug
	Status         string
	ErrorCode      pgtype.Text
	ErrorMessage   pgtype.Text
	StartedAt      pgtype.Timestamptz
	FinishedAt     pgtype.Timestamptz
	Started        bool
}

// toSchemaImportState validates database import status and maps nullable diagnostic/timestamp columns.
func toSchemaImportState(row schemaImportRow) (*SchemaImportState, error) {
	status, ok := schemaImportStatusFromDB(row.Status)
	if !ok {
		return nil, xerror.Unexpected().Detailf(
			"invalid data source schema import status in database: %s",
			row.Status,
		)
	}

	return &SchemaImportState{
		DataSourceSlug: row.DataSourceSlug,
		Status:         status,
		Diagnostic:     schemaImportDiagnosticFromDB(row.ErrorCode, row.ErrorMessage),
		StartedAt:      timePtrFromPgTimestamptz(row.StartedAt),
		FinishedAt:     timePtrFromPgTimestamptz(row.FinishedAt),
		Started:        row.Started,
	}, nil
}

// schemaImportStatusFromDB parses the stored import status and rejects unknown enum values.
func schemaImportStatusFromDB(statusText string) (model.DataSourceSchemaImportStatus, bool) {
	status, err := model.DataSourceSchemaImportStatusString(statusText)
	if err != nil || status == model.DataSourceSchemaImportStatusUnknown {
		return model.DataSourceSchemaImportStatusUnknown, false
	}
	return status, true
}

// schemaImportDiagnosticFromDB returns the safe stored failure diagnostic when either column is present.
func schemaImportDiagnosticFromDB(
	errorCode pgtype.Text,
	errorMessage pgtype.Text,
) *SchemaImportDiagnostic {
	if !errorCode.Valid && !errorMessage.Valid {
		return nil
	}
	return &SchemaImportDiagnostic{
		Code:    errorCode.String,
		Message: errorMessage.String,
	}
}

// timePtrFromPgTimestamptz converts nullable SQL timestamps into optional domain timestamps.
func timePtrFromPgTimestamptz(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}
