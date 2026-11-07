package http

import (
	"context"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/gofiber/fiber/v3"
)

type pipelineCreationService interface {
	List(context.Context, slug.Slug, slug.Slug, slug.Slug, int) (*pipelinesvc.CreationProposalPage, error)
	Create(context.Context, slug.Slug, slug.Slug, slug.Slug, model.EventActor, *pipelinespec.Pipeline) (*pipelinesvc.CreationProposal, error)
	Get(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinesvc.CreationProposal, error)
}

func (s *server) ListPipelineCreationProposals(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, params api.ListPipelineCreationProposalsParams) error {
	org, project, err := parseOrgProjectSlugs(orgParam, projectParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	var after slug.Slug
	if params.Cursor != nil {
		after, err = slug.New(*params.Cursor)
		if err != nil {
			return apierror.ValidationFailed("invalid creation cursor")
		}
	}
	limit := 20
	if params.Limit != nil {
		limit = int(*params.Limit)
	}
	page, err := s.pipelineCreationsService.List(c.Context(), org, project, after, limit)
	if err != nil {
		return mapServiceError(err)
	}
	response := api.PipelineCreationProposalListResponse{PipelineCreations: make([]api.PipelineCreationProposal, 0, len(page.Items)), HasMore: page.HasMore}
	for _, proposal := range page.Items {
		item, err := pipelineCreationToHTTP(proposal)
		if err != nil {
			return mapServiceError(err)
		}
		response.PipelineCreations = append(response.PipelineCreations, item)
	}
	if page.HasMore && len(page.Items) > 0 {
		cursor := page.Items[len(page.Items)-1].Slug.String()
		response.NextCursor = &cursor
	}
	return c.JSON(response)
}

func (s *server) CreatePipelineProposal(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam) error {
	org, project, err := parseOrgProjectSlugs(orgParam, projectParam)
	if err != nil {
		return err
	}
	member, err := s.canOrgUserPrincipal(c, org, model.Create, model.ResourcePipelines)
	if err != nil {
		return err
	}
	spec, err := loadPipelineFromRawSpec("pipeline.pipeline.json", c.Body())
	if err != nil {
		return mapServiceError(err)
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, org.String(), "pipeline.git", false); err != nil {
		return err
	}
	proposal, err := s.pipelineCreationsService.Create(c.Context(), org, project, member, s.eventActor(c), spec)
	if err != nil {
		return mapServiceError(err)
	}
	result, err := pipelineCreationToHTTP(proposal)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(result)
}

func (s *server) GetPipelineCreationProposal(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, creationParam api.Slug) error {
	org, project, err := parseOrgProjectSlugs(orgParam, projectParam)
	if err != nil {
		return err
	}
	creation, err := slug.New(creationParam)
	if err != nil {
		return apierror.ValidationFailed("invalid pipeline creation slug")
	}
	if err := s.canOrg(c, org, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	proposal, err := s.pipelineCreationsService.Get(c.Context(), org, project, creation)
	if err != nil {
		return mapServiceError(err)
	}
	result, err := pipelineCreationToHTTP(proposal)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(result)
}

func pipelineCreationToHTTP(p *pipelinesvc.CreationProposal) (api.PipelineCreationProposal, error) {
	if p.Spec == nil {
		return api.PipelineCreationProposal{}, xerror.Unexpected().Detail("pipeline creation definition is missing")
	}
	spec, err := pipelineSpecToAPI(p.Spec)
	if err != nil {
		return api.PipelineCreationProposal{}, err
	}
	result := api.PipelineCreationProposal{Slug: p.Slug.String(), ProjectSlug: p.ProjectSlug.String(), PipelineSlug: p.PipelineSlug.String(), InitiatingMember: p.MemberSlug.String(), SourcePath: p.SourcePath, SpecHash: p.SpecHash, Status: api.PipelineCreationProposalStatus(p.Status)}
	result.Spec = spec
	if p.FailureCode != "" {
		result.FailureCode = &p.FailureCode
	}
	if p.Proposal != nil {
		result.ProposalUrl, result.ProposalProvider, result.ProposalState = optionalString(p.Proposal.ProposalURL), optionalString(p.Proposal.Provider), optionalString(p.Proposal.ProposalState)
		result.BranchName, result.BranchHeadSha = optionalString(p.Proposal.BranchName), optionalString(p.Proposal.BranchHeadSHA)
	}
	return result, nil
}
