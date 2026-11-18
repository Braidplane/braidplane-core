package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	tracespb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	executionruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/tracing"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
)

const (
	collectorImage = "otel/opentelemetry-collector-contrib:0.123.0@sha256:e39311df1f3d941923c00da79ac7ba6269124a870ee87e3c3ad24d60f8aee4d2"
	canary         = "telemetry-private-canary-158"
)

// This suite uses production providers, a real pipeline runner and synthetic I/O.
func TestCollectorExportAndRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Docker collector")
	}
	t.Setenv("DOCKER_AUTH_CONFIG", `{"auths":{}}`)
	t.Setenv("OTEL_BSP_SCHEDULE_DELAY", "100")
	t.Setenv("OTEL_BLRP_SCHEDULE_DELAY", "100")
	var failedLogs, failedTraces, leaked atomic.Bool
	oldHandler := otel.GetErrorHandler()
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		message := err.Error()
		if strings.Contains(message, "/v1/logs") {
			failedLogs.Store(true)
		}
		if strings.Contains(message, "/v1/traces") {
			failedTraces.Store(true)
		}
		if strings.Contains(message, canary) {
			leaked.Store(true)
		}
	}))
	t.Cleanup(func() { otel.SetErrorHandler(oldHandler) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	config, err := os.ReadFile("../../../examples/otel/collector.yaml")
	require.NoError(t, err)
	// Docker otherwise chooses a different published port after stop/start.
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	fixturePort := fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)
	require.NoError(t, listener.Close())
	collector, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        collectorImage,
			Cmd:          []string{"--config=/etc/otelcol-contrib/config.yaml"},
			ExposedPorts: []string{"4318/tcp"},
			HostConfigModifier: func(config *container.HostConfig) {
				config.PortBindings = dockernetwork.PortMap{dockernetwork.MustParsePort("4318/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: fixturePort}}}
			},
			Files: []testcontainers.ContainerFile{
				{Reader: strings.NewReader(string(config)), ContainerFilePath: "/etc/otelcol-contrib/config.yaml", FileMode: 0o644},
				{Reader: strings.NewReader(""), ContainerFilePath: "/tmp/logs.json", FileMode: 0o666},
				{Reader: strings.NewReader(""), ContainerFilePath: "/tmp/traces.json", FileMode: 0o666},
			},
			WaitingFor: wait.ForHTTP("/v1/traces").WithPort("4318/tcp").WithStatusCodeMatcher(func(code int) bool { return code == http.StatusMethodNotAllowed }),
		}, Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if t.Failed() {
			reader, logErr := collector.Logs(context.Background())
			if logErr == nil {
				defer func() { _ = reader.Close() }()
				data, _ := io.ReadAll(reader)
				t.Log(string(data))
			}
		}
		require.NoError(t, collector.Terminate(context.Background()))
	})
	host, err := collector.Host(ctx)
	require.NoError(t, err)
	port, err := collector.MappedPort(ctx, "4318/tcp")
	require.NoError(t, err)
	endpoint := "http://" + net.JoinHostPort(host, port.Port())
	emit, closeProviders := newPipelineEmitter(t, endpoint)
	emit("before-restart", true)
	assertCollector(t, ctx, collector, "before-restart")
	timeout := time.Second
	require.NoError(t, collector.Stop(ctx, &timeout))
	// The same endpoint is unavailable; execution still succeeds and closes in bounds.
	emit("during-outage", false)
	require.Eventually(t, func() bool { return failedLogs.Load() && failedTraces.Load() }, 10*time.Second, 50*time.Millisecond,
		"both existing exporters must encounter the stopped collector before restart")
	require.NoError(t, collector.Start(ctx))
	emit("after-restart", true)
	closeProviders()
	assertCollector(t, ctx, collector, "after-restart")
	require.False(t, leaked.Load(), "exporter failure diagnostics must exclude canaries")
}

func assertCollector(t *testing.T, ctx context.Context, collector testcontainers.Container, run string) {
	t.Helper()
	var rawLogs, rawTraces []byte
	var readError error
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("collector readback: error=%v logs=%d bytes traces=%d bytes", readError, len(rawLogs), len(rawTraces))
		}
	})
	require.Eventually(t, func() bool {
		read := func(path string) []byte {
			reader, err := collector.CopyFileFromContainer(ctx, path)
			if err != nil {
				readError = err
				return nil
			}
			defer func() { _ = reader.Close() }()
			data, _ := io.ReadAll(reader)
			return data
		}
		rawLogs, rawTraces = read("/tmp/logs.json"), read("/tmp/traces.json")
		return strings.Contains(string(rawLogs), run) && strings.Contains(string(rawTraces), run)
	}, 10*time.Second, 100*time.Millisecond)
	require.NotContains(t, string(rawLogs), canary)
	require.NotContains(t, string(rawTraces), canary)
	spans := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(rawTraces)))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		var request tracespb.ExportTraceServiceRequest
		require.NoError(t, protojson.Unmarshal(scanner.Bytes(), &request))
		for _, resource := range request.ResourceSpans {
			assertResource(t, resource.Resource.Attributes, true)
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					require.NotEmpty(t, span.TraceId)
					require.NotEmpty(t, span.SpanId)
					spans[string(span.TraceId)+string(span.SpanId)] = true
				}
			}
		}
	}
	require.NoError(t, scanner.Err())
	correlated := false
	scanner = bufio.NewScanner(strings.NewReader(string(rawLogs)))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		var request logspb.ExportLogsServiceRequest
		require.NoError(t, protojson.Unmarshal(scanner.Bytes(), &request))
		for _, resource := range request.ResourceLogs {
			assertResource(t, resource.Resource.Attributes, false)
			for _, scope := range resource.ScopeLogs {
				for _, record := range scope.LogRecords {
					attrs := stringAttrs(record.Attributes)
					if attrs["pipeline.run_slug"] == run {
						require.Equal(t, "synthetic-org", attrs["organization.slug"])
						require.Equal(t, "synthetic-request", attrs["request_id"])
						require.True(t, spans[string(record.TraceId)+string(record.SpanId)])
						correlated = true
					}
				}
			}
		}
	}
	require.NoError(t, scanner.Err())
	require.True(t, correlated, "collector must retain log/span correlation")
}

func stringAttrs(attrs []*commonpb.KeyValue) map[string]string {
	values := map[string]string{}
	for _, attr := range attrs {
		values[attr.Key] = attr.Value.GetStringValue()
	}
	return values
}

func assertResource(t *testing.T, attrs []*commonpb.KeyValue, traces bool) {
	t.Helper()
	values := stringAttrs(attrs)
	require.Equal(t, "braidplane", values["service.name"])
	require.Equal(t, "qualification", values["deployment.environment"])
	require.Equal(t, "backend", values["braidplane.component"])
	if traces {
		require.Equal(t, "braidplane", values["service.namespace"])
	}
}

func TestExportFailureDoesNotChangePipelineOutcome(t *testing.T) {
	if testing.Short() {
		t.Skip("runtime qualification")
	}
	for _, mode := range []string{"absent", "reject", "timeout", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			var traceCalls atomic.Int32
			var logCalls atomic.Int32
			var leaked atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path == "/v1/traces" {
					traceCalls.Add(1)
				}
				if r.URL.Path == "/v1/logs" {
					logCalls.Add(1)
				}
				data, _ := io.ReadAll(r.Body)
				if strings.Contains(string(data), canary) {
					leaked.Store(true)
				}
				if mode == "timeout" {
					select {
					case <-r.Context().Done():
					case <-time.After(7 * time.Second):
					}
					return
				}
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer server.Close()
			endpoint := server.URL
			if mode == "absent" {
				server.Close()
			}
			if mode == "disabled" {
				t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
				t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", server.URL+"/v1/logs")
				t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", server.URL+"/v1/traces")
				endpoint = ""
			}
			var mu sync.Mutex
			var failures []string
			oldHandler := otel.GetErrorHandler()
			otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { mu.Lock(); defer mu.Unlock(); failures = append(failures, err.Error()) }))
			defer otel.SetErrorHandler(oldHandler)
			emitPipeline(t, endpoint, mode, true)
			mu.Lock()
			defer mu.Unlock()
			require.NotContains(t, strings.Join(failures, "\n"), canary)
			require.False(t, leaked.Load(), "canaries must be absent from failed export requests")
			if mode == "disabled" {
				require.Zero(t, calls.Load())
				require.Empty(t, failures)
			}
			if mode == "reject" || mode == "timeout" {
				require.Positive(t, traceCalls.Load())
				require.Positive(t, logCalls.Load())
			}
		})
	}
}

func TestExporterQueuesDropWithoutBlocking(t *testing.T) {
	if testing.Short() {
		t.Skip("runtime qualification")
	}
	// Small SDK queues make overflow deterministic without a large log fixture.
	for _, prefix := range []string{"OTEL_BSP_", "OTEL_BLRP_"} {
		t.Setenv(prefix+"MAX_QUEUE_SIZE", "8")
		t.Setenv(prefix+"MAX_EXPORT_BATCH_SIZE", "1")
		t.Setenv(prefix+"SCHEDULE_DELAY", "1")
	}
	gate := make(chan struct{})
	var release sync.Once
	unblock := func() { release.Do(func() { close(gate) }) }
	started := make(chan string, 2)
	var mu sync.Mutex
	traceNames, logNames := map[string]bool{}, map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		mu.Lock()
		var first bool
		if r.URL.Path == "/v1/traces" {
			first = len(traceNames) == 0
			var request tracespb.ExportTraceServiceRequest
			if proto.Unmarshal(data, &request) == nil {
				for _, resource := range request.ResourceSpans {
					for _, scope := range resource.ScopeSpans {
						for _, span := range scope.Spans {
							traceNames[span.Name] = true
						}
					}
				}
			}
		} else {
			first = len(logNames) == 0
			var request logspb.ExportLogsServiceRequest
			if proto.Unmarshal(data, &request) == nil {
				for _, resource := range request.ResourceLogs {
					for _, scope := range resource.ScopeLogs {
						for _, record := range scope.LogRecords {
							logNames[record.Body.GetStringValue()] = true
						}
					}
				}
			}
		}
		mu.Unlock()
		if first {
			started <- r.URL.Path
		}
		<-gate
	}))
	defer server.Close()
	defer unblock()
	provider, traceCloser, err := tracing.NewTracerProvider(context.Background(), tracing.Config{OTLPTracesEndpoint: server.URL + "/v1/traces", SamplingRatio: 1})
	require.NoError(t, err)
	t.Cleanup(func() { _ = traceCloser.Close() })
	logger, logCloser, err := logging.Setup(logging.Config{Level: "info", OTLPLogsEndpoint: server.URL + "/v1/logs"}, "backend")
	require.NoError(t, err)
	t.Cleanup(func() { _ = logCloser.Close() })
	emit := func(i int) {
		name := fmt.Sprintf("record-%d", i)
		_, span := provider.Tracer("qualification").Start(context.Background(), name)
		span.End()
		logger.Info(name)
	}
	emit(0)
	for range 2 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("both exporters must start")
		}
	}
	before := time.Now()
	for i := 1; i <= 40; i++ {
		emit(i)
	}
	runSyntheticPipeline(t, context.Background(), false)
	require.Less(t, time.Since(before), 2*time.Second)
	unblock()
	require.NoError(t, logCloser.Close())
	require.NoError(t, traceCloser.Close())
	mu.Lock()
	defer mu.Unlock()
	require.Less(t, len(traceNames), 41)
	require.Less(t, len(logNames), 41)
	require.True(t, traceNames["record-0"])
	require.False(t, traceNames["record-40"], "full trace queue drops incoming spans")
	require.True(t, logNames["record-40"], "full log queue retains newest records")
	require.False(t, logNames["record-1"], "full log queue drops oldest queued records")
}

func emitPipeline(t *testing.T, endpoint, run string, includeFailure bool) {
	t.Helper()
	emit, closeProviders := newPipelineEmitter(t, endpoint)
	emit(run, includeFailure)
	closeProviders()
}

func newPipelineEmitter(t *testing.T, endpoint string) (func(string, bool), func()) {
	t.Helper()
	traceEndpoint, logEndpoint := "", ""
	if endpoint != "" {
		traceEndpoint, logEndpoint = endpoint+"/v1/traces", endpoint+"/v1/logs"
	}
	provider, traceCloser, err := tracing.NewTracerProvider(context.Background(), tracing.Config{
		Component: "backend", Environment: "qualification", OTLPTracesEndpoint: traceEndpoint, SamplingRatio: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = traceCloser.Close() })
	logger, logCloser, err := logging.Setup(logging.Config{Level: "debug", Environment: "qualification", OTLPLogsEndpoint: logEndpoint}, "backend")
	require.NoError(t, err)
	oldProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	oldLogger := slog.Default()
	slog.SetDefault(logger)
	var once sync.Once
	closeProviders := func() {
		once.Do(func() {
			defer otel.SetTracerProvider(oldProvider)
			defer slog.SetDefault(oldLogger)
			started := time.Now()
			// Shutdown may report delivery failure; each provider gets a five-second budget.
			logErr, traceErr := logCloser.Close(), traceCloser.Close()
			require.Less(t, time.Since(started), 12*time.Second)
			for _, closeErr := range []error{logErr, traceErr} {
				if closeErr != nil {
					require.NotContains(t, closeErr.Error(), canary)
				}
			}
			if endpoint == "" {
				require.NoError(t, logErr)
				require.NoError(t, traceErr)
			}
		})
	}
	t.Cleanup(closeProviders)
	emit := func(run string, includeFailure bool) {
		ctx := reqctx.WithRequestID(context.Background(), "synthetic-request")
		ctx = logging.WithAttrs(ctx, slog.String("organization.slug", "synthetic-org"), slog.String("pipeline.run_slug", run), slog.String("password", canary))
		ctx, span := tracing.StartMethodSpan(ctx, tracing.MethodSpan{Layer: "service", Package: "qualification", Method: "Run"})
		require.True(t, span.IsRecording(), "qualification span must be sampled and recording")
		span.SetAttributes(attribute.String("qualification.run", run))
		started := time.Now()
		runSyntheticPipeline(t, ctx, false)
		if includeFailure {
			runSyntheticPipeline(t, ctx, true)
		}
		logging.LogMethodCompletion(ctx, logger, logging.MethodCompletion{Layer: "service", Package: "qualification", Method: "Run", StartedAt: started})
		privateErr := xerror.Unexpected().Detail("password=" + canary)
		tracing.RecordError(span, privateErr)
		logging.LogError(ctx, "synthetic failure", privateErr)
		span.End()
		require.Less(t, time.Since(started), 2*time.Second, "telemetry must not block pipeline execution")
	}
	return emit, closeProviders
}

type (
	syntheticSources struct{ datasourcessvc.Service }
	syntheticQueries struct{ testutil.FakeQueryExecutor }
)

func (syntheticQueries) GetExpectedInputContract(context.Context, slug.Slug, slug.Slug, slug.Slug) (*payload.Contract, error) {
	return nil, xerror.Unexpected().Detail("unexpected query in event-input fixture")
}

type (
	syntheticSink  struct{ fail bool }
	syntheticStore struct{ pipelinerun.RunStore }
)

func (syntheticStore) Get(context.Context, slug.Slug, slug.Slug, slug.Slug, string) (*pipelinerun.RunTimeline, error) {
	return nil, pipelinerun.ErrTimelineNotFound
}

func (syntheticStore) PreviousSuccess(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, int64, time.Time) (*executionruntime.SuccessfulRun, error) {
	return nil, pipelinerun.ErrTimelineNotFound
}

func (syntheticStore) PersistRun(context.Context, slug.Slug, slug.Slug, slug.Slug, *pipelinespec.Pipeline, *pipelinerun.RunResult) error {
	return nil
}

func (syntheticStore) ObserveWorker(context.Context, slug.Slug, slug.Slug, slug.Slug, string, time.Time) error {
	return nil
}

func (syntheticSources) FindBySlug(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error) {
	return &datasourcessvc.DataSource{Type: model.DataSourceTypeHTTP}, nil
}

func (s syntheticSink) Execute(ctx context.Context, input *pipelineruntime.SinkRuntimeInput) error {
	if s.fail {
		return xerror.Unexpected().Detail("password=" + canary)
	}
	return nil
}

func runSyntheticPipeline(t *testing.T, ctx context.Context, fail bool) {
	t.Helper()
	contract := &payload.Contract{
		Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject,
		Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueString, Required: true}},
	}
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "synthetic", Name: "Synthetic"}, Steps: []pipelinespec.Step{
		{Kind: "input", Slug: "event", Input: &pipelinespec.InputSpec{OutputContract: contract}},
		{Kind: "sink", Slug: "send", Sink: &pipelinespec.SinkSpec{
			Input: &pipelinespec.StepInput{FromStep: "event"}, InputContract: contract,
			DataSourceSlug: "synthetic-http", OperationType: "http", OperationRef: "send", IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
		}},
	}}
	input, err := pipelinerun.NewVerifiedJSONEventInput(must.Slug("event"), json.RawMessage(`{"id":"synthetic","password":"`+canary+`"}`))
	require.NoError(t, err)
	runner, err := pipelineruntime.NewRunner(&syntheticQueries{}, nil, syntheticStore{}, syntheticSources{}, syntheticSink{fail: fail})
	require.NoError(t, err)
	result, err := runner.Run(ctx, pipelineruntime.RunRequest{OrgSlug: "synthetic-org", ProjectSlug: "synthetic-project", EnvironmentSlug: "test", RunSlug: "synthetic-run", Pipeline: pipeline, EventInput: input})
	if fail {
		require.Error(t, err)
	} else {
		require.NoError(t, err)
		require.Equal(t, "succeeded", result.Metadata.Status)
	}
}
