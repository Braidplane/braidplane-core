package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	connectorresult "github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/tracing"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestPostgresExecutionErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		want  error
	}{
		{"missing column", &pgconn.PgError{Code: "42703", Message: "private-column-secret"}, xerror.SourceSchemaReferenceInvalid()},
		{"missing relation", &pgconn.PgError{Code: "42P01", Message: "private-relation-secret"}, xerror.SourceSchemaReferenceInvalid()},
		{"missing schema", &pgconn.PgError{Code: "3F000", Message: "private-schema-secret"}, xerror.SourceSchemaReferenceInvalid()},
		{"syntax", &pgconn.PgError{Code: "42601", Message: "private-query-secret"}, xerror.SourceQueryInvalid()},
		{"check", &pgconn.PgError{Code: "23514", Message: "private-row-secret", Detail: "private-detail-secret", ConstraintName: "private-constraint-secret"}, xerror.DestinationConstraintViolation()},
		{"unique", &pgconn.PgError{Code: "23505", Message: "private-row-secret", Detail: "private-detail-secret", ConstraintName: "private-constraint-secret"}, xerror.DestinationConstraintViolation()},
		{"foreign key", &pgconn.PgError{Code: "23503", Message: "private-row-secret", Detail: "private-detail-secret", ConstraintName: "private-constraint-secret"}, xerror.DestinationConstraintViolation()},
		{"not null", &pgconn.PgError{Code: "23502", Message: "private-row-secret", Detail: "private-detail-secret", ConstraintName: "private-constraint-secret"}, xerror.DestinationConstraintViolation()},
		{"exclusion", &pgconn.PgError{Code: "23P01", Message: "private-row-secret", Detail: "private-detail-secret", ConstraintName: "private-constraint-secret"}, xerror.DestinationConstraintViolation()},
		{"permission", &pgconn.PgError{Code: "42501", Message: "private-sql-secret"}, xerror.Forbidden(xerror.DataSource)},
		{"authentication", &pgconn.PgError{Code: "28P01", Message: "private-password-secret"}, xerror.InvalidCredentials()},
		{"timeout", context.DeadlineExceeded, xerror.Timeout(xerror.DataSource)},
		{"unreachable", &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("private-host-secret")}, xerror.ConnectionFailed(xerror.DataSource)},
		{"unknown", &pgconn.PgError{Code: "XX000", Message: "private-provider-secret"}, xerror.Unexpected()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := mapPostgresError(fmt.Errorf("wrapped: %w", tc.cause))
			require.ErrorIs(t, err, tc.want)
			var logs bytes.Buffer
			logging.LogMethodCompletion(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)), logging.MethodCompletion{Layer: "service", Method: "Execute", Err: err})
			require.NotEmpty(t, logs.String())
			if xerror.Is(err, xerror.InvalidCredentials()) || xerror.Is(err, xerror.DestinationConstraintViolation()) {
				require.NotContains(t, logs.String(), "private-")
				require.NotContains(t, err.Error(), "private-")
				stored, marshalErr := json.Marshal(connectorresult.NewSQLResult("postgres", &connectorresult.SQLResult{}, 0, err))
				require.NoError(t, marshalErr)
				require.NotContains(t, string(stored), "private-")
			} else {
				require.Contains(t, logs.String(), tc.cause.Error())
			}
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
			_, span := provider.Tracer("test").Start(t.Context(), "connector.postgres.execute")
			tracing.RecordError(span, err)
			span.End()
			require.Len(t, recorder.Ended(), 1)
			ended := recorder.Ended()[0]
			encoded, marshalErr := json.Marshal([]any{ended.Attributes(), ended.Events(), ended.Status()})
			require.NoError(t, marshalErr)
			require.NotContains(t, string(encoded), "private-")
		})
	}
}
