package consumer

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	proofrepo "github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/service/proofwebhooksvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestProofWebhookWorkerPersistsAndLogsOnlySafeFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		processErr error
		wantCancel bool
		wantCode   string
		wantErr    error
	}{
		{name: "Retryable", processErr: gitprovider.ErrChangeUnavailable.Detail("raw provider response secret"), wantCode: proofwebhooksvc.ErrProcessingUnavailable.Code(), wantErr: proofwebhooksvc.ErrProcessingUnavailable},
		{name: "Close before ingest commit", processErr: proofrepo.ErrCandidateNotFound, wantCode: proofwebhooksvc.ErrProcessingUnavailable.Code(), wantErr: proofwebhooksvc.ErrProcessingUnavailable},
		{name: "Terminal", processErr: gitprovider.ErrChangeStale.Detail("raw provider response secret"), wantCancel: true, wantCode: proofwebhooksvc.ErrDeliveryRejected.Code(), wantErr: proofwebhooksvc.ErrDeliveryRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			worker, err := NewProofWebhookWorker(
				proofWebhookProcessorFunc(func(context.Context, gitprovider.VerifiedChangeEvent) error { return tc.processErr }),
				slog.New(slog.NewJSONHandler(&logs, nil)),
			)
			require.NoError(t, err)
			err = worker.Work(t.Context(), &river.Job[jobs.ProofWebhookArgs]{Args: proofWebhookJobArgs()})
			require.Error(t, err)
			var cancelErr *rivertype.JobCancelError
			if tc.wantCancel {
				require.ErrorAs(t, err, &cancelErr)
			} else {
				require.NotErrorAs(t, err, &cancelErr)
			}
			require.ErrorIs(t, err, tc.wantErr)
			require.NotContains(t, err.Error(), "raw provider response secret")
			require.NotContains(t, logs.String(), "raw provider response secret")
			require.Contains(t, logs.String(), tc.wantCode)
		})
	}
}

func TestProofWebhookWorkerSucceeds(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	worker, err := NewProofWebhookWorker(
		proofWebhookProcessorFunc(func(context.Context, gitprovider.VerifiedChangeEvent) error { return nil }),
		slog.New(slog.NewJSONHandler(&logs, nil)),
	)
	require.NoError(t, err)
	require.NoError(t, worker.Work(t.Context(), &river.Job[jobs.ProofWebhookArgs]{Args: proofWebhookJobArgs()}))
	require.Contains(t, logs.String(), "proof webhook job succeeded")
}

func TestNewProofWebhookWorkerRequiresProcessor(t *testing.T) {
	t.Parallel()
	worker, err := NewProofWebhookWorker(nil, nil)
	require.ErrorIs(t, err, proofwebhooksvc.ErrProcessingUnavailable)
	require.Nil(t, worker)
}

type proofWebhookProcessorFunc func(context.Context, gitprovider.VerifiedChangeEvent) error

func (f proofWebhookProcessorFunc) Process(ctx context.Context, event gitprovider.VerifiedChangeEvent) error {
	return f(ctx, event)
}

func proofWebhookJobArgs() jobs.ProofWebhookArgs {
	return jobs.ProofWebhookArgs{
		DeliveryID: "delivery-1", Action: gitprovider.ChangeActionUpsert,
		Provider: repostore.ProviderGitHub, ProviderConnectionID: "9001",
		RepositoryID: "42", ChangeRequestID: "7",
		HeadSHA:   "a123456789012345678901234567890123456789",
		SourceRef: "refs/heads/feature", TargetRef: "refs/heads/main",
	}
}
