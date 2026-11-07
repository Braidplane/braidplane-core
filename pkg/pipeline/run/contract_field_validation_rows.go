package run

import (
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
)

// shouldCoerceContractFieldValidationRows reports whether a runtime boundary is
// allowed to normalize serialized scalar values before field validations run.
func shouldCoerceContractFieldValidationRows(source *StepResult, contract *payload.Contract) bool {
	if contract == nil {
		return false
	}

	switch contract.Representation {
	case model.ContractRepresentationRows, model.ContractRepresentationCSV:
		return true
	case model.ContractRepresentationUnknown:
	default:
		return false
	}

	runtimePayload, ok := source.Payload()
	if ok && runtimePayload != nil {
		switch runtimePayload.Representation {
		case model.ContractRepresentationRows, model.ContractRepresentationCSV:
			return true
		default:
			return false
		}
	}

	httpResult, ok := source.HTTP()
	if !ok || httpResult == nil || len(httpResult.Body) == 0 {
		return false
	}

	switch detectHTTPMediaType(httpResult.Headers) {
	case httpMediaTypeCSV, httpMediaTypeCSV2:
		return true
	default:
		return false
	}
}
