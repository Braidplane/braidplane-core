package bigquery

import (
	"context"
	"encoding/json"

	bigqueryclient "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery/client"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
)

const (
	operationQuery      = "query"
	operationInsertRows = "insert_rows"
)

// Config holds BigQuery connector configuration used by the Braidplane adapter.
type Config = bigqueryclient.Config

// Credentials holds write-only BigQuery service-account credentials.
type Credentials = bigqueryclient.Credentials

// TestConnectionRequest identifies a provider connection-test request.
type TestConnectionRequest = bigqueryclient.TestConnectionRequest

// ListDatasetsRequest identifies a provider dataset-list request.
type ListDatasetsRequest = bigqueryclient.ListDatasetsRequest

// Dataset is a normalized BigQuery dataset reference.
type Dataset = bigqueryclient.Dataset

// ListTablesRequest identifies a provider table-list request.
type ListTablesRequest = bigqueryclient.ListTablesRequest

// Table is a normalized BigQuery table reference.
type Table = bigqueryclient.Table

// GetTableRequest identifies a provider table metadata request.
type GetTableRequest = bigqueryclient.GetTableRequest

// TableMetadata contains BigQuery table schema metadata.
type TableMetadata = bigqueryclient.TableMetadata

// Field contains BigQuery field metadata or query result field metadata.
type Field = bigqueryclient.Field

// Column aliases the shared SQL connector column shape for BigQuery introspection tests.
type Column = sqlconnector.Column

// QueryRequest describes a bound BigQuery query request.
type QueryRequest = bigqueryclient.QueryRequest

// QueryResult contains normalized provider rows before Braidplane value coercion.
type QueryResult = bigqueryclient.QueryResult

// QueryParameter is one BigQuery named query parameter.
type QueryParameter = bigqueryclient.QueryParameter

// InsertRowsRequest describes a bound BigQuery insertRows request.
type InsertRowsRequest = bigqueryclient.InsertRowsRequest

// InsertRowsResult reports provider insertRows results.
type InsertRowsResult = bigqueryclient.InsertRowsResult

// SchemaIntrospector retrieves BigQuery schema metadata.
type SchemaIntrospector interface {
	IntrospectSchema(ctx context.Context) (*sqlconnector.Schema, error)
}

type providerClient interface {
	TestConnection(context.Context, TestConnectionRequest) error
	ListDatasets(context.Context, ListDatasetsRequest) ([]Dataset, error)
	ListTables(context.Context, ListTablesRequest) ([]Table, error)
	GetTable(context.Context, GetTableRequest) (*TableMetadata, error)
	Query(context.Context, QueryRequest) (*QueryResult, error)
	QueryPages(context.Context, QueryRequest, func(*QueryResult) error) error
	InsertRows(context.Context, InsertRowsRequest) (*InsertRowsResult, error)
	Close() error
}

type statement struct {
	Type               string          `json:"type,omitempty"`
	Operation          string          `json:"operation,omitempty"`
	SQL                string          `json:"sql,omitempty"`
	TimeoutSeconds     *int            `json:"timeout_seconds,omitempty"`
	MaximumBytesBilled *int64          `json:"maximum_bytes_billed,omitempty"`
	Dataset            string          `json:"dataset,omitempty"`
	Table              string          `json:"table,omitempty"`
	Rows               json.RawMessage `json:"rows,omitempty"`
	InsertIDPrefix     string          `json:"insert_id_prefix,omitempty"`
}
