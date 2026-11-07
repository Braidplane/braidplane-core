package queue

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/executionrequests"
)

// Config configures the internal River-backed queue client.
type Config struct {
	Queues       map[string]river.QueueConfig
	PeriodicJobs []*river.PeriodicJob
	Workers      *river.Workers
	Logger       *slog.Logger
	RetryPolicy  river.ClientRetryPolicy
}

// WorkerRegistrar registers one or more River workers on a shared bundle.
type WorkerRegistrar interface {
	Register(*river.Workers) error
}

type workerQueueProvider interface {
	Queues() map[string]river.QueueConfig
}

type periodicJobProvider interface {
	PeriodicJobs() ([]*river.PeriodicJob, error)
}

// WorkerHostConfig configures a worker-capable River client.
type WorkerHostConfig struct {
	Queues       map[string]river.QueueConfig
	PeriodicJobs []*river.PeriodicJob
	Registrars   []WorkerRegistrar
	Logger       *slog.Logger
	RetryPolicy  river.ClientRetryPolicy
}

// Client wraps River so callers can enqueue jobs on the shared Postgres
// runtime without depending on driver-specific setup details.
type Client struct {
	riverClient *river.Client[pgx.Tx]
	mu          sync.RWMutex
	started     bool
	willWork    bool
	queues      map[string]river.QueueConfig
	heartbeatAt time.Time
}

// WorkerLivenessMetric describes queue-worker liveness for metrics collection.
type WorkerLivenessMetric struct {
	Queue            string
	ActiveWorkers    int64
	MaxWorkers       int64
	LastHeartbeatAge time.Duration
}

// New creates a queue client backed by the shared pgx pool.
func New(pool *pgxpool.Pool, config *Config) (*Client, error) {
	if pool == nil {
		return nil, xerror.Unexpected().Detail("queue database pool is required")
	}

	riverConfig := &river.Config{}
	if config != nil {
		riverConfig.Queues = config.Queues
		riverConfig.PeriodicJobs = config.PeriodicJobs
		riverConfig.Workers = config.Workers
		riverConfig.Logger = config.Logger
		riverConfig.RetryPolicy = config.RetryPolicy
	}
	riverConfig.Middleware = []rivertype.Middleware{newTelemetryMiddleware(riverConfig.Logger)}

	riverClient, err := river.NewClient(riverpgxv5.New(pool), riverConfig)
	if err != nil {
		return nil, xerror.Wrapf("create River client: %w", err)
	}

	return &Client{
		riverClient: riverClient,
		willWork:    len(riverConfig.Queues) > 0 || len(riverConfig.PeriodicJobs) > 0,
		queues:      cloneQueueConfigs(riverConfig.Queues),
	}, nil
}

// NewEnqueueOnly creates a queue client that can enqueue River jobs inside SQL
// transactions, but does not start any River workers to execute jobs.
func NewEnqueueOnly(pool *pgxpool.Pool) (*Client, error) {
	return New(pool, nil)
}

// NewWorkerHost creates a River client that can work jobs when registrars are
// provided, and otherwise falls back to enqueue-only behavior without running
// any River workers.
func NewWorkerHost(pool *pgxpool.Pool, config *WorkerHostConfig) (*Client, error) {
	if config == nil {
		return NewEnqueueOnly(pool)
	}

	workers, hasWorkers, err := newWorkerBundle(config.Registrars)
	if err != nil {
		return nil, err
	}

	clientConfig := &Config{
		Logger:      config.Logger,
		RetryPolicy: config.RetryPolicy,
	}
	clientConfig.PeriodicJobs, err = workerPeriodicJobs(config.PeriodicJobs, config.Registrars)
	if err != nil {
		return nil, err
	}
	if hasWorkers {
		clientConfig.Queues = workerQueues(config.Queues, config.Registrars)
		clientConfig.Workers = workers
	}

	return New(pool, clientConfig)
}

func newWorkerBundle(registrars []WorkerRegistrar) (*river.Workers, bool, error) {
	if len(registrars) == 0 {
		return river.NewWorkers(), false, nil
	}

	workers := river.NewWorkers()
	for _, registrar := range registrars {
		if err := registrar.Register(workers); err != nil {
			return nil, false, xerror.Wrapf("register River worker: %w", err)
		}
	}

	return workers, true, nil
}

func workerQueues(
	configured map[string]river.QueueConfig,
	registrars []WorkerRegistrar,
) map[string]river.QueueConfig {
	if len(configured) > 0 {
		return configured
	}

	registrarQueues := make(map[string]river.QueueConfig)
	for _, registrar := range registrars {
		provider, ok := registrar.(workerQueueProvider)
		if !ok {
			continue
		}
		for queueName, queueConfig := range provider.Queues() {
			registrarQueues[queueName] = queueConfig
		}
	}
	if len(registrarQueues) > 0 {
		return registrarQueues
	}

	return map[string]river.QueueConfig{
		river.QueueDefault: {MaxWorkers: 1},
	}
}

func workerPeriodicJobs(
	configured []*river.PeriodicJob,
	registrars []WorkerRegistrar,
) ([]*river.PeriodicJob, error) {
	if len(configured) > 0 {
		return configured, nil
	}

	periodicJobs := make([]*river.PeriodicJob, 0)
	for _, registrar := range registrars {
		provider, ok := registrar.(periodicJobProvider)
		if !ok {
			continue
		}

		jobs, err := provider.PeriodicJobs()
		if err != nil {
			return nil, xerror.Wrapf("register River periodic jobs: %w", err)
		}
		periodicJobs = append(periodicJobs, jobs...)
	}

	return periodicJobs, nil
}

// Start enables worker execution when the client is configured with queues.
func (c *Client) Start(ctx context.Context) error {
	c.mu.RLock()
	started := c.started
	c.mu.RUnlock()

	if !c.willWork || started {
		return nil
	}

	if err := c.riverClient.Start(ctx); err != nil {
		return xerror.Wrapf("start River client: %w", err)
	}

	c.mu.Lock()
	c.started = true
	c.heartbeatAt = time.Now()
	c.mu.Unlock()

	return nil
}

// Stop gracefully shuts down worker execution when it was started.
func (c *Client) Stop(ctx context.Context) error {
	c.mu.RLock()
	started := c.started
	c.mu.RUnlock()

	if !started {
		return nil
	}

	if err := c.riverClient.Stop(ctx); err != nil {
		return xerror.Wrapf("stop River client: %w", err)
	}

	c.mu.Lock()
	c.started = false
	c.mu.Unlock()

	return nil
}

// WorkerLivenessSnapshot reports configured worker queue liveness without exposing job payloads.
func (c *Client) WorkerLivenessSnapshot(now time.Time) []WorkerLivenessMetric {
	if c == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}

	c.mu.RLock()
	started := c.started
	heartbeatAt := c.heartbeatAt
	queues := cloneQueueConfigs(c.queues)
	c.mu.RUnlock()

	queueNames := make([]string, 0, len(queues))
	for queueName := range queues {
		queueNames = append(queueNames, queueName)
	}
	sort.Strings(queueNames)

	metrics := make([]WorkerLivenessMetric, 0, len(queueNames))
	for _, queueName := range queueNames {
		maxWorkers := int64(queues[queueName].MaxWorkers)
		activeWorkers := int64(0)
		if started && maxWorkers > 0 {
			activeWorkers = maxWorkers
		}
		metrics = append(metrics, WorkerLivenessMetric{
			Queue:            queueName,
			ActiveWorkers:    activeWorkers,
			MaxWorkers:       maxWorkers,
			LastHeartbeatAge: durationSince(now, heartbeatAt),
		})
	}

	return metrics
}

func (c *Client) enqueueTx(
	ctx context.Context,
	tx postgres.DBTX,
	req EnqueueRequest,
) (int64, bool, error) {
	if tx == nil {
		return 0, false, xerror.Unexpected().Detail("queue transaction is required")
	}

	pgxTx, err := postgres.AsPgxTx(tx)
	if err != nil {
		return 0, false, xerror.Wrapf("unwrap queue transaction: %w", err)
	}
	// Serialize account-bound inserts with removal's cancellation scan.
	if req.InitiatingAccount != "" {
		if err := executionrequests.New().LockInitiatingAccount(ctx, tx, req.InitiatingAccount); err != nil {
			return 0, false, err
		}
	}

	result, err := c.riverClient.InsertTx(ctx, pgxTx, req.Args, toRiverInsertOpts(req.Args, req.Queue))
	if err != nil {
		return 0, false, xerror.Wrapf("enqueue River job: %w", err)
	}

	return result.Job.ID, result.UniqueSkippedAsDuplicate, nil
}

// CancelJob uses River's transactional state machine and running-job notification.
// A running job receives cancellation intent, not a claim that it has stopped.
func (c *Client) CancelJob(ctx context.Context, tx postgres.DBTX, id int64) error {
	if c == nil || c.riverClient == nil {
		return xerror.Unexpected().Detail("queue client is required")
	}
	pgxTx, err := postgres.AsPgxTx(tx)
	if err != nil {
		return err
	}
	if _, err := c.riverClient.JobCancelTx(ctx, pgxTx, id); err != nil {
		return xerror.Unexpected().With(err).Detail("cancel account queue job")
	}
	return nil
}

func toRiverInsertOpts(args JobArgs, queue string) *river.InsertOpts {
	opts := args.InsertOpts()
	if queue != "" {
		opts.Queue = queue
	}
	return &opts
}

// cloneQueueConfigs copies River queue configuration so liveness reads do not share mutable maps.
func cloneQueueConfigs(queues map[string]river.QueueConfig) map[string]river.QueueConfig {
	if len(queues) == 0 {
		return nil
	}
	ret := make(map[string]river.QueueConfig, len(queues))
	for name, config := range queues {
		ret[name] = config
	}
	return ret
}

// durationSince reports a non-negative age for queue liveness heartbeat metrics.
func durationSince(now, then time.Time) time.Duration {
	if then.IsZero() || now.Before(then) {
		return 0
	}
	return now.Sub(then)
}
