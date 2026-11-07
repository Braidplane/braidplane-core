package queries

import (
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// OperationSnapshot is an atomic statement, parameter, and input-requirement read.
type OperationSnapshot struct {
	DataSourceType           model.DataSourceType
	Data                     json.RawMessage
	ExpectedInputContract    json.RawMessage
	ExpectedInputContractRef json.RawMessage
	OutputContract           json.RawMessage
	OutputContractRef        json.RawMessage
	Parameters               json.RawMessage
	UpdatedAt                time.Time
}

// Query represents a saved query definition.
type Query struct {
	Slug                     slug.Slug
	Name                     string
	Description              *string
	Data                     json.RawMessage
	ExpectedInputContract    json.RawMessage
	ExpectedInputContractRef json.RawMessage
	OutputContract           json.RawMessage
	OutputContractRef        json.RawMessage
	DataSourceSlug           slug.Slug
	CreatedAt                time.Time
	UpdatedAt                time.Time
	DeletedAt                *time.Time
}

// QueryParameter represents a parameter definition for a query.
type QueryParameter struct {
	Slug         slug.Slug
	Name         string
	DataType     string
	IsRequired   bool
	DefaultValue *string
	Description  *string
	CreatedAt    time.Time
}

// QueryExecution represents a query execution record.
type QueryExecution struct {
	Slug         slug.Slug
	Status       string
	Result       json.RawMessage
	ErrorMessage *string
	DurationMs   *int32
	ExecutedAt   time.Time
}

// QueryExecutionIdentity identifies a recorded successful query execution.
type QueryExecutionIdentity struct {
	ExecutionSlug  slug.Slug
	DataSourceSlug slug.Slug
	QuerySlug      slug.Slug
	ExecutedAt     time.Time
}

// CreateParams contains parameters for creating a query.
type CreateParams struct {
	Slug                     slug.Slug
	Name                     string
	Description              *string
	DataSourceSlug           slug.Slug
	Data                     json.RawMessage
	ExpectedInputContract    json.RawMessage
	ExpectedInputContractRef json.RawMessage
	OutputContract           json.RawMessage
	OutputContractRef        json.RawMessage
	Parameters               []*QueryParameterParams
}

// UpdateParams contains parameters for updating a query.
type UpdateParams struct {
	Name                     string
	Description              *string
	Data                     json.RawMessage
	ExpectedInputContract    json.RawMessage
	ExpectedInputContractRef json.RawMessage
	OutputContract           json.RawMessage
	OutputContractRef        json.RawMessage
	Parameters               []*QueryParameterParams
}

// QueryParameterParams contains parameters for a query parameter.
type QueryParameterParams struct {
	Slug         slug.Slug
	Name         string
	DataType     string
	IsRequired   bool
	DefaultValue *string
	Description  *string
}

// CreateExecutionParams contains parameters for creating a query execution record.
type CreateExecutionParams struct {
	Slug   slug.Slug
	Status string
	Result json.RawMessage
}
