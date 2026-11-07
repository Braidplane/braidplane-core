package executionbroker

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	requestrepo "github.com/Braidplane/braidplane-core/postgres/executionrequests"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

func TestNativeFileAdmissionAndProfileBoundary(t *testing.T) {
	t.Parallel()
	profile := filestream.Profile{Organization: "fixture", MaxBytes: 1024, Timeout: time.Minute, Transfers: 1, Connectors: 2}
	for _, tc := range []struct {
		org                       string
		bytes                     int64
		wantConnectors, wantFiles int
		file                      bool
	}{
		{"fixture", 1024, 2, 1, true}, {"fixture", 0, 2, 1, false}, {"other", 0, 0, 0, false},
	} {
		t.Run(tc.org+"/"+time.Duration(tc.bytes).String(), func(t *testing.T) {
			cipher, err := crypto.NewAES256(make([]byte, 32))
			require.NoError(t, err)
			repo := &fakeRequestRepo{request: requestrepo.Request{State: requestrepo.StateSucceeded, ResultCiphertext: mustEncrypt(t, cipher, []byte(`{}`))}}
			b := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, dispatcher: NewPollerDispatcher(), requestTimeout: time.Second, nativeFiles: profile, logger: slog.New(slog.DiscardHandler)}
			_, err = b.submitAndWait(t.Context(), tc.org, kindQuery, queriessvc.ConnectorExecutionRequest{OrganizationSlug: slug.Slug(tc.org), DataSourceSlug: "source", ConcurrencyLimit: 2, ConcurrencyQueueLimit: 2, ConcurrencyQueueTimeout: time.Second, Limits: types.ExecutionLimits{MaxFileBytes: tc.bytes}})
			require.NoError(t, err)
			require.Equal(t, tc.wantConnectors, repo.orgConnectors)
			require.Equal(t, tc.wantFiles, repo.orgFiles)
			require.Equal(t, tc.file, repo.orgFileTransfer)
			if tc.file {
				require.Greater(t, time.Until(repo.created.DeadlineAt), 50*time.Second, "native files use the explicit allowance rather than the ordinary request timeout")
			}
		})
	}
	b := &Broker{nativeFiles: profile}
	for _, request := range []queriessvc.ConnectorExecutionRequest{
		{OrganizationSlug: "other", Limits: types.ExecutionLimits{MaxFileBytes: 1}},
		{OrganizationSlug: "fixture", Limits: types.ExecutionLimits{MaxFileBytes: 1025}},
	} {
		_, err := b.ExecuteConnector(t.Context(), request)
		require.ErrorIs(t, err, filestream.ErrUnavailable)
	}
	_, err := New(fakeTXM{}, nil, NewPollerDispatcher(), time.Second, nil, nil, testutil.FakeOperationAdmission{}, filestream.Profile{Organization: "fixture"})
	require.Error(t, err)
}

func TestNativeFilePipelineScopeOutlivesConnectorOperations(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	encoded, err := json.Marshal(encryptedResponse{Result: result.NewS3Result(&result.FileResult{Operation: "read", Format: "file"}, 0, nil)})
	require.NoError(t, err)
	repo := &fakeRequestRepo{request: requestrepo.Request{State: requestrepo.StateSucceeded, ResultCiphertext: mustEncrypt(t, cipher, encoded)}}
	profile := filestream.Profile{Organization: "fixture", MaxBytes: 16, Timeout: time.Minute, Transfers: 1, Connectors: 2}
	b := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, dispatcher: NewPollerDispatcher(), requestTimeout: time.Second, nativeFiles: profile, logger: slog.New(slog.DiscardHandler)}
	request := queriessvc.ConnectorExecutionRequest{OrganizationSlug: "fixture", DataSourceSlug: "source", ConcurrencyLimit: 2, ConcurrencyQueueLimit: 2, ConcurrencyQueueTimeout: time.Second, Limits: types.ExecutionLimits{MaxFileBytes: 16, MaxResponseBytes: 1024}}
	ctx, finishFiles := filestream.BeginAttempt(t.Context(), time.Now())
	defer finishFiles()
	scopeID, _ := filestream.AttemptID(ctx)
	for range 2 {
		_, err = b.ExecuteConnector(ctx, request)
		require.NoError(t, err)
		require.Equal(t, scopeID, repo.boundFileScope)
		require.Equal(t, filestream.Profile{Timeout: time.Minute}.Deadline(ctx, time.Now()), repo.boundFileDeadline)
		require.Zero(t, repo.releasedFileScopes.Load())
	}
	finishFiles()
	require.EqualValues(t, 1, repo.releasedFileScopes.Load())
	require.EqualValues(t, 1, repo.listedFileScopes.Load())
	_, err = b.ExecuteConnector(t.Context(), request)
	require.NoError(t, err)
	require.NotEqual(t, scopeID, repo.boundFileScope)
	require.EqualValues(t, 2, repo.releasedFileScopes.Load(), "standalone completion releases admission")
}

func TestTransferReleaseSignalsEveryBoundRequest(t *testing.T) {
	t.Parallel()
	repo := &fakeRequestRepo{pendingFileRequests: []uuid.UUID{uuid.New(), uuid.New()}}
	b := &Broker{txm: fakeTXM{}, repo: repo, logger: slog.New(slog.DiscardHandler)}
	b.releaseFileTransfer(t.Context(), uuid.New(), "fixture")
	require.EqualValues(t, 1, repo.releasedFileScopes.Load())
	require.EqualValues(t, 1, repo.listedFileScopes.Load())
	require.EqualValues(t, 2, repo.cancelCalls.Load())
}
