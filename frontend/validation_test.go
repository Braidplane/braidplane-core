package frontend

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/validate"
)

func TestFirstValidationMessage_KnownTagHandler(t *testing.T) {
	t.Parallel()

	err := validate.Default().Struct(
		signupInput{
			FullName: "",
			Email:    "test@example.com",
			Password: "secret",
		},
	)
	require.Error(t, err)

	message := firstValidationMessage(err)

	require.Equal(t, "Full name is required.", message)
}

func TestFirstValidationMessage_UnknownTagUsesFallback(t *testing.T) {
	t.Parallel()

	err := validate.Default().Struct(signupInput{
		FullName: strings.Repeat("a", 300),
		Email:    "test@example.com",
		Password: "secret",
	})
	require.Error(t, err)

	message := firstValidationMessage(err)

	require.Equal(t, "Full name is too long.", message)
}

func TestFirstValidationMessage_NonValidationError(t *testing.T) {
	t.Parallel()

	message := firstValidationMessage(fmt.Errorf("boom"))

	require.Empty(t, message)
}

func TestNormalizeFieldName(t *testing.T) {
	t.Parallel()

	require.Empty(t, normalizeFieldName(""))
	require.Equal(t, "full_name", normalizeFieldName("FullName"))
	require.Equal(t, "email", normalizeFieldName("Email"))
	require.Equal(t, "password", normalizeFieldName("password"))
}

func TestValidateSignupInput_PasswordTooShort(t *testing.T) {
	t.Parallel()

	message := validateSignupInput("Jane Doe", "test@example.com", "short")

	require.Equal(t, "Password must be at least 8 characters.", message)
}

func TestNormalizeSignupUsageType(t *testing.T) {
	t.Parallel()

	require.Equal(t, "personal", normalizeSignupUsageType(""))
	require.Equal(t, "personal", normalizeSignupUsageType("personal"))
	require.Equal(t, "organization", normalizeSignupUsageType("organization"))
	require.Equal(t, "personal", normalizeSignupUsageType("invalid"))
}

func TestValidateLoginInput_FieldIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ email, password, field string }{
		{"", "synthetic-password", "email"},
		{"operator@example.com", "short", "password"},
		{"operator@example.com", "synthetic-password", ""},
	} {
		message, field := validateLoginInput(tc.email, tc.password)
		require.Equal(t, tc.field, field)
		require.Equal(t, tc.field == "", message == "")
	}
}

func TestFormValidationField(t *testing.T) {
	t.Parallel()
	require.Empty(t, formValidationField(nil, "name"))
	payload := parseAPIErrorResponse([]byte(`{"error":{"validation_errors":[{"field":"config","message":"invalid"},{"field":"name","message":"too long"}]}}`))
	require.Empty(t, formValidationField(payload, "name", "slug"))
	require.Empty(t, formValidationField(payload, "slug"))
	payload = parseAPIErrorResponse([]byte(`{"error":{"validation_errors":[{"field":"name","message":"too long"}]}}`))
	require.Equal(t, "name", formValidationField(payload, "name", "slug"))
}
