package jobs

import (
	"encoding/json"
	"testing"

	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
)

func TestProofWebhookArgsAreDeliveryUniqueAndSecretFree(t *testing.T) {
	t.Parallel()
	args := ProofWebhookArgs{
		DeliveryID: "delivery-1", Action: gitprovider.ChangeActionUpsert,
		Provider: repostore.ProviderGitHub, ProviderConnectionID: "9001",
		RepositoryID: "42", ChangeRequestID: "7",
		HeadSHA:   "a123456789012345678901234567890123456789",
		SourceRef: "refs/heads/feature", TargetRef: "refs/heads/main",
	}
	opts := args.InsertOpts()
	require.Equal(t, PipelineGitQueue, opts.Queue)
	require.True(t, opts.UniqueOpts.ByArgs)
	require.ElementsMatch(t, rivertype.UniqueOptsByStateDefault(), opts.UniqueOpts.ByState)
	encoded, err := json.Marshal(args)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"action":"upsert"`)
	for _, forbidden := range []string{"payload", "signature", "token", "secret", "organization_slug", "pipeline_path"} {
		require.NotContains(t, string(encoded), forbidden)
	}
	require.Equal(t, args.DeliveryID, args.Event().DeliveryID)
}
