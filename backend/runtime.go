package backend

import (
	"context"
	"encoding/hex"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/compress"
	"github.com/gofiber/fiber/v3/middleware/cors"
	recovermiddleware "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	oapi "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/dbpool"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/i18n"
	"github.com/Braidplane/braidplane-core/pkg/lifecycle"
	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slack"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/tracing"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/abusecontrols"
	"github.com/Braidplane/braidplane-core/postgres/accountclosures"
	"github.com/Braidplane/braidplane-core/postgres/apikeys"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/Braidplane/braidplane-core/postgres/datasourcerefs"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/environments"
	eventsrepo "github.com/Braidplane/braidplane-core/postgres/events"
	githubappsrepo "github.com/Braidplane/braidplane-core/postgres/githubapps"
	"github.com/Braidplane/braidplane-core/postgres/gitlabproofwebhooks"
	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"
	"github.com/Braidplane/braidplane-core/postgres/memberinvitations"
	"github.com/Braidplane/braidplane-core/postgres/members"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/postgres/pats"
	"github.com/Braidplane/braidplane-core/postgres/permissions"
	"github.com/Braidplane/braidplane-core/postgres/pipelinecreations"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/pipelinesinkeffects"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/postgres/pipelinewebhooks"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	proofrepo "github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/postgres/proofconfig"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	postgresqueries "github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/postgres/repositorysetups"
	retentionrepo "github.com/Braidplane/braidplane-core/postgres/retention"
	"github.com/Braidplane/braidplane-core/postgres/roles"
	"github.com/Braidplane/braidplane-core/postgres/runrecency"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/postgres/wishlistregistrations"
	"github.com/Braidplane/braidplane-core/service/accountclosuresvc"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/apikeyssvc"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/contractplansvc"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	bigqueryoauth "github.com/Braidplane/braidplane-core/service/credentialssvc/provider/bigquery"
	gitlaboauth "github.com/Braidplane/braidplane-core/service/credentialssvc/provider/gitlab"
	workspaceoauth "github.com/Braidplane/braidplane-core/service/credentialssvc/provider/googleworkspace"
	hubspotoauth "github.com/Braidplane/braidplane-core/service/credentialssvc/provider/hubspot"
	slackoauth "github.com/Braidplane/braidplane-core/service/credentialssvc/provider/slack"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/email"
	"github.com/Braidplane/braidplane-core/service/environmentssvc"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/executionbroker"
	"github.com/Braidplane/braidplane-core/service/githubappsvc"
	"github.com/Braidplane/braidplane-core/service/gitlabproofwebhooksvc"
	"github.com/Braidplane/braidplane-core/service/memberssvc"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	slacknotifications "github.com/Braidplane/braidplane-core/service/notificationsvc/provider/slack"
	"github.com/Braidplane/braidplane-core/service/odcscontractsvc"
	"github.com/Braidplane/braidplane-core/service/oidc"
	"github.com/Braidplane/braidplane-core/service/orgsvc"
	"github.com/Braidplane/braidplane-core/service/patssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelineschedulesvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/pipelinetriggersvc"
	"github.com/Braidplane/braidplane-core/service/pipelinewebhooksvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
	"github.com/Braidplane/braidplane-core/service/proofcandidatesvc"
	"github.com/Braidplane/braidplane-core/service/proofconfigsvc"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
	"github.com/Braidplane/braidplane-core/service/proofstatussvc"
	"github.com/Braidplane/braidplane-core/service/proofwebhooksvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/Braidplane/braidplane-core/service/retentionsvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	"github.com/Braidplane/braidplane-core/service/runrecencysvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
	backgroundworker "github.com/Braidplane/braidplane-core/service/worker"
	workerconsumer "github.com/Braidplane/braidplane-core/service/worker/consumer"
	workerjobs "github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
	"github.com/Braidplane/braidplane-core/transport/http"
	sessionstore "github.com/gofiber/storage/postgres/v3"
)

// notificationLifecycleEventProducer adapts the worker event producer to the
// notification service's local event producer boundary.
func notificationLifecycleEventProducer(producer workerproducer.EventProducer) notificationsvc.LifecycleEventProducer {
	return notificationLifecycleEventProducerAdapter{producer: producer}
}

type notificationLifecycleEventProducerAdapter struct {
	producer workerproducer.EventProducer
}

func (a notificationLifecycleEventProducerAdapter) EnqueueRecord(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) {
	a.producer.EnqueueRecord(ctx, tx, orgSlug, actor, event)
}

func (a notificationLifecycleEventProducerAdapter) EnqueueRequired(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) error {
	_, err := a.producer.EnqueueRequired(ctx, tx, orgSlug, actor, event)
	return err
}

type pipelineRuntimeResolverBundle struct {
	datasourcerefssvc.Service
	pipelineruntime.EnvironmentSecretResolver
}

func newBackendQueueClient(
	pool *pgxpool.Pool,
	logger *slog.Logger,
	registrars []queue.WorkerRegistrar,
	queues map[string]river.QueueConfig,
) (*queue.Client, error) {
	return queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
		Logger:      logger,
		Queues:      queues,
		RetryPolicy: &river.DefaultClientRetryPolicy{},
		Registrars:  registrars,
	})
}

// riverQueueHealthProvider reads River table aggregates and DB-backed worker heartbeats.
func riverQueueHealthProvider(
	pool *pgxpool.Pool,
	queues map[string]river.QueueConfig,
) metrics.RiverQueueHealthProvider {
	return metrics.NewRiverQueueHealthPostgresProvider(pool, metrics.RiverQueueHealthPostgresConfig{
		AllowedQueues: riverQueueNames(queues),
		PeriodicJobKinds: []string{
			workerjobs.EventPruneArgs{}.Kind(),
			workerjobs.PipelineScheduleDispatchArgs{}.Kind(),
		},
	})
}

// riverQueueNames returns configured River queue names in deterministic order.
func riverQueueNames(queues map[string]river.QueueConfig) []string {
	queueNames := make([]string, 0, len(queues))
	for queueName := range queues {
		queueNames = append(queueNames, queueName)
	}
	sort.Strings(queueNames)
	return queueNames
}

// backendWorkerQueueConcurrency maps operator configuration to the worker-owned queue contract.
func backendWorkerQueueConcurrency(cfg backendconfig.WorkerConfig) backgroundworker.QueueConcurrency {
	return backgroundworker.QueueConcurrency{
		PipelineGit:            cfg.PipelineGitMaxWorkers,
		Email:                  cfg.EmailMaxWorkers,
		NotificationDelivery:   cfg.NotificationDeliveryMaxWorkers,
		Events:                 cfg.EventsMaxWorkers,
		EventMaintenance:       cfg.EventMaintenanceMaxWorkers,
		PipelineSchedule:       cfg.PipelineScheduleMaxWorkers,
		PipelineRetry:          cfg.PipelineRetryMaxWorkers,
		PipelineRun:            cfg.PipelineRunMaxWorkers,
		DataSourceSchemaImport: cfg.DataSourceSchemaImportMaxWorkers,
	}
}

// newBackendOperationalApp creates the probe and metrics surface shared by API and worker processes.
func newBackendOperationalApp(
	serverConfig backendconfig.ServerConfig,
	probeConfig http.ProbeRoutesConfig,
	metricsMeter *metrics.Meter,
) (*fiber.App, *lifecycle.DrainLifecycle) {
	app := fiber.New(fiber.Config{
		ErrorHandler:       apierror.Middleware(),
		ProxyHeader:        serverConfig.ProxyHeader,
		TrustProxy:         len(serverConfig.TrustedProxies) > 0,
		TrustProxyConfig:   fiber.TrustProxyConfig{Proxies: serverConfig.TrustedProxies},
		EnableIPValidation: serverConfig.ProxyHeader != "",
	})
	app.Use(
		recovermiddleware.New(recovermiddleware.Config{EnableStackTrace: true}),
		compress.New(),
		requestid.New(requestid.Config{Generator: uuid.NewString}),
	)
	app.Use(func(c fiber.Ctx) error {
		requestID := reqctx.FiberRequestID(c)
		if requestID != "" {
			c.SetContext(reqctx.WithRequestID(c.Context(), requestID))
		}

		return c.Next()
	})

	drainLifecycle := lifecycle.NewDrainLifecycle()
	probeConfig.Lifecycle = drainLifecycle
	http.RegisterProbeRoutes(app, probeConfig)
	metricsMeter.RegisterRoutes(app)

	return app, drainLifecycle
}

// runBackendHTTP serves one process HTTP surface and drains readiness before graceful shutdown.
func runBackendHTTP(
	app *fiber.App,
	drainLifecycle *lifecycle.DrainLifecycle,
	serverConfig backendconfig.ServerConfig,
	logger *slog.Logger,
) error {
	logger.Info("starting server", slog.String("address", serverConfig.Address()))

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- app.Listen(serverConfig.Address())
	}()

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(shutdown)

	select {
	case err := <-serverErrors:
		return xerror.Unexpected().With(err)
	case sig := <-shutdown:
		logger.Info("shutdown signal received", slog.String("signal", sig.String()))

		if drainLifecycle.BeginDrain() {
			logger.Info(
				"readiness drain started",
				slog.Duration("duration", serverConfig.DrainTimeout.TimeDuration()),
			)
		}
		if err := drainLifecycle.WaitDrainWindow(
			context.Background(),
			serverConfig.DrainTimeout.TimeDuration(),
		); err != nil {
			logger.Error("readiness drain wait failed", slog.Any("error", err))
			return xerror.Unexpected().With(err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), serverConfig.ShutdownTimeout.TimeDuration())
		defer cancel()

		if err := app.ShutdownWithContext(ctx); err != nil {
			logger.Error("graceful shutdown failed", slog.Any("error", err))
			return xerror.Unexpected().With(err)
		}
		logger.Info("server shutdown complete")
	}

	return nil
}

// backendSessionConfig returns the backend's host-only authentication cookie settings.
func backendSessionConfig(storage fiber.Storage, cfg backendconfig.SessionConfig) session.Config {
	return session.Config{
		Storage:        storage,
		IdleTimeout:    24 * time.Hour,
		CookieSecure:   cfg.CookieSecure,
		CookieHTTPOnly: true,
		CookieSameSite: "Lax",
		CookiePath:     "/",
	}
}

// backendCORSConfig returns the API's non-credentialed cross-origin policy.
func backendCORSConfig(cfg backendconfig.CORSConfig) cors.Config {
	return cors.Config{
		AllowOrigins:     cfg.AllowedOrigins,
		AllowCredentials: false,
		AllowHeaders: []string{
			"Origin",
			"Content-Type",
			"Accept",
			"Authorization",
			"X-CSRF-Token",
			fiber.HeaderXRequestID,
			"traceparent",
			"tracestate",
			"baggage",
		},
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
	}
}

func runExecutionOnlyBackend(
	cfg *backendconfig.Config,
	pool *pgxpool.Pool,
	migrations *postgres.MigrationPreflight,
	metricsMeter *metrics.Meter,
	logger *slog.Logger,
	distribution Distribution,
) error {
	key, err := hex.DecodeString(cfg.Security.ExecutionRequestKeyHex)
	if err != nil {
		return xerror.Wrapf("decode execution request key: %w", err)
	}
	cipher, err := crypto.NewAES256(key)
	if err != nil {
		return xerror.Wrapf("initialize execution request cipher: %w", err)
	}
	broker, err := executionbroker.New(postgres.NewTransactionManager(pool),
		cipher,
		executionbroker.NewPollerDispatcher(),
		cfg.Execution.RequestTimeout.TimeDuration(),
		distribution.ConnectorFactory(),
		logger,
		distribution,
		cfg.ExecutionLimits.NativeFiles.Profile(),
	)
	if err != nil {
		return xerror.Wrapf("initialize execution worker broker: %w", err)
	}
	defer func() {
		if closeErr := broker.Close(); closeErr != nil {
			logger.Error("close execution connector runtime pool", slog.Any("error", closeErr))
		}
	}()
	executorCtx, stopExecutor := context.WithCancel(context.Background())
	defer stopExecutor()
	if err := broker.Start(executorCtx, cfg.ExecutionLimits.ConnectorConcurrencyPerDataSource); err != nil {
		return xerror.Wrapf("start execution-only request poller: %w", err)
	}

	app, drainLifecycle := newBackendOperationalApp(cfg.Server, http.ProbeRoutesConfig{
		Database: pool, Migrations: migrations,
	}, metricsMeter)
	return runBackendHTTP(app, drainLifecycle, cfg.Server, logger)
}

// Run composes the core backend with required distribution-owned policies and routes.
func Run(cfg *backendconfig.Config, mode Mode, createDistribution DistributionFactory) error {
	if cfg == nil || createDistribution == nil {
		return xerror.Unexpected().Detail("backend configuration and distribution factory are required")
	}
	if !mode.IsAMode() {
		return xerror.ValidationFailed().Detail("invalid backend runtime mode")
	}
	var err error
	if err := validateBackendRuntimeMode(mode, cfg.Execution.Backend); err != nil {
		return err
	}
	runtimeCtx, stopRuntime := context.WithCancel(context.Background())
	defer stopRuntime()

	// Set up backend logger with file rotation
	backendLogger, backendCloser, err := logging.Setup(logging.Config{
		Level:            cfg.Logging.Level,
		FilePath:         cfg.Logging.File,
		RotationInterval: cfg.Logging.RotationInterval.TimeDuration(),
		MaxFileSizeMB:    cfg.Logging.MaxFileSizeMB,
		Environment:      cfg.Logging.Environment,
		OTLPLogsEndpoint: cfg.Logging.OTLPLogsEndpoint,
	}, "backend")
	if err != nil {
		return xerror.Wrapf("failed to setup backend logger: %w", err)
	}
	defer func() {
		if closeErr := backendCloser.Close(); closeErr != nil {
			backendLogger.Error("failed to close backend logger", slog.Any("error", closeErr))
		}
	}()

	traceCloser, err := tracing.Setup(context.Background(), tracing.Config{
		Component:              "backend",
		Environment:            cfg.Logging.Environment,
		OTLPTracesEndpoint:     cfg.Tracing.OTLPTracesEndpoint,
		SamplingRatio:          cfg.Tracing.SamplingRatio,
		InstallGlobalProviders: true,
	})
	if err != nil {
		return xerror.Wrapf("failed to setup backend tracing: %w", err)
	}
	defer func() {
		if closeErr := traceCloser.Close(); closeErr != nil {
			backendLogger.Error("failed to close backend tracer", slog.Any("error", closeErr))
		}
	}()

	slog.SetDefault(backendLogger)

	metricsMeter, err := metrics.New(metrics.Config{
		Enabled:     cfg.Metrics.Enabled,
		Path:        cfg.Metrics.Path,
		ServiceName: "backend",
		Environment: cfg.Logging.Environment,
	})
	if err != nil {
		return xerror.Wrapf("failed to setup backend metrics: %w", err)
	}
	restoreMetrics := metrics.SetProcessMeter(metricsMeter)
	defer restoreMetrics()

	backendLogger.Info("configuration loaded", slog.String("runtime_mode", mode.String()))

	launchCtx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	// Connect to the database with shared pool bootstrap.
	pool, err := dbpool.Open(launchCtx, cfg.Database.ConnectionString())
	if err != nil {
		return xerror.Wrapf("failed to initialize database pool: %w", err)
	}
	defer pool.Close()
	metricsMeter.RegisterPostgresPool(func() metrics.PostgresPoolStats {
		return pool.Stat()
	})
	backendLogger.Info("database connection established",
		slog.String("host", cfg.Database.Host),
		slog.Int("port", cfg.Database.Port),
		slog.String("database", cfg.Database.Database),
	)

	txm := postgres.NewTransactionManager(pool)
	resources := Resources{Config: cfg, Pool: pool, TransactionManager: txm}
	distribution, err := createDistribution(resources)
	if err != nil {
		return xerror.Wrapf("initialize backend distribution: %w", err)
	}
	if distribution == nil {
		return xerror.Unexpected().Detail("backend distribution is required")
	}

	// Refuse to start when the release binary and database schema are out of sync.
	migrationVersion, err := postgres.LatestEmbeddedMigrationVersion()
	if err != nil {
		return xerror.Wrapf("read release migration version: %w", err)
	}
	migrationPreflight, err := postgres.NewMigrationPreflight(pool, "core_schema_migrations", migrationVersion)
	if err != nil {
		return xerror.Wrapf("failed to initialize migration preflight: %w", err)
	}
	migrationPreflightCtx, migrationPreflightCancel := context.WithTimeout(context.Background(), time.Second*30)
	migrationStatus, err := migrationPreflight.Status(migrationPreflightCtx)
	migrationPreflightCancel()
	if err != nil {
		return xerror.Wrapf("migration preflight failed: %w", err)
	}
	backendLogger.Info("migration preflight passed",
		slog.Uint64("current_version", uint64(migrationStatus.CurrentVersion)),
		slog.Uint64("required_version", uint64(migrationStatus.RequiredVersion)),
	)

	if mode == ModeExecutor {
		return runExecutionOnlyBackend(
			cfg,
			pool,
			migrationPreflight,
			metricsMeter,
			backendLogger,
			distribution,
		)
	}

	// Initialize Argon2 hasher with configured parameters
	argon2Hasher := crypto.NewArgon2WithParams(
		cfg.Security.Argon2Memory,
		cfg.Security.Argon2Iterations,
		cfg.Security.Argon2Parallelism,
		cfg.Security.Argon2SaltLength,
		cfg.Security.Argon2KeyLength,
	)

	// Initialize the users repository and service
	usersRepo := users.NewRepositoryWithLogging(users.NewRepository(), backendLogger)
	orgsRepo := organizations.NewRepositoryWithLogging(organizations.NewRepository(), backendLogger)
	membersRepo := members.NewRepositoryWithLogging(members.NewRepository(), backendLogger)
	memberInvitationsRepo := memberinvitations.NewRepository()
	rolesRepo := roles.NewRepositoryWithLogging(roles.NewRepository(), backendLogger)
	apiKeysRepo := apikeys.NewRepository()
	patsRepo := pats.NewRepository()
	permissionsRepo := permissions.NewRepositoryWithLogging(permissions.NewRepository(), backendLogger)
	projectsRepo := projects.NewRepositoryWithLogging(projects.NewRepository(), backendLogger)
	environmentsRepo := environments.NewRepositoryWithLogging(environments.NewRepository(), backendLogger)
	distributionSources, err := distribution.WrapDataSources(datasources.New())
	if err != nil {
		return xerror.Wrapf("initialize distribution data source repository: %w", err)
	}
	datasourcesRepo := datasources.NewRepositoryWithLogging(distributionSources, backendLogger)
	dataSourceRefsRepo := datasourcerefs.NewRepository()
	pipelineTriggersRepo := pipelinetriggers.NewRepository()
	eventsRepo := eventsrepo.New()
	wishlistRegistrationsRepo := wishlistregistrations.NewRepository()

	// Decode encryption key from hex
	encryptionKeyBytes, err := hex.DecodeString(cfg.Security.EncryptionKeyHex)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	encrypter, err := crypto.NewAES256(encryptionKeyBytes)
	if err != nil {
		return xerror.Wrapf("failed to initialize AES256 encrypter: %w", err)
	}

	connectorFactory := func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		return nil, xerror.Unexpected().Detail("connector execution is forbidden in API runtime")
	}

	enqueueClient, err := queue.NewEnqueueOnly(pool)
	if err != nil {
		return xerror.Wrapf("failed to initialize River enqueue client: %w", err)
	}
	queueEnqueuer := queue.NewEnqueuer(enqueueClient)
	var executionRegistrar *backgroundworker.ExecutionRegistrar

	executionComposition := executionBackendCompositionFor(mode, cfg.Execution.Backend)
	if err := cfg.ValidateExecutionRequestKey(); err != nil {
		return xerror.Wrapf("invalid API execution request key: %w", err)
	}
	executionKey, err := hex.DecodeString(cfg.Security.ExecutionRequestKeyHex)
	if err != nil {
		return xerror.Wrapf("decode execution request key: %w", err)
	}
	executionCipher, err := crypto.NewAES256(executionKey)
	if err != nil {
		return xerror.Wrapf("initialize execution request cipher: %w", err)
	}
	dispatcher := executionbroker.NewPollerDispatcher()
	if executionComposition.dispatchesRiverJobs {
		dispatcher, err = workerproducer.NewExecutionDispatcher(queueEnqueuer)
		if err != nil {
			return xerror.Wrapf("initialize River execution dispatcher: %w", err)
		}
	}
	var executionFactory connector.NewConnectorFunc
	if executionComposition.processesRiverJobs {
		executionFactory = distribution.ConnectorFactory()
	}
	executionRequestBroker, err := executionbroker.New(txm,
		executionCipher,
		dispatcher,
		cfg.Execution.RequestTimeout.TimeDuration(),
		executionFactory,
		backendLogger,
		distribution,
		cfg.ExecutionLimits.NativeFiles.Profile(),
	)
	if err != nil {
		return xerror.Wrapf("initialize API execution request broker: %w", err)
	}
	defer func() {
		if closeErr := executionRequestBroker.Close(); closeErr != nil {
			backendLogger.Error("close execution connector runtime pool", slog.Any("error", closeErr))
		}
	}()
	if executionComposition.processesRiverJobs {
		executionRegistrar, err = backgroundworker.NewExecutionRegistrar(
			executionRequestBroker,
			cfg.Worker.ExecutionMaxWorkers,
		)
		if err != nil {
			return xerror.Wrapf("initialize River execution processor: %w", err)
		}
	}
	executionMaintenanceCtx, stopExecutionMaintenance := context.WithCancel(runtimeCtx)
	defer stopExecutionMaintenance()
	executionRequestBroker.StartMaintenance(executionMaintenanceCtx)
	backendLogger.Info("execution backend selected", slog.String("execution_backend", cfg.Execution.Backend))

	eventProducer, err := workerproducer.NewEventProducer(queueEnqueuer, clock.New())
	if err != nil {
		return xerror.Wrapf("failed to initialize durable event producer: %w", err)
	}
	pipelineGitProducer, err := workerproducer.NewPipelineGitProducer(queueEnqueuer)
	if err != nil {
		return xerror.Wrapf("failed to initialize pipeline git producer: %w", err)
	}
	proofWebhookProducer, err := workerproducer.NewProofWebhookProducer(queueEnqueuer)
	if err != nil {
		return xerror.Wrapf("failed to initialize proof webhook producer: %w", err)
	}
	emailProducer, err := workerproducer.NewEmailProducer(queueEnqueuer, encrypter)
	if err != nil {
		return xerror.Wrapf("failed to initialize email producer: %w", err)
	}
	notificationDeliveryProducer, err := workerproducer.NewNotificationDeliveryProducer(queueEnqueuer)
	if err != nil {
		return xerror.Wrapf("failed to initialize notification delivery producer: %w", err)
	}
	pipelineScheduleProducer, err := workerproducer.NewPipelineScheduleProducer(queueEnqueuer)
	if err != nil {
		return xerror.Wrapf("failed to initialize pipeline schedule producer: %w", err)
	}
	pipelineWebhookProducer, err := workerproducer.NewPipelineWebhookProducer(queueEnqueuer)
	if err != nil {
		return xerror.Wrapf("failed to initialize pipeline webhook producer: %w", err)
	}
	pipelineRetryProducer, err := workerproducer.NewPipelineRetryProducer(queueEnqueuer)
	if err != nil {
		return xerror.Wrapf("failed to initialize pipeline retry producer: %w", err)
	}
	distributionPipelineRuns, err := distribution.WrapPipelineRuns(pipelineruns.New())
	if err != nil {
		return xerror.Wrapf("initialize distribution pipeline run repository: %w", err)
	}
	pipelineRunRepo := pipelineruns.NewRepositoryWithLogging(
		distributionPipelineRuns,
		backendLogger,
	)
	pipelineRunProducer, err := workerproducer.NewPipelineRunProducer(queueEnqueuer, pipelineRunRepo)
	if err != nil {
		return xerror.Wrapf("failed to initialize pipeline run producer: %w", err)
	}
	schemaImportProducer, err := workerproducer.NewDataSourceSchemaImportProducer(queueEnqueuer)
	if err != nil {
		return xerror.Wrapf("failed to initialize data source schema import producer: %w", err)
	}

	eventsService := eventssvc.New(txm, eventsRepo, clock.New())
	proofMaintenance, err := proofevidencesvc.NewMaintenance(txm, proofobservations.New(), proofreceipts.New(), eventrecorder.New(eventsService))
	if err != nil {
		return xerror.Wrapf("initialize Proof retention maintenance: %w", err)
	}
	rolesService := rolessvc.NewServiceWithLogging(
		rolessvc.New(txm, rolesRepo, permissionsRepo, eventProducer),
		backendLogger,
	)
	apiKeysService := apikeyssvc.New(txm, apiKeysRepo, rolesService, eventProducer)
	patsService := patssvc.New(txm, patsRepo)
	providerRegistry := credentialssvc.ProviderRegistry{}
	if strings.TrimSpace(cfg.GoogleWorkspace.OAuthClientID) != "" {
		provider, err := workspaceoauth.New(workspaceoauth.Config{ClientID: cfg.GoogleWorkspace.OAuthClientID, ClientSecret: cfg.GoogleWorkspace.OAuthClientSecret, RedirectURI: cfg.GoogleWorkspace.OAuthRedirectURI})
		if err != nil {
			return xerror.Wrapf("configure Google Workspace OAuth provider: %w", err)
		}
		providerRegistry["google_workspace"] = provider
	}

	if strings.TrimSpace(cfg.BigQuery.OAuthClientID) != "" {
		provider, providerErr := bigqueryoauth.New(bigqueryoauth.Config{
			ClientID:     cfg.BigQuery.OAuthClientID,
			ClientSecret: cfg.BigQuery.OAuthClientSecret,
			RedirectURI:  cfg.BigQuery.OAuthRedirectURI,
		})
		if providerErr != nil {
			return xerror.Wrapf("configure BigQuery OAuth provider: %w", providerErr)
		}
		providerRegistry["bigquery"] = provider
	}
	if strings.TrimSpace(cfg.HubSpot.OAuthClientID) != "" {
		hubSpotProvider, providerErr := hubspotoauth.New(hubspotoauth.Config{
			ClientID:       cfg.HubSpot.OAuthClientID,
			ClientSecret:   cfg.HubSpot.OAuthClientSecret,
			RedirectURI:    cfg.HubSpot.OAuthRedirectURI,
			BaseURL:        cfg.HubSpot.OAuthBaseURL,
			AllowPlaintext: cfg.Security.AllowPlaintextConnectors,
		})
		if providerErr != nil {
			return xerror.Wrapf("configure HubSpot OAuth provider: %w", providerErr)
		}
		providerRegistry["hubspot"] = hubSpotProvider
	}
	if strings.TrimSpace(cfg.GitLab.OAuthClientID) != "" {
		gitLabProvider, providerErr := gitlaboauth.New(gitlaboauth.Config{
			ClientID:       cfg.GitLab.OAuthClientID,
			ClientSecret:   cfg.GitLab.OAuthClientSecret,
			RedirectURI:    cfg.GitLab.OAuthRedirectURI,
			BaseURL:        cfg.GitLab.OAuthBaseURL,
			AllowPlaintext: cfg.Security.AllowPlaintextConnectors,
		})
		if providerErr != nil {
			return xerror.Wrapf("configure GitLab OAuth provider: %w", providerErr)
		}
		providerRegistry["gitlab"] = gitLabProvider
	}
	if strings.TrimSpace(cfg.Slack.OAuthClientID) != "" {
		provider, err := slackoauth.New(slackoauth.Config{ClientID: cfg.Slack.OAuthClientID, ClientSecret: cfg.Slack.OAuthClientSecret, RedirectURL: cfg.Slack.OAuthRedirectURI})
		if err != nil {
			return xerror.Wrapf("configure Slack OAuth provider: %w", err)
		}
		providerRegistry["slack"] = provider
	}
	credentialCipher, err := cfg.Security.CredentialKeyring()
	if err != nil {
		return err
	}
	credentialsService, err := credentialssvc.New(
		txm,
		repocredentials.New(),
		credentialCipher,
		providerRegistry,
		clock.New(), cfg.Security.AllowPrivateDataSourceHosts)
	if err != nil {
		return xerror.Wrapf("failed to create credentials service: %w", err)
	}
	admissionService := admissionsvc.NewServiceWithLogging(
		admissionsvc.New(txm, orgsRepo),
		backendLogger,
	)
	admissionService, err = distribution.WrapAdmission(admissionService)
	if err != nil {
		return err
	}

	// Initialize data sources repository and service before org service
	// (org service depends on datasources service for backend validation)
	schemaMetadataRepo := datasources.NewSchemaMetadataRepositoryWithLogging(
		datasources.NewSchemaMetadataRepository(),
		backendLogger,
	)
	driftReportsRepo := driftimpactrepo.New()

	// Initialize data sources service with default (production) connector factory
	gitLabProofWebhookService, err := gitlabproofwebhooksvc.New(
		txm, gitlabproofwebhooks.New(), credentialCipher, cfg.FrontendURL, gitprovider.Config{BaseURL: cfg.GitLab.OAuthBaseURL, AllowPrivateHosts: cfg.Security.AllowPrivateDataSourceHosts, AllowPlaintext: cfg.Security.AllowPlaintextConnectors},
	)
	if err != nil {
		return xerror.Wrapf("failed to initialize GitLab Proof webhook service: %w", err)
	}
	gitLabWebhookManager := gitLabProofWebhookService
	githubProvider, err := githubappsvc.NewGitHubProvider(
		cfg.GitHubProof.AppID, cfg.GitHubProof.ClientID, cfg.GitHubProof.ClientSecret,
		cfg.GitHubProof.CallbackURL, cfg.GitHubProof.PrivateKeyFile,
	)
	if err != nil {
		return xerror.Wrapf("failed to initialize GitHub App provider: %w", err)
	}
	githubAppService, err := githubappsvc.New(txm, githubappsrepo.New(), githubProvider, eventProducer, cfg.GitHubProof.InstallationURL)
	if err != nil {
		return xerror.Wrapf("failed to initialize GitHub App service: %w", err)
	}
	datasourceConfig := datasourcessvc.ServiceConfig{
		HTTPOAuthRedirectURI:  cfg.HTTPOAuth.OAuthRedirectURI,
		GoogleWorkspacePicker: datasourcessvc.WorkspacePickerSettings{ClientID: cfg.GoogleWorkspace.OAuthClientID, DeveloperKey: cfg.GoogleWorkspace.PickerAPIKey, AppID: cfg.GoogleWorkspace.ProjectNumber},
		SnowflakeWIFIssuer:    cfg.Snowflake.WIFIssuer,
		CustomerConfigPolicy: datasourcessvc.CustomerConfigPolicy{
			AllowPrivateDataSourceHosts: cfg.Security.AllowPrivateDataSourceHosts,
			AllowPlaintextConnectors:    cfg.Security.AllowPlaintextConnectors,
		},
	}

	datasourceConfig.ConnectionTestExecutor = executionRequestBroker
	datasourceConfig.SchemaIntrospectionExecutor = executionRequestBroker
	datasourceConfig.SchemaMaxBytes = int(cfg.ExecutionLimits.MaxResponseBytes)
	datasourceConfig.SchemaMaxItems = cfg.ExecutionLimits.MaxResultRows
	datasourceConfig.GitBranchExecutor = executionRequestBroker
	datasourceConfig.GitBranchMaxBytes = int(cfg.ExecutionLimits.MaxResponseBytes)
	datasourceConfig.GitBranchMaxItems = cfg.ExecutionLimits.MaxResultRows

	datasourcesService, err := datasourcessvc.New(
		txm,
		datasourcesRepo,
		schemaMetadataRepo,
		driftReportsRepo,
		credentialsService,
		githubAppService,
		gitLabWebhookManager,
		admissionService,
		connectorFactory,
		eventProducer,
		schemaImportProducer,
		datasourceConfig,
	)
	if err != nil {
		return xerror.Wrapf("failed to create datasources service: %w", err)
	}
	datasourcesService = datasourcessvc.NewServiceWithLogging(datasourcesService, backendLogger)
	proofCandidatesRepo := proofrepo.New()
	proofConfigurationService, err := proofconfigsvc.New(txm, proofconfig.New(), datasourcesService, eventProducer)
	if err != nil {
		return err
	}
	proofSourceVerifier, err := proofcandidatesvc.NewExactSourceVerifier(datasourcesService)
	if err != nil {
		return xerror.Wrapf("failed to initialize proof source verifier: %w", err)
	}
	proofCandidatesService, err := proofcandidatesvc.New(
		txm, proofCandidatesRepo, proofSourceVerifier, eventProducer,
	)
	if err != nil {
		return xerror.Wrapf("failed to initialize proof candidate service: %w", err)
	}
	var proofWebhookService *proofwebhooksvc.Service

	pipelineReferencesRepo := pipelinesrepo.NewRepositoryWithLogging(pipelinesrepo.New(), backendLogger)
	pipelineStore, err := pipelinesvc.NewPipelineStore(pool,
		txm,
		pipelineReferencesRepo,
		odcscontracts.NewRepository(),
	)
	if err != nil {
		return xerror.Wrapf("failed to create pipeline store: %w", err)
	}
	pipelineStore = pipelinesvc.NewPipelineStoreWithLogging(pipelineStore, backendLogger)

	accountPolicy := distribution.AccountPolicy(emailProducer)
	usersService := userssvc.NewUsersServiceWithLogging(
		userssvc.New(
			txm,
			argon2Hasher,
			usersRepo,
			emailProducer,
			eventProducer,
			cfg.FrontendURL, accountPolicy.Users...,
		),
		backendLogger,
	)
	accountClosureService, err := accountclosuresvc.New(txm, accountclosures.Repository{}, eventProducer, enqueueClient, accountPolicy.AccountClosure...)
	if err != nil {
		return xerror.Wrapf("initialize account closure service: %w", err)
	}
	membersService := memberssvc.NewServiceWithLogging(
		memberssvc.New(
			txm,
			membersRepo,
			memberInvitationsRepo,
			orgsRepo,
			rolesService,
			usersService,
			emailProducer,
			eventProducer,
			cfg.FrontendURL,
			clock.New(),
			accountPolicy.Members...,
		),
		backendLogger,
	)
	environmentsService := environmentssvc.NewServiceWithLogging(
		environmentssvc.New(
			txm,
			environmentsRepo,
			eventProducer,
			environmentssvc.WithCredentialService(credentialsService),
		),
		backendLogger,
	)
	orgService := orgsvc.NewServiceWithLogging(
		orgsvc.New(
			txm,
			orgsRepo,
			environmentsService,
			membersService,
			rolesService,
			eventProducer,
			admissionService, accountPolicy.Organizations...,
		),
		backendLogger,
	)
	authorizationService, err := authorization.New(usersService, rolesService)
	if err != nil {
		return xerror.Wrapf("failed to initialize authorization service: %w", err)
	}
	authorizationService = authorization.NewServiceWithLogging(authorizationService, backendLogger)

	// Initialize queries repository and service
	queriesRepo := postgresqueries.NewRepositoryWithLogging(postgresqueries.New(), backendLogger)

	queriesService, err := queriessvc.NewWithExecutionLimits(
		txm,
		queriesRepo,
		datasourcesService,
		executionRequestBroker,
		credentialsService,
		clock.New(),
		queriessvc.ExecutionLimits{
			NativeFiles:                       cfg.ExecutionLimits.NativeFiles.Profile(),
			QueryTimeout:                      cfg.ExecutionLimits.QueryTimeout.TimeDuration(),
			ConnectorTimeout:                  cfg.ExecutionLimits.ConnectorTimeout.TimeDuration(),
			MaxResultRows:                     cfg.ExecutionLimits.MaxResultRows,
			MaxResponseBytes:                  cfg.ExecutionLimits.MaxResponseBytes,
			ConnectorConcurrencyPerDataSource: cfg.ExecutionLimits.ConnectorConcurrencyPerDataSource,
			ConnectorConcurrencyQueueTimeout:  cfg.ExecutionLimits.ConnectorConcurrencyQueueTimeout.TimeDuration(),
			ConnectorConcurrencyMaxQueued:     cfg.ExecutionLimits.ConnectorConcurrencyMaxQueued,
			HTTPTimeout:                       cfg.ExecutionLimits.HTTPTimeout.TimeDuration(),
			HTTPMaxRedirects:                  cfg.ExecutionLimits.HTTPMaxRedirects,
			HTTPMaxRequestBytes:               cfg.ExecutionLimits.HTTPMaxRequestBytes,
			HTTPMaxResponseBytes:              cfg.ExecutionLimits.HTTPMaxResponseBytes,
			HTTPRetryAttempts:                 cfg.ExecutionLimits.HTTPRetryAttempts,
			HTTPRetryBackoff:                  cfg.ExecutionLimits.HTTPRetryBackoff.TimeDuration(),
		},
		eventProducer,
	)
	if err != nil {
		return xerror.Wrapf("failed to create queries service: %w", err)
	}
	queriesService = queriessvc.NewServiceWithLogging(queriesService, backendLogger)

	projectOptions := make([]projectssvc.Option, 0, 1)

	projectOptions = append(projectOptions, projectssvc.WithRemotePipelineBackendExecution(
		executionRequestBroker,
		cfg.ExecutionLimits.MaxResponseBytes,
	))
	pipelineRepositoryManager, err := pipelinegit.NewRemoteRepositoryManager(
		datasourcesService,
		executionRequestBroker,
	)
	if err != nil {
		return xerror.Wrapf("failed to initialize pipeline repository manager: %w", err)
	}
	odcsContractsService := odcscontractsvc.New(txm, odcscontracts.NewRepository(), projectsRepo, compiledplans.NewRepository())

	projectsService := projectssvc.NewServiceWithLogging(
		projectssvc.New(
			txm,
			projectsRepo,
			orgsRepo,
			environmentsRepo,
			datasourcesService,
			pipelineStore,
			pipelineGitProducer,
			pipelineRepositoryManager,
			eventProducer,
			projectOptions...,
		),
		backendLogger,
	)
	managedRepositoryBootstrapper, err := pipelinegit.NewManagedRepositoryBootstrapper(credentialsService, gitprovider.Config{BaseURL: cfg.GitLab.OAuthBaseURL, AllowPrivateHosts: cfg.Security.AllowPrivateDataSourceHosts, AllowPlaintext: cfg.Security.AllowPlaintextConnectors})
	if err != nil {
		return xerror.Wrapf("initialize managed repository bootstrapper: %w", err)
	}
	repositorySetupService, err := projectssvc.NewRepositorySetupService(txm, repositorysetups.New(), projectsService, datasourcesService, managedRepositoryBootstrapper, githubAppService, credentialsService, eventProducer, distribution)
	if err != nil {
		return xerror.Wrapf("initialize project repository setup: %w", err)
	}

	dataSourceRefsService := datasourcerefssvc.New(
		txm,
		dataSourceRefsRepo,
		datasourcesService,
		eventProducer,
	)

	compiledPlansService, err := contractplansvc.New(pipelineReferencesRepo, txm, compiledplans.NewRepository(), queriesRepo, odcscontracts.NewRepository(), pipelineStore, dataSourceRefsService)
	if err != nil {
		return xerror.Wrapf("initialize compiled plan service: %w", err)
	}

	pipelineGitSourceResolver, err := pipelinegit.NewRemoteSourceResolver(
		projectsService,
		datasourcesService,
		executionRequestBroker,
		cfg.ExecutionLimits.MaxResponseBytes,
	)
	if err != nil {
		return xerror.Wrapf("failed to initialize pipeline git source resolver: %w", err)
	}
	notificationsService := notificationsvc.New(
		txm,
		reponotifications.New(),
		clock.New(),
		notificationDeliveryProducer,
		emailProducer,
		notificationLifecycleEventProducer(eventProducer),
		notificationsvc.ConnectionProviders{model.AlertDestinationTypeSlack: slacknotifications.New(credentialsService, slack.NewWebhookClient(nil))},
		cfg.FrontendURL,
	)
	notificationCandidateProducer, err := workerproducer.NewNotificationCandidateProducer(queueEnqueuer)
	if err != nil {
		return err
	}
	runStore := pipelineruntime.NewRunStore(txm, pool, clock.New(), runrecencysvc.NewSuccessProducer(runrecency.New(), notificationCandidateProducer), pipelineRunRepo)
	sinkEffectLedger, err := pipelinesvc.NewSinkEffectLedger(
		txm,
		pipelinesinkeffects.NewRepositoryWithLogging(pipelinesinkeffects.New(), backendLogger),
	)
	if err != nil {
		return xerror.Wrapf("failed to initialize pipeline sink effect ledger: %w", err)
	}
	pipelineOptions := []pipelinesvc.ServiceOption{pipelinesvc.WithTransformLimits(transformsql.Limits{
		MaxWork:              cfg.ExecutionLimits.TransformMaxWork,
		MaxIntermediateRows:  cfg.ExecutionLimits.TransformMaxIntermediateRows,
		MaxIntermediateBytes: cfg.ExecutionLimits.TransformMaxIntermediateBytes,
		MaxOutputRows:        cfg.ExecutionLimits.TransformMaxOutputRows,
		MaxOutputBytes:       cfg.ExecutionLimits.TransformMaxOutputBytes,
		MaxDepth:             cfg.ExecutionLimits.TransformMaxDepth,
		Timeout:              cfg.ExecutionLimits.TransformTimeout.TimeDuration(),
	})}

	pipelineOptions = append(pipelineOptions, pipelinesvc.WithTransformExecutor(executionRequestBroker))

	pipelinesService, err := pipelinesvc.New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		pipelineStore,
		pool,
		txm,
		pipelineRunRepo, incrementalcheckpoints.New(),
		runStore,
		driftReportsRepo,
		queriesService,
		authorizationService,
		admissionService,
		datasourcesService,
		projectsService,
		pipelineGitSourceResolver,
		clock.New(),
		pipelineRunProducer,
		pipelineGitProducer,
		eventProducer,
		pipelineRuntimeResolverBundle{
			Service:                   dataSourceRefsService,
			EnvironmentSecretResolver: environmentsService,
		},
		sinkEffectLedger,
		proofreceipts.New(),
		notificationCandidateProducer, pipelineOptions...,
	)
	if err != nil {
		return xerror.Wrapf("failed to initialize pipelines service: %w", err)
	}
	pipelinesService = pipelinesvc.NewServiceWithLogging(pipelinesService, backendLogger)
	extension, err := distribution.NewExtension(runtimeCtx, Services{
		Resources: resources,
		Queue:     queueEnqueuer, Cipher: encrypter, EncryptionKey: encryptionKeyBytes,
		Authorization: authorizationService, Organizations: orgService,
		Projects: projectsService, Members: membersService, Roles: rolesService,
		DataSources: datasourcesService, Queries: queriesService, DataSourceRefs: dataSourceRefsService,
		Pipelines: pipelinesService, CompiledPlans: compiledPlansService,
	})
	if err != nil {
		return xerror.Wrapf("initialize backend distribution services: %w", err)
	}
	if extension == nil {
		return xerror.Unexpected().Detail("backend distribution extension is required")
	}
	if mode == ModeServiceCommand {
		return extension.RunCommand(runtimeCtx, func() error {
			app, drainLifecycle := newBackendOperationalApp(cfg.Server, http.ProbeRoutesConfig{
				Database: pool, Migrations: migrationPreflight,
			}, metricsMeter)
			return runBackendHTTP(app, drainLifecycle, cfg.Server, backendLogger)
		})
	}
	pipelineCreationsService, err := pipelinesvc.NewCreationService(txm, pipelinecreations.New(), projectsService, pipelineStore, authorizationService, pipelineGitProducer, eventProducer)
	if err != nil {
		return xerror.Unexpected().Detail("initialize pipeline creation service").With(err)
	}
	proposalLifecycle, err := pipelinesvc.NewProposalLifecycle(pipelineStore, pipelinesService, pipelineCreationsService)
	if err != nil {
		return xerror.Unexpected().Detail("initialize proposal lifecycle").With(err)
	}
	var proofReceiptService proofreceiptsvc.Service
	var proofObservationService *proofreceiptsvc.LiveCompiler
	proofCompiler, err := proofreceiptsvc.New(
		txm,
		proofreceipts.New(),
		pipelinesService,
		dataSourceRefsService,
		schemaMetadataRepo,
		pipelineReferencesRepo,
		environmentsService,
		odcsContractsService,
		clock.New(),
	)
	if err != nil {
		return xerror.Wrapf("failed to initialize proof receipt service: %w", err)
	}
	proofReceiptService = proofCompiler
	proofPublicationProducer, err := workerproducer.NewProofPublicationProducer(queueEnqueuer)
	if err != nil {
		return err
	}
	if cfg.ProofObservations.Enabled {
		key, err := hex.DecodeString(cfg.ProofObservations.EvidenceKeyHex)
		if err != nil {
			return xerror.ValidationFailed().Detail("invalid Proof evidence key")
		}
		defer clear(key)
		evidenceCipher, err := crypto.NewAES256(key)
		if err != nil {
			return xerror.ValidationFailed().Detail("invalid Proof evidence key")
		}
		audit := eventrecorder.New(eventsService)
		ledger, err := proofevidencesvc.NewLedger(txm, proofobservations.New(), evidenceCipher, audit)
		if err != nil {
			return err
		}
		coordinator, err := proofevidencesvc.NewCoordinator(ledger, credentialsService, executionRequestBroker)
		if err != nil {
			return err
		}
		proofObservationService, err = proofreceiptsvc.NewLiveCompiler(proofCompiler, ledger, datasources.New(), postgresqueries.New(), coordinator, audit, proofPublicationProducer, cfg.Proof.GitChecksEnabled)
		if err != nil {
			return err
		}
		proofReceiptService = proofObservationService
	}
	proofReceiptService, err = proofreceiptsvc.NewOperationalCompiler(proofReceiptService, proofCompiler, proofreceipts.New(), eventrecorder.New(eventsService))
	if err != nil {
		return err
	}

	proofStatusService, err := proofstatussvc.New(txm, proofreceipts.New(), datasourcesService, proofReceiptService, proofPublicationProducer, cfg.Proof.GitChecksEnabled, cfg.FrontendURL)
	if err != nil {
		return xerror.Wrapf("failed to initialize proof status service: %w", err)
	}
	proofWebhookService, err = proofwebhooksvc.New(
		txm, proofCandidatesRepo, proofrepo.NewDeliveryClaimer(), proofCandidatesService,
		proofReceiptService, proofWebhookProducer, githubAppService, proofStatusService, cfg.Proof.GitChecksEnabled,
	)
	if err != nil {
		return xerror.Wrapf("failed to initialize proof webhook service: %w", err)
	}

	pipelineTriggersService := pipelinetriggersvc.New(
		cfg.Execution.CompletionTriggersEnabled,
		txm,
		pipelineTriggersRepo,
		pipelinesService,
		eventProducer,
		credentialsService,
	)
	completionDispatcher, err := pipelinetriggersvc.NewCompletionDispatcher(cfg.Execution.CompletionTriggersEnabled,
		txm, pipelineTriggersRepo, pipelineTriggersService, pipelinesService, pipelineRunProducer)
	if err != nil {
		return xerror.Wrapf("initialize completion dispatcher: %w", err)
	}
	pipelineWebhookService, err := pipelinewebhooksvc.New(
		txm,
		pipelinewebhooks.New(),
		credentialsService,
		pipelineWebhookProducer,
		pipelinesService,
		credentialCipher,
		clock.New(),
		notificationCandidateProducer,
	)
	if err != nil {
		return xerror.Wrapf("failed to initialize pipeline webhook service: %w", err)
	}
	pipelineScheduleService := pipelineschedulesvc.New(
		txm,
		pipelineTriggersRepo,
		pipelinesService,
		pipelineScheduleProducer,
		pipelineRetryProducer,
		pipelineschedulesvc.WithNotificationProducer(notificationCandidateProducer),
	)
	retentionService := retentionsvc.New(
		txm,
		retentionrepo.New(),
		wishlistRegistrationsRepo,
		clock.New(),
	)

	return runBackendRuntimeLifecycle(mode, backendRuntimeLifecycle{
		startRiverWorkers: func() (func() error, error) {
			// Worker-only dependencies are constructed only when this process consumes River jobs.
			bundle, err := i18n.NewBundle(cfg.DefaultLanguage)
			if err != nil {
				return nil, xerror.Wrapf("failed to initialize i18n bundle: %w", err)
			}
			emailService, err := email.NewService(cfg.SMTP, bundle)
			if err != nil {
				return nil, xerror.Wrapf("failed to initialize email service: %w", err)
			}
			emailService = email.NewServiceWithLogging(emailService, backendLogger)

			pipelineGitFileExecutor, err := pipelinegit.NewRemoteFileExecutor(
				datasourcesService,
				executionRequestBroker,
			)
			if err != nil {
				return nil, xerror.Wrapf("failed to initialize pipeline git file executor: %w", err)
			}

			pipelineGitProposalExecutor, err := pipelinegit.NewRemoteProposalExecutor(
				datasourcesService,
				executionRequestBroker,
			)
			if err != nil {
				return nil, xerror.Wrapf("failed to initialize pipeline git proposal executor: %w", err)
			}
			pipelineGitWorkerExecutor, err := workerconsumer.NewPipelineGitExecutor(
				pipelineGitFileExecutor,
				pipelineGitProposalExecutor,
			)
			if err != nil {
				return nil, xerror.Wrapf("failed to initialize pipeline git worker executor: %w", err)
			}

			workerService, err := backgroundworker.New(
				pipelineGitWorkerExecutor,
				proposalLifecycle,
				proposalLifecycle,
				emailService,
				eventsService,
				retentionService,
				recencyScheduleObserver{Service: pipelineScheduleService, logger: backendLogger, recency: runrecencysvc.New(txm, runrecency.New(), notificationsService)},
				completionDispatcher,
				datasourcesService,
				notificationsService,
				encrypter,
				backendLogger,
				backendWorkerQueueConcurrency(cfg.Worker),
				pipelineWebhookService,
			)
			if err != nil {
				return nil, xerror.Wrapf("failed to initialize background worker service: %w", err)
			}
			workerQueues := workerService.Queues()
			if executionRegistrar != nil {
				for queueName, queueConfig := range executionRegistrar.Queues() {
					workerQueues[queueName] = queueConfig
				}
				preflightCtx, cancelPreflight := context.WithTimeout(runtimeCtx, 30*time.Second)
				preflightErr := executionRequestBroker.PreflightProcessor(preflightCtx)
				cancelPreflight()
				if preflightErr != nil {
					return nil, xerror.Wrapf("preflight River execution processor: %w", preflightErr)
				}
				if err := executionRequestBroker.StartProcessorObservability(
					executionMaintenanceCtx,
					cfg.Worker.ExecutionMaxWorkers,
				); err != nil {
					return nil, xerror.Wrapf("start River execution processor observability: %w", err)
				}
			}
			workerRegistrars := []queue.WorkerRegistrar{
				workerService,
				&backgroundworker.AccountDeletionWorker{Service: accountClosureService},
			}

			proofMaintenanceRegistrar, err := backgroundworker.NewProofObservationMaintenanceRegistrar(proofMaintenance, backendLogger)
			if err != nil {
				return nil, xerror.Wrapf("initialize Proof retention worker: %w", err)
			}
			workerRegistrars = append(workerRegistrars, proofMaintenanceRegistrar)

			proofWebhookRegistrar, err := backgroundworker.NewProofWebhookRegistrar(proofWebhookService, backendLogger)
			if err != nil {
				return nil, xerror.Wrapf("failed to initialize proof webhook worker: %w", err)
			}
			workerRegistrars = append(workerRegistrars, proofWebhookRegistrar)
			proofPublicationRegistrar, err := backgroundworker.NewProofPublicationRegistrar(proofStatusService, backendLogger)
			if err != nil {
				return nil, xerror.Wrapf("initialize Proof publication worker: %w", err)
			}
			workerRegistrars = append(workerRegistrars, proofPublicationRegistrar)
			if cfg.Execution.CompletionTriggersEnabled {
				workerRegistrars = append(workerRegistrars, backgroundworker.NewCompletionDispatchRegistrar(completionDispatcher))
			}

			if executionRegistrar != nil {
				workerRegistrars = append(workerRegistrars, executionRegistrar)
			}
			queueClient, err := newBackendQueueClient(
				pool,
				backendLogger,
				workerRegistrars,
				workerQueues,
			)
			if err != nil {
				return nil, xerror.Wrapf("failed to initialize River queue client: %w", err)
			}
			metricsMeter.RegisterProofMaintenanceHealth(proofMaintenance.Health)
			metricsMeter.RegisterProofActivity(proofMaintenance.Activity)
			metricsMeter.RegisterRiverQueueHealth(metrics.RiverQueueHealthConfig{
				AllowedQueues: riverQueueNames(workerQueues),
				Provider:      riverQueueHealthProvider(pool, workerQueues),
			})

			if err := queueClient.Start(runtimeCtx); err != nil {
				return nil, xerror.Wrapf("failed to start River queue client: %w", err)
			}
			return func() error {
				if stopErr := queueClient.Stop(context.WithoutCancel(runtimeCtx)); stopErr != nil {
					return xerror.Wrapf("stop River queue client: %w", stopErr)
				}
				return nil
			}, nil
		},
		runHTTP: func() error {
			// HTTP-only dependencies stay out of worker-only composition.
			sessionStore := sessionstore.New(sessionstore.Config{DB: pool, Table: cfg.Session.TableName})
			defer func() {
				if err := sessionStore.Close(); err != nil {
					backendLogger.Error("failed to close session store", slog.Any("error", err))
				}
			}()
			store := session.NewStore(backendSessionConfig(sessionStore, cfg.Session))

			oidcService, err := oidc.New(
				"https://accounts.google.com",
				cfg.Auth.GoogleClientID,
				cfg.Auth.GoogleClientSecret,
				cfg.Auth.GoogleRedirectURI,
			)
			if err != nil {
				return xerror.Wrapf("failed to initialize Google OIDC service: %w", err)
			}
			if strings.TrimSpace(cfg.Auth.GoogleClientID) == "" && strings.TrimSpace(cfg.Auth.GoogleClientSecret) == "" {
				backendLogger.Info("Google sign-in is not configured")
			}
			oidcService = oidc.NewServiceWithLogging(oidcService, backendLogger)

			coreServer := http.NewServer(
				usersService,
				accountClosureService,
				oidcService,
				store,
				authorizationService,
				orgService,
				admissionService,
				projectsService,
				repositorySetupService,
				pipelineCreationsService,
				odcsContractsService,
				compiledPlansService,
				odcscontractsvc.NewArtifactImporter(txm, odcscontracts.NewRepository(), projectsRepo, pipelineGitSourceResolver),
				environmentsService,
				membersService,
				rolesService,
				apiKeysService,
				patsService,
				credentialsService,
				datasourcesService,
				dataSourceRefsService,
				queriesService,
				eventsService,
				githubAppService,
				notificationsService,
				retentionService,
				pipelinesService,
				proofReceiptService,
				proofConfigurationService,
				proofObservationService,
				pipelineTriggersService,
				pipelineWebhookService,
				runrecencysvc.New(txm, runrecency.New(), notificationsService),
				cfg.FrontendURL,
				cfg.Auth.GoogleRedirectURI,
				cfg.Auth.SelfServiceRegistrationEnabled,
				accountPolicy.RegistrationEnabled,
				extension,
			)

			swagger, err := extension.Document()
			if err != nil {
				return xerror.Unexpected().With(err)
			}
			publicOpenAPIDocument, err := http.PublicOpenAPIDocument(swagger)
			if err != nil {
				return xerror.Unexpected().With(err)
			}

			fiberRequestValidator, err := http.NewOpenAPIRequestValidator(swagger)
			if err != nil {
				return xerror.Wrapf("initialize OpenAPI request validation: %w", err)
			}

			// Root app carries operational routes and middleware shared with worker-only mode.
			app, drainLifecycle := newBackendOperationalApp(cfg.Server, http.ProbeRoutesConfig{
				Database:   pool,
				Migrations: migrationPreflight,
			}, metricsMeter)
			http.RegisterOpenAPIRoute(app, publicOpenAPIDocument)
			secret := strings.TrimSpace(cfg.GitHubProof.WebhookSecret)
			if secret != "" {
				if err := http.RegisterGitHubProofWebhookRoute(app, proofWebhookService, githubAppService, []byte(secret), backendLogger); err != nil {
					return xerror.Wrapf("register GitHub Proof webhook route: %w", err)
				}
			}
			if err := http.RegisterGitLabProofWebhookRoute(app, gitLabProofWebhookService, proofWebhookService, backendLogger); err != nil {
				return xerror.Wrapf("register GitLab Proof webhook route: %w", err)
			}

			apiApp := fiber.New(fiber.Config{
				ErrorHandler:       apierror.Middleware(),
				ProxyHeader:        cfg.Server.ProxyHeader,
				TrustProxy:         len(cfg.Server.TrustedProxies) > 0,
				TrustProxyConfig:   fiber.TrustProxyConfig{Proxies: cfg.Server.TrustedProxies},
				EnableIPValidation: cfg.Server.ProxyHeader != "",
			})
			apiApp.Use(
				tracing.HTTPMiddleware("backend"),
				metricsMeter.HTTPMiddleware(),
				logging.HTTPMiddleware(backendLogger, cfg.Logging.LogHTTPRequests),
				cors.New(backendCORSConfig(cfg.CORS)),
				http.NewBackendSecurityMiddlewareForApp(
					store,
					backendLogger,
					&postgresAbuseLimiter{db: pool, repository: abusecontrols.New()},
					cfg.AbuseControls.AuthAttempts,
					cfg.AbuseControls.AuthWindow.TimeDuration(),
					cfg.AbuseControls.ExpensiveClientAttempts,
					cfg.AbuseControls.ExpensiveTenantAttempts,
					cfg.AbuseControls.CredentialAttempts,
					cfg.AbuseControls.ExpensiveWindow.TimeDuration(),
				),
				http.NewBearerAuthMiddleware(apiKeysService, patsService),
				http.NewSessionAuthenticationMiddleware(usersService, store),
				fiberRequestValidator,
			)
			if err := extension.RegisterRoutes(apiApp, coreServer); err != nil {
				return xerror.Wrapf("register distribution routes: %w", err)
			}
			oapi.RegisterHandlersWithOptions(apiApp, coreServer, oapi.FiberServerOptions{})

			app.Use("/api", apiApp)
			return runBackendHTTP(app, drainLifecycle, cfg.Server, backendLogger)
		},
		runWorkerHTTP: func() error {
			// Worker-only mode exposes operational routes without constructing or mounting the public API.
			app, drainLifecycle := newBackendOperationalApp(cfg.Server, http.ProbeRoutesConfig{
				Database:   pool,
				Migrations: migrationPreflight,
			}, metricsMeter)
			return runBackendHTTP(app, drainLifecycle, cfg.Server, backendLogger)
		},
	})
}

type postgresAbuseLimiter struct {
	db         postgres.DBTX
	repository abusecontrols.Repository
}

func (l *postgresAbuseLimiter) Attempt(
	ctx context.Context,
	budget httpsecurity.AbuseLimit,
) (httpsecurity.AbuseDecision, error) {
	return l.repository.Attempt(ctx, l.db, budget)
}
