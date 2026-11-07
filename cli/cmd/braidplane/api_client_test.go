package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/cli/apiclient"
)

func TestNewCLIAPIClientInjectsBearerTokenIntoGeneratedClient(t *testing.T) {
	t.Parallel()

	var gotAuth string
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"organizations":[],"next_cursor":null}`))
	}))
	t.Cleanup(server.Close)

	client, err := newCLIAPIClient(cliAuthConfig{
		ServerURL:    server.URL,
		Organization: "acme",
		APIKey:       "xto_test_secret",
	})
	require.NoError(t, err)

	resp, err := client.openapi.ListOrganizationsWithResponse(
		context.Background(),
		&api.ListOrganizationsParams{},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode())
	require.Equal(t, "/organizations", gotPath)
	require.Equal(t, "Bearer xto_test_secret", gotAuth)
	require.Equal(t, "acme", client.organization)
}

func TestNewCLIAPIClientRejectsAmbiguousCredentials(t *testing.T) {
	client, err := newCLIAPIClient(cliAuthConfig{ServerURL: "https://api.example.test", APIKey: "fixture-key", PAT: "fixture-pat"})
	require.Nil(t, client)
	require.ErrorContains(t, err, "select one explicitly")
	require.NotContains(t, err.Error(), "fixture-key")
	require.NotContains(t, err.Error(), "fixture-pat")
}

func TestNewCLIAPIClientDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, target := range []string{
			"https://other.example.test/receive",
			"https://api.example.test:8443/receive",
			"http://api.example.test/receive",
			"/redirected",
		} {
			t.Run(strconv.Itoa(status)+"/"+target, func(t *testing.T) {
				t.Parallel()

				client, err := newCLIAPIClient(cliAuthConfig{
					ServerURL:    "https://api.example.test",
					Organization: "acme",
					PAT:          "synthetic-redirect-token",
				})
				require.NoError(t, err)
				generated, ok := client.openapi.ClientInterface.(*api.Client)
				require.True(t, ok)
				httpClient, ok := generated.Client.(*http.Client)
				require.True(t, ok)
				transport, ok := httpClient.Transport.(bearerTransport)
				require.True(t, ok)

				requests := 0
				transport.next = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					requests++
					require.Equal(t, "Bearer synthetic-redirect-token", req.Header.Get("Authorization"))
					if requests > 1 {
						return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
					}
					return &http.Response{
						StatusCode: status,
						Header:     http.Header{"Location": []string{target}},
						Body:       http.NoBody,
						Request:    req,
					}, nil
				})
				httpClient.Transport = transport

				err = client.verifyOrganizationAccess(context.Background())

				require.Equal(t, 1, requests, "redirect destination must never be contacted")
				require.ErrorContains(t, err, "unexpected API response: status "+strconv.Itoa(status))
			})
		}
	}
}

func TestNewCLIAPIClientConfiguresRequestTimeout(t *testing.T) {
	t.Parallel()

	client, err := newCLIAPIClient(cliAuthConfig{
		ServerURL:    "https://api.example.test",
		Organization: "acme",
		APIKey:       "xto_test_secret",
	})
	require.NoError(t, err)

	openapiClient, ok := client.openapi.ClientInterface.(*api.Client)
	require.True(t, ok)
	httpClient, ok := openapiClient.Client.(*http.Client)
	require.True(t, ok)
	require.Equal(t, cliAPIClientTimeout, httpClient.Timeout)
}

func TestNormalizeAPIServerURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "root server URL",
			raw:  "http://localhost:3001",
			want: "http://localhost:3001",
		},
		{
			name: "already API base URL",
			raw:  "http://localhost:3001/api",
			want: "http://localhost:3001/api",
		},
		{
			name: "nested root server URL",
			raw:  "https://example.test/braidplane",
			want: "https://example.test/braidplane",
		},
		{
			name: "trailing slash",
			raw:  "https://api.example.test/",
			want: "https://api.example.test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeAPIServerURL(tt.raw)

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeAPIServerURLRejectsUnsafeForms(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"userinfo", "https://fixture-user:fixture-secret@example.test/api"},
		{"query", "https://example.test/api?token=fixture-secret"},
		{"fragment", "https://example.test/api#fixture-secret"},
		{"invalid userinfo", "https://fixture-secret%@example.test/api"},
		{"unsupported scheme", "ftp://example.test/api"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeAPIServerURL(tc.raw)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "fixture-secret")
		})
	}
}

func TestDecodeAPIErrorMapsStructuredError(t *testing.T) {
	t.Parallel()

	detail := "role does not grant pipelines:read"
	body, err := json.Marshal(api.ErrorResponse{
		Error: api.Error{
			Code:          "FORBIDDEN",
			Message:       "not allowed",
			DetailedError: &detail,
		},
	})
	require.NoError(t, err)

	err = decodeAPIError(http.StatusForbidden, body)

	require.Error(t, err)
	require.ErrorContains(t, err, "403")
	require.ErrorContains(t, err, "FORBIDDEN")
	require.ErrorContains(t, err, "not allowed")
	require.ErrorContains(t, err, detail)
}

func TestDecodeAPIErrorsPreserveRequestID(t *testing.T) {
	t.Parallel()
	const requestID = "550e8400-e29b-41d4-a716-446655440000"
	for _, tc := range []struct {
		name   string
		decode func(int, []byte) error
		body   string
	}{
		{"ordinary", decodeAPIError, `{"error":{"code":"FORBIDDEN","message":"not allowed","request_id":"` + requestID + `"}}`},
		{"draft conflict", decodePipelineDraftConflictError, `{"error":{"code":"PIPELINE_DRAFT_CONFLICT","message":"conflict","request_id":"` + requestID + `"},"pipeline_slug":"orders"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.decode(http.StatusConflict, []byte(tc.body))
			require.ErrorContains(t, err, `request_id="`+requestID+`"`)
		})
	}
}

func TestDecodeAPIErrorsAcceptOpaqueRequestID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		decode func(int, []byte) error
		body   string
		want   string
	}{
		{decodeAPIError, `{"error":{"code":"FORBIDDEN","message":"not allowed","request_id":"credential-validation"}}`, "FORBIDDEN"},
		{decodePipelineDraftConflictError, `{"error":{"code":"PIPELINE_DRAFT_CONFLICT","message":"conflict","request_id":"credential-validation"},"pipeline_slug":"orders","draft_preserved":true}`, "draft_preserved=true"},
	} {
		err := tc.decode(http.StatusConflict, []byte(tc.body))
		require.ErrorContains(t, err, `request_id="credential-validation"`)
		require.ErrorContains(t, err, tc.want)
		require.NotContains(t, err.Error(), "unexpected API response")
	}
	err := decodeAPIError(http.StatusForbidden, []byte(`{"error":{"code":"FORBIDDEN","message":"not allowed","request_id":"req\u001b[31m"}}`))
	require.ErrorContains(t, err, `request_id="req\x1b[31m"`)
	require.NotContains(t, err.Error(), "\x1b")
}

func TestDecodeAPIErrorShowsValidationFieldsWithoutValues(t *testing.T) {
	t.Parallel()
	body := []byte(`{"error":{"code":"VALIDATION_FAILED","message":"Request validation failed","validation_errors":[{"field":"password","code":"VALIDATION_REQUIRED_FIELD","message":"submitted-secret","details":{"provided_value":"submitted-secret"}},{"field":"name","code":"VALUE_TOO_LONG","message":"submitted-secret"}]}}`)
	err := decodeAPIError(http.StatusBadRequest, body)
	require.ErrorContains(t, err, `field="password" code="VALIDATION_REQUIRED_FIELD"`)
	require.ErrorContains(t, err, `field="name" code="VALUE_TOO_LONG"`)
	require.NotContains(t, err.Error(), "submitted-secret")
}

func TestDecodeAPIErrorFallsBackForInvalidBody(t *testing.T) {
	t.Parallel()

	err := decodeAPIError(http.StatusInternalServerError, []byte(`not-json`))

	require.Error(t, err)
	require.ErrorContains(t, err, "500")
	require.ErrorContains(t, err, "unexpected API response")
}
