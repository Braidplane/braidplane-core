package http

import (
	"context"
	"fmt"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/Braidplane/braidplane-core/service/readinesssvc"
)

func (s *server) GetProjectReadiness(c fiber.Ctx, org api.SlugParam, project api.ProjectSlugParam) error {
	return s.getReadiness(c, org, project, "")
}

func (s *server) GetPipelineReadiness(c fiber.Ctx, org api.SlugParam, project api.ProjectSlugParam, pipeline api.PipelineSlugParam) error {
	pipelineSlug, err := slug.New(pipeline)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}
	return s.getReadiness(c, org, project, pipelineSlug)
}

func (s *server) getReadiness(c fiber.Ctx, org api.SlugParam, project api.ProjectSlugParam, pipeline slug.Slug) error {
	orgSlug, projectSlug, err := parseOrgProjectSlugs(org, project)
	if err != nil {
		return err
	}
	if err := s.canReadReadiness(c, orgSlug); err != nil {
		return err
	}
	if pipeline == "" {
		if _, err := s.projectsService.GetProject(c.Context(), orgSlug, projectSlug); err != nil {
			return mapServiceError(err)
		}
	}
	report, err := readinesssvc.Load(c.Context(), s.pipelinesService, s.environmentsService, s.dataSourceRefsService, s.readinessOperations(c, orgSlug), orgSlug, projectSlug, pipeline)
	if err != nil {
		var authErr *apierror.Error
		if xerror.As(err, &authErr) {
			return authErr
		}
		return mapServiceError(err)
	}
	return c.JSON(report)
}

// A binding may point to an organization-owned query outside the project grant.
func (s *server) readinessOperations(c fiber.Ctx, org slug.Slug) readinesssvc.OperationReader {
	return func(ctx context.Context, _, dataSource, operation slug.Slug) (*queriessvc.Query, error) {
		target := model.AuthorizationTarget{Kind: "queries", Slug: operation, DataSourceSlug: dataSource}
		// The Fiber request carries the operation and authenticated principal for this callback.
		if err := s.canTarget(c, org, model.Read, model.ResourceQueries, target); err != nil { //nolint:contextcheck
			return nil, err
		}
		return s.queriesService.FindForReadiness(ctx, org, dataSource, operation)
	}
}

func (s *server) canReadReadiness(c fiber.Ctx, org slug.Slug) error {
	for _, resource := range []model.Resource{model.ResourcePipelines, model.ResourceEnvironments, model.ResourceDataSources} {
		if err := s.canOrg(c, org, model.Read, resource); err != nil {
			return err
		}
	}
	return nil
}

func toAPIReadinessReport(report readinesssvc.Report) *api.ReadinessReport {
	result := &api.ReadinessReport{Status: api.ReadinessStatus(report.Status), Environments: make([]api.ReadinessEnvironment, len(report.Environments))}
	for i, env := range report.Environments {
		diagnostics := make([]api.ReadinessDiagnostic, len(env.Diagnostics))
		for j, d := range env.Diagnostics {
			diagnostics[j] = api.ReadinessDiagnostic{
				Severity: api.ReadinessDiagnosticSeverity(d.Severity), Code: d.Code, EnvironmentSlug: d.EnvironmentSlug.String(),
				PipelineSlug: d.PipelineSlug.String(), StepSlug: d.StepSlug.String(), RequirementKey: d.RequirementKey, FieldPath: d.FieldPath, NextAction: d.NextAction,
			}
		}
		result.Environments[i] = api.ReadinessEnvironment{EnvironmentSlug: env.EnvironmentSlug.String(), Status: api.ReadinessStatus(env.Status), Diagnostics: diagnostics}
	}
	return result
}
