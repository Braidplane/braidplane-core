package executionbroker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	requestrepo "github.com/Braidplane/braidplane-core/postgres/executionrequests"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	s3connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

func TestResourceScopeRejectionBrokerRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, key, bucket string }{
		{name: "traversal", key: "../private"},
		{name: "absolute", key: "/private"},
		{name: "absolute component", key: "partition//private"},
		{name: "ambiguous", key: "%2e%2e/private"},
		{name: "bucket substitution", key: "allowed.json", bucket: "private-bucket"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var providerCalls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				providerCalls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(provider.Close)
			conn, err := s3connector.New(transportsecurity.WithPlaintext(t.Context()), s3connector.Config{Endpoint: provider.URL, Region: "test", Bucket: "fixed-bucket", Prefix: "imports", ForcePathStyle: true, AllowPrivateHosts: true}, s3connector.Credentials{AccessKeyID: "test", SecretAccessKey: "test"})
			require.NoError(t, err)
			scope := &resourcescope.Scope{PolicyID: resourcescope.S3PolicyID, DataSource: "source", Operation: "orders", Contract: "orders", Revision: "v1", ObjectID: "row", Representation: model.ContractRepresentationRows, S3: &resourcescope.S3{Endpoint: provider.URL, Region: "test", Bucket: "fixed-bucket", Prefix: "imports", DynamicKey: true, ForcePathStyle: true}}
			request := scopeTestRequest()
			request.DataSourceType = model.DataSourceTypeS3
			request.ResourceScope = scope
			request.StatementJSON = json.RawMessage(`{"type":"s3","operation":"read","key":"{{key}}","format":"json"}`)
			request.Parameters = map[string]types.TypedParam{"key": {Value: tc.key}}
			if tc.bucket != "" {
				request.StatementJSON = json.RawMessage(`{"type":"s3","operation":"read","key":"{{key}}","bucket":"{{bucket}}","format":"json"}`)
				request.Parameters["bucket"] = types.TypedParam{Value: tc.bucket}
			}
			broker, repo := scopeTestBroker(t, conn)
			roundTripScopeFailure(t, broker, repo, request)
			require.Zero(t, providerCalls.Load())
		})
	}
}

func TestResourceScopeErrorDetailsDoNotCrossBroker(t *testing.T) {
	t.Parallel()
	for _, fromResult := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "result"}[fromResult], func(t *testing.T) {
			t.Parallel()
			privateErr := xerror.Unexpected().With(resourcescope.ErrInvalid).
				Detail("private-selector token=synthetic-secret").WithPublicDetail("private-public-detail")
			conn := &fixedResultConnector{err: privateErr}
			if fromResult {
				conn.err = nil
				conn.result = result.NewSQLResult("synthetic", nil, 0, privateErr)
			}
			broker, repo := scopeTestBroker(t, conn)
			roundTripScopeFailure(t, broker, repo, scopeTestRequest())
			require.EqualValues(t, 1, conn.calls.Load())
		})
	}
}

func scopeTestRequest() queriessvc.ConnectorExecutionRequest {
	return queriessvc.ConnectorExecutionRequest{
		OrganizationSlug: "acme", DataSourceSlug: "source", DataSourceType: model.DataSourceTypePostgres,
		ConfigVersion: "config-v1", CredentialVersion: "credential-v1",
		CredentialReference: "secret/2026-10-18T10:00:00Z",
		ConcurrencyLimit:    1, ConcurrencyQueueLimit: 1, ConcurrencyQueueTimeout: time.Second,
		Limits: types.ExecutionLimits{ReadOnly: true, MaxResponseBytes: 1024},
	}
}

func scopeTestBroker(t *testing.T, conn connector.Connector) (*Broker, *fakeRequestRepo) {
	t.Helper()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	repo := &fakeRequestRepo{}
	broker := &Broker{
		admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Hour,
		requestTimeout: testRequestTimeout,
		factory: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
			return conn, nil
		},
	}
	configureTestProcessor(t, broker)
	broker.dispatcher = scopeTestDispatcher{broker: broker, repo: repo}
	return broker, repo
}

type scopeTestDispatcher struct {
	broker *Broker
	repo   *fakeRequestRepo
}

func (d scopeTestDispatcher) Dispatch(ctx context.Context, _ postgres.DBTX, id uuid.UUID) error {
	d.repo.request = d.repo.created
	d.repo.request.State = requestrepo.StateRunning
	_, err := d.broker.Process(ctx, id)
	return err
}

func roundTripScopeFailure(t *testing.T, broker *Broker, repo *fakeRequestRepo, request queriessvc.ConnectorExecutionRequest) {
	t.Helper()
	output, err := broker.ExecuteConnector(t.Context(), request)
	require.Nil(t, output)
	require.ErrorIs(t, err, resourcescope.ErrInvalid)
	require.Equal(t, resourcescope.ErrInvalid.Error(), err.Error())
	require.Empty(t, xerror.PublicDetail(err))
	require.Equal(t, requestrepo.StateFailed, repo.request.State)
	plaintext, err := broker.cipher.Decrypt(repo.request.ResultCiphertext)
	require.NoError(t, err)
	require.JSONEq(t, `{"error_code":"resource_scope_rejected"}`, string(plaintext))
}
