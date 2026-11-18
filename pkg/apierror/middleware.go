package apierror

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v3"

	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// RecoverPanic discards the panic value and returns a fixed server error.
func RecoverPanic(_ fiber.Ctx, _ any) error {
	return InternalServerError()
}

// Middleware is a Fiber middleware that converts errors to structured API responses
func Middleware() fiber.ErrorHandler {
	return func(c fiber.Ctx, err error) error {
		requestID := reqctx.FiberRequestID(c)

		var apiErr *Error

		// Check if it's already an API error
		if xerror.As(err, &apiErr) {
			apiErr = apiErr.withRequestID(requestID)
			return c.Status(apiErr.HTTPStatus).JSON(ErrorResponse{Error: apiErr})
		}

		// Check if it's a validator error
		var validationErrs validator.ValidationErrors
		if xerror.As(err, &validationErrs) {
			apiErr = ValidationFailed("Request validation failed").
				withValidationErrors(convertValidatorErrors(validationErrs)).
				withRequestID(requestID)
			return c.Status(apiErr.HTTPStatus).JSON(ErrorResponse{Error: apiErr})
		}

		// Check if it's a JSON unmarshaling error (from Fiber body binding)
		var jsonErr *json.UnmarshalTypeError
		if xerror.As(err, &jsonErr) {
			apiErr = ValidationFailed("Invalid value type in request body").
				withMessage("Invalid value type in request body").
				withValidationErrors([]ValidationError{
					{
						Field:   jsonErr.Field,
						Code:    CodeValidationInvalidFormat,
						Message: "Unexpected JSON value type",
					},
				}).
				withRequestID(requestID)
			return c.Status(apiErr.HTTPStatus).JSON(ErrorResponse{Error: apiErr})
		}

		// Check if it's a JSON syntax error
		var jsonSyntaxErr *json.SyntaxError
		if xerror.As(err, &jsonSyntaxErr) {
			apiErr = ValidationFailed("Malformed JSON in request body").
				withMessage("Malformed JSON in request body").
				withRequestID(requestID)
			return c.Status(apiErr.HTTPStatus).JSON(ErrorResponse{Error: apiErr})
		}

		// Check if it's a Fiber error
		var fiberErr *fiber.Error
		if xerror.As(err, &fiberErr) {
			// Most Fiber errors from generated code are parameter parsing errors
			switch fiberErr.Code {
			case fiber.StatusBadRequest:
				apiErr = ValidationFailed("Request validation failed")
			case fiber.StatusUnauthorized:
				apiErr = Unauthorized()
			case fiber.StatusForbidden:
				apiErr = New(fiber.StatusForbidden, CodeAuthForbidden, http.StatusText(fiberErr.Code))
			case fiber.StatusNotFound:
				apiErr = New(fiber.StatusNotFound, CodeInternalServerError, "Resource not found")
			case fiber.StatusMethodNotAllowed:
				apiErr = New(fiber.StatusMethodNotAllowed, CodeMethodNotAllowed, http.StatusText(fiberErr.Code))
			default:
				apiErr = New(fiberErr.Code, CodeInternalServerError, http.StatusText(fiberErr.Code))
			}

			apiErr = apiErr.withRequestID(requestID)

			return c.Status(apiErr.HTTPStatus).JSON(ErrorResponse{Error: apiErr})
		}

		if isGeneratedRequestValidationError(err) {
			apiErr = ValidationFailed("Request validation failed").
				withRequestID(requestID)
			return c.Status(apiErr.HTTPStatus).JSON(ErrorResponse{Error: apiErr})
		}

		// HTTP request completion records the classification.
		apiErr = InternalServerError().
			withRequestID(requestID)
		return c.Status(apiErr.HTTPStatus).JSON(ErrorResponse{Error: apiErr})
	}
}

func isGeneratedRequestValidationError(err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "argument") &&
		strings.Contains(message, "required") &&
		strings.Contains(message, "not found")
}

// convertValidatorErrors converts go-playground validator errors to our ValidationError format
func convertValidatorErrors(errs validator.ValidationErrors) []ValidationError {
	validationErrors := make([]ValidationError, 0, len(errs))

	for _, err := range errs {
		validationErr := ValidationError{
			Field:   fieldName(err),
			Code:    validationErrorCode(err),
			Message: validationErrorMessage(err),
		}

		validationErrors = append(validationErrors, validationErr)
	}

	return validationErrors
}

// fieldName converts the validator field name to a JSON-friendly format
func fieldName(err validator.FieldError) string {
	// Convert from struct field name (e.g., "Email") to JSON field name (e.g., "email")
	field := err.Field()
	if len(field) > 0 {
		// Simple lowercase conversion - adjust if you use different JSON tags
		return strings.ToLower(field[:1]) + field[1:]
	}
	return field
}

// validationErrorCode generates an error code from the validation tag
func validationErrorCode(err validator.FieldError) string {
	tag := err.Tag()

	switch tag {
	case "required":
		return CodeValidationRequiredField
	case "email":
		return CodeEmailInvalid
	case "min":
		return CodeValueTooShort
	case "max":
		return CodeValueTooLong
	case "len":
		return CodeValueInvalidLength
	case "oneof":
		return CodeValueNotInList
	case "uuid":
		return CodeUUIDInvalid
	case "url":
		return CodeURLInvalid
	case "datetime":
		return CodeDatetimeInvalid
	default:
		return CodeValidationInvalidFormat
	}
}

// validationErrorMessage generates a human-readable message from the validation error
func validationErrorMessage(err validator.FieldError) string {
	field := err.Field()
	tag := err.Tag()
	param := err.Param()

	switch tag {
	case "required":
		return field + " is required"
	case "email":
		return "Invalid email format"
	case "min":
		if err.Type().String() == "string" {
			return field + " must be at least " + param + " characters"
		}
		return field + " must be at least " + param
	case "max":
		if err.Type().String() == "string" {
			return field + " must not exceed " + param + " characters"
		}
		return field + " must not exceed " + param
	case "len":
		return field + " must be exactly " + param + " characters"
	case "oneof":
		return field + " must be one of: " + param
	case "uuid":
		return "Invalid UUID format"
	case "url":
		return "Invalid URL format"
	case "datetime":
		return "Invalid date/time format"
	default:
		return field + " validation failed"
	}
}
