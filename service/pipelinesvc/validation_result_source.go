//go:generate -command enumer go tool -modfile=../../tools/go.mod enumer
//go:generate enumer -type=PipelineValidationResultSource -json -text -linecomment -output=validation_result_source_enumer.go
package pipelinesvc

// PipelineValidationResultSource identifies the product boundary that recorded a validation result.
type PipelineValidationResultSource int

const (
	PipelineValidationResultSourceUnknown    PipelineValidationResultSource = iota // unknown
	PipelineValidationResultSourceAPI                                              // api
	PipelineValidationResultSourceExecute                                          // execute
	PipelineValidationResultSourceRevalidate                                       // revalidate
)
