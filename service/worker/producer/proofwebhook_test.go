package producer

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestProofWebhookProducerEnqueuesNormalizedDelivery(t *testing.T) {
	t.Parallel()
	enqueuer := &recordingEnqueuer{queued: queue.QueuedJob{Kind: "proof_webhook", Queue: jobs.PipelineGitQueue, State: "queued"}}
	producer, err := NewProofWebhookProducer(enqueuer)
	require.NoError(t, err)
	event := gitprovider.VerifiedChangeEvent{
		DeliveryID: "delivery-1", Action: gitprovider.ChangeActionUpsert,
		Provider: repostore.ProviderGitHub, ProviderConnectionID: "9001",
		RepositoryID: "42", ChangeRequestID: "7",
		HeadSHA:   "a123456789012345678901234567890123456789",
		SourceRef: "refs/heads/feature", TargetRef: "refs/heads/main",
	}
	queued, err := producer.EnqueueProofChange(t.Context(), nil, event)
	require.NoError(t, err)
	require.Equal(t, enqueuer.queued, queued)
	require.Equal(t, jobs.PipelineGitQueue, enqueuer.req.Queue)
	require.Equal(t, event, enqueuer.req.Args.(jobs.ProofWebhookArgs).Event())
}
