package apierror

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/stretchr/testify/require"
)

func TestOapiValidationFailedUsesOnlyDeclaredParameterMetadata(t *testing.T) {
	t.Parallel()

	resp := OapiValidationFailed(&openapi3filter.RequestError{
		Parameter: &openapi3.Parameter{Name: "limit", In: "query"},
		Err:       fmt.Errorf("opaque-parameter-canary"),
	}, "req-123")
	require.Equal(t, http.StatusBadRequest, resp.Error.HTTPStatus)
	require.Equal(t, CodeValidationFailed, resp.Error.Code)
	require.Equal(t, "Request validation failed", resp.Error.Message)
	require.Equal(t, "req-123", resp.Error.RequestID)
	require.Equal(t, []ValidationError{{
		Field: "limit", Code: CodeValidationInvalidFormat, Message: "Invalid parameter",
	}}, resp.Error.ValidationErrors)
}

func TestOapiValidationFailedUsesGenericFallback(t *testing.T) {
	t.Parallel()

	resp := OapiValidationFailed(fmt.Errorf("opaque-validation-canary"), "")
	require.Equal(t, http.StatusBadRequest, resp.Error.HTTPStatus)
	require.Equal(t, CodeValidationFailed, resp.Error.Code)
	require.Equal(t, "Request validation failed", resp.Error.Message)
	require.Empty(t, resp.Error.ValidationErrors)
}
