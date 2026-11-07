package apierror

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOapiValidationFailed_ParsesFieldValidation(t *testing.T) {
	t.Parallel()

	resp := OapiValidationFailed(
		`error in openapi3filter.RequestError: request body has an error: doesn't match schema #/components/schemas/NewUser: Error at "/password": minimum string length is 1`,
		"req-123",
		400,
	)

	require.NotNil(t, resp.Error)
	require.Equal(t, 400, resp.Error.HTTPStatus)
	require.Equal(t, CodeValidationFailed, resp.Error.Code)
	require.Equal(t, "Request validation failed", resp.Error.Message)
	require.Equal(t, "req-123", resp.Error.RequestID)
	require.Len(t, resp.Error.ValidationErrors, 1)
	require.Equal(t, "password", resp.Error.ValidationErrors[0].Field)
	require.Equal(t, CodeValidationRequiredField, resp.Error.ValidationErrors[0].Code)
	require.Equal(t, "is required", resp.Error.ValidationErrors[0].Message)
}

func TestOapiValidationFailed_FallbackToRawMessage(t *testing.T) {
	t.Parallel()

	resp := OapiValidationFailed("plain validator failure", "", 400)

	require.NotNil(t, resp.Error)
	require.Equal(t, 400, resp.Error.HTTPStatus)
	require.Equal(t, CodeValidationFailed, resp.Error.Code)
	require.Equal(t, "plain validator failure", resp.Error.Message)
	require.Empty(t, resp.Error.ValidationErrors)
}
