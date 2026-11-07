package apierror

import (
	"fmt"
	"net/http"
)

// New creates a new API error
func New(httpStatus int, code, message string) *Error {
	return &Error{
		HTTPStatus: httpStatus,
		Code:       code,
		Message:    message,
	}
}

// Authentication Errors

func Unauthorized() *Error {
	return New(http.StatusUnauthorized, CodeAuthUnauthorized, "Authentication required")
}

func Forbidden(resource, action string) *Error {
	message := "You don't have permission to perform this action"
	if resource != "" && action != "" {
		message = fmt.Sprintf("You don't have permission to %s %s", action, resource)
	}
	return New(
		http.StatusForbidden,
		CodeAuthForbidden,
		message,
	)
}

func UserPrincipalRequired() *Error {
	return New(
		http.StatusForbidden,
		CodeAuthUserPrincipalRequired,
		"This endpoint requires a supported user principal; organization API keys and other unsupported token types cannot be used",
	)
}

func CSRFInvalid() *Error {
	return New(http.StatusForbidden, CodeCSRFInvalid, "Invalid or missing CSRF token")
}

func RateLimited() *Error {
	return New(http.StatusTooManyRequests, CodeRateLimited, "Too many attempts. Try again later.")
}

// Validation Errors

func ValidationFailed(message string) *Error {
	return New(http.StatusBadRequest, CodeValidationFailed, message)
}

func InvalidRequestBody() *Error {
	return New(http.StatusBadRequest, CodeValidationFailed, "Invalid request body format")
}

// System Errors

func InternalServerError() *Error {
	return New(
		http.StatusInternalServerError,
		CodeInternalServerError,
		"An unexpected error occurred",
	)
}

func NotImplemented() *Error {
	return New(
		http.StatusNotImplemented,
		CodeNotImplemented,
		"This endpoint is not yet implemented",
	)
}
