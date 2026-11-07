package repostore

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/hostguard"
)

func TestHTTPSAuthEnvSuppliesCredentialsWithoutAskPassFile(t *testing.T) {
	t.Parallel()

	env := HTTPSAuthEnv(nil, "alice", "supersecret")
	cmd := exec.CommandContext(context.Background(), "git", "credential", "fill")
	cmd.Env = sanitizedGitEnv(os.Environ(), env)
	cmd.Stdin = strings.NewReader("protocol=https\nhost=example.com\n\n")

	output, err := cmd.Output()
	require.NoError(t, err)
	require.Contains(t, string(output), "username=alice\n")
	require.Contains(t, string(output), "password=supersecret\n")
	require.NotContains(t, strings.Join(env, "\n"), "GIT_ASKPASS=")
	for _, value := range env {
		if strings.HasPrefix(value, "GIT_CONFIG_VALUE_") {
			require.NotContains(t, value, "alice")
			require.NotContains(t, value, "supersecret")
		}
	}
}

// TestPrepareRemotePinsHTTPSAndDisablesUnvalidatedRedirects verifies HTTPS dials stay pinned and redirects cannot change destinations.
func TestPrepareRemotePinsHTTPSAndDisablesUnvalidatedRedirects(t *testing.T) {
	t.Parallel()

	remote, env, err := prepareRemoteForTest(
		context.Background(),
		"https://alice:secret@git.example.com:8443/acme/repo.git",
		[]string{
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=url.ssh://internal/.insteadOf",
			"GIT_CONFIG_VALUE_0=https://git.example.com/",
			"HTTPS_PROXY=http://proxy.example:8080",
		},
		hostguard.NewDestinationPolicy(false),
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		},
	)

	require.NoError(t, err)
	require.Equal(t, "https://git.example.com:8443/acme/repo.git", remote)
	require.Contains(t, env, "GIT_CONFIG_GLOBAL=/dev/null")
	require.Contains(t, env, "GIT_CONFIG_NOSYSTEM=1")
	require.Contains(t, env, "GIT_CONFIG_COUNT=3")
	require.Contains(t, env, "GIT_CONFIG_KEY_0=http.curloptResolve")
	require.Contains(t, env, "GIT_CONFIG_VALUE_0=git.example.com:8443:93.184.216.34")
	require.Contains(t, env, "GIT_CONFIG_KEY_1=http.followRedirects")
	require.Contains(t, env, "GIT_CONFIG_VALUE_1=false")
	require.Contains(t, env, "GIT_CONFIG_KEY_2=http.proxy")
	require.Contains(t, env, "GIT_CONFIG_VALUE_2=")
	require.NotContains(t, env, "GIT_CONFIG_KEY_0=url.ssh://internal/.insteadOf")
	require.NotContains(t, env, "HTTPS_PROXY=http://proxy.example:8080")
	require.Contains(t, env, "HTTP_PROXY=")
	require.Contains(t, env, "HTTPS_PROXY=")
	require.Contains(t, env, "ALL_PROXY=")
}

func TestPrepareRemoteCanonicalizesGitLabHTTPSURLBeforePinning(t *testing.T) {
	t.Parallel()

	for _, repoURL := range []string{
		"https://gitlab.com/braidplane/ai-agent-test-fixtures/",
		"https://gitlab.com/braidplane/ai-agent-test-fixtures.git/",
		"https://gitlab.com/braidplane/ai-agent-test-fixtures.git//",
	} {
		remote, _, err := prepareRemoteForTest(
			context.Background(), repoURL, nil,
			hostguard.NewDestinationPolicy(false),
			func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
			},
		)

		require.NoError(t, err)
		require.Equal(t, "https://gitlab.com/braidplane/ai-agent-test-fixtures.git", remote)
	}
}

// TestPrepareRemotePinsSSHAndPreservesKnownHostIdentity verifies SSH pinning retains the configured host identity and isolates inherited routes.
func TestPrepareRemotePinsSSHAndPreservesKnownHostIdentity(t *testing.T) {
	t.Parallel()

	remote, env, err := prepareRemoteForTest(
		context.Background(),
		"ssh://git@git.example.com:2222/acme/repo.git",
		[]string{"GIT_SSH_COMMAND=ssh -i /tmp/key -o ProxyCommand=nc%20proxy.example -o ProxyJump=jump.example -o HostName=attacker.example -oHostKeyAlias=attacker-alias"},
		hostguard.NewDestinationPolicy(false),
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("2606:2800:220:1:248:1893:25c8:1946")}}, nil
		},
	)

	require.NoError(t, err)
	require.Equal(t, "ssh://git@git.example.com:2222/acme/repo.git", remote)
	require.Contains(t, env, "GIT_SSH_COMMAND=ssh -i /tmp/key -F /dev/null -o ProxyCommand=none -o ProxyJump=none -o HostName='2606:2800:220:1:248:1893:25c8:1946' -o HostKeyAlias='[git.example.com]:2222'")
	require.NotContains(t, env, "attacker.example")
	require.NotContains(t, env, "attacker-alias")
}

// TestPrepareRemoteRejectsAmbiguousSSHCommand verifies malformed inherited shell syntax cannot bypass pinning.
func TestPrepareRemoteRejectsAmbiguousSSHCommand(t *testing.T) {
	t.Parallel()

	_, _, err := prepareRemoteForTest(
		context.Background(),
		"ssh://git@git.example.com/acme/repo.git",
		[]string{`GIT_SSH_COMMAND=ssh -o ProxyCommand='nc proxy.example`},
		hostguard.NewDestinationPolicy(false),
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		},
	)

	require.ErrorContains(t, err, "unterminated GIT_SSH_COMMAND")
}

// TestPrepareRemoteRejectsMetadataAndMixedAnswersBeforeGitStarts verifies unsafe or mixed DNS answers fail before Git runs.
func TestPrepareRemoteRejectsMetadataAndMixedAnswersBeforeGitStarts(t *testing.T) {
	t.Parallel()

	_, _, err := prepareRemoteForTest(
		context.Background(),
		"https://git.example.com/acme/repo.git",
		nil,
		hostguard.NewDestinationPolicy(false),
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{
				{IP: net.ParseIP("93.184.216.34")},
				{IP: net.ParseIP("169.254.169.254")},
			}, nil
		},
	)

	require.ErrorContains(t, err, "non-public ip address")
}

// TestPrepareRemotePinsFirstVettedAddressAcrossAddressFamilies verifies the first vetted DNS answer is selected across IP families.
func TestPrepareRemotePinsFirstVettedAddressAcrossAddressFamilies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		repoURL   string
		addresses []net.IPAddr
		wantEnv   string
	}{
		{
			name:    "HTTPSFirstIPv6",
			repoURL: "https://git.example.com/acme/repo.git",
			addresses: []net.IPAddr{
				{IP: net.ParseIP("2606:2800:220:1:248:1893:25c8:1946")},
				{IP: net.ParseIP("93.184.216.34")},
			},
			wantEnv: "GIT_CONFIG_VALUE_0=git.example.com:443:[2606:2800:220:1:248:1893:25c8:1946]",
		},
		{
			name:    "SSHFirstIPv4",
			repoURL: "ssh://git@git.example.com/acme/repo.git",
			addresses: []net.IPAddr{
				{IP: net.ParseIP("93.184.216.34")},
				{IP: net.ParseIP("2606:2800:220:1:248:1893:25c8:1946")},
			},
			wantEnv: "GIT_SSH_COMMAND=ssh -F /dev/null -o ProxyCommand=none -o ProxyJump=none -o HostName='93.184.216.34' -o HostKeyAlias='git.example.com'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, env, err := prepareRemoteForTest(
				context.Background(),
				tt.repoURL,
				nil,
				hostguard.NewDestinationPolicy(false),
				func(context.Context, string) ([]net.IPAddr, error) {
					return tt.addresses, nil
				},
			)

			require.NoError(t, err)
			require.Contains(t, env, tt.wantEnv)
		})
	}
}

// TestPrepareRemoteRejectsUnsupportedGitHTTPSRuntime verifies HTTPS pinning fails closed without verified runtime capabilities.
func TestPrepareRemoteRejectsUnsupportedGitHTTPSRuntime(t *testing.T) {
	t.Parallel()

	_, _, err := prepareRemoteWithLookupAndGitHTTPSCapabilities(
		context.Background(),
		"https://git.example.com/acme/repo.git",
		nil,
		hostguard.NewDestinationPolicy(false),
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		},
		func(context.Context, net.IP) error {
			return ErrRepositorySetup.Detail("Git 2.37 or newer is required to pin HTTPS destinations")
		},
	)

	require.ErrorContains(t, err, "Git 2.37 or newer is required")
}

// TestValidateGitHTTPSCapabilities verifies Git HTTPS pinning requires a supported Git version.
func TestValidateGitHTTPSCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		buildOutput string
		wantError   string
	}{
		{
			name:        "Supported",
			buildOutput: "git version 2.37.0\n",
		},
		{
			name:        "OldGit",
			buildOutput: "git version 2.36.9\n",
			wantError:   "Git 2.37 or newer",
		},
		{
			name:        "MissingGitVersion",
			buildOutput: "cpu: x86_64\n",
			wantError:   "could not verify Git version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateGitHTTPSCapabilities(tt.buildOutput)
			if tt.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantError)
		})
	}
}

// TestProbeGitHTTPSResolve verifies Git's linked HTTP transport honors the pinned resolver entry.
func TestProbeGitHTTPSResolve(t *testing.T) {
	t.Parallel()

	require.NoError(t, probeGitHTTPSResolve(t.Context(), net.ParseIP("93.184.216.34")))
}

// prepareRemoteForTest bypasses the installed Git probe so destination tests remain hermetic.
func prepareRemoteForTest(
	ctx context.Context,
	repoURL string,
	env []string,
	policy hostguard.DestinationPolicy,
	lookup func(context.Context, string) ([]net.IPAddr, error),
) (string, []string, error) {
	return prepareRemoteWithLookupAndGitHTTPSCapabilities(
		ctx,
		repoURL,
		env,
		policy,
		lookup,
		func(context.Context, net.IP) error { return nil },
	)
}
