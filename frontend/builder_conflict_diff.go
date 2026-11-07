package frontend

import (
	"context"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

type builderConflictDiff struct {
	CanonicalComparableJSON string `json:"canonical_comparable_json"`
	DraftComparableJSON     string `json:"draft_comparable_json"`
	ComparableUnifiedDiff   string `json:"comparable_unified_diff"`
}

type builderDraftConflictResponse struct {
	api.PipelineDraftConflictResponse
	ConflictDiff *builderConflictDiff `json:"conflict_diff,omitempty"`
}

func (r builderDraftConflictResponse) hasNoComparableDiff() bool {
	if r.ConflictDiff == nil {
		return false
	}
	return r.ConflictDiff.CanonicalComparableJSON == r.ConflictDiff.DraftComparableJSON
}

func buildBuilderConflictDiff(
	canonicalState builderState,
	draftState builderState,
) (*builderConflictDiff, error) {
	canonicalComparable, err := serializeBuilderStateComparable(canonicalState)
	if err != nil {
		return nil, err
	}

	draftComparable, err := serializeBuilderStateComparable(draftState)
	if err != nil {
		return nil, err
	}

	return &builderConflictDiff{
		CanonicalComparableJSON: string(canonicalComparable),
		DraftComparableJSON:     string(draftComparable),
		ComparableUnifiedDiff: unifiedLineDiff(
			"canonical",
			"draft",
			canonicalComparable,
			draftComparable,
			3,
		),
	}, nil
}

func (r *Router) enrichBuilderConflictResponseWithComparableDiff(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	pipelineSlug string,
	conflictResponse *api.PipelineDraftConflictResponse,
	draftState builderState,
) any {
	if conflictResponse == nil {
		return nil
	}

	currentPipeline, status, errorResponse, err := r.getPipelineViaAPI(
		ctx,
		cookieHeader,
		organizationSlug,
		projectSlug,
		pipelineSlug,
	)
	if err != nil {
		r.logger.Warn(
			"failed to fetch canonical pipeline for builder conflict diff",
			"organization_slug",
			organizationSlug,
			"pipeline_slug",
			pipelineSlug,
			"status",
			status,
			"api_error",
			messageFromErrorResponse(errorResponse),
			"error",
			err,
		)
		return conflictResponse
	}

	canonicalState, err := loadCanonicalBuilderStateFromAPIPipeline(currentPipeline)
	if err != nil {
		r.logger.Warn(
			"failed to load canonical builder state for conflict diff",
			"organization_slug",
			organizationSlug,
			"pipeline_slug",
			pipelineSlug,
			"error",
			err,
		)
		return conflictResponse
	}

	diff, err := buildBuilderConflictDiff(canonicalState, draftState)
	if err != nil {
		r.logger.Warn(
			"failed to build comparable builder conflict diff",
			"organization_slug",
			organizationSlug,
			"pipeline_slug",
			pipelineSlug,
			"error",
			err,
		)
		return conflictResponse
	}

	return builderDraftConflictResponse{
		PipelineDraftConflictResponse: *conflictResponse,
		ConflictDiff:                  diff,
	}
}

func (r *Router) enrichBuilderPublishConflictResponseWithComparableDiff(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	pipelineSlug string,
	conflictResponse *api.PipelineDraftConflictResponse,
) any {
	if conflictResponse == nil {
		return nil
	}

	currentPipeline, status, errorResponse, err := r.getPipelineViaAPI(
		ctx,
		cookieHeader,
		organizationSlug,
		projectSlug,
		pipelineSlug,
	)
	if err != nil {
		r.logger.Warn(
			"failed to fetch pipeline for publish conflict diff",
			"organization_slug",
			organizationSlug,
			"pipeline_slug",
			pipelineSlug,
			"status",
			status,
			"api_error",
			messageFromErrorResponse(errorResponse),
			"error",
			err,
		)
		return conflictResponse
	}

	canonicalState, err := loadCanonicalBuilderStateFromAPIPipeline(currentPipeline)
	if err != nil {
		r.logger.Warn(
			"failed to load canonical builder state for publish conflict diff",
			"organization_slug",
			organizationSlug,
			"pipeline_slug",
			pipelineSlug,
			"error",
			err,
		)
		return conflictResponse
	}

	draftState, err := loadDraftBuilderStateFromAPIPipeline(currentPipeline)
	if err != nil {
		r.logger.Warn(
			"failed to load draft builder state for publish conflict diff",
			"organization_slug",
			organizationSlug,
			"pipeline_slug",
			pipelineSlug,
			"error",
			err,
		)
		return conflictResponse
	}

	diff, err := buildBuilderConflictDiff(canonicalState, draftState)
	if err != nil {
		r.logger.Warn(
			"failed to build comparable publish conflict diff",
			"organization_slug",
			organizationSlug,
			"pipeline_slug",
			pipelineSlug,
			"error",
			err,
		)
		return conflictResponse
	}

	return builderDraftConflictResponse{
		PipelineDraftConflictResponse: *conflictResponse,
		ConflictDiff:                  diff,
	}
}
