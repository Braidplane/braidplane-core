package frontend

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/go-playground/validator/v10"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type validationTagHandler func(fieldErr validator.FieldError, label string) string

const pipelineDraftConflictErrorCode = "PIPELINE_DRAFT_CONFLICT"

const (
	signupUsagePersonal     = "personal"
	signupUsageOrganization = "organization"
)

var validationFallbackHandler = func(_ validator.FieldError, label string) string {
	return label + " is invalid."
}

var validationTagHandlers = map[string]validationTagHandler{
	"required": func(_ validator.FieldError, label string) string {
		return label + " is required."
	},
	"email": func(_ validator.FieldError, _ string) string {
		return "Email must be a valid email address."
	},
	"min": func(fieldErr validator.FieldError, label string) string {
		if normalizeFieldName(fieldErr.Field()) == "password" {
			return fmt.Sprintf("Password must be at least %s characters.", fieldErr.Param())
		}
		return fmt.Sprintf("%s is too short.", label)
	},
	"max": func(_ validator.FieldError, label string) string {
		return fmt.Sprintf("%s is too long.", label)
	},
}

func parseAPIErrorResponse(body []byte) *api.ErrorResponse {
	if len(body) == 0 {
		return nil
	}

	var payload api.ErrorResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}

	return &payload
}

func formValidationField(payload *api.ErrorResponse, fields ...string) string {
	if payload == nil || payload.Error.ValidationErrors == nil {
		return ""
	}
	validation := *payload.Error.ValidationErrors
	if len(validation) > 0 && slices.Contains(fields, validation[0].Field) {
		return validation[0].Field
	}
	return ""
}

func parsePipelineDraftConflictResponse(body []byte) *api.PipelineDraftConflictResponse {
	if len(body) == 0 {
		return nil
	}

	var payload api.PipelineDraftConflictResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}

	if payload.Error.Code != pipelineDraftConflictErrorCode {
		return nil
	}

	return &payload
}

func messageFromErrorResponse(payload *api.ErrorResponse) string {
	if payload == nil {
		return ""
	}

	validationMessage := ""
	if payload.Error.ValidationErrors != nil {
		validationErrors := *payload.Error.ValidationErrors
		if len(validationErrors) > 0 {
			first := validationErrors[0]
			if first.Field != "" && first.Message != "" {
				validationMessage = formatValidationError(fieldLabel(first.Field), first.Message)
			} else if first.Message != "" {
				validationMessage = first.Message
			}
		}
	}

	if payload.Error.Message != "" {
		if validationMessage != "" {
			return validationMessage + " (" + payload.Error.Message + ")"
		}
		return payload.Error.Message
	}

	if validationMessage != "" {
		return validationMessage
	}

	return ""
}

func detailedErrorFromErrorResponse(payload *api.ErrorResponse) string {
	if payload == nil || payload.Error.DetailedError == nil {
		return ""
	}

	return strings.TrimSpace(*payload.Error.DetailedError)
}

// formFeedbackFromErrorResponse combines short backend messages with safe detailed text for swapped form alerts.
func formFeedbackFromErrorResponse(payload *api.ErrorResponse, fallback string) string {
	message := strings.TrimSpace(messageFromErrorResponse(payload))
	if message == "" {
		message = strings.TrimSpace(fallback)
	}

	details := detailedErrorFromErrorResponse(payload)
	if details == "" {
		return message
	}
	if message == "" || message == details {
		return details
	}
	if strings.Contains(message, details) {
		return message
	}

	return message + ": " + details
}

func mergeErrorDetails(details ...string) string {
	parts := make([]string, 0, len(details))

	for _, detail := range details {
		trimmed := strings.TrimSpace(detail)
		if trimmed == "" {
			continue
		}

		duplicate := slices.Contains(parts, trimmed)

		if !duplicate {
			parts = append(parts, trimmed)
		}
	}

	return strings.Join(parts, "\n---\n")
}

func errorPopupDetails(payload *api.ErrorResponse, fallback string) string {
	return mergeErrorDetails(fallback, detailedErrorFromErrorResponse(payload))
}

func backendErrorDetails(
	action string,
	status int,
	payload *api.ErrorResponse,
	callErr error,
) string {
	parts := []string{fmt.Sprintf("action=%s", action), fmt.Sprintf("status=%d", status)}

	if payload != nil {
		if payload.Error.Code != "" {
			parts = append(parts, "backend_code="+payload.Error.Code)
		}
		if payload.Error.Message != "" {
			parts = append(parts, "backend_message="+payload.Error.Message)
		}
		if payload.Error.ValidationErrors != nil {
			validationErrors := *payload.Error.ValidationErrors
			if len(validationErrors) > 0 {
				first := validationErrors[0]
				field := strings.TrimSpace(first.Field)
				msg := strings.TrimSpace(first.Message)
				if field != "" || msg != "" {
					parts = append(parts, fmt.Sprintf("validation=%s:%s", field, msg))
				}
			}
		}
	}

	if callErr != nil {
		parts = append(parts, "client_error="+callErr.Error())
	}

	return errorPopupDetails(payload, strings.Join(parts, "\n"))
}

func formatValidationError(label, message string) string {
	trimmed := strings.TrimSpace(message)
	if strings.HasPrefix(trimmed, "is ") || strings.HasPrefix(trimmed, "must ") ||
		strings.HasPrefix(trimmed, "has ") {
		return label + " " + trimmed
	}
	return label + ": " + trimmed
}

func fieldLabel(field string) string {
	switch field {
	case "usage_type":
		return "Usage type"
	case "organization_workspace_name":
		return "Organization workspace name"
	case "full_name":
		return "Full name"
	case "email":
		return "Email"
	case "password":
		return "Password"
	default:
		if field == "" {
			return "Field"
		}
		return strings.ToUpper(field[:1]) + strings.ReplaceAll(field[1:], "_", " ")
	}
}

func fallbackMessageFromStatus(status int) string {
	return fmt.Sprintf("Signup failed with status %d.", status)
}

func fallbackLoginMessageFromStatus(status int) string {
	return fmt.Sprintf("Sign in failed with status %d.", status)
}

type signupInput struct {
	FullName string `validate:"required,min=1,max=255"`
	Email    string `validate:"required,min=3,max=255,email"`
	Password string `validate:"required,min=8"`
}

func validateSignupInput(fullName, email, password string) string {
	return validateInput(signupInput{
		FullName: fullName,
		Email:    email,
		Password: password,
	}, "Signup data is invalid.")
}

// normalizeSignupUsageType defaults unknown workspace choices to personal setup.
func normalizeSignupUsageType(value string) string {
	switch strings.TrimSpace(value) {
	case signupUsageOrganization:
		return signupUsageOrganization
	default:
		return signupUsagePersonal
	}
}

type loginInput struct {
	Email    string `validate:"required,min=3,max=255,email"`
	Password string `validate:"required,min=8"`
}

func validateLoginInput(email, password string) (string, string) {
	return validateInputWithField(loginInput{
		Email:    email,
		Password: password,
	}, "Sign in data is invalid.")
}

type forgotPasswordInput struct {
	Email string `validate:"required,min=3,max=255,email"`
}

func validateForgotPasswordInput(email string) string {
	return validateInput(forgotPasswordInput{Email: email}, "Forgot password data is invalid.")
}

type memberInviteInput struct {
	Email string `validate:"required,min=3,max=255,email"`
	Role  string `validate:"required,min=1,max=255"`
}

func validateMemberInviteInput(email, role string) string {
	return validateInput(memberInviteInput{Email: email, Role: role}, "Invite data is invalid.")
}

type resetPasswordInput struct {
	Token    string `validate:"required,min=1"`
	Password string `validate:"required,min=8"`
}

func validateResetPasswordInput(token, password string) string {
	return validateInput(
		resetPasswordInput{Token: token, Password: password},
		"Reset password data is invalid.",
	)
}

type organizationInput struct {
	Name string `validate:"required,min=1,max=255"`
}

func validateOrganizationInput(name string) string {
	return validateInput(organizationInput{Name: name}, "Organization data is invalid.")
}

func validateInput[T any](v T, fallbackMessage string) string {
	message, _ := validateInputWithField(v, fallbackMessage)
	return message
}

func validateInputWithField[T any](v T, fallbackMessage string) (string, string) {
	err := validate.Default().Struct(v)
	if err == nil {
		return "", ""
	}
	var validationErrors validator.ValidationErrors
	if xerror.As(err, &validationErrors) && len(validationErrors) > 0 {
		if message := firstValidationMessage(err); message != "" {
			return message, normalizeFieldName(validationErrors[0].Field())
		}
	}
	return fallbackMessage, ""
}

func firstValidationMessage(err error) string {
	var validationErrors validator.ValidationErrors
	if !xerror.As(err, &validationErrors) || len(validationErrors) == 0 {
		return ""
	}

	first := validationErrors[0]
	label := fieldLabel(normalizeFieldName(first.Field()))

	if handler, ok := validationTagHandlers[first.Tag()]; ok {
		if message := handler(first, label); message != "" {
			return message
		}
	}

	if validationFallbackHandler == nil {
		return ""
	}

	return validationFallbackHandler(first, label)
}

func normalizeFieldName(field string) string {
	if field == "" {
		return ""
	}

	var builder strings.Builder
	builder.Grow(len(field) + 4)

	for i := 0; i < len(field); i++ {
		ch := field[i]
		if i > 0 && ch >= 'A' && ch <= 'Z' {
			builder.WriteByte('_')
		}
		if ch >= 'A' && ch <= 'Z' {
			ch = ch - 'A' + 'a'
		}
		builder.WriteByte(ch)
	}

	return builder.String()
}
