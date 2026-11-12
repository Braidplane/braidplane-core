package executionbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	pipepayload "github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	runtimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	requestrepo "github.com/Braidplane/braidplane-core/postgres/executionrequests"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	connectortypes "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

const testRequestTimeout = 30 * time.Second

type fakeTXM struct{}

func (fakeTXM) RunInTx(ctx context.Context, fn func(context.Context, postgres.DBTX) error) error {
	return fn(ctx, nil)
}

func (fakeTXM) RunRead(ctx context.Context, fn func(context.Context, postgres.DBTX) error) error {
	return fn(ctx, nil)
}

type recordingDispatcher struct {
	requestID uuid.UUID
	calls     atomic.Int32
	err       error
}

func (d *recordingDispatcher) Dispatch(
	_ context.Context,
	_ postgres.DBTX,
	requestID uuid.UUID,
) error {
	d.calls.Add(1)
	d.requestID = requestID
	return d.err
}

type fakeRequestRepo struct {
	dispatchDenied        bool
	dispatchErr           error
	lastDispatch          requestrepo.DispatchAuthority
	dispatchCalls         int
	request               requestrepo.Request
	findErr               error
	findHook              func(context.Context) error
	created               requestrepo.Request
	heartbeatCancelled    bool
	heartbeatCalls        atomic.Int32
	heartbeatCancelAfter  int32
	heartbeatErr          error
	completeCalls         atomic.Int32
	claimCalls            atomic.Int32
	preflightErr          error
	preflightDeliveryErr  error
	claimFound            *bool
	deliverySnapshot      requestrepo.DeliverySnapshot
	deleteExpiredCalls    atomic.Int32
	metrics               requestrepo.Metrics
	cancelBeforeExecution bool
	cancelErr             error
	cancelCalls           atomic.Int32
	boundUser             slug.Slug
	boundSource           slug.Slug
	bindErr               error
}

func (r *fakeRequestRepo) BindUser(_ context.Context, _ postgres.DBTX, _ uuid.UUID, _ string, user slug.Slug) error {
	r.boundUser = user
	return r.bindErr
}

func (r *fakeRequestRepo) BindSource(_ context.Context, _ postgres.DBTX, _ uuid.UUID, _ string, source slug.Slug) error {
	r.boundSource = source
	return nil
}

func (r *fakeRequestRepo) Preflight(context.Context, postgres.DBTX) error { return r.preflightErr }
func (r *fakeRequestRepo) PreflightDelivery(context.Context, postgres.DBTX) error {
	return r.preflightDeliveryErr
}

func (r *fakeRequestRepo) Create(_ context.Context, _ postgres.DBTX, request requestrepo.Request, _ time.Time) error {
	r.created = request
	return nil
}

func (r *fakeRequestRepo) DeleteExpired(context.Context, postgres.DBTX) (int64, error) {
	r.deleteExpiredCalls.Add(1)
	return 0, nil
}

func (r *fakeRequestRepo) Metrics(context.Context, postgres.DBTX) (requestrepo.Metrics, error) {
	return r.metrics, nil
}

func (r *fakeRequestRepo) Find(ctx context.Context, _ postgres.DBTX, _ uuid.UUID, _ string) (requestrepo.Request, error) {
	if r.findHook != nil {
		return r.request, r.findHook(ctx)
	}
	return r.request, r.findErr
}

func (r *fakeRequestRepo) Claim(context.Context, postgres.DBTX, uuid.UUID) (requestrepo.Request, bool, error) {
	r.claimCalls.Add(1)
	if r.claimFound != nil {
		return r.request, *r.claimFound, nil
	}
	return r.request, true, nil
}

func (r *fakeRequestRepo) FindForDelivery(
	context.Context,
	postgres.DBTX,
	uuid.UUID,
) (requestrepo.DeliverySnapshot, bool, error) {
	if r.deliverySnapshot.Request.ID != uuid.Nil {
		return r.deliverySnapshot, true, nil
	}
	return requestrepo.DeliverySnapshot{}, false, nil
}

func (r *fakeRequestRepo) ClaimNext(context.Context, postgres.DBTX) (requestrepo.Request, bool, error) {
	return r.request, r.request.ID != uuid.Nil, nil
}

func (r *fakeRequestRepo) Heartbeat(context.Context, postgres.DBTX, uuid.UUID, uuid.UUID) (bool, error) {
	calls := r.heartbeatCalls.Add(1)
	return r.heartbeatCancelled || (r.heartbeatCancelAfter > 0 && calls > r.heartbeatCancelAfter), r.heartbeatErr
}

func (r *fakeRequestRepo) Complete(
	_ context.Context,
	_ postgres.DBTX,
	_ uuid.UUID,
	_ uuid.UUID,
	resultCiphertext []byte,
	succeeded bool,
) error {
	r.completeCalls.Add(1)
	r.request.ResultCiphertext = append([]byte(nil), resultCiphertext...)
	if succeeded {
		r.request.State = requestrepo.StateSucceeded
	} else {
		r.request.State = requestrepo.StateFailed
	}
	return nil
}

func (r *fakeRequestRepo) Cancel(context.Context, postgres.DBTX, uuid.UUID) (bool, error) {
	r.cancelCalls.Add(1)
	return r.cancelBeforeExecution, r.cancelErr
}

func configureTestProcessor(t *testing.T, broker *Broker) {
	t.Helper()
	if broker.logger == nil {
		broker.logger = slog.New(slog.DiscardHandler)
	}
	if broker.factory == nil {
		broker.factory = func(
			context.Context,
			model.DataSourceType,
			[]byte,
			[]byte,
		) (connector.Connector, error) {
			return nil, xerror.Unexpected().Detail("connector creation should not be reached")
		}
	}
	runtimePool, err := connector.NewManager(broker.factory, connector.RuntimePoolSettings{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtimePool.Close()) })
	broker.runtimePool = runtimePool
}

func TestBrokerMaintenanceDeletesExpiredEnvelopesWithoutNewTraffic(t *testing.T) {
	t.Parallel()

	repo := &fakeRequestRepo{}
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher,
		dispatcher: NewPollerDispatcher(), requestTimeout: testRequestTimeout,
		logger: slog.New(slog.DiscardHandler),
	}
	broker.maintenanceEvery = time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	broker.StartMaintenance(ctx)
	require.Eventually(t, func() bool {
		return repo.deleteExpiredCalls.Load() >= 2
	}, time.Second, time.Millisecond)
}

func TestBrokerSubmitDoesNotPruneExpiredEnvelopes(t *testing.T) {
	t.Parallel()

	repo := &fakeRequestRepo{}
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher,
		dispatcher: NewPollerDispatcher(), requestTimeout: testRequestTimeout,
		logger: slog.New(slog.DiscardHandler),
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = broker.submitAndWait(ctx, "acme", kindConnectionTest, struct{}{})
	require.Error(t, err)
	require.Zero(t, repo.deleteExpiredCalls.Load())
}

type blockingConnector struct{ cancelled atomic.Bool }

func (c *blockingConnector) Execute(context.Context, json.RawMessage, map[string]connectortypes.TypedParam, ...connectortypes.ExecutionLimits) (*result.ExecutionResult, error) {
	return nil, xerror.Unexpected().Detail("blocking connector execute is unsupported")
}

func (c *blockingConnector) TestConnection(ctx context.Context) error {
	<-ctx.Done()
	c.cancelled.Store(true)
	return ctx.Err()
}
func (*blockingConnector) Close() error { return nil }

type recordingQueryConnector struct {
	limits        connectortypes.ExecutionLimits
	parameters    map[string]connectortypes.TypedParam
	sinkExecution bool
}

func (c *recordingQueryConnector) Execute(
	ctx context.Context,
	_ json.RawMessage,
	parameters map[string]connectortypes.TypedParam,
	limits ...connectortypes.ExecutionLimits,
) (*result.ExecutionResult, error) {
	c.limits = connectortypes.ResolveExecutionLimits(limits...)
	c.parameters = parameters
	c.sinkExecution = runtimevars.IsSinkExecution(ctx)
	return result.NewSQLResult(
		"postgres",
		&result.SQLResult{Rows: []map[string]any{}, ColumnNames: []string{}},
		0,
		nil,
	), nil
}

func (*recordingQueryConnector) TestConnection(context.Context) error { return nil }
func (*recordingQueryConnector) Close() error                         { return nil }

type fixedResultConnector struct {
	calls  atomic.Int32
	result *result.ExecutionResult
	err    error
}

func (c *fixedResultConnector) Execute(
	context.Context,
	json.RawMessage,
	map[string]connectortypes.TypedParam,
	...connectortypes.ExecutionLimits,
) (*result.ExecutionResult, error) {
	c.calls.Add(1)
	return c.result, c.err
}

func (*fixedResultConnector) TestConnection(context.Context) error { return nil }
func (*fixedResultConnector) Close() error                         { return nil }

func TestProcessPreservesReadOnlyModeAndNativeBytesAcrossEncryptedQueryEnvelope(t *testing.T) {
	t.Parallel()

	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	content := []byte{0xff, 0xd8, 0xff, 0xe0}
	payload, err := marshalRequestPlaintext(kindQuery, queriessvc.ConnectorExecutionRequest{
		OrganizationSlug:    slug.Slug("acme"),
		DataSourceSlug:      slug.Slug("warehouse"),
		DataSourceType:      model.DataSourceTypePostgres,
		ConfigVersion:       "config-v1",
		CredentialVersion:   "credential-v1",
		CredentialReference: "secret/2026-10-18T10:00:00Z",
		StatementJSON:       json.RawMessage(`{"type":"postgres","sql":"SELECT 1"}`),
		Parameters: map[string]connectortypes.TypedParam{
			"input": {Value: content, DataType: "text"},
		},
		Limits: connectortypes.ExecutionLimits{
			ReadOnly:         true,
			MaxResponseBytes: 1024,
		},
	})
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt(payload)
	require.NoError(t, err)

	id := uuid.New()
	repo := &fakeRequestRepo{request: requestrepo.Request{
		ID: id, State: requestrepo.StateRunning, Kind: kindQuery,
		RequestCiphertext: encrypted, DeadlineAt: time.Now().Add(time.Minute),
	}}
	conn := &recordingQueryConnector{}
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour,
		factory: func(
			context.Context,
			model.DataSourceType,
			[]byte,
			[]byte,
		) (connector.Connector, error) {
			return conn, nil
		},
	}
	configureTestProcessor(t, broker)

	processResult, err := broker.Process(t.Context(), id)

	require.NoError(t, err)
	require.Equal(t, executiondelivery.ProcessDone, processResult.Disposition)
	require.True(t, conn.limits.ReadOnly)
	require.Equal(t, content, conn.parameters["input"].Value)
	require.EqualValues(t, 1, repo.completeCalls.Load())
}

func TestProcessRejectsIncompatibleEnvelopesBeforeProviderWork(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	foreignCipher, err := crypto.NewAES256(bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)
	for _, tc := range []struct {
		name, kind string
		ciphertext []byte
	}{
		{"different envelope key", kindConnectionTest, mustEncrypt(t, foreignCipher, []byte(`{"credentials_json":{"password":"synthetic-only"}}`))},
		{"malformed envelope", kindConnectionTest, mustEncrypt(t, cipher, []byte(`{"incomplete":`))},
		{"unknown operation contract", "future_operation", mustEncrypt(t, cipher, []byte(`{}`))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New()
			repo := &fakeRequestRepo{request: requestrepo.Request{ID: id, State: requestrepo.StateRunning, Kind: tc.kind, RequestCiphertext: tc.ciphertext, DeadlineAt: time.Now().Add(time.Minute)}}
			var providerCalls atomic.Int32
			broker := &Broker{
				admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour,
				factory: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
					providerCalls.Add(1)
					return &testutil.FakeSQLConnector{}, nil
				},
			}
			configureTestProcessor(t, broker)
			processed, err := broker.Process(t.Context(), id)
			require.NoError(t, err)
			require.Equal(t, executiondelivery.ProcessDone, processed.Disposition)
			require.Zero(t, providerCalls.Load())
			require.EqualValues(t, 1, repo.completeCalls.Load())
			require.Equal(t, requestrepo.StateFailed, repo.request.State)
			plaintext, err := cipher.Decrypt(repo.request.ResultCiphertext)
			require.NoError(t, err)
			var response encryptedResponse
			require.NoError(t, json.Unmarshal(plaintext, &response))
			require.Equal(t, errorCodeInvalidRequest, response.ErrorCode)
			require.NotContains(t, string(plaintext), "synthetic-only")
		})
	}
}

func TestProcessCompletesOversizedRuntimeResponseWithoutReexecution(t *testing.T) {
	t.Parallel()

	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	requestPlaintext, err := marshalRequestPlaintext(
		kindQuery,
		queriessvc.ConnectorExecutionRequest{
			CredentialReference: "secret/2026-10-18T10:00:00Z",
			OrganizationSlug:    slug.Slug("acme"),
			DataSourceSlug:      slug.Slug("source"),
			DataSourceType:      model.DataSourceTypeHTTP,
			Limits:              connectortypes.ExecutionLimits{MaxResponseBytes: 1},
		},
	)
	require.NoError(t, err)
	requestCiphertext, err := cipher.Encrypt(requestPlaintext)
	require.NoError(t, err)
	request := requestrepo.Request{
		ID:                uuid.New(),
		OrganizationSlug:  "acme",
		State:             requestrepo.StateRunning,
		Kind:              kindQuery,
		RequestCiphertext: requestCiphertext,
		DeadlineAt:        time.Now().Add(time.Minute),
	}
	repo := &fakeRequestRepo{request: request}
	conn := &fixedResultConnector{result: result.NewHTTPResult(
		&result.HTTPResult{RawBody: []byte{0x01, 0x02}},
		0,
		nil,
	)}
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour,
		factory: func(
			context.Context,
			model.DataSourceType,
			[]byte,
			[]byte,
		) (connector.Connector, error) {
			return conn, nil
		},
	}
	configureTestProcessor(t, broker)

	require.NoError(t, broker.processClaimed(t.Context(), request))
	require.EqualValues(t, 1, conn.calls.Load())
	require.EqualValues(t, 1, repo.completeCalls.Load())
	require.Equal(t, requestrepo.StateFailed, repo.request.State)

	response, done, err := broker.readResponse(
		t.Context(),
		request.ID,
		request.OrganizationSlug,
	)
	require.NoError(t, err)
	require.True(t, done)
	require.True(
		t,
		xerror.Is(responseError(response), xerror.ExecutionResponseSizeExceeded()),
	)
}

type freshConnectionTestConnector struct {
	closed *atomic.Int32
}

func (*freshConnectionTestConnector) Execute(
	context.Context,
	json.RawMessage,
	map[string]connectortypes.TypedParam,
	...connectortypes.ExecutionLimits,
) (*result.ExecutionResult, error) {
	return nil, xerror.Unexpected().Detail("execute is unsupported")
}

func (*freshConnectionTestConnector) TestConnection(context.Context) error { return nil }
func (c *freshConnectionTestConnector) Close() error {
	c.closed.Add(1)
	return nil
}

type panickingConnector struct{}

func (*panickingConnector) Execute(
	context.Context,
	json.RawMessage,
	map[string]connectortypes.TypedParam,
	...connectortypes.ExecutionLimits,
) (*result.ExecutionResult, error) {
	panic("execute failed")
}

func (*panickingConnector) TestConnection(context.Context) error { return nil }
func (*panickingConnector) Close() error                         { return nil }

func TestProcessConnectionTestStaysOutsideRuntimePool(t *testing.T) {
	t.Parallel()

	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	payload, err := json.Marshal(datasourcessvc.ConnectionTestRequest{
		OrganizationSlug: slug.Slug("acme"), DataSourceType: model.DataSourceTypePostgres,
	})
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt(payload)
	require.NoError(t, err)
	id := uuid.New()
	closed := &atomic.Int32{}
	factory := func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (connector.Connector, error) {
		return &freshConnectionTestConnector{closed: closed}, nil
	}
	runtimePool, err := connector.NewManager(factory, connector.RuntimePoolSettings{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtimePool.Close()) })
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{},
		repo: &fakeRequestRepo{request: requestrepo.Request{
			ID: id, State: requestrepo.StateRunning, Kind: kindConnectionTest,
			RequestCiphertext: encrypted, DeadlineAt: time.Now().Add(time.Minute),
		}},
		cipher: cipher, factory: factory, runtimePool: runtimePool,
		heartbeatEvery: time.Hour,
	}

	processResult, err := broker.Process(t.Context(), id)

	require.NoError(t, err)
	require.Equal(t, executiondelivery.ProcessDone, processResult.Disposition)
	require.EqualValues(t, 1, closed.Load())
	require.Zero(t, runtimePool.Snapshot().Entries)
}

func TestProcessQueryPreservesOnlyPublicDetail(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		fromFactory bool
		detail      string
		outcome     *xerror.Error
		code        string
	}{
		{name: "missing column or relation", outcome: xerror.SourceSchemaReferenceInvalid(), code: errorCodeSchemaReferenceInvalid},
		{name: "invalid query", outcome: xerror.SourceQueryInvalid(), code: errorCodeQueryInvalid},
		{name: "permission result", fromFactory: false, outcome: xerror.Forbidden(xerror.DataSource), code: errorCodeDataSourcePermission},
		{name: "permission factory", fromFactory: true, outcome: xerror.Forbidden(xerror.DataSource), code: errorCodeDataSourcePermission},
		{name: "authentication result", fromFactory: false, outcome: xerror.InvalidCredentials(), code: errorCodeDataSourceCredentials},
		{name: "authentication factory", fromFactory: true, outcome: xerror.InvalidCredentials(), code: errorCodeDataSourceCredentials},
		{name: "connection result", fromFactory: false, outcome: xerror.ConnectionFailed(xerror.DataSource), code: errorCodeDataSourceConnection},
		{name: "connection factory", fromFactory: true, outcome: xerror.ConnectionFailed(xerror.DataSource), code: errorCodeDataSourceConnection},
		{name: "timeout result", fromFactory: false, outcome: xerror.Timeout(xerror.DataSource), code: errorCodeDataSourceTimeout},
		{name: "timeout factory", fromFactory: true, outcome: xerror.Timeout(xerror.DataSource), code: errorCodeDataSourceTimeout},
		{name: "result safe detail", detail: "Headers must be nonblank unique strings"},
		{name: "factory safe detail", fromFactory: true, detail: "Headers must be nonblank unique strings"},
		{name: "result private cause"},
		{name: "factory private cause", fromFactory: true},
		{name: "result acknowledgement rejected", outcome: xerror.HTTPAcknowledgementRejected(), code: errorCodeAcknowledgementRejected, detail: "Verify remote state before retrying."},
		{name: "factory acknowledgement rejected", fromFactory: true, outcome: xerror.HTTPAcknowledgementRejected(), code: errorCodeAcknowledgementRejected, detail: "Verify remote state before retrying."},
		{name: "result acknowledgement uncertain", outcome: xerror.HTTPAcknowledgementUncertain(), code: errorCodeAcknowledgementUncertain, detail: "Verify remote state before retrying."},
		{name: "factory acknowledgement uncertain", fromFactory: true, outcome: xerror.HTTPAcknowledgementUncertain(), code: errorCodeAcknowledgementUncertain, detail: "Verify remote state before retrying."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cipher, err := crypto.NewAES256(make([]byte, 32))
			require.NoError(t, err)
			payload, err := marshalRequestPlaintext(kindQuery, queriessvc.ConnectorExecutionRequest{
				OrganizationSlug: slug.Slug("acme"), DataSourceSlug: slug.Slug("source"),
				DataSourceType: model.DataSourceTypeGoogleWorkspace,
				ConfigVersion:  "config-v1", CredentialVersion: "credential-v1",
				CredentialReference: "secret/2026-10-18T10:00:00Z",
				Limits:              connectortypes.ExecutionLimits{ReadOnly: true, MaxResponseBytes: 1024},
			})
			require.NoError(t, err)
			privateCause := "private-provider-cell-content"
			queryErr := tc.outcome
			if queryErr == nil {
				queryErr = xerror.ValidationFailed()
			}
			queryErr = queryErr.Detail(privateCause).WithPublicDetail(tc.detail)
			repo := &fakeRequestRepo{}
			broker := &Broker{
				admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour,
				factory: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
					if tc.fromFactory {
						return nil, xerror.Wrapf("connector setup: %w", queryErr)
					}
					return &fixedResultConnector{result: result.NewSQLResult("synthetic", nil, 0, xerror.Wrapf("query execution: %w", queryErr))}, nil
				},
			}
			configureTestProcessor(t, broker)
			require.NoError(t, broker.processClaimed(t.Context(), requestrepo.Request{
				ID: uuid.New(), Kind: kindQuery, RequestCiphertext: mustEncrypt(t, cipher, payload),
				DeadlineAt: time.Now().Add(time.Minute),
			}))
			plaintext, err := cipher.Decrypt(repo.request.ResultCiphertext)
			require.NoError(t, err)
			require.NotContains(t, string(plaintext), privateCause)
			response, err := decodeResponsePlaintext(plaintext)
			require.NoError(t, err)
			code := tc.code
			if code == "" {
				code = errorCodeQueryFailed
			}
			require.Equal(t, code, response.ErrorCode)
			if tc.outcome != nil {
				require.ErrorIs(t, responseError(response), tc.outcome)
			}
			require.Nil(t, response.Result)
			require.Equal(t, tc.detail, response.PublicDetail)
			require.Equal(t, tc.detail, xerror.PublicDetail(responseError(response)))
			require.NotContains(t, responseError(response).Error(), privateCause)
		})
	}
}

func TestProcessConnectionTestPreservesPublicDetail(t *testing.T) {
	t.Parallel()

	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	payload, err := json.Marshal(datasourcessvc.ConnectionTestRequest{
		OrganizationSlug: slug.Slug("acme"), DataSourceType: model.DataSourceTypeGitlab,
	})
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt(payload)
	require.NoError(t, err)
	repo := &fakeRequestRepo{}
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour,
		logger: slog.New(slog.DiscardHandler),
		factory: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
			return nil, xerror.Unavailable(xerror.RepoFile).
				WithPublicDetail("git ls-remote failed: repository not found").
				Detail("repository not found")
		},
	}
	configureTestProcessor(t, broker)
	require.NoError(t, broker.processClaimed(t.Context(), requestrepo.Request{
		ID: uuid.New(), Kind: kindConnectionTest, RequestCiphertext: encrypted,
		DeadlineAt: time.Now().Add(time.Minute),
	}))

	plaintext, err := cipher.Decrypt(repo.request.ResultCiphertext)
	require.NoError(t, err)
	response, err := decodeResponsePlaintext(plaintext)
	require.NoError(t, err)
	require.Equal(t, "git ls-remote failed: repository not found", response.PublicDetail)
	require.Equal(t, response.PublicDetail, xerror.PublicDetail(responseError(response)))
}

func TestProcessConnectionTestPreservesTypedDiagnosticWithoutProviderText(t *testing.T) {
	t.Parallel()
	for _, dsType := range []model.DataSourceType{model.DataSourceTypePostgres, model.DataSourceTypeS3, model.DataSourceTypeBigQuery, model.DataSourceTypeHubSpot} {
		t.Run(dsType.String(), func(t *testing.T) {
			cipher, err := crypto.NewAES256(make([]byte, 32))
			require.NoError(t, err)
			payload, err := json.Marshal(datasourcessvc.ConnectionTestRequest{OrganizationSlug: "acme", DataSourceType: dsType})
			require.NoError(t, err)
			repo := &fakeRequestRepo{}
			broker := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour, logger: slog.New(slog.DiscardHandler), factory: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
				return nil, xerror.InvalidCredentials().Detail("private-provider-password")
			}}
			configureTestProcessor(t, broker)
			require.NoError(t, broker.processClaimed(t.Context(), requestrepo.Request{ID: uuid.New(), Kind: kindConnectionTest, RequestCiphertext: mustEncrypt(t, cipher, payload), DeadlineAt: time.Now().Add(time.Minute)}))
			plaintext, err := cipher.Decrypt(repo.request.ResultCiphertext)
			require.NoError(t, err)
			require.NotContains(t, string(plaintext), "private-provider-password")
			response, err := decodeResponsePlaintext(plaintext)
			require.NoError(t, err)
			require.Contains(t, xerror.PublicDetail(responseError(response)), "Authentication failed.")
		})
	}
}

func TestProcessConnectionTestLogsCorrelatedManagedGitFailure(t *testing.T) {
	t.Parallel()

	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	payload, err := json.Marshal(datasourcessvc.ConnectionTestRequest{
		OrganizationSlug: slug.Slug("acme"), DataSourceType: model.DataSourceTypeGitlab,
	})
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt(payload)
	require.NoError(t, err)
	requestID := uuid.New()
	var logs bytes.Buffer
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: &fakeRequestRepo{}, cipher: cipher, heartbeatEvery: time.Hour,
		logger: slog.New(slog.NewJSONHandler(&logs, nil)),
		factory: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
			return nil, xerror.Unexpected().Detail(
				"git ls-remote failed: fatal: unable to access " +
					"'https://oauth2:supersecret@gitlab.com/acme/repo.git'",
			)
		},
	}

	require.NoError(t, broker.processClaimed(t.Context(), requestrepo.Request{
		ID: requestID, Kind: kindConnectionTest, RequestCiphertext: encrypted,
		DeadlineAt: time.Now().Add(time.Minute),
	}))

	var entry map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry))
	require.Equal(t, "execution.connection_test.worker_failed", entry["braidplane.event"])
	require.Equal(t, requestID.String(), entry["execution_request_id"])
	require.Equal(t, "gitlab", entry["data_source.type"])
	require.Contains(t, entry["error"], "https://[REDACTED]@gitlab.com/acme/repo.git")
	require.NotContains(t, logs.String(), "supersecret")
}

func TestGitProposalCreationUsesDistinctExecutionKind(t *testing.T) {
	t.Parallel()
	for _, createOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(createOnly), func(t *testing.T) {
			cipher, err := crypto.NewAES256(make([]byte, 32))
			require.NoError(t, err)
			repo := &fakeRequestRepo{}
			dispatchErr := xerror.ExecutionUnavailable()
			broker := &Broker{
				admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher,
				dispatcher: &recordingDispatcher{err: dispatchErr}, requestTimeout: testRequestTimeout,
			}
			_, err = broker.ExecuteGitProposal(t.Context(), pipelinegit.RemoteProposalRequest{
				Operation: pipelinegit.ProposalOperation{OrganizationSlug: "acme", CreateOnly: createOnly},
			})
			require.ErrorIs(t, err, dispatchErr)
			want := kindGitProposal
			if createOnly {
				want = kindGitCreationProposal
			}
			require.Equal(t, want, repo.created.Kind)
		})
	}
}

func TestGitProposalRejectsMismatchedCreationKindBeforeExecution(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{kindGitProposal, kindGitCreationProposal} {
		t.Run(kind, func(t *testing.T) {
			cipher, err := crypto.NewAES256(make([]byte, 32))
			require.NoError(t, err)
			payload, err := json.Marshal(pipelinegit.RemoteProposalRequest{
				Operation: pipelinegit.ProposalOperation{CreateOnly: kind == kindGitProposal},
			})
			require.NoError(t, err)
			encrypted, err := cipher.Encrypt(payload)
			require.NoError(t, err)
			repo := &fakeRequestRepo{}
			broker := &Broker{
				admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour,
				logger: slog.New(slog.DiscardHandler),
			}
			require.NoError(t, broker.processClaimed(t.Context(), requestrepo.Request{
				ID: uuid.New(), Kind: kind, RequestCiphertext: encrypted, DeadlineAt: time.Now().Add(time.Minute),
			}))
			plaintext, err := cipher.Decrypt(repo.request.ResultCiphertext)
			require.NoError(t, err)
			response, err := decodeResponsePlaintext(plaintext)
			require.NoError(t, err)
			require.Equal(t, errorCodeInvalidRequest, response.ErrorCode)
		})
	}
	require.ErrorIs(t, responseError(encryptedResponse{ErrorCode: errorCodeCreationTargetExists}), pipelinegit.ErrCreationTargetExists)
}

func TestProcessGitProposalLogsCorrelatedSanitizedFailure(t *testing.T) {
	t.Parallel()

	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	payload, err := json.Marshal(pipelinegit.RemoteProposalRequest{Auth: datasourcessvc.GitExecutionAuth{
		DataSourceType:  model.DataSourceTypeGithub,
		ConfigJSON:      []byte(`{"repo_url":"https://oauth2:supersecret@github.com/acme/repo%zz"}`),
		CredentialsJSON: []byte(`{"auth_type":"https","token":"synthetic-installation-token"}`),
	}})
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt(payload)
	require.NoError(t, err)
	requestID := uuid.New()
	var logs bytes.Buffer
	repo := &fakeRequestRepo{}
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour,
		logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	}

	require.NoError(t, broker.processClaimed(t.Context(), requestrepo.Request{
		ID: requestID, Kind: kindGitProposal, RequestCiphertext: encrypted,
		DeadlineAt: time.Now().Add(time.Minute),
	}))

	var entry map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry))
	require.Equal(t, "execution.git_proposal.worker_failed", entry["braidplane.event"])
	require.Equal(t, requestID.String(), entry["execution_request_id"])
	require.Equal(t, "github", entry["data_source.type"])
	require.Contains(t, entry["error"], "https://[REDACTED]@github.com/acme/repo%zz")
	require.NotContains(t, logs.String(), "supersecret")
	require.NotContains(t, logs.String(), "synthetic-installation-token")
	plaintext, err := cipher.Decrypt(repo.request.ResultCiphertext)
	require.NoError(t, err)
	response, err := decodeResponsePlaintext(plaintext)
	require.NoError(t, err)
	require.Equal(t, errorCodeGitFailed, response.ErrorCode)
	require.Empty(t, response.PublicDetail)
}

func TestProcessChecksCancellationBeforeProviderWork(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"CancelledAfterClaim", "CancellationCheckFailed"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			cipher, err := crypto.NewAES256(make([]byte, 32))
			require.NoError(t, err)
			payload, err := json.Marshal(datasourcessvc.ConnectionTestRequest{
				OrganizationSlug: slug.Slug("acme"), DataSourceType: model.DataSourceTypePostgres,
			})
			require.NoError(t, err)
			encrypted, err := cipher.Encrypt(payload)
			require.NoError(t, err)
			id := uuid.New()
			repo := &fakeRequestRepo{request: requestrepo.Request{
				ID: id, State: requestrepo.StateRunning, Kind: kindConnectionTest,
				RequestCiphertext: encrypted, DeadlineAt: time.Now().Add(time.Minute),
			}}
			if scenario == "CancelledAfterClaim" {
				repo.heartbeatCancelled = true
			} else {
				repo.heartbeatErr = xerror.ExecutionUnavailable().Detail("test cancellation lookup failure")
			}
			var providerCalls atomic.Int32
			b := &Broker{
				admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour,
				factory: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
					providerCalls.Add(1)
					return nil, xerror.Unexpected().Detail("provider must not be reached")
				},
			}
			configureTestProcessor(t, b)
			result, err := b.Process(t.Context(), id)
			if scenario == "CancelledAfterClaim" {
				require.NoError(t, err)
				require.Equal(t, executiondelivery.ProcessDone, result.Disposition)
				require.EqualValues(t, 1, repo.completeCalls.Load())
			} else {
				require.ErrorIs(t, err, repo.heartbeatErr)
				require.Zero(t, repo.completeCalls.Load())
			}
			require.EqualValues(t, 1, repo.heartbeatCalls.Load())
			require.Zero(t, providerCalls.Load())
		})
	}
}

func TestProcessHeartbeatCancelsInFlightConnector(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	payload, err := json.Marshal(datasourcessvc.ConnectionTestRequest{
		OrganizationSlug: slug.Slug("acme"), DataSourceType: model.DataSourceTypePostgres,
	})
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt(payload)
	require.NoError(t, err)
	id := uuid.New()
	repo := &fakeRequestRepo{request: requestrepo.Request{
		ID: id, State: requestrepo.StateRunning, Kind: kindConnectionTest,
		RequestCiphertext: encrypted, DeadlineAt: time.Now().Add(time.Minute),
	}, heartbeatCancelAfter: 1}
	conn := &blockingConnector{}
	b := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Millisecond,
		factory: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
			return conn, nil
		},
	}
	configureTestProcessor(t, b)

	processResult, err := b.Process(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, executiondelivery.ProcessDone, processResult.Disposition)
	require.True(t, conn.cancelled.Load(), "durable cancellation must reach the connector context")
	require.EqualValues(t, 1, repo.completeCalls.Load())
}

func TestProcessDoesNotReplayDurablyAmbiguousRequest(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	called := atomic.Bool{}
	b := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: &fakeRequestRepo{request: requestrepo.Request{ID: id, State: requestrepo.StateAmbiguous}},
		factory: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
			called.Store(true)
			return nil, xerror.Unexpected().Detail("connector creation must not be reached")
		},
	}
	configureTestProcessor(t, b)
	processResult, err := b.Process(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, executiondelivery.ProcessDone, processResult.Disposition)
	require.False(t, called.Load(), "ambiguous external side effects must never be replayed")
}

func TestStartFailsClosedWhenExecutorCapabilitiesAreUnavailable(t *testing.T) {
	t.Parallel()
	expected := xerror.Unexpected().Detail("missing claim capability")
	b := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: &fakeRequestRepo{preflightErr: expected},
		factory: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
			return nil, xerror.Unexpected().Detail("connector creation must not be reached")
		},
	}
	require.ErrorIs(t, b.Start(context.Background(), 1), expected)
}

func TestSubmitCancellationForSideEffectingWorkIsAmbiguous(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	b := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: &fakeRequestRepo{}, cipher: cipher,
		dispatcher: NewPollerDispatcher(), requestTimeout: testRequestTimeout,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = b.submitAndWait(ctx, "acme", kindQuery, queriessvc.ConnectorExecutionRequest{})
	require.ErrorIs(t, err, ErrAmbiguousExecution)

	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, err = b.submitAndWait(ctx, "acme", kindTransform, struct{}{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestPollingCancellationStillPersistsCancellation(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	repo := &fakeRequestRepo{findHook: func(ctx context.Context) error { cancel(); return ctx.Err() }}
	broker := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, dispatcher: NewPollerDispatcher(), requestTimeout: testRequestTimeout}
	_, err = broker.submitAndWait(ctx, "acme", kindQuery, queriessvc.ConnectorExecutionRequest{})
	require.EqualValues(t, 1, repo.cancelCalls.Load(), "cancellation during response polling must reach the executor")
	require.ErrorIs(t, err, ErrAmbiguousExecution, "started writes retain their uncertain-outcome classification")
}

func TestQueuedQueryCancellationIsNotAmbiguous(t *testing.T) {
	t.Parallel()

	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	b := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: &fakeRequestRepo{cancelBeforeExecution: true}, cipher: cipher,
		dispatcher: NewPollerDispatcher(), requestTimeout: testRequestTimeout,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = b.submitAndWait(ctx, "acme", kindQuery, queriessvc.ConnectorExecutionRequest{
		DataSourceSlug:          slug.Slug("warehouse"),
		ConcurrencyLimit:        1,
		ConcurrencyQueueLimit:   1,
		ConcurrencyQueueTimeout: time.Second,
	})

	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrAmbiguousExecution)
}

func TestQueryCancellationFailureIsAmbiguous(t *testing.T) {
	t.Parallel()

	cancelErr := xerror.Unexpected().Detail("cancel persistence failed")
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	b := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: &fakeRequestRepo{
			cancelBeforeExecution: true,
			cancelErr:             cancelErr,
		}, cipher: cipher,
		dispatcher: NewPollerDispatcher(), requestTimeout: testRequestTimeout,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = b.submitAndWait(ctx, "acme", kindQuery, queriessvc.ConnectorExecutionRequest{
		DataSourceSlug:          slug.Slug("warehouse"),
		ConcurrencyLimit:        1,
		ConcurrencyQueueLimit:   1,
		ConcurrencyQueueTimeout: time.Second,
	})

	require.ErrorIs(t, err, ErrAmbiguousExecution)
	require.ErrorIs(t, err, cancelErr)
}

func TestPureExecutionCancellationFailureIsReturned(t *testing.T) {
	t.Parallel()

	cancelErr := xerror.Unexpected().Detail("cancel persistence failed")
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	b := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: &fakeRequestRepo{cancelErr: cancelErr}, cipher: cipher,
		dispatcher: NewPollerDispatcher(), requestTimeout: testRequestTimeout,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = b.submitAndWait(ctx, "acme", kindTransform, struct{}{})

	require.ErrorIs(t, err, cancelErr)
	require.NotErrorIs(t, err, ErrAmbiguousExecution)
}

func TestExecuteConnectorReleasesLeaseAfterPanic(t *testing.T) {
	t.Parallel()

	runtimePool, err := connector.NewManager(
		func(
			context.Context,
			model.DataSourceType,
			[]byte,
			[]byte,
		) (connector.Connector, error) {
			return &panickingConnector{}, nil
		},
		connector.RuntimePoolSettings{},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtimePool.Close()) })
	b := &Broker{admission: testutil.FakeOperationAdmission{}, runtimePool: runtimePool, txm: fakeTXM{}, repo: &fakeRequestRepo{}}

	require.Panics(t, func() {
		_, _ = b.executeConnector(t.Context(), uuid.New(), uuid.New(), queriessvc.ConnectorExecutionRequest{
			OrganizationSlug:    "acme",
			DataSourceSlug:      "warehouse",
			DataSourceType:      model.DataSourceTypePostgres,
			ConfigVersion:       "config-v1",
			CredentialVersion:   "credential-v1",
			CredentialReference: "secret/2026-10-18T10:00:00Z",
		})
	})
	require.Zero(t, runtimePool.Snapshot().ActiveLeases)
}

func TestExecuteConnectorRecordsAcquisitionFailure(t *testing.T) { // Process metrics are global.
	meter, err := metrics.New(metrics.Config{
		Enabled: true, Path: "/metrics", ServiceName: "executor", Environment: "test",
	})
	require.NoError(t, err)
	restore := metrics.SetProcessMeter(meter)
	t.Cleanup(restore)

	acquireErr := xerror.ExecutionUnavailable().Detail("connector creation failed")
	runtimePool, err := connector.NewManager(
		func(
			context.Context,
			model.DataSourceType,
			[]byte,
			[]byte,
		) (connector.Connector, error) {
			return nil, acquireErr
		},
		connector.RuntimePoolSettings{},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtimePool.Close()) })
	b := &Broker{admission: testutil.FakeOperationAdmission{}, runtimePool: runtimePool, txm: fakeTXM{}, repo: &fakeRequestRepo{}}

	_, err = b.executeConnector(t.Context(), uuid.New(), uuid.New(), queriessvc.ConnectorExecutionRequest{
		OrganizationSlug:    "acme",
		DataSourceSlug:      "warehouse",
		DataSourceType:      model.DataSourceTypePostgres,
		ConfigVersion:       "config-v1",
		CredentialVersion:   "credential-v1",
		CredentialReference: "secret/2026-10-18T10:00:00Z",
	})
	require.ErrorIs(t, err, acquireErr)

	app := fiber.New()
	meter.RegisterRoutes(app)
	response, err := app.Test(httptest.NewRequestWithContext(
		context.Background(), http.MethodGet, "/metrics", nil,
	))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `braidplane_connector_admission_total{deployment_environment="test",outcome="error",service_name="executor"} 1`)
}

func TestTestConnectionPersistsScopedConnectionRequest(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	repo := &fakeRequestRepo{}
	dispatcher := &recordingDispatcher{}
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher,
		dispatcher: dispatcher, requestTimeout: testRequestTimeout,
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err = broker.TestConnection(ctx, datasourcessvc.ConnectionTestRequest{OrganizationSlug: slug.Slug("acme")})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "acme", repo.created.OrganizationSlug)
	require.Equal(t, kindConnectionTest, repo.created.Kind)
	require.NotEmpty(t, repo.created.RequestCiphertext)
	require.EqualValues(t, 1, dispatcher.calls.Load())
	require.Equal(t, repo.created.ID, dispatcher.requestID)
}

func TestTestConnectionFailureLogBridgesHTTPTraceToExecutionRequest(t *testing.T) {
	t.Parallel()

	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	responseJSON, err := json.Marshal(encryptedResponse{ErrorCode: "connection_failed"})
	require.NoError(t, err)
	responseCiphertext, err := cipher.Encrypt(responseJSON)
	require.NoError(t, err)
	repo := &fakeRequestRepo{request: requestrepo.Request{
		State: requestrepo.StateFailed, ResultCiphertext: responseCiphertext,
	}}
	var logs bytes.Buffer
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher,
		dispatcher: NewPollerDispatcher(), requestTimeout: testRequestTimeout,
		logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	}
	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, Remote: true,
	}))
	ctx = reqctx.WithRequestID(ctx, "http-request-123")

	err = broker.TestConnection(ctx, datasourcessvc.ConnectionTestRequest{
		OrganizationSlug: slug.Slug("acme"),
	})

	require.ErrorIs(t, err, datasourcessvc.ErrConnectionFailed)
	require.False(t, executiondelivery.IsNotExecuted(err))
	var entry map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry))
	require.Equal(t, "execution.connection_test.request_failed", entry["braidplane.event"])
	require.Equal(t, repo.created.ID.String(), entry["execution_request_id"])
	require.Equal(t, "http-request-123", entry["request_id"])
	require.Equal(t, traceID.String(), entry["trace_id"])
}

func TestConnectionAdmissionFailureIsMarkedNotExecuted(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	responseJSON, err := json.Marshal(encryptedResponse{ErrorCode: "fixture_admission_denied"})
	require.NoError(t, err)
	responseCiphertext, err := cipher.Encrypt(responseJSON)
	require.NoError(t, err)
	repo := &fakeRequestRepo{request: requestrepo.Request{State: requestrepo.StateFailed, ResultCiphertext: responseCiphertext}}
	admissionError := xerror.Unavailable(xerror.DataSource)
	guard := testutil.FakeOperationAdmission{
		EncodeOperationErrorFn: func(err error) (string, bool) { return "fixture_admission_denied", xerror.Is(err, admissionError) },
		DecodeOperationErrorFn: func(code string) (error, bool) { return admissionError, code == "fixture_admission_denied" },
	}
	broker := &Broker{admission: guard, txm: fakeTXM{}, repo: repo, cipher: cipher, dispatcher: NewPollerDispatcher(), requestTimeout: testRequestTimeout, logger: slog.New(slog.DiscardHandler)}
	err = broker.TestConnection(t.Context(), datasourcessvc.ConnectionTestRequest{OrganizationSlug: "acme", DataSourceSlug: "source"})
	require.ErrorIs(t, err, admissionError)
	require.True(t, executiondelivery.IsNotExecuted(err))
}

func TestSubmitAppliesConfiguredTimeoutToActualWait(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	repo := &fakeRequestRepo{request: requestrepo.Request{State: requestrepo.StatePending}}
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher,
		dispatcher: NewPollerDispatcher(), requestTimeout: 5 * time.Millisecond,
	}

	startedAt := time.Now()
	_, err = broker.submitAndWait(t.Context(), "acme", kindConnectionTest, struct{}{})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(startedAt), time.Second)
	require.WithinDuration(t, startedAt.Add(5*time.Millisecond), repo.created.DeadlineAt, 100*time.Millisecond)
}

func TestProcessReturnsRetryAtForDuplicateLiveLease(t *testing.T) {
	t.Parallel()
	retryAt := time.Now().Add(time.Minute)
	notClaimed := false
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{},
		repo: &fakeRequestRepo{
			claimFound: &notClaimed,
			deliverySnapshot: requestrepo.DeliverySnapshot{
				Request: requestrepo.Request{
					ID:             uuid.New(),
					State:          requestrepo.StateRunning,
					LeaseExpiresAt: &retryAt,
				},
				ObservedAt: time.Now(),
			},
		},
	}
	configureTestProcessor(t, broker)

	result, err := broker.Process(t.Context(), uuid.New())

	require.NoError(t, err)
	require.Equal(t, executiondelivery.ProcessRetryAt, result.Disposition)
	require.Equal(t, retryAt, result.RetryAt)
}

func TestProcessReturnsRetryAtForQueuedAdmission(t *testing.T) {
	t.Parallel()
	observedAt := time.Now()
	deadlineAt := observedAt.Add(time.Minute)
	notClaimed := false
	requestID := uuid.New()
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{},
		repo: &fakeRequestRepo{
			claimFound: &notClaimed,
			deliverySnapshot: requestrepo.DeliverySnapshot{
				Request: requestrepo.Request{
					ID:         requestID,
					State:      requestrepo.StatePending,
					DeadlineAt: deadlineAt,
				},
				CreatedAt:  observedAt.Add(-50 * time.Millisecond),
				ObservedAt: observedAt,
			},
		},
	}
	configureTestProcessor(t, broker)

	result, err := broker.Process(t.Context(), requestID)

	require.NoError(t, err)
	require.Equal(t, executiondelivery.ProcessRetryAt, result.Disposition)
	require.Equal(t, observedAt.Add(admissionRetryInterval), result.RetryAt)
}

func TestPendingAdmissionRetryAtBacksOffAndClamps(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	deadlineAt := observedAt.Add(10 * time.Second)
	queueDeadlineAt := observedAt.Add(50 * time.Millisecond)
	tests := []struct {
		name          string
		age           time.Duration
		deadlineAt    time.Time
		queueDeadline *time.Time
		want          time.Time
	}{
		{
			name:       "young request uses initial delay",
			age:        50 * time.Millisecond,
			deadlineAt: deadlineAt,
			want:       observedAt.Add(100 * time.Millisecond),
		},
		{
			name:       "older request increases delay exponentially",
			age:        250 * time.Millisecond,
			deadlineAt: deadlineAt,
			want:       observedAt.Add(400 * time.Millisecond),
		},
		{
			name:       "old request uses capped delay",
			age:        2 * time.Second,
			deadlineAt: deadlineAt,
			want:       observedAt.Add(time.Second),
		},
		{
			name:       "request deadline clamps delay",
			age:        50 * time.Millisecond,
			deadlineAt: observedAt.Add(75 * time.Millisecond),
			want:       observedAt.Add(75 * time.Millisecond),
		},
		{
			name:          "queue deadline clamps delay",
			age:           50 * time.Millisecond,
			deadlineAt:    deadlineAt,
			queueDeadline: &queueDeadlineAt,
			want:          queueDeadlineAt,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := pendingAdmissionRetryAt(requestrepo.DeliverySnapshot{
				Request: requestrepo.Request{
					DeadlineAt:      testCase.deadlineAt,
					QueueDeadlineAt: testCase.queueDeadline,
				},
				CreatedAt:  observedAt.Add(-testCase.age),
				ObservedAt: observedAt,
			})

			require.Equal(t, testCase.want, got)
		})
	}
}

func TestProcessRejectsPendingAdmissionPastDeadline(t *testing.T) {
	t.Parallel()
	observedAt := time.Now()
	notClaimed := false
	requestID := uuid.New()
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{},
		repo: &fakeRequestRepo{
			claimFound: &notClaimed,
			deliverySnapshot: requestrepo.DeliverySnapshot{
				Request: requestrepo.Request{
					ID:         requestID,
					State:      requestrepo.StatePending,
					DeadlineAt: observedAt,
				},
				ObservedAt: observedAt,
			},
		},
	}
	configureTestProcessor(t, broker)

	_, err := broker.Process(t.Context(), requestID)

	require.ErrorContains(t, err, "was not terminalized")
}

func TestSubmitPersistsQueryAdmissionMetadata(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	repo := &fakeRequestRepo{}
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher,
		dispatcher: NewPollerDispatcher(), requestTimeout: 5 * time.Millisecond,
	}

	_, err = broker.submitAndWait(t.Context(), "acme", kindQuery, queriessvc.ConnectorExecutionRequest{
		DataSourceSlug:          slug.Slug("warehouse"),
		ConcurrencyLimit:        2,
		ConcurrencyQueueLimit:   5,
		ConcurrencyQueueTimeout: time.Second,
	})

	require.ErrorIs(t, err, ErrAmbiguousExecution)
	require.Equal(t, "warehouse", repo.created.DataSourceSlug)
	require.Equal(t, 2, repo.created.ConcurrencyLimit)
	require.Equal(t, 5, repo.created.QueueCapacity)
	require.NotNil(t, repo.created.QueueDeadlineAt)
}

func TestSubmitRejectsUnexpectedQueryPayloadTypes(t *testing.T) {
	t.Parallel()

	request := queriessvc.ConnectorExecutionRequest{}
	tests := []struct {
		name    string
		payload any
	}{
		{name: "pointer", payload: &request},
		{
			name: "wrapper",
			payload: struct {
				Request queriessvc.ConnectorExecutionRequest `json:"request"`
			}{Request: request},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cipher, err := crypto.NewAES256(make([]byte, 32))
			require.NoError(t, err)
			repo := &fakeRequestRepo{}
			dispatcher := &recordingDispatcher{}
			broker := &Broker{
				admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher,
				dispatcher: dispatcher, requestTimeout: testRequestTimeout,
			}

			_, err = broker.submitAndWait(t.Context(), "acme", kindQuery, tt.payload)

			require.True(t, xerror.Is(err, xerror.Unexpected()), "got %v", err)
			require.ErrorContains(t, err, "query execution payload has unexpected type")
			require.EqualValues(t, 0, dispatcher.calls.Load())
			require.Equal(t, uuid.Nil, repo.created.ID)
		})
	}
}

func TestSubmitRejectsOversizedRuntimeByteParametersBeforeDispatch(t *testing.T) {
	t.Parallel()

	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	repo := &fakeRequestRepo{}
	dispatcher := &recordingDispatcher{}
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher,
		dispatcher: dispatcher, requestTimeout: testRequestTimeout,
	}

	_, err = broker.submitAndWait(
		t.Context(),
		"acme",
		kindQuery,
		queriessvc.ConnectorExecutionRequest{
			Parameters: map[string]connectortypes.TypedParam{
				"input": {Value: []byte{0x01, 0x02}, DataType: "text"},
			},
			Limits: connectortypes.ExecutionLimits{MaxResponseBytes: 1},
		},
	)

	require.True(t, xerror.Is(err, xerror.ExecutionRequestSizeExceeded()), "got %v", err)
	require.EqualValues(t, 0, dispatcher.calls.Load())
	require.Equal(t, uuid.Nil, repo.created.ID)
}

func TestSubmitBindsAccountBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"active", "removed", "system"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			cipher, err := crypto.NewAES256(make([]byte, 32))
			require.NoError(t, err)
			dispatchErr := xerror.Unexpected().Detail("dispatch test stop")
			repo := &fakeRequestRepo{}
			dispatcher := &recordingDispatcher{err: dispatchErr}
			ctx := t.Context()
			if scenario != "system" {
				ctx = executiondelivery.WithInitiatingUser(ctx, slug.Slug("alice"))
			}
			if scenario == "removed" {
				repo.bindErr = executiondelivery.ErrAccountUnavailable
			}
			broker := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, dispatcher: dispatcher, requestTimeout: testRequestTimeout}
			_, err = broker.submitAndWait(ctx, "acme", kindTransform, struct{}{})
			if scenario == "removed" {
				require.ErrorIs(t, err, repo.bindErr)
				require.Zero(t, dispatcher.calls.Load())
			} else {
				require.ErrorIs(t, err, dispatchErr)
				require.EqualValues(t, 1, dispatcher.calls.Load())
			}
			if scenario == "system" {
				require.Empty(t, repo.boundUser)
			} else {
				require.Equal(t, slug.Slug("alice"), repo.boundUser)
			}
		})
	}
}

func TestReadResponseMapsDurableTerminalStates(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	succeeded, err := cipher.Encrypt([]byte(`{}`))
	require.NoError(t, err)
	expiredQueueDeadline := time.Now().Add(-time.Second)

	tests := []struct {
		name    string
		request requestrepo.Request
		done    bool
		wantErr error
	}{
		{name: "pending", request: requestrepo.Request{State: requestrepo.StatePending}},
		{name: "cancelled", request: requestrepo.Request{State: requestrepo.StateCancelled}, done: true, wantErr: context.Canceled},
		{
			name: "queue timeout",
			request: requestrepo.Request{
				State:           requestrepo.StateCancelled,
				QueueDeadlineAt: &expiredQueueDeadline,
			},
			done:    true,
			wantErr: xerror.ExecutionConcurrencyLimitExceeded(),
		},
		{name: "ambiguous", request: requestrepo.Request{State: requestrepo.StateAmbiguous}, done: true, wantErr: ErrAmbiguousExecution},
		{name: "succeeded", request: requestrepo.Request{State: requestrepo.StateSucceeded, ResultCiphertext: succeeded}, done: true},
		{name: "invalid", request: requestrepo.Request{State: "unknown"}, done: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &fakeRequestRepo{request: tt.request}
			broker := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher}
			_, done, err := broker.readResponse(t.Context(), uuid.New(), "acme")
			require.Equal(t, tt.done, done)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			if tt.name == "invalid" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestReadResponseRejectsUnreadableDurableResults(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)

	for _, request := range []requestrepo.Request{
		{State: requestrepo.StateSucceeded, ResultCiphertext: []byte("not-ciphertext")},
		{State: requestrepo.StateFailed, ResultCiphertext: mustEncrypt(t, cipher, []byte("not-json"))},
	} {
		broker := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: &fakeRequestRepo{request: request}, cipher: cipher}
		_, done, readErr := broker.readResponse(t.Context(), uuid.New(), "acme")
		require.True(t, done)
		require.Error(t, readErr)
	}
}

func TestResponsePlaintextRoundTripPreservesRuntimeOnlyFileBytes(t *testing.T) {
	t.Parallel()

	content := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}
	fileMetadata := filepayload.NewFilePayloadMetadata(
		"earthrise.jpg",
		"image/jpeg",
		content,
	)
	tests := []struct {
		name   string
		result *result.ExecutionResult
		assert func(*testing.T, *result.ExecutionResult)
	}{
		{
			name: "HTTP file response",
			result: result.NewHTTPResult(&result.HTTPResult{
				StatusCode: http.StatusOK,
				Headers:    map[string]string{"Content-Type": "image/jpeg"},
				File:       &fileMetadata,
				RawBody:    content,
			}, 11, nil),
			assert: func(t *testing.T, executionResult *result.ExecutionResult) {
				t.Helper()
				httpResult, ok := executionResult.HTTP()
				require.True(t, ok)
				require.Equal(t, content, httpResult.RawBody)
			},
		},
		{
			name: "S3 file response",
			result: result.NewS3Result(&result.FileResult{
				Format:     "file",
				Operation:  "read",
				TargetPath: "earthrise.jpg",
				File:       &fileMetadata,
				Content:    content,
			}, 13, nil),
			assert: func(t *testing.T, executionResult *result.ExecutionResult) {
				t.Helper()
				fileResult, ok := executionResult.S3()
				require.True(t, ok)
				require.Equal(t, content, fileResult.Content)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			storageJSON, err := json.Marshal(tt.result)
			require.NoError(t, err)
			var storageResult result.ExecutionResult
			require.NoError(t, json.Unmarshal(storageJSON, &storageResult))
			switch tt.result.Type() {
			case result.ExecutionResultTypeHTTP:
				httpResult, ok := storageResult.HTTP()
				require.True(t, ok)
				require.Empty(t, httpResult.RawBody)
			case result.ExecutionResultTypeS3:
				fileResult, ok := storageResult.S3()
				require.True(t, ok)
				require.Empty(t, fileResult.Content)
			default:
				require.Fail(t, "unexpected execution result type")
			}

			cipher, err := crypto.NewAES256(make([]byte, 32))
			require.NoError(t, err)
			requestPlaintext, err := marshalRequestPlaintext(
				kindQuery,
				queriessvc.ConnectorExecutionRequest{
					Limits: connectortypes.ExecutionLimits{
						MaxResponseBytes: 1024,
					},
				},
			)
			require.NoError(t, err)
			requestCiphertext, err := cipher.Encrypt(requestPlaintext)
			require.NoError(t, err)
			requestID := uuid.New()
			organizationSlug := "acme"
			repo := &fakeRequestRepo{request: requestrepo.Request{
				ID:                requestID,
				OrganizationSlug:  organizationSlug,
				Kind:              kindQuery,
				RequestCiphertext: requestCiphertext,
			}}
			broker := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher}
			require.NoError(t, broker.finish(
				t.Context(),
				requestID,
				uuid.New(),
				encryptedResponse{
					Result:                tt.result,
					resultRuntimeMaxBytes: 1024,
				},
				true,
			))

			restored, done, err := broker.readResponse(
				t.Context(),
				requestID,
				organizationSlug,
			)
			require.NoError(t, err)
			require.True(t, done)
			require.Nil(t, restored.ResultRuntime)
			tt.assert(t, restored.Result)
		})
	}
}

func TestMarshalResponsePlaintextRejectsOversizedRuntimePayload(t *testing.T) {
	t.Parallel()

	_, err := marshalResponsePlaintext(encryptedResponse{
		resultRuntimeMaxBytes: 4,
		Result: result.NewHTTPResult(&result.HTTPResult{
			RawBody: []byte{0x01, 0x02, 0x03, 0x04, 0x05},
		}, 0, nil),
	})

	require.True(t, xerror.Is(err, xerror.ExecutionResponseSizeExceeded()), "got %v", err)
	require.ErrorContains(t, err, "byte broker limit")

	plaintext, err := json.Marshal(encryptedResponse{
		ResultRuntime: &executionResultRuntimePayload{
			HTTPRawBody: []byte{0x01, 0x02, 0x03, 0x04},
			FileContent: []byte{0x01},
		},
	})
	require.NoError(t, err)
	_, err = unmarshalResponsePlaintext(plaintext, 4)
	require.True(t, xerror.Is(err, xerror.ExecutionResponseSizeExceeded()), "got %v", err)
}

func TestRestoreResultRuntimePayloadRejectsMissingOrMismatchedResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response encryptedResponse
	}{
		{
			name: "missing result",
			response: encryptedResponse{
				ResultRuntime: &executionResultRuntimePayload{HTTPRawBody: []byte{0x01}},
			},
		},
		{
			name: "result without byte payload",
			response: encryptedResponse{
				Result: result.NewSQLResult(
					"postgres",
					&result.SQLResult{},
					0,
					nil,
				),
				ResultRuntime: &executionResultRuntimePayload{HTTPRawBody: []byte{0x01}},
			},
		},
		{
			name: "mismatched payload kind",
			response: encryptedResponse{
				Result: result.NewHTTPResult(&result.HTTPResult{}, 0, nil),
				ResultRuntime: &executionResultRuntimePayload{
					HTTPRawBody: []byte{0x01},
					FileContent: []byte{0x01},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runtimePayload := tt.response.ResultRuntime
			err := tt.response.restoreResultRuntimePayload()

			require.True(t, xerror.Is(err, xerror.Unexpected()), "got %v", err)
			require.Same(t, runtimePayload, tt.response.ResultRuntime)
			if tt.response.Result != nil {
				if httpResult, ok := tt.response.Result.HTTP(); ok && httpResult != nil {
					require.Empty(t, httpResult.RawBody)
				}
			}

			plaintext, marshalErr := json.Marshal(tt.response)
			require.NoError(t, marshalErr)
			_, unmarshalErr := unmarshalResponsePlaintext(plaintext, 1024)
			require.True(
				t,
				xerror.Is(unmarshalErr, xerror.Unexpected()),
				"got %v",
				unmarshalErr,
			)
		})
	}
}

func TestRuntimeByteSidecarsRequireFullyRolledOutReaders(t *testing.T) {
	t.Parallel()

	content := []byte{0xff, 0xd8, 0xff, 0xe0}
	requestPlaintext, err := marshalRequestPlaintext(
		kindQuery,
		queriessvc.ConnectorExecutionRequest{
			Parameters: map[string]connectortypes.TypedParam{
				"input": {Value: content, DataType: "text"},
			},
			Limits: connectortypes.ExecutionLimits{MaxResponseBytes: 1024},
		},
	)
	require.NoError(t, err)

	var legacyRequest queriessvc.ConnectorExecutionRequest
	require.NoError(t, json.Unmarshal(requestPlaintext, &legacyRequest))
	require.Nil(t, legacyRequest.Parameters["input"].Value)

	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	invalidRequests := []struct {
		name        string
		envelope    connectorExecutionRequestEnvelope
		wantErr     error
		wantMessage string
	}{
		{
			name: "missing parameter",
			envelope: connectorExecutionRequestEnvelope{
				ConnectorExecutionRequest: queriessvc.ConnectorExecutionRequest{
					Parameters: map[string]connectortypes.TypedParam{},
				},
				RuntimeParameterBytes: map[string][]byte{"missing": content},
			},
			wantErr:     xerror.Unexpected(),
			wantMessage: `runtime byte parameter "missing" is missing`,
		},
		{
			name: "oversized payload",
			envelope: connectorExecutionRequestEnvelope{
				ConnectorExecutionRequest: queriessvc.ConnectorExecutionRequest{
					Parameters: map[string]connectortypes.TypedParam{
						"input": {DataType: "text"},
					},
					Limits: connectortypes.ExecutionLimits{MaxResponseBytes: 1},
				},
				RuntimeParameterBytes: map[string][]byte{"input": content},
			},
			wantErr: xerror.ExecutionRequestSizeExceeded(),
		},
	}
	for _, tt := range invalidRequests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			invalidRequestPlaintext, marshalErr := json.Marshal(tt.envelope)
			require.NoError(t, marshalErr)
			_, unmarshalErr := unmarshalConnectorExecutionRequest(invalidRequestPlaintext)
			require.True(t, xerror.Is(unmarshalErr, tt.wantErr), "got %v", unmarshalErr)
			if tt.wantMessage != "" {
				require.ErrorContains(t, unmarshalErr, tt.wantMessage)
			}

			invalidRequestCiphertext, encryptErr := cipher.Encrypt(invalidRequestPlaintext)
			require.NoError(t, encryptErr)
			repo := &fakeRequestRepo{}
			var factoryCalls atomic.Int32
			broker := &Broker{
				admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour,
				factory: func(
					context.Context,
					model.DataSourceType,
					[]byte,
					[]byte,
				) (connector.Connector, error) {
					factoryCalls.Add(1)
					return nil, xerror.Unexpected().
						Detail("connector creation should not be reached")
				},
			}
			configureTestProcessor(t, broker)
			require.NoError(t, broker.processClaimed(t.Context(), requestrepo.Request{
				ID:                uuid.New(),
				Kind:              kindQuery,
				RequestCiphertext: invalidRequestCiphertext,
				DeadlineAt:        time.Now().Add(time.Minute),
			}))
			processedPlaintext, decryptErr := cipher.Decrypt(repo.request.ResultCiphertext)
			require.NoError(t, decryptErr)
			processedResponse, responseErr := unmarshalResponsePlaintext(processedPlaintext, 0)
			require.NoError(t, responseErr)
			require.Equal(t, errorCodeInvalidRequest, processedResponse.ErrorCode)
			require.Zero(t, factoryCalls.Load())
		})
	}

	fileMetadata := filepayload.NewFilePayloadMetadata(
		"earthrise.jpg",
		"image/jpeg",
		content,
	)
	responsePlaintext, err := marshalResponsePlaintext(encryptedResponse{
		resultRuntimeMaxBytes: 1024,
		Result: result.NewHTTPResult(&result.HTTPResult{
			StatusCode: http.StatusOK,
			Headers:    map[string]string{"Content-Type": "image/jpeg"},
			File:       &fileMetadata,
			RawBody:    content,
		}, 11, nil),
	})
	require.NoError(t, err)

	var legacyResponse struct {
		Result *result.ExecutionResult `json:"result,omitempty"`
	}
	require.NoError(t, json.Unmarshal(responsePlaintext, &legacyResponse))
	httpResult, ok := legacyResponse.Result.HTTP()
	require.True(t, ok)
	require.Empty(t, httpResult.RawBody)
}

func TestQueryEnvelopeRestoresRuntimeInputRows(t *testing.T) {
	t.Parallel()

	plaintext, err := marshalRequestPlaintext(kindQuery, queriessvc.ConnectorExecutionRequest{
		Parameters: map[string]connectortypes.TypedParam{
			sinkbinding.InternalInputRows: {
				Value: []map[string]any{{"id": 1, "email": "alice@example.test"}},
			},
		},
	})
	require.NoError(t, err)

	request, err := unmarshalConnectorExecutionRequest(plaintext)
	require.NoError(t, err)
	require.Equal(
		t,
		[]map[string]any{{"id": 1, "email": "alice@example.test"}},
		request.Parameters[sinkbinding.InternalInputRows].Value,
	)
}

func TestWorkerRoundTripPreservesNumericRows(t *testing.T) {
	t.Parallel()
	const largeID = 9007199254740993
	rows := []map[string]any{{"id": largeID, "amount": -2300, "ratio": 1.25}}
	encoded, err := marshalResponsePlaintext(encryptedResponse{
		Result: result.NewSQLResult("postgres", &result.SQLResult{
			ColumnNames: []string{"id", "amount", "ratio"}, RowCount: 1, Rows: rows,
		}, 1, nil),
	})
	require.NoError(t, err)
	source, err := unmarshalResponsePlaintext(encoded, 0)
	require.NoError(t, err)
	sqlRows, ok := source.Result.SQL()
	require.True(t, ok)

	request, err := json.Marshal(pipelineruntime.TransformExecutionRequest{
		OrganizationSlug: slug.Slug("acme"),
		Transform: transformsql.Transform{
			SQL: "SELECT id, amount, ratio FROM input", OutputKind: model.TransformOutputKindRows,
		},
		UpstreamPayloads: map[string]*pipepayload.Payload{
			"input": {
				Representation: model.ContractRepresentationRows,
				ColumnNames:    sqlRows.ColumnNames, RowCount: 1, Rows: sqlRows.Rows,
			},
		},
	})
	require.NoError(t, err)
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	repo := &fakeRequestRepo{}
	broker := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour}
	require.NoError(t, broker.processClaimed(t.Context(), requestrepo.Request{
		ID: uuid.New(), State: requestrepo.StateRunning, Kind: kindTransform,
		RequestCiphertext: mustEncrypt(t, cipher, request), DeadlineAt: time.Now().Add(time.Minute),
	}))
	require.Equal(t, requestrepo.StateSucceeded, repo.request.State)
	responseBytes, err := cipher.Decrypt(repo.request.ResultCiphertext)
	require.NoError(t, err)
	response, err := unmarshalResponsePlaintext(responseBytes, 0)
	require.NoError(t, err)
	transportRows := []map[string]any{{"id": largeID, "amount": -2300, "ratio": json.Number("1.25")}}
	require.Equal(t, transportRows, response.TransformOutput.Rows)

	sinkBytes, err := marshalRequestPlaintext(kindQuery, queriessvc.ConnectorExecutionRequest{
		Parameters: map[string]connectortypes.TypedParam{
			sinkbinding.InternalInputRows: {Value: response.TransformOutput.Rows},
		},
	})
	require.NoError(t, err)
	sink, err := unmarshalConnectorExecutionRequest(sinkBytes)
	require.NoError(t, err)
	require.Equal(t, transportRows, sink.Parameters[sinkbinding.InternalInputRows].Value)
}

func TestWorkerRoundTripPreservesDeclaredDecimals(t *testing.T) {
	t.Parallel()
	precision, inputScale, outputScale := 38, 3, 2
	inputContract := &pipepayload.Contract{Fields: []pipepayload.ContractField{
		{Name: "price", Type: model.ContractFieldTypeValueDecimal, Precision: &precision, Scale: &inputScale, Required: true},
		{Name: "adjustment", Type: model.ContractFieldTypeValueDecimal, Precision: &precision, Scale: &inputScale, Required: true},
	}}
	outputContract := &pipepayload.Contract{Fields: []pipepayload.ContractField{
		{Name: "total", Type: model.ContractFieldTypeValueDecimal, Precision: &precision, Scale: &outputScale, Required: true},
	}}
	rows := []map[string]any{
		{"price": json.Number("0.10"), "adjustment": json.Number("0.20")},
		{"price": json.Number("2.675"), "adjustment": 0},
		{"price": json.Number("-2.675"), "adjustment": 0},
		{"price": json.Number("9007199254740993.10"), "adjustment": json.Number("0.20")},
	}
	request, err := json.Marshal(pipelineruntime.TransformExecutionRequest{
		OrganizationSlug: slug.Slug("acme"),
		Transform: transformsql.Transform{
			SQL:        "SELECT CAST(CAST(price AS DECIMAL(38,3)) + CAST(adjustment AS DECIMAL(38,3)) AS DECIMAL(38,2)) AS total FROM input",
			OutputKind: model.TransformOutputKindRows, InputContract: inputContract, OutputContract: outputContract,
		},
		UpstreamPayloads: map[string]*pipepayload.Payload{
			"input": {Representation: model.ContractRepresentationRows, Rows: rows, RowCount: len(rows)},
		},
	})
	require.NoError(t, err)
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	repo := &fakeRequestRepo{}
	broker := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour}
	require.NoError(t, broker.processClaimed(t.Context(), requestrepo.Request{
		ID: uuid.New(), State: requestrepo.StateRunning, Kind: kindTransform,
		RequestCiphertext: mustEncrypt(t, cipher, request), DeadlineAt: time.Now().Add(time.Minute),
	}))
	require.Equal(t, requestrepo.StateSucceeded, repo.request.State)
	responseBytes, err := cipher.Decrypt(repo.request.ResultCiphertext)
	require.NoError(t, err)
	response, err := unmarshalResponsePlaintext(responseBytes, 0)
	require.NoError(t, err)
	require.NoError(t, transformsql.ValidateOutputContract(response.TransformOutput, outputContract))

	sinkBytes, err := marshalRequestPlaintext(kindQuery, queriessvc.ConnectorExecutionRequest{
		Parameters: map[string]connectortypes.TypedParam{
			sinkbinding.InternalInputRows: {Value: response.TransformOutput.Rows},
		},
	})
	require.NoError(t, err)
	sink, err := unmarshalConnectorExecutionRequest(sinkBytes)
	require.NoError(t, err)
	sinkRows := sink.Parameters[sinkbinding.InternalInputRows].Value.([]map[string]any)
	for i, expected := range []string{"0.30", "2.68", "-2.68", "9007199254740993.30"} {
		for _, row := range []map[string]any{response.TransformOutput.Rows[i], sinkRows[i]} {
			require.IsType(t, json.Number(""), row["total"])
			value, err := pipepayload.DecimalFieldValue(row["total"], outputContract.Fields[0])
			require.NoError(t, err)
			require.Equal(t, expected, value.String())
		}
	}
}

func TestResponseEnvelopePreservesEmptyMongoDBDocuments(t *testing.T) {
	t.Parallel()

	plaintext, err := marshalResponsePlaintext(encryptedResponse{
		Result: result.NewMongoDBResult(&result.MongoDBResult{
			OperationType: "find",
			Documents:     []map[string]any{},
		}, 1, nil),
	})
	require.NoError(t, err)

	response, err := unmarshalResponsePlaintext(plaintext, 0)
	require.NoError(t, err)
	mongoResult, ok := response.Result.MongoDB()
	require.True(t, ok)
	require.NotNil(t, mongoResult.Documents)
	require.Empty(t, mongoResult.Documents)
}

func TestListGitBranchesReturnsExecutorResponse(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	payload, err := json.Marshal(encryptedResponse{Branches: []datasourcessvc.DataSourceBranch{{Name: "main"}}})
	require.NoError(t, err)
	repo := &fakeRequestRepo{request: requestrepo.Request{
		State:            requestrepo.StateSucceeded,
		ResultCiphertext: mustEncrypt(t, cipher, payload),
	}}
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher,
		dispatcher: NewPollerDispatcher(), requestTimeout: testRequestTimeout,
	}

	branches, err := broker.ListGitBranches(t.Context(), datasourcessvc.GitBranchRequest{OrganizationSlug: slug.Slug("acme")})
	require.NoError(t, err)
	require.Equal(t, []datasourcessvc.DataSourceBranch{{Name: "main"}}, branches)
}

func mustEncrypt(t *testing.T, cipher crypto.AES256, plaintext []byte) []byte {
	t.Helper()
	ciphertext, err := cipher.Encrypt(plaintext)
	require.NoError(t, err)
	return ciphertext
}

func TestResponseErrorMapsWorkerFailureCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		code    string
		wantErr error
	}{
		{code: "connection_failed", wantErr: datasourcessvc.ErrConnectionFailed},
		{code: "query_failed"},
		{code: errorCodeQueryRowLimit, wantErr: xerror.ExecutionRowLimitExceeded()},
		{code: errorCodeDestinationConstraint, wantErr: xerror.DestinationConstraintViolation()},
		{code: errorCodeAcknowledgementRejected, wantErr: xerror.HTTPAcknowledgementRejected()},
		{code: errorCodeAcknowledgementUncertain, wantErr: xerror.HTTPAcknowledgementUncertain()},
		{code: "transform_work", wantErr: transformsql.ErrWorkBudgetExceeded},
		{code: "transform_intermediate_rows", wantErr: transformsql.ErrIntermediateRowsExceeded},
		{code: "transform_intermediate_bytes", wantErr: transformsql.ErrIntermediateBytesExceeded},
		{code: "transform_output_rows", wantErr: transformsql.ErrOutputRowsExceeded},
		{code: "transform_output_bytes", wantErr: transformsql.ErrOutputBytesExceeded},
		{code: "transform_depth", wantErr: transformsql.ErrDepthBudgetExceeded},
		{code: "transform_interrupted", wantErr: transformsql.ErrExecutionBudgetInterrupted},
		{code: errorCodeRepositoryForbidden, wantErr: xerror.Forbidden(xerror.DataSource)},
		{code: errorCodeRepositoryConflict, wantErr: xerror.AlreadyExists(xerror.RepoFile)},
		{code: errorCodeRepositoryInvalid, wantErr: xerror.InvalidInput(xerror.RepoFile)},
		{code: errorCodeRepositoryCleanup, wantErr: gitprovider.ErrRepositoryCleanupFailed},
		{code: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			t.Parallel()
			err := responseError(encryptedResponse{ErrorCode: tt.code})
			require.Error(t, err)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
	require.NoError(t, responseError(encryptedResponse{}))
}

func TestResponseSizeErrorSurvivesWorkerBoundary(t *testing.T) {
	t.Parallel()
	original := xerror.ExecutionResponseSizeExceeded().Detail("fixture exceeded limit")
	require.ErrorIs(t, responseError(encryptedResponse{ErrorCode: safeQueryErrorCode(original)}), xerror.ExecutionResponseSizeExceeded())
}

func TestSafeQueryErrorCode(t *testing.T) {
	t.Parallel()
	require.Equal(t, errorCodeQueryRowLimit, safeQueryErrorCode(xerror.ExecutionRowLimitExceeded()))
	require.Equal(t, errorCodeDestinationConstraint, safeQueryErrorCode(xerror.DestinationConstraintViolation()))
	require.Equal(t, errorCodeAcknowledgementRejected, safeQueryErrorCode(xerror.HTTPAcknowledgementRejected()))
	require.Equal(t, errorCodeAcknowledgementUncertain, safeQueryErrorCode(xerror.HTTPAcknowledgementUncertain()))
	require.Equal(t, errorCodeDataSourceConnection, safeQueryErrorCode(datasourcessvc.ErrConnectionFailed))
	require.Equal(t, errorCodeQueryFailed, safeQueryErrorCode(xerror.Unexpected()))
}

func TestBrokerNewRequiresTransactionAndCipher(t *testing.T) {
	t.Parallel()
	broker, err := New(nil, nil, nil, 0, nil, nil, testutil.FakeOperationAdmission{})
	require.Nil(t, broker)
	require.Error(t, err)

	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	broker, err = New(fakeTXM{}, cipher, NewPollerDispatcher(), testRequestTimeout, nil, nil, testutil.FakeOperationAdmission{})
	require.NoError(t, err)
	require.NotNil(t, broker)
	require.NotNil(t, broker.logger)
}

func TestProcessRejectsSubmitOnlyBrokerBeforeClaim(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	broker, err := New(fakeTXM{}, cipher, NewPollerDispatcher(), testRequestTimeout, nil, nil, testutil.FakeOperationAdmission{})
	require.NoError(t, err)

	repo := &fakeRequestRepo{request: requestrepo.Request{
		ID:    uuid.New(),
		State: requestrepo.StateRunning,
		Kind:  kindConnectionTest,
	}}
	broker.repo = repo

	_, err = broker.Process(t.Context(), repo.request.ID)

	require.ErrorContains(t, err, "connector factory is required by execution processor")
	require.Zero(t, repo.claimCalls.Load())
	require.Zero(t, repo.completeCalls.Load())
}

func TestBrokerStartRejectsInvalidExecutorConfiguration(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)

	broker := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: &fakeRequestRepo{}, cipher: cipher}
	require.ErrorContains(t, broker.Start(t.Context(), 1), "connector factory is required by execution poller")
	broker.factory = func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		return nil, xerror.Unexpected().Detail("connector factory should not be invoked")
	}
	require.Error(t, broker.Start(t.Context(), 0))
}

func TestTerminalOutcomeMapsBrokerFailures(t *testing.T) {
	t.Parallel()
	require.Equal(t, "success", terminalOutcome(nil))
	require.Equal(t, "ambiguous", terminalOutcome(ErrAmbiguousExecution))
	require.Equal(t, "timeout", terminalOutcome(context.DeadlineExceeded))
	require.Equal(t, "cancelled", terminalOutcome(context.Canceled))
	require.Equal(t, "error", terminalOutcome(xerror.Unexpected().Detail("worker failed")))
}

func TestProcessTransformPublishesWorkerBudgetMetrics(t *testing.T) { // Process metrics are global.
	meter, err := metrics.New(metrics.Config{
		Enabled: true, Path: "/metrics", ServiceName: "executor", Environment: "test",
	})
	require.NoError(t, err)
	restore := metrics.SetProcessMeter(meter)
	t.Cleanup(restore)

	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	payload, err := json.Marshal(pipelineruntime.TransformExecutionRequest{
		OrganizationSlug: slug.Slug("acme"),
		Transform:        transformsql.Transform{SQL: "SELECT JSON_ARRAY(JSON_ARRAY(id)) AS value FROM input"},
		UpstreamPayloads: map[string]*pipepayload.Payload{
			"input": {
				Representation: model.ContractRepresentationRows,
				ColumnNames:    []string{"id"},
				RowCount:       1,
				Rows:           []map[string]any{{"id": 1}},
			},
		},
		Limits: transformsql.Limits{MaxDepth: 1},
	})
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt(payload)
	require.NoError(t, err)

	id := uuid.New()
	repo := &fakeRequestRepo{}
	b := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour}
	require.NoError(t, b.processClaimed(context.Background(), requestrepo.Request{
		ID: id, State: requestrepo.StateRunning, Kind: kindTransform,
		RequestCiphertext: encrypted, DeadlineAt: time.Now().Add(time.Minute),
	}))
	require.EqualValues(t, 1, repo.completeCalls.Load())

	app := fiber.New()
	meter.RegisterRoutes(app)
	response, err := app.Test(httptest.NewRequestWithContext(
		context.Background(), http.MethodGet, "/metrics", nil,
	))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `braidplane_transform_budget_hit_total{deployment_environment="test",limit_type="depth",service_name="executor"} 1`)
	require.Contains(t, string(body), `braidplane_transform_max_depth_count{deployment_environment="test",limit_type="depth",outcome="error",service_name="executor"} 1`)
	require.Contains(t, string(body), "braidplane_transform_output_rows")
	require.Contains(t, string(body), "braidplane_transform_output_bytes")
}

func TestScopeSurvivesWorkerTransport(t *testing.T) {
	t.Parallel()
	scope := &resourcescope.Scope{PolicyID: resourcescope.S3PolicyID, DataSource: "store", Operation: "read", Contract: "orders", Revision: "v1", ObjectID: "row", Representation: model.ContractRepresentationRows, S3: &resourcescope.S3{Endpoint: "https://storage.example", Region: "test", Bucket: "fixed-bucket", Prefix: "imports", DynamicKey: true}}
	request := queriessvc.ConnectorExecutionRequest{SinkExecution: true, OrganizationSlug: "acme", ResourceScope: scope, Limits: connectortypes.ExecutionLimits{MaxResponseBytes: 1024}}
	raw, err := marshalRequestPlaintext(kindQuery, request)
	require.NoError(t, err)
	// Retained requests may still carry the retired admission marker.
	raw = bytes.Replace(raw, []byte(`"organization_slug":`), []byte(`"contract_composition_required":true,"organization_slug":`), 1)
	decoded, err := unmarshalConnectorExecutionRequest(raw)
	require.NoError(t, err)
	require.Equal(t, scope, decoded.ResourceScope)
	require.True(t, decoded.SinkExecution)
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	repo := &fakeRequestRepo{}
	broker := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour}
	require.NoError(t, broker.processClaimed(t.Context(), requestrepo.Request{ID: uuid.New(), State: requestrepo.StateRunning, Kind: kindQuery, RequestCiphertext: mustEncrypt(t, cipher, raw), DeadlineAt: time.Now().Add(time.Minute)}))
	plaintext, err := cipher.Decrypt(repo.request.ResultCiphertext)
	require.NoError(t, err)
	response, err := decodeResponsePlaintext(plaintext)
	require.NoError(t, err)
	require.Equal(t, errorCodeQueryFailed, response.ErrorCode)
}

func (r *fakeRequestRepo) CheckDispatch(_ context.Context, _ postgres.DBTX, expected requestrepo.DispatchAuthority) (bool, error) {
	r.lastDispatch = expected
	r.dispatchCalls++
	return !r.dispatchDenied, r.dispatchErr
}

func TestExecuteConnectorRechecksAuthorityBeforeConstruction(t *testing.T) {
	t.Parallel()
	called := false
	pool, err := connector.NewManager(func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		called = true
		return nil, context.Canceled
	}, connector.RuntimePoolSettings{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	repo := &fakeRequestRepo{dispatchDenied: true}
	b := &Broker{admission: testutil.FakeOperationAdmission{}, runtimePool: pool, repo: repo, txm: fakeTXM{}}
	id := uuid.New()
	_, err = b.executeConnector(t.Context(), id, uuid.New(), queriessvc.ConnectorExecutionRequest{OrganizationSlug: "acme", DataSourceSlug: "db", DataSourceType: model.DataSourceTypePostgres, CredentialVersion: "secret/2026-10-18T10:00:00Z"})
	require.ErrorIs(t, err, deploymentplan.ErrInvalid)
	require.False(t, called)
	require.Equal(t, 1, repo.dispatchCalls)
	require.Equal(t, id, repo.lastDispatch.RequestID)
	require.Equal(t, "secret", *repo.lastDispatch.CredentialSlug)
	require.Equal(t, "2026-10-18T10:00:00Z", repo.lastDispatch.CredentialUpdatedAt.Format(time.RFC3339))
}

func TestExecuteConnectorRejectsMissingCredentialLifecycleReference(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"", "credential-hash", "secret/not-a-timestamp"} {
		repo := &fakeRequestRepo{}
		b := &Broker{admission: testutil.FakeOperationAdmission{}, repo: repo, txm: fakeTXM{}}
		_, err := b.executeConnector(t.Context(), uuid.New(), uuid.New(), queriessvc.ConnectorExecutionRequest{CredentialVersion: version})
		require.ErrorIs(t, err, deploymentplan.ErrInvalid)
		require.Zero(t, repo.dispatchCalls)
	}
}

func TestWorkerPreservesSinkRetryAuthority(t *testing.T) {
	for _, sink := range []bool{false, true} {
		t.Run(fmt.Sprint(sink), func(t *testing.T) {
			recording := &recordingQueryConnector{}
			pool, err := connector.NewManager(func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
				return recording, nil
			}, connector.RuntimePoolSettings{})
			require.NoError(t, err)
			defer func() { require.NoError(t, pool.Close()) }()
			broker := &Broker{admission: testutil.FakeOperationAdmission{}, runtimePool: pool, txm: fakeTXM{}, repo: &fakeRequestRepo{}}
			request := queriessvc.ConnectorExecutionRequest{SinkExecution: sink, OrganizationSlug: "acme", DataSourceSlug: "http-api", DataSourceType: model.DataSourceTypeHTTP, CredentialReference: "secret/2026-10-18T10:00:00Z", ConfigVersion: "v1", CredentialVersion: "v1"}
			encoded, err := marshalRequestPlaintext(kindQuery, request)
			require.NoError(t, err)
			decoded, err := unmarshalConnectorExecutionRequest(encoded)
			require.NoError(t, err)
			_, err = broker.executeConnector(t.Context(), uuid.New(), uuid.New(), decoded)
			require.NoError(t, err)
			require.Equal(t, sink, recording.sinkExecution)
		})
	}
}

func TestOperationAdmissionFailuresDiscardResultsAndRemainExecutionFailures(t *testing.T) {
	for _, afterExecution := range []bool{false, true} {
		t.Run(fmt.Sprint(afterExecution), func(t *testing.T) {
			cipher, err := crypto.NewAES256(make([]byte, 32))
			require.NoError(t, err)
			conn := &fixedResultConnector{result: result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": 1}}, ColumnNames: []string{"id"}}, 1, nil)}
			repo := &fakeRequestRepo{}
			guard := testutil.FakeOperationAdmission{WithOperationFn: func(ctx context.Context, _, _ slug.Slug, execute func(context.Context) error) error {
				if afterExecution {
					require.NoError(t, execute(ctx))
				}
				return xerror.Unexpected().Detail("synthetic authority or unlock failure")
			}}
			broker := &Broker{
				admission: guard, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour,
				factory: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
					return conn, nil
				},
			}
			configureTestProcessor(t, broker)
			payload, err := marshalRequestPlaintext(kindQuery, queriessvc.ConnectorExecutionRequest{
				OrganizationSlug: "tester", DataSourceSlug: "fixture", DataSourceType: model.DataSourceTypePostgres,
				ConfigVersion: "config-v1", CredentialVersion: "credential-v1", CredentialReference: "secret/2026-10-18T10:00:00Z",
				StatementJSON: json.RawMessage(`{"type":"postgres","sql":"SELECT 1"}`), Limits: connectortypes.ExecutionLimits{MaxResponseBytes: 1024},
			})
			require.NoError(t, err)
			require.NoError(t, broker.processClaimed(t.Context(), requestrepo.Request{ID: uuid.New(), OrganizationSlug: "tester", Kind: kindQuery, RequestCiphertext: mustEncrypt(t, cipher, payload), DeadlineAt: time.Now().Add(time.Minute)}))
			require.Equal(t, requestrepo.StateFailed, repo.request.State)
			raw, err := cipher.Decrypt(repo.request.ResultCiphertext)
			require.NoError(t, err)
			response, err := unmarshalResponsePlaintext(raw, 0)
			require.NoError(t, err)
			require.ErrorIs(t, responseError(response), xerror.ExecutionUnavailable())
			require.Nil(t, response.Result)
			require.Empty(t, response.ResultRuntime)
			if afterExecution {
				require.EqualValues(t, 1, conn.calls.Load())
			} else {
				require.Zero(t, conn.calls.Load())
			}
		})
	}
}

func TestQueryClassificationSurvivesWorkerBoundary(t *testing.T) {
	for _, cause := range []*xerror.Error{xerror.SourceQueryInvalid(), xerror.SourceSchemaReferenceInvalid(), xerror.Forbidden(xerror.DataSource), xerror.ExecutionResponseSizeExceeded()} {
		t.Run(cause.Code(), func(t *testing.T) {
			original := cause.Detail("SELECT private_column FROM private_relation password=private-secret")
			restored := responseError(encryptedResponse{ErrorCode: safeQueryErrorCode(original)})
			require.ErrorIs(t, restored, cause)
			require.NotContains(t, restored.Error(), "private")
		})
	}
}

func TestDestinationConstraintSurvivesWorkerBoundary(t *testing.T) {
	t.Parallel()
	original := xerror.DestinationConstraintViolation().Detail("private-row-value").WithPublicDetail("Review destination constraints before retrying.")
	wire, err := json.Marshal(encryptedResponse{ErrorCode: safeQueryErrorCode(original), PublicDetail: xerror.PublicDetail(original)})
	require.NoError(t, err)
	require.NotContains(t, string(wire), "private-row-value")
	var decoded encryptedResponse
	require.NoError(t, json.Unmarshal(wire, &decoded))
	restored := responseError(decoded)
	require.ErrorIs(t, restored, xerror.DestinationConstraintViolation())
	var typed *xerror.Error
	require.ErrorAs(t, restored, &typed)
	require.Equal(t, xerror.KindInvalidInput, typed.Kind())
	require.Equal(t, xerror.PublicDetail(original), xerror.PublicDetail(restored))
	require.NotContains(t, restored.Error(), "private-row-value")
}
