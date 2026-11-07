package main

import (
	"bytes"
	"testing"

	"github.com/Braidplane/braidplane-core/postgres/credentialrekey"
	"github.com/stretchr/testify/require"
)

func TestParseCredentialRekeyArgs(t *testing.T) {
	t.Parallel()
	result, _, err := parseAdminCLI([]string{"credential-rekey", "--after-family", "3", "--after-id", "42", "--batch-size", "100"}, &bytes.Buffer{}, &bytes.Buffer{})
	require.NoError(t, err)
	after, limit, err := result.CLI.CredentialRekey.cursor()
	require.NoError(t, err)
	require.Equal(t, credentialrekey.Cursor{Family: 3, ID: 42}, after)
	require.EqualValues(t, 100, limit)
	for _, args := range [][]string{{"--batch-size", "101"}, {"--batch-size", "0"}, {"--after-family", "7"}, {"--after-id", "-1"}, {"--after-id", "1"}, {"extra"}} {
		result, _, err := parseAdminCLI(append([]string{"credential-rekey"}, args...), &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil {
			_, _, err = result.CLI.CredentialRekey.cursor()
		}
		require.Error(t, err)
	}
	var help bytes.Buffer
	require.NoError(t, runAdminCLI([]string{"credential-rekey", "--help"}, &help, &bytes.Buffer{}))
	require.Contains(t, help.String(), "batch-size")
}
