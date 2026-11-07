package run

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=CheckStatus -json -text -linecomment -output=check_status_enumer.go

type CheckStatus int

const (
	CheckStatusUnavailable CheckStatus = iota // unavailable
	CheckStatusPassed                         // passed
	CheckStatusFailed                         // failed
	CheckStatusSkipped                        // skipped
	CheckStatusError                          // error
)

// StructuralCheck records one executed obligation, never delivery or freshness.
// Only fixed codes, declaration identities and bounded aggregates belong here.
type StructuralCheck struct {
	ContractFailure            *ContractFailureDiagnostic `json:"contract_failure,omitempty"`
	ID                         string                     `json:"id"`
	Step                       slug.Slug                  `json:"step"`
	Input                      string                     `json:"input,omitempty"`
	Origin                     string                     `json:"origin"`
	Phase                      string                     `json:"phase"`
	Declaration                string                     `json:"declaration"`
	ProjectionDigest           string                     `json:"projection_digest"`
	CompiledPlanDigest         string                     `json:"compiled_plan_digest,omitempty"`
	DeploymentResolutionDigest string                     `json:"deployment_resolution_digest,omitempty"`
	PipelineVersionID          int64                      `json:"-"`
	AttemptNumber              int                        `json:"attempt_number"`
	Evaluator                  string                     `json:"evaluator"`
	Policy                     string                     `json:"policy"`
	Target                     slug.Slug                  `json:"target,omitempty"`
	Status                     CheckStatus                `json:"status"`
	ReasonCode                 string                     `json:"reason_code"`
	StartedAt                  *time.Time                 `json:"started_at,omitempty"`
	CompletedAt                *time.Time                 `json:"completed_at,omitempty"`
	Evidence                   *CheckEvidence             `json:"evidence,omitempty"`
	EvidenceDigest             string                     `json:"evidence_digest,omitempty"`
	ReusedFromRun              string                     `json:"reused_from_run,omitempty"`
	Window                     CheckWindow                `json:"window"`
}

type CheckEvidence struct {
	Rows       int   `json:"rows"`
	DurationMs int64 `json:"duration_ms"`
	Empty      bool  `json:"empty"`
	// Completeness is unknown unless the connector explicitly reports truncation.
	BatchScope string `json:"batch_scope"`
}

type CheckWindow = model.StructuralCheckWindow

// FinalizeStructuralChecks preserves completed checks after downstream failure.
func FinalizeStructuralChecks(checks []StructuralCheck, at time.Time) {
	for i := range checks {
		check := &checks[i]
		if check.Status != CheckStatusUnavailable {
			continue
		}
		switch check.ReasonCode {
		case "not_reached":
			check.Status, check.ReasonCode = CheckStatusSkipped, "downstream_not_reached"
		case "evaluating":
			check.Status, check.ReasonCode = CheckStatusError, "evaluation_interrupted"
		default:
			continue
		}
		check.CompletedAt = &at
	}
}
