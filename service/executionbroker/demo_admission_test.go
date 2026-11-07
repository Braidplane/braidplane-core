package executionbroker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	requestrepo "github.com/Braidplane/braidplane-core/postgres/executionrequests"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/stretchr/testify/require"
)

func TestNewRequiresOperationAdmission(t *testing.T) {
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	broker, err := New(fakeTXM{}, cipher, NewPollerDispatcher(), time.Minute, nil, nil, nil, filestream.Profile{})
	require.ErrorContains(t, err, "execution broker dependencies are required")
	require.Nil(t, broker)
}

func TestSavedOperationsAreFencedBeforeProviderIO(t *testing.T) {
	admissionDenied := xerror.New(xerror.KindUnavailable, "TEST_ADMISSION_DENIED", "execution admission denied")
	for _, kind := range []string{kindConnectionTest, kindSchema, kindQuery} {
		t.Run(kind, func(t *testing.T) {
			cipher, err := crypto.NewAES256(make([]byte, 32))
			require.NoError(t, err)
			id := uuid.New()
			calls := 0
			guard := testutil.FakeOperationAdmission{WithOperationFn: func(ctx context.Context, org, source slug.Slug, execute func(context.Context) error) error {
				calls++
				bound, ok := executiondelivery.Request(ctx)
				require.True(t, ok)
				require.Equal(t, id, bound)
				require.Equal(t, slug.Slug("tester"), org)
				require.Equal(t, slug.Slug("fixture"), source)
				return admissionDenied
			}, EncodeOperationErrorFn: func(err error) (string, bool) {
				return "test_admission_denied", xerror.Is(err, admissionDenied)
			}, DecodeOperationErrorFn: func(code string) (error, bool) {
				return admissionDenied, code == "test_admission_denied"
			}}
			broker, err := New(fakeTXM{}, cipher, NewPollerDispatcher(), time.Minute, func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
				t.Fatal("paused fixture reached provider")
				return nil, xerror.Unexpected()
			}, nil, guard, filestream.Profile{})
			require.NoError(t, err)
			defer func() { require.NoError(t, broker.Close()) }()
			repo := &fakeRequestRepo{}
			broker.repo = repo
			payload := datasourcessvc.ConnectionTestRequest{OrganizationSlug: "tester", DataSourceSlug: "fixture", DataSourceType: model.DataSourceTypePostgres}
			requestJSON, err := json.Marshal(payload)
			require.NoError(t, err)
			err = broker.processClaimed(t.Context(), requestrepo.Request{ID: id, OrganizationSlug: "tester", Kind: kind, RequestCiphertext: mustEncrypt(t, cipher, requestJSON), DeadlineAt: time.Now().Add(time.Minute)})
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, requestrepo.StateFailed, repo.request.State)
			raw, err := cipher.Decrypt(repo.request.ResultCiphertext)
			require.NoError(t, err)
			var response encryptedResponse
			require.NoError(t, json.Unmarshal(raw, &response))
			require.ErrorIs(t, broker.responseError(response), admissionDenied)
		})
	}
}

func TestInvalidAdmissionErrorMappingFailsClosed(t *testing.T) {
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	guard := testutil.FakeOperationAdmission{
		WithOperationFn: func(context.Context, slug.Slug, slug.Slug, func(context.Context) error) error {
			return xerror.Unexpected().Detail("private admission detail")
		},
		EncodeOperationErrorFn: func(error) (string, bool) { return "", true },
		DecodeOperationErrorFn: func(string) (error, bool) { return nil, true },
	}
	broker, err := New(fakeTXM{}, cipher, NewPollerDispatcher(), time.Minute, nil, nil, guard, filestream.Profile{})
	require.NoError(t, err)
	defer func() { require.NoError(t, broker.Close()) }()
	repo := &fakeRequestRepo{}
	broker.repo = repo
	payload, err := json.Marshal(datasourcessvc.ConnectionTestRequest{OrganizationSlug: "tester", DataSourceSlug: "fixture", DataSourceType: model.DataSourceTypePostgres})
	require.NoError(t, err)
	err = broker.processClaimed(t.Context(), requestrepo.Request{ID: uuid.New(), OrganizationSlug: "tester", Kind: kindConnectionTest, RequestCiphertext: mustEncrypt(t, cipher, payload), DeadlineAt: time.Now().Add(time.Minute)})
	require.NoError(t, err)
	require.Equal(t, requestrepo.StateFailed, repo.request.State)
	raw, err := cipher.Decrypt(repo.request.ResultCiphertext)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private admission detail")
	var response encryptedResponse
	require.NoError(t, json.Unmarshal(raw, &response))
	require.Equal(t, errorCodeQueryFailed, response.ErrorCode)
	require.ErrorIs(t, broker.responseError(response), xerror.ExecutionUnavailable())
}

func TestSavedConnectionAndSchemaRequestsBindOnlyTheirSourceCapability(t *testing.T) {
	for _, kind := range []string{kindConnectionTest, kindSchema} {
		t.Run(kind, func(t *testing.T) {
			cipher, err := crypto.NewAES256(make([]byte, 32))
			require.NoError(t, err)
			plain, err := marshalResponsePlaintext(encryptedResponse{})
			require.NoError(t, err)
			response, err := cipher.Encrypt(plain)
			require.NoError(t, err)
			repo := &fakeRequestRepo{request: requestrepo.Request{State: requestrepo.StateSucceeded, ResultCiphertext: response}}
			broker, err := New(fakeTXM{}, cipher, NewPollerDispatcher(), time.Second, nil, nil, testutil.FakeOperationAdmission{}, filestream.Profile{})
			require.NoError(t, err)
			broker.repo = repo
			_, err = broker.submitAndWait(t.Context(), "tester", kind, datasourcessvc.ConnectionTestRequest{OrganizationSlug: "tester", DataSourceSlug: "fixture", DataSourceType: model.DataSourceTypePostgres})
			require.NoError(t, err)
			require.Equal(t, slug.Slug("fixture"), repo.boundSource)
		})
	}
}
