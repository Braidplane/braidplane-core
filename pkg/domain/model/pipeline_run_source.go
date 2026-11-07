//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=PipelineRunSourceType,PipelineRunSourceEntrypoint -json -text -linecomment -output=pipeline_run_source_enumer.go
package model

// PipelineRunSourceType categorizes launch-supported reasons a pipeline run started.
type PipelineRunSourceType int

const (
	PipelineRunSourceTypeUnknown    PipelineRunSourceType = iota // unknown
	PipelineRunSourceTypeManual                                  // manual
	PipelineRunSourceTypeAPI                                     // api
	PipelineRunSourceTypeSchedule                                // schedule
	PipelineRunSourceTypeWebhook                                 // webhook
	PipelineRunSourceTypeCompletion                              // completion
)

// PipelineRunSourceEntrypoint identifies the product or API boundary that requested a run.
type PipelineRunSourceEntrypoint int

const (
	PipelineRunSourceEntrypointUnknown   PipelineRunSourceEntrypoint = iota // unknown
	PipelineRunSourceEntrypointUI                                           // ui
	PipelineRunSourceEntrypointAPI                                          // api
	PipelineRunSourceEntrypointScheduler                                    // scheduler
	PipelineRunSourceEntrypointWebhook                                      // webhook
)
