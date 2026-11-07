package googleworkspace

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(t.Context(), Credentials{AccessToken: "test-token", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}})
	require.NoError(t, err)
	client.client.Transport = server.Client().Transport
	client.sheets.BasePath, client.drive.BasePath = server.URL+"/sheets/", server.URL+"/drive/"
	return client, server
}

func TestMetadataAndDiscoveryUseBoundedGoogleFields(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/sheets/v4/spreadsheets/book":
			assert.Equal(t, "spreadsheetId,properties(title),sheets(properties(sheetId,title,gridProperties(rowCount,columnCount)))", r.URL.Query().Get("fields"))
			_, _ = w.Write([]byte(`{"spreadsheetId":"book","properties":{"title":"Budget"},"sheets":[{"properties":{"sheetId":7,"title":"Q1","gridProperties":{"rowCount":11,"columnCount":3}}}]}`))
		case "/drive/files/book":
			_, _ = w.Write([]byte(`{"id":"book","mimeType":"application/vnd.google-apps.spreadsheet"}`))
		case "/drive/files":
			assert.Equal(t, "trashed=false", r.URL.Query().Get("q"))
			assert.Equal(t, "nextPageToken,files("+fileFields+")", r.URL.Query().Get("fields"))
			assert.Equal(t, "next", r.URL.Query().Get("pageToken"))
			_, _ = w.Write([]byte(`{"nextPageToken":"later","files":[{"id":"book","name":"Budget"}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	})
	metadata, err := client.Metadata(context.Background(), "book", Limits{})
	require.NoError(t, err)
	assert.Equal(t, "Budget", metadata.Title)
	require.Len(t, metadata.Worksheets, 1)
	discovery, err := client.Discovery(context.Background(), workspace.ListOptions{PageToken: "next"}, Limits{}, 0)
	require.NoError(t, err)
	assert.Equal(t, "later", discovery.NextPageToken)
	assert.Equal(t, "book", discovery.Files[0].FileID)
}

func TestReadProtocolAndQuotaResponseLimit(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/sheets/v4/spreadsheets/book/values/A1:B2", r.URL.Path)
		assert.Equal(t, "ROWS", r.URL.Query().Get("majorDimension"))
		assert.Equal(t, "UNFORMATTED_VALUE", r.URL.Query().Get("valueRenderOption"))
		assert.Equal(t, "SERIAL_NUMBER", r.URL.Query().Get("dateTimeRenderOption"))
		_, _ = w.Write([]byte(`{"values":[[1,true],[null,"x"]]}`))
	})
	ctx := schemaquota.WithQuota(context.Background(), 10, 1024)
	rows, err := client.Read(ctx, "book", "A1:B2", Limits{MaxResponseBytes: 1024})
	require.NoError(t, err)
	assert.Equal(t, [][]any{{float64(1), true}, {nil, "x"}}, rows)

	_, err = client.Read(schemaquota.WithQuota(context.Background(), 10, 1), "book", "A1:B2", Limits{})
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

func TestReadRetriesOnlyRetryableStatuses(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"errors":[{"reason":"rateLimitExceeded"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"values":[[1]]}`))
	})
	rows, err := client.Read(context.Background(), "book", "A1:A1", Limits{RetryAttempts: 2})
	require.NoError(t, err)
	assert.Equal(t, [][]any{{float64(1)}}, rows)
	assert.EqualValues(t, 2, calls.Load())
}

func TestProviderFailuresAreSafeAndTyped(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, xerror.InvalidCredentials()},
		{http.StatusForbidden, xerror.Forbidden(xerror.DataSource)},
		{http.StatusNotFound, xerror.NotFound(xerror.DataSource)},
	} {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"error":{"message":"token=do-not-leak"}}`))
			})
			_, err := client.Read(context.Background(), "book", "A1:A1", Limits{})
			require.ErrorIs(t, err, tt.want)
			assert.NotContains(t, err.Error(), "do-not-leak")
		})
	}
}

func TestUpdateRawIsBoundedAndNeverRetried(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "RAW", r.URL.Query().Get("valueInputOption"))
		assert.Equal(t, "false", r.URL.Query().Get("includeValuesInResponse"))
		var payload struct {
			Range, MajorDimension string
			Values                [][]any
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		assert.Equal(t, "A1:B1", payload.Range)
		assert.Equal(t, "ROWS", payload.MajorDimension)
		w.WriteHeader(http.StatusInternalServerError)
	})
	err := client.Update(context.Background(), "book", "A1:B1", [][]any{{nil, "x"}}, Limits{RetryAttempts: 9})
	require.ErrorIs(t, err, xerror.ExecutionFailed())
	assert.EqualValues(t, 1, calls.Load())

	err = client.Update(context.Background(), "book", "A1:B1", [][]any{{"too long"}}, Limits{MaxRequestBytes: 1})
	require.ErrorIs(t, err, xerror.ExecutionRequestSizeExceeded())
	assert.EqualValues(t, 1, calls.Load())
}

func TestReadCancellationAndTransportRetryBoundary(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		<-time.After(time.Second)
		_, _ = w.Write([]byte(`{"values":[]}`))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Read(ctx, "book", "A1:A1", Limits{})
	require.ErrorIs(t, err, xerror.Timeout(xerror.DataSource))

	client.client.Transport = roundTripper(func(*http.Request) (*http.Response, error) { return nil, &url.Error{Err: temporaryError{}} })
	_, err = client.Read(context.Background(), "book", "A1:A1", Limits{RetryAttempts: 2})
	require.ErrorIs(t, err, xerror.Unavailable(xerror.DataSource))
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type temporaryError struct{}

func (temporaryError) Error() string   { return "temporary" }
func (temporaryError) Timeout() bool   { return true }
func (temporaryError) Temporary() bool { return true }

var _ error = temporaryError{}

func TestReadAttemptsQuotaAndRetryAfterDeadline(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		attempts  int
		wantCalls int
	}{
		{"quota403", 403, `{"error":{"errors":[{"reason":"userRateLimitExceeded"}]}}`, 3, 3},
		{"server", 503, `{}`, 2, 2},
		{"permission", 403, `{"error":{"errors":[{"reason":"forbidden"}]}}`, 3, 1},
		{"authentication", 401, `{}`, 3, 1},
		{"missing", 404, `{}`, 3, 1},
		{"single attempt", 429, `{}`, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := client.Read(t.Context(), "book", "A1:A1", Limits{RetryAttempts: tc.attempts, RetryBackoff: time.Millisecond})
			require.Error(t, err)
			assert.Equal(t, tc.wantCalls, calls)
		})
	}
	for _, header := range []string{"1", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)} {
		t.Run(header, func(t *testing.T) {
			calls := 0
			client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Retry-After", header)
				w.WriteHeader(429)
			})
			_, err := client.Read(t.Context(), "book", "A1:A1", Limits{RetryAttempts: 3, Timeout: 10 * time.Millisecond})
			require.ErrorIs(t, err, xerror.Timeout(xerror.DataSource))
			assert.Equal(t, 1, calls)
		})
	}
}

func TestUpdatePayloadExactCeilingAndLostResponse(t *testing.T) {
	calls := 0
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(`{}`)) })
	// ASCII adds exactly one byte per character to the JSON payload.
	baseline := struct {
		Range     string  `json:"range"`
		Dimension string  `json:"majorDimension"`
		Values    [][]any `json:"values"`
	}{"A1:A1", "ROWS", [][]any{{""}}}
	encoded, err := json.Marshal(baseline)
	require.NoError(t, err)
	values := [][]any{{strings.Repeat("x", maxRequestBytes-len(encoded)-1)}}
	require.NoError(t, client.Update(t.Context(), "book", "A1:A1", values, Limits{}))
	assert.Equal(t, 1, calls)
	values[0][0] = values[0][0].(string) + "x"
	require.ErrorIs(t, client.Update(t.Context(), "book", "A1:A1", values, Limits{}), xerror.ExecutionRequestSizeExceeded())
	assert.Equal(t, 1, calls)
	client.client.Transport = roundTripper(func(*http.Request) (*http.Response, error) { calls++; return nil, io.EOF })
	err = client.Update(t.Context(), "book", "A1:A1", [][]any{{"write"}}, Limits{RetryAttempts: 4})
	require.ErrorIs(t, err, xerror.ExecutionFailed())
	require.Contains(t, err.Error(), "outcome is unknown")
	assert.Equal(t, 2, calls)
}

func TestReadInterruptedBodyRetriesAndSchemaQuota(t *testing.T) {
	client, err := New(t.Context(), Credentials{AccessToken: "synthetic", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}})
	require.NoError(t, err)
	calls := 0
	client.client.Transport = roundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(brokenReader{})}, nil
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"values":[[1]]}`))}, nil
	})
	rows, err := client.Read(t.Context(), "book", "A1:A1", Limits{RetryAttempts: 2})
	require.NoError(t, err)
	assert.Equal(t, [][]any{{float64(1)}}, rows)
	assert.Equal(t, 2, calls)
	client, _ = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/drive/files/book" {
			_, _ = w.Write([]byte(`{"id":"book","mimeType":"application/vnd.google-apps.spreadsheet"}`))
			return
		}
		_, _ = w.Write([]byte(`{"spreadsheetId":"book","properties":{},"sheets":[{"properties":{"sheetId":0,"title":"A"}},{"properties":{"sheetId":1,"title":"B"}}]}`))
	})
	_, err = client.Metadata(schemaquota.WithQuota(t.Context(), 1, 1024), "book", Limits{})
	require.ErrorIs(t, err, xerror.ExecutionUnavailable())
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestInterruptedAuthenticationResponseIsNotRetried(t *testing.T) {
	client, err := New(t.Context(), Credentials{AccessToken: "synthetic", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}})
	require.NoError(t, err)
	calls := 0
	client.client.Transport = roundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(brokenReader{})}, nil
	})
	_, err = client.Read(t.Context(), "book", "A1:A1", Limits{RetryAttempts: 3})
	require.ErrorIs(t, err, xerror.InvalidCredentials())
	require.Equal(t, 1, calls)
}

func TestMalformedReadResponsesAreNotRetried(t *testing.T) {
	for _, body := range []string{`null`, `{"values":`, ``} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) { calls++; _, _ = w.Write([]byte(body)) })
			_, err := client.Read(t.Context(), "book", "A1:A1", Limits{RetryAttempts: 3})
			require.Error(t, err)
			assert.Equal(t, 1, calls)
		})
	}
}

func TestUpdateSerializationFailureDoesNotDispatch(t *testing.T) {
	calls := 0
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) { calls++; _, _ = w.Write([]byte(`{}`)) })
	err := client.Update(t.Context(), "book", "A1:A1", [][]any{{make(chan int)}}, Limits{})
	require.ErrorIs(t, err, xerror.ValidationFailed())
	assert.Zero(t, calls)
}

func TestOfficialClientDoesNotFollowRedirects(t *testing.T) {
	redirected := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected++; _, _ = w.Write([]byte(`{"values":[]}`)) }))
	t.Cleanup(destination.Close)
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	})
	_, err := client.Read(t.Context(), "book", "A1:A1", Limits{RetryAttempts: 3})
	require.Error(t, err)
	assert.Zero(t, redirected)
	assert.NotContains(t, err.Error(), "test-token")
}

func TestUpdateDefinitiveRejectionsAreTypedAndNotRetried(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusBadRequest, xerror.InvalidInput(xerror.DataSource)},
		{http.StatusUnauthorized, xerror.InvalidCredentials()},
		{http.StatusForbidden, xerror.Forbidden(xerror.DataSource)},
		{http.StatusNotFound, xerror.NotFound(xerror.DataSource)},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			calls := 0
			client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"message":"do-not-leak"}}`))
			})
			err := client.Update(t.Context(), "book", "A1:A1", [][]any{{"write"}}, Limits{RetryAttempts: 3})
			require.ErrorIs(t, err, tc.want)
			assert.Equal(t, 1, calls)
			assert.NotContains(t, err.Error(), "do-not-leak")
			// The status remains definitive when its response body is interrupted.
			client.client.Transport = roundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(brokenReader{})}, nil
			})
			require.ErrorIs(t, client.Update(t.Context(), "book", "A1:A1", [][]any{{"write"}}, Limits{RetryAttempts: 3}), tc.want)
			assert.Equal(t, 2, calls)
		})
	}
}
