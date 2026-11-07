package sql

import (
	"context"
)

// Capabilities describes what a connector can do.
type Capabilities struct {
	// SupportsSchemaIntrospection indicates whether the connector can introspect database schema.
	SupportsSchemaIntrospection bool
	// SupportsPagination indicates whether the connector supports paginated queries.
	SupportsPagination bool
	// SupportsTransactions indicates whether the connector supports transactions.
	SupportsTransactions bool
}

// SQLSchemaIntrospector retrieves SQL database schema metadata.
type SQLSchemaIntrospector interface {
	// IntrospectSchema retrieves the SQL schema (tables, columns, types).
	IntrospectSchema(ctx context.Context) (*Schema, error)
}

// SQLConnector defines the SQL-specific connector contract.
type SQLConnector interface {
	GetCapabilities() Capabilities
	SQLSchemaIntrospector
}

// Result represents the output of a query execution.
type Result struct {
	// ColumnNames is the list of column names in the result.
	// Empty for non-query statements (INSERT, UPDATE, DELETE, DDL).
	ColumnNames []string

	// Rows is the list of data rows, where each row is a map of column name -> value.
	// Empty for non-query statements (INSERT, UPDATE, DELETE, DDL).
	Rows []map[string]any

	// RowCount represents either:
	// - For SELECT/query statements: number of rows returned
	// - For INSERT/UPDATE/DELETE: number of rows affected
	// - For DDL statements: typically 0
	RowCount int

	// LastInsertID is the auto-generated ID from an INSERT statement (if applicable).
	// Only populated for INSERT statements on tables with auto-increment columns.
	// Nil for all other statement types (SELECT, UPDATE, DELETE, DDL).
	LastInsertID *int64
}

// Schema represents the SQL schema of a data source.
type Schema struct {
	// Tables is a list of tables in the data source.
	Tables []Table
}

// Table represents a single SQL table in the data source schema.
type Table struct {
	// Name is the name of the table.
	Name string

	// Columns is a list of columns in the table.
	Columns []Column
}

// Column represents a single column in a table.
type Column struct {
	// Name is the name of the column.
	Name string

	// Type is the SQL type of the column (e.g., "INTEGER", "VARCHAR", "TIMESTAMP").
	Type string

	// Nullable indicates whether the column can contain NULL values.
	Nullable bool
}
