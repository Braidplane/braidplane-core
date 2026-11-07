package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/stretchr/testify/require"

	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	transporthttp "github.com/Braidplane/braidplane-core/transport/http"
)

// TestBackendRuntimeModeCapabilities locks the composition boundary for each command.
func TestBackendRuntimeModeCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		mode             Mode
		wantAPIHTTP      bool
		wantRiverWorkers bool
	}{
		{name: "API", mode: ModeAPI, wantAPIHTTP: true},
		{name: "Worker", mode: ModeWorker, wantRiverWorkers: true},
		{name: "Executor", mode: ModeExecutor},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			capabilities := tt.mode.capabilities()
			require.Equal(t, tt.wantAPIHTTP, capabilities.apiHTTP)
			require.Equal(t, tt.wantRiverWorkers, capabilities.riverWorkers)
		})
	}
}

func TestExecutionBackendComposition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mode          Mode
		backend       string
		wantDispatch  bool
		wantProcessor bool
	}{
		{name: "APIRiver", mode: ModeAPI, backend: backendconfig.ExecutionBackendRiver, wantDispatch: true},
		{name: "WorkerRiver", mode: ModeWorker, backend: backendconfig.ExecutionBackendRiver, wantDispatch: true, wantProcessor: true},
		{name: "APIPoller", mode: ModeAPI, backend: backendconfig.ExecutionBackendPoller},
		{name: "WorkerPoller", mode: ModeWorker, backend: backendconfig.ExecutionBackendPoller},
		{name: "ExecutorPoller", mode: ModeExecutor, backend: backendconfig.ExecutionBackendPoller},
		{name: "ServiceCommandRiver", mode: ModeServiceCommand, backend: backendconfig.ExecutionBackendRiver, wantDispatch: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			composition := executionBackendCompositionFor(tt.mode, tt.backend)
			require.Equal(t, tt.wantDispatch, composition.dispatchesRiverJobs)
			require.Equal(t, tt.wantProcessor, composition.processesRiverJobs)
		})
	}
}

func TestValidateBackendRuntimeMode(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateBackendRuntimeMode(ModeAPI, backendconfig.ExecutionBackendRiver))
	require.NoError(t, validateBackendRuntimeMode(ModeWorker, backendconfig.ExecutionBackendRiver))
	require.NoError(t, validateBackendRuntimeMode(ModeExecutor, backendconfig.ExecutionBackendPoller))
	require.ErrorContains(
		t,
		validateBackendRuntimeMode(ModeExecutor, backendconfig.ExecutionBackendRiver),
		"requires execution.backend=poller",
	)
}

func TestBackendSessionConfigUsesHostOnlyCookie(t *testing.T) {
	t.Parallel()

	got := backendSessionConfig(nil, backendconfig.SessionConfig{CookieSecure: true})

	require.Equal(t, 24*time.Hour, got.IdleTimeout)
	require.Equal(t, "/", got.CookiePath)
	require.Empty(t, got.CookieDomain)
	require.Equal(t, "Lax", got.CookieSameSite)
	require.True(t, got.CookieHTTPOnly)
	require.True(t, got.CookieSecure)
}

func TestBackendCORSConfigDisablesCredentialedCrossOriginRequests(t *testing.T) {
	t.Parallel()

	got := backendCORSConfig(backendconfig.CORSConfig{
		AllowedOrigins: []string{"https://app.example.test"},
	})

	require.Equal(t, "https://app.example.test", strings.Join(got.AllowOrigins, ","))
	require.False(t, got.AllowCredentials)
}

// TestRunBackendRuntimeLifecycle verifies each role starts only its owned components and stops workers on every exit path.
func TestRunBackendRuntimeLifecycle(t *testing.T) {
	t.Parallel()

	startErr := xerror.Unexpected().Detail("worker startup failed")
	httpErr := xerror.Unexpected().Detail("HTTP runtime failed")
	cleanupErr := xerror.Unexpected().Detail("worker cleanup failed")
	tests := []struct {
		name       string
		mode       Mode
		startErr   error
		httpErr    error
		cleanupErr error
		wantEvents []string
		wantErrs   []error
	}{
		{
			name:       "APIStartsOnlyHTTP",
			mode:       ModeAPI,
			wantEvents: []string{"http.run"},
		},
		{
			name:       "WorkerStartsOperationalHTTPThenStopsWorkers",
			mode:       ModeWorker,
			wantEvents: []string{"workers.start", "worker-http.run", "workers.stop"},
		},
		{
			name:       "ExecutorStartsOnlyOperationalHTTPAndExecutionWorkers",
			mode:       ModeExecutor,
			wantEvents: []string{"worker-http.run"},
		},
		{
			name:       "WorkerStartupFailureDoesNotStartHTTP",
			mode:       ModeWorker,
			startErr:   startErr,
			wantEvents: []string{"workers.start"},
			wantErrs:   []error{startErr},
		},
		{
			name:       "WorkerHTTPFailureStillStopsWorkers",
			mode:       ModeWorker,
			httpErr:    httpErr,
			wantEvents: []string{"workers.start", "worker-http.run", "workers.stop"},
			wantErrs:   []error{httpErr},
		},
		{
			name:       "WorkerCleanupFailureIsReturned",
			mode:       ModeWorker,
			cleanupErr: cleanupErr,
			wantEvents: []string{"workers.start", "worker-http.run", "workers.stop"},
			wantErrs:   []error{cleanupErr},
		},
		{
			name:       "WorkerHTTPAndCleanupFailuresAreBothReturned",
			mode:       ModeWorker,
			httpErr:    httpErr,
			cleanupErr: cleanupErr,
			wantEvents: []string{"workers.start", "worker-http.run", "workers.stop"},
			wantErrs:   []error{httpErr, cleanupErr},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			events := make([]string, 0, len(tt.wantEvents))
			err := runBackendRuntimeLifecycle(tt.mode, backendRuntimeLifecycle{
				startRiverWorkers: func() (func() error, error) {
					events = append(events, "workers.start")
					if tt.startErr != nil {
						return nil, tt.startErr
					}
					return func() error {
						events = append(events, "workers.stop")
						return tt.cleanupErr
					}, nil
				},
				runHTTP: func() error {
					events = append(events, "http.run")
					return tt.httpErr
				},
				runWorkerHTTP: func() error {
					events = append(events, "worker-http.run")
					return tt.httpErr
				},
			})

			if len(tt.wantErrs) == 0 {
				require.NoError(t, err)
			} else {
				for _, wantErr := range tt.wantErrs {
					require.ErrorIs(t, err, wantErr)
				}
			}
			require.Equal(t, tt.wantEvents, events)
		})
	}
}

func TestRunBackendRuntimeLifecycleWaitsForWorkerCleanup(t *testing.T) {
	t.Parallel()

	cleanupStarted := make(chan struct{})
	allowCleanup := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- runBackendRuntimeLifecycle(ModeWorker, backendRuntimeLifecycle{
			startRiverWorkers: func() (func() error, error) {
				return func() error {
					close(cleanupStarted)
					<-allowCleanup
					return nil
				}, nil
			},
			runWorkerHTTP: func() error { return nil },
		})
	}()

	<-cleanupStarted
	select {
	case err := <-result:
		t.Fatalf("lifecycle returned before worker cleanup completed: %v", err)
	default:
	}

	close(allowCleanup)
	require.NoError(t, <-result)
}

// TestNewBackendOperationalApp verifies worker-safe operational routes are available without the public API.
func TestNewBackendOperationalApp(t *testing.T) {
	t.Parallel()

	meter, err := metrics.New(metrics.Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend-test",
		Environment: "test",
	})
	require.NoError(t, err)

	dependencies := healthyProbeDependencies{}
	app, _ := newBackendOperationalApp(
		backendconfig.ServerConfig{},
		transporthttp.ProbeRoutesConfig{
			Database:   dependencies,
			Migrations: dependencies,
		},
		meter,
	)

	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		response, requestErr := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		require.NoError(t, requestErr)
		require.Equal(t, http.StatusOK, response.StatusCode, path)
		_, requestIDErr := uuid.Parse(response.Header.Get("X-Request-ID"))
		require.NoError(t, requestIDErr)
		require.NoError(t, response.Body.Close())
	}

	response, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api", nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, response.StatusCode)
	require.NoError(t, response.Body.Close())
}

type healthyProbeDependencies struct{}

// Ping reports a healthy database dependency for operational route tests.
func (healthyProbeDependencies) Ping(context.Context) error {
	return nil
}

// Check reports a current migration dependency for operational route tests.
func (healthyProbeDependencies) Check(context.Context) error {
	return nil
}
