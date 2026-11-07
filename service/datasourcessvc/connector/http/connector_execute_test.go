package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	formpayload "github.com/Braidplane/braidplane-core/pkg/payload/form"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func TestConnectorExecute_SendsTopLevelJSONArrayBody(t *testing.T) {
	t.Parallel()

	requestBodies := make(chan []map[string]any, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sink" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}

		defer func() { _ = r.Body.Close() }()

		var body []map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requestBodies <- body

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"POST","path":"/sink","body":"{{payload}}"}`),
		map[string]types.TypedParam{
			"payload": {
				Value: []any{
					map[string]any{"id": int64(1)},
					map[string]any{"id": int64(2)},
				},
				DataType: "text",
			},
		},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	received := <-requestBodies
	require.Len(t, received, 2)
	require.InEpsilon(t, float64(1), received[0]["id"], 0)
	require.InEpsilon(t, float64(2), received[1]["id"], 0)
}

func TestConnectorExecute_PreservesMultipartResponseAsStructuredBody(t *testing.T) {
	t.Parallel()

	var responseBody bytes.Buffer
	writer := multipart.NewWriter(&responseBody)
	require.NoError(t, writer.WriteField("name", "Alice"))
	filePart, err := writer.CreateFormFile("upload", "report.txt")
	require.NoError(t, err)
	_, err = filePart.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/multipart" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", writer.FormDataContentType())
		_, _ = w.Write(responseBody.Bytes())
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"GET","path":"/multipart"}`),
		nil,
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	httpResult, ok := execResult.HTTP()
	require.True(t, ok)
	require.Nil(t, httpResult.File)
	require.NotEmpty(t, httpResult.RawBody)
	var storedBody string
	require.NoError(t, json.Unmarshal(httpResult.Body, &storedBody))
	require.Equal(t, responseBody.String(), storedBody)
}

func TestConnectorExecute_OmitsNilQueryParams(t *testing.T) {
	t.Parallel()

	requestQueries := make(chan url.Values, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/items" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		requestQueries <- r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"GET","path":"/items","query_params":{"cursor":"{{runtime.incremental.lower_bound.cursor}}","status":"active"}}`),
		map[string]types.TypedParam{
			"runtime.incremental.lower_bound.cursor": {
				Value:    nil,
				DataType: "text",
			},
		},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	query := <-requestQueries
	require.NotContains(t, query, "cursor")
	require.Equal(t, "active", query.Get("status"))
}

func TestConnectorExecute_BindsIncrementalCursorInRequestParts(t *testing.T) {
	t.Parallel()

	type receivedRequest struct {
		query  url.Values
		header string
		body   map[string]any
	}
	requests := make(chan receivedRequest, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/items" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		defer func() { _ = r.Body.Close() }()

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- receivedRequest{
			query:  r.URL.Query(),
			header: r.Header.Get("X-Page-Cursor"),
			body:   body,
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"next_page_token":"cursor-2"}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{
			"method": "POST",
			"path": "/items",
			"query_params": {"cursor": "{{runtime.incremental.lower_bound.cursor}}"},
			"headers": {"X-Page-Cursor": "{{runtime.incremental.lower_bound.cursor}}"},
			"body": {"cursor": "{{runtime.incremental.lower_bound.cursor}}"}
		}`),
		map[string]types.TypedParam{
			"runtime.incremental.lower_bound.cursor": {
				Value:    "cursor-1",
				DataType: "text",
			},
		},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	received := <-requests
	require.Equal(t, "cursor-1", received.query.Get("cursor"))
	require.Equal(t, "cursor-1", received.header)
	require.Equal(t, "cursor-1", received.body["cursor"])
}

func TestConnectorExecute_HTTPResultLimits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		body        string
		contentType string
		rowLimit    int
		byteLimit   int64
		wantErr     error
	}{
		{"array exceeds rows", `[{"id":1},{"id":2}]`, "application/json", 1, 1024, xerror.ExecutionRowLimitExceeded()},
		{"array at limit", `[1,2]`, "application/json", 2, 1024, nil},
		{"empty array", `[]`, "application/json", 1, 1024, nil},
		{"object is one document", `{"items":[1,2,3]}`, "application/json", 1, 1024, nil},
		{"scalar is one document", `42`, "application/json", 1, 1024, nil},
		{"null", `null`, "application/json", 1, 1024, nil},
		{"text", `plain text`, "text/plain", 1, 1024, nil},
		{"file has no JSON rows", `[1,2,3]`, "application/octet-stream", 1, 1024, nil},
		{"array exceeds bytes within rows", `[1]`, "application/json", 1, 2, xerror.ExecutionResponseSizeExceeded()},
		{"object exceeds bytes", `{"items":[1,2,3]}`, "application/json", 1, 2, xerror.ExecutionResponseSizeExceeded()},
		{"file exceeds bytes", `[1,2,3]`, "application/octet-stream", 1, 2, xerror.ExecutionResponseSizeExceeded()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)
			conn, err := New(transportsecurity.WithPlaintext(t.Context()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			execResult, err := conn.Execute(transportsecurity.WithPlaintext(t.Context()), []byte(`{"method":"GET","path":"/posts"}`), nil,
				types.ExecutionLimits{MaxResultRows: tc.rowLimit, HTTPMaxResponseBytes: tc.byteLimit})
			require.NoError(t, err)
			require.NotNil(t, execResult)
			if tc.wantErr != nil {
				require.ErrorIs(t, execResult.Error(), tc.wantErr)
				payload, ok := execResult.HTTP()
				require.True(t, ok)
				require.Nil(t, payload, "over-limit response must not be returned")
			} else {
				require.NoError(t, execResult.Error())
				payload, ok := execResult.HTTP()
				require.True(t, ok)
				require.Equal(t, tc.body, string(payload.RawBody))
			}
		})
	}
}

func TestConnectorExecute_ReportsCompletedWriteWhenResponseExceedsLimits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		limits  types.ExecutionLimits
		wantErr error
	}{
		{"rows", types.ExecutionLimits{MaxResultRows: 1, HTTPMaxResponseBytes: 1024, HTTPRetryAttempts: 3}, xerror.ExecutionRowLimitExceeded()},
		{"bytes", types.ExecutionLimits{MaxResultRows: 10, HTTPMaxResponseBytes: 2, HTTPRetryAttempts: 3}, xerror.ExecutionResponseSizeExceeded()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				writes.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`[1,2]`))
			}))
			t.Cleanup(server.Close)
			conn, err := New(transportsecurity.WithPlaintext(t.Context()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			got, err := conn.Execute(transportsecurity.WithPlaintext(t.Context()), []byte(`{"method":"POST","path":"/sink","body":{}}`), nil, tc.limits)
			require.NoError(t, err)
			require.ErrorIs(t, got.Error(), tc.wantErr)
			require.Contains(t, xerror.PublicDetail(got.Error()), "HTTP request completed with status 201")
			require.Contains(t, xerror.PublicDetail(got.Error()), "Verify remote state before retrying a write")
			payload, ok := got.HTTP()
			require.True(t, ok)
			require.Nil(t, payload)
			require.EqualValues(t, 1, writes.Load(), "the completed write must not be retried")
		})
	}
}

func TestConnectorExecute_ReturnsResponseSizeViolation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"payload":"too-large"}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"GET","path":"/users"}`),
		map[string]types.TypedParam{},
		types.ExecutionLimits{HTTPMaxResponseBytes: int64(len(`{"ok":true}`))},
	)

	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.Error(t, execResult.Error())
	require.True(t, xerror.Is(execResult.Error(), xerror.ExecutionResponseSizeExceeded()))
}

func TestConnectorExecute_ReturnsRequestSizeViolation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"POST","path":"/sink","body":"{{payload}}"}`),
		map[string]types.TypedParam{
			"payload": {Value: "this request body is too large", DataType: "text"},
		},
		types.ExecutionLimits{HTTPMaxRequestBytes: 8},
	)

	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.Error(t, execResult.Error())
	require.True(t, xerror.Is(execResult.Error(), xerror.ExecutionRequestSizeExceeded()))
}

func TestConnectorExecute_ReturnsRedirectLimitViolation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/middle", http.StatusFound)
		case "/middle":
			http.Redirect(w, r, "/end", http.StatusFound)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"GET","path":"/start"}`),
		map[string]types.TypedParam{},
		types.ExecutionLimits{HTTPMaxRedirects: 1, HTTPMaxResponseBytes: 1024},
	)

	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.Error(t, execResult.Error())
	require.True(t, xerror.Is(execResult.Error(), xerror.ExecutionRedirectLimitExceeded()))
}

func TestConnectorExecute_RetriesIdempotentTransientFailures(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"temporary"}`))
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"GET","path":"/users"}`),
		map[string]types.TypedParam{},
		types.ExecutionLimits{
			HTTPRetryAttempts:    2,
			HTTPRetryBackoff:     1 * time.Millisecond,
			HTTPMaxResponseBytes: 1024,
		},
	)

	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())
	payload, ok := execResult.HTTP()
	require.True(t, ok)
	require.Equal(t, http.StatusOK, payload.StatusCode)
	require.Equal(t, 2, requestCount)
}

func TestConnectorExecute_RetryDrainUsesResponseSizeLimit(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write(bytes.Repeat([]byte("x"), 128))
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"GET","path":"/users"}`),
		map[string]types.TypedParam{},
		types.ExecutionLimits{
			HTTPRetryAttempts:    2,
			HTTPRetryBackoff:     1 * time.Millisecond,
			HTTPMaxResponseBytes: 16,
		},
	)

	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())
	payload, ok := execResult.HTTP()
	require.True(t, ok)
	require.Equal(t, http.StatusOK, payload.StatusCode)
	require.Equal(t, 2, requestCount)
}

func TestConnectorExecute_DoesNotRetryMutatingMethods(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"temporary"}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"POST","path":"/users","body":{"name":"alice"}}`),
		map[string]types.TypedParam{},
		types.ExecutionLimits{
			HTTPRetryAttempts:    2,
			HTTPRetryBackoff:     1 * time.Millisecond,
			HTTPMaxResponseBytes: 1024,
			HTTPMaxRequestBytes:  1024,
		},
	)

	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())
	payload, ok := execResult.HTTP()
	require.True(t, ok)
	require.Equal(t, http.StatusInternalServerError, payload.StatusCode)
	require.Equal(t, 1, requestCount)
}

func TestConnectorExecute_SendsNativeCSVBodyFromInputPlaceholder(t *testing.T) {
	t.Parallel()

	type requestCapture struct {
		body        string
		contentType string
	}

	requests := make(chan requestCapture, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sink" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		defer func() { _ = r.Body.Close() }()

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		requests <- requestCapture{
			body:        string(bodyBytes),
			contentType: r.Header.Get("Content-Type"),
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"POST","path":"/sink","body":"{{input}}"}`),
		map[string]types.TypedParam{
			sinkbinding.Input: {
				Value:    "id,email\n1,alice@example.com\n2,bob@example.com\n",
				DataType: "text",
			},
			sinkbinding.InternalInputRepresentation: {
				Value:    model.ContractRepresentationCSV.String(),
				DataType: "text",
			},
		},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	received := <-requests
	require.Equal(t, "text/csv", received.contentType)
	require.Equal(t, "id,email\n1,alice@example.com\n2,bob@example.com\n", received.body)
}

func TestConnectorExecute_SendsNativeFileBodyFromInputPlaceholder(t *testing.T) {
	t.Parallel()

	type requestCapture struct {
		body        []byte
		contentType string
	}

	requests := make(chan requestCapture, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sink" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		defer func() { _ = r.Body.Close() }()

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		requests <- requestCapture{
			body:        bodyBytes,
			contentType: r.Header.Get("Content-Type"),
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	content := []byte{0, 1, 2}
	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"POST","path":"/sink","body":"{{input}}"}`),
		map[string]types.TypedParam{
			sinkbinding.Input: {
				Value:    content,
				DataType: "text",
			},
			sinkbinding.InternalInputRepresentation: {
				Value:    model.ContractRepresentationFile.String(),
				DataType: "text",
			},
			sinkbinding.InternalInputFileMIMEType: {
				Value:    "application/pdf",
				DataType: "text",
			},
		},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	received := <-requests
	require.Equal(t, "application/pdf", received.contentType)
	require.Equal(t, content, received.body)
}

func TestConnectorExecute_PreservesCallerProvidedContentTypeForNativeJSONBody(t *testing.T) {
	t.Parallel()

	type requestCapture struct {
		body        []map[string]any
		contentType string
	}

	requests := make(chan requestCapture, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sink" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		defer func() { _ = r.Body.Close() }()

		var body []map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		requests <- requestCapture{
			body:        body,
			contentType: r.Header.Get("Content-Type"),
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(
			`{"method":"POST","path":"/sink","headers":{"Content-Type":"application/vnd.braidplane.bulk+json"},"body":"{{payload}}"}`,
		),
		map[string]types.TypedParam{
			"payload": {
				Value: []any{
					map[string]any{"id": int64(1)},
					map[string]any{"id": int64(2)},
				},
				DataType: "text",
			},
		},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	received := <-requests
	require.Equal(t, "application/vnd.braidplane.bulk+json", received.contentType)
	require.Len(t, received.body, 2)
	require.InEpsilon(t, float64(1), received.body[0]["id"], 0)
	require.InEpsilon(t, float64(2), received.body[1]["id"], 0)
}

func TestConnectorExecute_PreservesCallerProvidedContentTypeForNativeCSVBody(t *testing.T) {
	t.Parallel()

	type requestCapture struct {
		body        string
		contentType string
	}

	requests := make(chan requestCapture, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sink" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		defer func() { _ = r.Body.Close() }()

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		requests <- requestCapture{
			body:        string(bodyBytes),
			contentType: r.Header.Get("Content-Type"),
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(
			`{"method":"POST","path":"/sink","headers":{"Content-Type":"text/plain"},"body":"{{input}}"}`,
		),
		map[string]types.TypedParam{
			sinkbinding.Input: {
				Value:    "id,email\n1,alice@example.com\n2,bob@example.com\n",
				DataType: "text",
			},
			sinkbinding.InternalInputRepresentation: {
				Value:    model.ContractRepresentationCSV.String(),
				DataType: "text",
			},
		},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	received := <-requests
	require.Equal(t, "text/plain", received.contentType)
	require.Equal(t, "id,email\n1,alice@example.com\n2,bob@example.com\n", received.body)
}

func TestConnectorExecute_ReturnsFileMetadataForOpaqueResponse(t *testing.T) {
	t.Parallel()

	content := []byte{0, 1, 2}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/download" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="../../users.bin"`)
		_, _ = w.Write(content)
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"GET","path":"/download"}`),
		map[string]types.TypedParam{},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	httpResult, ok := execResult.HTTP()
	require.True(t, ok)
	require.Empty(t, httpResult.Body)
	require.Equal(t, content, httpResult.RawBody)
	require.NotNil(t, httpResult.File)
	require.Equal(t, "users.bin", httpResult.File.Filename)
	require.Equal(t, "application/octet-stream", httpResult.File.MIMEType)
	require.EqualValues(t, len(content), httpResult.File.SizeBytes)
	require.Equal(t, filepayload.FilePayloadDigestSHA256, httpResult.File.DigestAlgorithm)
	require.NotEmpty(t, httpResult.File.Digest)
}

func TestConnectorExecute_SendsRawStringBodyForExplicitNonJSONContentType(t *testing.T) {
	t.Parallel()

	type requestCapture struct {
		body        string
		contentType string
	}

	requests := make(chan requestCapture, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sink" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		defer func() { _ = r.Body.Close() }()

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		requests <- requestCapture{
			body:        string(bodyBytes),
			contentType: r.Header.Get("Content-Type"),
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(
			`{"method":"POST","path":"/sink","headers":{"Content-Type":"text/plain; charset=utf-8"},"body":"{{message}}"}`,
		),
		map[string]types.TypedParam{
			"message": {
				Value:    "hello from braidplane",
				DataType: "text",
			},
		},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	received := <-requests
	require.Equal(t, "text/plain; charset=utf-8", received.contentType)
	require.Equal(t, "hello from braidplane", received.body)
}

func TestConnectorExecute_PreservesExplicitJSONNullBody(t *testing.T) {
	t.Parallel()

	type requestCapture struct {
		body        string
		contentType string
	}

	requests := make(chan requestCapture, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sink" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		defer func() { _ = r.Body.Close() }()

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		requests <- requestCapture{
			body:        string(bodyBytes),
			contentType: r.Header.Get("Content-Type"),
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"POST","path":"/sink","body":"{{payload}}"}`),
		map[string]types.TypedParam{
			"payload": {
				Value:    nil,
				DataType: "text",
			},
		},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	received := <-requests
	require.Equal(t, "application/json", received.contentType)
	require.JSONEq(t, "null", received.body)
}

func TestConnectorExecute_PreservesLiteralJSONNullBody(t *testing.T) {
	t.Parallel()

	type requestCapture struct {
		body        string
		contentType string
	}

	requests := make(chan requestCapture, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sink" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		defer func() { _ = r.Body.Close() }()

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		requests <- requestCapture{
			body:        string(bodyBytes),
			contentType: r.Header.Get("Content-Type"),
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"POST","path":"/sink","body":null}`),
		nil,
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	received := <-requests
	require.Equal(t, "application/json", received.contentType)
	require.JSONEq(t, "null", received.body)
}

func TestConnectorExecute_RejectsNullBodyForExplicitNonJSONContentType(t *testing.T) {
	t.Parallel()

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: "https://example.com", AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(
			`{"method":"POST","path":"/sink","headers":{"Content-Type":"text/plain"},"body":null}`,
		),
		nil,
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.ErrorContains(t, execResult.Error(), "null HTTP body requires a JSON content type")
}

func TestConnectorExecute_RejectsMissingBodyForExplicitNonJSONContentType(t *testing.T) {
	t.Parallel()

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: "https://example.com", AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(
			`{"method":"POST","path":"/sink","headers":{"Content-Type":"text/plain"}}`,
		),
		nil,
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.ErrorContains(
		t,
		execResult.Error(),
		"non-JSON HTTP content types require an explicit compatible body",
	)
}

func TestConnectorExecute_SendsFormEncodedBodyForExplicitFormContentType(t *testing.T) {
	t.Parallel()

	type requestCapture struct {
		body        string
		contentType string
	}

	requests := make(chan requestCapture, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sink" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		defer func() { _ = r.Body.Close() }()

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		requests <- requestCapture{
			body:        string(bodyBytes),
			contentType: r.Header.Get("Content-Type"),
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(
			`{"method":"POST","path":"/sink","headers":{"Content-Type":"application/x-www-form-urlencoded"},"body":{"name":"Alice","count":"42"}}`,
		),
		nil,
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	received := <-requests
	require.Equal(t, "application/x-www-form-urlencoded", received.contentType)

	parsed, err := url.ParseQuery(received.body)
	require.NoError(t, err)
	require.Equal(t, "Alice", parsed.Get("name"))
	require.Equal(t, "42", parsed.Get("count"))
}

func TestConnectorExecute_SendsMultipartBodyForExplicitMultipartContentType(t *testing.T) {
	t.Parallel()

	type requestCapture struct {
		contentType string
		values      map[string]string
	}

	requests := make(chan requestCapture, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sink" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		defer func() { _ = r.Body.Close() }()

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			requests <- requestCapture{contentType: r.Header.Get("Content-Type"), values: map[string]string{}}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if mediaType != "multipart/form-data" {
			requests <- requestCapture{contentType: r.Header.Get("Content-Type"), values: map[string]string{}}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if params["boundary"] == "" {
			requests <- requestCapture{contentType: r.Header.Get("Content-Type"), values: map[string]string{}}
			w.WriteHeader(http.StatusAccepted)
			return
		}

		reader := multipart.NewReader(r.Body, params["boundary"])
		form, err := reader.ReadForm(1024)
		if err != nil {
			requests <- requestCapture{contentType: r.Header.Get("Content-Type"), values: map[string]string{}}
			w.WriteHeader(http.StatusAccepted)
			return
		}

		values := make(map[string]string, len(form.Value))
		for key, entries := range form.Value {
			if len(entries) > 0 {
				values[key] = entries[0]
			}
		}

		requests <- requestCapture{
			contentType: r.Header.Get("Content-Type"),
			values:      values,
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(
			`{"method":"POST","path":"/sink","headers":{"Content-Type":"multipart/form-data"},"body":{"name":"Alice","count":"42"}}`,
		),
		nil,
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	received := <-requests
	require.Contains(t, received.contentType, "multipart/form-data")
	require.Contains(t, received.contentType, "boundary=")
	require.Equal(t, "Alice", received.values["name"])
	require.Equal(t, "42", received.values["count"])
}

func TestConnectorExecute_SendsMultipartFormPayloadWithGeneratedBoundary(t *testing.T) {
	t.Parallel()

	type requestCapture struct {
		contentType string
		mediaType   string
		boundary    string
		values      map[string][]string
		fileName    string
		fileContent string
	}

	requests := make(chan requestCapture, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sink" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		defer func() { _ = r.Body.Close() }()

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
			requests <- requestCapture{
				contentType: r.Header.Get("Content-Type"),
				mediaType:   mediaType,
				boundary:    params["boundary"],
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}

		reader := multipart.NewReader(r.Body, params["boundary"])
		form, err := reader.ReadForm(1024)
		if err != nil {
			requests <- requestCapture{
				contentType: r.Header.Get("Content-Type"),
				mediaType:   mediaType,
				boundary:    params["boundary"],
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		defer func() { _ = form.RemoveAll() }()

		uploads := form.File["upload"]
		if len(uploads) == 0 {
			requests <- requestCapture{
				contentType: r.Header.Get("Content-Type"),
				mediaType:   mediaType,
				boundary:    params["boundary"],
				values:      form.Value,
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		file, err := uploads[0].Open()
		if err != nil {
			requests <- requestCapture{
				contentType: r.Header.Get("Content-Type"),
				mediaType:   mediaType,
				boundary:    params["boundary"],
				values:      form.Value,
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		defer func() { _ = file.Close() }()
		fileBytes, err := io.ReadAll(file)
		if err != nil {
			requests <- requestCapture{
				contentType: r.Header.Get("Content-Type"),
				mediaType:   mediaType,
				boundary:    params["boundary"],
				values:      form.Value,
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}

		requests <- requestCapture{
			contentType: r.Header.Get("Content-Type"),
			mediaType:   mediaType,
			boundary:    params["boundary"],
			values:      form.Value,
			fileName:    uploads[0].Filename,
			fileContent: string(fileBytes),
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"POST","path":"/sink","body":"{{input}}"}`),
		map[string]types.TypedParam{
			sinkbinding.Input: {
				Value: &formpayload.FormData{
					Encoding: formpayload.FormEncodingMultipart,
					Fields: []formpayload.FormField{
						{Name: "name", Values: []string{"Alice"}},
						{Name: "tag", Values: []string{"urgent", "customer"}},
					},
					Files: []formpayload.FormFile{{
						Name:        "upload",
						Filename:    "../import.csv",
						ContentType: "text/csv",
						Content:     []byte("id\n1\n"),
					}},
				},
				DataType: "text",
			},
			sinkbinding.InternalInputRepresentation: {
				Value:    model.ContractRepresentationMultipartForm.String(),
				DataType: "text",
			},
		},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())

	received := <-requests
	require.Contains(t, received.contentType, "boundary=")
	require.Equal(t, "multipart/form-data", received.mediaType)
	require.NotEmpty(t, received.boundary)
	require.Equal(t, []string{"Alice"}, received.values["name"])
	require.Equal(t, []string{"urgent", "customer"}, received.values["tag"])
	require.Equal(t, "import.csv", received.fileName)
	require.Equal(t, "id\n1\n", received.fileContent)
}

func TestConnectorExecute_RejectsOversizedResponseBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, maxExecuteResponseSizeBytes+32))
	}))
	t.Cleanup(server.Close)

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: server.URL, AllowPrivateHosts: true},
		Credentials{},
	)
	require.NoError(t, err)

	execResult, execErr := connector.Execute(
		transportsecurity.WithPlaintext(context.Background()),
		[]byte(`{"method":"GET","path":"/"}`),
		nil,
	)
	require.NoError(t, execErr)
	require.ErrorContains(t, execResult.Error(), "response body exceeds")
}

func TestEncodeFormBody_StringifiesScalarValues(t *testing.T) {
	t.Parallel()

	encoded, err := encodeFormBody(map[string]any{
		"name":    "Alice",
		"count":   int64(42),
		"enabled": true,
		"empty":   nil,
		"tag":     []any{"urgent", "customer"},
	})
	require.NoError(t, err)
	require.Equal(t, "count=42&empty=&enabled=true&name=Alice&tag=urgent&tag=customer", encoded)
}

func TestEncodeFormBody_RejectsNestedObject(t *testing.T) {
	t.Parallel()

	_, err := encodeFormBody(map[string]any{
		"user": map[string]any{"name": "Alice"},
	})
	require.ErrorContains(t, err, `form-encoded body field "user" must be scalar`)
}

func TestEncodeFormBody_RejectsFileParts(t *testing.T) {
	t.Parallel()

	_, err := encodeFormBody(&formpayload.FormData{
		Files: []formpayload.FormFile{{
			Name:    "upload",
			Content: []byte("file"),
		}},
	})
	require.ErrorContains(t, err, "form-encoded body cannot include file parts")
}

func TestEncodeMultipartBody_RejectsNonObject(t *testing.T) {
	t.Parallel()

	_, _, err := encodeMultipartBody("not-an-object", "")
	require.ErrorContains(t, err, "multipart body must resolve to object or multipart_form content")
}

func TestIsJSONContentType(t *testing.T) {
	t.Parallel()

	require.True(t, isJSONContentType("application/json"))
	require.True(t, isJSONContentType("application/vnd.braidplane.bulk+json; charset=utf-8"))
	require.False(t, isJSONContentType("text/plain"))
}
