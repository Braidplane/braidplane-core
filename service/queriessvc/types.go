package queriessvc

import (
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// ParameterDataType is the data type of a query parameter.
type ParameterDataType = model.ParameterDataType

const (
	ParameterDataTypeUnknown = model.ParameterDataTypeUnknown
	ParameterDataTypeText    = model.ParameterDataTypeText
	ParameterDataTypeInteger = model.ParameterDataTypeInteger
	ParameterDataTypeBoolean = model.ParameterDataTypeBoolean
	ParameterDataTypeDate    = model.ParameterDataTypeDate
	ParameterDataTypeUUID    = model.ParameterDataTypeUUID
)

// ParameterDataTypeString maps stored or transport text to a query parameter data type.
func ParameterDataTypeString(value string) ParameterDataType {
	dataType, err := model.ParameterDataTypeString(value)
	if err != nil {
		return model.ParameterDataTypeUnknown
	}
	return dataType
}

// Query is a saved query definition.
type Query struct {
	Slug           slug.Slug
	Name           string
	Description    *string
	DataSourceSlug slug.Slug
	// Data contains query-type-specific configuration (e.g. SQL statement, row limit, timeout).
	Data                     json.RawMessage
	ExpectedInputContract    *payload.Contract
	ExpectedInputContractRef *pipelinespec.ODCSContractRef
	OutputContract           *payload.Contract
	OutputContractRef        *pipelinespec.ODCSContractRef
	Parameters               []*QueryParameter
	CreatedAt                time.Time
	UpdatedAt                time.Time
	DeletedAt                *time.Time
}

// QueryExecution is a persisted query execution result.
type QueryExecution struct {
	Slug       slug.Slug
	Status     string
	Result     json.RawMessage
	ExecutedAt time.Time
}

// QueryExecutionIdentity identifies a recorded successful saved-query execution.
type QueryExecutionIdentity struct {
	ExecutionSlug  slug.Slug
	DataSourceSlug slug.Slug
	QuerySlug      slug.Slug
	ExecutedAt     time.Time
}

// QueryParameter defines a named parameter accepted by a query.
type QueryParameter struct {
	Slug         slug.Slug         `validate:"omitempty,slug"`
	Name         string            `validate:"required,min=1,max=255"`
	DataType     ParameterDataType `validate:"queryparamdatatype"`
	IsRequired   bool
	DefaultValue *string `validate:"omitempty,max=4000"`
	Description  *string `validate:"omitempty,max=2000"`
}

// CreateParams are parameters for creating a new query.
type CreateParams struct {
	Slug                     slug.Slug // empty → generated from Name
	Name                     string    `validate:"required,min=1,max=255"`
	Description              *string
	Data                     json.RawMessage `validate:"required"`
	ExpectedInputContract    *payload.Contract
	ExpectedInputContractRef *pipelinespec.ODCSContractRef
	OutputContract           *payload.Contract
	OutputContractRef        *pipelinespec.ODCSContractRef
	Parameters               []*QueryParameter `validate:"omitempty,dive,required"`
}

// UpdateParams are parameters for updating a query (full replacement).
// RowLimit is embedded in Data rather than being a separate field.
type UpdateParams struct {
	Name                     string `validate:"required,min=1,max=255"`
	Description              *string
	Data                     json.RawMessage `validate:"required"`
	ExpectedInputContract    *payload.Contract
	ExpectedInputContractRef *pipelinespec.ODCSContractRef
	OutputContract           *payload.Contract
	OutputContractRef        *pipelinespec.ODCSContractRef
	Parameters               []*QueryParameter `validate:"omitempty,dive,required"`
}
