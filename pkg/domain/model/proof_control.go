//go:generate go tool -modfile=../../../tools/go.mod enumer -type=ProofControlOutcome -json -text -linecomment -output=proof_control_enumer.go
package model

type ProofControlOutcome int

const (
	ProofControlOutcomeUnknown          ProofControlOutcome = iota // unknown
	ProofControlOutcomeAccepted                                    // accepted
	ProofControlOutcomeDenied                                      // denied
	ProofControlOutcomeLimited                                     // limited
	ProofControlOutcomeAuditUnavailable                            // audit_unavailable
	ProofControlOutcomeError                                       // error
)
