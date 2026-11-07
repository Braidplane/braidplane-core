package repostore

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// PrepareRemote resolves a Git remote once and adds native-Git transport settings that force
// HTTPS or SSH sockets to a vetted IP while preserving the original hostname for TLS and host keys.
// HTTPS redirects are disabled because native Git cannot expose each redirect target for revalidation.
func PrepareRemote(
	ctx context.Context,
	repoURL string,
	env []string,
	policy hostguard.DestinationPolicy,
) (string, []string, error) {
	return prepareRemoteWithLookup(ctx, repoURL, env, policy, net.DefaultResolver.LookupIPAddr)
}

// prepareRemoteWithLookup is the testable implementation of PrepareRemote.
func prepareRemoteWithLookup(
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
		verifyGitHTTPSCapabilities,
	)
}

// prepareRemoteWithLookupAndGitHTTPSCapabilities is the testable implementation of PrepareRemote
// with an injectable runtime capability check for the HTTPS resolve configuration.
func prepareRemoteWithLookupAndGitHTTPSCapabilities(
	ctx context.Context,
	repoURL string,
	env []string,
	policy hostguard.DestinationPolicy,
	lookup func(context.Context, string) ([]net.IPAddr, error),
	verifyCapabilities func(context.Context, net.IP) error,
) (string, []string, error) {
	if isLocalRemote(repoURL) {
		return strings.TrimSpace(repoURL), env, nil
	}

	remote, hostname, port, isHTTPS, err := parseRemoteDestination(repoURL)
	if err != nil {
		return "", nil, err
	}
	addresses, err := policy.ResolveWithLookup(ctx, hostname, lookup)
	if err != nil {
		return "", nil, xerror.Wrapf("validate Git destination: %w", err)
	}
	if len(addresses) == 0 {
		return "", nil, xerror.Unexpected().Detail("Git destination resolved to no IP addresses")
	}
	if isHTTPS {
		if err := verifyCapabilities(ctx, addresses[0].IP); err != nil {
			return "", nil, err
		}
	}

	preparedEnv := withoutGitConfigEnv(env)
	preparedEnv = withoutProxyEnv(preparedEnv)
	preparedEnv = withGitEnvOverrides(
		preparedEnv,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		// Empty values replace inherited proxy settings when the command environment is built.
		// A proxy would re-resolve the Git hostname outside the vetted destination policy.
		"HTTP_PROXY=",
		"HTTPS_PROXY=",
		"ALL_PROXY=",
		"http_proxy=",
		"https_proxy=",
		"all_proxy=",
	)
	if isHTTPS {
		return remote, withPinnedHTTPSDestination(preparedEnv, hostname, port, addresses[0].IP.String()), nil
	}

	preparedEnv, err = withPinnedSSHDestination(preparedEnv, hostname, port, addresses[0].IP.String())
	if err != nil {
		return "", nil, err
	}
	return remote, preparedEnv, nil
}

// isLocalRemote identifies file-backed test and operator remotes that do not open a network socket.
func isLocalRemote(repoURL string) bool {
	trimmed := strings.TrimSpace(repoURL)
	return filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "./") ||
		strings.HasPrefix(trimmed, "../") || strings.HasPrefix(trimmed, "file://")
}

// parseRemoteDestination validates a remote URL and returns the original command URL plus its
// network authority. HTTPS userinfo is deliberately removed because auth uses askpass helpers.
func parseRemoteDestination(repoURL string) (remote, hostname, port string, isHTTPS bool, err error) {
	if err := validateRemoteRepoURLScheme(repoURL); err != nil {
		return "", "", "", false, err
	}

	trimmed := strings.TrimSpace(repoURL)
	if strings.HasPrefix(strings.ToLower(trimmed), "https://") {
		parsed, parseErr := url.Parse(trimmed)
		if parseErr != nil {
			return "", "", "", false, ErrRepositorySetup.With(parseErr)
		}
		if parsed.Hostname() == "" {
			return "", "", "", false, ErrRepositorySetup.Detail("Git HTTPS URL has no hostname")
		}
		parsed.User = nil
		// GitLab redirects browser-style repository URLs to the direct Git endpoint.
		// Canonicalize that known same-host route before destination pinning so redirects stay disabled.
		if strings.EqualFold(parsed.Hostname(), gitlabHost) && parsed.Path != "" && parsed.Path != "/" {
			path := strings.TrimRight(parsed.Path, "/")
			if path != "" {
				parsed.Path = path
				if !strings.HasSuffix(strings.ToLower(parsed.Path), ".git") {
					parsed.Path += ".git"
				}
			}
		}
		port = parsed.Port()
		if port == "" {
			port = "443"
		}
		return parsed.String(), parsed.Hostname(), port, true, nil
	}

	if strings.HasPrefix(strings.ToLower(trimmed), "ssh://") {
		parsed, parseErr := url.Parse(trimmed)
		if parseErr != nil {
			return "", "", "", false, ErrRepositorySetup.With(parseErr)
		}
		if parsed.Hostname() == "" {
			return "", "", "", false, ErrRepositorySetup.Detail("Git SSH URL has no hostname")
		}
		port = parsed.Port()
		if port == "" {
			port = "22"
		}
		return parsed.String(), parsed.Hostname(), port, false, nil
	}

	hostname, _, ok := extractRepoHostAndPath(trimmed)
	if !ok || hostname == "" {
		return "", "", "", false, ErrRepositorySetup.Detail("Git SSH URL has no hostname")
	}
	return trimmed, hostname, "22", false, nil
}

// withPinnedHTTPSDestination configures libcurl's in-process resolver and blocks redirects that
// would otherwise create an unobservable effective URL boundary inside the native Git process.
func withPinnedHTTPSDestination(env []string, hostname, port, ip string) []string {
	entries := []gitConfigEntry{
		{key: "http.curloptResolve", value: fmt.Sprintf("%s:%s:%s", hostname, port, curlResolveAddress(ip))},
		{key: "http.followRedirects", value: "false"},
		{key: "http.proxy", value: ""},
	}
	if envValue(env, gitCredentialUsernameEnv) != "" || envValue(env, gitCredentialPasswordEnv) != "" {
		entries = append(entries, gitConfigEntry{key: "credential.helper", value: gitCredentialHelper})
	}
	return withGitConfig(env, entries...)
}

// withPinnedSSHDestination overrides the SSH socket host with the vetted IP while retaining the
// configured hostname as the known-hosts identity, including the non-default port form.
func withPinnedSSHDestination(env []string, hostname, port, ip string) ([]string, error) {
	hostKeyAlias := hostname
	if port != "22" {
		hostKeyAlias = "[" + hostname + "]:" + port
	}
	sshCommand := strings.TrimSpace(envValue(env, "GIT_SSH_COMMAND"))
	if sshCommand == "" {
		sshCommand = "ssh"
	}
	sshCommand, err := withoutSSHDestinationOptions(sshCommand)
	if err != nil {
		return nil, ErrRepositorySetup.With(err)
	}
	// Ignore per-user SSH configuration and disable proxy hops explicitly so a host match cannot
	// replace the policy-vetted socket destination with ProxyCommand or ProxyJump routing.
	sshCommand += " -F /dev/null"
	sshCommand += " -o ProxyCommand=none"
	sshCommand += " -o ProxyJump=none"
	sshCommand += " -o HostName=" + shellQuote(ip)
	sshCommand += " -o HostKeyAlias=" + shellQuote(hostKeyAlias)

	return withGitEnvOverrides(env, "GIT_SSH_COMMAND="+sshCommand), nil
}

// curlResolveAddress formats literal IPv6 values for libcurl's HOST:PORT:ADDRESS syntax.
func curlResolveAddress(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed != nil && parsed.To4() == nil {
		return "[" + ip + "]"
	}
	return ip
}

// withoutSSHDestinationOptions removes inherited routing, host, and configuration options before
// the pinned SSH command appends its fail-closed destination settings.
func withoutSSHDestinationOptions(command string) (string, error) {
	fields, err := splitSSHCommand(command)
	if err != nil {
		return "", err
	}
	filtered := make([]string, 0, len(fields))
	for index := 0; index < len(fields); index++ {
		field := fields[index]
		switch {
		case field == "-F" || field == "-J":
			index++
			continue
		case strings.HasPrefix(field, "-F") || strings.HasPrefix(field, "-J"):
			continue
		case field == "-o" && index+1 < len(fields) && isSSHDestinationOverride(fields[index+1]):
			index++
			continue
		case strings.HasPrefix(field, "-o") && isSSHDestinationOverride(strings.TrimPrefix(field, "-o")):
			continue
		}
		filtered = append(filtered, field)
	}
	return strings.Join(filtered, " "), nil
}

// splitSSHCommand accepts only simple shell words so inherited routing cannot hide in ambiguous syntax.
func splitSSHCommand(command string) ([]string, error) {
	var words []string
	var word strings.Builder
	quote := rune(0)
	escaped := false
	for _, char := range command {
		if escaped {
			word.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				word.WriteRune(char)
			}
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
		case ' ', '\t', '\n':
			if word.Len() > 0 {
				words = append(words, word.String())
				word.Reset()
			}
		case ';', '|', '&', '<', '>', '$', '`':
			return nil, xerror.Unexpected().Detail("ambiguous GIT_SSH_COMMAND syntax")
		default:
			word.WriteRune(char)
		}
	}
	if escaped || quote != 0 {
		return nil, xerror.Unexpected().Detail("unterminated GIT_SSH_COMMAND quoting")
	}
	if word.Len() > 0 {
		words = append(words, word.String())
	}
	if len(words) == 0 {
		return nil, xerror.Unexpected().Detail("empty GIT_SSH_COMMAND")
	}
	return words, nil
}

// isSSHDestinationOverride reports whether an SSH -o value can replace a pinned route or identity.
func isSSHDestinationOverride(option string) bool {
	name, _, ok := strings.Cut(option, "=")
	if !ok {
		return false
	}
	return strings.EqualFold(name, "ProxyCommand") ||
		strings.EqualFold(name, "ProxyJump") ||
		strings.EqualFold(name, "HostName") ||
		strings.EqualFold(name, "HostKeyAlias")
}

type gitVersion struct {
	major int
	minor int
	patch int
}

// atLeast reports whether a Git or libcurl version meets a required compatibility floor.
func (v gitVersion) atLeast(required gitVersion) bool {
	if v.major != required.major {
		return v.major > required.major
	}
	if v.minor != required.minor {
		return v.minor > required.minor
	}
	return v.patch >= required.patch
}

var minimumGitConfigEnvVersion = gitVersion{major: 2, minor: 37}

// verifyGitHTTPSCapabilities fails closed unless native Git supports isolated GIT_CONFIG_* injection
// and its linked HTTP transport honors the resolve syntax used for the destination address family.
func verifyGitHTTPSCapabilities(ctx context.Context, destinationIP net.IP) error {
	output, err := exec.CommandContext(ctx, "git", "--version", "--build-options").Output()
	if err != nil {
		return ErrRepositorySetup.With(err)
	}
	if err := validateGitHTTPSCapabilities(string(output)); err != nil {
		return err
	}
	return probeGitHTTPSResolve(ctx, destinationIP)
}

// probeGitHTTPSResolve verifies Git reaches a local listener only through http.curloptResolve.
func probeGitHTTPSResolve(ctx context.Context, destinationIP net.IP) error {
	network, loopback := "tcp4", "127.0.0.1"
	if destinationIP.To4() == nil {
		network, loopback = "tcp6", "::1"
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, network, net.JoinHostPort(loopback, "0"))
	if err != nil {
		return ErrRepositorySetup.With(err)
	}
	defer func() { _ = listener.Close() }()

	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	probeEnv := withoutProxyEnv(withoutGitConfigEnv(os.Environ()))
	probeEnv = withGitEnvOverrides(
		probeEnv,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY=",
		"http_proxy=", "https_proxy=", "all_proxy=",
	)
	probeEnv = withPinnedHTTPSDestination(
		probeEnv,
		"braidplane-git-capability.invalid",
		port,
		loopback,
	)

	accepted := make(chan bool, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
		accepted <- acceptErr == nil
	}()

	probeCtx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	command := exec.CommandContext(
		probeCtx,
		"git",
		"ls-remote",
		"--exit-code",
		fmt.Sprintf("https://braidplane-git-capability.invalid:%s/probe.git", port),
	)
	command.Env = probeEnv
	_ = command.Run()
	_ = listener.Close()
	if !<-accepted {
		return ErrRepositorySetup.Detail("Git HTTPS destination pinning capability is unavailable")
	}
	return nil
}

// validateGitHTTPSCapabilities verifies Git supports GIT_CONFIG_* and http.curloptResolve.
func validateGitHTTPSCapabilities(buildOutput string) error {
	gitVersion, gitFound := parseGitHTTPSCapabilities(buildOutput)
	if !gitFound {
		return ErrRepositorySetup.Detail("could not verify Git version for pinned HTTPS destination")
	}
	if !gitVersion.atLeast(minimumGitConfigEnvVersion) {
		return ErrRepositorySetup.Detail("Git 2.37 or newer is required to pin HTTPS destinations")
	}
	return nil
}

// parseGitHTTPSCapabilities extracts the Git version from git build output.
func parseGitHTTPSCapabilities(buildOutput string) (gitVersion, bool) {
	for _, line := range strings.Split(buildOutput, "\n") {
		if version, ok := parseGitVersionLine(line, "git version "); ok {
			return version, true
		}
	}
	return gitVersion{}, false
}

// parseGitVersionLine parses a dotted tool version from a named git build-output line.
func parseGitVersionLine(line, prefix string) (gitVersion, bool) {
	if !strings.HasPrefix(line, prefix) {
		return gitVersion{}, false
	}
	version := strings.Fields(strings.TrimPrefix(line, prefix))
	if len(version) == 0 {
		return gitVersion{}, false
	}
	return parseVersion(version[0])
}

func parseVersion(value string) (gitVersion, bool) {
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return gitVersion{}, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return gitVersion{}, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return gitVersion{}, false
	}
	patch := 0
	if len(parts) >= 3 {
		patch, err = strconv.Atoi(parts[2])
		if err != nil {
			return gitVersion{}, false
		}
	}
	return gitVersion{major: major, minor: minor, patch: patch}, true
}

type gitConfigEntry struct {
	key   string
	value string
}

const (
	gitCredentialUsernameEnv = "BRAIDPLANE_GIT_USERNAME"
	gitCredentialPasswordEnv = "BRAIDPLANE_GIT_PASSWORD"
	gitCredentialHelper      = `!f() { if test "$1" = get; then printf '%s\n' "username=$BRAIDPLANE_GIT_USERNAME" "password=$BRAIDPLANE_GIT_PASSWORD"; fi; }; f`
)

// HTTPSAuthEnv configures Git HTTPS credentials without executable temporary files or secrets in
// repository URLs, Git configuration values, or process arguments.
func HTTPSAuthEnv(env []string, username, password string) []string {
	env = withGitEnvOverrides(
		env,
		"GIT_TERMINAL_PROMPT=0",
		gitCredentialUsernameEnv+"="+username,
		gitCredentialPasswordEnv+"="+password,
	)
	return withGitConfig(env, gitConfigEntry{key: "credential.helper", value: gitCredentialHelper})
}

// withGitConfig appends isolated Git config environment entries after inherited config is removed.
func withGitConfig(env []string, entries ...gitConfigEntry) []string {
	overrides := make([]string, 0, 1+len(entries)*2)
	overrides = append(overrides, "GIT_CONFIG_COUNT="+strconv.Itoa(len(entries)))
	for index, entry := range entries {
		overrides = append(overrides,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", index, entry.key),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", index, entry.value),
		)
	}
	return withGitEnvOverrides(env, overrides...)
}

// withoutGitConfigEnv removes caller-supplied configuration so URL rewrite rules cannot replace
// the reviewed remote with a different effective destination inside the Git subprocess.
func withoutGitConfigEnv(env []string) []string {
	result := make([]string, 0, len(env))
	for _, value := range env {
		key := envKey(value)
		if key == "GIT_CONFIG_COUNT" || strings.HasPrefix(key, "GIT_CONFIG_KEY_") ||
			strings.HasPrefix(key, "GIT_CONFIG_VALUE_") || key == "GIT_CONFIG_PARAMETERS" {
			continue
		}
		result = append(result, value)
	}
	return result
}

// withoutProxyEnv removes caller-supplied proxy endpoints before empty overrides prevent the
// inherited process environment from routing native Git through an unvetted proxy.
func withoutProxyEnv(env []string) []string {
	proxyKeys := map[string]struct{}{
		"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "ALL_PROXY": {},
		"http_proxy": {}, "https_proxy": {}, "all_proxy": {},
	}

	result := make([]string, 0, len(env))
	for _, value := range env {
		if _, ok := proxyKeys[envKey(value)]; ok {
			continue
		}
		result = append(result, value)
	}
	return result
}

// shellQuote returns a POSIX-shell-safe token for GIT_SSH_COMMAND option values.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
