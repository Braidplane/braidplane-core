package apierror

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var openAPIFieldErrorPattern = regexp.MustCompile(`Error at "?/([a-zA-Z0-9_]+)"?:\s*(.+)$`)

func OapiValidationFailed(message, requestID string, statusCode int) ErrorResponse {
	if statusCode <= 0 {
		statusCode = 400
	}

	apiErr := &Error{
		HTTPStatus: statusCode,
		Code:       CodeValidationFailed,
		Message:    message,
		RequestID:  requestID,
	}

	// Multi-error diagnostics retain their aggregate message rather than becoming one field error.
	if strings.HasPrefix(message, "multiple errors encountered: ") {
		return ErrorResponse{Error: apiErr}
	}

	fieldErr, ok := parseOpenAPIValidationError(message)
	if ok {
		apiErr.Message = "Request validation failed"
		apiErr.ValidationErrors = []ValidationError{fieldErr}
	}

	return ErrorResponse{Error: apiErr}
}

func parseOpenAPIValidationError(raw string) (ValidationError, bool) {
	normalized := strings.ReplaceAll(raw, `\"`, `"`)
	match := openAPIFieldErrorPattern.FindStringSubmatch(normalized)
	if len(match) != 3 {
		return ValidationError{}, false
	}

	field := match[1]
	detail := strings.TrimSpace(match[2])

	if strings.Contains(detail, "minimum string length is 1") {
		return ValidationError{
			Field:   field,
			Code:    CodeValidationRequiredField,
			Message: "is required",
		}, true
	}

	if minimum, ok := parseIntSuffix(detail, "minimum string length is "); ok {
		return ValidationError{
			Field:   field,
			Code:    CodeValueTooShort,
			Message: fmt.Sprintf("must be at least %d characters", minimum),
		}, true
	}

	if maximum, ok := parseIntSuffix(detail, "maximum string length is "); ok {
		return ValidationError{
			Field:   field,
			Code:    CodeValueTooLong,
			Message: fmt.Sprintf("must be at most %d characters", maximum),
		}, true
	}

	if strings.Contains(strings.ToLower(detail), "format") {
		return ValidationError{
			Field:   field,
			Code:    CodeValidationInvalidFormat,
			Message: "has invalid format",
		}, true
	}

	return ValidationError{Field: field, Code: CodeValidationInvalidFormat, Message: detail}, true
}

func parseIntSuffix(input, prefix string) (int, bool) {
	_, after, ok := strings.Cut(input, prefix)
	if !ok {
		return 0, false
	}

	number := strings.TrimSpace(after)
	number = strings.Split(number, " ")[0]
	parsed, err := strconv.Atoi(number)
	if err != nil {
		return 0, false
	}

	return parsed, true
}
