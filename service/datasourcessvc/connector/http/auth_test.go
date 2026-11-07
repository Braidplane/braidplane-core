package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const authSpec = `{"openapi":"3.0.3","info":{"title":"Fixture","version":"1"},"paths":{"/items":{"get":{"operationId":"items","responses":{"200":{"description":"OK"}}}}}}`

func TestAPIKeyHealthExecutionAndSameOriginSchema(t *testing.T) {
	for _, header := range []string{"X-Api-Key", "Authorization"} {
		t.Run(header, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				want := "synthetic-key"
				if header == "Authorization" {
					want = "ApiKey " + want
				}
				assert.Equal(t, want, r.Header.Get(header))
				calls++
				if r.URL.Path == "/openapi.json" {
					http.Redirect(w, r, "/spec.json", http.StatusFound)
					return
				}
				if r.URL.Path == "/spec.json" {
					_, _ = fmt.Fprint(w, authSpec)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ok":true}`)
			}))
			defer server.Close()
			connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, OpenAPIURL: server.URL + "/openapi.json", AllowPrivateHosts: true}, Credentials{APIKey: "synthetic-key", APIKeyHeader: header})
			require.NoError(t, err)
			require.NoError(t, connector.TestConnection(transportsecurity.WithPlaintext(context.Background())))
			execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), []byte(`{"method":"GET","path":"/items","headers":{"X-Api-Key":"authored-value","Authorization":"authored-value"}}`), nil)
			require.NoError(t, err)
			require.NoError(t, execution.Error())
			schema, err := connector.IntrospectSchema(transportsecurity.WithPlaintext(context.Background()))
			require.NoError(t, err)
			require.Len(t, schema.Operations, 1)
			require.Equal(t, 4, calls)
		})
	}
}

func TestHTTPAuthenticationAndSpecCredentialDestinations(t *testing.T) {
	for _, creds := range []Credentials{{SecretHeaders: map[string]string{"X-Secret": "synthetic-key"}}, {APIKey: "synthetic-key", APIKeyHeader: "X-Api-Key"}, {APIKey: "synthetic-key", APIKeyHeader: "Authorization"}, {BearerToken: "synthetic-key"}, {Username: "test", Password: "synthetic-key"}} {
		t.Run(fmt.Sprint(creds.APIKeyHeader, creds.BearerToken != "", creds.Username != ""), func(t *testing.T) {
			hits := 0
			unrelated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				assert.Empty(t, r.Header.Get("X-Api-Key"))
				assert.Empty(t, r.Header.Get("Authorization"))
				assert.Empty(t, r.Header.Get("X-Secret"))
				_, _ = fmt.Fprint(w, authSpec)
			}))
			defer unrelated.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, unrelated.URL+"/openapi.json", http.StatusFound)
			}))
			defer origin.Close()
			connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: origin.URL, OpenAPIURL: origin.URL + "/openapi.json", AllowPrivateHosts: true}, creds)
			require.NoError(t, err)
			_, err = connector.IntrospectSchema(transportsecurity.WithPlaintext(context.Background()))
			require.Error(t, err)
			require.NotContains(t, err.Error(), "synthetic-key")
			require.Zero(t, hits)
			require.Error(t, connector.TestConnection(transportsecurity.WithPlaintext(context.Background())))
			execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), []byte(`{"method":"GET","path":"/redirect"}`), nil)
			require.NoError(t, err)
			require.Error(t, execution.Error())
			require.Zero(t, hits)
			connector.config.OpenAPIURL = unrelated.URL + "/openapi.json"
			_, err = connector.IntrospectSchema(transportsecurity.WithPlaintext(context.Background()))
			require.NoError(t, err)
			require.Equal(t, 1, hits)
		})
	}
}

func TestValidateHTTPCredentials(t *testing.T) {
	for _, creds := range []Credentials{
		{APIKey: "secret"},
		{APIKeyHeader: "X-Api-Key"},
		{APIKey: "secret", APIKeyHeader: "Cookie"},
		{APIKey: "secret\r\ninjected", APIKeyHeader: "X-Api-Key"},
		{APIKey: "secret", APIKeyHeader: "X-Api-Key", BearerToken: "bearer"},
		{APIKey: "secret", APIKeyHeader: "Authorization", Username: "user", Password: "pass"},
		{BearerToken: "bearer", Username: "user", Password: "pass"},
	} {
		err := ValidateCredentials(creds)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
}

func TestOAuthSchemaCredentialsStayWithinGrantPath(t *testing.T) {
	c := &Connector{config: Config{BaseURL: "https://api.example/v1"}, bearerToken: "grant-token", oauthDestination: "https://api.example/v1"}
	for _, spec := range []string{"https://api.example/openapi.json", "https://api.example/v1/../other", "https://other.example/v1/spec.json"} {
		c.config.OpenAPIURL = spec
		require.Empty(t, c.openAPIHeaders())
	}
	c.config.OpenAPIURL = "https://api.example/v1/spec.json"
	require.Equal(t, "Bearer grant-token", c.openAPIHeaders().Get("Authorization"))
}

func TestOAuthSchemaRedirectCannotEscapeGrant(t *testing.T) {
	for _, target := range []string{"/other", "/v1/final"} {
		t.Run(target, func(t *testing.T) {
			fetched := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer grant-token", r.Header.Get("Authorization"))
				if r.URL.Path == "/v1/spec" {
					http.Redirect(w, r, target, http.StatusFound)
					return
				}
				fetched++
				_, _ = fmt.Fprint(w, authSpec)
			}))
			defer server.Close()
			oldTransport := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			defer func() { http.DefaultTransport = oldTransport }()
			c, err := New(transportsecurity.WithPlaintext(t.Context()), Config{BaseURL: server.URL + "/v1", OpenAPIURL: server.URL + "/v1/spec", AllowPrivateHosts: true}, Credentials{BearerToken: "grant-token", OAuthDestination: server.URL + "/v1"})
			require.NoError(t, err)
			defer func() { require.NoError(t, c.Close()) }()
			_, err = c.IntrospectSchema(transportsecurity.WithPlaintext(t.Context()))
			if target == "/other" {
				require.Error(t, err)
				require.Zero(t, fetched)
			} else {
				require.NoError(t, err)
				require.Equal(t, 1, fetched)
			}
		})
	}
}

func TestSecretHeaderHealthExecutionAndSchema(t *testing.T) {
	headers := map[string]string{"accessKey": "synthetic-access", "secretKey": "synthetic-secret", "appKey": "synthetic-app"}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		for name, value := range headers {
			assert.Equal(t, value, r.Header.Get(name))
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/spec.json" {
			_, _ = fmt.Fprint(w, authSpec)
			return
		}
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()
	c, err := New(transportsecurity.WithPlaintext(t.Context()), Config{BaseURL: server.URL, OpenAPIURL: server.URL + "/spec.json", AllowPrivateHosts: true}, Credentials{SecretHeaders: headers})
	require.NoError(t, err)
	require.NoError(t, c.TestConnection(transportsecurity.WithPlaintext(t.Context())))
	execution, err := c.Execute(transportsecurity.WithPlaintext(t.Context()), []byte(`{"method":"POST","path":"/items"}`), nil)
	require.NoError(t, err)
	require.NoError(t, execution.Error())
	_, err = c.IntrospectSchema(transportsecurity.WithPlaintext(t.Context()))
	require.NoError(t, err)
	execution, err = c.Execute(transportsecurity.WithPlaintext(t.Context()), []byte(`{"method":"POST","path":"/items","headers":{"ACCESSKEY":"authored-secret"}}`), nil)
	require.NoError(t, err)
	require.Error(t, execution.Error())
	require.NotContains(t, execution.Error().Error(), "authored-secret")
	require.Equal(t, 3, calls)
}

func TestSecretHeaderValidation(t *testing.T) {
	for _, headers := range []map[string]string{
		{"Host": "synthetic-secret"},
		{"Cookie": "synthetic-secret"},
		{"Content-Length": "synthetic-secret"},
		{"Connection": "synthetic-secret"},
		{"Proxy-Token": "synthetic-secret"},
		{"bad name": "synthetic-secret"},
		{"X-Key": "synthetic-secret", "x-key": "other"},
		{"X-Key": "synthetic-secret\r\nInjected: yes"},
		{"X-Key": ""},
		{"X-Key": string(make([]byte, 4097))},
		{"a": "a", "b": "b", "c": "c", "d": "d", "e": "e", "f": "f", "g": "g", "h": "h", "i": "i"},
	} {
		err := ValidateCredentials(Credentials{SecretHeaders: headers})
		require.Error(t, err)
		require.NotContains(t, err.Error(), "synthetic-secret")
	}
	for _, credentials := range []Credentials{
		{SecretHeaders: map[string]string{"X-Key": "secret"}, APIKey: "other", APIKeyHeader: "X-Api-Key"},
		{SecretHeaders: map[string]string{"X-Key": "secret"}, BearerToken: "other"},
		{SecretHeaders: map[string]string{"X-Key": "secret"}, Username: "user", Password: "pass"},
	} {
		require.Error(t, ValidateCredentials(credentials))
	}
}

func TestSecretHeaderEchoIsWithheld(t *testing.T) {
	for _, mode := range []string{"header", "body", "escaped", "escaped duplicate"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch mode {
				case "header":
					w.Header().Set("X-Echo", r.Header.Get("X-Key"))
					_, _ = fmt.Fprint(w, `{}`)
				case "escaped duplicate":
					_, _ = fmt.Fprint(w, `{"echo":"synthetic-\u0073ecret","echo":"safe"}`)
				case "escaped":
					_, _ = fmt.Fprint(w, `{"echo":"synthetic-\u0073ecret"}`)
				default:
					assert.NoError(t, json.NewEncoder(w).Encode(map[string]string{"echo": r.Header.Get("X-Key")}))
				}
			}))
			defer server.Close()
			c, err := New(transportsecurity.WithPlaintext(t.Context()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{SecretHeaders: map[string]string{"X-Key": "synthetic-secret"}})
			require.NoError(t, err)
			execution, err := c.Execute(transportsecurity.WithPlaintext(t.Context()), []byte(`{"method":"POST","path":"/records"}`), nil)
			require.NoError(t, err)
			require.ErrorIs(t, execution.Error(), xerror.HTTPAcknowledgementUncertain())
			body, _ := execution.HTTP()
			require.Nil(t, body)
			require.NotContains(t, execution.Error().Error(), "synthetic-secret")
		})
	}
}
