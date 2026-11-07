package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExplicitPATOverridesEnvironmentAPIKey(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"slug":"acme","name":"Acme"}`))
	}))
	defer server.Close()
	t.Setenv("BRAIDPLANE_API_KEY", "fixture-machine-key")
	t.Setenv("BRAIDPLANE_PAT", "")
	var stdout, stderr bytes.Buffer
	err := runWithRuntime([]string{"braidplane", "organization", "get", "--server-url", server.URL, "--org", "acme", "--pat", "fixture-user-pat"}, &stdout, &stderr, cliRuntime{configPath: filepath.Join(t.TempDir(), "config")})
	require.NoError(t, err)
	require.Equal(t, "Bearer fixture-user-pat", authorization)
}

func TestCredentialSelectionRejectsAmbiguityBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"slug":"acme","name":"Acme"}`))
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, apiKey, pat string
		args              []string
	}{
		{"both environment credentials", "fixture-key", "fixture-pat", nil},
		{"both explicit credentials", "", "", []string{"--api-key", "fixture-key", "--pat", "fixture-pat"}},
		{"empty explicit PAT", "fixture-key", "", []string{"--pat="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BRAIDPLANE_API_KEY", tc.apiKey)
			t.Setenv("BRAIDPLANE_PAT", tc.pat)
			args := append([]string{"braidplane", "organization", "get", "--server-url", server.URL, "--org", "acme"}, tc.args...)
			var stdout, stderr bytes.Buffer
			err := runWithRuntime(args, &stdout, &stderr, cliRuntime{configPath: filepath.Join(t.TempDir(), "config")})
			require.Error(t, err)
			require.Empty(t, stdout.String())
			require.NotContains(t, err.Error(), "fixture-key")
			require.NotContains(t, err.Error(), "fixture-pat")
		})
	}
	require.Zero(t, requests)
}

func TestExplicitAPIKeyOverridesEnvironmentPAT(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"slug":"acme","name":"Acme"}`))
	}))
	defer server.Close()
	t.Setenv("BRAIDPLANE_API_KEY", "")
	t.Setenv("BRAIDPLANE_PAT", "fixture-user-pat")
	var stdout, stderr bytes.Buffer
	err := runWithRuntime([]string{"braidplane", "organization", "get", "--server-url", server.URL, "--org", "acme", "--api-key", "fixture-machine-key"}, &stdout, &stderr, cliRuntime{configPath: filepath.Join(t.TempDir(), "config")})
	require.NoError(t, err)
	require.Equal(t, "Bearer fixture-machine-key", authorization)
}

func TestPATOnlyPipelineCommandsRejectExplicitAPIKey(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	t.Setenv("BRAIDPLANE_SERVER_URL", server.URL)
	t.Setenv("BRAIDPLANE_ORG", "acme")
	t.Setenv("BRAIDPLANE_PROJECT", "etl")
	t.Setenv("BRAIDPLANE_ENVIRONMENT", "dev")
	t.Setenv("BRAIDPLANE_PAT", "fixture-user-pat")
	t.Setenv("BRAIDPLANE_API_KEY", "")
	for _, command := range [][]string{
		{"pipeline", "draft", "get", "orders"},
		{"pipeline", "creation", "retry", "proposal"},
		{"pipeline", "validation", "revalidate", "orders"},
	} {
		for _, keyArgs := range [][]string{{"--api-key", "fixture-machine-key"}, {"--api-key=fixture-machine-key"}, {"--api-key", "fixture-machine-key", "--pat", "fixture-explicit-pat"}} {
			args := append([]string{"braidplane"}, command...)
			args = append(args, keyArgs...)
			var stdout, stderr bytes.Buffer
			err := runWithRuntime(args, &stdout, &stderr, cliRuntime{configPath: filepath.Join(t.TempDir(), "config")})
			require.Error(t, err, "%v", args[:len(command)+1])
			require.Contains(t, stderr.String(), "unknown flag --api-key")
			require.Empty(t, stdout.String())
			require.NotContains(t, stderr.String(), "fixture-machine-key")
			require.NotContains(t, stderr.String(), "fixture-explicit-pat")
			require.NotContains(t, err.Error(), "fixture-machine-key")
			require.NotContains(t, err.Error(), "fixture-user-pat")
			require.NotContains(t, err.Error(), "fixture-explicit-pat")
		}
	}
	require.Zero(t, requests)
}

func TestPATOnlyPipelineHelpNamesRequiredCredential(t *testing.T) {
	for _, command := range [][]string{
		{"pipeline", "create"},
		{"pipeline", "creation", "retry"},
		{"pipeline", "validation", "revalidate"},
		{"pipeline", "draft", "get"},
		{"pipeline", "draft", "save"},
		{"pipeline", "draft", "publish"},
		{"pipeline", "draft", "discard"},
		{"pipeline", "draft", "refresh-proposal"},
	} {
		args := append([]string{"braidplane"}, command...)
		args = append(args, "--help")
		var stdout, stderr bytes.Buffer
		err := runWithRuntime(args, &stdout, &stderr, cliRuntime{configPath: filepath.Join(t.TempDir(), "config")})
		require.NoError(t, err, "%v", command)
		require.Contains(t, strings.Join(strings.Fields(stdout.String()), " "), "PAT required", "%v", command)
		require.Contains(t, stdout.String(), "--pat", "%v", command)
		require.NotContains(t, stdout.String(), "--api-key", "%v", command)
	}
}

func TestExplicitPATOverridesSavedAPIKey(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"slug":"acme","name":"Acme"}`))
	}))
	defer server.Close()
	for _, key := range []string{"BRAIDPLANE_SERVER_URL", "BRAIDPLANE_ORG", "BRAIDPLANE_API_KEY", "BRAIDPLANE_PAT"} {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, saveLogin(path, cliAuthConfig{ServerURL: server.URL, Organization: "acme", APIKey: "fixture-saved-key"}))
	var stdout, stderr bytes.Buffer
	err := runWithRuntime([]string{"braidplane", "organization", "get", "--pat", "fixture-user-pat"}, &stdout, &stderr, cliRuntime{configPath: path})
	require.NoError(t, err)
	require.Equal(t, "Bearer fixture-user-pat", authorization)
}

func TestEnvironmentCredentialOverridesSavedOtherType(t *testing.T) {
	for _, tc := range []struct {
		name, envKey, envToken, savedKey, savedPAT string
	}{
		{"PAT over saved API key", "BRAIDPLANE_PAT", "fixture-env-pat", "fixture-saved-key", ""},
		{"API key over saved PAT", "BRAIDPLANE_API_KEY", "fixture-env-key", "", "fixture-saved-pat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var authorization string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authorization = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"slug":"acme","name":"Acme"}`))
			}))
			defer server.Close()
			for _, key := range []string{"BRAIDPLANE_SERVER_URL", "BRAIDPLANE_ORG", "BRAIDPLANE_API_KEY", "BRAIDPLANE_PAT"} {
				t.Setenv(key, "")
			}
			t.Setenv(tc.envKey, tc.envToken)
			path := filepath.Join(t.TempDir(), "config")
			require.NoError(t, saveLogin(path, cliAuthConfig{ServerURL: server.URL, Organization: "acme", APIKey: tc.savedKey, PAT: tc.savedPAT}))
			var stdout, stderr bytes.Buffer
			err := runWithRuntime([]string{"braidplane", "organization", "get"}, &stdout, &stderr, cliRuntime{configPath: path})
			require.NoError(t, err)
			require.Equal(t, "Bearer "+tc.envToken, authorization)
		})
	}
}

func TestAmbiguousStoredCredentialsAreRejected(t *testing.T) {
	for _, key := range []string{"BRAIDPLANE_SERVER_URL", "BRAIDPLANE_ORG", "BRAIDPLANE_API_KEY", "BRAIDPLANE_PAT"} {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, saveLoginMetadata(path, cliAuthConfig{ServerURL: "https://api.example.test", Organization: "acme"}))
	store := newMemoryCredentialStore()
	require.NoError(t, store.Set(credentialTypeAPIKey, "acme", "https://api.example.test", "fixture-key"))
	require.NoError(t, store.Set(credentialTypePAT, "acme", "https://api.example.test", "fixture-pat"))
	_, err := (cliRuntime{configPath: path, credentialStore: store}).resolveAuthConfig(cliAuthConfig{})
	require.ErrorContains(t, err, "select one explicitly")
	require.NotContains(t, err.Error(), "fixture-key")
	require.NotContains(t, err.Error(), "fixture-pat")
}

func TestEmptyExplicitCredentialDoesNotUseSavedToken(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"slug":"acme","name":"Acme"}`))
	}))
	defer server.Close()
	for _, key := range []string{"BRAIDPLANE_SERVER_URL", "BRAIDPLANE_ORG", "BRAIDPLANE_API_KEY", "BRAIDPLANE_PAT"} {
		t.Setenv(key, "")
	}
	for _, tc := range []struct {
		flag string
		cfg  cliAuthConfig
	}{
		{"--pat=", cliAuthConfig{ServerURL: server.URL, Organization: "acme", PAT: "fixture-saved-pat"}},
		{"--api-key=", cliAuthConfig{ServerURL: server.URL, Organization: "acme", APIKey: "fixture-saved-key"}},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			require.NoError(t, saveLogin(path, tc.cfg))
			var stdout, stderr bytes.Buffer
			err := runWithRuntime([]string{"braidplane", "organization", "get", tc.flag}, &stdout, &stderr, cliRuntime{configPath: path})
			require.Error(t, err)
			require.Empty(t, stdout.String())
		})
	}
	require.Zero(t, requests)
}

func TestSavedTokenIsNotSentToDifferentServer(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"slug":"acme","name":"Acme"}`))
	}))
	defer server.Close()
	for _, key := range []string{"BRAIDPLANE_SERVER_URL", "BRAIDPLANE_ORG", "BRAIDPLANE_PROJECT", "BRAIDPLANE_API_KEY", "BRAIDPLANE_PAT"} {
		t.Setenv(key, "")
	}
	for _, tc := range []struct {
		name    string
		command []string
		apiKey  string
		pat     string
	}{
		{"general API key", []string{"organization", "get", "--org", "acme"}, "fixture-saved-key", ""},
		{"general PAT", []string{"organization", "get", "--org", "acme"}, "", "fixture-saved-pat"},
		{"pipeline PAT", []string{"pipeline", "draft", "get", "orders", "--org", "acme", "--project", "etl"}, "", "fixture-saved-pat"},
		{"account PAT", []string{"user", "profile", "get"}, "", "fixture-saved-pat"},
		{"key management", []string{"api-key", "list", "--org", "acme"}, "fixture-saved-key", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{"flag", "environment"} {
				t.Run(mode, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "config")
					require.NoError(t, saveLogin(path, cliAuthConfig{ServerURL: "https://previous.example", Organization: "acme", APIKey: tc.apiKey, PAT: tc.pat}))
					args := append([]string{"braidplane"}, tc.command...)
					if mode == "flag" {
						args = append(args, "--server-url", server.URL)
					} else {
						t.Setenv("BRAIDPLANE_SERVER_URL", server.URL)
					}
					var stdout, stderr bytes.Buffer
					err := runWithRuntime(args, &stdout, &stderr, cliRuntime{configPath: path, credentialStore: newMemoryCredentialStore()})
					require.Error(t, err)
					require.Empty(t, stdout.String())
					require.NotContains(t, err.Error(), "fixture-saved-key")
					require.NotContains(t, err.Error(), "fixture-saved-pat")
				})
			}
		})
	}
	require.Zero(t, requests)
}

func TestSavedAPIKeyIsNotUsedForAnotherOrganization(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"slug":"beta","name":"Beta","api_keys":[],"has_more":false}`))
	}))
	defer server.Close()
	for _, key := range []string{"BRAIDPLANE_SERVER_URL", "BRAIDPLANE_ORG", "BRAIDPLANE_API_KEY", "BRAIDPLANE_PAT"} {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, saveLogin(path, cliAuthConfig{ServerURL: server.URL, Organization: "acme", APIKey: "fixture-acme-key"}))
	for _, command := range [][]string{{"organization", "get"}, {"api-key", "list"}} {
		for _, mode := range []string{"flag", "environment"} {
			t.Run(strings.Join(command, "_")+"/"+mode, func(t *testing.T) {
				args := append([]string{"braidplane"}, command...)
				if mode == "flag" {
					args = append(args, "--org", "beta")
				} else {
					t.Setenv("BRAIDPLANE_ORG", "beta")
				}
				var stdout, stderr bytes.Buffer
				err := runWithRuntime(args, &stdout, &stderr, cliRuntime{configPath: path, credentialStore: newMemoryCredentialStore()})
				require.Error(t, err)
				require.Empty(t, stdout.String())
				require.NotContains(t, err.Error(), "fixture-acme-key")
			})
		}
	}
	require.Zero(t, requests)
}

func TestSavedPATCanBeUsedInAnotherOrganization(t *testing.T) {
	var authorization, requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization, requestPath = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"slug":"beta","name":"Beta"}`))
	}))
	defer server.Close()
	for _, key := range []string{"BRAIDPLANE_SERVER_URL", "BRAIDPLANE_ORG", "BRAIDPLANE_API_KEY", "BRAIDPLANE_PAT"} {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, saveLogin(path, cliAuthConfig{ServerURL: server.URL, Organization: "acme", PAT: "fixture-user-pat"}))
	var stdout, stderr bytes.Buffer
	err := runWithRuntime([]string{"braidplane", "organization", "get", "--org", "beta"}, &stdout, &stderr, cliRuntime{configPath: path})
	require.NoError(t, err)
	require.Equal(t, "Bearer fixture-user-pat", authorization)
	require.Equal(t, "/organizations/beta", requestPath)
}

func TestDifferentOrganizationCanUseItsOwnStoredCredentials(t *testing.T) {
	for _, key := range []string{"BRAIDPLANE_SERVER_URL", "BRAIDPLANE_ORG", "BRAIDPLANE_API_KEY", "BRAIDPLANE_PAT"} {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, saveLogin(path, cliAuthConfig{ServerURL: "https://api.example.test", Organization: "acme", APIKey: "old-key"}))
	store := newMemoryCredentialStore()
	require.NoError(t, store.Set(credentialTypeAPIKey, "beta", "https://api.example.test", "beta-key"))
	runtime := cliRuntime{configPath: path, credentialStore: store}
	cfg, err := runtime.resolveAuthConfig(cliAuthConfig{Organization: "beta"})
	require.NoError(t, err)
	require.Equal(t, "beta-key", cfg.APIKey)
	cfg, err = (apiKeyManagementAuthFlags{Organization: "beta"}).resolveAuthConfig(&runtime)
	require.NoError(t, err)
	require.Equal(t, "beta-key", cfg.APIKey)
	require.NoError(t, store.Delete(credentialTypeAPIKey, "beta", "https://api.example.test"))
	require.NoError(t, store.Set(credentialTypePAT, "beta", "https://api.example.test", "beta-pat"))
	cfg, err = runtime.resolvePATAuthConfig(cliAuthConfig{Organization: "beta"})
	require.NoError(t, err)
	require.Equal(t, "beta-pat", cfg.PAT)
}

func TestSavedTokenScopeAllowsMatchingServerAndExplicitCredentials(t *testing.T) {
	saved := cliAuthConfig{ServerURL: "https://saved.example/api", Organization: "acme", APIKey: "saved-key", PAT: "saved-pat"}
	for _, tc := range []struct {
		name   string
		flags  cliAuthConfig
		apiKey string
		pat    string
	}{
		{"same server", cliAuthConfig{ServerURL: "https://saved.example/api/"}, "saved-key", "saved-pat"},
		{"different organization", cliAuthConfig{Organization: "beta"}, "", "saved-pat"},
		{"different path", cliAuthConfig{ServerURL: "https://saved.example/other"}, "", ""},
		{"different scheme", cliAuthConfig{ServerURL: "http://saved.example/api"}, "", ""},
		{"different server", cliAuthConfig{ServerURL: "https://other.example/api"}, "", ""},
		{"explicit credential", cliAuthConfig{ServerURL: "https://other.example/api", PAT: "explicit-pat"}, "", "explicit-pat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mergeCLIAuthConfig(cliAuthSources{Flags: tc.flags, Saved: saved})
			require.Equal(t, tc.apiKey, cfg.APIKey)
			require.Equal(t, tc.pat, cfg.PAT)
		})
	}
}

func TestDifferentServerCanUseItsOwnStoredPAT(t *testing.T) {
	for _, key := range []string{"BRAIDPLANE_SERVER_URL", "BRAIDPLANE_ORG", "BRAIDPLANE_API_KEY", "BRAIDPLANE_PAT"} {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, saveLogin(path, cliAuthConfig{ServerURL: "https://old.example", Organization: "acme", PAT: "old-pat"}))
	store := newMemoryCredentialStore()
	require.NoError(t, store.Set(credentialTypePAT, "acme", "https://new.example", "new-pat"))
	require.NoError(t, store.Set(credentialTypeAPIKey, "acme", "https://new.example", "new-key"))
	runtime := cliRuntime{configPath: path, credentialStore: store}
	for _, resolve := range []func(cliAuthConfig) (cliAuthConfig, error){runtime.resolvePATAuthConfig, runtime.resolveAccountPATAuthConfig} {
		cfg, err := resolve(cliAuthConfig{ServerURL: "https://new.example"})
		require.NoError(t, err)
		require.Equal(t, "new-pat", cfg.PAT)
	}
	cfg, err := (apiKeyManagementAuthFlags{ServerURL: "https://new.example"}).resolveAuthConfig(&runtime)
	require.NoError(t, err)
	require.Equal(t, "new-key", cfg.APIKey)
}

func TestAPIKeyManagementDoesNotUseStaleStoredKeyAfterInlinePATLogin(t *testing.T) {
	requests := 0
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"api_keys":[],"has_more":false}`))
	}))
	defer server.Close()
	for _, key := range []string{"BRAIDPLANE_SERVER_URL", "BRAIDPLANE_ORG", "BRAIDPLANE_API_KEY", "BRAIDPLANE_PAT"} {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, saveLogin(path, cliAuthConfig{ServerURL: server.URL, Organization: "acme", PAT: "fixture-current-pat"}))
	store := newMemoryCredentialStore()
	require.NoError(t, store.Set(credentialTypeAPIKey, "acme", server.URL, "fixture-stale-key"))
	var stdout, stderr bytes.Buffer
	err := runWithRuntime([]string{"braidplane", "api-key", "list"}, &stdout, &stderr, cliRuntime{configPath: path, credentialStore: store})
	require.ErrorContains(t, err, "management-enabled API key")
	require.Empty(t, stdout.String())
	require.NotContains(t, err.Error(), "fixture-current-pat")
	require.NotContains(t, err.Error(), "fixture-stale-key")
	require.Zero(t, requests)

	for _, mode := range []string{"flag", "environment"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"braidplane", "api-key", "list"}
			if mode == "flag" {
				args = append(args, "--api-key", "fixture-selected-key")
			} else {
				t.Setenv("BRAIDPLANE_API_KEY", "fixture-selected-key")
			}
			var stdout, stderr bytes.Buffer
			err := runWithRuntime(args, &stdout, &stderr, cliRuntime{configPath: path, credentialStore: store})
			require.NoError(t, err)
			require.Equal(t, "Bearer fixture-selected-key", authorization)
		})
	}
}

func TestLogoutDoesNotReactivateOlderStoredCredential(t *testing.T) {
	requests := 0
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"slug":"acme","name":"Acme","api_keys":[],"has_more":false}`))
	}))
	defer server.Close()
	for _, key := range []string{"BRAIDPLANE_SERVER_URL", "BRAIDPLANE_ORG", "BRAIDPLANE_PROJECT", "BRAIDPLANE_API_KEY", "BRAIDPLANE_PAT"} {
		t.Setenv(key, "")
	}
	for _, tc := range []struct {
		name       string
		command    []string
		inlinePAT  string
		inlineKey  string
		storedType string
	}{
		{"general", []string{"organization", "get"}, "fixture-inline-pat", "", credentialTypeAPIKey},
		{"key management", []string{"api-key", "list"}, "fixture-inline-pat", "", credentialTypeAPIKey},
		{"pipeline", []string{"pipeline", "draft", "get", "orders", "--project", "etl"}, "", "fixture-inline-key", credentialTypePAT},
		{"account", []string{"user", "profile", "get"}, "", "fixture-inline-key", credentialTypePAT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			store := newMemoryCredentialStore()
			require.NoError(t, saveLogin(path, cliAuthConfig{ServerURL: server.URL, Organization: "acme", APIKey: tc.inlineKey, PAT: tc.inlinePAT}))
			require.NoError(t, store.Set(tc.storedType, "acme", server.URL, "fixture-older-token"))
			runtime := cliRuntime{configPath: path, credentialStore: store}
			var logoutOut, logoutErr bytes.Buffer
			require.NoError(t, runWithRuntime([]string{"braidplane", "logout"}, &logoutOut, &logoutErr, runtime))
			t.Setenv("BRAIDPLANE_SERVER_URL", server.URL)
			t.Setenv("BRAIDPLANE_ORG", "acme")
			args := append([]string{"braidplane"}, tc.command...)
			var stdout, stderr bytes.Buffer
			err := runWithRuntime(args, &stdout, &stderr, runtime)
			require.Error(t, err)
			require.Empty(t, stdout.String())
			require.NotContains(t, err.Error(), "fixture-older-token")
		})
	}
	require.Zero(t, requests)
	t.Setenv("BRAIDPLANE_SERVER_URL", server.URL)
	t.Setenv("BRAIDPLANE_ORG", "acme")
	path := filepath.Join(t.TempDir(), "missing-config")
	for _, mode := range []string{"flag", "environment"} {
		t.Run("explicit_"+mode, func(t *testing.T) {
			args := []string{"braidplane", "organization", "get"}
			if mode == "flag" {
				args = append(args, "--api-key", "fixture-new-token")
			} else {
				t.Setenv("BRAIDPLANE_API_KEY", "fixture-new-token")
			}
			var stdout, stderr bytes.Buffer
			err := runWithRuntime(args, &stdout, &stderr, cliRuntime{configPath: path, credentialStore: newMemoryCredentialStore()})
			require.NoError(t, err)
			require.Equal(t, "Bearer fixture-new-token", authorization)
		})
	}
}

func TestLoginExplicitCredentialOverridesOtherEnvironmentType(t *testing.T) {
	for _, tc := range []struct {
		name, envKey, envValue, flag, token, wantType string
	}{
		{"API key", "BRAIDPLANE_PAT", "fixture-env-pat", "--api-key", "fixture-key", credentialTypeAPIKey},
		{"PAT", "BRAIDPLANE_API_KEY", "fixture-env-key", "--pat", "fixture-pat", credentialTypePAT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var authorization string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authorization = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"slug":"acme","name":"Acme"}`))
			}))
			defer server.Close()
			t.Setenv("BRAIDPLANE_API_KEY", "")
			t.Setenv("BRAIDPLANE_PAT", "")
			t.Setenv(tc.envKey, tc.envValue)
			store := newMemoryCredentialStore()
			var stdout, stderr bytes.Buffer
			err := runWithRuntime([]string{"braidplane", "login", "--server-url", server.URL, "--org", "acme", tc.flag, tc.token}, &stdout, &stderr, cliRuntime{configPath: filepath.Join(t.TempDir(), "config"), credentialStore: store})
			require.NoError(t, err)
			require.Equal(t, "Bearer "+tc.token, authorization)
			stored, err := store.Get(tc.wantType, "acme", server.URL)
			require.NoError(t, err)
			require.Equal(t, tc.token, stored)
			require.NotContains(t, stdout.String(), tc.token)
		})
	}
}

func TestLoginRejectsCredentialsInServerURLBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"slug":"acme","name":"Acme"}`))
	}))
	defer server.Close()
	for _, key := range []string{"BRAIDPLANE_SERVER_URL", "BRAIDPLANE_ORG", "BRAIDPLANE_API_KEY", "BRAIDPLANE_PAT"} {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), "config")
	serverURL := strings.Replace(server.URL, "http://", "http://fixture-user:fixture-secret@", 1)
	var stdout, stderr bytes.Buffer
	err := runWithRuntime([]string{"braidplane", "login", "--server-url", serverURL, "--org", "acme", "--pat", "fixture-pat"}, &stdout, &stderr, cliRuntime{configPath: path, credentialStore: newMemoryCredentialStore()})
	require.Error(t, err)
	require.Empty(t, stdout.String())
	require.NotContains(t, stderr.String(), "fixture-secret")
	require.NotContains(t, err.Error(), "fixture-secret")
	require.Zero(t, requests)
	_, statErr := os.Stat(path)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestSaveLoginWithCredentialStoreWritesMetadataOnlyAndRoundTripsSecret(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	store := newMemoryCredentialStore()
	want := cliAuthConfig{
		ServerURL:    "http://127.0.0.1:8080",
		Organization: "acme",
		APIKey:       "xto_test_secret",
	}

	require.NoError(t, saveLoginWithCredentialStore(path, store, want))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), "xto_test_secret")
	require.NotContains(t, string(data), "api_key")
	require.NotContains(t, string(data), "pat")

	got, err := loadLoginWithCredentialStore(path, store)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestSaveLoginWithCredentialStoreReplacesPreviousCredentialType(t *testing.T) {
	t.Parallel()

	t.Run("PATRemovesPreviousAPIKey", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.toml")
		store := newMemoryCredentialStore()
		require.NoError(t, store.Set(credentialTypeAPIKey, "acme", "https://api.example.test", "xto_stale_api_key"))

		require.NoError(t, saveLoginWithCredentialStore(path, store, cliAuthConfig{
			ServerURL:    "https://api.example.test",
			Organization: "acme",
			PAT:          "xto_pat_current",
		}))

		_, err := store.Get(credentialTypeAPIKey, "acme", "https://api.example.test")
		require.ErrorIs(t, err, errMemoryCredentialNotFound)
		got, err := store.Get(credentialTypePAT, "acme", "https://api.example.test")
		require.NoError(t, err)
		require.Equal(t, "xto_pat_current", got)
	})

	t.Run("APIKeyRemovesPreviousPAT", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.toml")
		store := newMemoryCredentialStore()
		require.NoError(t, store.Set(credentialTypePAT, "acme", "https://api.example.test", "xto_pat_stale"))

		require.NoError(t, saveLoginWithCredentialStore(path, store, cliAuthConfig{
			ServerURL:    "https://api.example.test",
			Organization: "acme",
			APIKey:       "xto_current_api_key",
		}))

		_, err := store.Get(credentialTypePAT, "acme", "https://api.example.test")
		require.ErrorIs(t, err, errMemoryCredentialNotFound)
		got, err := store.Get(credentialTypeAPIKey, "acme", "https://api.example.test")
		require.NoError(t, err)
		require.Equal(t, "xto_current_api_key", got)
	})
}

func TestLoadLoginWithCredentialStoreReportsMissingCredential(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	store := newMemoryCredentialStore()
	require.NoError(t, saveLoginWithCredentialStore(path, store, cliAuthConfig{
		ServerURL:    "https://api.example.test",
		Organization: "acme",
		APIKey:       "xto_test_secret",
	}))
	require.NoError(t, store.Delete(credentialTypeAPIKey, "acme", "https://api.example.test"))

	_, err := loadLoginWithCredentialStore(path, store)

	require.Error(t, err)
	require.ErrorContains(t, err, "stored CLI token")
	require.ErrorContains(t, err, "acme")
}

func TestDeleteLoginWithCredentialStoreRemovesConfigAndCredential(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	store := newMemoryCredentialStore()
	require.NoError(t, saveLoginWithCredentialStore(path, store, cliAuthConfig{
		ServerURL:    "https://api.example.test",
		Organization: "acme",
		APIKey:       "xto_test_secret",
		PAT:          "xto_pat_test_secret",
	}))

	require.NoError(t, deleteLoginWithCredentialStore(path, store))

	_, statErr := os.Stat(path)
	require.ErrorIs(t, statErr, os.ErrNotExist)
	_, err := store.Get(credentialTypeAPIKey, "acme", "https://api.example.test")
	require.ErrorIs(t, err, errMemoryCredentialNotFound)
	_, err = store.Get(credentialTypePAT, "acme", "https://api.example.test")
	require.ErrorIs(t, err, errMemoryCredentialNotFound)
}

func TestDeleteLoginWithCredentialStoreSkipsCredentialWhenConfigContainsAPIKey(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	store := newMemoryCredentialStore()
	require.NoError(t, store.Set(credentialTypeAPIKey, "acme", "https://api.example.test", "xto_keyring_secret"))
	require.NoError(t, store.Set(credentialTypePAT, "acme", "https://api.example.test", "xto_pat_keyring_secret"))
	require.NoError(t, saveLogin(path, cliAuthConfig{
		ServerURL:    "https://api.example.test",
		Organization: "acme",
		APIKey:       "xto_plaintext_secret",
	}))

	require.NoError(t, deleteLoginWithCredentialStore(path, store))

	_, statErr := os.Stat(path)
	require.ErrorIs(t, statErr, os.ErrNotExist)
	got, err := store.Get(credentialTypeAPIKey, "acme", "https://api.example.test")
	require.NoError(t, err)
	require.Equal(t, "xto_keyring_secret", got)
	got, err = store.Get(credentialTypePAT, "acme", "https://api.example.test")
	require.NoError(t, err)
	require.Equal(t, "xto_pat_keyring_secret", got)
}

func TestDeleteLoginWithCredentialStoreSkipsCredentialDeleteFailureWhenConfigContainsAPIKey(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, saveLogin(path, cliAuthConfig{
		ServerURL:    "https://api.example.test",
		Organization: "acme",
		APIKey:       "xto_plaintext_secret",
	}))

	err := deleteLoginWithCredentialStore(path, failingDeleteCredentialStore{
		err: errMemoryCredentialDeleteFailed,
	})

	require.NoError(t, err)
	_, statErr := os.Stat(path)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestDeleteLoginWithCredentialStorePreservesMetadataWhenCredentialDeleteFails(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, saveLoginMetadata(path, cliAuthConfig{
		ServerURL:    "https://api.example.test",
		Organization: "acme",
	}))

	err := deleteLoginWithCredentialStore(path, failingDeleteCredentialStore{
		err: errMemoryCredentialDeleteFailed,
	})

	require.ErrorIs(t, err, errMemoryCredentialDeleteFailed)
	require.ErrorContains(t, err, "delete stored CLI API key")
	got, loadErr := loadLogin(path)
	require.NoError(t, loadErr)
	require.Equal(t, cliAuthConfig{
		ServerURL:    "https://api.example.test",
		Organization: "acme",
	}, got)
}

func TestResolveCLIAuthConfigPrecedence(t *testing.T) {
	t.Parallel()

	got, err := resolveCLIAuthConfig(cliAuthSources{
		Flags: cliAuthConfig{
			ServerURL: "https://flag.example",
		},
		Env: cliAuthConfig{
			ServerURL:    "https://env.example",
			Organization: "env-org",
			APIKey:       "env-token",
			PAT:          "env-pat",
		},
		Saved: cliAuthConfig{
			ServerURL:    "https://saved.example",
			Organization: "saved-org",
			APIKey:       "saved-token",
			PAT:          "saved-pat",
		},
	})

	require.NoError(t, err)
	require.Equal(t, cliAuthConfig{
		ServerURL:    "https://flag.example",
		Organization: "env-org",
		APIKey:       "env-token",
		PAT:          "env-pat",
	}, got)
}

func TestResolveCLIAuthConfigRequiresAPIFields(t *testing.T) {
	t.Parallel()

	_, err := resolveCLIAuthConfig(cliAuthSources{})

	require.Error(t, err)
	require.ErrorContains(t, err, "server URL")
	require.ErrorContains(t, err, "organization")
	require.ErrorContains(t, err, "API key or personal access token")
}

func TestResolvePATAuthConfigRequiresPATAndDropsAPIKey(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, saveLogin(path, cliAuthConfig{
		ServerURL:    "https://saved.example",
		Organization: "saved-org",
		APIKey:       "saved-api-key",
		PAT:          "saved-pat",
	}))

	got, err := apiAuthFlags{}.resolvePATAuthConfig(&cliRuntime{configPath: path})

	require.NoError(t, err)
	require.Equal(t, cliAuthConfig{
		ServerURL:    "https://saved.example",
		Organization: "saved-org",
		PAT:          "saved-pat",
	}, got)
}

func TestResolvePATAuthConfigLoadsSavedPATWhenAPIKeyEnvIsSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	store := newMemoryCredentialStore()
	require.NoError(t, saveLoginMetadata(path, cliAuthConfig{
		ServerURL:    "https://saved.example/api",
		Organization: "saved-org",
	}))
	require.NoError(t, store.Set(credentialTypePAT, "saved-org", "https://saved.example/api", "saved-pat"))
	t.Setenv("BRAIDPLANE_API_KEY", "env-api-key")

	got, err := apiAuthFlags{}.resolvePATAuthConfig(&cliRuntime{
		configPath:      path,
		credentialStore: store,
	})

	require.NoError(t, err)
	require.Equal(t, cliAuthConfig{
		ServerURL:    "https://saved.example/api",
		Organization: "saved-org",
		PAT:          "saved-pat",
	}, got)
}

func TestResolvePATAuthConfigRejectsAPIKeyOnlyConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, saveLogin(path, cliAuthConfig{
		ServerURL:    "https://saved.example",
		Organization: "saved-org",
		APIKey:       "saved-api-key",
	}))

	_, err := apiAuthFlags{}.resolvePATAuthConfig(&cliRuntime{configPath: path})

	require.Error(t, err)
	require.ErrorContains(t, err, "personal access token")
}

func TestRuntimeResolveAuthConfigUsesEnvAndSavedLogin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, saveLogin(path, cliAuthConfig{
		ServerURL:    "https://saved.example",
		Organization: "saved-org",
		PAT:          "saved-token",
	}))
	t.Setenv("BRAIDPLANE_ORG", "env-org")

	got, err := cliRuntime{configPath: path}.resolveAuthConfig(cliAuthConfig{})

	require.NoError(t, err)
	require.Equal(t, cliAuthConfig{
		ServerURL:    "https://saved.example",
		Organization: "env-org",
		PAT:          "saved-token",
	}, got)
}

func TestRuntimeResolveAuthConfigDoesNotReadCredentialStoreWhenAPIKeyComesFromFlags(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, saveLoginMetadata(path, cliAuthConfig{
		ServerURL:    "https://saved.example",
		Organization: "saved-org",
	}))

	got, err := cliRuntime{
		configPath:      path,
		credentialStore: newMemoryCredentialStore(),
	}.resolveAuthConfig(cliAuthConfig{APIKey: "flag-token"})

	require.NoError(t, err)
	require.Equal(t, cliAuthConfig{
		ServerURL:    "https://saved.example",
		Organization: "saved-org",
		APIKey:       "flag-token",
	}, got)
}

func TestRuntimeResolveAuthConfigDoesNotReadCredentialStoreWhenAPIKeyComesFromEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, saveLoginMetadata(path, cliAuthConfig{
		ServerURL:    "https://saved.example",
		Organization: "saved-org",
	}))
	t.Setenv("BRAIDPLANE_API_KEY", "env-token")

	got, err := cliRuntime{
		configPath:      path,
		credentialStore: newMemoryCredentialStore(),
	}.resolveAuthConfig(cliAuthConfig{})

	require.NoError(t, err)
	require.Equal(t, cliAuthConfig{
		ServerURL:    "https://saved.example",
		Organization: "saved-org",
		APIKey:       "env-token",
	}, got)
}

func TestRuntimeResolveAuthConfigNormalizesTrailingSlashBeforeCredentialStoreLookup(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	store := newMemoryCredentialStore()
	require.NoError(t, saveLoginMetadata(path, cliAuthConfig{
		ServerURL:    "https://saved.example.test",
		Organization: "saved-org",
	}))
	require.NoError(t, store.Set(credentialTypeAPIKey, "saved-org", "https://saved.example.test", "stored-token"))

	got, err := cliRuntime{
		configPath:      path,
		credentialStore: store,
	}.resolveAuthConfig(cliAuthConfig{ServerURL: "https://saved.example.test/"})

	require.NoError(t, err)
	require.Equal(t, cliAuthConfig{
		ServerURL:    "https://saved.example.test/",
		Organization: "saved-org",
		APIKey:       "stored-token",
	}, got)
}

func TestRuntimeResolveAuthConfigIgnoresMalformedSavedLoginWhenFlagsAreComplete(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("server_url = ["), 0o600))

	flags := cliAuthConfig{
		ServerURL:    "https://flag.example",
		Organization: "flag-org",
		APIKey:       "flag-token",
	}
	got, err := cliRuntime{configPath: path}.resolveAuthConfig(flags)

	require.NoError(t, err)
	require.Equal(t, flags, got)
}

func TestRuntimeResolveEnvironmentSlugPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, saveLogin(path, cliAuthConfig{Environment: "saved"}))

	t.Run("flag", func(t *testing.T) {
		t.Setenv("BRAIDPLANE_ENVIRONMENT", "env")

		got, err := cliRuntime{configPath: path}.resolveEnvironmentSlug("flag")

		require.NoError(t, err)
		require.Equal(t, "flag", got)
	})

	t.Run("env", func(t *testing.T) {
		t.Setenv("BRAIDPLANE_ENVIRONMENT", "env")

		got, err := cliRuntime{configPath: path}.resolveEnvironmentSlug("")

		require.NoError(t, err)
		require.Equal(t, "env", got)
	})

	t.Run("saved", func(t *testing.T) {
		t.Setenv("BRAIDPLANE_ENVIRONMENT", "")

		got, err := cliRuntime{configPath: path}.resolveEnvironmentSlug("")

		require.NoError(t, err)
		require.Equal(t, "saved", got)
	})
}

func TestRuntimeResolveEnvironmentSlugRequiresConfiguredEnvironment(t *testing.T) {
	t.Parallel()

	_, err := cliRuntime{configPath: filepath.Join(t.TempDir(), "missing.toml")}.resolveEnvironmentSlug("")

	require.Error(t, err)
	require.ErrorContains(t, err, "project environment is required")
	require.ErrorContains(t, err, "set --environment or BRAIDPLANE_ENVIRONMENT")
}

func TestBearerTransportInjectsAuthorizationHeader(t *testing.T) {
	t.Parallel()

	var gotAuth string
	transport := bearerTransport{
		token: "xto_test_secret",
		next: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			gotAuth = req.Header.Get("Authorization")
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Body:       http.NoBody,
				Request:    req,
			}, nil
		}),
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.example.test/pipelines", nil)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, "Bearer xto_test_secret", gotAuth)
}

func TestDefaultServerURLDoesNotReadRuntimeConfigEnv(t *testing.T) {
	t.Setenv("BACKEND_API_URL", "http://backend.example.test:49912")
	t.Setenv("SERVER_PORT", "49912")

	got := defaultServerURL()

	require.Equal(t, "http://localhost:3001/api", got)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

var (
	errMemoryCredentialNotFound     error = memoryCredentialNotFoundError{}
	errMemoryCredentialDeleteFailed error = memoryCredentialDeleteFailedError{}
)

type memoryCredentialNotFoundError struct{}

func (memoryCredentialNotFoundError) Error() string {
	return "credential not found"
}

type memoryCredentialDeleteFailedError struct{}

func (memoryCredentialDeleteFailedError) Error() string {
	return "credential delete failed"
}

type memoryCredentialStore struct {
	values map[string]string
}

func newMemoryCredentialStore() *memoryCredentialStore {
	return &memoryCredentialStore{values: make(map[string]string)}
}

func (s *memoryCredentialStore) Get(credentialType, organization, serverURL string) (string, error) {
	value, ok := s.values[memoryCredentialKey(credentialType, organization, serverURL)]
	if !ok {
		return "", errMemoryCredentialNotFound
	}

	return value, nil
}

func (s *memoryCredentialStore) Set(credentialType, organization, serverURL, token string) error {
	s.values[memoryCredentialKey(credentialType, organization, serverURL)] = token
	return nil
}

func (s *memoryCredentialStore) Delete(credentialType, organization, serverURL string) error {
	key := memoryCredentialKey(credentialType, organization, serverURL)
	delete(s.values, key)

	return nil
}

func memoryCredentialKey(credentialType, organization, serverURL string) string {
	return credentialType + "\x00" + organization + "\x00" + serverURL
}

type failingDeleteCredentialStore struct {
	err error
}

func (s failingDeleteCredentialStore) Get(_, _, _ string) (string, error) {
	return "", s.err
}

func (s failingDeleteCredentialStore) Set(_, _, _, _ string) error {
	return s.err
}

func (s failingDeleteCredentialStore) Delete(_, _, _ string) error {
	return s.err
}

func loadLoginWithCredentialStore(path string, store cliCredentialStore) (cliAuthConfig, error) {
	cfg, err := loadLogin(path)
	if err != nil {
		return cliAuthConfig{}, err
	}
	if cfg.ServerURL == "" && cfg.Organization == "" {
		return cfg, nil
	}
	if cfg.APIKey != "" || cfg.PAT != "" {
		return cfg, nil
	}

	cfg.APIKey, cfg.PAT, err = loadStoredCLITokens(store, cfg.Organization, cfg.ServerURL)
	if err != nil {
		return cliAuthConfig{}, err
	}

	return cfg, nil
}
