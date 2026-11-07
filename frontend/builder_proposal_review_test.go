package frontend

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestBuildBuilderProposalReviewShowsExactGitTargetAndLastKnownState(t *testing.T) {
	t.Parallel()

	var pipeline api.Pipeline
	require.NoError(t, json.Unmarshal([]byte(`{
  "slug": "customer-sync",
  "name": "Customer Sync",
  "has_draft": true,
  "sync_status": "synced",
  "source_path": "pipelines/customer-sync.pipeline.json",
  "source_ref": "refs/heads/main",
  "last_applied_commit": "abc123",
  "created_at": "2026-08-13T08:00:00Z",
  "updated_at": "2026-08-13T08:30:00Z",
  "spec": {
    "metadata": {"slug": "customer-sync", "name": "Customer Sync"},
    "steps": [{"kind": "query", "slug": "fetch-users", "query": {"data_source_ref": "warehouse", "operation_type": "sql", "operation_ref": "users"}}]
  },
  "draft": {
    "pipeline_slug": "customer-sync",
	"initiating_member": "member-a",
    "draft_state": "proposal_open",
    "draft_spec_hash": "draft-hash",
    "repository": "https://github.com/acme/pipelines.git",
    "base_ref": "refs/heads/main",
    "base_commit_sha": "abc123",
    "branch_name": "braidplane/default/customer-sync/member-a",
    "branch_head_sha": "def456",
    "proposal_provider": "github",
    "proposal_id": "42",
    "proposal_url": "https://github.com/acme/pipelines/pull/42",
    "proposal_state": "open",
    "proposal_is_stale": true,
    "proposal_supported": true,
	"publication_mode": "delegated_user",
	"provider_actor": "Alice Provider",
	"authority_source": "delegated OAuth grant",
    "last_proposed_draft_hash": "older-hash",
    "proposal_updated_at": "2026-08-13T08:25:00Z",
    "created_at": "2026-08-13T08:10:00Z",
    "updated_at": "2026-08-13T08:30:00Z",
    "spec": {
      "metadata": {"slug": "customer-sync", "name": "Customer Sync"},
      "steps": [{"kind": "query", "slug": "fetch-users", "name": "Fetch active users", "query": {"data_source_ref": "warehouse", "operation_type": "sql", "operation_ref": "users"}}]
    }
  }
}`), &pipeline))

	review, err := buildBuilderProposalReview(&pipeline)
	require.NoError(t, err)
	require.Equal(t, "https://github.com/acme/pipelines.git", review.Repository)
	require.Equal(t, "member-a", review.InitiatingMember)
	require.Equal(t, "delegated_user", review.PublicationMode)
	require.Equal(t, "Alice Provider", review.ProviderActor)
	require.Equal(t, "Configured to publish as Alice Provider via Braidplane.", review.Attribution)
	require.Equal(t, "pipelines/customer-sync.pipeline.json", review.SourcePath)
	require.Equal(t, "refs/heads/main", review.BaseRef)
	require.Equal(t, "abc123", review.BaseCommitSHA)
	require.Empty(t, review.BaseSpecHash)
	require.Equal(t, "braidplane/default/customer-sync/member-a", review.BranchName)
	require.Equal(t, "42", review.ProposalID)
	require.True(t, review.ProviderSupported)
	require.True(t, review.ProposalIsStale)
	require.Equal(t, "open", review.ProposalState)
	require.Equal(t, "2026-08-13T08:25:00Z (last known)", review.StatusObservedAt)
	require.False(t, review.MatchesCanonicalSource)
	require.Contains(t, review.StructuralDiff, "Fetch active users")
	require.Contains(t, review.SourceDiff, "pipelines/customer-sync.pipeline.json (proposal)")
	require.JSONEq(t, `{"metadata":{"slug":"customer-sync","name":"Customer Sync"},"steps":[{"kind":"query","slug":"fetch-users","name":"Fetch active users","query":{"data_source_ref":"warehouse","operation_type":"sql","operation_ref":"users"}}]}`, review.ProposedSource)
}

func TestBuildBuilderProposalReviewDisablesGenericGitPublication(t *testing.T) {
	t.Parallel()

	var pipeline api.Pipeline
	require.NoError(t, json.Unmarshal([]byte(`{
  "slug":"customer-sync","name":"Customer Sync","has_draft":true,"sync_status":"synced",
  "source_path":"pipelines/customer-sync.pipeline.yaml","source_ref":"refs/heads/main",
  "created_at":"2026-08-13T08:00:00Z","updated_at":"2026-08-13T08:30:00Z",
  "spec":{"metadata":{"slug":"customer-sync","name":"Customer Sync"},"steps":[{"kind":"query","slug":"fetch-users","query":{"data_source_ref":"warehouse","operation_type":"sql","operation_ref":"users"}}]},
  "draft":{"pipeline_slug":"customer-sync","initiating_member":"member-a","draft_state":"editing","draft_spec_hash":"draft-hash","repository":"ssh://git@git.example.com/acme/pipelines.git","proposal_is_stale":false,"proposal_supported":false,"created_at":"2026-08-13T08:10:00Z","updated_at":"2026-08-13T08:30:00Z","spec":{"metadata":{"slug":"customer-sync","name":"Customer Sync"},"steps":[{"kind":"query","slug":"fetch-users","query":{"data_source_ref":"warehouse","operation_type":"sql","operation_ref":"users"}}]}}
}`), &pipeline))

	review, err := buildBuilderProposalReview(&pipeline)
	require.NoError(t, err)
	require.False(t, review.ProviderSupported)
	require.True(t, review.MatchesCanonicalSource)
	require.True(t, proposalReviewView(review).MatchesCanonicalSource)
}

func TestProposalAttributionDistinguishesOrganizationServiceIdentity(t *testing.T) {
	t.Parallel()

	mode := api.PipelineDraftPublicationMode("organization_service")
	actor := "Braidplane Service"
	draft := &api.PipelineDraft{
		InitiatingMember: "member-a",
		PublicationMode:  &mode,
		ProviderActor:    &actor,
	}

	require.Equal(
		t,
		"Configured for Braidplane to publish for member-a using provider actor Braidplane Service.",
		proposalAttribution(draft),
	)
}
