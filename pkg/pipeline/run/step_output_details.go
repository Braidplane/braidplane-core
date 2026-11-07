//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=StepOutputDetailKind -json -text -linecomment -output=step_output_detail_kind_enumer.go
package run

import filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"

// StepOutputDetailKind identifies the typed details payload attached to a run step output.
type StepOutputDetailKind int

const (
	StepOutputDetailKindUnknown      StepOutputDetailKind = iota // unknown
	StepOutputDetailKindRows                                     // rows
	StepOutputDetailKindDocuments                                // documents
	StepOutputDetailKindHTTPResponse                             // http_response
	StepOutputDetailKindFileRead                                 // file_read
	StepOutputDetailKindFileWrite                                // file_write
)

// StepOutputDetails stores result-type-specific run output metadata under a typed discriminator.
type StepOutputDetails struct {
	Kind          StepOutputDetailKind             `json:"kind"`
	RowCount      *int                             `json:"row_count,omitempty"`
	ColumnNames   []string                         `json:"column_names,omitempty"`
	DocumentCount *int                             `json:"document_count,omitempty"`
	StatusCode    *int                             `json:"status_code,omitempty"`
	IsHeadlessCSV bool                             `json:"is_headless_csv,omitempty"`
	File          *filepayload.FilePayloadMetadata `json:"file,omitempty"`
	TargetPath    *string                          `json:"target_path,omitempty"`
	WrittenRows   *int                             `json:"written_rows,omitempty"`
	WrittenBytes  *int                             `json:"written_bytes,omitempty"`
}
