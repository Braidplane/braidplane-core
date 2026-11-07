package redaction

import (
	"fmt"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestErrorMessageRedactsSensitiveDiagnostics(t *testing.T) {
	t.Parallel()

	secret := "known-secret"
	message := ErrorMessage(fmt.Errorf(
		"provider failed: password=pass-123 Bearer bearer-123 ghp_abcdefghijklmnop glpat-secret-123 "+
			"https://user:pass@example.test/token/path?access_token=query-token %s -----BEGIN OPENSSH PRIVATE KEY-----\nkey\n-----END OPENSSH PRIVATE KEY-----",
		secret,
	), secret)

	require.Contains(t, message, "provider failed")
	for _, value := range []string{
		"pass-123", "bearer-123", "ghp_abcdefghijklmnop", "glpat-secret-123", "user:pass",
		"query-token", "known-secret", "BEGIN OPENSSH PRIVATE KEY", "\nkey\n",
	} {
		require.NotContains(t, message, value)
	}
}

func TestErrorMessageRedactsQuotedFieldsAndDatabaseURLs(t *testing.T) {
	t.Parallel()
	message := ErrorMessage(xerror.Unexpected().Detail(`provider failed {"refresh_token":"refresh-value","password":"password-value","session_id":"session-value"} postgres://user:db-password@host/database`))
	require.Contains(t, message, "provider failed")
	for _, value := range []string{"refresh-value", "password-value", "session-value", "db-password"} {
		require.NotContains(t, message, value)
	}
}

func TestErrorMessageDoesNotUnwrapRedactedError(t *testing.T) {
	t.Parallel()

	hidden := xerror.Unexpected().Detail("raw provider response secret=must-not-log")
	err := redactedError{message: "provider request failed", cause: hidden}

	message := ErrorMessage(err)
	require.Equal(t, "provider request failed", message)
	require.NotContains(t, message, "must-not-log")
}

func TestErrorMessageRedactsEscapedQuotedCredentials(t *testing.T) {
	t.Parallel()
	for _, diagnostic := range []string{
		`provider failed: password="ab\"cd" retry later`,
		`provider failed: password='ab\'cd' retry later`,
		`provider failed: password="ab\\" retry later`,
		`provider failed: password='ab\\' retry later`,
	} {
		t.Run(diagnostic, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, "provider failed: password=[REDACTED] retry later",
				ErrorMessage(xerror.Unexpected().Detail(diagnostic)))
		})
	}
}

type redactedError struct {
	message string
	cause   error
}

func (e redactedError) Error() string { return e.message }

func (e redactedError) Unwrap() error { return e.cause }
