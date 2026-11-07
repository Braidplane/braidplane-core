package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

func TestRegisterRoutesDoesNotExposeMetricsWhenDisabled(t *testing.T) {
	t.Parallel()

	meter, err := New(Config{
		Enabled:     false,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)

	app := fiber.New()
	meter.RegisterRoutes(app)

	resp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestRegisterRoutesExposesPrometheusRuntimeMetricsWhenEnabled(t *testing.T) {
	t.Parallel()

	meter, err := New(Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)

	app := fiber.New()
	meter.RegisterRoutes(app)

	resp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	contentType := resp.Header.Get("Content-Type")
	require.Contains(t, contentType, "text/plain")
	require.NotContains(t, contentType, ",")

	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	body := string(bodyBytes)
	require.Contains(t, body, "braidplane_target_info")
	require.Contains(t, body, `service_name="backend"`)
	require.Contains(t, body, `deployment_environment="test"`)
	require.Contains(t, body, "go_goroutines")
	require.Contains(t, body, "process_cpu_seconds_total")
}

func TestHTTPMiddlewareRecordsRouteTemplateWithoutRawSlugs(t *testing.T) {
	t.Parallel()

	meter, err := New(Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)

	app := fiber.New()
	app.Use(meter.HTTPMiddleware())
	app.Get("/orgs/:orgSlug/data-sources/:dataSourceSlug/test", func(c fiber.Ctx) error {
		return c.SendStatus(http.StatusNoContent)
	})
	meter.RegisterRoutes(app)

	resp, err := app.Test(
		httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/orgs/acme/data-sources/slow-prod/test",
			strings.NewReader("")),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)

	resp, err = app.Test(
		httptest.NewRequestWithContext(context.Background(),
			http.MethodGet,
			"/orgs/acme/data-sources/slow-prod/test",
			nil),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	metricsResp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, metricsResp.Body.Close())
	})
	require.Equal(t, http.StatusOK, metricsResp.StatusCode)

	bodyBytes, err := io.ReadAll(metricsResp.Body)
	require.NoError(t, err)
	body := string(bodyBytes)
	require.Contains(t, body, "http_server_request_duration_seconds")
	require.Contains(t, body, `http_route="/orgs/:orgSlug/data-sources/:dataSourceSlug/test"`)
	require.Contains(t, body, `http_request_method="GET"`)
	require.Contains(t, body, `http_response_status_code="204"`)
	require.NotContains(t, body, `http_request_method="GETT"`)
	require.NotContains(t, body, `http_request_method="POS"`)
	require.NotContains(t, body, "acme")
	require.NotContains(t, body, "slow-prod")
}

func TestRecordConnectorOperationPublishesSafeSeries(t *testing.T) {
	t.Parallel()

	meter, err := New(Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)

	meter.RecordConnectorOperation(ConnectorOperationMetric{
		DataSourceType: "http",
		Operation:      "execute_query",
		Outcome:        "success",
		ErrorType:      "none",
		Duration:       150 * time.Millisecond,
		ResultRows:     12,
		ResultBytes:    512,
	})

	body := scrapeMetrics(t, meter)
	require.Contains(t, body, "braidplane_connector_operation_duration_seconds")
	require.Contains(t, body, "braidplane_connector_operation_total")
	require.Contains(t, body, "braidplane_connector_result_rows")
	require.Contains(t, body, "braidplane_connector_result_bytes")
	require.Contains(t, body, `service_name="backend"`)
	require.Contains(t, body, `deployment_environment="test"`)
	require.Contains(t, body, `data_source_type="http"`)
	require.Contains(t, body, `connector_operation="execute_query"`)
	require.Contains(t, body, `outcome="success"`)
	require.Contains(t, body, `error_type="none"`)
}

func TestRecordQueryExecutionAndLimitHitPublishSafeSeries(t *testing.T) {
	t.Parallel()

	meter, err := New(Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)

	meter.RecordQueryExecution(QueryExecutionMetric{
		DataSourceType: "postgres",
		Outcome:        "limited",
		ErrorType:      "execution_row_limit_exceeded",
		Duration:       2 * time.Second,
	})
	meter.RecordQueryLimitHit(QueryLimitHitMetric{
		DataSourceType: "postgres",
		LimitType:      "row_limit",
	})

	body := scrapeMetrics(t, meter)
	require.Contains(t, body, "braidplane_query_execution_duration_seconds")
	require.Contains(t, body, "braidplane_query_execution_total")
	require.Contains(t, body, "braidplane_query_limit_hit_total")
	require.Contains(t, body, `data_source_type="postgres"`)
	require.Contains(t, body, `outcome="limited"`)
	require.Contains(t, body, `error_type="execution_row_limit_exceeded"`)
	require.Contains(t, body, `limit_type="row_limit"`)
}

func TestRecordTransformExecutionPublishesAggregateUsageAndBudgetHits(t *testing.T) {
	t.Parallel()

	meter, err := New(Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)

	meter.RecordTransformExecution(TransformExecutionMetric{
		Outcome:              "error",
		LimitType:            "work",
		Duration:             25 * time.Millisecond,
		Work:                 10_001,
		IntermediateRows:     100,
		IntermediateBytes:    4096,
		OutputRows:           50,
		OutputBytes:          2048,
		MaxDepth:             7,
		RoundedDecimalValues: 3,
		InexactDecimalValues: 2,
	})

	body := scrapeMetrics(t, meter)
	for _, metric := range []string{
		"braidplane_transform_execution_duration_seconds_count",
		"braidplane_transform_work_units_count",
		"braidplane_transform_intermediate_rows_count",
		"braidplane_transform_intermediate_bytes_count",
		"braidplane_transform_output_rows_count",
		"braidplane_transform_output_bytes_count",
		"braidplane_transform_max_depth_count",
	} {
		require.Contains(t, body, metric+`{deployment_environment="test",limit_type="work",outcome="error",service_name="backend"} 1`)
	}
	require.Contains(t, body, `braidplane_transform_budget_hit_total{deployment_environment="test",limit_type="work",service_name="backend"} 1`)
	require.Contains(t, body, `braidplane_transform_decimal_conditions_total{condition="rounded",deployment_environment="test",service_name="backend"} 3`)
	require.Contains(t, body, `braidplane_transform_decimal_conditions_total{condition="inexact",deployment_environment="test",service_name="backend"} 2`)
}

func TestRecordExecutionBrokerPublishesQueueSaturationAndOutcomeMetrics(t *testing.T) {
	t.Parallel()

	meter, err := New(Config{
		Enabled: true, Path: "/metrics", ServiceName: "executor", Environment: "test",
	})
	require.NoError(t, err)
	meter.RecordExecutionBroker(ExecutionBrokerMetric{
		Kind: "git_write", Outcome: "ambiguous", Duration: 2 * time.Second,
	})
	meter.RecordExecutionBrokerSnapshot(ExecutionBrokerSnapshot{
		PendingCount: 7, RunningCount: 2, OldestPendingAgeSeconds: 4.5,
		Active: 2, Capacity: 2, LastSuccessfulPoll: time.Unix(1234, 0),
		ConnectorActive: 2, ConnectorQueued: 5,
	})
	meter.RecordConnectorConcurrency(ConnectorConcurrencyScopeProcess, -1, 3)
	meter.RecordConnectorAdmission("rejected")
	meter.RecordSQLConnectorPoolSnapshot(SQLConnectorPoolSnapshot{
		Entries: 3, Retired: 1, ActiveLeases: 2,
		OpenConnections: 4, InUseConnections: 2, IdleConnections: 2,
		MaxOpenConnections: 10, WaitCount: 7, WaitDuration: 9 * time.Millisecond,
		MaxIdleClosed: 11, MaxIdleTimeClosed: 13, MaxLifetimeClosed: 17,
	})

	body := scrapeMetrics(t, meter)
	require.Contains(t, body, `braidplane_execution_broker_duration_seconds_count{deployment_environment="test",execution_kind="git_write",outcome="ambiguous",service_name="executor"} 1`)
	require.Contains(t, body, `braidplane_execution_broker_total{deployment_environment="test",execution_kind="git_write",outcome="ambiguous",service_name="executor"} 1`)
	require.Contains(t, body, `braidplane_execution_queue_depth{deployment_environment="test",service_name="executor",state="pending"} 7`)
	require.Contains(t, body, "braidplane_execution_oldest_pending_age_seconds")
	require.Contains(t, body, "braidplane_execution_active")
	require.Contains(t, body, "braidplane_execution_capacity")
	require.Contains(t, body, "braidplane_execution_last_successful_poll_timestamp_seconds")
	require.Contains(t, body, `braidplane_connector_concurrency_current{deployment_environment="test",scope="deployment",service_name="executor",state="active"} 2`)
	require.Contains(t, body, `braidplane_connector_concurrency_current{deployment_environment="test",scope="deployment",service_name="executor",state="queued"} 5`)
	require.Contains(t, body, `braidplane_connector_concurrency_current{deployment_environment="test",scope="process",service_name="executor",state="active"} 0`)
	require.Contains(t, body, `braidplane_connector_concurrency_current{deployment_environment="test",scope="process",service_name="executor",state="queued"} 3`)
	require.Contains(t, body, `braidplane_connector_admission_total{deployment_environment="test",outcome="rejected",service_name="executor"} 1`)
	require.Contains(t, body, `braidplane_sql_connector_pool_entries{deployment_environment="test",service_name="executor",state="active"} 2`)
	require.Contains(t, body, `braidplane_sql_connector_pool_connections{deployment_environment="test",service_name="executor",state="in_use"} 2`)
	require.Contains(t, body, `braidplane_sql_connector_pool_wait_count{deployment_environment="test",service_name="executor"} 7`)
	require.Contains(t, body, `braidplane_sql_connector_pool_wait_duration_seconds{deployment_environment="test",service_name="executor"} 0.009`)
	require.Contains(t, body, `braidplane_sql_connector_pool_closed_connections{deployment_environment="test",reason="max_idle",service_name="executor"} 11`)
	require.Contains(t, body, `braidplane_sql_connector_pool_closed_connections{deployment_environment="test",reason="max_idle_time",service_name="executor"} 13`)
	require.Contains(t, body, `braidplane_sql_connector_pool_closed_connections{deployment_environment="test",reason="max_lifetime",service_name="executor"} 17`)
}

func TestRecordPipelineRunPublishesTriggerScopedSeries(t *testing.T) {
	t.Parallel()

	meter, err := New(Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)

	meter.RecordPipelineRun(PipelineRunMetric{
		Trigger:   "manual",
		Outcome:   "success",
		ErrorType: "none",
		Duration:  5 * time.Second,
	})

	body := scrapeMetrics(t, meter)
	require.Contains(t, body, "braidplane_pipeline_run_duration_seconds")
	require.Contains(t, body, "braidplane_pipeline_run_total")
	require.Contains(t, body, `pipeline_trigger="manual"`)
	require.Contains(t, body, `outcome="success"`)
	require.Contains(t, body, `error_type="none"`)
}

func TestRecordHTTPClientPublishesSafeSeries(t *testing.T) {
	t.Parallel()

	meter, err := New(Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "frontend",
		Environment: "test",
	})
	require.NoError(t, err)

	meter.RecordHTTPClient(HTTPClientMetric{
		Component:     "Frontend",
		Method:        "gett",
		ServerAddress: "backend_api",
		StatusCode:    http.StatusServiceUnavailable,
		ErrorType:     "503",
		Duration:      75 * time.Millisecond,
	})

	body := scrapeMetrics(t, meter)
	require.Contains(t, body, "http_client_request_duration_seconds")
	require.Contains(t, body, `service_name="frontend"`)
	require.Contains(t, body, `deployment_environment="test"`)
	require.Contains(t, body, `braidplane_component="frontend"`)
	require.Contains(t, body, `http_request_method="_OTHER"`)
	require.Contains(t, body, `server_address="backend_api"`)
	require.Contains(t, body, `http_response_status_code="503"`)
	require.Contains(t, body, `error_type="503"`)
}

func TestHTTPClientRoundTripperRecordsTransportErrorsWithBoundedTarget(t *testing.T) { // This test mutates the global metrics meter.
	meter, err := New(Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "frontend",
		Environment: "test",
	})
	require.NoError(t, err)
	restore := SetProcessMeter(meter)
	t.Cleanup(restore)

	expectedErr := transportError("dial tcp secret.internal: connection refused")
	client := &http.Client{
		Transport: NewHTTPClientRoundTripper(
			roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, expectedErr
			}),
			"frontend",
			"backend_api",
		),
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://secret.internal/api", nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	if resp != nil {
		t.Cleanup(func() {
			require.NoError(t, resp.Body.Close())
		})
	}
	require.ErrorIs(t, err, expectedErr)
	require.Nil(t, resp)

	body := scrapeMetrics(t, meter)
	require.Contains(t, body, "http_client_request_duration_seconds")
	require.Contains(t, body, "http_client_active_requests")
	require.Contains(t, body, `braidplane_component="frontend"`)
	require.Contains(t, body, `http_request_method="POST"`)
	require.Contains(t, body, `server_address="backend_api"`)
	require.Contains(t, body, `http_response_status_code="0"`)
	require.Contains(t, body, `error_type="transport_error"`)
	require.NotContains(t, body, "secret.internal")
}

func TestRegisterPostgresPoolPublishesPoolSeries(t *testing.T) {
	t.Parallel()

	meter, err := New(Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)

	meter.RegisterPostgresPool(func() PostgresPoolStats {
		return postgresPoolStats{
			acquiredConns:        2,
			idleConns:            3,
			totalConns:           6,
			constructingConns:    1,
			maxConns:             12,
			acquireCount:         17,
			canceledAcquireCount: 4,
			emptyAcquireCount:    5,
			acquireDuration:      9 * time.Second,
		}
	})

	body := scrapeMetrics(t, meter)
	require.Contains(t, body, "braidplane_postgres_pool_connections")
	require.Contains(t, body, "braidplane_postgres_pool_max_connections")
	require.Contains(t, body, "braidplane_postgres_pool_acquire_total")
	require.Contains(t, body, "braidplane_postgres_pool_acquire_duration_seconds_total")
	require.Contains(t, body, "# TYPE braidplane_postgres_pool_acquire_duration_seconds_total counter")
	require.Contains(t, body, `state="acquired"`)
	require.Contains(t, body, `state="idle"`)
	require.Contains(t, body, `state="total"`)
	require.Contains(t, body, `state="constructing"`)
	require.Contains(t, body, `outcome="success"`)
	require.Contains(t, body, `outcome="canceled"`)
	require.Contains(t, body, `outcome="empty"`)
}

func TestRegisterPostgresPoolAcceptsPoolLikeStatMethod(t *testing.T) {
	t.Parallel()

	meter, err := New(Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)

	meter.RegisterPostgresPool(func() PostgresPoolStats {
		return &postgresPoolStats{
			acquiredConns:        1,
			idleConns:            2,
			totalConns:           3,
			constructingConns:    0,
			maxConns:             8,
			acquireCount:         9,
			canceledAcquireCount: 1,
			emptyAcquireCount:    2,
			acquireDuration:      3 * time.Second,
		}
	})

	body := scrapeMetrics(t, meter)
	require.Contains(t, body, "braidplane_postgres_pool_connections")
	require.Contains(t, body, `state="acquired"`)
}

func TestRegisterRiverQueueHealthPublishesSafeLowCardinalitySeries(t *testing.T) {
	t.Parallel()

	meter, err := New(Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)

	meter.RegisterRiverQueueHealth(RiverQueueHealthConfig{
		AllowedQueues: []string{"pipeline_schedule", "pipeline_retry"},
		Provider: func(context.Context) RiverQueueHealthSnapshot {
			return RiverQueueHealthSnapshot{
				QueueDepth: []RiverQueueDepthMetric{
					{Queue: "pipeline_schedule", State: "available", Depth: 7},
					{Queue: "pipeline_schedule", State: "scheduled", Depth: 2},
					{Queue: "pipeline_retry", State: "retryable", Depth: 3},
				},
				OldestJobAge: []RiverJobAgeMetric{
					{Queue: "pipeline_schedule", State: "available", Age: 12 * time.Minute},
					{Queue: "pipeline_schedule", State: "scheduled", Age: 45 * time.Second},
				},
				RetryTotal: []RiverQueueCountMetric{
					{Queue: "pipeline_retry", Count: 5},
				},
				DiscardedTotal: []RiverQueueCountMetric{
					{Queue: "pipeline_retry", Count: 2},
				},
				FinalizedTotal: []RiverJobFinalizedMetric{
					{Queue: "pipeline_schedule", State: "completed", Count: 13},
					{Queue: "pipeline_retry", State: "discarded", Count: 2},
				},
				Workers: []RiverWorkerMetric{
					{Queue: "pipeline_schedule", ActiveWorkers: 2, MaxWorkers: 4, LastHeartbeatAge: 5 * time.Second},
					{Queue: "pipeline_retry", ActiveWorkers: 1, MaxWorkers: 4, LastHeartbeatAge: 7 * time.Second},
				},
				PeriodicJobLag: []RiverPeriodicJobLagMetric{
					{Queue: "pipeline_schedule", Lag: 65 * time.Second},
				},
			}
		},
	})

	body := scrapeMetrics(t, meter)
	require.Contains(t, body, "braidplane_river_queue_depth")
	require.Contains(t, body, "braidplane_river_job_oldest_age_seconds")
	require.Contains(t, body, "braidplane_river_job_retry_total")
	require.Contains(t, body, "braidplane_river_job_discarded_total")
	require.Contains(t, body, "braidplane_river_job_finalized_total")
	require.Contains(t, body, "braidplane_river_worker_liveness")
	require.Contains(
		t,
		body,
		`braidplane_river_worker_liveness{deployment_environment="test",queue_name="pipeline_schedule",service_name="backend"} 1`,
	)
	require.Contains(
		t,
		body,
		`braidplane_river_worker_liveness{deployment_environment="test",queue_name="pipeline_retry",service_name="backend"} 1`,
	)
	require.Contains(t, body, "braidplane_river_worker_last_heartbeat_age_seconds")
	require.Contains(t, body, "braidplane_river_periodic_job_lag_seconds")
	require.Contains(t, body, `service_name="backend"`)
	require.Contains(t, body, `deployment_environment="test"`)
	require.Contains(t, body, `queue_name="pipeline_schedule"`)
	require.Contains(t, body, `queue_name="pipeline_retry"`)
	require.Contains(t, body, `state="available"`)
	require.Contains(t, body, `state="scheduled"`)
	require.Contains(t, body, `state="retryable"`)
	require.Contains(t, body, `state="completed"`)
	require.Contains(t, body, `state="discarded"`)
	require.NotContains(t, body, "job_id")
	require.NotContains(t, body, "args")
	require.NotContains(t, body, "organization")
	require.NotContains(t, body, "tenant")
	require.NotContains(t, body, "token")
	require.NotContains(t, body, "email")
}

func TestRegisterRiverQueueHealthDropsUnallowlistedQueuesAndUnsafeStates(t *testing.T) {
	t.Parallel()

	meter, err := New(Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)

	meter.RegisterRiverQueueHealth(RiverQueueHealthConfig{
		AllowedQueues: []string{"pipeline_schedule"},
		Provider: func(context.Context) RiverQueueHealthSnapshot {
			return RiverQueueHealthSnapshot{
				QueueDepth: []RiverQueueDepthMetric{
					{Queue: "pipeline_schedule", State: "available", Depth: 1},
					{Queue: "org-acme-secret", State: "available", Depth: 99},
					{Queue: "pipeline_schedule", State: "available;email=alice@example.test", Depth: 99},
				},
				OldestJobAge: []RiverJobAgeMetric{
					{Queue: "pipeline_schedule", State: "scheduled", Age: 2 * time.Minute},
					{Queue: "tenant-123", State: "scheduled", Age: time.Hour},
				},
				FinalizedTotal: []RiverJobFinalizedMetric{
					{Queue: "pipeline_schedule", State: "completed", Count: 4},
					{Queue: "pipeline_schedule", State: "completed_with_token_secret", Count: 99},
				},
			}
		},
	})

	body := scrapeMetrics(t, meter)
	require.Contains(t, body, `queue_name="pipeline_schedule"`)
	require.Contains(t, body, `state="available"`)
	require.Contains(t, body, `state="scheduled"`)
	require.Contains(t, body, `state="completed"`)
	require.NotContains(t, body, "org-acme-secret")
	require.NotContains(t, body, "tenant-123")
	require.NotContains(t, body, "alice@example.test")
	require.NotContains(t, body, "token_secret")
}

func scrapeMetrics(t *testing.T, meter *Meter) string {
	t.Helper()

	app := fiber.New()
	meter.RegisterRoutes(app)

	resp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(bodyBytes)
}

type postgresPoolStats struct {
	acquiredConns        int32
	idleConns            int32
	totalConns           int32
	constructingConns    int32
	maxConns             int32
	acquireCount         int64
	canceledAcquireCount int64
	emptyAcquireCount    int64
	acquireDuration      time.Duration
}

func (s postgresPoolStats) AcquiredConns() int32        { return s.acquiredConns }
func (s postgresPoolStats) IdleConns() int32            { return s.idleConns }
func (s postgresPoolStats) TotalConns() int32           { return s.totalConns }
func (s postgresPoolStats) ConstructingConns() int32    { return s.constructingConns }
func (s postgresPoolStats) MaxConns() int32             { return s.maxConns }
func (s postgresPoolStats) AcquireCount() int64         { return s.acquireCount }
func (s postgresPoolStats) CanceledAcquireCount() int64 { return s.canceledAcquireCount }
func (s postgresPoolStats) EmptyAcquireCount() int64    { return s.emptyAcquireCount }
func (s postgresPoolStats) AcquireDuration() time.Duration {
	return s.acquireDuration
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type transportError string

func (e transportError) Error() string {
	return string(e)
}
