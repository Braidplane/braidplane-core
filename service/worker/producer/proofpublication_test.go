package producer

import (
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/stretchr/testify/require"
)

func TestProofPublicationQueuesOnlyProtectedReceiptSelectors(t *testing.T) {
	t.Parallel()
	enqueuer := &recordingEnqueuer{}
	producer, err := NewProofPublicationProducer(enqueuer)
	require.NoError(t, err)
	receipt := model.ProofReceipt{ID: "proof-1", Candidate: model.ProofCandidateIdentity{OrganizationSlug: "acme", ProjectSlug: "data", EnvironmentSlug: "staging"}, Claims: []model.ProofClaim{{Description: "private-evidence"}}}
	require.NoError(t, producer.EnqueueProofReceipt(t.Context(), nil, receipt))
	require.Equal(t, jobs.PipelineGitQueue, enqueuer.req.Queue)
	raw, err := json.Marshal(enqueuer.req.Args)
	require.NoError(t, err)
	require.JSONEq(t, `{"organization":"acme","project":"data","environment":"staging","receipt":"proof-1"}`, string(raw))
}
