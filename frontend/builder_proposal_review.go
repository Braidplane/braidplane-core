package frontend

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type builderProposalReviewResponse struct {
	MatchesCanonicalSource bool   `json:"matches_canonical_source"`
	Repository             string `json:"repository"`
	InitiatingMember       string `json:"initiating_member"`
	PublicationMode        string `json:"publication_mode"`
	ProviderActor          string `json:"provider_actor"`
	AuthoritySource        string `json:"authority_source"`
	Attribution            string `json:"attribution"`
	SourcePath             string `json:"source_path"`
	BaseRef                string `json:"base_ref"`
	BaseCommitSHA          string `json:"base_commit_sha"`
	BaseSpecHash           string `json:"base_spec_hash"`
	BranchName             string `json:"branch_name"`
	BranchHeadSHA          string `json:"branch_head_sha"`
	DraftState             string `json:"draft_state"`
	ProposalProvider       string `json:"proposal_provider"`
	ProposalID             string `json:"proposal_id"`
	ProposalURL            string `json:"proposal_url"`
	ProposalState          string `json:"proposal_state"`
	ProposalIsStale        bool   `json:"proposal_is_stale"`
	ProviderSupported      bool   `json:"provider_supported"`
	StatusObservedAt       string `json:"status_observed_at"`
	StructuralDiff         string `json:"structural_diff"`
	SourceDiff             string `json:"source_diff"`
	ProposedSource         string `json:"proposed_source"`
	CurrentSpecHash        string `json:"current_spec_hash"`
	DraftSpecHash          string `json:"draft_spec_hash"`
	LastProposedSpecHash   string `json:"last_proposed_spec_hash"`
}

func buildBuilderProposalReview(pipeline *api.Pipeline) (*builderProposalReviewResponse, error) {
	if pipeline == nil || pipeline.Draft == nil {
		return nil, xerror.NotFound(xerror.Pipeline).Detail("pipeline proposal draft is required")
	}

	canonicalState, err := loadCanonicalBuilderStateFromAPIPipeline(pipeline)
	if err != nil {
		return nil, xerror.Wrapf("load canonical pipeline for proposal review: %w", err)
	}
	draftState, err := loadDraftBuilderStateFromAPIPipeline(pipeline)
	if err != nil {
		return nil, xerror.Wrapf("load draft pipeline for proposal review: %w", err)
	}
	structural, err := buildBuilderConflictDiff(canonicalState, draftState)
	if err != nil {
		return nil, xerror.Wrapf("build structural proposal diff: %w", err)
	}

	format, err := builderPipelineFormatFromPath(pipeline.SourcePath)
	if err != nil {
		return nil, xerror.Wrapf("resolve pipeline source format for proposal review: %w", err)
	}
	canonicalSource, err := serializeBuilderPipelineSpec(canonicalState, format)
	if err != nil {
		return nil, xerror.Wrapf("render canonical proposal source: %w", err)
	}
	proposedSource, err := serializeBuilderPipelineSpec(draftState, format)
	if err != nil {
		return nil, xerror.Wrapf("render proposed pipeline source: %w", err)
	}

	draft := pipeline.Draft
	return &builderProposalReviewResponse{
		// Canonical and draft hashes use different identities; compare their rendered definitions.
		MatchesCanonicalSource: bytes.Equal(canonicalSource, proposedSource),
		Repository:             draft.Repository,
		InitiatingMember:       draft.InitiatingMember,
		PublicationMode:        stringValue((*string)(draft.PublicationMode)),
		ProviderActor:          stringValue(draft.ProviderActor),
		AuthoritySource:        stringValue(draft.AuthoritySource),
		Attribution:            proposalAttribution(draft),
		SourcePath:             pipeline.SourcePath,
		BaseRef:                firstString(draft.BaseRef, &pipeline.SourceRef),
		BaseCommitSHA:          firstString(draft.BaseCommitSha, &pipeline.LastAppliedCommit),
		BaseSpecHash:           stringValue(draft.BasedOnSpecHash),
		BranchName:             stringValue(draft.BranchName),
		BranchHeadSHA:          stringValue(draft.BranchHeadSha),
		DraftState:             string(draft.DraftState),
		ProposalProvider:       proposalProviderString(draft.ProposalProvider),
		ProposalID:             stringValue(draft.ProposalId),
		ProposalURL:            stringValue(draft.ProposalUrl),
		ProposalState:          proposalStateString(draft.ProposalState),
		ProposalIsStale:        draft.ProposalIsStale,
		ProviderSupported:      draft.ProposalSupported,
		StatusObservedAt:       proposalObservedAt(draft),
		StructuralDiff:         structural.ComparableUnifiedDiff,
		SourceDiff: unifiedLineDiff(
			pipeline.SourcePath+" (materialized)",
			pipeline.SourcePath+" (proposal)",
			canonicalSource,
			proposedSource,
			3,
		),
		ProposedSource:       string(proposedSource),
		CurrentSpecHash:      stringValue(pipeline.CurrentVersionSpecHash),
		DraftSpecHash:        draft.DraftSpecHash,
		LastProposedSpecHash: stringValue(draft.LastProposedDraftHash),
	}, nil
}

func proposalReviewView(review *builderProposalReviewResponse) *views.BuilderProposalReview {
	if review == nil {
		return nil
	}
	return &views.BuilderProposalReview{
		MatchesCanonicalSource: review.MatchesCanonicalSource,
		Repository:             review.Repository,
		InitiatingMember:       review.InitiatingMember,
		PublicationMode:        review.PublicationMode,
		ProviderActor:          review.ProviderActor,
		AuthoritySource:        review.AuthoritySource,
		Attribution:            review.Attribution,
		SourcePath:             review.SourcePath,
		BaseRef:                review.BaseRef,
		BaseCommitSHA:          review.BaseCommitSHA,
		BranchName:             review.BranchName,
		BranchHeadSHA:          review.BranchHeadSHA,
		DraftState:             review.DraftState,
		ProposalProvider:       review.ProposalProvider,
		ProposalID:             review.ProposalID,
		ProposalURL:            review.ProposalURL,
		ProposalState:          review.ProposalState,
		ProposalIsStale:        review.ProposalIsStale,
		ProviderSupported:      review.ProviderSupported,
		StatusObservedAt:       review.StatusObservedAt,
	}
}

func proposalAttribution(draft *api.PipelineDraft) string {
	if draft == nil || draft.PublicationMode == nil {
		return "Provider identity is selected when publication is queued."
	}
	if *draft.PublicationMode == api.PipelineDraftPublicationMode("repository_credential") {
		return fmt.Sprintf("Configured to save a Git proposal branch for %s using the project's Git connection.", draft.InitiatingMember)
	}
	actor := stringValue(draft.ProviderActor)
	if *draft.PublicationMode == api.PipelineDraftPublicationMode("delegated_user") {
		return fmt.Sprintf("Configured to publish as %s via Braidplane.", actor)
	}
	return fmt.Sprintf("Configured for Braidplane to publish for %s using provider actor %s.", draft.InitiatingMember, actor)
}

func firstString(values ...*string) string {
	for _, value := range values {
		if value != nil && strings.TrimSpace(*value) != "" {
			return strings.TrimSpace(*value)
		}
	}
	return ""
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func proposalProviderString(value *api.PipelineDraftProposalProvider) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func proposalStateString(value *api.PipelineDraftProposalState) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func proposalObservedAt(draft *api.PipelineDraft) string {
	if draft == nil {
		return "Not observed yet"
	}
	if draft.DraftState == api.PipelineDraftDraftState("publish_failed") && draft.FailedAt != nil {
		return fmt.Sprintf("%s (failure recorded)", draft.FailedAt.UTC().Format(time.RFC3339))
	}
	if draft.DraftState == api.PipelineDraftDraftState("publish_queued") && draft.QueuedAt != nil {
		return fmt.Sprintf("%s (queued)", draft.QueuedAt.UTC().Format(time.RFC3339))
	}
	value := draft.ProposalUpdatedAt
	if value == nil {
		return "Not observed yet"
	}
	return fmt.Sprintf("%s (last known)", value.UTC().Format(time.RFC3339))
}
