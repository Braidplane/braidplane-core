package run

import (
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// validateQueryOutputContractAtRuntime checks the actual execution result column names
// against the declared output contract at runtime.
// This supplements the pre-flight schema check by verifying the real result shape,
// not just what the data source schema advertises.
// Returns ErrQueryOutputContractSchemaMismatch (wrapped) on the first detected violation.
func validateQueryOutputContractAtRuntime(
	execResult *result.ExecutionResult,
	contract *payload.Contract,
) error {
	if execResult == nil || contract == nil {
		return nil
	}

	source := newStepResult(execResult)
	if detail, mismatch := runtimeRepresentationMismatch(source, contract); mismatch {
		return xerror.Wrapf("%s: %w", detail, ErrQueryOutputContractSchemaMismatch)
	}

	columnNames, rows, err := ContractData(source)
	if err != nil {
		return xerror.Wrapf(
			"failed to inspect query execution result for output contract validation: %w",
			xerror.Join(ErrQueryOutputContractSchemaMismatch, err),
		)
	}

	if len(columnNames) == 0 && len(rows) == 0 {
		// No structured columns or rows available; skip validation.
		return nil
	}

	if fieldName, missing := payload.MissingRequiredField(columnNames, rows, contract); missing {
		return xerror.Wrapf(
			"required field %q is missing from query result: %w",
			fieldName,
			xerror.Join(ErrQueryOutputContractSchemaMismatch, missingRequiredFieldFailure(contract, fieldName)),
		)
	}
	if contract.Representation == model.ContractRepresentationMultipartForm {
		return nil
	}
	rows, err = payload.CoerceDecimalRows(contract, rows)
	if err != nil {
		return xerror.Wrapf("decimal contract validation: %w", xerror.Join(ErrQueryOutputContractSchemaMismatch, err))
	}
	if shouldCoerceContractFieldValidationRows(source, contract) {
		rows = payload.CoerceContractFieldValidationRows(contract, rows)
	}
	if failure := payload.ValidateContractFieldValues(contract, rows); failure != nil {
		return xerror.Wrapf(
			"%s: %w",
			failure.Summary(),
			xerror.Join(ErrQueryOutputContractSchemaMismatch, failure),
		)
	}

	return nil
}
