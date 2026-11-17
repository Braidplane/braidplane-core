package tracing

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestRecordErrorUsesBoundedClassifications(t *testing.T) {
	t.Parallel()
	const canary = "opaque-canary"
	network := &url.Error{URL: "https://" + canary, Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}
	for _, tt := range []struct {
		name, code, kind, category string
		err                        error
	}{
		{name: "unknown", err: fmt.Errorf("%s", canary), code: "error"},
		{name: "canceled", err: context.Canceled, code: "request_canceled", category: "request_canceled"},
		{name: "deadline", err: context.DeadlineExceeded, code: "deadline_exceeded", category: "deadline_exceeded"},
		{name: "timeout", err: &net.DNSError{Name: canary, IsTimeout: true}, code: "timeout", category: "timeout"},
		{name: "DNS", err: &net.DNSError{Name: canary}, code: "dns_error", category: "dns_error"},
		{name: "network", err: network, code: "network_error", category: "network_error"},
		{name: "database", err: &pgconn.PgError{Code: "XX000", Detail: canary}, code: "database_error", category: "database_error"},
		{name: "typed", err: xerror.Unexpected().With(network), code: "INTERNAL_ERROR", kind: "internal", category: "network_error"},
		{name: "no formatting", err: unformattableError{}, code: "error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			defer func() { require.NoError(t, provider.Shutdown(context.Background())) }()
			_, span := provider.Tracer("test").Start(t.Context(), "failure")
			err := xerror.Join(fmt.Errorf("%s", canary), xerror.Join(tt.err))
			RecordError(span, err)
			span.End()

			ended := recorder.Ended()[0]
			attrs := spanAttributes(ended)
			require.Equal(t, codes.Error, ended.Status().Code)
			require.Equal(t, tt.code, ended.Status().Description)
			require.Equal(t, tt.code, attrs["error.type"])
			if tt.kind == "" {
				require.NotContains(t, attrs, "error.kind")
			} else {
				require.Equal(t, tt.kind, attrs["error.kind"])
			}
			if tt.category == "" {
				require.NotContains(t, attrs, "error.category")
			} else {
				require.Equal(t, tt.category, attrs["error.category"])
			}
			require.Empty(t, ended.Events())
			encoded, err := json.Marshal(attrs)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), canary)
		})
	}
	require.Empty(t, ErrorAttributes(nil))
}

type unformattableError struct{}

func (unformattableError) Error() string { panic("classification formatted the error") }
