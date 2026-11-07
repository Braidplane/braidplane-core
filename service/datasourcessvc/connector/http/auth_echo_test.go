package http

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestAuthenticationEchoesAreWithheld(t *testing.T) {
	const secret = "synthetic-auth-echo-secret"
	for _, auth := range []struct {
		name   string
		creds  Credentials
		header string
	}{
		{name: "named", creds: Credentials{SecretHeaders: map[string]string{"X-Key": secret}}, header: "X-Key"},
		{name: "api-key", creds: Credentials{APIKey: secret, APIKeyHeader: "X-Api-Key"}, header: "X-Api-Key"},
		{name: "api-authorization", creds: Credentials{APIKey: secret, APIKeyHeader: "Authorization"}, header: "Authorization"},
		{name: "bearer", creds: Credentials{BearerToken: secret}, header: "Authorization"},
		{name: "oauth", creds: Credentials{BearerToken: secret}, header: "Authorization"},
		{name: "basic-wire", creds: Credentials{Username: "synthetic-user", Password: secret}, header: "Authorization"},
		{name: "basic-password", creds: Credentials{Username: "synthetic-user", Password: secret}, header: "Authorization"},
	} {
		for _, mode := range []string{"header", "body", "escaped", "duplicate", "spec-json", "spec-yaml", "spec-binary", "benign"} {
			t.Run(auth.name+"/"+mode, func(t *testing.T) {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					value := r.Header.Get(auth.header)
					if auth.name == "basic-wire" {
						value = strings.TrimPrefix(value, "Basic ")
					}
					if auth.name == "basic-password" {
						_, value, _ = r.BasicAuth()
					}
					var escaped strings.Builder
					for _, char := range value {
						_, _ = fmt.Fprintf(&escaped, `\u%04x`, char)
					}
					w.Header().Set("Content-Type", "application/json")
					switch mode {
					case "header":
						w.Header().Set("X-Echo", value)
						_, _ = fmt.Fprint(w, `{}`)
					case "escaped", "duplicate":
						suffix := ""
						if mode == "duplicate" {
							suffix = `,"echo":"safe"`
						}
						_, _ = fmt.Fprintf(w, `{"echo":"%s"%s}`, escaped.String(), suffix)
					case "spec-json":
						_, _ = fmt.Fprintf(w, `{"openapi":"3.0.3","info":{"title":"Fixture","version":"1"},"paths":{"/items":{"get":{"operationId":"items","description":"%s","responses":{"200":{"description":"OK"}}}}}}`, escaped.String())
					case "spec-yaml", "spec-binary":
						w.Header().Set("Content-Type", "application/yaml")
						description := `"` + escaped.String() + `"`
						if mode == "spec-binary" {
							description = "!!binary " + base64.StdEncoding.EncodeToString([]byte(value))
						}
						// #nosec G705 -- This owned YAML fixture deliberately reflects synthetic credentials.
						_, _ = fmt.Fprintf(w, "openapi: 3.0.3\ninfo: {title: Fixture, version: '1'}\npaths:\n  /items:\n    get:\n      operationId: items\n      description: %s\n      responses: {'200': {description: OK}}\n", description)
					case "benign":
						_, _ = fmt.Fprint(w, `{"ok":true}`)
					default:
						_ = json.NewEncoder(w).Encode(map[string]string{"echo": value})
					}
				}))
				defer server.Close()
				originalTransport := http.DefaultTransport
				http.DefaultTransport = server.Client().Transport
				defer func() { http.DefaultTransport = originalTransport }()
				creds := auth.creds
				if auth.name == "oauth" {
					creds.OAuthDestination = server.URL
				}
				ctx := transportsecurity.WithPlaintext(t.Context())
				connector, err := New(ctx, Config{BaseURL: server.URL, OpenAPIURL: server.URL + "/spec", AllowPrivateHosts: true}, creds)
				require.NoError(t, err)
				defer func() { require.NoError(t, connector.Close()) }()
				if strings.HasPrefix(mode, "spec-") {
					schema, schemaErr := connector.IntrospectSchema(ctx)
					require.ErrorIs(t, schemaErr, xerror.SchemaFetchFailed())
					require.Nil(t, schema)
					require.NotContains(t, schemaErr.Error(), secret)
					return
				}
				execution, err := connector.Execute(ctx, []byte(`{"method":"POST","path":"/echo"}`), nil)
				require.NoError(t, err)
				if mode == "benign" {
					require.NoError(t, execution.Error())
					return
				}
				require.ErrorIs(t, execution.Error(), xerror.HTTPAcknowledgementUncertain())
				payload, _ := execution.HTTP()
				require.Nil(t, payload)
				require.NotContains(t, execution.Error().Error(), secret)
			})
		}
	}
}

func TestOpenAPIEchoInspectionKeepsBenignYAMLAndTerminatesAliases(t *testing.T) {
	connector := &Connector{bearerToken: "synthetic-auth-echo-secret"}
	for _, body := range []string{
		"openapi: 3.0.3\ninfo: {title: Fixture, version: '1'}\npaths: {}\n",
		"value: &cycle [*cycle]\n",
		"invalid: [\n",
	} {
		require.False(t, connector.openAPIResponseExposesSecret([]byte(body)))
	}
	unauthenticated := &Connector{}
	require.False(t, unauthenticated.responseExposesSecret(http.Header{"X-Echo": {"synthetic-auth-echo-secret"}}, []byte(`{"echo":"synthetic-auth-echo-secret"}`)))
	require.False(t, unauthenticated.openAPIResponseExposesSecret([]byte("description: synthetic-auth-echo-secret\n")))
}

func TestOpenAPIEchoWithholdsNormalizedNumericCredentials(t *testing.T) {
	for _, body := range []string{
		`{"openapi":"3.0.3","info":{"title":"Fixture","version":"1"},"paths":{"/items":{"get":{"operationId":"items","responses":{"200":{"description":"OK","content":{"application/json":{"schema":{"type":"integer","enum":[1.234e3]}}}}}}}}}`,
		"openapi: 3.0.3\ninfo: {title: Fixture, version: '1'}\npaths:\n  /items:\n    get:\n      operationId: items\n      responses:\n        '200':\n          description: OK\n          content:\n            application/json:\n              schema: {type: integer, enum: [0x4d2]}\n",
	} {
		t.Run(body[:1], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()
			ctx := transportsecurity.WithPlaintext(t.Context())
			connector, err := New(ctx, Config{BaseURL: server.URL, OpenAPIURL: server.URL + "/spec", AllowPrivateHosts: true}, Credentials{BearerToken: "1234"})
			require.NoError(t, err)
			defer func() { require.NoError(t, connector.Close()) }()
			schema, schemaErr := connector.IntrospectSchema(ctx)
			if schemaErr == nil {
				encoded, encodeErr := json.Marshal(schema)
				require.NoError(t, encodeErr)
				require.Contains(t, string(encoded), "1234", "confirm normalized output before reporting the bypass")
			}
			require.ErrorIs(t, schemaErr, xerror.SchemaFetchFailed())
			require.Nil(t, schema)
		})
	}
}
