package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func oauthTestConnector(t *testing.T, server *httptest.Server, health string) *Connector {
	t.Helper()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = oldTransport }()
	connector, err := New(transportsecurity.WithPlaintext(t.Context()), Config{BaseURL: server.URL + "/v1", HealthPath: health, AllowPrivateHosts: true}, Credentials{BearerToken: "grant-token", OAuthDestination: server.URL + "/v1"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connector.Close()) })
	return connector
}

func TestOAuthDestinationAllowsBoundRequestsAndBlocksPathEscapes(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, "Bearer grant-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	connector := oauthTestConnector(t, server, "/health")
	require.NoError(t, connector.TestConnection(transportsecurity.WithPlaintext(t.Context())))
	result, err := connector.Execute(transportsecurity.WithPlaintext(t.Context()), []byte(`{"method":"GET","path":"/users"}`), nil)
	require.NoError(t, err)
	require.NoError(t, result.Error())
	require.Equal(t, 2, calls)
	for _, path := range []string{"/../other", "/%2e%2e/other", "/a%2fb", "/%252e%252e/other"} {
		operation, err := json.Marshal(map[string]string{"method": "GET", "path": path})
		require.NoError(t, err)
		result, err := connector.Execute(transportsecurity.WithPlaintext(t.Context()), operation, nil)
		require.NoError(t, err)
		require.ErrorIs(t, result.Error(), ErrInvalidConfig)
	}
	require.Equal(t, 2, calls)
	invalidHealth := oauthTestConnector(t, server, "/../other")
	require.ErrorIs(t, invalidHealth.TestConnection(transportsecurity.WithPlaintext(t.Context())), ErrInvalidConfig)
	require.Equal(t, 2, calls)
	_, err = New(transportsecurity.WithPlaintext(t.Context()), Config{BaseURL: server.URL + "/other", AllowPrivateHosts: true}, Credentials{BearerToken: "grant-token", OAuthDestination: server.URL + "/v1"})
	require.ErrorIs(t, err, ErrInvalidConfig)
	_, err = New(transportsecurity.WithPlaintext(t.Context()), Config{BaseURL: server.URL + "/v1", AllowPrivateHosts: true}, Credentials{BearerToken: "grant-token", Username: "basic-user", Password: "password", OAuthDestination: server.URL + "/v1"})
	require.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestOAuthDestinationRedirectPolicySurvivesExecutionLimits(t *testing.T) {
	escaped := 0
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escaped++
		t.Error("request escaped OAuth grant destination")
	}))
	defer other.Close()
	for _, target := range []string{other.URL + "/v1/users", "/other"} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		}))
		connector := oauthTestConnector(t, server, "/health")
		require.ErrorIs(t, connector.TestConnection(transportsecurity.WithPlaintext(t.Context())), ErrInvalidConfig)
		result, err := connector.Execute(transportsecurity.WithPlaintext(t.Context()), []byte(`{"method":"GET","path":"/users"}`), nil, types.ExecutionLimits{HTTPMaxRedirects: 4, HTTPMaxResponseBytes: 1024})
		require.NoError(t, err)
		require.ErrorIs(t, result.Error(), ErrInvalidConfig)
		server.Close()
	}
	require.Zero(t, escaped)
}

func TestOAuthDestinationAllowsSameOriginRedirectWithinBasePath(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, "Bearer grant-token", r.Header.Get("Authorization"))
		if r.URL.Path == "/v1/start" {
			http.Redirect(w, r, "/v1/end", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	connector := oauthTestConnector(t, server, "/start")
	result, err := connector.Execute(transportsecurity.WithPlaintext(t.Context()), []byte(`{"method":"GET","path":"/start"}`), nil, types.ExecutionLimits{HTTPMaxRedirects: 4, HTTPMaxResponseBytes: 1024})
	require.NoError(t, err)
	require.NoError(t, result.Error())
	require.Equal(t, 2, calls)
}

func TestOAuthRedirectLimitCanExceedConnectionTestDefault(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer grant-token", r.Header.Get("Authorization"))
		step, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/v1/"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if step < 7 {
			http.Redirect(w, r, fmt.Sprintf("/v1/%d", step+1), http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	connector := oauthTestConnector(t, server, "/0")
	result, err := connector.Execute(transportsecurity.WithPlaintext(t.Context()), []byte(`{"method":"GET","path":"/0"}`), nil, types.ExecutionLimits{HTTPMaxRedirects: 8, HTTPMaxResponseBytes: 1024})
	require.NoError(t, err)
	require.NoError(t, result.Error())
	require.ErrorIs(t, connector.TestConnection(transportsecurity.WithPlaintext(t.Context())), xerror.ExecutionRedirectLimitExceeded())
	result, err = connector.Execute(transportsecurity.WithPlaintext(t.Context()), []byte(`{"method":"GET","path":"/0"}`), nil, types.ExecutionLimits{HTTPMaxRedirects: 5, HTTPMaxResponseBytes: 1024})
	require.NoError(t, err)
	require.ErrorIs(t, result.Error(), xerror.ExecutionRedirectLimitExceeded())
}
