package client

import "time"

// Config holds BigQuery connector runtime configuration.
type Config struct {
	ProjectID          string `json:"project_id"                     validate:"required,min=1"`
	Location           string `json:"location,omitempty"             validate:"omitempty,min=1"`
	DefaultDataset     string `json:"default_dataset,omitempty"      validate:"omitempty,min=1"`
	TimeoutSeconds     int    `json:"timeout_seconds,omitempty"      validate:"omitempty,min=1,max=300"`
	MaximumBytesBilled int64  `json:"maximum_bytes_billed,omitempty" validate:"omitempty,min=1"`
	BaseURL            string `json:"base_url,omitempty"             validate:"omitempty,url"`
	TokenURL           string `json:"token_url,omitempty"            validate:"omitempty,url"`
	AllowPrivateHosts  bool   `json:"allow_private_hosts,omitempty"`
}

// Credentials holds a service-account key or an access token resolved by credentialssvc.
type Credentials struct {
	ServiceAccountJSON string `json:"service_account_json,omitempty" validate:"required_without=AccessToken,excluded_with=AccessToken"`
	AccessToken        string `json:"access_token,omitempty" validate:"required_without=ServiceAccountJSON,excluded_with=ServiceAccountJSON"`
}

// TestConnectionRequest identifies the BigQuery project checked by connection tests.
type TestConnectionRequest struct {
	DatasetID string
	ProjectID string
	Location  string
}

// ListDatasetsRequest identifies the project whose datasets should be listed.
type ListDatasetsRequest struct {
	ProjectID string
	Location  string
}

// Dataset is a provider dataset reference normalized for the adapter.
type Dataset struct {
	ID string
}

// ListTablesRequest identifies the dataset whose tables should be listed.
type ListTablesRequest struct {
	ProjectID string
	DatasetID string
	Location  string
}

// Table is a provider table reference normalized for the adapter.
type Table struct {
	DatasetID string
	TableID   string
}

// GetTableRequest identifies a BigQuery table metadata request.
type GetTableRequest struct {
	ProjectID string
	DatasetID string
	TableID   string
	Location  string
}

// TableType is the provider resource kind, not a guarantee of write access.
//
//go:generate -command enumer go tool -modfile=../../../../../tools/go.mod enumer
//go:generate enumer -type=TableType -json -text -linecomment -output=table_type_enumer.go
type TableType int

const (
	TableTypeUnknown          TableType = iota // unknown
	TableTypeTable                             // TABLE
	TableTypeView                              // VIEW
	TableTypeExternal                          // EXTERNAL
	TableTypeMaterializedView                  // MATERIALIZED_VIEW
	TableTypeSnapshot                          // SNAPSHOT
)

// TableMetadata preserves the observed resource identity, kind, and schema.
type TableMetadata struct {
	ProjectID string
	DatasetID string
	TableID   string
	Type      TableType
	Fields    []Field
}

// Field describes one BigQuery result or table schema field.
type Field struct {
	Name      string
	Type      string
	Mode      string
	Precision string
	Scale     string
	Fields    []Field
}

// QueryParameter is one BigQuery named query parameter.
type QueryParameter struct {
	Name  string
	Type  string
	Value any
}

// QueryRequest describes a BigQuery jobs.query request after Braidplane binding.
type QueryRequest struct {
	ProjectID          string
	Location           string
	SQL                string
	Parameters         []QueryParameter
	Timeout            time.Duration
	MaximumBytesBilled int64
	MaxResultRows      int   // Maximum retained rows, including the adapter's overflow sentinel.
	MaxResponseBytes   int64 // Cumulative provider bytes and a separate retained-row budget.
}

// QueryResult contains provider rows up to the requested row cap.
type QueryResult struct {
	Schema []Field
	Rows   []map[string]any
}

// InsertRowsRequest describes an append-style BigQuery insertAll request.
type InsertRowsRequest struct {
	ProjectID        string
	DatasetID        string
	TableID          string
	InsertIDPrefix   string
	Rows             []map[string]any
	MaxRequestBytes  int64
	MaxResponseBytes int64
}

// InsertRowsResult reports the number of rows accepted by BigQuery.
type InsertRowsResult struct {
	InsertedRows int
}

type serviceAccount struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}
