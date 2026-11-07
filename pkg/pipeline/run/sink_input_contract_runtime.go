package run

import (
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// ErrSinkInputContractViolation is returned when the upstream step result does not
// satisfy the declared sink input contract.
var ErrSinkInputContractViolation = xerror.SinkInputContractViolation()

// validateSinkInputContract checks that all required fields declared in the sink input
// contract are present in the source step result.
// Validation uses a two-pass strategy: flat column name lookup first, then nested
// field-path resolution against actual row data (supports paths like "profile.email").
func validateSinkInputContract(source *StepResult, contract *payload.Contract) error {
	if source == nil || contract == nil {
		return nil
	}

	if detail, mismatch := runtimeRepresentationMismatch(source, contract); mismatch {
		return xerror.Wrapf("%s: %w", detail, ErrSinkInputContractViolation)
	}

	columnNames, rows, err := ContractData(source)
	if err != nil {
		if !contractHasRequiredFields(contract) && !payload.ContractHasFieldValidations(contract) {
			return nil
		}
		return xerror.Wrapf(
			"failed to inspect source step result for sink input contract: %w",
			xerror.Join(ErrSinkInputContractViolation, err),
		)
	}
	if len(columnNames) == 0 && len(rows) == 0 {
		// No structured data available; skip validation.
		return nil
	}

	if fieldName, missing := payload.MissingRequiredField(columnNames, rows, contract); missing {
		return xerror.Wrapf(
			"required field %q is missing from source step result: %w",
			fieldName,
			xerror.Join(ErrSinkInputContractViolation, missingRequiredFieldFailure(contract, fieldName)),
		)
	}
	if contract.Representation == model.ContractRepresentationMultipartForm {
		return nil
	}
	rows, err = payload.CoerceDecimalRows(contract, rows)
	if err != nil {
		return xerror.Wrapf("decimal contract validation: %w", xerror.Join(ErrSinkInputContractViolation, err))
	}
	if shouldCoerceContractFieldValidationRows(source, contract) {
		rows = payload.CoerceContractFieldValidationRows(contract, rows)
	}
	if failure := payload.ValidateContractFieldValues(contract, rows); failure != nil {
		return xerror.Wrapf(
			"%s: %w",
			failure.Summary(),
			xerror.Join(ErrSinkInputContractViolation, failure),
		)
	}

	return nil
}

// contractHasRequiredFields reports whether a sink contract contains any field
// whose absence should fail runtime validation.
func contractHasRequiredFields(contract *payload.Contract) bool {
	if contract == nil {
		return false
	}
	for _, field := range contract.Fields {
		if field.Required {
			return true
		}
	}
	return false
}
