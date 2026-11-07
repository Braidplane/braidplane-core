package datasources

import (
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// SchemaMetadata represents cached introspected schema for a data source.
type SchemaMetadata struct {
	Slug       slug.Slug
	SchemaData json.RawMessage
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type UpsertSchemaMetadataParams struct {
	SchemaData json.RawMessage
}

// SchemaImportDiagnostic contains safe operator-facing import failure details.
type SchemaImportDiagnostic struct {
	Code    string
	Message string
}

// SchemaImportState represents the durable schema import lifecycle for one data source.
type SchemaImportState struct {
	DataSourceSlug slug.Slug
	Status         model.DataSourceSchemaImportStatus
	Diagnostic     *SchemaImportDiagnostic
	StartedAt      *time.Time
	FinishedAt     *time.Time
	// Started is true when this call transitioned the import into importing instead of reusing an in-flight attempt.
	Started bool
}

// MarkSchemaImportFailedParams stores the sanitized terminal failure from one import attempt.
type MarkSchemaImportFailedParams struct {
	AttemptID    string
	ErrorCode    string
	ErrorMessage string
	FinishedAt   time.Time
}
