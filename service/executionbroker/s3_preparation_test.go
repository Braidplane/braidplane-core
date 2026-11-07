package executionbroker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/connector/writegate"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/postgres"
	requestrepo "github.com/Braidplane/braidplane-core/postgres/executionrequests"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	connectortypes "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type preparationDispatcher struct {
	repo    *fakeRequestRepo
	kinds   []string
	process func(requestrepo.Request) error
}

func (d *preparationDispatcher) Dispatch(_ context.Context, _ postgres.DBTX, _ uuid.UUID) error {
	d.kinds = append(d.kinds, d.repo.created.Kind)
	d.repo.request = d.repo.created
	d.repo.request.State = requestrepo.StateRunning
	return d.process(d.repo.request)
}

func TestS3PreparationCrossesEncryptedWorkerBoundary(t *testing.T) {
	for _, mode := range []string{"malformed", "serialization", "read", "valid", "native", "skip", "old_worker"} {
		t.Run(mode, func(t *testing.T) {
			var writes, claims atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writes.Add(1)
				assert.EqualValues(t, 1, claims.Load(), "ledger admission must precede provider write")
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				if mode == "native" {
					assert.Equal(t, []byte{0, 0xff, 1}, body)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			workerCtx := transportsecurity.WithPlaintext(t.Context())
			cipher, err := crypto.NewAES256(make([]byte, 32))
			require.NoError(t, err)
			repo := &fakeRequestRepo{}
			broker := &Broker{
				txm: fakeTXM{}, repo: repo, cipher: cipher, admission: testutil.FakeOperationAdmission{}, requestTimeout: testRequestTimeout, heartbeatEvery: time.Hour,
				factory: func(ctx context.Context, _ model.DataSourceType, _, _ []byte) (connector.Connector, error) {
					return s3.New(ctx, s3.Config{Region: "test", Bucket: "fixtures", Endpoint: server.URL, ForcePathStyle: true, AllowPrivateHosts: true}, s3.Credentials{AccessKeyID: "fixture-key", SecretAccessKey: "fixture-secret"})
				},
			}
			configureTestProcessor(t, broker)
			dispatcher := &preparationDispatcher{repo: repo, process: func(request requestrepo.Request) error {
				// Only the encrypted envelope crosses the worker boundary; no submitter context values survive.
				if mode == "old_worker" && request.Kind == kindPrepareS3Write {
					return broker.finish(workerCtx, request.ID, request.ClaimToken, encryptedResponse{ErrorCode: errorCodeInvalidRequest}, false)
				}
				return broker.processClaimed(workerCtx, request)
			}}
			broker.dispatcher = dispatcher
			statement := json.RawMessage(`{"operation":"write","key":"out.json","format":"json"}`)
			params := map[string]connectortypes.TypedParam{sinkbinding.Input: {Value: []any{map[string]any{"id": 1}}}}
			switch mode {
			case "malformed":
				statement = json.RawMessage(`{"operation":"write","key":42,"metadata":{"private-name":42}}`)
			case "serialization":
				statement = json.RawMessage(`{"operation":"write","key":"out.csv","format":"csv","delimiter":"||"}`)
			case "read":
				statement = json.RawMessage(`{"operation":"read","key":"out.json"}`)
			case "native":
				statement = json.RawMessage(`{"operation":"write","key":"out.bin","format":"file"}`)
				params[sinkbinding.Input] = connectortypes.TypedParam{Value: []byte{0, 0xff, 1}}
			}
			request := queriessvc.ConnectorExecutionRequest{ConcurrencyLimit: 1, ConcurrencyQueueLimit: 1, ConcurrencyQueueTimeout: time.Second, SinkExecution: true, OrganizationSlug: "acme", DataSourceSlug: "objects", DataSourceType: model.DataSourceTypeS3, ConfigVersion: "config-v1", CredentialVersion: "credential-v1", CredentialReference: "secret/2026-10-18T10:00:00Z", ConfigJSON: []byte(`{}`), CredentialsJSON: []byte(`{}`), StatementJSON: statement, Parameters: params, Limits: connectortypes.ExecutionLimits{MaxResponseBytes: 1024}}
			ctx := writegate.WithBeforeWrite(t.Context(), func(context.Context) error {
				claims.Add(1)
				if mode == "skip" {
					return writegate.ErrSkipped
				}
				return nil
			})
			output, err := broker.ExecuteConnector(ctx, request)
			switch mode {
			case "malformed", "serialization", "read":
				require.ErrorIs(t, err, writegate.ErrPreparationFailed)
				require.Zero(t, claims.Load())
				require.Zero(t, writes.Load())
				require.NotContains(t, err.Error(), "private-")
				require.Equal(t, []string{kindPrepareS3Write}, dispatcher.kinds)
			case "old_worker":
				require.Error(t, err)
				require.Zero(t, claims.Load())
				require.Zero(t, writes.Load())
				require.Equal(t, []string{kindPrepareS3Write}, dispatcher.kinds)
			case "skip":
				require.ErrorIs(t, err, writegate.ErrSkipped)
				require.EqualValues(t, 1, claims.Load())
				require.Zero(t, writes.Load())
				require.Equal(t, []string{kindPrepareS3Write}, dispatcher.kinds)
			default:
				require.NoError(t, err)
				require.NotNil(t, output)
				require.EqualValues(t, 1, claims.Load())
				require.EqualValues(t, 1, writes.Load())
				require.Equal(t, []string{kindPrepareS3Write, kindQuery}, dispatcher.kinds)
			}
		})
	}
}
