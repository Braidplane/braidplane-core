package logging

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"

	"github.com/Braidplane/braidplane-core/pkg/reqctx"
)

func TestSetupWritesJSONWithStandardResourceFields(t *testing.T) {
	t.Parallel()

	logPath := filepath.Join(t.TempDir(), "backend.log")
	logger, closer, err := Setup(Config{
		Level:            "info",
		FilePath:         logPath,
		RotationInterval: time.Hour,
		MaxFileSizeMB:    100,
		Environment:      "test",
	}, "http")
	require.NoError(t, err)

	logger.InfoContext(context.Background(), "server started")
	require.NoError(t, closer.Close())

	entry := readSingleJSONLogEntry(t, logPath)
	require.Equal(t, "info", entry["level"])
	require.Equal(t, "server started", entry["msg"])
	require.Equal(t, "braidplane", entry["service.name"])
	require.Equal(t, "test", entry["deployment.environment"])
	require.Equal(t, "http", entry["braidplane.component"])
	require.NotContains(t, entry, "component")
}

func TestSetupExportsOTLPLogsWhenEndpointConfigured(t *testing.T) {
	t.Parallel()

	requests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger, closer, err := Setup(Config{
		Level:            "info",
		RotationInterval: time.Hour,
		MaxFileSizeMB:    100,
		Environment:      "test",
		OTLPLogsEndpoint: server.URL + "/v1/logs",
	}, "backend")
	require.NoError(t, err)

	logger.InfoContext(context.Background(), "export me")
	require.NoError(t, closer.Close())

	select {
	case path := <-requests:
		require.Equal(t, "/v1/logs", path)
	case <-time.After(2 * time.Second):
		require.Fail(t, "expected OTLP logs export request")
	}
}

func TestSetupUsesServiceScopeForOTLPLogs(t *testing.T) {
	t.Parallel()

	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read OTLP request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		requests <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger, closer, err := Setup(Config{
		Level:            "info",
		RotationInterval: time.Hour,
		MaxFileSizeMB:    100,
		Environment:      "test",
		OTLPLogsEndpoint: server.URL + "/v1/logs",
	}, "backend")
	require.NoError(t, err)

	logger.InfoContext(context.Background(), "export me")
	require.NoError(t, closer.Close())

	select {
	case body := <-requests:
		var req collectorlogsv1.ExportLogsServiceRequest
		require.NoError(t, proto.Unmarshal(body, &req))
		require.NotEmpty(t, req.ResourceLogs)
		require.NotEmpty(t, req.ResourceLogs[0].ScopeLogs)
		resourceAttrs := otlpResourceAttrs(req.ResourceLogs[0].Resource.Attributes)
		require.Equal(t, "braidplane", resourceAttrs["service.name"])
		require.Equal(t, "test", resourceAttrs["deployment.environment"])
		require.Equal(t, "backend", resourceAttrs["braidplane.component"])
		scopeName := req.ResourceLogs[0].ScopeLogs[0].Scope.Name
		require.Equal(t, "braidplane", scopeName)
		require.NotContains(t, scopeName, "github.com")
		recordAttrs := otlpResourceAttrs(req.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes)
		require.NotContains(t, recordAttrs, "service.name")
		require.NotContains(t, recordAttrs, "deployment.environment")
		require.NotContains(t, recordAttrs, "braidplane.component")
	case <-time.After(2 * time.Second):
		require.Fail(t, "expected OTLP logs export request")
	}
}

func TestSetupExportsOTLPSourceMetadata(t *testing.T) {
	t.Parallel()

	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read OTLP request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		requests <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger, closer, err := Setup(Config{
		Level:            "info",
		RotationInterval: time.Hour,
		MaxFileSizeMB:    100,
		Environment:      "test",
		OTLPLogsEndpoint: server.URL + "/v1/logs",
	}, "backend")
	require.NoError(t, err)

	logOTLPSourceTestRecord(logger)
	require.NoError(t, closer.Close())

	select {
	case body := <-requests:
		var req collectorlogsv1.ExportLogsServiceRequest
		require.NoError(t, proto.Unmarshal(body, &req))
		record := req.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
		attrs := otlpRecordAttrs(record.Attributes)
		require.Contains(t, attrs["code.file.path"].GetStringValue(), "pkg/logging/logging_test.go")
		require.Contains(t, attrs["code.function.name"].GetStringValue(), "logOTLPSourceTestRecord")
		require.Positive(t, attrs["code.line.number"].GetIntValue())
	case <-time.After(2 * time.Second):
		require.Fail(t, "expected OTLP logs export request")
	}
}

func logOTLPSourceTestRecord(logger *slog.Logger) {
	logger.InfoContext(context.Background(), "export source metadata")
}

func otlpRecordAttrs(attrs []*commonv1.KeyValue) map[string]*commonv1.AnyValue {
	result := make(map[string]*commonv1.AnyValue, len(attrs))
	for _, attr := range attrs {
		result[attr.GetKey()] = attr.GetValue()
	}
	return result
}

func otlpResourceAttrs(attrs []*commonv1.KeyValue) map[string]string {
	result := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		if attr.GetValue() == nil {
			continue
		}
		result[attr.GetKey()] = attr.GetValue().GetStringValue()
	}
	return result
}

func TestContextAttrsIncludesRequestAndTraceIDs(t *testing.T) {
	t.Parallel()

	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
		Remote:  true,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanContext)
	ctx = reqctx.WithRequestID(ctx, "req-123")

	attrs := attrsByKey(ContextAttrs(ctx))

	require.Equal(t, "req-123", attrs["request_id"].Value.String())
	require.Equal(t, traceID.String(), attrs["trace_id"].Value.String())
	require.Equal(t, spanID.String(), attrs["span_id"].Value.String())
}

func TestContextAttrsIncludesAllowlistedLogAttrs(t *testing.T) {
	t.Parallel()

	ctx := WithAttrs(
		context.Background(),
		slog.String("organization.slug", "acme"),
		slog.String("query.slug", "active-users"),
		slog.String("url.query", "access_token=secret"),
	)

	attrs := attrsByKey(ContextAttrs(ctx))

	require.Equal(t, "acme", attrs["organization.slug"].Value.String())
	require.Equal(t, "active-users", attrs["query.slug"].Value.String())
	require.NotContains(t, attrs, "url.query")
}

func TestContextAttrsUsesLatestAllowlistedLogAttr(t *testing.T) {
	t.Parallel()

	ctx := WithAttrs(context.Background(), slog.String("organization.slug", "old"))
	ctx = WithAttrs(ctx, slog.String("organization.slug", "new"))

	attrs := attrsByKey(ContextAttrs(ctx))

	require.Equal(t, "new", attrs["organization.slug"].Value.String())
}

func readSingleJSONLogEntry(t *testing.T, path string) map[string]any {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(raw, &entry))
	return entry
}

func attrsByKey(attrs []slog.Attr) map[string]slog.Attr {
	result := make(map[string]slog.Attr, len(attrs))
	for _, attr := range attrs {
		result[attr.Key] = attr
	}
	return result
}

func TestFileRotationRetainsOnlyOnePreviousFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	writer, err := newRotatingWriter(path, time.Hour, 1)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Close()) })
	for _, marker := range []byte{'a', 'b', 'c'} {
		record := make([]byte, 1024*1024)
		for i := range record {
			record[i] = marker
		}
		_, err = writer.Write(record)
		require.NoError(t, err)
	}
	current, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Len(t, current, 1024*1024)
	require.Equal(t, byte('c'), current[0])
	previous, err := os.ReadFile(path + ".1")
	require.NoError(t, err)
	require.Len(t, previous, 1024*1024)
	require.Equal(t, byte('b'), previous[0])
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestTimeRotationAndRestartPreservePreviousRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	require.NoError(t, os.WriteFile(path, []byte("before restart\n"), 0o600))
	writer, err := newRotatingWriter(path, time.Hour, 1)
	require.NoError(t, err)
	writer.nextRotation = time.Now().Add(-time.Second)
	_, err = writer.Write([]byte("after rotation\n"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	old, err := os.ReadFile(path + ".1")
	require.NoError(t, err)
	require.Equal(t, "before restart\n", string(old))
	current, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "after rotation\n", string(current))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestEmptyEndpointIgnoresAmbientOTLPExportSettings(t *testing.T) {
	var calls atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", collector.URL+"/v1/logs")
	path := filepath.Join(t.TempDir(), "local.log")
	logger, closer, err := Setup(Config{Level: "info", FilePath: path, RotationInterval: time.Hour, MaxFileSizeMB: 1}, "synthetic")
	require.NoError(t, err)
	logger.Info("local-only synthetic record")
	require.NoError(t, closer.Close())
	require.Zero(t, calls.Load(), "an empty application endpoint must disable log exports")
	entry := readSingleJSONLogEntry(t, path)
	require.Equal(t, "local-only synthetic record", entry["msg"])
}
