package metrics

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	defaultMetricsPath = "/metrics"
	defaultServiceName = "braidplane"
	defaultEnvironment = "local"
)

// Config contains metrics runtime configuration.
type Config struct {
	Enabled     bool
	Path        string
	ServiceName string
	Environment string
}

// Meter owns the application metrics registry and Fiber instrumentation.
type Meter struct {
	enabled                    bool
	path                       string
	serviceName                string
	environment                string
	registry                   *prometheus.Registry
	httpDuration               *prometheus.HistogramVec
	httpClientDuration         *prometheus.HistogramVec
	httpClientActive           *prometheus.GaugeVec
	connectorDuration          *prometheus.HistogramVec
	connectorTotal             *prometheus.CounterVec
	connectorResultRows        *prometheus.HistogramVec
	connectorResultBytes       *prometheus.HistogramVec
	queryExecutionDuration     *prometheus.HistogramVec
	queryExecutionTotal        *prometheus.CounterVec
	queryLimitHitTotal         *prometheus.CounterVec
	transformDuration          *prometheus.HistogramVec
	transformWork              *prometheus.HistogramVec
	transformIntermediateRows  *prometheus.HistogramVec
	transformIntermediateBytes *prometheus.HistogramVec
	transformDecimalConditions *prometheus.CounterVec
	transformOutputRows        *prometheus.HistogramVec
	transformOutputBytes       *prometheus.HistogramVec
	transformMaxDepth          *prometheus.HistogramVec
	transformBudgetHitTotal    *prometheus.CounterVec
	executionBrokerDuration    *prometheus.HistogramVec
	executionBrokerTotal       *prometheus.CounterVec
	executionQueueDepth        *prometheus.GaugeVec
	executionOldestPendingAge  *prometheus.GaugeVec
	executionActive            *prometheus.GaugeVec
	executionCapacity          *prometheus.GaugeVec
	executionLastPollTimestamp *prometheus.GaugeVec
	connectorConcurrency       *prometheus.GaugeVec
	connectorAdmissionTotal    *prometheus.CounterVec
	sqlPoolEntries             *prometheus.GaugeVec
	sqlPoolConnections         *prometheus.GaugeVec
	sqlPoolWaitCount           *prometheus.GaugeVec
	sqlPoolWaitDuration        *prometheus.GaugeVec
	sqlPoolClosedConnections   *prometheus.GaugeVec
	pipelineRunDuration        *prometheus.HistogramVec
	pipelineRunTotal           *prometheus.CounterVec
	proofControlTotal          *prometheus.CounterVec
}

const (
	// ConnectorConcurrencyScopeDeployment identifies database-coordinated admission.
	ConnectorConcurrencyScopeDeployment = "deployment"
	// ConnectorConcurrencyScopeProcess identifies in-process admission.
	ConnectorConcurrencyScopeProcess = "process"
)

type ConnectorOperationMetric struct {
	DataSourceType string
	Operation      string
	Outcome        string
	ErrorType      string
	Duration       time.Duration
	ResultRows     int
	ResultBytes    int64
}

type QueryExecutionMetric struct {
	DataSourceType string
	Outcome        string
	ErrorType      string
	Duration       time.Duration
}

type QueryLimitHitMetric struct {
	DataSourceType string
	LimitType      string
}

type TransformExecutionMetric struct {
	Outcome              string
	LimitType            string
	Duration             time.Duration
	Work                 int64
	IntermediateRows     int64
	IntermediateBytes    int64
	OutputRows           int
	OutputBytes          int64
	MaxDepth             int
	RoundedDecimalValues int64
	InexactDecimalValues int64
}

type ExecutionBrokerMetric struct {
	Kind     string
	Outcome  string
	Duration time.Duration
}

type ExecutionBrokerSnapshot struct {
	PendingCount            int64
	RunningCount            int64
	OldestPendingAgeSeconds float64
	Active                  int64
	Capacity                int64
	LastSuccessfulPoll      time.Time
	ConnectorActive         int64
	ConnectorQueued         int64
}

type SQLConnectorPoolSnapshot struct {
	Entries            int
	Retired            int
	ActiveLeases       int
	OpenConnections    int
	InUseConnections   int
	IdleConnections    int
	MaxOpenConnections int
	WaitCount          int64
	WaitDuration       time.Duration
	MaxIdleClosed      int64
	MaxIdleTimeClosed  int64
	MaxLifetimeClosed  int64
}

type PipelineRunMetric struct {
	Trigger   string
	Outcome   string
	ErrorType string
	Duration  time.Duration
}

type HTTPClientMetric struct {
	Component     string
	Method        string
	ServerAddress string
	StatusCode    int
	ErrorType     string
	Duration      time.Duration
}

type PostgresPoolStats interface {
	AcquiredConns() int32
	IdleConns() int32
	TotalConns() int32
	ConstructingConns() int32
	MaxConns() int32
	AcquireCount() int64
	CanceledAcquireCount() int64
	EmptyAcquireCount() int64
	AcquireDuration() time.Duration
}

type PostgresPoolStatsProvider func() PostgresPoolStats

// RiverQueueHealthProvider returns one point-in-time aggregate River queue snapshot.
type RiverQueueHealthProvider func(ctx context.Context) RiverQueueHealthSnapshot

// RiverQueueHealthConfig configures the River queue health collector.
type RiverQueueHealthConfig struct {
	AllowedQueues []string
	Provider      RiverQueueHealthProvider
}

// RiverQueueHealthSnapshot contains pre-aggregated River queue health values.
type RiverQueueHealthSnapshot struct {
	QueueDepth     []RiverQueueDepthMetric
	OldestJobAge   []RiverJobAgeMetric
	RetryTotal     []RiverQueueCountMetric
	DiscardedTotal []RiverQueueCountMetric
	FinalizedTotal []RiverJobFinalizedMetric
	Workers        []RiverWorkerMetric
	PeriodicJobLag []RiverPeriodicJobLagMetric
}

// RiverQueueDepthMetric is a River queue depth value grouped by queue and state.
type RiverQueueDepthMetric struct {
	Queue string
	State string
	Depth int64
}

// RiverJobAgeMetric is the age of the oldest River job in one queue and state.
type RiverJobAgeMetric struct {
	Queue string
	State string
	Age   time.Duration
}

// RiverQueueCountMetric is a River queue cumulative count grouped by queue.
type RiverQueueCountMetric struct {
	Queue string
	Count int64
}

// RiverJobFinalizedMetric is a finalized River job count grouped by queue and final state.
type RiverJobFinalizedMetric struct {
	Queue string
	State string
	Count int64
}

// RiverWorkerMetric is River worker liveness and heartbeat information for one queue.
type RiverWorkerMetric struct {
	Queue            string
	ActiveWorkers    int64
	MaxWorkers       int64
	LastHeartbeatAge time.Duration
}

// RiverPeriodicJobLagMetric is lag for periodic River work grouped by queue.
type RiverPeriodicJobLagMetric struct {
	Queue string
	Lag   time.Duration
}

// RiverQueueHealthPostgresConfig configures a Postgres-backed River health provider.
type RiverQueueHealthPostgresConfig struct {
	AllowedQueues    []string
	PeriodicJobKinds []string
	QueryTimeout     time.Duration
	Now              func() time.Time
}

var (
	processMeterMu sync.RWMutex
	processMeter   *Meter
)

// New creates an isolated Prometheus registry and registers baseline collectors.
func New(cfg Config) (*Meter, error) {
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		path = defaultMetricsPath
	}
	serviceName := strings.TrimSpace(cfg.ServiceName)
	if serviceName == "" {
		serviceName = defaultServiceName
	}
	environment := strings.TrimSpace(cfg.Environment)
	if environment == "" {
		environment = defaultEnvironment
	}

	registry := prometheus.NewRegistry()
	meter := &Meter{
		enabled:     cfg.Enabled,
		path:        path,
		serviceName: serviceName,
		environment: environment,
		registry:    registry,
		httpDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name: "http_server_request_duration_seconds",
				Help: "Duration of inbound HTTP server requests.",
				Buckets: []float64{
					0.005, 0.01, 0.025, 0.05, 0.075, 0.1,
					0.25, 0.5, 0.75, 1,
					2.5, 5, 7.5, 10,
				},
				ConstLabels: prometheus.Labels{
					"service_name":           serviceName,
					"deployment_environment": environment,
				},
			},
			[]string{
				"http_request_method",
				"http_route",
				"http_response_status_code",
				"error_type",
			},
		),
		httpClientDuration: newHistogramVec(
			"http_client_request_duration_seconds",
			"Duration of outbound HTTP client requests.",
			[]string{
				"braidplane_component",
				"http_request_method",
				"server_address",
				"http_response_status_code",
				"error_type",
			},
			serviceName,
			environment,
			[]float64{
				0.005, 0.01, 0.025, 0.05, 0.075, 0.1,
				0.25, 0.5, 0.75, 1,
				2.5, 5, 7.5, 10,
			},
		),
		httpClientActive: newGaugeVec(
			"http_client_active_requests",
			"Current outbound HTTP client requests.",
			[]string{"braidplane_component", "http_request_method", "server_address"},
			serviceName,
			environment,
		),
		connectorDuration: newHistogramVec(
			"braidplane_connector_operation_duration_seconds",
			"Duration of connector operations.",
			[]string{"data_source_type", "connector_operation", "outcome", "error_type"},
			serviceName,
			environment,
			[]float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		),
		connectorTotal: newCounterVec(
			"braidplane_connector_operation_total",
			"Count of connector operations.",
			[]string{"data_source_type", "connector_operation", "outcome", "error_type"},
			serviceName,
			environment,
		),
		connectorResultRows: newHistogramVec(
			"braidplane_connector_result_rows",
			"Observed row counts returned by connector operations.",
			[]string{"data_source_type", "connector_operation"},
			serviceName,
			environment,
			prometheus.DefBuckets,
		),
		connectorResultBytes: newHistogramVec(
			"braidplane_connector_result_bytes",
			"Observed response sizes returned by connector operations.",
			[]string{"data_source_type", "connector_operation"},
			serviceName,
			environment,
			prometheus.ExponentialBuckets(128, 2, 12),
		),
		queryExecutionDuration: newHistogramVec(
			"braidplane_query_execution_duration_seconds",
			"Duration of saved query executions.",
			[]string{"data_source_type", "outcome", "error_type"},
			serviceName,
			environment,
			[]float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		),
		queryExecutionTotal: newCounterVec(
			"braidplane_query_execution_total",
			"Count of saved query executions.",
			[]string{"data_source_type", "outcome", "error_type"},
			serviceName,
			environment,
		),
		queryLimitHitTotal: newCounterVec(
			"braidplane_query_limit_hit_total",
			"Count of saved query execution guardrail limit hits.",
			[]string{"data_source_type", "limit_type"},
			serviceName,
			environment,
		),
		transformDuration: newHistogramVec(
			"braidplane_transform_execution_duration_seconds",
			"Duration of SQL transform executions.",
			[]string{"outcome", "limit_type"},
			serviceName,
			environment,
			[]float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		),
		transformWork: newHistogramVec(
			"braidplane_transform_work_units",
			"Observed cumulative SQL transform work units.",
			[]string{"outcome", "limit_type"},
			serviceName,
			environment,
			prometheus.ExponentialBuckets(1, 10, 8),
		),
		transformIntermediateRows: newHistogramVec(
			"braidplane_transform_intermediate_rows",
			"Observed cumulative SQL transform intermediate rows.",
			[]string{"outcome", "limit_type"},
			serviceName,
			environment,
			prometheus.ExponentialBuckets(1, 10, 7),
		),
		transformIntermediateBytes: newHistogramVec(
			"braidplane_transform_intermediate_bytes",
			"Observed cumulative estimated SQL transform intermediate bytes.",
			[]string{"outcome", "limit_type"},
			serviceName,
			environment,
			prometheus.ExponentialBuckets(128, 4, 10),
		),
		transformDecimalConditions: newCounterVec(
			"braidplane_transform_decimal_conditions_total",
			"Evaluated decimal values carrying a rounding condition, including propagated conditions.",
			[]string{"condition"}, serviceName, environment,
		),
		transformOutputRows: newHistogramVec(
			"braidplane_transform_output_rows",
			"Observed SQL transform output rows.",
			[]string{"outcome", "limit_type"},
			serviceName,
			environment,
			prometheus.ExponentialBuckets(1, 10, 7),
		),
		transformOutputBytes: newHistogramVec(
			"braidplane_transform_output_bytes",
			"Observed estimated SQL transform output bytes.",
			[]string{"outcome", "limit_type"},
			serviceName,
			environment,
			prometheus.ExponentialBuckets(128, 4, 10),
		),
		transformMaxDepth: newHistogramVec(
			"braidplane_transform_max_depth",
			"Peak SQL transform relation, value, or expression evaluation depth.",
			[]string{"outcome", "limit_type"},
			serviceName,
			environment,
			[]float64{1, 2, 4, 8, 16, 32, 64, 128},
		),
		transformBudgetHitTotal: newCounterVec(
			"braidplane_transform_budget_hit_total",
			"Count of SQL transform execution budget violations.",
			[]string{"limit_type"},
			serviceName,
			environment,
		),
		executionBrokerDuration: newHistogramVec(
			"braidplane_execution_broker_duration_seconds",
			"End-to-end duration of persisted execution broker requests.",
			[]string{"execution_kind", "outcome"},
			serviceName,
			environment,
			[]float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		),
		executionBrokerTotal: newCounterVec(
			"braidplane_execution_broker_total",
			"Count of persisted execution broker requests by terminal outcome.",
			[]string{"execution_kind", "outcome"},
			serviceName,
			environment,
		),
		executionQueueDepth: newGaugeVec(
			"braidplane_execution_queue_depth",
			"Current persisted execution envelope count by state.",
			[]string{"state"},
			serviceName,
			environment,
		),
		executionOldestPendingAge: newGaugeVec(
			"braidplane_execution_oldest_pending_age_seconds",
			"Age in seconds of the oldest pending execution envelope.",
			nil,
			serviceName,
			environment,
		),
		executionActive: newGaugeVec(
			"braidplane_execution_active",
			"Current active persisted execution operations.",
			nil,
			serviceName,
			environment,
		),
		executionCapacity: newGaugeVec(
			"braidplane_execution_capacity",
			"Configured concurrent persisted execution capacity.",
			nil,
			serviceName,
			environment,
		),
		executionLastPollTimestamp: newGaugeVec(
			"braidplane_execution_last_successful_poll_timestamp_seconds",
			"Unix timestamp of the executor's last successful queue-health poll.",
			nil,
			serviceName,
			environment,
		),
		connectorConcurrency: newGaugeVec(
			"braidplane_connector_concurrency_current",
			"Current connector executions by admission scope and state.",
			[]string{"scope", "state"},
			serviceName,
			environment,
		),
		connectorAdmissionTotal: newCounterVec(
			"braidplane_connector_admission_total",
			"Count of connector admission outcomes.",
			[]string{"outcome"},
			serviceName,
			environment,
		),
		sqlPoolEntries: newGaugeVec(
			"braidplane_sql_connector_pool_entries",
			"Process-local SQL connector pool cache entries by state.",
			[]string{"state"},
			serviceName,
			environment,
		),
		sqlPoolConnections: newGaugeVec(
			"braidplane_sql_connector_pool_connections",
			"Process-local SQL connector pool connections by state.",
			[]string{"state"},
			serviceName,
			environment,
		),
		sqlPoolWaitCount: newGaugeVec(
			"braidplane_sql_connector_pool_wait_count",
			"Current aggregate database/sql wait count across retained connector pools.",
			nil,
			serviceName,
			environment,
		),
		sqlPoolWaitDuration: newGaugeVec(
			"braidplane_sql_connector_pool_wait_duration_seconds",
			"Current aggregate database/sql wait duration across retained connector pools.",
			nil,
			serviceName,
			environment,
		),
		sqlPoolClosedConnections: newGaugeVec(
			"braidplane_sql_connector_pool_closed_connections",
			"Current aggregate database/sql closed connection count across retained pools by reason.",
			[]string{"reason"},
			serviceName,
			environment,
		),
		pipelineRunDuration: newHistogramVec(
			"braidplane_pipeline_run_duration_seconds",
			"Duration of pipeline runs.",
			[]string{"pipeline_trigger", "outcome", "error_type"},
			serviceName,
			environment,
			[]float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		),
		pipelineRunTotal: newCounterVec(
			"braidplane_pipeline_run_total",
			"Count of pipeline runs.",
			[]string{"pipeline_trigger", "outcome", "error_type"},
			serviceName,
			environment,
		),
	}

	meter.proofControlTotal = newCounterVec("braidplane_proof_control_total", "Proof control outcomes after the service transaction returns.", []string{"action", "outcome"}, serviceName, environment)
	if err := registry.Register(meter.proofControlTotal); err != nil {
		return nil, xerror.Wrapf("register Proof control metrics: %w", err)
	}
	for _, action := range []string{"preview", "confirm", "policy", "cancel", "access", "read", "compile", "unknown"} {
		for _, outcome := range model.ProofControlOutcomeValues() {
			meter.proofControlTotal.WithLabelValues(action, outcome.String()).Add(0)
		}
	}

	if err := registry.Register(collectors.NewGoCollector()); err != nil {
		return nil, xerror.Wrapf("register Go runtime metrics: %w", err)
	}
	if err := registry.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		return nil, xerror.Wrapf("register process metrics: %w", err)
	}
	if err := registry.Register(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "braidplane_target_info",
			Help: "Static Braidplane metrics target information.",
			ConstLabels: prometheus.Labels{
				"service_name":           serviceName,
				"deployment_environment": environment,
			},
		},
		func() float64 { return 1 },
	)); err != nil {
		return nil, xerror.Wrapf("register target info metric: %w", err)
	}
	if err := registry.Register(meter.httpDuration); err != nil {
		return nil, xerror.Wrapf("register HTTP duration metric: %w", err)
	}
	if err := registry.Register(meter.httpClientDuration); err != nil {
		return nil, xerror.Wrapf("register HTTP client duration metric: %w", err)
	}
	if err := registry.Register(meter.httpClientActive); err != nil {
		return nil, xerror.Wrapf("register HTTP client active requests metric: %w", err)
	}
	if err := registry.Register(meter.connectorDuration); err != nil {
		return nil, xerror.Wrapf("register connector duration metric: %w", err)
	}
	if err := registry.Register(meter.connectorTotal); err != nil {
		return nil, xerror.Wrapf("register connector total metric: %w", err)
	}
	if err := registry.Register(meter.connectorResultRows); err != nil {
		return nil, xerror.Wrapf("register connector result rows metric: %w", err)
	}
	if err := registry.Register(meter.connectorResultBytes); err != nil {
		return nil, xerror.Wrapf("register connector result bytes metric: %w", err)
	}
	if err := registry.Register(meter.queryExecutionDuration); err != nil {
		return nil, xerror.Wrapf("register query execution duration metric: %w", err)
	}
	if err := registry.Register(meter.queryExecutionTotal); err != nil {
		return nil, xerror.Wrapf("register query execution total metric: %w", err)
	}
	if err := registry.Register(meter.queryLimitHitTotal); err != nil {
		return nil, xerror.Wrapf("register query limit hit metric: %w", err)
	}
	if err := registry.Register(meter.transformDuration); err != nil {
		return nil, xerror.Wrapf("register transform duration metric: %w", err)
	}
	if err := registry.Register(meter.transformWork); err != nil {
		return nil, xerror.Wrapf("register transform work metric: %w", err)
	}
	if err := registry.Register(meter.transformIntermediateRows); err != nil {
		return nil, xerror.Wrapf("register transform intermediate rows metric: %w", err)
	}
	if err := registry.Register(meter.transformIntermediateBytes); err != nil {
		return nil, xerror.Wrapf("register transform intermediate bytes metric: %w", err)
	}
	if err := registry.Register(meter.transformDecimalConditions); err != nil {
		return nil, xerror.Wrapf("register decimal condition metric: %w", err)
	}
	if err := registry.Register(meter.transformOutputRows); err != nil {
		return nil, xerror.Wrapf("register transform output rows metric: %w", err)
	}
	if err := registry.Register(meter.transformOutputBytes); err != nil {
		return nil, xerror.Wrapf("register transform output bytes metric: %w", err)
	}
	if err := registry.Register(meter.transformMaxDepth); err != nil {
		return nil, xerror.Wrapf("register transform max depth metric: %w", err)
	}
	if err := registry.Register(meter.transformBudgetHitTotal); err != nil {
		return nil, xerror.Wrapf("register transform budget hit metric: %w", err)
	}
	if err := registry.Register(meter.executionBrokerDuration); err != nil {
		return nil, xerror.Wrapf("register execution broker duration metric: %w", err)
	}
	if err := registry.Register(meter.executionBrokerTotal); err != nil {
		return nil, xerror.Wrapf("register execution broker total metric: %w", err)
	}
	if err := registry.Register(meter.executionQueueDepth); err != nil {
		return nil, xerror.Wrapf("register execution queue depth metric: %w", err)
	}
	if err := registry.Register(meter.executionOldestPendingAge); err != nil {
		return nil, xerror.Wrapf("register execution oldest pending age metric: %w", err)
	}
	if err := registry.Register(meter.executionActive); err != nil {
		return nil, xerror.Wrapf("register execution active metric: %w", err)
	}
	if err := registry.Register(meter.executionCapacity); err != nil {
		return nil, xerror.Wrapf("register execution capacity metric: %w", err)
	}
	if err := registry.Register(meter.executionLastPollTimestamp); err != nil {
		return nil, xerror.Wrapf("register execution last poll metric: %w", err)
	}
	if err := registry.Register(meter.connectorConcurrency); err != nil {
		return nil, xerror.Wrapf("register connector concurrency metric: %w", err)
	}
	if err := registry.Register(meter.connectorAdmissionTotal); err != nil {
		return nil, xerror.Wrapf("register connector admission metric: %w", err)
	}
	if err := registry.Register(meter.sqlPoolEntries); err != nil {
		return nil, xerror.Wrapf("register SQL connector pool entries metric: %w", err)
	}
	if err := registry.Register(meter.sqlPoolConnections); err != nil {
		return nil, xerror.Wrapf("register SQL connector pool connections metric: %w", err)
	}
	if err := registry.Register(meter.sqlPoolWaitCount); err != nil {
		return nil, xerror.Wrapf("register SQL connector pool wait count metric: %w", err)
	}
	if err := registry.Register(meter.sqlPoolWaitDuration); err != nil {
		return nil, xerror.Wrapf("register SQL connector pool wait duration metric: %w", err)
	}
	if err := registry.Register(meter.sqlPoolClosedConnections); err != nil {
		return nil, xerror.Wrapf("register SQL connector pool closed connections metric: %w", err)
	}
	if err := registry.Register(meter.pipelineRunDuration); err != nil {
		return nil, xerror.Wrapf("register pipeline run duration metric: %w", err)
	}
	if err := registry.Register(meter.pipelineRunTotal); err != nil {
		return nil, xerror.Wrapf("register pipeline run total metric: %w", err)
	}

	return meter, nil
}

func SetProcessMeter(meter *Meter) func() {
	processMeterMu.Lock()
	prev := processMeter
	processMeter = meter
	processMeterMu.Unlock()

	return func() {
		processMeterMu.Lock()
		processMeter = prev
		processMeterMu.Unlock()
	}
}

func RecordConnectorOperation(metric ConnectorOperationMetric) {
	currentProcessMeter().RecordConnectorOperation(metric)
}

func RecordQueryExecution(metric QueryExecutionMetric) {
	currentProcessMeter().RecordQueryExecution(metric)
}

func RecordQueryLimitHit(metric QueryLimitHitMetric) {
	currentProcessMeter().RecordQueryLimitHit(metric)
}

func RecordTransformExecution(metric TransformExecutionMetric) {
	currentProcessMeter().RecordTransformExecution(metric)
}

func RecordExecutionBroker(metric ExecutionBrokerMetric) {
	currentProcessMeter().RecordExecutionBroker(metric)
}

func RecordExecutionBrokerSnapshot(snapshot ExecutionBrokerSnapshot) {
	currentProcessMeter().RecordExecutionBrokerSnapshot(snapshot)
}

func RecordConnectorAdmission(outcome string) {
	currentProcessMeter().RecordConnectorAdmission(outcome)
}

func RecordConnectorConcurrency(scope string, active, queued int64) {
	currentProcessMeter().RecordConnectorConcurrency(scope, active, queued)
}

func RecordSQLConnectorPoolSnapshot(snapshot SQLConnectorPoolSnapshot) {
	currentProcessMeter().RecordSQLConnectorPoolSnapshot(snapshot)
}

func RecordPipelineRun(metric PipelineRunMetric) {
	currentProcessMeter().RecordPipelineRun(metric)
}

func RecordHTTPClient(metric HTTPClientMetric) {
	currentProcessMeter().RecordHTTPClient(metric)
}

func NewHTTPClientRoundTripper(base http.RoundTripper, component, serverAddress string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return httpClientRoundTripper{
		base:          base,
		component:     component,
		serverAddress: serverAddress,
	}
}

// RegisterRoutes exposes the Prometheus scrape endpoint when metrics are enabled.
func (m *Meter) RegisterRoutes(app *fiber.App) {
	if m == nil || !m.enabled {
		return
	}

	handler := promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
	app.Get(m.path, func(c fiber.Ctx) error {
		req, err := http.NewRequestWithContext(c.Context(), http.MethodGet, m.path, nil)
		if err != nil {
			return xerror.Wrapf("create metrics scrape request: %w", err)
		}

		recorder := newFiberResponseRecorder()
		handler.ServeHTTP(recorder, req)

		for key, values := range recorder.header {
			if len(values) == 0 {
				continue
			}
			c.Set(key, strings.Join(values, ", "))
		}
		return c.Status(recorder.statusCode()).Send(recorder.body)
	})
}

// HTTPMiddleware records low-cardinality HTTP server metrics.
func (m *Meter) HTTPMiddleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		if m == nil || !m.enabled || c.Path() == m.path {
			return c.Next()
		}

		method := strings.Clone(c.Method())
		start := time.Now()
		err := c.Next()
		statusCode := responseStatusCode(c, err)
		route := strings.Clone(fiberRoute(c))
		if route == "" {
			route = "_unknown"
		}

		m.httpDuration.WithLabelValues(
			normalizedMethod(method),
			route,
			strconv.Itoa(statusCode),
			strings.Clone(errorType(err, statusCode)),
		).Observe(time.Since(start).Seconds())

		return err
	}
}

func (m *Meter) RecordConnectorOperation(metric ConnectorOperationMetric) {
	if m == nil || !m.enabled {
		return
	}

	dataSourceType := normalizedLabelValue(metric.DataSourceType, "unknown")
	operation := normalizedLabelValue(metric.Operation, "unknown")
	outcome := normalizedLabelValue(metric.Outcome, "unknown")
	errorType := normalizedLabelValue(metric.ErrorType, "none")

	m.connectorDuration.WithLabelValues(
		dataSourceType,
		operation,
		outcome,
		errorType,
	).Observe(metric.Duration.Seconds())
	m.connectorTotal.WithLabelValues(
		dataSourceType,
		operation,
		outcome,
		errorType,
	).Inc()

	if metric.ResultRows >= 0 {
		m.connectorResultRows.WithLabelValues(dataSourceType, operation).Observe(float64(metric.ResultRows))
	}
	if metric.ResultBytes >= 0 {
		m.connectorResultBytes.WithLabelValues(dataSourceType, operation).Observe(float64(metric.ResultBytes))
	}
}

func (m *Meter) RecordQueryExecution(metric QueryExecutionMetric) {
	if m == nil || !m.enabled {
		return
	}

	dataSourceType := normalizedLabelValue(metric.DataSourceType, "unknown")
	outcome := normalizedLabelValue(metric.Outcome, "unknown")
	errorType := normalizedLabelValue(metric.ErrorType, "none")

	m.queryExecutionDuration.WithLabelValues(dataSourceType, outcome, errorType).Observe(metric.Duration.Seconds())
	m.queryExecutionTotal.WithLabelValues(dataSourceType, outcome, errorType).Inc()
}

func (m *Meter) RecordQueryLimitHit(metric QueryLimitHitMetric) {
	if m == nil || !m.enabled {
		return
	}

	m.queryLimitHitTotal.WithLabelValues(
		normalizedLabelValue(metric.DataSourceType, "unknown"),
		normalizedLabelValue(metric.LimitType, "unknown"),
	).Inc()
}

func (m *Meter) RecordTransformExecution(metric TransformExecutionMetric) {
	if m == nil || !m.enabled {
		return
	}
	outcome := normalizedLabelValue(metric.Outcome, "unknown")
	const noLimit = "none"
	limitType := normalizedLabelValue(metric.LimitType, noLimit)
	m.transformDuration.WithLabelValues(outcome, limitType).Observe(metric.Duration.Seconds())
	m.transformWork.WithLabelValues(outcome, limitType).Observe(float64(max(0, metric.Work)))
	m.transformIntermediateRows.WithLabelValues(outcome, limitType).Observe(float64(max(0, metric.IntermediateRows)))
	m.transformIntermediateBytes.WithLabelValues(outcome, limitType).Observe(float64(max(0, metric.IntermediateBytes)))
	m.transformOutputRows.WithLabelValues(outcome, limitType).Observe(float64(max(0, metric.OutputRows)))
	m.transformOutputBytes.WithLabelValues(outcome, limitType).Observe(float64(max(0, metric.OutputBytes)))
	m.transformMaxDepth.WithLabelValues(outcome, limitType).Observe(float64(max(0, metric.MaxDepth)))
	m.transformDecimalConditions.WithLabelValues("rounded").Add(float64(max(0, metric.RoundedDecimalValues)))
	m.transformDecimalConditions.WithLabelValues("inexact").Add(float64(max(0, metric.InexactDecimalValues)))
	if limitType != noLimit {
		m.transformBudgetHitTotal.WithLabelValues(limitType).Inc()
	}
}

func (m *Meter) RecordExecutionBroker(metric ExecutionBrokerMetric) {
	if m == nil || !m.enabled {
		return
	}
	kind := normalizedLabelValue(metric.Kind, "unknown")
	outcome := normalizedLabelValue(metric.Outcome, "unknown")
	m.executionBrokerDuration.WithLabelValues(kind, outcome).Observe(metric.Duration.Seconds())
	m.executionBrokerTotal.WithLabelValues(kind, outcome).Inc()
}

func (m *Meter) RecordExecutionBrokerSnapshot(snapshot ExecutionBrokerSnapshot) {
	if m == nil || !m.enabled {
		return
	}
	m.executionQueueDepth.WithLabelValues("pending").Set(float64(max(0, snapshot.PendingCount)))
	m.executionQueueDepth.WithLabelValues("running").Set(float64(max(0, snapshot.RunningCount)))
	m.executionOldestPendingAge.WithLabelValues().Set(max(0, snapshot.OldestPendingAgeSeconds))
	m.executionActive.WithLabelValues().Set(float64(max(0, snapshot.Active)))
	m.executionCapacity.WithLabelValues().Set(float64(max(0, snapshot.Capacity)))
	m.RecordConnectorConcurrency(
		ConnectorConcurrencyScopeDeployment,
		snapshot.ConnectorActive,
		snapshot.ConnectorQueued,
	)
	if !snapshot.LastSuccessfulPoll.IsZero() {
		m.executionLastPollTimestamp.WithLabelValues().Set(float64(snapshot.LastSuccessfulPoll.Unix()))
	}
}

func (m *Meter) RecordConnectorConcurrency(scope string, active, queued int64) {
	if m == nil || !m.enabled {
		return
	}
	scope = connectorConcurrencyScope(scope)
	m.connectorConcurrency.WithLabelValues(scope, "active").Set(float64(max(0, active)))
	m.connectorConcurrency.WithLabelValues(scope, "queued").Set(float64(max(0, queued)))
}

func (m *Meter) RecordConnectorAdmission(outcome string) {
	if m == nil || !m.enabled {
		return
	}
	m.connectorAdmissionTotal.WithLabelValues(normalizedLabelValue(outcome, "unknown")).Inc()
}

func (m *Meter) RecordSQLConnectorPoolSnapshot(snapshot SQLConnectorPoolSnapshot) {
	if m == nil || !m.enabled {
		return
	}
	m.sqlPoolEntries.WithLabelValues("active").Set(float64(max(0, snapshot.Entries-snapshot.Retired)))
	m.sqlPoolEntries.WithLabelValues("retired").Set(float64(max(0, snapshot.Retired)))
	m.sqlPoolEntries.WithLabelValues("leased").Set(float64(max(0, snapshot.ActiveLeases)))
	m.sqlPoolConnections.WithLabelValues("open").Set(float64(max(0, snapshot.OpenConnections)))
	m.sqlPoolConnections.WithLabelValues("in_use").Set(float64(max(0, snapshot.InUseConnections)))
	m.sqlPoolConnections.WithLabelValues("idle").Set(float64(max(0, snapshot.IdleConnections)))
	m.sqlPoolConnections.WithLabelValues("max_open").Set(float64(max(0, snapshot.MaxOpenConnections)))
	m.sqlPoolWaitCount.WithLabelValues().Set(float64(max(0, snapshot.WaitCount)))
	m.sqlPoolWaitDuration.WithLabelValues().Set(max(0, snapshot.WaitDuration.Seconds()))
	m.sqlPoolClosedConnections.WithLabelValues("max_idle").Set(float64(max(0, snapshot.MaxIdleClosed)))
	m.sqlPoolClosedConnections.WithLabelValues("max_idle_time").Set(float64(max(0, snapshot.MaxIdleTimeClosed)))
	m.sqlPoolClosedConnections.WithLabelValues("max_lifetime").Set(float64(max(0, snapshot.MaxLifetimeClosed)))
}

func connectorConcurrencyScope(scope string) string {
	switch scope {
	case ConnectorConcurrencyScopeDeployment, ConnectorConcurrencyScopeProcess:
		return scope
	default:
		return "unknown"
	}
}

func (m *Meter) RecordPipelineRun(metric PipelineRunMetric) {
	if m == nil || !m.enabled {
		return
	}

	trigger := normalizedLabelValue(metric.Trigger, "manual")
	outcome := normalizedLabelValue(metric.Outcome, "unknown")
	errorType := normalizedLabelValue(metric.ErrorType, "none")

	m.pipelineRunDuration.WithLabelValues(trigger, outcome, errorType).Observe(metric.Duration.Seconds())
	m.pipelineRunTotal.WithLabelValues(trigger, outcome, errorType).Inc()
}

func (m *Meter) RecordHTTPClient(metric HTTPClientMetric) {
	if m == nil || !m.enabled {
		return
	}

	component := normalizedLabelValue(metric.Component, "unknown")
	method := normalizedMethod(metric.Method)
	serverAddress := normalizedLabelValue(metric.ServerAddress, "unknown")
	statusCode := "0"
	if metric.StatusCode > 0 {
		statusCode = strconv.Itoa(metric.StatusCode)
	}
	errorType := normalizedLabelValue(metric.ErrorType, "none")

	m.httpClientDuration.WithLabelValues(
		component,
		method,
		serverAddress,
		statusCode,
		errorType,
	).Observe(metric.Duration.Seconds())
}

func (m *Meter) RegisterPostgresPool(provider PostgresPoolStatsProvider) {
	if m == nil || !m.enabled || provider == nil {
		return
	}

	registerer := m.registry
	mustRegisterCollector(registerer, prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "braidplane_postgres_pool_connections",
			Help: "Current PostgreSQL pool connections grouped by state.",
			ConstLabels: prometheus.Labels{
				"service_name":           m.serviceName,
				"deployment_environment": m.environment,
				"state":                  "acquired",
			},
		},
		func() float64 { return float64(provider().AcquiredConns()) },
	))
	mustRegisterCollector(registerer, prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "braidplane_postgres_pool_connections",
			Help: "Current PostgreSQL pool connections grouped by state.",
			ConstLabels: prometheus.Labels{
				"service_name":           m.serviceName,
				"deployment_environment": m.environment,
				"state":                  "idle",
			},
		},
		func() float64 { return float64(provider().IdleConns()) },
	))
	mustRegisterCollector(registerer, prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "braidplane_postgres_pool_connections",
			Help: "Current PostgreSQL pool connections grouped by state.",
			ConstLabels: prometheus.Labels{
				"service_name":           m.serviceName,
				"deployment_environment": m.environment,
				"state":                  "total",
			},
		},
		func() float64 { return float64(provider().TotalConns()) },
	))
	mustRegisterCollector(registerer, prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "braidplane_postgres_pool_connections",
			Help: "Current PostgreSQL pool connections grouped by state.",
			ConstLabels: prometheus.Labels{
				"service_name":           m.serviceName,
				"deployment_environment": m.environment,
				"state":                  "constructing",
			},
		},
		func() float64 { return float64(provider().ConstructingConns()) },
	))
	mustRegisterCollector(registerer, prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "braidplane_postgres_pool_max_connections",
			Help: "Configured PostgreSQL pool maximum connections.",
			ConstLabels: prometheus.Labels{
				"service_name":           m.serviceName,
				"deployment_environment": m.environment,
			},
		},
		func() float64 { return float64(provider().MaxConns()) },
	))
	mustRegisterCollector(registerer, prometheus.NewCounterFunc(
		prometheus.CounterOpts{
			Name: "braidplane_postgres_pool_acquire_total",
			Help: "Cumulative PostgreSQL pool acquire counts grouped by outcome.",
			ConstLabels: prometheus.Labels{
				"service_name":           m.serviceName,
				"deployment_environment": m.environment,
				"outcome":                "success",
			},
		},
		func() float64 { return float64(provider().AcquireCount()) },
	))
	mustRegisterCollector(registerer, prometheus.NewCounterFunc(
		prometheus.CounterOpts{
			Name: "braidplane_postgres_pool_acquire_total",
			Help: "Cumulative PostgreSQL pool acquire counts grouped by outcome.",
			ConstLabels: prometheus.Labels{
				"service_name":           m.serviceName,
				"deployment_environment": m.environment,
				"outcome":                "canceled",
			},
		},
		func() float64 { return float64(provider().CanceledAcquireCount()) },
	))
	mustRegisterCollector(registerer, prometheus.NewCounterFunc(
		prometheus.CounterOpts{
			Name: "braidplane_postgres_pool_acquire_total",
			Help: "Cumulative PostgreSQL pool acquire counts grouped by outcome.",
			ConstLabels: prometheus.Labels{
				"service_name":           m.serviceName,
				"deployment_environment": m.environment,
				"outcome":                "empty",
			},
		},
		func() float64 { return float64(provider().EmptyAcquireCount()) },
	))
	mustRegisterCollector(registerer, prometheus.NewCounterFunc(
		prometheus.CounterOpts{
			Name: "braidplane_postgres_pool_acquire_duration_seconds_total",
			Help: "Cumulative PostgreSQL pool acquire wait time in seconds.",
			ConstLabels: prometheus.Labels{
				"service_name":           m.serviceName,
				"deployment_environment": m.environment,
				"outcome":                "success",
			},
		},
		func() float64 { return provider().AcquireDuration().Seconds() },
	))
}

// RegisterRiverQueueHealth exposes low-cardinality River queue health metrics.
func (m *Meter) RegisterRiverQueueHealth(config RiverQueueHealthConfig) {
	if m == nil || !m.enabled || config.Provider == nil {
		return
	}

	collector := newRiverQueueHealthCollector(m.serviceName, m.environment, config)
	if collector == nil {
		return
	}

	mustRegisterCollector(m.registry, collector)
}

// NewRiverQueueHealthPostgresProvider builds River health snapshots from River's Postgres tables.
func NewRiverQueueHealthPostgresProvider(
	pool *pgxpool.Pool,
	config RiverQueueHealthPostgresConfig,
) RiverQueueHealthProvider {
	allowedQueues := newRiverQueueAllowlist(config.AllowedQueues)
	periodicJobKinds := riverSafeTokens(config.PeriodicJobKinds)
	queryTimeout := config.QueryTimeout
	if queryTimeout <= 0 {
		queryTimeout = 5 * time.Second
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}

	return func(ctx context.Context) RiverQueueHealthSnapshot {
		if pool == nil || len(allowedQueues) == 0 {
			return RiverQueueHealthSnapshot{}
		}

		ctx, cancel := context.WithTimeout(ctx, queryTimeout)
		defer cancel()

		provider := riverPostgresHealthProvider{
			pool:             pool,
			allowedQueues:    allowedQueues.sortedValues(),
			periodicJobKinds: periodicJobKinds,
			now:              now,
		}

		return provider.snapshot(ctx)
	}
}

func (m *Meter) beginHTTPClientRequest(component, method, serverAddress string) func() {
	if m == nil || !m.enabled {
		return func() {}
	}

	m.httpClientActive.WithLabelValues(
		normalizedLabelValue(component, "unknown"),
		normalizedMethod(method),
		normalizedLabelValue(serverAddress, "unknown"),
	).Inc()

	return func() {
		m.httpClientActive.WithLabelValues(
			normalizedLabelValue(component, "unknown"),
			normalizedMethod(method),
			normalizedLabelValue(serverAddress, "unknown"),
		).Dec()
	}
}

func currentProcessMeter() *Meter {
	processMeterMu.RLock()
	defer processMeterMu.RUnlock()
	return processMeter
}

func newHistogramVec(
	name, help string,
	labels []string,
	serviceName, environment string,
	buckets []float64,
) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    name,
			Help:    help,
			Buckets: buckets,
			ConstLabels: prometheus.Labels{
				"service_name":           serviceName,
				"deployment_environment": environment,
			},
		},
		labels,
	)
}

func newCounterVec(
	name, help string,
	labels []string,
	serviceName, environment string,
) *prometheus.CounterVec {
	return prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: name,
			Help: help,
			ConstLabels: prometheus.Labels{
				"service_name":           serviceName,
				"deployment_environment": environment,
			},
		},
		labels,
	)
}

func newGaugeVec(
	name, help string,
	labels []string,
	serviceName, environment string,
) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: name,
			Help: help,
			ConstLabels: prometheus.Labels{
				"service_name":           serviceName,
				"deployment_environment": environment,
			},
		},
		labels,
	)
}

func normalizedLabelValue(value, fallback string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return fallback
	}
	return value
}

func mustRegisterCollector(registerer prometheus.Registerer, collector prometheus.Collector) {
	if err := registerer.Register(collector); err != nil {
		var alreadyRegisteredErr prometheus.AlreadyRegisteredError
		if xerror.As(err, &alreadyRegisteredErr) {
			return
		}
		panic(err)
	}
}

func normalizedMethod(method string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	switch method {
	case http.MethodGet,
		http.MethodHead,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodConnect,
		http.MethodOptions,
		http.MethodTrace:
		return method
	default:
		return "_OTHER"
	}
}

func fiberRoute(c fiber.Ctx) string {
	route := c.Route()
	if route == nil {
		return ""
	}
	return route.Path
}

func responseStatusCode(c fiber.Ctx, err error) int {
	if err == nil {
		return c.Response().StatusCode()
	}

	var fiberErr *fiber.Error
	if xerror.As(err, &fiberErr) {
		return fiberErr.Code
	}

	var apiErr *apierror.Error
	if xerror.As(err, &apiErr) {
		return apiErr.HTTPStatus
	}

	return http.StatusInternalServerError
}

func errorType(err error, statusCode int) string {
	if err == nil && statusCode < http.StatusInternalServerError {
		return "none"
	}

	if err == nil {
		return strconv.Itoa(statusCode)
	}

	var fiberErr *fiber.Error
	if xerror.As(err, &fiberErr) {
		return "fiber_error"
	}

	var apiErr *apierror.Error
	if xerror.As(err, &apiErr) {
		return apiErr.Code
	}

	var appErr *xerror.Error
	if xerror.As(err, &appErr) {
		return appErr.Code()
	}

	return "error"
}

func httpClientErrorType(err error, statusCode int) string {
	if err != nil {
		var appErr *xerror.Error
		if xerror.As(err, &appErr) {
			return appErr.Code()
		}
		return "transport_error"
	}
	if statusCode >= http.StatusInternalServerError {
		return strconv.Itoa(statusCode)
	}
	return "none"
}

type httpClientRoundTripper struct {
	base          http.RoundTripper
	component     string
	serverAddress string
}

func (rt httpClientRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	meter := currentProcessMeter()
	if meter == nil || !meter.enabled {
		return rt.base.RoundTrip(req)
	}

	endActive := meter.beginHTTPClientRequest(rt.component, req.Method, rt.serverAddress)
	defer endActive()

	start := time.Now()
	resp, err := rt.base.RoundTrip(req)
	statusCode := 0
	if resp != nil {
		statusCode = resp.StatusCode
	}
	meter.RecordHTTPClient(HTTPClientMetric{
		Component:     rt.component,
		Method:        req.Method,
		ServerAddress: rt.serverAddress,
		StatusCode:    statusCode,
		ErrorType:     httpClientErrorType(err, statusCode),
		Duration:      time.Since(start),
	})

	return resp, err
}

type fiberResponseRecorder struct {
	header          http.Header
	statusCodeValue int
	body            []byte
}

func newFiberResponseRecorder() *fiberResponseRecorder {
	return &fiberResponseRecorder{
		header: http.Header{},
	}
}

func (r *fiberResponseRecorder) Header() http.Header {
	return r.header
}

func (r *fiberResponseRecorder) WriteHeader(statusCode int) {
	r.statusCodeValue = statusCode
}

func (r *fiberResponseRecorder) Write(body []byte) (int, error) {
	r.body = append(r.body, body...)
	return len(body), nil
}

func (r *fiberResponseRecorder) statusCode() int {
	if r.statusCodeValue == 0 {
		return http.StatusOK
	}
	return r.statusCodeValue
}
