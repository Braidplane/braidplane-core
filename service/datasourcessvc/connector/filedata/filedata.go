// Package filedata provides shared file-format detection, CSV/JSON/spreadsheet parsing,
// and schema inference helpers used by file-backed data source connectors (FTP, Git).
//
//go:generate -command enumer go tool -modfile=../../../../tools/go.mod enumer
//go:generate enumer -type=QueryFormat -output=query_format_string.go -text -json -linecomment
package filedata

import "context"

// QueryFormat determines how file content should be parsed.
type QueryFormat int

const (
	QueryFormatUnknown     QueryFormat = iota // unknown
	QueryFormatAuto                           // auto
	QueryFormatCSV                            // csv
	QueryFormatJSON                           // json
	QueryFormatFile                           // file
	QueryFormatSpreadsheet                    // spreadsheet
)

// ParseQueryFormat maps external format text to a supported query format.
func ParseQueryFormat(value string) QueryFormat {
	format, err := QueryFormatString(value)
	if err != nil {
		return QueryFormatUnknown
	}
	return format
}

// ParseCSVOptions controls how CSV content is parsed.
type ParseCSVOptions struct {
	Delimiter string
	HasHeader *bool
	MaxRows   int
	MaxBytes  int64
}

// ParseJSONOptions controls how JSON content is parsed.
type ParseJSONOptions struct {
	MaxRows  int
	MaxBytes int64
}

// SpreadsheetOptions controls spreadsheet import/export behavior.
type SpreadsheetOptions struct {
	Sheet     string
	Range     string
	HasHeader *bool
	MaxRows   int
	// MaxBytes bounds input, decompression and parsing work during import.
	MaxBytes int64
	FilePath string
}

// Schema represents discovered file schemas for a file-backed data source.
type Schema struct {
	Files       []File
	Diagnostics []SchemaDiagnostic
}

// File represents one discoverable file and its inferred fields.
type File struct {
	ResourceID string `json:"resource_id,omitempty"`
	MIMEType   string `json:"mime_type,omitempty"`
	SizeBytes  *int64 `json:"size_bytes,omitempty"`
	Name       string
	Fields     []Field
}

// Field represents one inferred field path and native type.
type Field struct {
	Name string
	Type string
}

// SchemaDiagnostic reports a non-fatal file schema sampling issue.
type SchemaDiagnostic struct {
	Name     string
	Target   string
	Severity string
	Code     string
	Message  string
}

// SchemaIntrospector retrieves file-backed data source schema metadata.
type SchemaIntrospector interface {
	IntrospectSchema(ctx context.Context) (*Schema, error)
}
