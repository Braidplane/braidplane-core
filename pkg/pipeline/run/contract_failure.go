package run

import (
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
)

// ContractFailureError carries only safe declaration diagnostics through wrapped errors.
type ContractFailureError struct{ Diagnostic ContractFailureDiagnostic }

func (e *ContractFailureError) Error() string { return e.Diagnostic.ReasonCode.String() }

func missingRequiredFieldFailure(contract *payload.Contract, field string) error {
	return &ContractFailureError{Diagnostic: ContractFailureDiagnostic{
		ContractName: contract.Name,
		FieldName:    field, Rule: "required", ViolationCount: 1, ReasonCode: pipelinevalidate.ContractReasonMissingRequiredField,
	}}
}

// HasContractFailure excludes unavailable evaluators and unrelated execution errors.
func HasContractFailure(timeline RunTimeline) bool {
	for _, check := range timeline.StructuralChecks {
		if check.Status == CheckStatusFailed {
			return true
		}
	}
	if timeline.Failure == nil {
		return false
	}
	if timeline.Failure.Contract != nil {
		return true
	}
	switch timeline.Failure.ReasonCode {
	case FailureReasonCodeSourceResultShapeMismatch, FailureReasonCodeTransformContractMismatch, FailureReasonCodeSinkContractMismatch:
		return true
	default:
		return false
	}
}
