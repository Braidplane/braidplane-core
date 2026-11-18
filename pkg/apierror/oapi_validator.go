package apierror

import (
	"github.com/getkin/kin-openapi/openapi3filter"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// OapiValidationFailed exposes only validation metadata declared in the schema.
func OapiValidationFailed(err error, requestID string) ErrorResponse {
	apiErr := ValidationFailed("Request validation failed").withRequestID(requestID)

	var requestErr *openapi3filter.RequestError
	if xerror.As(err, &requestErr) && requestErr != nil && requestErr.Parameter != nil {
		apiErr = apiErr.withValidationErrors([]ValidationError{{
			Field: requestErr.Parameter.Name, Code: CodeValidationInvalidFormat, Message: "Invalid parameter",
		}})
	}
	return ErrorResponse{Error: apiErr}
}
