//go:generate go tool -modfile=../../../tools/go.mod enumer -type=RunRecencyOutcome -transform=snake -output=run_recency_outcome_string.go -text -json -linecomment
package model

type RunRecencyOutcome int

const (
	RunRecencyOutcomeUnknown     RunRecencyOutcome = iota // unknown
	RunRecencyOutcomeGrace                                // grace
	RunRecencyOutcomeHealthy                              // healthy
	RunRecencyOutcomeBreached                             // breached
	RunRecencyOutcomeUnavailable                          // unavailable
)
