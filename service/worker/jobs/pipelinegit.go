package jobs

import (
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const PipelineGitQueue = "pipeline_git"

var ErrPipelineProposalAuthorityDenied = xerror.New(
	xerror.KindUnauthorized,
	"PIPELINE_PROPOSAL_AUTHORITY_DENIED",
	"pipeline proposal provider authority is unavailable or revoked",
)

// PipelineGitWriteFileArgs is the durable River payload for writing a pipeline file.
type PipelineGitWriteFileArgs struct {
	InitiatingUserSlug slug.Slug `json:"initiating_user_slug,omitempty"`
	OrganizationSlug   slug.Slug `json:"organization_slug"`
	DataSourceSlug     slug.Slug `json:"data_source_slug"`
	PipelineSlug       slug.Slug `json:"pipeline_slug"`
	Branch             *string   `json:"branch,omitempty"`
	Path               string    `json:"path"`
	Content            []byte    `json:"content"`
	CommitMessage      string    `json:"commit_message"`
	FailIfExists       bool      `json:"fail_if_exists,omitempty"`
}

func (PipelineGitWriteFileArgs) Kind() string {
	return "pipeline_git_write_file"
}

func (PipelineGitWriteFileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: PipelineGitQueue}
}

// PipelineGitDeleteFileArgs is the durable River payload for deleting a pipeline file.
type PipelineGitDeleteFileArgs struct {
	InitiatingUserSlug slug.Slug `json:"initiating_user_slug,omitempty"`
	OrganizationSlug   slug.Slug `json:"organization_slug"`
	DataSourceSlug     slug.Slug `json:"data_source_slug"`
	PipelineSlug       slug.Slug `json:"pipeline_slug"`
	Branch             *string   `json:"branch,omitempty"`
	Path               string    `json:"path"`
	CommitMessage      string    `json:"commit_message"`
}

func (PipelineGitDeleteFileArgs) Kind() string {
	return "pipeline_git_delete_file"
}

func (PipelineGitDeleteFileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: PipelineGitQueue}
}

// PipelineGitPublishProposalArgs is the durable River payload for publishing a Git-authored draft proposal.
type PipelineGitPublishProposalArgs struct {
	CreationSlug     slug.Slug                              `json:"creation_slug,omitempty"`
	OrganizationSlug slug.Slug                              `json:"organization_slug"`
	ProjectSlug      slug.Slug                              `json:"project_slug"`
	DataSourceSlug   slug.Slug                              `json:"data_source_slug"`
	PipelineSlug     slug.Slug                              `json:"pipeline_slug"`
	MemberSlug       slug.Slug                              `json:"member_slug"`
	BaseRef          *string                                `json:"base_ref,omitempty"`
	BaseCommitSHA    *string                                `json:"base_commit_sha,omitempty"`
	ExpectedHeadSHA  *string                                `json:"expected_head_sha,omitempty"`
	BranchName       string                                 `json:"branch_name"`
	Path             string                                 `json:"path"`
	Content          []byte                                 `json:"content"`
	CommitMessage    string                                 `json:"commit_message"`
	ProposalTitle    string                                 `json:"proposal_title"`
	ProposalBody     string                                 `json:"proposal_body"`
	DraftSpecHash    string                                 `json:"draft_spec_hash"`
	PublishAttemptID string                                 `json:"publish_attempt_id" river:"unique"`
	Authority        model.PipelineProposalAuthorityPayload `json:"authority"`
}

// PipelineGitRefreshProposalArgs refreshes one exact provider PR/MR into member-scoped draft state.
type PipelineGitRefreshProposalArgs struct {
	OrganizationSlug slug.Slug `json:"organization_slug"`
	ProjectSlug      slug.Slug `json:"project_slug"`
	DataSourceSlug   slug.Slug `json:"data_source_slug"`
	PipelineSlug     slug.Slug `json:"pipeline_slug"`
	MemberSlug       slug.Slug `json:"member_slug"`
	ProposalID       string    `json:"proposal_id"`
	RefreshAttemptID string    `json:"refresh_attempt_id"`
}

func (PipelineGitRefreshProposalArgs) Kind() string { return "pipeline_git_refresh_proposal" }

func (PipelineGitRefreshProposalArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: PipelineGitQueue}
}

func (PipelineGitPublishProposalArgs) Kind() string {
	return "pipeline_git_publish_proposal"
}

func (PipelineGitPublishProposalArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:      PipelineGitQueue,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: rivertype.UniqueOptsByStateDefault()},
	}
}
