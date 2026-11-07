package queries

import (
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

// toQuery converts CreateQueryRow to domain Query.
func toQuery(row dbgen.Query, dsSlug slug.Slug) *Query {
	return &Query{
		DataSourceSlug:           dsSlug,
		Slug:                     row.Slug,
		Name:                     row.Name,
		Description:              postgres.FromNullableText(row.Description),
		Data:                     row.Data,
		ExpectedInputContract:    row.ExpectedInputContract,
		ExpectedInputContractRef: row.ExpectedInputContractRef,
		OutputContract:           row.OutputContract,
		OutputContractRef:        row.OutputContractRef,
		CreatedAt:                row.CreatedAt.Time,
		UpdatedAt:                row.UpdatedAt.Time,
		DeletedAt:                postgres.FromNullableTimestamptz(row.DeletedAt),
	}
}

// toQueryParameter converts QueryParameter to domain QueryParameter.
func toQueryParameter(row dbgen.QueryParameter) *QueryParameter {
	return &QueryParameter{
		Slug:         row.Slug,
		Name:         row.Name,
		DataType:     row.DataType,
		IsRequired:   row.IsRequired,
		DefaultValue: postgres.FromNullableText(row.DefaultValue),
		Description:  postgres.FromNullableText(row.Description),
		CreatedAt:    row.CreatedAt.Time,
	}
}

// toQueryExecution converts QueryExecution to domain QueryExecution.
func toQueryExecution(row dbgen.QueryExecution) *QueryExecution {
	return &QueryExecution{
		Status:     row.Status,
		Slug:       row.Slug,
		Result:     row.Result,
		ExecutedAt: row.ExecutedAt.Time,
	}
}
