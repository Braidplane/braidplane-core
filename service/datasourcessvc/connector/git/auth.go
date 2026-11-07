package git

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// AuthEnv returns the git environment variables and a URL modifier needed to
// perform raw git operations (ls-remote, clone, push) with the given credentials.
// For HTTPS: Git's credential helper reads credentials from the child environment.
// For SSH: a temp key file is written and GIT_SSH_COMMAND is set.
// The returned cleanup func must be called (via defer) to remove temp files.
func AuthEnv(
	creds Credentials,
) (env []string, modifyURL func(string) (string, error), cleanup func(), err error) {
	return authEnv(creds)
}

// authEnv returns the git environment variables needed for the given credentials.
// For HTTPS: Git's credential helper and a URL stripped of userinfo.
// For SSH: a temp key file is written and GIT_SSH_COMMAND is set.
// The cleanup func must be called (via defer) to remove temp files.
func authEnv(
	creds Credentials,
) (env []string, repoURLWithCreds repoURLModifier, cleanup func(), err error) {
	cleanup = func() {}

	switch creds.AuthType {
	case AuthTypeHTTPS:
		username := strings.TrimSpace(creds.Username)
		if username == "" && strings.TrimSpace(creds.Token) != "" {
			return nil, nil, cleanup, xerror.InvalidCredentials().Detail("HTTPS auth requires username")
		}
		env = repostore.HTTPSAuthEnv(env, username, creds.Token)
		repoURLWithCreds = httpsURLModifier(username, creds.Token)
		return env, repoURLWithCreds, cleanup, nil

	case AuthTypeSSH:
		if creds.SSHPrivateKey == "" {
			return nil, nil, cleanup, xerror.Unexpected().Detail(
				"SSH auth requires ssh_private_key",
			)
		}
		if strings.TrimSpace(creds.SSHKnownHosts) == "" {
			return nil, nil, cleanup, xerror.Unexpected().Detail(
				"SSH auth requires ssh_known_hosts",
			)
		}

		tmpFile, writeErr := os.CreateTemp("", "braidplane-git-ssh-key-*")
		if writeErr != nil {
			return nil, nil, cleanup, xerror.Unexpected().With(writeErr)
		}

		cleanup = func() {
			_ = os.Remove(tmpFile.Name())
		}

		if _, writeErr := tmpFile.WriteString(creds.SSHPrivateKey); writeErr != nil {
			cleanup()
			return nil, nil, func() {}, xerror.Unexpected().With(writeErr)
		}
		if closeErr := tmpFile.Close(); closeErr != nil {
			cleanup()
			return nil, nil, func() {}, xerror.Unexpected().With(closeErr)
		}
		if chmodErr := os.Chmod(tmpFile.Name(), 0o600); chmodErr != nil {
			cleanup()
			return nil, nil, func() {}, xerror.Unexpected().With(chmodErr)
		}

		knownHostsFile, writeErr := os.CreateTemp("", "braidplane-git-known-hosts-*")
		if writeErr != nil {
			cleanup()
			return nil, nil, func() {}, xerror.Unexpected().With(writeErr)
		}
		prevCleanup := cleanup
		cleanup = func() {
			_ = os.Remove(knownHostsFile.Name())
			prevCleanup()
		}
		if _, writeErr := knownHostsFile.WriteString(creds.SSHKnownHosts); writeErr != nil {
			cleanup()
			return nil, nil, func() {}, xerror.Unexpected().With(writeErr)
		}
		if closeErr := knownHostsFile.Close(); closeErr != nil {
			cleanup()
			return nil, nil, func() {}, xerror.Unexpected().With(closeErr)
		}
		if chmodErr := os.Chmod(knownHostsFile.Name(), 0o600); chmodErr != nil {
			cleanup()
			return nil, nil, func() {}, xerror.Unexpected().With(chmodErr)
		}

		keyPath := filepath.ToSlash(tmpFile.Name())
		knownHostsPath := filepath.ToSlash(knownHostsFile.Name())
		sshCmd := fmt.Sprintf(
			"ssh -i %s -o StrictHostKeyChecking=yes -o UserKnownHostsFile=%s -o ConnectTimeout=10 -o BatchMode=yes -o IdentitiesOnly=yes",
			keyPath,
			knownHostsPath,
		)

		if creds.SSHPassphrase != "" {
			passTmp, passErr := os.CreateTemp("", "braidplane-git-ssh-pass-*")
			if passErr != nil {
				cleanup()
				return nil, nil, func() {}, xerror.Unexpected().With(passErr)
			}
			prevCleanup := cleanup
			cleanup = func() {
				_ = os.Remove(passTmp.Name())
				prevCleanup()
			}
			_ = passTmp.Close()
			// SSH_ASKPASS approach: write a tiny script that echoes the passphrase
			scriptPath := passTmp.Name() + ".sh"
			scriptContent := fmt.Sprintf(
				"#!/bin/sh\necho '%s'\n",
				escapeShellSingleQuote(creds.SSHPassphrase),
			)
			if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o700); err != nil {
				cleanup()
				return nil, nil, func() {}, xerror.Unexpected().With(err)
			}
			prevCleanup2 := cleanup
			cleanup = func() {
				_ = os.Remove(scriptPath)
				prevCleanup2()
			}
			env = append(env,
				"SSH_ASKPASS="+scriptPath,
				"SSH_ASKPASS_REQUIRE=force",
				"DISPLAY=:0",
			)
		}

		env = append(env, "GIT_SSH_COMMAND="+sshCmd)
		repoURLWithCreds = identityURLModifier()
		return env, repoURLWithCreds, cleanup, nil

	default:
		return nil, nil, cleanup, xerror.Unexpected().Detailf(
			"unsupported auth type %q: must be %q or %q",
			creds.AuthType,
			AuthTypeHTTPS,
			AuthTypeSSH,
		)
	}
}

// repoURLModifier normalizes the repository URL before it reaches a git subprocess.
type repoURLModifier func(repoURL string) (string, error)

// httpsURLModifier parses an HTTPS repository URL structurally and removes userinfo so
// credentials remain confined to the askpass helper instead of native git arguments.
func httpsURLModifier(username, token string) repoURLModifier {
	return func(repoURL string) (string, error) {
		_ = username
		_ = token

		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(repoURL)), "https://") {
			return repoURL, nil
		}
		parsed, err := url.Parse(strings.TrimSpace(repoURL))
		if err != nil {
			return "", xerror.ValidationFailed().With(err)
		}
		if parsed.Hostname() == "" {
			return repoURL, nil
		}
		parsed.User = nil
		return parsed.String(), nil
	}
}

// identityURLModifier returns the URL unchanged (for SSH which does not embed creds in URL).
func identityURLModifier() repoURLModifier {
	return func(repoURL string) (string, error) { return repoURL, nil }
}

// escapeShellSingleQuote escapes single quotes for embedding in a single-quoted shell string.
func escapeShellSingleQuote(s string) string {
	result := ""
	for _, c := range s {
		if c == '\'' {
			result += `'\''`
		} else {
			result += string(c)
		}
	}
	return result
}
