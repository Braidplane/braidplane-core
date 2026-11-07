package queriessvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repoq "github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

const (
	executionSlugBase                  = slug.Slug("exec")
	defaultConcurrencyQueueTimeout     = 5 * time.Second
	defaultConcurrencyMaxQueuedPerData = 16
)

// ExecuteOptions configures saved-query execution behavior.
type ExecuteOptions struct {
	RecordExecution bool
}

// ExecuteOption mutates saved-query execution behavior.
type ExecuteOption func(*ExecuteOptions)

// WithoutExecutionRecording skips persistence of the query execution record.
func WithoutExecutionRecording() ExecuteOption {
	return func(opts *ExecuteOptions) {
		opts.RecordExecution = false
	}
}

func resolveExecuteOptions(opts []ExecuteOption) ExecuteOptions {
	resolved := ExecuteOptions{RecordExecution: true}

	for _, opt := range opts {
		if opt == nil {
			continue
		}

		opt(&resolved)
	}

	return resolved
}

// ExecutionLimits contains runtime guardrails for query and connector execution.
type ExecutionLimits struct {
	QueryTimeout                      time.Duration
	ConnectorTimeout                  time.Duration
	MaxResultRows                     int
	MaxResponseBytes                  int64
	ConnectorConcurrencyPerDataSource int
	ConnectorConcurrencyQueueTimeout  time.Duration
	ConnectorConcurrencyMaxQueued     int
	HTTPTimeout                       time.Duration
	HTTPMaxRedirects                  int
	HTTPMaxRequestBytes               int64
	HTTPMaxResponseBytes              int64
	HTTPRetryAttempts                 int
	HTTPRetryBackoff                  time.Duration
}

func (l ExecutionLimits) connectorLimits() types.ExecutionLimits {
	return types.ExecutionLimits{
		ConnectorTimeout:     l.ConnectorTimeout,
		MaxResultRows:        l.MaxResultRows,
		MaxResponseBytes:     l.MaxResponseBytes,
		HTTPTimeout:          l.HTTPTimeout,
		HTTPMaxRedirects:     l.HTTPMaxRedirects,
		HTTPMaxRequestBytes:  l.HTTPMaxRequestBytes,
		HTTPMaxResponseBytes: l.HTTPMaxResponseBytes,
		HTTPRetryAttempts:    l.HTTPRetryAttempts,
		HTTPRetryBackoff:     l.HTTPRetryBackoff,
	}
}

// Service defines the interface for query business logic.
type Service interface {
	ExecuteSnapshot(context.Context, slug.Slug, slug.Slug, contractplan.OperationContent, map[string]any, *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error)

	Create(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		actor model.EventActor,
		params *CreateParams,
	) (*Query, error)
	FindBySlug(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug) (*Query, error)
	GetExpectedInputContract(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
	) (*payload.Contract, error)
	ListExecutions(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[*QueryExecution], error)
	GetExecution(
		ctx context.Context,
		orgSlug, dsSlug, querySlug, executionSlug slug.Slug,
	) (*QueryExecution, error)
	GetExecutionSummary(context.Context, slug.Slug) (*QueryExecutionIdentity, error)
	ListByDataSource(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		req model.ListRequest,
		includeDeleted bool,
	) (model.ListResponse[*Query], error)
	Update(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		actor model.EventActor,
		params *UpdateParams,
	) (*Query, error)
	Delete(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor) error
	Restore(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor) error
	Purge(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor) error
	Execute(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		actor model.EventActor,
		paramValues map[string]any,
		opts ...ExecuteOption,
	) (*result.ExecutionResult, error)
	ExecuteInline(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		operationInline map[string]any,
		paramValues map[string]any,
	) (*result.ExecutionResult, error)
	ExecuteIncremental(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
		checkpoint *pipelinerun.IncrementalCheckpointInput,
	) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error)
	ExecuteInlineIncremental(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		operationInline map[string]any,
		paramValues map[string]any,
		checkpoint *pipelinerun.IncrementalCheckpointInput,
	) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error)
}

// service provides business logic for managing and executing queries.
type service struct {
	txm               postgres.TransactionManager
	repo              repoq.Repository
	datasourcesSvc    datasourcessvc.Service
	connectorExecutor ConnectorExecutor
	credentials       credentialssvc.Service
	clock             clock.Clock
	limits            ExecutionLimits
	eventProducer     workerproducer.EventProducer
}

// ConnectorExecutionRequest is the complete connector payload executed by a
// worker. Remote implementations must encrypt it before persistence.
type ConnectorExecutionRequest struct {
	SinkExecution           bool                        `json:"sink_execution,omitempty"`
	PinnedConfigDigest      string                      `json:"pinned_config_digest,omitempty"`
	ResourceScope           *resourcescope.Scope        `json:"resource_scope,omitempty"`
	OrganizationSlug        slug.Slug                   `json:"organization_slug"`
	DataSourceSlug          slug.Slug                   `json:"data_source_slug"`
	DataSourceType          model.DataSourceType        `json:"data_source_type"`
	ConfigVersion           string                      `json:"config_version"`
	CredentialReference     string                      `json:"credential_reference"`
	CredentialVersion       string                      `json:"credential_version"`
	ConcurrencyLimit        int                         `json:"concurrency_limit"`
	ConcurrencyQueueLimit   int                         `json:"concurrency_queue_limit"`
	ConcurrencyQueueTimeout time.Duration               `json:"concurrency_queue_timeout"`
	ConfigJSON              []byte                      `json:"config_json"`
	CredentialsJSON         []byte                      `json:"credentials_json"`
	StatementJSON           json.RawMessage             `json:"statement_json"`
	Parameters              map[string]types.TypedParam `json:"parameters"`
	Limits                  types.ExecutionLimits       `json:"limits"`
}

// ConnectorExecutor crosses the connector-capable execution boundary.
type ConnectorExecutor interface {
	ExecuteConnector(context.Context, ConnectorExecutionRequest) (*result.ExecutionResult, error)
}

type localConnectorExecutor struct {
	manager   *connector.Manager
	admission *localConnectorAdmission
}

// NewLocalConnectorExecutor creates the direct-mode executor around a
// process-owned connector manager. The caller owns and must close the manager.
func NewLocalConnectorExecutor(manager *connector.Manager) (ConnectorExecutor, error) {
	if manager == nil {
		return nil, xerror.Unexpected().Detail("connector runtime pool is required")
	}
	return localConnectorExecutor{
		manager:   manager,
		admission: newLocalConnectorAdmission(),
	}, nil
}

type localConnectorAdmissionKey struct {
	organizationSlug slug.Slug
	dataSourceSlug   slug.Slug
}

// localConnectorAdmission protects direct/combined execution. Broker-backed
// execution uses the durable distributed admission path instead.
type localConnectorAdmission struct {
	mu     sync.Mutex
	queues map[localConnectorAdmissionKey]*localConnectorAdmissionQueue
	active int64
	queued int64
}

type localConnectorAdmissionQueue struct {
	active  int
	waiters []*localConnectorAdmissionWaiter
}

type localConnectorAdmissionWaiter struct {
	ready   chan struct{}
	granted bool
}

func newLocalConnectorAdmission() *localConnectorAdmission {
	return &localConnectorAdmission{
		queues: make(map[localConnectorAdmissionKey]*localConnectorAdmissionQueue),
	}
}

func (a *localConnectorAdmission) acquire(
	ctx context.Context,
	key localConnectorAdmissionKey,
	limit int,
	queueLimit int,
	queueTimeout time.Duration,
) (func(), error) {
	a.mu.Lock()
	queue := a.queues[key]
	if queue == nil {
		queue = &localConnectorAdmissionQueue{}
		a.queues[key] = queue
	}
	if queue.active < limit && len(queue.waiters) == 0 {
		queue.active++
		a.active++
		a.recordSnapshotLocked()
		a.mu.Unlock()
		metrics.RecordConnectorAdmission("acquired")
		return a.releaseFunc(key), nil
	}
	if len(queue.waiters) >= queueLimit {
		a.mu.Unlock()
		metrics.RecordConnectorAdmission("rejected")
		return nil, xerror.ExecutionConcurrencyLimitExceeded().
			Detail("connector execution queue is full")
	}

	waiter := &localConnectorAdmissionWaiter{ready: make(chan struct{})}
	queue.waiters = append(queue.waiters, waiter)
	a.queued++
	a.recordSnapshotLocked()
	a.mu.Unlock()

	timer := time.NewTimer(queueTimeout)
	defer timer.Stop()

	select {
	case <-waiter.ready:
		metrics.RecordConnectorAdmission("acquired")
		return a.releaseFunc(key), nil
	case <-ctx.Done():
		a.cancelWaiter(key, waiter)
		metrics.RecordConnectorAdmission(localAdmissionContextOutcome(ctx.Err()))
		return nil, ctx.Err()
	case <-timer.C:
		a.cancelWaiter(key, waiter)
		metrics.RecordConnectorAdmission("timeout")
		return nil, xerror.ExecutionConcurrencyLimitExceeded().
			Detail("connector execution queue wait timed out")
	}
}

func (a *localConnectorAdmission) releaseFunc(key localConnectorAdmissionKey) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.releaseLocked(key)
		})
	}
}

func (a *localConnectorAdmission) cancelWaiter(
	key localConnectorAdmissionKey,
	waiter *localConnectorAdmissionWaiter,
) {
	a.mu.Lock()
	defer a.mu.Unlock()

	queue := a.queues[key]
	if queue == nil {
		return
	}
	if waiter.granted {
		a.releaseLocked(key)
		return
	}
	for idx, queued := range queue.waiters {
		if queued != waiter {
			continue
		}
		queue.waiters = append(queue.waiters[:idx], queue.waiters[idx+1:]...)
		a.queued--
		break
	}
	a.deleteQueueIfIdle(key, queue)
	a.recordSnapshotLocked()
}

func (a *localConnectorAdmission) releaseLocked(key localConnectorAdmissionKey) {
	queue := a.queues[key]
	if queue == nil {
		return
	}
	if queue.active > 0 {
		queue.active--
		a.active--
	}
	if len(queue.waiters) > 0 {
		waiter := queue.waiters[0]
		queue.waiters = queue.waiters[1:]
		a.queued--
		waiter.granted = true
		queue.active++
		a.active++
		close(waiter.ready)
	}
	a.deleteQueueIfIdle(key, queue)
	a.recordSnapshotLocked()
}

func (a *localConnectorAdmission) deleteQueueIfIdle(
	key localConnectorAdmissionKey,
	queue *localConnectorAdmissionQueue,
) {
	if queue.active == 0 && len(queue.waiters) == 0 {
		delete(a.queues, key)
	}
}

func (a *localConnectorAdmission) recordSnapshotLocked() {
	metrics.RecordConnectorConcurrency(
		metrics.ConnectorConcurrencyScopeProcess,
		a.active,
		a.queued,
	)
}

func localAdmissionContextOutcome(err error) string {
	if xerror.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if xerror.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "error"
}

func (e localConnectorExecutor) ExecuteConnector(
	ctx context.Context,
	request ConnectorExecutionRequest,
) (*result.ExecutionResult, error) {
	releaseAdmission, err := e.admission.acquire(
		ctx,
		localConnectorAdmissionKey{
			organizationSlug: request.OrganizationSlug,
			dataSourceSlug:   request.DataSourceSlug,
		},
		request.ConcurrencyLimit,
		request.ConcurrencyQueueLimit,
		request.ConcurrencyQueueTimeout,
	)
	if err != nil {
		return nil, err
	}
	defer releaseAdmission()

	conn, release, err := e.manager.Acquire(
		ctx,
		connector.RuntimeIdentity{
			OrganizationSlug:  request.OrganizationSlug,
			DataSourceSlug:    request.DataSourceSlug,
			DataSourceType:    request.DataSourceType,
			ConfigVersion:     request.ConfigVersion,
			CredentialVersion: request.CredentialVersion,
		},
		request.ConfigJSON,
		request.CredentialsJSON,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = release() }()
	if request.SinkExecution {
		ctx = pipelineruntime.WithSinkExecution(ctx)
	}
	return connector.Execute(ctx, conn, request.StatementJSON, request.Parameters, request.ResourceScope, request.Limits)
}

// NewWithExecutionLimits creates a query service with explicit runtime safety limits.
func NewWithExecutionLimits(
	txm postgres.TransactionManager,
	repo repoq.Repository,
	datasourcesSvc datasourcessvc.Service,
	connectorExecutor ConnectorExecutor,
	credentials credentialssvc.Service,
	clock clock.Clock,
	limits ExecutionLimits,
	eventProducer workerproducer.EventProducer,
) (Service, error) {
	if credentials == nil {
		return nil, xerror.Unexpected().Detail("credential service is required")
	}
	if connectorExecutor == nil {
		return nil, xerror.Unexpected().Detail("connector executor is required")
	}
	if limits.ConnectorConcurrencyQueueTimeout == 0 {
		limits.ConnectorConcurrencyQueueTimeout = defaultConcurrencyQueueTimeout
	}
	if limits.ConnectorConcurrencyMaxQueued == 0 {
		limits.ConnectorConcurrencyMaxQueued = defaultConcurrencyMaxQueuedPerData
	}
	if err := validateExecutionLimits(limits); err != nil {
		return nil, err
	}

	svc := &service{
		txm:               txm,
		repo:              repo,
		datasourcesSvc:    datasourcesSvc,
		credentials:       credentials,
		clock:             clock,
		limits:            limits,
		eventProducer:     eventProducer,
		connectorExecutor: connectorExecutor,
	}
	return svc, nil
}

func validateExecutionLimits(limits ExecutionLimits) error {
	if limits.QueryTimeout <= 0 {
		return ErrInvalidInput.Detail("query timeout must be greater than 0")
	}
	if limits.ConnectorTimeout <= 0 {
		return ErrInvalidInput.Detail("connector timeout must be greater than 0")
	}
	if limits.MaxResultRows <= 0 {
		return ErrInvalidInput.Detail("max result rows must be greater than 0")
	}
	if limits.MaxResponseBytes <= 0 {
		return ErrInvalidInput.Detail("max response bytes must be greater than 0")
	}
	if limits.ConnectorConcurrencyPerDataSource <= 0 {
		return ErrInvalidInput.Detail("connector concurrency per data source must be greater than 0")
	}
	if limits.ConnectorConcurrencyQueueTimeout <= 0 {
		return ErrInvalidInput.Detail("connector concurrency queue timeout must be greater than 0")
	}
	if limits.ConnectorConcurrencyMaxQueued <= 0 {
		return ErrInvalidInput.Detail("connector concurrency max queued per data source must be greater than 0")
	}
	if limits.HTTPTimeout <= 0 {
		return ErrInvalidInput.Detail("http timeout must be greater than 0")
	}
	if limits.HTTPMaxRedirects <= 0 {
		return ErrInvalidInput.Detail("http max redirects must be greater than 0")
	}
	if limits.HTTPMaxRequestBytes <= 0 {
		return ErrInvalidInput.Detail("http max request bytes must be greater than 0")
	}
	if limits.HTTPMaxResponseBytes <= 0 {
		return ErrInvalidInput.Detail("http max response bytes must be greater than 0")
	}
	if limits.HTTPRetryAttempts <= 0 {
		return ErrInvalidInput.Detail("http retry attempts must be greater than 0")
	}
	if limits.HTTPRetryBackoff < 0 {
		return ErrInvalidInput.Detail("http retry backoff must be greater than or equal to 0")
	}
	return nil
}

// Create creates a new query for the given data source.
func (s *service) Create(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	params *CreateParams,
) (*Query, error) {
	if err := validate.Default().Struct(params); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	if err := pipelinevalidate.ValidateContractAttachment(params.ExpectedInputContract, params.ExpectedInputContractRef); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	if err := pipelinevalidate.ValidateContractAttachment(params.OutputContract, params.OutputContractRef); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	if err := validateReservedRuntimeParamNamespace(params.Parameters); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	if err := validateRuntimeQueryData(params.Data); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	if hubSpotQueryDataIsUpsert(params.Data) {
		ds, err := s.datasourcesSvc.FindBySlug(ctx, orgSlug, dsSlug)
		if err != nil {
			if xerror.Is(err, datasourcessvc.ErrDataSourceNotFound) {
				return nil, ErrDataSourceUnavailable.With(err)
			}
			return nil, xerror.Wrapf("fetch data source for query creation: %w", err)
		}
		if ds.ReadOnly {
			return nil, datasourcessvc.ErrDataSourceReadOnly.Detailf(
				"data source %s is read-only: HubSpot upsert queries require a writable data source",
				dsSlug,
			)
		}
	}

	var err error

	params.Slug, err = params.Slug.ValidateOrGenerate(params.Name)
	if err != nil {
		return nil, ErrInvalidInput.Detailf("failed to generate slug: %w", err)
	}

	// One-time slug collision check: if the slug is already taken, append a short random suffix.
	slugExists := false
	slugCheckErr := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		_, _, err := s.repo.FindBySlug(ctx, db, orgSlug, dsSlug, params.Slug)
		if err == nil {
			slugExists = true
			return nil
		}
		if xerror.Is(err, repoq.ErrQueryNotFound) {
			return nil
		}
		return xerror.Wrapf("check query slug collision: %w", err)
	})
	if slugCheckErr != nil {
		return nil, slugCheckErr
	}
	if slugExists {
		// A query with this slug already exists; append a suffix.
		params.Slug, err = slug.WithRandomSuffix(params.Slug, 6)
		if err != nil {
			return nil, ErrInvalidInput.Detailf("failed to generate unique slug: %w", err)
		}
	}

	repoParams, err := toRepoCreateParams(dsSlug, params)
	if err != nil {
		return nil, ErrInvalidInput.With(err)
	}

	var ret *Query

	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		repoQ, repoQParams, err := s.repo.Create(ctx, tx, orgSlug, dsSlug, repoParams)
		if err != nil {
			return xerror.Wrapf("create query in repo: %w", err)
		}

		ret, err = toQuery(repoQ, repoQParams)
		if err != nil {
			return xerror.Wrapf("decode created query: %w", err)
		}
		if err := s.recordQueryEvent(
			ctx,
			tx,
			orgSlug,
			dsSlug,
			actor,
			ret.Slug,
			model.EventActionCreate,
		); err != nil {
			return xerror.Wrapf("record query create event: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, xerror.Wrapf("create query: %w", err)
	}

	return ret, nil
}

// FindBySlug retrieves a query by its slug.
func (s *service) FindBySlug(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
) (*Query, error) {
	var ret *Query

	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		repoQ, repoQParams, err := s.repo.FindBySlug(ctx, db, orgSlug, dsSlug, querySlug)
		if err != nil {
			return xerror.Wrapf("find query in repo: %w", err)
		}

		ret, err = toQuery(repoQ, repoQParams)
		if err != nil {
			return xerror.Wrapf("decode query: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, xerror.Wrapf("find query: %w", err)
	}

	return ret, nil
}

// GetExpectedInputContract returns the saved sink-input contract for a query when present.
func (s *service) GetExpectedInputContract(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
) (*payload.Contract, error) {
	query, err := s.FindBySlug(ctx, orgSlug, dsSlug, querySlug)
	if err != nil {
		return nil, err
	}

	return payload.CloneContract(query.ExpectedInputContract), nil
}

// ListExecutions lists query executions for the given query.
func (s *service) ListExecutions(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[*QueryExecution], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortExecutedAt)

	var executions []*QueryExecution

	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		if _, _, err := s.repo.FindBySlug(ctx, db, orgSlug, dsSlug, querySlug); err != nil {
			return xerror.Wrapf("find query for executions in repo: %w", err)
		}

		repoExecutions, err := s.repo.ListExecutions(ctx, db, orgSlug, dsSlug, querySlug, queryReq)
		if err != nil {
			return xerror.Wrapf("list executions in repo: %w", err)
		}

		executions = make([]*QueryExecution, len(repoExecutions))
		for i, repoExecution := range repoExecutions {
			executions[i] = toExecution(repoExecution)
		}

		return nil
	})
	if err != nil {
		return model.ListResponse[*QueryExecution]{}, xerror.Wrapf("list query executions: %w", err)
	}

	executions, hasMore := model.FinalizePage(executions, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		executions,
		normalized,
		hasMore,
		func(item *QueryExecution, _ string) (string, string) {
			return item.ExecutedAt.Format(time.RFC3339Nano), item.Slug.String()
		},
	)

	return model.ListResponse[*QueryExecution]{
		Items:      executions,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

// GetExecution retrieves a query execution by slug.
func (s *service) GetExecution(
	ctx context.Context,
	orgSlug, dsSlug, querySlug, executionSlug slug.Slug,
) (*QueryExecution, error) {
	var execution *QueryExecution

	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		repoExecution, err := s.repo.GetExecution(
			ctx,
			db,
			orgSlug,
			dsSlug,
			querySlug,
			executionSlug,
		)
		if err != nil {
			return xerror.Wrapf("get execution in repo: %w", err)
		}

		execution = toExecution(repoExecution)
		return nil
	})
	if err != nil {
		return nil, xerror.Wrapf("get query execution: %w", err)
	}

	return execution, nil
}

// GetExecutionSummary returns the latest recorded successful saved-query execution in an organization.
func (s *service) GetExecutionSummary(
	ctx context.Context,
	orgSlug slug.Slug,
) (*QueryExecutionIdentity, error) {
	var summary *QueryExecutionIdentity

	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		repoSummary, err := s.repo.FindLatestSuccessfulExecution(ctx, db, orgSlug)
		if err != nil {
			return xerror.Wrapf("find latest successful query execution in repo: %w", err)
		}
		if repoSummary == nil {
			return nil
		}

		summary = &QueryExecutionIdentity{
			ExecutionSlug:  repoSummary.ExecutionSlug,
			DataSourceSlug: repoSummary.DataSourceSlug,
			QuerySlug:      repoSummary.QuerySlug,
			ExecutedAt:     repoSummary.ExecutedAt,
		}
		return nil
	})
	if err != nil {
		return nil, xerror.Wrapf("get query execution summary: %w", err)
	}

	return summary, nil
}

// ListByDataSource lists all queries for the given data source with pagination.
func (s *service) ListByDataSource(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	req model.ListRequest,
	includeDeleted bool,
) (model.ListResponse[*Query], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	var result []*Query

	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		repoList, err := s.repo.ListByDataSource(ctx, db, orgSlug, dsSlug, queryReq, includeDeleted)
		if err != nil {
			return xerror.Wrapf("list queries in repo: %w", err)
		}

		result = make([]*Query, 0, len(repoList))
		for _, repoQ := range repoList {
			query, decodeErr := toQuery(repoQ, nil)
			if decodeErr != nil {
				return xerror.Wrapf("decode listed query: %w", decodeErr)
			}
			result = append(result, query)
		}

		return nil
	})
	if err != nil {
		return model.ListResponse[*Query]{}, xerror.Wrapf("list queries: %w", err)
	}

	result, hasMore := model.FinalizePage(result, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		result,
		normalized,
		hasMore,
		func(item *Query, sortBy string) (string, string) {
			cursorValue := item.CreatedAt.Format(time.RFC3339Nano)
			switch sortBy {
			case model.ListSortUpdatedAt:
				cursorValue = item.UpdatedAt.Format(time.RFC3339Nano)
			case model.ListSortName:
				cursorValue = item.Name
			}
			return cursorValue, item.Slug.String()
		},
	)

	return model.ListResponse[*Query]{
		Items:      result,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

// Update replaces an existing query's definition (full replacement).
func (s *service) Update(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	actor model.EventActor,
	params *UpdateParams,
) (*Query, error) {
	if err := validate.Default().Struct(params); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	if err := pipelinevalidate.ValidateContractAttachment(params.ExpectedInputContract, params.ExpectedInputContractRef); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	if err := pipelinevalidate.ValidateContractAttachment(params.OutputContract, params.OutputContractRef); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	if err := validateReservedRuntimeParamNamespace(params.Parameters); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	if err := validateRuntimeQueryData(params.Data); err != nil {
		return nil, ErrInvalidInput.With(err)
	}

	repoParams, err := toRepoUpdateParams(params, querySlug)
	if err != nil {
		return nil, ErrInvalidInput.With(err)
	}

	var ret *Query

	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		repoQ, repoQParams, err := s.repo.Update(ctx, tx, orgSlug, dsSlug, querySlug, repoParams)
		if err != nil {
			return xerror.Wrapf("update query in repo: %w", err)
		}

		ret, err = toQuery(repoQ, repoQParams)
		if err != nil {
			return xerror.Wrapf("decode updated query: %w", err)
		}
		if err := s.recordQueryEvent(
			ctx,
			tx,
			orgSlug,
			dsSlug,
			actor,
			querySlug,
			model.EventActionUpdate,
		); err != nil {
			return xerror.Wrapf("record query update event: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, xerror.Wrapf("update query: %w", err)
	}

	return ret, nil
}

// Delete soft-deletes a query by slug.
func (s *service) Delete(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	actor model.EventActor,
) error {
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.repo.Delete(ctx, tx, orgSlug, dsSlug, querySlug); err != nil {
			return err
		}
		return s.recordQueryEvent(
			ctx,
			tx,
			orgSlug,
			dsSlug,
			actor,
			querySlug,
			model.EventActionDelete,
		)
	})
	if err != nil {
		return xerror.Wrapf("delete query: %w", err)
	}

	return nil
}

// Restore clears the deletion tombstone for a query by slug.
func (s *service) Restore(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	actor model.EventActor,
) error {
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.repo.Restore(ctx, tx, orgSlug, dsSlug, querySlug); err != nil {
			return err
		}
		return s.recordQueryEvent(
			ctx,
			tx,
			orgSlug,
			dsSlug,
			actor,
			querySlug,
			model.EventActionRestore,
		)
	})
	if err != nil {
		return xerror.Wrapf("restore query: %w", err)
	}

	return nil
}

// Purge permanently deletes a soft-deleted query by slug.
func (s *service) Purge(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	actor model.EventActor,
) error {
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.repo.Purge(ctx, tx, orgSlug, dsSlug, querySlug); err != nil {
			return err
		}
		return s.recordQueryEvent(
			ctx,
			tx,
			orgSlug,
			dsSlug,
			actor,
			querySlug,
			model.EventActionPurge,
		)
	})
	if err != nil {
		return xerror.Wrapf("purge query: %w", err)
	}

	return nil
}

// Execute runs a saved query against its data source. By default it records the execution.
func (s *service) Execute(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	actor model.EventActor,
	paramValues map[string]any,
	opts ...ExecuteOption,
) (*result.ExecutionResult, error) {
	preparedResult, err := s.prepareSavedQueryExecution(
		ctx,
		orgSlug,
		dsSlug,
		querySlug,
	)
	if err != nil {
		return nil, xerror.Wrapf("execute query: %w", err)
	}

	executionOptions := resolveExecuteOptions(opts)
	if !executionOptions.RecordExecution {
		preparedResult.recordQuerySlug = ""
	}

	execResult, err := s.executePrepared(
		ctx,
		orgSlug,
		dsSlug,
		paramValues,
		preparedResult,
		actor,
	)
	if err != nil {
		return nil, xerror.Wrapf("execute query: %w", err)
	}

	return execResult, nil
}

// ExecuteInline runs an inline query operation against its data source without loading a stored
// query.
func (s *service) ExecuteInline(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	operationInline map[string]any,
	paramValues map[string]any,
) (*result.ExecutionResult, error) {
	preparedResult, err := prepareInlineExecution(ctx, operationInline)
	if err != nil {
		return nil, xerror.Wrapf("execute inline query: %w", err)
	}

	execResult, err := s.executePrepared(
		ctx,
		orgSlug,
		dsSlug,
		paramValues,
		preparedResult,
		model.SystemEventActor(),
	)
	if err != nil {
		return nil, xerror.Wrapf("execute inline query: %w", err)
	}

	return execResult, nil
}

func (s *service) recordQueryEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	dsSlug slug.Slug,
	actor model.EventActor,
	querySlug slug.Slug,
	action model.EventAction,
) error {
	switch action {
	case model.EventActionCreate,
		model.EventActionUpdate,
		model.EventActionDelete,
		model.EventActionRestore,
		model.EventActionPurge:
	default:
		return xerror.InvalidInput(xerror.Event).Detail("unsupported query event action")
	}

	event := queryEvent{
		dataSourceSlug: dsSlug,
		querySlug:      querySlug,
		action:         action,
	}
	if action == model.EventActionDelete || action == model.EventActionPurge {
		if _, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, event); err != nil {
			return xerror.Wrapf("enqueue required query audit event: %w", err)
		}
		return nil
	}
	s.eventProducer.EnqueueRecord(ctx, tx, orgSlug, actor, event)
	return nil
}

type preparedExecution struct {
	inline          bool
	resourceScope   *resourcescope.Scope
	queryParams     []*QueryParameter
	statementJSON   json.RawMessage
	timeoutSeconds  int
	rowLimit        int
	recordQuerySlug slug.Slug
}

func (s *service) prepareSavedQueryExecution(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
) (*preparedExecution, error) {
	var prepared *preparedExecution

	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		repoQ, repoQParams, err := s.repo.FindBySlug(ctx, db, orgSlug, dsSlug, querySlug)
		if err != nil {
			if xerror.Is(err, ErrQueryNotFound) {
				// The query may be missing because the data source was deleted (cascade).
				// Check the data source to return the more accurate error.
				_, dsErr := s.datasourcesSvc.FindBySlug(ctx, orgSlug, dsSlug)
				if xerror.Is(dsErr, datasourcessvc.ErrDataSourceNotFound) {
					return ErrDataSourceUnavailable.With(dsErr)
				}
			}
			return xerror.Wrapf("find query for execution: %w", err)
		}

		q, err := toQuery(repoQ, repoQParams)
		if err != nil {
			return xerror.Wrapf("decode query for execution: %w", err)
		}
		if q.ExpectedInputContract != nil || q.ExpectedInputContractRef != nil || q.OutputContract != nil || q.OutputContractRef != nil {
			return ErrOperationSnapshotRequired
		}
		statementJSON, err := ExtractQueryStatement(q.Data)
		if err != nil {
			return ErrInvalidInput.With(err)
		}

		prepared = &preparedExecution{
			queryParams:     q.Parameters,
			statementJSON:   statementJSON,
			timeoutSeconds:  parseTimeoutSeconds(q.Data),
			rowLimit:        parseRowLimit(q.Data),
			recordQuerySlug: querySlug,
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return prepared, nil
}

func prepareInlineExecution(ctx context.Context, operationInline map[string]any) (*preparedExecution, error) {
	if len(operationInline) == 0 {
		return nil, ErrInvalidInput.Detail("inline operation payload is required")
	}

	statementJSON, err := json.Marshal(operationInline)
	if err != nil {
		return nil, ErrInvalidInput.Detailf("marshal inline operation: %w", err)
	}
	if err := validateRuntimeExecutionData(ctx, statementJSON, true); err != nil {
		return nil, ErrInvalidInput.With(err)
	}

	return &preparedExecution{
		inline:        true,
		statementJSON: statementJSON,
	}, nil
}

func (s *service) executePrepared(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	paramValues map[string]any,
	prepared *preparedExecution,
	actor model.EventActor,
) (*result.ExecutionResult, error) {
	if prepared == nil {
		return nil, ErrInvalidInput.Detail("missing execution payload")
	}
	if err := validateRuntimeExecutionData(ctx, prepared.statementJSON, prepared.inline); err != nil {
		return nil, ErrInvalidInput.With(err)
	}

	ds, err := s.datasourcesSvc.FindBySlug(ctx, orgSlug, dsSlug)
	if err != nil {
		if xerror.Is(err, datasourcessvc.ErrDataSourceNotFound) {
			return nil, ErrDataSourceUnavailable.With(err)
		}

		return nil, xerror.Wrapf("fetch data source for execution: %w", err)
	}

	if !ds.IsActive {
		return nil, ErrDataSourceUnavailable
	}
	if ds.Type == model.DataSourceTypeUnknown {
		return nil, ErrUnsupportedDataSourceType
	}
	if scope := prepared.resourceScope; scope != nil && (ds.Type != model.DataSourceTypeS3 || scope.ValidateStatement(prepared.statementJSON) != nil || scope.DataSource != dsSlug) {
		return nil, resourcescope.ErrInvalid
	}

	pinnedConfig, err := deploymentplan.DispatchConfig(ctx, orgSlug, dsSlug)
	if err != nil {
		return nil, err
	}
	if pinnedConfig != "" {
		current, err := connector.PublicConfigDigest(ds.Type, ds.Config)
		if err != nil || current != pinnedConfig {
			return nil, deploymentplan.ErrInvalid.With(err)
		}
	}
	typedParams, err := buildTypedParams(prepared.queryParams, paramValues)
	if err != nil {
		if prepared.resourceScope != nil {
			return nil, resourcescope.ErrInvalid.With(err)
		}
		return nil, ErrInvalidInput.With(err)
	}
	runtimeNow := s.clock.Now()
	if snapshot, ok := pipelineruntime.ExecutionContextFrom(ctx); ok {
		runtimeNow = snapshot.At
	}
	typedParams, err = withReservedRuntimeParams(ctx, typedParams, runtimeNow)
	if err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	for name, value := range pipelineruntime.LiteralBindingsFrom(ctx) {
		typedParams[name] = types.TypedParam{Value: value, DataType: ParameterDataTypeText.String()}
	}
	if err := validateQueryParams(prepared.statementJSON, typedParams); err != nil {
		if prepared.resourceScope != nil {
			return nil, resourcescope.ErrInvalid
		}
		return nil, ErrInvalidInput.With(err)
	}
	if err := enforceReadOnlyDirectExecution(ds, dsSlug, prepared.statementJSON); err != nil {
		return nil, err
	}

	configJSON, credentialsJSON, configVersion, credentialVersion, credentialReference, err := s.resolveExecutionPayload(
		ctx,
		orgSlug,
		dsSlug,
		ds,
	)
	if err != nil {
		return nil, err
	}

	if err := deploymentplan.Admit(ctx, dsSlug, credentialReference); err != nil {
		return nil, err
	}

	execCtx, cancel := context.WithTimeout(ctx, s.effectiveTimeout(prepared))
	defer cancel()

	connectorLimits := s.effectiveConnectorLimits(prepared)
	connectorLimits.ReadOnly = ds.ReadOnly
	execResult, err := s.connectorExecutor.ExecuteConnector(execCtx, ConnectorExecutionRequest{
		SinkExecution:           pipelineruntime.IsSinkExecution(ctx),
		PinnedConfigDigest:      pinnedConfig,
		CredentialReference:     credentialReference,
		ResourceScope:           prepared.resourceScope,
		OrganizationSlug:        orgSlug,
		DataSourceSlug:          dsSlug,
		DataSourceType:          ds.Type,
		ConfigVersion:           configVersion,
		CredentialVersion:       credentialVersion,
		ConcurrencyLimit:        s.limits.ConnectorConcurrencyPerDataSource,
		ConcurrencyQueueLimit:   s.limits.ConnectorConcurrencyMaxQueued,
		ConcurrencyQueueTimeout: s.limits.ConnectorConcurrencyQueueTimeout,
		ConfigJSON:              configJSON,
		CredentialsJSON:         credentialsJSON,
		StatementJSON:           prepared.statementJSON,
		Parameters:              typedParams,
		Limits:                  connectorLimits,
	})
	if err != nil {
		if xerror.Is(execCtx.Err(), context.Canceled) {
			return nil, context.Canceled
		}
		if xerror.Is(execCtx.Err(), context.DeadlineExceeded) {
			return nil, ErrQueryTimeout.With(execCtx.Err())
		}
		if xerror.Is(err, resourcescope.ErrInvalid) {
			logging.LogError(execCtx, "query resource scope rejected", err)
			return nil, resourcescope.ErrInvalid
		}
		if isPreservedExecutionError(err) {
			return nil, err
		}
		return nil, xerror.Wrapf("%w: connector execution failed: %w", ErrDataSourceUnavailable, err)
	}

	if execResult != nil {
		if err := s.enforceResultLimits(execResult, connectorLimits); err != nil {
			if httpResult, ok := execResult.HTTP(); ok && httpResult != nil {
				var limitErr *xerror.Error
				if xerror.As(err, &limitErr) && (xerror.Is(err, xerror.ExecutionRowLimitExceeded()) ||
					xerror.Is(err, xerror.ExecutionResponseSizeExceeded())) {
					err = limitErr.WithPublicDetail(fmt.Sprintf(
						"HTTP request completed with status %d; its response exceeded the configured limit. Verify remote state before retrying a write.", httpResult.StatusCode,
					))
				}
			}
			execResult = resultWithError(execResult, err)
		}
	}

	if prepared.recordQuerySlug != "" && execResult != nil {
		s.recordExecutionInNewTx(
			ctx,
			orgSlug,
			dsSlug,
			prepared.recordQuerySlug,
			execResult,
			actor,
			metadataParameterKeys(paramValues),
		)
	}

	if execResult != nil && execResult.Error() != nil {
		if xerror.Is(execCtx.Err(), context.Canceled) {
			return nil, context.Canceled
		}
		if xerror.Is(execCtx.Err(), context.DeadlineExceeded) {
			return nil, ErrQueryTimeout.With(execCtx.Err())
		}
		if xerror.Is(execResult.Error(), resourcescope.ErrInvalid) {
			logging.LogError(execCtx, "query resource scope rejected", execResult.Error())
			return nil, resourcescope.ErrInvalid
		}
		if isPreservedExecutionError(execResult.Error()) {
			return nil, execResult.Error()
		}

		return nil, ErrQueryExecutionFailed.With(execResult.Error())
	}

	return execResult, nil
}

func (s *service) resolveExecutionPayload(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	ds *datasourcessvc.DataSource,
) ([]byte, []byte, string, string, string, error) {
	if ds.Type == model.DataSourceTypeGithub || ds.Type == model.DataSourceTypeGitlab {
		auth, err := s.datasourcesSvc.ResolveGitExecutionAuth(ctx, orgSlug, dsSlug)
		if err != nil {
			return nil, nil, "", "", "", xerror.Wrapf("resolve provider repository execution auth: %w", err)
		}
		return auth.ConfigJSON,
			auth.CredentialsJSON,
			jsonPayloadVersion(auth.ConfigJSON),
			jsonPayloadVersion(auth.CredentialsJSON),
			auth.CredentialReference,
			nil
	}

	runtimeCredential, err := s.resolveCredential(ctx, orgSlug, dsSlug, ds)
	if err != nil {
		return nil, nil, "", "", "", xerror.Wrapf("resolve data source credentials: %w", err)
	}
	poolVersion := runtimeCredential.Version
	if poolVersion == "" {
		poolVersion = jsonPayloadVersion(runtimeCredential.PlaintextJSON)
	}
	return ds.Config,
		runtimeCredential.PlaintextJSON,
		jsonPayloadVersion(ds.Config),
		poolVersion,
		runtimeCredential.Version,
		nil
}

func (s *service) resolveCredential(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	ds *datasourcessvc.DataSource,
) (credentialssvc.RuntimeCredential, error) {
	runtimeCredential, err := s.credentials.ResolveRuntimeCredential(
		ctx,
		credentialssvc.ResolveRuntimeCredentialParams{
			Owner: credentialssvc.CredentialOwner{
				OrgSlug:        orgSlug,
				DataSourceSlug: dsSlug,
			},
		},
	)
	if err != nil {
		return credentialssvc.RuntimeCredential{}, err
	}
	return runtimeCredential, nil
}

func jsonPayloadVersion(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func (s *service) effectiveTimeout(prepared *preparedExecution) time.Duration {
	queryTimeout := s.limits.QueryTimeout

	if prepared != nil && prepared.timeoutSeconds > 0 {
		savedTimeout := time.Duration(prepared.timeoutSeconds) * time.Second
		if savedTimeout < queryTimeout {
			queryTimeout = savedTimeout
		}
	}

	if s.limits.ConnectorTimeout > 0 && s.limits.ConnectorTimeout < queryTimeout {
		queryTimeout = s.limits.ConnectorTimeout
	}

	return queryTimeout
}

func (s *service) effectiveConnectorLimits(prepared *preparedExecution) types.ExecutionLimits {
	limits := s.limits.connectorLimits()
	if prepared != nil && prepared.rowLimit > 0 && prepared.rowLimit < limits.MaxResultRows {
		limits.MaxResultRows = prepared.rowLimit
	}
	return limits
}

func (s *service) enforceResultLimits(
	execResult *result.ExecutionResult,
	limits types.ExecutionLimits,
) error {
	if execResult == nil {
		return nil
	}

	maxRows := limits.MaxResultRows
	if maxRows > 0 {
		if rowCountOf(execResult) > maxRows {
			return xerror.ExecutionRowLimitExceeded().Detailf(
				"query returned more than %d rows",
				maxRows,
			)
		}
	}

	return execResult.CheckResponseSize(limits.MaxResponseBytes)
}

func rowCountOf(execResult *result.ExecutionResult) int {
	if httpResult, ok := execResult.HTTP(); ok && httpResult != nil {
		return httpResult.RowCount()
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

func resultWithError(execResult *result.ExecutionResult, err error) *result.ExecutionResult {
	if execResult == nil {
		return nil
	}

	switch execResult.Type() {
	case result.ExecutionResultTypeSQL:
		payload, _ := execResult.SQL()
		return result.NewSQLResult(execResult.VendorType(), payload, execResult.DurationMs(), err)
	case result.ExecutionResultTypeMongoDB:
		payload, _ := execResult.MongoDB()
		return result.NewMongoDBResult(payload, execResult.DurationMs(), err)
	case result.ExecutionResultTypeHTTP:
		payload, _ := execResult.HTTP()
		return result.NewHTTPResult(payload, execResult.DurationMs(), err)
	case result.ExecutionResultTypeObjectRows:
		payload, _ := execResult.ObjectRows()
		return result.NewObjectRowsResult(execResult.VendorType(), payload, execResult.DurationMs(), err)
	case result.ExecutionResultTypeFTP:
		payload, _ := execResult.FTP()
		return result.NewFTPResult(payload, execResult.DurationMs(), err)
	case result.ExecutionResultTypeGit:
		payload, _ := execResult.Git()
		return result.NewGitResult(payload, execResult.DurationMs(), err)
	case result.ExecutionResultTypeWorkspaceFile:
		payload, _ := execResult.File()
		return result.NewWorkspaceFileResult(payload, execResult.DurationMs(), err)
	case result.ExecutionResultTypeS3:
		payload, _ := execResult.S3()
		return result.NewS3Result(payload, execResult.DurationMs(), err)
	default:
		return execResult
	}
}

func isPreservedExecutionError(err error) bool {
	return xerror.Is(err, xerror.ExecutionRowLimitExceeded()) ||
		xerror.Is(err, xerror.ExecutionResponseSizeExceeded()) ||
		xerror.Is(err, xerror.ExecutionRequestSizeExceeded()) ||
		xerror.Is(err, xerror.ExecutionRedirectLimitExceeded()) ||
		xerror.Is(err, xerror.ExecutionConcurrencyLimitExceeded()) ||
		xerror.Is(err, xerror.HTTPAcknowledgementRejected()) ||
		xerror.Is(err, xerror.HTTPAcknowledgementUncertain()) ||
		xerror.Is(err, xerror.DestinationConstraintViolation())
}

func (s *service) recordExecutionInNewTx(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	result *result.ExecutionResult,
	actor model.EventActor,
	parameterKeys []string,
) {
	if err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		s.recordExecution(ctx, tx, orgSlug, dsSlug, querySlug, result, actor, parameterKeys)
		return nil
	}); err != nil {
		slog.Warn("failed to open query execution recording transaction", "error", err)
	}
}

// recordExecution persists an execution record within the provided transaction.
// Failures are logged but not returned since execution recording is best-effort.
func (s *service) recordExecution(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
	result *result.ExecutionResult,
	actor model.EventActor,
	parameterKeys []string,
) {
	execSlug, err := slug.WithRandomSuffix(querySlug+"-"+executionSlugBase, 12)
	if err != nil {
		slog.Warn("failed to generate execution slug", "error", err)
		return
	}

	// Serialize the result to JSON for storage.
	resultJSON, err := json.Marshal(result)
	if err != nil {
		slog.Warn("failed to marshal execution result", "error", err)
		return
	}

	params := &repoq.CreateExecutionParams{
		Slug:   execSlug,
		Status: executionStatusFromResult(result),
		Result: resultJSON,
	}

	execution, err := s.repo.CreateExecution(ctx, tx, orgSlug, dsSlug, querySlug, params)
	if err != nil {
		slog.Warn("failed to record query execution", "error", err)
		return
	}

	status := executionStatusFromResult(result)
	executed := queryExecutedEvent{
		dataSourceSlug: dsSlug,
		querySlug:      querySlug,
		executionSlug:  execution.Slug,
		status:         status,
		parameterKeys:  parameterKeys,
	}
	if rowCount, ok := executionRowCount(result); ok {
		executed.rowCount = &rowCount
	}

	s.eventProducer.EnqueueRecord(ctx, tx, orgSlug, actor, executed)
}

func metadataParameterKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func executionRowCount(executionResult *result.ExecutionResult) (int, bool) {
	if sqlResult, ok := executionResult.SQL(); ok && sqlResult != nil {
		return sqlResult.RowCount, true
	}
	if mongoResult, ok := executionResult.MongoDB(); ok && mongoResult != nil {
		if mongoResult.DocumentCount != nil {
			return *mongoResult.DocumentCount, true
		}
	}
	if httpResult, ok := executionResult.HTTP(); ok && httpResult != nil {
		if httpResult.Body == nil {
			return 0, true
		}
		return 1, true
	}
	return 0, false
}

func executionStatusFromResult(result *result.ExecutionResult) string {
	if result.Error() != nil {
		return "failed"
	}

	return "success"
}

// buildTypedParams constructs a map of TypedParam structs from query parameter metadata and runtime
// values. Connectors use the DataType field to perform engine-specific normalization (SQL int64,
// HTTP formatting, Mongo BSON).
func buildTypedParams(
	queryParams []*QueryParameter,
	paramValues map[string]any,
) (map[string]types.TypedParam, error) {
	resolvedValues := make(map[string]any, len(paramValues))
	for name, value := range paramValues {
		resolved, err := pipelineruntime.ResolveIncrementalValue(value, paramValues)
		if err != nil {
			return nil, err
		}
		resolvedValues[name] = resolved
	}
	paramValues = resolvedValues
	typedParams := make(map[string]types.TypedParam, len(queryParams)+len(paramValues))

	for _, qp := range queryParams {
		value, ok := paramValues[qp.Name]
		if !ok {
			if qp.DefaultValue == nil {
				continue
			}

			value = *qp.DefaultValue
		}

		typedParams[qp.Name] = types.TypedParam{
			Value:    pipelineruntime.LiteralValue(value),
			DataType: qp.DataType.String(),
		}
	}

	for name, value := range paramValues {
		if _, ok := typedParams[name]; ok {
			continue
		}

		typedParams[name] = types.TypedParam{
			Value:    pipelineruntime.LiteralValue(value),
			DataType: ParameterDataTypeText.String(),
		}
	}

	return typedParams, nil
}

// parseTimeoutSeconds reads the "timeout_seconds" field from query Data JSON.
func parseTimeoutSeconds(data json.RawMessage) int {
	var obj struct {
		TimeoutSeconds int `json:"timeout_seconds"`
	}

	if err := json.Unmarshal(data, &obj); err != nil {
		return 0
	}

	if obj.TimeoutSeconds <= 0 {
		return 0
	}

	return obj.TimeoutSeconds
}

func parseRowLimit(data json.RawMessage) int {
	var obj struct {
		RowLimit int `json:"row_limit"`
	}

	if err := json.Unmarshal(data, &obj); err != nil {
		return 0
	}

	return obj.RowLimit
}

// validateQueryParams extracts all {{paramName}} patterns from the query data JSON
// and validates that each referenced parameter exists in paramValues.
// Returns an error if any required parameter is missing.
// Note: param_location is optional UI metadata; all parameters are reusable across any location.
func validateQueryParams(data json.RawMessage, params map[string]types.TypedParam) error {
	dataStr := string(data)
	paramNames := param.ExtractParamNames(dataStr)

	for _, name := range paramNames {
		if _, ok := params[name]; !ok {
			return ErrInvalidInput.Detailf("missing required parameter %q", name)
		}
	}

	return nil
}

// ExtractQueryStatement extracts the polymorphic query statement from the wrapped query data JSON.
// The data structure is: {"statement": "...", "timeout_seconds": ...}
// This function returns just the polymorphic statement part as JSON.
func ExtractQueryStatement(data json.RawMessage) (json.RawMessage, error) {
	var wrapper struct {
		Statement string `json:"statement"`
	}

	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, ErrInvalidInput.Detailf("failed to unmarshal query data wrapper: %w", err)
	}

	if wrapper.Statement == "" {
		return nil, ErrInvalidInput.Detail("query data must contain a statement")
	}

	return []byte(wrapper.Statement), nil
}
