//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=DataSourceSchemaImportStatus -transform=snake -output=data_source_schema_import_status_string.go -text -json -linecomment
package model

type DataSourceSchemaImportStatus int

const (
	DataSourceSchemaImportStatusUnknown     DataSourceSchemaImportStatus = iota // unknown
	DataSourceSchemaImportStatusNotImported                                     // not_imported
	DataSourceSchemaImportStatusImporting                                       // importing
	DataSourceSchemaImportStatusFailed                                          // failed
	DataSourceSchemaImportStatusReady                                           // ready
)
