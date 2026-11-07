package http

import (
	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

func (s *server) ImportODCSArtifacts(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam) error {
	org, project, err := parseProjectPath(orgParam, projectParam)
	if err != nil {
		return err
	}
	// Preview also reads the repository through the project's configured credential.
	if err := s.canOrg(c, org, model.Create, model.ResourceProjects); err != nil {
		return err
	}
	var input api.ImportODCSArtifactsJSONRequestBody
	if err := bindODCSBody(c, &input); err != nil {
		return err
	}
	result, err := s.odcsArtifactImporter.Import(c.Context(), org, project, input.Apply, input.ExpectedCommit)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(result)
}

func (s *server) GetODCSArtifact(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, contractParam api.ContractSlugParam, revisionParam api.RevisionSlugParam, params api.GetODCSArtifactParams) error {
	org, project, contract, err := parseODCSContractPath(orgParam, projectParam, contractParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceProjects); err != nil {
		return err
	}
	revision, err := slug.New(revisionParam)
	if err != nil {
		return apierror.ValidationFailed("Invalid revision slug")
	}
	result, err := s.odcsArtifactImporter.Get(c.Context(), org, project, contract, revision, params.SourceRepo, params.Commit, params.Path)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(result)
}
