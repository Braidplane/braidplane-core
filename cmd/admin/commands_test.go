package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAdminCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args        []string
		wantCommand string
		wantErr     bool
	}{
		{args: []string{"grant", "--user", "alice"}, wantCommand: "grant"},
		{args: []string{"revoke", "--user", "alice"}, wantCommand: "revoke"},
		{args: []string{"bootstrap", "--user", "alice", "--name", "Alice", "--email", "alice@example.test", "--email-ownership-verified"}, wantCommand: "bootstrap"},
		{args: []string{"grant"}, wantErr: true},
		{args: []string{"bootstrap", "--user", "alice"}, wantErr: true},
		{args: []string{"promote", "--user", "alice"}, wantErr: true},
		{args: []string{"grant", "--password", "synthetic", "--user", "alice"}, wantErr: true},
		{args: []string{"grant", "--user", "alice", "extra"}, wantErr: true},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			result, _, err := parseAdminCLI(tc.args, &bytes.Buffer{}, &bytes.Buffer{})
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantCommand, result.Context.Command())
		})
	}
	for _, command := range []string{"grant", "revoke"} {
		require.Error(t, runAdminCLI([]string{command, "--user", "Alice"}, &bytes.Buffer{}, &bytes.Buffer{}))
	}
	require.Error(t, runBootstrapCLI(nil, bootstrapCommand{User: "alice"}, strings.NewReader("synthetic password"), &bytes.Buffer{}))
	require.Error(t, runBootstrapCLI(nil, bootstrapCommand{User: "Alice", EmailOwnershipVerified: true}, strings.NewReader("synthetic password"), &bytes.Buffer{}))
}

func TestAdminHelpDoesNotLoadConfiguration(t *testing.T) {
	t.Setenv("CONFIG_PATH", t.TempDir()+"/missing.toml")
	for _, args := range [][]string{{"--help"}, {"-h"}, {"bootstrap", "--help"}, {"grant", "--help"}, {"revoke", "--help"}, {"credential-rekey", "--help"}} {
		var stdout, stderr bytes.Buffer
		require.NoError(t, runAdminCLI(args, &stdout, &stderr))
		require.Contains(t, stdout.String(), "braidplane-admin")
		require.Empty(t, stderr.String())
	}
}

func TestAdminPreservesConfigurationFlags(t *testing.T) {
	t.Parallel()
	result, configArgs, err := parseAdminCLI([]string{"grant", "--user", "alice", "--db-port", "6543"}, &bytes.Buffer{}, &bytes.Buffer{})
	require.NoError(t, err)
	require.Equal(t, "alice", result.CLI.Grant.User)
	require.Equal(t, []string{"--db-port", "6543"}, configArgs)
}
