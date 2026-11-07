package connector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func TestNewConnectorWithTracingRecordsSafeConnectorSpans(t *testing.T) { // This test mutates the global OpenTelemetry provider.
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	base := func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (Connector, error) {
		return fakeConnector{}, nil
	}

	factory := NewConnectorWithTracing(base)
	conn, err := factory(
		context.Background(),
		model.DataSourceTypeHTTP,
		[]byte(`{"base_url":"https://example.com","api_key":"secret-config"}`),
		[]byte(`{"bearer_token":"secret-token"}`),
	)
	require.NoError(t, err)

	execResult, err := conn.Execute(
		context.Background(),
		[]byte(`{"method":"POST","path":"/customers?token=secret-query"}`),
		map[string]types.TypedParam{
			"api_key": {Value: "secret-param", DataType: "text"},
		},
	)
	require.NoError(t, err)
	require.NoError(t, execResult.Error())

	require.Len(t, recorder.Ended(), 1)
	span := recorder.Ended()[0]
	require.Equal(t, "connector.http.execute", span.Name())

	attrs := connectorSpanAttributes(span)
	require.Equal(t, "http", attrs["data_source.type"])
	require.Equal(t, "execute", attrs["connector.operation"])

	rawSpan := spanToString(span)
	require.NotContains(t, rawSpan, "secret-config")
	require.NotContains(t, rawSpan, "secret-token")
	require.NotContains(t, rawSpan, "secret-query")
	require.NotContains(t, rawSpan, "secret-param")
}

func TestNewConnectorWithTracingRecordsConnectorMetrics(t *testing.T) { // This test mutates the global metrics meter.
	meter, err := metrics.New(metrics.Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)
	restore := metrics.SetProcessMeter(meter)
	t.Cleanup(restore)

	base := func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (Connector, error) {
		return fakeConnector{}, nil
	}

	factory := NewConnectorWithTracing(base)
	conn, err := factory(
		context.Background(),
		model.DataSourceTypeHTTP,
		[]byte(`{"base_url":"https://example.com"}`),
		nil,
	)
	require.NoError(t, err)

	execResult, err := conn.Execute(
		context.Background(),
		[]byte(`{"method":"GET","path":"/customers"}`),
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, execResult)

	app := fiber.New()
	meter.RegisterRoutes(app)

	resp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	body := string(bodyBytes)
	require.Contains(t, body, "braidplane_connector_operation_duration_seconds")
	require.Contains(t, body, "braidplane_connector_operation_total")
	require.Contains(t, body, "braidplane_connector_result_rows")
	require.Contains(t, body, "braidplane_connector_result_bytes")
	require.Contains(t, body, `data_source_type="http"`)
	require.Contains(t, body, `connector_operation="execute"`)
	require.Contains(t, body, `outcome="success"`)
}

func TestNewConnectorWithTracingSkipsResultHistogramsForNonExecutionOperations(t *testing.T) { // This test mutates the global metrics meter.
	meter, err := metrics.New(metrics.Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)
	restore := metrics.SetProcessMeter(meter)
	t.Cleanup(restore)

	base := func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (Connector, error) {
		return fakeConnector{}, nil
	}

	factory := NewConnectorWithTracing(base)
	conn, err := factory(
		context.Background(),
		model.DataSourceTypeHTTP,
		[]byte(`{"base_url":"https://example.com"}`),
		nil,
	)
	require.NoError(t, err)
	require.NoError(t, conn.TestConnection(context.Background()))

	app := fiber.New()
	meter.RegisterRoutes(app)

	resp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	body := string(bodyBytes)
	require.Contains(t, body, "braidplane_connector_operation_total")
	require.Contains(t, body, `connector_operation="test_connection"`)
	require.NotContains(t, body, "braidplane_connector_result_rows_bucket")
	require.NotContains(t, body, "braidplane_connector_result_bytes_bucket")
}

func TestConnectorResultBytesUsesOnlyKnownPayloadSizes(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		int64(len(`{"ok":true}`)),
		connectorResultBytes(result.NewHTTPResult(
			&result.HTTPResult{Body: json.RawMessage(`{"ok":true}`)},
			1,
			nil,
		)),
	)

	writtenBytes := 128
	require.Equal(
		t,
		int64(writtenBytes),
		connectorResultBytes(result.NewGitResult(
			&result.FileResult{WrittenBytes: &writtenBytes},
			1,
			nil,
		)),
	)

	require.Equal(
		t,
		int64(-1),
		connectorResultBytes(result.NewSQLResult(
			"postgres",
			&result.SQLResult{
				Rows: []map[string]any{{
					"payload": strings.Repeat("x", 1024),
				}},
				RowCount: 1,
			},
			1,
			nil,
		)),
	)
}

type fakeConnector struct{}

func (fakeConnector) Execute(
	context.Context,
	json.RawMessage,
	map[string]types.TypedParam,
	...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	return result.NewHTTPResult(&result.HTTPResult{StatusCode: 200}, 1, nil), nil
}

func (fakeConnector) TestConnection(context.Context) error {
	return nil
}

func (fakeConnector) Close() error {
	return nil
}

func connectorSpanAttributes(span sdktrace.ReadOnlySpan) map[string]any {
	attrs := make(map[string]any)
	for _, attr := range span.Attributes() {
		attrs[string(attr.Key)] = attr.Value.AsInterface()
	}
	return attrs
}

func spanToString(span sdktrace.ReadOnlySpan) string {
	raw, err := json.Marshal(span.Attributes())
	if err != nil {
		return ""
	}
	return string(raw)
}
