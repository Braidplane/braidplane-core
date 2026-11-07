package oauthflow

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashState_ReturnsStableNonSecretDigest(t *testing.T) {
	t.Parallel()

	rawState := "raw-browser-state-value"

	got := HashState(rawState)

	assert.Equal(t, HashState(rawState), got)
	assert.Len(t, got, 64)
	assert.NotContains(t, got, rawState)
	_, err := hex.DecodeString(got)
	require.NoError(t, err)
}

func TestRandomState_ReturnsBrowserSafeOpaqueValues(t *testing.T) {
	t.Parallel()

	first, err := RandomState()
	require.NoError(t, err)
	second, err := RandomState()
	require.NoError(t, err)

	require.NotEmpty(t, first)
	require.NotEmpty(t, second)
	assert.NotEqual(t, first, second)
	assert.NotContains(t, first, "+")
	assert.NotContains(t, first, "/")
	assert.NotContains(t, first, "=")
}

func TestProviderError_RedactsSensitiveValues(t *testing.T) {
	t.Parallel()

	err := SafeProviderError("invalid_grant", map[string]string{
		"error_description": "refresh token r1-secret-token was revoked",
		"access_token":      "a1-secret-token",
		"refresh_token":     "r1-secret-token",
	})

	got := err.Error()
	assert.Contains(t, got, "invalid_grant")
	assert.NotContains(t, got, "a1-secret-token")
	assert.NotContains(t, got, "r1-secret-token")
	assert.NotContains(t, got, "Authorization")
}

func TestProviderError_SanitizesProviderControlledCode(t *testing.T) {
	t.Parallel()

	err := SafeProviderError("", map[string]string{
		"error": " Invalid Grant: Needs Login ",
	})

	var providerErr ProviderError
	require.ErrorAs(t, err, &providerErr)
	assert.Equal(t, "invalid_grant__needs_login", providerErr.Code)
}
