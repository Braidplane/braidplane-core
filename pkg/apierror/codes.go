package apierror

const (
	// Authentication & Authorization
	CodeAuthUnauthorized          = "AUTH_UNAUTHORIZED"
	CodeAuthForbidden             = "AUTH_FORBIDDEN"
	CodeAuthUserPrincipalRequired = "AUTH_USER_PRINCIPAL_REQUIRED"
	CodeCSRFInvalid               = "CSRF_INVALID"
	CodeRateLimited               = "RATE_LIMITED"

	// Validation
	CodeValidationFailed        = "VALIDATION_FAILED"
	CodeValidationRequiredField = "VALIDATION_REQUIRED_FIELD"
	CodeValidationInvalidFormat = "VALIDATION_INVALID_FORMAT"

	// Validation - Specific field types
	CodeEmailInvalid       = "EMAIL_INVALID"
	CodeValueTooShort      = "VALUE_TOO_SHORT"
	CodeValueTooLong       = "VALUE_TOO_LONG"
	CodeValueInvalidLength = "VALUE_INVALID_LENGTH"
	CodeValueNotInList     = "VALUE_NOT_IN_LIST"
	CodeUUIDInvalid        = "UUID_INVALID"
	CodeURLInvalid         = "URL_INVALID"
	CodeDatetimeInvalid    = "DATETIME_INVALID"

	// System
	CodeInternalServerError = "INTERNAL_SERVER_ERROR"
	CodeNotImplemented      = "NOT_IMPLEMENTED"
	CodeMethodNotAllowed    = "METHOD_NOT_ALLOWED"
)
