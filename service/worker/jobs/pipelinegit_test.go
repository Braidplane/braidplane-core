package jobs

import (
	"encoding/json"
	"testing"

	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
)

func TestPipelineGitJobArgsExposeStableQueueAndKinds(t *testing.T) {
	t.Parallel()

	var writeArgs queue.JobArgs = PipelineGitWriteFileArgs{}
	var deleteArgs queue.JobArgs = PipelineGitDeleteFileArgs{}
	var publishProposalArgs queue.JobArgs = PipelineGitPublishProposalArgs{}

	require.Equal(t, "pipeline_git", PipelineGitQueue)
	require.Equal(t, "pipeline_git_write_file", writeArgs.Kind())
	require.Equal(t, "pipeline_git_delete_file", deleteArgs.Kind())
	require.Equal(t, "pipeline_git_publish_proposal", publishProposalArgs.Kind())

	writeOpts := PipelineGitWriteFileArgs{}.InsertOpts()
	require.Equal(t, PipelineGitQueue, writeOpts.Queue)
	require.False(t, writeOpts.UniqueOpts.ByArgs)

	deleteOpts := PipelineGitDeleteFileArgs{}.InsertOpts()
	require.Equal(t, PipelineGitQueue, deleteOpts.Queue)
	require.False(t, deleteOpts.UniqueOpts.ByArgs)

	publishProposalOpts := PipelineGitPublishProposalArgs{}.InsertOpts()
	require.Equal(t, PipelineGitQueue, publishProposalOpts.Queue)
	require.True(t, publishProposalOpts.UniqueOpts.ByArgs)
	require.ElementsMatch(t, rivertype.UniqueOptsByStateDefault(), publishProposalOpts.UniqueOpts.ByState)
}

func TestPipelineGitPublishProposalArgsExcludeProviderCredentialIdentity(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(PipelineGitPublishProposalArgs{Authority: model.PipelineProposalAuthorityPayload{
		ID: 1, ProviderAccountID: "account-1",
	}})

	require.NoError(t, err)
	require.NotContains(t, string(data), "provider_credential_id")
}
