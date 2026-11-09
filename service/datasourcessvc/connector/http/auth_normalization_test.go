package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestOpenAPIEchoWithholdsNormalizedYAMLKeys(t *testing.T) {
	for _, tc := range []struct {
		name    string
		secret  string
		anchor  string
		key     string
		blocked bool
	}{
		{name: "positive exponent", secret: "1e+06", key: "1000000.0", blocked: true},
		{name: "negative exponent", secret: "1e-07", key: "0.0000001", blocked: true},
		{name: "alias key", secret: "1e+06", anchor: "x-value: &property 1000000.0\n", key: "*property ", blocked: true},
		{name: "ordinary numeric key", secret: "unrelated-synthetic-token", key: "1000000.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := fmt.Sprintf("openapi: 3.0.3\ninfo: {title: Fixture, version: '1'}\n%spaths:\n  /items:\n    get:\n      operationId: items\n      responses:\n        '200':\n          description: OK\n          content:\n            application/json:\n              schema:\n                type: object\n                properties:\n                  %s: {type: string}\n", tc.anchor, tc.key)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, spec)
			}))
			defer server.Close()
			ctx := transportsecurity.WithPlaintext(t.Context())
			connector, err := New(ctx, Config{BaseURL: server.URL, OpenAPIURL: server.URL + "/spec", AllowPrivateHosts: true}, Credentials{BearerToken: tc.secret})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, connector.Close()) })
			schema, err := connector.IntrospectSchema(ctx)
			if tc.blocked {
				if err == nil {
					encoded, encodeErr := json.Marshal(schema)
					require.NoError(t, encodeErr)
					require.Contains(t, string(encoded), tc.secret, "confirm the normalized credential reaches returned schema")
				}
				require.ErrorIs(t, err, xerror.SchemaFetchFailed())
				require.Nil(t, schema)
				require.NotContains(t, err.Error(), tc.secret)
				return
			}
			require.NoError(t, err)
			encoded, err := json.Marshal(schema)
			require.NoError(t, err)
			require.Contains(t, string(encoded), `"1e+06"`)
		})
	}
}

func TestOpenAPIEchoKeepsNumericValues(t *testing.T) {
	const spec = "openapi: 3.0.3\ninfo: {title: Fixture, version: '1'}\npaths:\n  /items:\n    get:\n      operationId: items\n      responses:\n        '200':\n          description: OK\n          content:\n            application/json:\n              schema: {type: number, enum: [1000000.0]}\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, spec)
	}))
	defer server.Close()
	ctx := transportsecurity.WithPlaintext(t.Context())
	connector, err := New(ctx, Config{BaseURL: server.URL, OpenAPIURL: server.URL + "/spec", AllowPrivateHosts: true}, Credentials{BearerToken: "1e+06"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connector.Close()) })
	schema, err := connector.IntrospectSchema(ctx)
	require.NoError(t, err)
	encoded, err := json.Marshal(schema)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "1000000")
	require.NotContains(t, string(encoded), "1e+06")
}

func TestOpenAPIEchoWithholdsNormalizedBinaryStrings(t *testing.T) {
	for _, tc := range []struct {
		name        string
		anchor      string
		description string
		password    string
		blocked     bool
	}{
		{name: "binary", description: "!!binary YWL/Y2Q=", password: "ab\ufffdcd", blocked: true},
		{name: "alias", anchor: "x-value: &echo !!binary YWL/Y2Q=\n", description: "*echo", password: "ab\ufffdcd", blocked: true},
		{name: "unrelated password", description: "!!binary YWL/Y2Q=", password: "unrelated-synthetic-password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := fmt.Sprintf("openapi: 3.0.3\ninfo: {title: Fixture, version: '1'}\n%spaths:\n  /items:\n    get:\n      operationId: items\n      description: %s\n      responses: {'200': {description: OK}}\n", tc.anchor, tc.description)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, spec)
			}))
			defer server.Close()
			ctx := transportsecurity.WithPlaintext(t.Context())
			connector, err := New(ctx, Config{BaseURL: server.URL, OpenAPIURL: server.URL + "/spec", AllowPrivateHosts: true}, Credentials{Username: "synthetic-user", Password: tc.password})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, connector.Close()) })
			schema, err := connector.IntrospectSchema(ctx)
			if tc.blocked {
				if err == nil {
					encoded, encodeErr := json.Marshal(schema)
					require.NoError(t, encodeErr)
					var normalized HTTPSchema
					require.NoError(t, json.Unmarshal(encoded, &normalized))
					require.Len(t, normalized.Operations, 1)
					require.Contains(t, normalized.Operations[0].Description, tc.password, "confirm the normalized password reaches returned schema")
				}
				require.ErrorIs(t, err, xerror.SchemaFetchFailed())
				require.Nil(t, schema)
				require.NotContains(t, err.Error(), tc.password)
				return
			}
			require.NoError(t, err)
			require.Len(t, schema.Operations, 1)
		})
	}
}

func TestAuthenticationEchoWithholdsNormalizedText(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		password    string
		body        []byte
		header      []byte
		blocked     bool
	}{
		{name: "invalid UTF-8", contentType: "application/json", password: "ab\ufffdcd", body: []byte{'a', 'b', 0xff, 'c', 'd'}, blocked: true},
		{name: "multiple invalid bytes", contentType: "text/csv", password: "ab\ufffd\ufffdcd", body: []byte{'a', 'b', 0xc0, 0xaf, 'c', 'd'}, blocked: true},
		{name: "JSON string", contentType: "application/json", password: "ab\ufffdcd", body: []byte{'"', 'a', 'b', 0xff, 'c', 'd', '"'}, blocked: true},
		{name: "header UTF-8", contentType: "application/json", password: "ab\ufffdcd", body: []byte(`{}`), header: []byte{'a', 'b', 0xff, 'c', 'd'}, blocked: true},
		{name: "header multiple invalid bytes", contentType: "application/json", password: "ab\ufffd\ufffdcd", body: []byte(`{}`), header: []byte{'a', 'b', 0xc0, 0xaf, 'c', 'd'}, blocked: true},
		{name: "unrelated header", contentType: "application/json", password: "unrelated-synthetic-password", body: []byte(`{}`), header: []byte{'a', 'b', 0xff, 'c', 'd'}},
		{name: "ordinary text", contentType: "application/json", password: "unrelated-synthetic-password", body: []byte{'a', 'b', 0xff, 'c', 'd'}},
		{name: "opaque text", contentType: "text/plain", password: "ab\ufffdcd", body: []byte{'a', 'b', 0xff, 'c', 'd'}},
		{name: "native file", contentType: "application/octet-stream", password: "ab\ufffdcd", body: []byte{'a', 'b', 0xff, 'c', 'd'}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				if tc.header != nil {
					w.Header().Set("X-Echo", string(tc.header))
				}
				_, _ = w.Write(tc.body)
			}))
			defer server.Close()
			ctx := transportsecurity.WithPlaintext(t.Context())
			connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{Username: "synthetic-user", Password: tc.password})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, connector.Close()) })
			execution, err := connector.Execute(ctx, []byte(`{"method":"POST","path":"/echo"}`), nil)
			require.NoError(t, err)
			payload, ok := execution.HTTP()
			require.True(t, ok)
			if tc.blocked {
				if execution.Error() == nil {
					var value string
					if tc.header != nil {
						encoded, encodeErr := json.Marshal(payload)
						require.NoError(t, encodeErr)
						var normalized struct {
							Headers map[string]string `json:"headers"`
						}
						require.NoError(t, json.Unmarshal(encoded, &normalized))
						value = normalized.Headers["X-Echo"]
					} else {
						require.NoError(t, json.Unmarshal(payload.Body, &value))
					}
					require.Contains(t, value, tc.password, "confirm normalization introduces the configured password")
				}
				require.ErrorIs(t, execution.Error(), xerror.HTTPAcknowledgementUncertain())
				require.Nil(t, payload)
				require.NotContains(t, execution.Error().Error(), tc.password)
				return
			}
			require.NoError(t, execution.Error())
			require.Equal(t, tc.body, payload.RawBody)
			if tc.header != nil {
				require.Equal(t, string(tc.header), payload.Headers["X-Echo"])
				return
			}
			if tc.contentType == "application/octet-stream" || tc.contentType == "text/plain" {
				require.NotNil(t, payload.File)
				require.Empty(t, payload.Body)
			} else {
				var value string
				require.NoError(t, json.Unmarshal(payload.Body, &value))
				require.Equal(t, "ab\ufffdcd", value)
			}
		})
	}
}
