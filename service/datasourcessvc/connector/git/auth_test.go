package git

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPSURLModifier_RemovesCredentialsFromGitArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		username string
		token    string
		inputURL string
		wantURL  string
	}{
		{
			name:     "UsernameAndToken",
			username: "alice",
			token:    "tok123",
			inputURL: "https://github.com/org/repo.git",
			wantURL:  "https://github.com/org/repo.git",
		},
		{
			name:     "TokenOnly",
			username: "",
			token:    "ghp_abc",
			inputURL: "https://github.com/org/repo.git",
			wantURL:  "https://github.com/org/repo.git",
		},
		{
			name:     "ExistingURLUserinfo",
			username: "alice",
			token:    "tok123",
			inputURL: "https://old-user:old-token@github.com/org/repo.git",
			wantURL:  "https://github.com/org/repo.git",
		},
		{
			name:     "NoCredentials",
			username: "",
			token:    "",
			inputURL: "https://github.com/org/repo.git",
			wantURL:  "https://github.com/org/repo.git",
		},
		{
			name:     "HTTPURLUnchanged",
			username: "user",
			token:    "pass",
			inputURL: "http://internal.example.com/repo.git",
			wantURL:  "http://internal.example.com/repo.git",
		},
		{
			name:     "NonHTTPSchemeUnchanged",
			username: "user",
			token:    "pass",
			inputURL: "git@github.com:org/repo.git",
			wantURL:  "git@github.com:org/repo.git",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			modifier := httpsURLModifier(tc.username, tc.token)
			got, err := modifier(tc.inputURL)

			require.NoError(t, err)
			require.Equal(t, tc.wantURL, got)
		})
	}
}

func TestAuthEnv_HTTPSUsesCredentialHelperWithoutExecutableTempFile(t *testing.T) {
	t.Parallel()

	creds := Credentials{
		AuthType: AuthTypeHTTPS,
		Username: "alice",
		Token:    "supersecret",
	}

	env, _, cleanup, err := authEnv(creds)
	t.Cleanup(cleanup)

	require.NoError(t, err)
	require.Contains(t, env, "GIT_TERMINAL_PROMPT=0")
	require.Contains(t, env, "BRAIDPLANE_GIT_USERNAME=alice")
	require.Contains(t, env, "BRAIDPLANE_GIT_PASSWORD=supersecret")
	require.NotContains(t, strings.Join(env, "\n"), "GIT_ASKPASS=")
	require.NotContains(t, strings.Join(env, "\n"), "username=alice")
	require.NotContains(t, strings.Join(env, "\n"), "password=supersecret")
}

func TestAuthEnv_HTTPSTokenOnlyRequiresUsername(t *testing.T) {
	t.Parallel()

	_, _, cleanup, err := authEnv(Credentials{AuthType: AuthTypeHTTPS, Token: "supersecret"})
	t.Cleanup(cleanup)
	require.ErrorContains(t, err, "HTTPS auth requires username")
}

func TestAuthEnv_SSHRequiresKey(t *testing.T) {
	t.Parallel()

	creds := Credentials{
		AuthType:      AuthTypeSSH,
		SSHPrivateKey: "",
	}

	_, _, cleanup, err := authEnv(creds)
	defer cleanup()

	require.ErrorContains(t, err, "SSH auth requires ssh_private_key")
}

func TestAuthEnv_SSHRequiresKnownHosts(t *testing.T) {
	t.Parallel()

	creds := Credentials{
		AuthType:      AuthTypeSSH,
		SSHPrivateKey: "test-private-key",
	}

	_, _, cleanup, err := authEnv(creds)
	defer cleanup()

	require.ErrorContains(t, err, "SSH auth requires ssh_known_hosts")
}

func TestAuthEnv_SSHVerifiesHostKeysWithKnownHosts(t *testing.T) {
	t.Parallel()

	creds := Credentials{
		AuthType:      AuthTypeSSH,
		SSHPrivateKey: "test-private-key",
		SSHKnownHosts: "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExamplePinnedHostKey",
	}

	env, _, cleanup, err := authEnv(creds)
	defer cleanup()

	require.NoError(t, err)

	sshCommand := ""
	for _, value := range env {
		if strings.HasPrefix(value, "GIT_SSH_COMMAND=") {
			sshCommand = strings.TrimPrefix(value, "GIT_SSH_COMMAND=")
			break
		}
	}
	require.NotEmpty(t, sshCommand)
	require.Contains(t, sshCommand, "-o StrictHostKeyChecking=yes")
	require.Contains(t, sshCommand, "-o UserKnownHostsFile=")
	require.NotContains(t, sshCommand, "StrictHostKeyChecking=no")
	require.NotContains(t, sshCommand, "UserKnownHostsFile=/dev/null")
}

func TestAuthEnv_InvalidAuthType(t *testing.T) {
	t.Parallel()

	creds := Credentials{
		AuthType: AuthType(99),
	}

	_, _, cleanup, err := authEnv(creds)
	defer cleanup()

	require.ErrorContains(t, err, "unsupported auth type")
}

func TestIdentityURLModifier(t *testing.T) {
	t.Parallel()

	modifier := identityURLModifier()
	got, err := modifier("git@github.com:org/repo.git")

	require.NoError(t, err)
	require.Equal(t, "git@github.com:org/repo.git", got)
}
