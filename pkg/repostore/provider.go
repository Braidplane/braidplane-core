package repostore

import (
	"net/url"
	"regexp"
	"strings"
)

// credentialURLPattern matches embedded HTTP(S) credentials in text
// (e.g. git error output containing "https://user:token@host/...").
var credentialURLPattern = regexp.MustCompile(`(https?://)[^:@/\s]+:[^@/\s]+@`)

// sanitizeGitOutput removes credential-bearing URLs from git command output
// before it is surfaced in error messages. Git sometimes echoes the remote URL
// including embedded credentials in error output
// (e.g. "fatal: unable to access 'https://user:token@host/repo'").
func sanitizeGitOutput(s string) string {
	return credentialURLPattern.ReplaceAllString(s, "$1[REDACTED]@")
}

// SanitizeGitErrorDetail removes credential-bearing URLs before Git errors reach operator logs.
func SanitizeGitErrorDetail(detail string) string {
	return sanitizeGitOutput(detail)
}

// Provider identifies a git hosting provider inferred from a repository URL.
type Provider string

const (
	ProviderGeneric Provider = "git"
	ProviderGitHub  Provider = "github"
	ProviderGitLab  Provider = "gitlab"
	githubHost      string   = "github.com"
	gitlabHost      string   = "gitlab.com"
)

// DetectProvider infers the provider from a repository URL.
// Unknown/self-hosted remotes are treated as generic git.
func DetectProvider(repoURL string) Provider {
	host := extractRepoHost(repoURL)
	switch host {
	case githubHost:
		return ProviderGitHub
	case gitlabHost:
		return ProviderGitLab
	default:
		return ProviderGeneric
	}
}

// NormalizeRemoteURL returns a stable repo identifier for provider-backed
// remotes when possible. For GitHub/GitLab, it normalizes SSH/HTTPS variants
// to "<host>/<owner>/<repo>" without the .git suffix. Unknown formats are
// returned unchanged (trimmed).
func NormalizeRemoteURL(repoURL string) string {
	raw := SanitizeRemoteURL(repoURL)
	if raw == "" {
		return ""
	}

	host, path, ok := extractRepoHostAndPath(raw)
	if !ok {
		return raw
	}

	host = strings.ToLower(host)
	if host != githubHost && host != gitlabHost {
		return raw
	}

	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return raw
	}
	path = strings.TrimSuffix(path, ".git")
	if path == "" {
		return raw
	}

	return host + "/" + path
}

// SanitizeRemoteURL removes any embedded userinfo from HTTP(S)/SSH URLs before
// they are logged, persisted, or surfaced in errors. SCP-style SSH remotes do
// not encode credentials in the URL and are returned unchanged.
func SanitizeRemoteURL(repoURL string) string {
	raw := strings.TrimSpace(repoURL)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	// Strip userinfo (credentials) if present, even for malformed URLs with empty hostname.
	// This prevents credential leakage in error messages or metadata.
	if parsed.User != nil {
		parsed.User = nil
		return parsed.String()
	}

	return raw
}

func extractRepoHost(repoURL string) string {
	raw := strings.TrimSpace(repoURL)
	if raw == "" {
		return ""
	}

	host, _, ok := extractRepoHostAndPath(raw)
	if !ok {
		return ""
	}

	return strings.ToLower(host)
}

func extractRepoHostAndPath(raw string) (string, string, bool) {
	// Persisted provider identities use NormalizeRemoteURL's host/path form.
	if host, path, ok := strings.Cut(raw, "/"); ok && path != "" &&
		(host == githubHost || host == gitlabHost) {
		return host, "/" + path, true
	}
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Host != "" {
		return parsed.Hostname(), parsed.Path, true
	}

	// SCP-like SSH syntax: git@github.com:owner/repo.git
	if at := strings.Index(raw, "@"); at >= 0 {
		rest := raw[at+1:]
		if colon := strings.Index(rest, ":"); colon > 0 {
			return rest[:colon], rest[colon+1:], true
		}
	}

	return "", "", false
}
