package connector

import (
	"context"
	stdsql "database/sql"
	"encoding/json"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/tracing"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	hubspotconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	mongoconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/mongo"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

type connectorOperationFunc[T any] func(context.Context) (T, error)

// NewConnectorWithTracing wraps connector operations with safe OpenTelemetry spans.
func NewConnectorWithTracing(base NewConnectorFunc) NewConnectorFunc {
	if base == nil {
		base = NewConnector
	}

	return func(
		ctx context.Context,
		dsType model.DataSourceType,
		configJSON, credentialsJSON []byte,
	) (Connector, error) {
		conn, err := base(ctx, dsType, configJSON, credentialsJSON)
		if err != nil {
			return nil, err
		}

		return wrapTracingConnector(conn, dsType), nil
	}
}

func wrapTracingConnector(conn Connector, dsType model.DataSourceType) Connector {
	base := tracingConnector{base: conn, dsType: dsType}

	if introspector, ok := conn.(sqlconnector.SQLSchemaIntrospector); ok {
		return tracingSQLConnector{tracingConnector: base, introspector: introspector}
	}
	if introspector, ok := conn.(mongoconnector.SchemaIntrospector); ok {
		return tracingMongoConnector{tracingConnector: base, introspector: introspector}
	}
	if introspector, ok := conn.(httpconnector.HTTPSchemaIntrospector); ok {
		return tracingHTTPConnector{tracingConnector: base, introspector: introspector}
	}
	if introspector, ok := conn.(hubspotconnector.SchemaIntrospector); ok {
		return tracingHubSpotConnector{tracingConnector: base, introspector: introspector}
	}
	if introspector, ok := conn.(filedata.SchemaIntrospector); ok {
		return tracingFileConnector{tracingConnector: base, introspector: introspector}
	}

	return base
}

type tracingConnector struct {
	base   Connector
	dsType model.DataSourceType
}

func (c tracingConnector) ExecuteScoped(ctx context.Context, queryData json.RawMessage, params map[string]types.TypedParam, scope resourcescope.Scope, limits ...types.ExecutionLimits) (*result.ExecutionResult, error) {
	return traceConnectorOperation(ctx, c, "execute", func(ctx context.Context) (*result.ExecutionResult, error) {
		return Execute(ctx, c.base, queryData, params, &scope, types.ResolveExecutionLimits(limits...))
	}, nil)
}

func (c tracingConnector) Execute(
	ctx context.Context,
	queryData json.RawMessage,
	params map[string]types.TypedParam,
	limits ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	return traceConnectorOperation(
		ctx,
		c,
		"execute",
		func(ctx context.Context) (*result.ExecutionResult, error) {
			return c.base.Execute(ctx, queryData, params, limits...)
		},
		func(span trace.Span, result *result.ExecutionResult) {
			if result == nil {
				return
			}
			if resultErr := result.Error(); resultErr != nil {
				tracing.RecordError(span, resultErr)
			}
		},
	)
}

func (c tracingConnector) TestConnection(ctx context.Context) error {
	_, err := traceConnectorOperation(
		ctx,
		c,
		"test_connection",
		func(ctx context.Context) (struct{}, error) {
			return struct{}{}, c.base.TestConnection(ctx)
		},
		nil,
	)
	return err
}

func (c tracingConnector) Close() error {
	return c.base.Close()
}

// SQLPoolStats preserves optional SQL pool observability through tracing wrappers.
func (c tracingConnector) SQLPoolStats() stdsql.DBStats {
	provider, ok := c.base.(interface {
		SQLPoolStats() stdsql.DBStats
	})
	if !ok {
		return stdsql.DBStats{}
	}
	return provider.SQLPoolStats()
}

func (c tracingConnector) startSpan(ctx context.Context, operation string) (context.Context, trace.Span) {
	dsType := dataSourceTypeName(c.dsType)
	return otel.Tracer("braidplane/connector").Start( //nolint:spancheck // Caller receives the span and owns End.
		ctx,
		"connector."+dsType+"."+operation,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("data_source.type", dsType),
			attribute.String("connector.operation", operation),
		),
	)
}

func dataSourceTypeName(dsType model.DataSourceType) string {
	value := dsType.String()
	if value == "" {
		return "unknown"
	}
	return value
}

func traceConnectorOperation[T any](
	ctx context.Context,
	conn tracingConnector,
	operation string,
	call connectorOperationFunc[T],
	observe func(trace.Span, T),
) (T, error) {
	ctx, span := conn.startSpan(ctx, operation)
	defer span.End()
	startedAt := time.Now()

	value, err := call(ctx)
	recordConnectorMetrics(conn.dsType, operation, value, err, time.Since(startedAt))
	if err != nil {
		tracing.RecordError(span, err)
		return value, err
	}
	if observe != nil {
		observe(span, value)
	}
	return value, nil
}

func recordConnectorMetrics[T any](
	dsType model.DataSourceType,
	operation string,
	value T,
	err error,
	duration time.Duration,
) {
	resultRows := -1
	resultBytes := int64(-1)
	if execResult, ok := any(value).(*result.ExecutionResult); ok && execResult != nil {
		resultRows = connectorResultRows(execResult)
		resultBytes = connectorResultBytes(execResult)
		if execErr := execResult.Error(); execErr != nil {
			err = execErr
		}
	}

	metrics.RecordConnectorOperation(metrics.ConnectorOperationMetric{
		DataSourceType: dataSourceTypeName(dsType),
		Operation:      operation,
		Outcome:        connectorOutcome(err),
		ErrorType:      connectorErrorType(err),
		Duration:       duration,
		ResultRows:     resultRows,
		ResultBytes:    resultBytes,
	})
}

func connectorOutcome(err error) string {
	if err == nil {
		return "success"
	}
	return "error"
}

func connectorErrorType(err error) string {
	if err == nil {
		return "none"
	}

	var appErr *xerror.Error
	if xerror.As(err, &appErr) {
		return appErr.Code()
	}

	return "error"
}

func connectorResultRows(execResult *result.ExecutionResult) int {
	if execResult == nil {
		return 0
	}
	if sqlResult, ok := execResult.SQL(); ok && sqlResult != nil {
		return sqlResult.RowCount
	}
	if mongoResult, ok := execResult.MongoDB(); ok && mongoResult != nil {
		if mongoResult.DocumentCount != nil {
			return *mongoResult.DocumentCount
		}
		return len(mongoResult.Documents)
	}
	if objectRowsResult, ok := execResult.ObjectRows(); ok && objectRowsResult != nil {
		return objectRowsResult.RowCount
	}
	if fileResult, ok := execResult.File(); ok && fileResult != nil {
		return fileResult.RowCount
	}
	return 0
}

func connectorResultBytes(execResult *result.ExecutionResult) int64 {
	if execResult == nil {
		return -1
	}
	if httpResult, ok := execResult.HTTP(); ok && httpResult != nil {
		return int64(len(httpResult.Body))
	}
	if fileResult, ok := execResult.File(); ok && fileResult != nil && fileResult.WrittenBytes != nil {
		return int64(*fileResult.WrittenBytes)
	}
	return -1
}

type tracingSQLConnector struct {
	tracingConnector
	introspector sqlconnector.SQLSchemaIntrospector
}

func (c tracingSQLConnector) IntrospectSchema(ctx context.Context) (*sqlconnector.Schema, error) {
	return traceConnectorOperation(
		ctx,
		c.tracingConnector,
		"introspect_schema",
		c.introspector.IntrospectSchema,
		nil,
	)
}

type tracingMongoConnector struct {
	tracingConnector
	introspector mongoconnector.SchemaIntrospector
}

func (c tracingMongoConnector) IntrospectSchema(ctx context.Context) (*mongoconnector.Schema, error) {
	return traceConnectorOperation(
		ctx,
		c.tracingConnector,
		"introspect_schema",
		c.introspector.IntrospectSchema,
		nil,
	)
}

type tracingHTTPConnector struct {
	tracingConnector
	introspector httpconnector.HTTPSchemaIntrospector
}

func (c tracingHTTPConnector) IntrospectSchema(ctx context.Context) (*httpconnector.HTTPSchema, error) {
	return traceConnectorOperation(
		ctx,
		c.tracingConnector,
		"introspect_schema",
		c.introspector.IntrospectSchema,
		nil,
	)
}

type tracingHubSpotConnector struct {
	tracingConnector
	introspector hubspotconnector.SchemaIntrospector
}

func (c tracingHubSpotConnector) IntrospectSchema(ctx context.Context) (*hubspotconnector.Schema, error) {
	return traceConnectorOperation(
		ctx,
		c.tracingConnector,
		"introspect_schema",
		c.introspector.IntrospectSchema,
		nil,
	)
}

type tracingFileConnector struct {
	tracingConnector
	introspector filedata.SchemaIntrospector
}

func (c tracingFileConnector) IntrospectSchema(ctx context.Context) (*filedata.Schema, error) {
	return traceConnectorOperation(
		ctx,
		c.tracingConnector,
		"introspect_schema",
		c.introspector.IntrospectSchema,
		nil,
	)
}
