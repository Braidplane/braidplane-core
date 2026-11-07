package validate

import (
	"github.com/go-playground/validator/v10"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// ErrorDetail maps a validator field/tag pair to a caller-owned error detail.
// An empty Tag matches any validation tag for the field.
type ErrorDetail struct {
	Field  string
	Tag    string
	Detail string
}

// DetailForFirstError extracts the first validator failure and returns the matching
// caller-owned detail. It returns fallback for non-validator errors and unmapped failures.
func DetailForFirstError(err error, fallback string, details ...ErrorDetail) string {
	var validationErrs validator.ValidationErrors
	if !xerror.As(err, &validationErrs) || len(validationErrs) == 0 {
		return fallback
	}

	fieldErr := validationErrs[0]
	for _, detail := range details {
		if detail.Field != fieldErr.Field() {
			continue
		}
		if detail.Tag != "" && detail.Tag != fieldErr.Tag() {
			continue
		}
		return detail.Detail
	}

	return fallback
}
