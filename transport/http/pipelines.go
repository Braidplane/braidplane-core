package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	pipelinedrift "github.com/Braidplane/braidplane-core/pkg/pipeline/drift"
	pipelineformat "github.com/Braidplane/braidplane-core/pkg/pipeline/format"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelineruntimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

// ListPipelines returns persisted pipeline references for an organization.
func (s *server) ListPipelines(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	params api.ListPipelinesParams,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}

	userSlug := reqctx.GetUserSlug(c, s.sessionStore)
	listReq := toPipelinesListRequest(params)
	response, err := s.pipelinesService.List(c.Context(), orgSlug, projectSlug, userSlug, listReq)
	if err != nil {
		return mapServiceError(err)
	}

	pipelinesResponse := make([]api.PipelineRef, len(response.Items))
	for i, item := range response.Items {
		apiPipeline, mapErr := toAPIPipelineRef(item)
		if mapErr != nil {
			slog.Error("failed to map pipeline list item", "error", mapErr)
			return apierror.InternalServerError()
		}
		pipelinesResponse[i] = apiPipeline
	}

	return c.JSON(api.PipelineListResponse{
		Pipelines:  pipelinesResponse,
		HasMore:    response.HasMore,
		NextCursor: response.NextCursor,
		PrevCursor: response.PrevCursor,
	})
}

// GetPipeline returns a persisted pipeline definition by slug.
func (s *server) GetPipeline(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}

	pipelineSlug, err := slug.New(pipelineSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	userSlug := reqctx.GetUserSlug(c, s.sessionStore)

	pipeline, err := s.pipelinesService.Get(c.Context(), orgSlug, projectSlug, pipelineSlug, userSlug)
	if err != nil {
		return mapServiceError(err)
	}

	apiPipeline, err := toAPIPipeline(orgSlug, pipeline)
	if err != nil {
		slog.Error("failed to map pipeline", "error", err)
		return apierror.InternalServerError()
	}

	return c.JSON(apiPipeline)
}

// UpdatePipeline queues a Git proposal for an existing materialized pipeline.
func (s *server) UpdatePipeline(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}

	pipelineSlug, err := slug.New(pipelineSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}

	userSlug, err := s.canOrgUserPrincipal(c, orgSlug, model.Update, model.ResourcePipelines)
	if err != nil {
		return err
	}

	var req api.UpdatePipeline
	if requestBodyHasKey(c.Body(), "mode") || requestBodyHasKey(c.Body(), "source_path") {
		return apierror.ValidationFailed("pipeline update does not accept mode or source_path")
	}
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	pipeline, err := pipelineFromUpdateBody(c.Body(), pipelineSlug)
	if err != nil {
		return mapServiceError(err)
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "pipeline.git", false); err != nil {
		return err
	}

	updated, err := s.pipelinesService.Update(
		c.Context(),
		orgSlug,
		projectSlug,
		pipelineSlug,
		userSlug,
		s.eventActor(c),
		pipeline,
	)
	if err != nil {
		return mapServiceError(err)
	}

	apiPipeline, err := toAPIPipeline(orgSlug, updated)
	if err != nil {
		slog.Error("failed to map updated pipeline", "error", err)
		return apierror.InternalServerError()
	}

	return c.JSON(apiPipeline)
}

// ReconcilePipelines imports Git-authored pipeline specs into canonical DB storage.
func (s *server) ReconcilePipelines(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourcePipelines); err != nil {
		return err
	}
	userSlug := reqctx.GetUserSlug(c, s.sessionStore)

	var req api.PipelineReconcileRequest
	if requestBodyHasKey(c.Body(), "branch") {
		return apierror.ValidationFailed("branch is no longer accepted; routing uses the project pipeline backend tracked branch")
	}
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return apierror.InvalidRequestBody()
	}

	serviceReq, err := pipelineReconcileRequestToService(req)
	if err != nil {
		return mapServiceError(err)
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "pipeline.git", false); err != nil {
		return err
	}

	result, err := s.pipelinesService.ReconcilePipelines(
		c.Context(),
		orgSlug,
		projectSlug,
		userSlug,
		s.eventActor(c),
		serviceReq,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPIPipelineReconcileResponse(result))
}

// ValidatePipeline validates a project-scoped pipeline spec and records failed history for write-authorized callers.
func (s *server) ValidatePipeline(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}
	environmentSlug, err := slug.New(environmentSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid environment slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceDataSources); err != nil {
		return err
	}

	if _, err := s.projectsService.GetProject(c.Context(), orgSlug, projectSlug); err != nil {
		return mapServiceError(err)
	}

	var req api.PipelineValidationRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	pipeline, err := pipelineFromCreateBodyPermissive(c.Body())
	if err != nil {
		return mapServiceError(err)
	}
	for _, step := range pipeline.Steps {
		hasReference := (step.Input != nil && step.Input.OutputContractRef != nil) ||
			(step.Query != nil && step.Query.OutputContractRef != nil) ||
			(step.Transform != nil && (step.Transform.InputContractRef != nil || step.Transform.OutputContractRef != nil)) ||
			(step.Sink != nil && step.Sink.InputContractRef != nil)
		if step.Transform != nil {
			for _, attachment := range step.Transform.InputContracts {
				hasReference = hasReference || (attachment != nil && attachment.ContractRef != nil)
			}
		}
		if hasReference {
			if err := s.canOrg(c, orgSlug, model.Read, model.ResourceProjects); err != nil {
				return err
			}
			break
		}
	}

	userSlug := reqctx.GetUserSlug(c, s.sessionStore)
	result, err := s.pipelinesService.Validate(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		userSlug,
		pipeline,
	)
	if err != nil {
		return mapServiceError(err)
	}

	if pipeline != nil && pipeline.Metadata.Slug != "" && !result.Valid {
		canRecord, authErr := s.canRecordPipelineValidationHistory(c, orgSlug)
		if authErr != nil {
			slog.WarnContext(
				c.Context(),
				"authorize pipeline validation result recording failed",
				slog.String("org_slug", orgSlug.String()),
				slog.String("project_slug", projectSlug.String()),
				slog.String("environment_slug", environmentSlug.String()),
				slog.String("pipeline_slug", pipeline.Metadata.Slug.String()),
				slog.Any("error", authErr),
			)
			return c.JSON(toAPIPipelineValidationResponse(result))
		}
		if !canRecord {
			return c.JSON(toAPIPipelineValidationResponse(result))
		}
		if _, recordErr := s.pipelinesService.RecordValidationResult(
			c.Context(),
			orgSlug,
			projectSlug,
			environmentSlug,
			s.pipelineValidationHistoryPrincipal(c),
			pipelinesvc.PipelineValidationResultSourceAPI,
			pipeline,
			result,
		); recordErr != nil {
			slog.WarnContext(
				c.Context(),
				"record pipeline validation result failed",
				slog.String("org_slug", orgSlug.String()),
				slog.String("project_slug", projectSlug.String()),
				slog.String("environment_slug", environmentSlug.String()),
				slog.String("pipeline_slug", pipeline.Metadata.Slug.String()),
				slog.Any("error", recordErr),
			)
		}
	}

	return c.JSON(toAPIPipelineValidationResponse(result))
}

// canRecordPipelineValidationHistory checks update permission for optional validation-history writes.
func (s *server) canRecordPipelineValidationHistory(c fiber.Ctx, orgSlug slug.Slug) (bool, error) {
	input := authorization.Input{
		Action:   model.Update,
		Resource: model.ResourcePipelines,
		OrgSlug:  &orgSlug,
	}
	if principal, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		if principal.OrgSlug != orgSlug {
			return false, nil
		}
		input.APIKeySlug = principal.Slug
		input.APIKeyRoleSlug = principal.Role
	} else {
		userSlug := reqctx.GetUserSlug(c, s.sessionStore)
		if userSlug == "" {
			return false, apierror.Unauthorized()
		}
		input.UserSlug = userSlug
	}

	can, err := s.authorizationService.Can(c.Context(), input)
	if err != nil {
		slog.LogAttrs(
			c.Context(),
			slog.LevelError,
			"authorization check failed",
			authorizationErrorAttrs(c.Context(), err)...,
		)
		return false, apierror.InternalServerError()
	}
	return can, nil
}

// pipelineValidationHistoryPrincipal maps the authenticated request actor into the service auth boundary.
func (s *server) pipelineValidationHistoryPrincipal(c fiber.Ctx) pipelinesvc.ValidationHistoryPrincipal {
	if principal, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		return pipelinesvc.ValidationHistoryPrincipal{
			APIKeySlug:     principal.Slug,
			APIKeyOrgSlug:  principal.OrgSlug,
			APIKeyRoleSlug: principal.Role,
		}
	}
	return pipelinesvc.ValidationHistoryPrincipal{UserSlug: reqctx.GetUserSlug(c, s.sessionStore)}
}

// ListPipelineValidationResults returns durable non-run validation history for one runtime target.
func (s *server) ListPipelineValidationResults(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	params api.ListPipelineValidationResultsParams,
) error {
	orgSlug, projectSlug, environmentSlug, parseErr := parseOrgProjectEnvironmentSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
	)
	if parseErr != nil {
		return parseErr
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	if params.Limit != nil && *params.Limit < 0 {
		return apierror.ValidationFailed("limit must be zero or greater")
	}

	filters := pipelinesvc.PipelineValidationResultFilters{}
	if params.PipelineSlug != nil {
		pipelineSlug, err := slug.New(*params.PipelineSlug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
		}
		filters.PipelineSlug = &pipelineSlug
	}
	listReq := model.ListRequest{Limit: int(ptr.Value(params.Limit))}
	response, err := s.pipelinesService.ListValidationResults(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		s.pipelineValidationHistoryPrincipal(c),
		filters,
		listReq,
	)
	if err != nil {
		return mapServiceError(err)
	}

	results := make([]api.PipelineValidationResultRecord, len(response.Items))
	for i, item := range response.Items {
		results[i] = toAPIPipelineValidationResultRecord(item)
	}
	return c.JSON(api.PipelineValidationResultListResponse{
		ValidationResults: results,
	})
}

// GetPipelineValidationResult returns one durable non-run validation history record.
func (s *server) GetPipelineValidationResult(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	validationSlugParam api.ValidationSlugParam,
) error {
	orgSlug, projectSlug, environmentSlug, parseErr := parseOrgProjectEnvironmentSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
	)
	if parseErr != nil {
		return parseErr
	}
	validationSlug, err := slug.New(validationSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid validation slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceDataSources); err != nil {
		return err
	}

	record, err := s.pipelinesService.GetValidationResult(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		s.pipelineValidationHistoryPrincipal(c),
		validationSlug,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPIPipelineValidationResultRecord(record))
}

// ListDriftImpactReports returns persisted drift and contract impact reports for a project.
func (s *server) ListDriftImpactReports(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	params api.ListDriftImpactReportsParams,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	if params.Limit != nil && *params.Limit < 0 {
		return apierror.ValidationFailed("limit must be zero or greater")
	}
	if params.Limit != nil && *params.Limit > math.MaxInt32 {
		return apierror.ValidationFailed("limit must be less than or equal to 2147483647")
	}

	limit := ptr.Value(params.Limit)
	filters := pipelinesvc.DriftImpactReportFilters{
		ProjectSlug: &projectSlug,
		Limit:       int32(limit), // #nosec G115 -- limit is range-checked above before narrowing.
	}
	if params.PipelineSlug != nil {
		pipelineSlug, err := slug.New(*params.PipelineSlug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
		}
		filters.PipelineSlug = &pipelineSlug
	}
	if params.DataSourceSlug != nil {
		dataSourceSlug, err := slug.New(*params.DataSourceSlug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
		}
		filters.DataSourceSlug = &dataSourceSlug
	}
	if params.Status != nil {
		status, err := pipelinedrift.ReportStatusString(string(*params.Status))
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid report status: %v", err))
		}
		filters.Status = &status
	}
	if params.Severity != nil {
		severity, err := pipelinedrift.ReportSeverityString(string(*params.Severity))
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid report severity: %v", err))
		}
		filters.Severity = &severity
	}

	reports, err := s.pipelinesService.ListDriftImpactReports(c.Context(), orgSlug, filters)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(api.DriftImpactReportListResponse{
		Reports: toAPIDriftImpactReports(reports),
	})
}

// GetDriftImpactReport returns one persisted drift or contract impact report for a project.
func (s *server) GetDriftImpactReport(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	reportSlugParam api.Slug,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}
	reportSlug, err := slug.New(reportSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid report slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceDataSources); err != nil {
		return err
	}

	report, err := s.pipelinesService.GetDriftImpactReport(c.Context(), orgSlug, projectSlug, reportSlug)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPIDriftImpactReport(report))
}

// FormatPipelineSpec returns a graph-aware canonical JSON representation of a pipeline spec.
func (s *server) FormatPipelineSpec(
	c fiber.Ctx,
	slugParam api.SlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}

	var req api.PipelineSpecFormatRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	if !req.Format.Valid() {
		return apierror.ValidationFailed("Invalid pipeline spec format: expected json or yaml")
	}

	formattedData, diagnostics, err := pipelineformat.FormatData(string(req.Format), []byte(req.Data))
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(api.PipelineSpecFormatResponse{
		Format:      api.PipelineSpecFormatResponseFormat(req.Format),
		Data:        string(formattedData),
		Diagnostics: toAPIPipelineSpecFormatDiagnostics(diagnostics),
	})
}

// GetPipelineDraft returns a persisted draft definition by pipeline slug.
func (s *server) GetPipelineDraft(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}

	pipelineSlug, err := slug.New(pipelineSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}

	userSlug, err := s.canOrgUserPrincipal(c, orgSlug, model.Read, model.ResourcePipelines)
	if err != nil {
		return err
	}

	draft, err := s.pipelinesService.GetDraft(c.Context(), orgSlug, projectSlug, pipelineSlug, userSlug)
	if err != nil {
		return mapServiceError(err)
	}

	apiDraft, err := toAPIPipelineDraft(draft)
	if err != nil {
		slog.Error("failed to map pipeline draft", "error", err)
		return apierror.InternalServerError()
	}

	return c.JSON(api.PipelineDraftResponse{Draft: apiDraft})
}

// SavePipelineDraft stores or updates mutable draft content for a pipeline.
func (s *server) SavePipelineDraft(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}

	pipelineSlug, err := slug.New(pipelineSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}

	userSlug, err := s.canOrgUserPrincipal(c, orgSlug, model.Update, model.ResourcePipelines)
	if err != nil {
		return err
	}

	var req api.SavePipelineDraftJSONRequestBody
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	pipeline, err := pipelineFromUpdateBodyPermissive(c.Body(), pipelineSlug)
	if err != nil {
		return mapServiceError(err)
	}
	draft, err := s.pipelinesService.SaveDraft(
		c.Context(),
		orgSlug,
		projectSlug,
		pipelineSlug,
		userSlug,
		s.eventActor(c),
		pipeline,
		req.ExpectedSpecHash,
	)
	if err != nil {
		if response, ok := toAPIPipelineDraftConflictResponse(err); ok {
			return c.Status(fiber.StatusConflict).JSON(response)
		}
		return mapServiceError(err)
	}

	apiDraft, err := toAPIPipelineDraft(draft)
	if err != nil {
		slog.Error("failed to map saved pipeline draft", "error", err)
		return apierror.InternalServerError()
	}

	return c.JSON(api.PipelineDraftResponse{Draft: apiDraft})
}

// PublishPipelineDraft queues a Git proposal without mutating the canonical materialization.
func (s *server) PublishPipelineDraft(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}

	pipelineSlug, err := slug.New(pipelineSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}

	userSlug, err := s.canOrgUserPrincipal(c, orgSlug, model.Update, model.ResourcePipelines)
	if err != nil {
		return err
	}

	var req api.PublishPipelineDraftJSONRequestBody
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "pipeline.git", false); err != nil {
		return err
	}

	published, err := s.pipelinesService.PublishDraft(
		c.Context(),
		orgSlug,
		projectSlug,
		pipelineSlug,
		userSlug,
		s.eventActor(c),
		req.ExpectedSpecHash,
		req.Force != nil && *req.Force,
	)
	if err != nil {
		if response, ok := toAPIPipelineDraftConflictResponse(err); ok {
			return c.Status(fiber.StatusConflict).JSON(response)
		}
		return mapServiceError(err)
	}

	apiPipeline, err := toAPIPipeline(orgSlug, published)
	if err != nil {
		slog.Error("failed to map published pipeline", "error", err)
		return apierror.InternalServerError()
	}

	return c.JSON(apiPipeline)
}

// RefreshPipelineDraftProposal queues an exact provider proposal state refresh.
func (s *server) RefreshPipelineDraftProposal(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}
	pipelineSlug, err := slug.New(pipelineSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}
	userSlug, err := s.canOrgUserPrincipal(c, orgSlug, model.Update, model.ResourcePipelines)
	if err != nil {
		return err
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "pipeline.git", false); err != nil {
		return err
	}
	refreshed, err := s.pipelinesService.RefreshDraftProposal(
		c.Context(), orgSlug, projectSlug, pipelineSlug, userSlug, s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}
	apiPipeline, err := toAPIPipeline(orgSlug, refreshed)
	if err != nil {
		return apierror.InternalServerError()
	}
	return c.JSON(apiPipeline)
}

// DeletePipelineDraft removes a persisted draft definition by pipeline slug.
func (s *server) DeletePipelineDraft(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}

	pipelineSlug, err := slug.New(pipelineSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}

	userSlug, err := s.canOrgUserPrincipal(c, orgSlug, model.Update, model.ResourcePipelines)
	if err != nil {
		return err
	}

	if err := s.pipelinesService.DeleteDraft(
		c.Context(),
		orgSlug,
		projectSlug,
		pipelineSlug,
		userSlug,
		s.eventActor(c),
	); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// ListPipelineVersions returns persisted immutable versions for a pipeline.
func (s *server) ListPipelineVersions(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}

	pipelineSlug, err := slug.New(pipelineSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}

	items, err := s.pipelinesService.ListVersions(c.Context(), orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		return mapServiceError(err)
	}

	response := make([]api.PipelineVersionRef, len(items))
	for i, item := range items {
		response[i] = toAPIPipelineVersionRef(item)
	}

	return c.JSON(api.PipelineVersionListResponse{Versions: response})
}

// GetPipelineVersion returns one persisted immutable pipeline version snapshot.
func (s *server) GetPipelineVersion(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
	specHashParam api.PipelineVersionIdParam,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}

	pipelineSlug, err := slug.New(pipelineSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}

	version, err := s.pipelinesService.GetVersion(
		c.Context(),
		orgSlug,
		projectSlug,
		pipelineSlug,
		specHashParam,
	)
	if err != nil {
		return mapServiceError(err)
	}

	apiVersion, err := toAPIPipelineVersion(version)
	if err != nil {
		slog.Error("failed to map pipeline version", "error", err)
		return apierror.InternalServerError()
	}

	apiVersion.CompositionAdmissionEnabled = true
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.JSON(apiVersion)
}

// ListPipelineDeployments returns deployed pipeline versions for an environment.
func (s *server) ListPipelineDeployments(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
) error {
	orgSlug, projectSlug, parseErr := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if parseErr != nil {
		return parseErr
	}
	environmentSlug, err := slug.New(environmentSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid environment slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}

	deployments, err := s.pipelinesService.ListDeployments(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
	)
	if err != nil {
		return mapServiceError(err)
	}

	response := make([]api.PipelineDeployment, len(deployments))
	for i, deployment := range deployments {
		response[i] = toAPIPipelineDeployment(deployment)
	}

	return c.JSON(api.PipelineDeploymentListResponse{Deployments: response})
}

// GetPipelineDeployment returns one environment-scoped deployment pointer.
func (s *server) GetPipelineDeployment(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
) error {
	orgSlug, projectSlug, environmentSlug, pipelineSlug, parseErr := parseDeploymentSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
		pipelineSlugParam,
	)
	if parseErr != nil {
		return parseErr
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}

	deployment, err := s.pipelinesService.GetDeployment(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPIPipelineDeployment(deployment))
}

// DeployPipeline points an environment at an immutable pipeline version.
func (s *server) DeployPipeline(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
) error {
	orgSlug, projectSlug, environmentSlug, pipelineSlug, parseErr := parseDeploymentSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
		pipelineSlugParam,
	)
	if parseErr != nil {
		return parseErr
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourcePipelines); err != nil {
		return err
	}

	var req api.DeployPipelineJSONRequestBody
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	var sourceEnvironmentSlug *slug.Slug
	if req.SourceEnvironmentSlug != nil {
		parsedSourceEnvironmentSlug, err := slug.New(*req.SourceEnvironmentSlug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid source environment slug: %v", err))
		}
		sourceEnvironmentSlug = &parsedSourceEnvironmentSlug
	}
	var confirmation *slug.Slug
	if err := protectedConfirmationFromNullableSlug(req.ProtectedEnvironmentConfirmation, &confirmation); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid protected environment confirmation: %v", err))
	}
	var proofReceiptSlug *slug.Slug
	if req.ProofReceiptSlug != nil {
		parsedProofReceiptSlug, err := slug.New(*req.ProofReceiptSlug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid proof receipt slug: %v", err))
		}
		proofReceiptSlug = &parsedProofReceiptSlug
	}
	if _, err := s.pipelinesService.GetDeployment(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
	); xerror.Is(err, pipelinesvc.ErrPipelineDeploymentNotFound) {
		if err := s.admissionService.CheckDeploymentAdmission(c.Context(), orgSlug); err != nil {
			return mapServiceError(err)
		}
	} else if err != nil {
		return mapServiceError(err)
	}

	inputs := map[string]odcs.ResolutionValue{}
	if req.ResolutionInputs != nil {
		for key, value := range *req.ResolutionInputs {
			inputs[key] = odcs.ResolutionValue{Value: value.Value, Revision: value.Revision}
		}
	}

	deployment, err := s.pipelinesService.Deploy(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
		s.eventActor(c),
		pipelinesvc.DeployPipelineRequest{
			ResolutionInputs:                 inputs,
			SpecHash:                         req.SpecHash,
			ExpectedCurrentSpecHash:          req.ExpectedCurrentSpecHash,
			ExpectedRevision:                 req.ExpectedRevision,
			CompiledPlanDigest:               req.CompiledPlanDigest,
			ResolutionDigest:                 req.ResolutionDigest,
			SourceEnvironmentSlug:            sourceEnvironmentSlug,
			ProtectedEnvironmentConfirmation: confirmation,
			ProofReceiptSlug:                 proofReceiptSlug,
		},
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPIPipelineDeployment(deployment))
}

// ListPipelineRuns returns paginated run metadata for an organization.
func (s *server) ListPipelineRuns(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	params api.ListPipelineRunsParams,
) error {
	orgSlug, projectSlug, environmentSlug, parseErr := parseOrgProjectEnvironmentSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
	)
	if parseErr != nil {
		return parseErr
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}

	filters := &pipelinesvc.RunFilters{}
	if params.Status != nil {
		status := string(*params.Status)
		filters.Status = &status
	}
	if params.PipelineSlug != nil {
		pipelineSlug, err := slug.New(*params.PipelineSlug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
		}
		filters.PipelineSlug = &pipelineSlug
	}
	if params.Mode != nil {
		mode := string(*params.Mode)
		switch mode {
		case "full", "incremental", "backfill":
			filters.Mode = &mode
		default:
			return apierror.ValidationFailed("Invalid run mode: must be full, incremental, or backfill")
		}
	}
	if params.BackfillSeriesSlug != nil {
		backfillSeriesSlug, err := slug.New(*params.BackfillSeriesSlug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid backfill series slug: %v", err))
		}
		backfillSeriesSlugString := backfillSeriesSlug.String()
		filters.BackfillSeriesSlug = &backfillSeriesSlugString
	}

	response, err := s.pipelinesService.ListRuns(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		filters,
		toPipelineRunsListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	runs := make([]api.PipelineRunMetadata, len(response.Items))
	for i, item := range response.Items {
		runs[i] = toAPIPipelineRunMetadata(item)
	}

	return c.JSON(api.PipelineRunListResponse{
		Runs:       runs,
		HasMore:    response.HasMore,
		NextCursor: response.NextCursor,
		PrevCursor: response.PrevCursor,
	})
}

// GetPipelineRun returns persisted run diagnostics for a run slug.
func (s *server) GetPipelineRun(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	runSlugParam api.RunSlugParam,
) error {
	orgSlug, projectSlug, environmentSlug, parseErr := parseOrgProjectEnvironmentSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
	)
	if parseErr != nil {
		return parseErr
	}

	runSlug, err := slug.New(runSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid run slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}

	run, err := s.pipelinesService.GetRun(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		string(runSlug),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPIPipelineRunRecord(run))
}

// ListPipelineRunTriggerRetryAttempts returns durable trigger retry diagnostics for a run.
func (s *server) ListPipelineRunTriggerRetryAttempts(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	runSlugParam api.RunSlugParam,
) error {
	orgSlug, projectSlug, environmentSlug, parseErr := parseOrgProjectEnvironmentSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
	)
	if parseErr != nil {
		return parseErr
	}

	runSlug, err := slug.New(runSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid run slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		return err
	}

	// Keep run-not-found behavior identical to the run detail endpoint while
	// letting a real run with no retries return an empty diagnostics list.
	if _, err := s.pipelinesService.GetRun(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		runSlug.String(),
	); err != nil {
		return mapServiceError(err)
	}

	attempts, err := s.pipelineTriggersService.ListPipelineRunTriggerRetryAttempts(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		runSlug,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(api.PipelineRunTriggerRetryAttemptListResponse{
		RetryAttempts: toAPIPipelineRunTriggerRetryAttempts(attempts),
	})
}

// ExecutePipeline accepts a pipeline run and returns the queued run record.
func (s *server) ExecutePipeline(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
) error {
	orgSlug, projectSlug, environmentSlug, pipelineSlug, parseErr := parseDeploymentSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
		pipelineSlugParam,
	)
	if parseErr != nil {
		return parseErr
	}

	if err := s.canOrg(c, orgSlug, model.Execute, model.ResourcePipelines); err != nil {
		return err
	}

	var req api.ExecutePipelineRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	var runSlug *slug.Slug
	if req.RunSlug != nil {
		validatedRunSlug, err := slug.New(*req.RunSlug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid run slug: %v", err))
		}
		runSlug = &validatedRunSlug
	}
	var confirmation *slug.Slug
	if err := protectedConfirmationFromNullableSlug(req.ProtectedEnvironmentConfirmation, &confirmation); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid protected environment confirmation: %v", err))
	}

	actor := s.eventActor(c)
	runtimeInput := executePipelineRuntimeInput(req.Input)
	source := pipelineRunSourceSummary(c, actor, executePipelineSourceParameterKeys(c.Body()))
	if err := s.admissionService.CheckRunAccess(c.Context(), orgSlug); err != nil {
		return mapServiceError(err)
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "pipeline.run", false); err != nil {
		return err
	}
	var run *pipelinesvc.PipelineRunDetail
	var err error
	if req.Backfill != nil {
		windows := make(map[slug.Slug]pipelinerun.IncrementalSourceWindow, len(req.Backfill.SourceWindows))
		for name, window := range req.Backfill.SourceWindows {
			stepSlug, parseErr := slug.New(name)
			if parseErr != nil {
				return apierror.ValidationFailed("Invalid backfill source step slug")
			}
			windows[stepSlug] = pipelinerun.IncrementalSourceWindow{LowerBound: window.LowerBound, UpperBound: window.UpperBound}
		}
		run, err = s.pipelinesService.StartBackfill(c.Context(), orgSlug, projectSlug, environmentSlug, pipelineSlug, runSlug, confirmation, actor, source, runtimeInput, windows)
	} else {
		run, err = s.pipelinesService.StartRun(
			c.Context(),
			orgSlug,
			projectSlug,
			environmentSlug,
			pipelineSlug,
			runSlug,
			confirmation,
			actor,
			source,
			runtimeInput,
		)
	}
	if err != nil {
		return mapServiceError(err)
	}

	return c.Status(fiber.StatusAccepted).JSON(toAPIPipelineRunRecord(run))
}

// RecoverPipelineRun recovers sink delivery using retained inputs in the same runtime scope.
func (s *server) RecoverPipelineRun(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, environmentParam api.EnvironmentSlugParam, runParam api.RunSlugParam) error {
	org, project, environment, err := parseOrgProjectEnvironmentSlugs(orgParam, projectParam, environmentParam)
	if err != nil {
		return err
	}
	runSlug, err := slug.New(runParam)
	if err != nil {
		return apierror.ValidationFailed("Invalid run slug")
	}
	if err := s.canOrg(c, org, model.Execute, model.ResourcePipelines); err != nil {
		return err
	}
	var req api.RecoverPipelineRunRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	var confirmation *slug.Slug
	if err := protectedConfirmationFromNullableSlug(req.ProtectedEnvironmentConfirmation, &confirmation); err != nil {
		return apierror.ValidationFailed("Invalid protected environment confirmation")
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, org.String(), "pipeline.run", false); err != nil {
		return err
	}
	actor := s.eventActor(c)
	run, recoverErr := s.pipelinesService.RecoverRun(c.Context(), org, project, environment, runSlug, confirmation, actor, pipelineRunSourceSummary(c, actor, nil))
	if recoverErr != nil {
		// A persisted failed recovery is observable through its run record.
		if run == nil || run.Metadata.Status != "failed" {
			return mapServiceError(recoverErr)
		}
	}
	return c.JSON(toAPIPipelineRunRecord(run))
}

// RevalidatePipeline validates the current pipeline and records non-run diagnostic history.
func (s *server) RevalidatePipeline(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
) error {
	orgSlug, projectSlug, environmentSlug, pipelineSlug, parseErr := parseDeploymentSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
		pipelineSlugParam,
	)
	if parseErr != nil {
		return parseErr
	}

	userSlug, err := s.canOrgUserPrincipal(c, orgSlug, model.Update, model.ResourcePipelines)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	principal := pipelinesvc.ValidationHistoryPrincipal{UserSlug: userSlug}

	record, err := s.pipelinesService.RevalidatePipeline(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		principal,
		pipelineSlug,
		userSlug,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPIPipelineValidationResultRecord(record))
}

func pipelineRunSourceEntrypoint(c fiber.Ctx) model.PipelineRunSourceEntrypoint {
	if _, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		return model.PipelineRunSourceEntrypointAPI
	}
	return model.PipelineRunSourceEntrypointUI
}

// pipelineRunSourceSummary builds the required safe run source summary from HTTP
// request context without copying request values, headers, tokens, or credentials.
func pipelineRunSourceSummary(
	c fiber.Ctx,
	actor model.EventActor,
	parameterKeys []string,
) pipelinerun.RunSourceSummary {
	return pipelinerun.RunSourceSummary{
		Type:          pipelineRunSourceTypeForActor(actor),
		Summary:       pipelineRunSourceSummaryForActor(actor),
		Entrypoint:    pipelineRunSourceEntrypoint(c),
		ActorType:     actor.Type,
		ActorSlug:     actor.Slug,
		RequestID:     reqctx.GetRequestID(c.Context()),
		TraceID:       traceIDFromContext(c.Context()),
		ParameterKeys: parameterKeys,
	}
}

// pipelineRunSourceTypeForActor maps the authenticated actor class to the launch source category.
func pipelineRunSourceTypeForActor(actor model.EventActor) model.PipelineRunSourceType {
	if actor.Type == model.EventActorTypeAPIKey {
		return model.PipelineRunSourceTypeAPI
	}
	return model.PipelineRunSourceTypeManual
}

// pipelineRunSourceSummaryForActor returns the operator-facing source label for the actor.
func pipelineRunSourceSummaryForActor(actor model.EventActor) string {
	if actor.Type == model.EventActorTypeAPIKey {
		return "API key run"
	}
	return "Manual run"
}

// traceIDFromContext returns the active OpenTelemetry trace identifier when one is available.
func traceIDFromContext(ctx context.Context) *string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return nil
	}
	traceID := spanContext.TraceID().String()
	if traceID == "" {
		return nil
	}
	return &traceID
}

func executePipelineSourceParameterKeys(body []byte) []string {
	keys := make([]string, 0, 2)
	if requestBodyHasKey(body, "run_slug") {
		keys = append(keys, "run_slug")
	}
	if requestBodyHasKey(body, "protected_environment_confirmation") {
		keys = append(keys, "protected_environment_confirmation")
	}
	keys = append(keys, executePipelineSourceInputKeys(body)...)
	sort.Strings(keys)
	return keys
}

func executePipelineRuntimeInput(input *map[string]interface{}) map[string]any {
	if input == nil {
		return nil
	}
	return *input
}

func executePipelineSourceInputKeys(body []byte) []string {
	if len(body) == 0 {
		return nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	inputRaw, ok := raw["input"]
	if !ok || string(inputRaw) == "null" {
		return nil
	}

	var input map[string]any
	if err := json.Unmarshal(inputRaw, &input); err != nil {
		return nil
	}
	return pipelineruntimevars.InputParameterKeys(input)
}

func parseOrgProjectSlugs(
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
) (slug.Slug, slug.Slug, error) {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	projectSlug, err := slug.New(projectSlugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid project slug: %v", err))
	}

	return orgSlug, projectSlug, nil
}

func parseDeploymentSlugs(
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
) (slug.Slug, slug.Slug, slug.Slug, slug.Slug, error) {
	orgSlug, projectSlug, err := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if err != nil {
		return "", "", "", "", err
	}

	environmentSlug, err := slug.New(environmentSlugParam)
	if err != nil {
		return "", "", "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid environment slug: %v", err))
	}

	pipelineSlug, err := slug.New(pipelineSlugParam)
	if err != nil {
		return "", "", "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}

	return orgSlug, projectSlug, environmentSlug, pipelineSlug, nil
}

func toAPIPipelineRunMetadata(item *pipelinesvc.PipelineRunMetadata) api.PipelineRunMetadata {
	if item == nil {
		return api.PipelineRunMetadata{}
	}

	return api.PipelineRunMetadata{
		EnvironmentSlug:         item.EnvironmentSlug,
		PipelineSlug:            item.PipelineSlug,
		PipelineVersionSpecHash: item.PipelineVersionSpecHash,
		RunSlug:                 item.RunSlug,
		Source:                  toAPIPipelineRunSourceSummary(item.Source),
		RequestId:               item.RequestID,
		Status:                  api.PipelineRunMetadataStatus(item.Status),
		DurationMs:              item.DurationMs,
		StepCount:               item.StepCount,
		StepSlug:                item.StepSlug,
		ErrorSummary:            item.ErrorSummary,
		FailureKind:             toAPIFailureKind(item.FailureKind),
		StartedAt:               item.StartedAt,
		EndedAt:                 item.EndedAt,
		ReplayOfRunSlug:         ptr.StringPtrOrNil(item.ReplayOfRunSlug),
		ReplayFromStep:          ptr.StringPtrOrNil(item.ReplayFromStep),
		Incremental:             toAPIIncrementalSummary(item.Incremental),
		CreatedAt:               item.CreatedAt,
	}
}

func toAPIPipelineRunSourceSummary(source pipelinerun.RunSourceSummary) *api.PipelineRunSourceSummary {
	if !validPipelineRunSourceType(source.Type) ||
		strings.TrimSpace(source.Summary) == "" ||
		!validPipelineRunSourceEntrypoint(source.Entrypoint) ||
		!validEventActorType(source.ActorType) {
		return nil
	}

	var summary api.PipelineRunSourceSummary
	switch source.Type {
	case model.PipelineRunSourceTypeManual:
		if source.Entrypoint != model.PipelineRunSourceEntrypointUI {
			return nil
		}
		if err := summary.FromPipelineRunManualSourceSummary(api.PipelineRunManualSourceSummary{
			Summary:         source.Summary,
			Entrypoint:      api.PipelineRunManualSourceSummaryEntrypointUi,
			ActorType:       api.EventActorType(source.ActorType.String()),
			ActorSlug:       nullableSlugValue(source.ActorSlug),
			RequestId:       source.RequestID,
			TraceId:         source.TraceID,
			ParameterKeys:   source.ParameterKeys,
			SourceEventSlug: nullableSlugValue(source.SourceEventSlug),
			AttemptNumber:   source.AttemptNumber,
			RootRunSlug:     source.RootRunSlug.String(),
			RetryOfRunSlug:  nullableSlugValue(source.RetryOfRunSlug),
		}); err != nil {
			return nil
		}
	case model.PipelineRunSourceTypeAPI:
		if source.Entrypoint != model.PipelineRunSourceEntrypointAPI {
			return nil
		}
		if err := summary.FromPipelineRunAPISourceSummary(api.PipelineRunAPISourceSummary{
			Summary:         source.Summary,
			Entrypoint:      api.PipelineRunAPISourceSummaryEntrypointApi,
			ActorType:       api.EventActorType(source.ActorType.String()),
			ActorSlug:       nullableSlugValue(source.ActorSlug),
			RequestId:       source.RequestID,
			TraceId:         source.TraceID,
			ParameterKeys:   source.ParameterKeys,
			SourceEventSlug: nullableSlugValue(source.SourceEventSlug),
			AttemptNumber:   source.AttemptNumber,
			RootRunSlug:     source.RootRunSlug.String(),
			RetryOfRunSlug:  nullableSlugValue(source.RetryOfRunSlug),
		}); err != nil {
			return nil
		}
	case model.PipelineRunSourceTypeCompletion:
		if source.Entrypoint != model.PipelineRunSourceEntrypointScheduler {
			return nil
		}
		if err := summary.FromPipelineRunCompletionSourceSummary(api.PipelineRunCompletionSourceSummary{
			Summary:         source.Summary,
			Entrypoint:      api.PipelineRunCompletionSourceSummaryEntrypointScheduler,
			ActorType:       api.EventActorType(source.ActorType.String()),
			ActorSlug:       nullableSlugValue(source.ActorSlug),
			RequestId:       source.RequestID,
			TraceId:         source.TraceID,
			ParameterKeys:   source.ParameterKeys,
			SourceEventSlug: nullableSlugValue(source.SourceEventSlug),
			AttemptNumber:   source.AttemptNumber,
			RootRunSlug:     source.RootRunSlug.String(),
			RetryOfRunSlug:  nullableSlugValue(source.RetryOfRunSlug),
		}); err != nil {
			return nil
		}
	case model.PipelineRunSourceTypeSchedule:
		if source.Entrypoint != model.PipelineRunSourceEntrypointScheduler {
			return nil
		}
		schedule := toAPIPipelineRunSourceScheduleSummary(source.Schedule)
		if schedule == nil {
			return nil
		}
		if err := summary.FromPipelineRunScheduleSourceSummary(api.PipelineRunScheduleSourceSummary{
			Summary:         source.Summary,
			Entrypoint:      api.PipelineRunScheduleSourceSummaryEntrypointScheduler,
			ActorType:       api.EventActorType(source.ActorType.String()),
			ActorSlug:       nullableSlugValue(source.ActorSlug),
			RequestId:       source.RequestID,
			TraceId:         source.TraceID,
			ParameterKeys:   source.ParameterKeys,
			SourceEventSlug: nullableSlugValue(source.SourceEventSlug),
			AttemptNumber:   source.AttemptNumber,
			RootRunSlug:     source.RootRunSlug.String(),
			RetryOfRunSlug:  nullableSlugValue(source.RetryOfRunSlug),
			Schedule:        *schedule,
		}); err != nil {
			return nil
		}
	case model.PipelineRunSourceTypeWebhook:
		if source.Entrypoint != model.PipelineRunSourceEntrypointWebhook || source.Webhook == nil {
			return nil
		}
		webhook := source.Webhook
		if webhook.TriggerSlug == "" || webhook.TriggerRevision <= 0 || webhook.DeliveryID == "" ||
			webhook.PayloadDigest == "" || webhook.PayloadSizeBytes < 0 || webhook.ReceivedAt.IsZero() ||
			webhook.VerificationStatus != "verified" {
			return nil
		}
		if err := summary.FromPipelineRunWebhookSourceSummary(api.PipelineRunWebhookSourceSummary{
			Summary:         source.Summary,
			Entrypoint:      api.PipelineRunWebhookSourceSummaryEntrypointWebhook,
			ActorType:       api.EventActorType(source.ActorType.String()),
			ActorSlug:       nullableSlugValue(source.ActorSlug),
			RequestId:       source.RequestID,
			TraceId:         source.TraceID,
			ParameterKeys:   source.ParameterKeys,
			SourceEventSlug: nullableSlugValue(source.SourceEventSlug),
			AttemptNumber:   source.AttemptNumber,
			RootRunSlug:     source.RootRunSlug.String(),
			RetryOfRunSlug:  nullableSlugValue(source.RetryOfRunSlug),
			Webhook: api.PipelineRunSourceWebhookSummary{
				TriggerSlug:        webhook.TriggerSlug.String(),
				TriggerRevision:    webhook.TriggerRevision,
				DeliveryId:         webhook.DeliveryID,
				PayloadDigest:      webhook.PayloadDigest,
				PayloadSizeBytes:   webhook.PayloadSizeBytes,
				ReceivedAt:         webhook.ReceivedAt,
				VerificationStatus: api.PipelineRunSourceWebhookSummaryVerificationStatusVerified,
			},
		}); err != nil {
			return nil
		}
	default:
		return nil
	}
	return &summary
}

// toAPIPipelineRunSourceScheduleSummary maps executable schedule metadata to the API payload.
func toAPIPipelineRunSourceScheduleSummary(
	schedule *pipelinerun.RunSourceScheduleSummary,
) *api.PipelineRunSourceScheduleSummary {
	if schedule == nil ||
		schedule.TriggerSlug == "" ||
		schedule.TriggerRevision <= 0 ||
		strings.TrimSpace(schedule.Cron) == "" ||
		strings.TrimSpace(schedule.Timezone) == "" ||
		schedule.ScheduledFireAt.IsZero() ||
		schedule.ActualFireAt.IsZero() {
		return nil
	}
	misfireResult, ok := toAPIPipelineRunScheduleMisfireResult(schedule.MisfireResult)
	if !ok {
		return nil
	}
	return &api.PipelineRunSourceScheduleSummary{
		TriggerSlug:     schedule.TriggerSlug.String(),
		TriggerRevision: schedule.TriggerRevision,
		Cron:            schedule.Cron,
		Timezone:        schedule.Timezone,
		ScheduledFireAt: schedule.ScheduledFireAt,
		ActualFireAt:    schedule.ActualFireAt,
		MisfireResult:   misfireResult,
	}
}

// toAPIPipelineRunScheduleMisfireResult narrows persisted fire results to executed-run results.
func toAPIPipelineRunScheduleMisfireResult(
	value model.PipelineTriggerScheduleMisfireResult,
) (api.PipelineRunScheduleMisfireResult, bool) {
	switch value {
	case model.PipelineTriggerScheduleMisfireResultOnTime:
		return api.PipelineRunScheduleMisfireResultOnTime, true
	case model.PipelineTriggerScheduleMisfireResultFireOnce:
		return api.PipelineRunScheduleMisfireResultFireOnce, true
	default:
		return "", false
	}
}

// validPipelineRunSourceType checks that an internal run source type can be safely cast to the API enum.
func validPipelineRunSourceType(value model.PipelineRunSourceType) bool {
	return value != model.PipelineRunSourceTypeUnknown && value.IsAPipelineRunSourceType()
}

// validPipelineRunSourceEntrypoint checks that an internal run source entrypoint can be safely cast to the API enum.
func validPipelineRunSourceEntrypoint(value model.PipelineRunSourceEntrypoint) bool {
	return value != model.PipelineRunSourceEntrypointUnknown && value.IsAPipelineRunSourceEntrypoint()
}

// validEventActorType checks that an internal event actor type can be safely cast to the API enum.
func validEventActorType(value model.EventActorType) bool {
	return value != model.EventActorTypeUnknown && value.IsAEventActorType()
}

func toAPIPipelineRunRecord(run *pipelinesvc.PipelineRunDetail) api.PipelineRunRecord {
	return api.PipelineRunRecord{
		Composition:            toAPIRunComposition(run.Composition),
		Metadata:               toAPIPipelineRunMetadata(&run.Metadata),
		Timeline:               toAPIPipelineRunTimeline(run.Timeline),
		IncrementalDiagnostics: toAPIIncrementalDiagnostics(run.IncrementalDiagnostics),
		ProofLink:              toAPIProofLifecycleLink(run.ProofLink),
	}
}

func toAPIProofLifecycleLink(link *pipelinesvc.ProofLifecycleLink) *api.ProofLifecycleLink {
	if link == nil {
		return nil
	}
	return &api.ProofLifecycleLink{
		Kind: api.ProofLifecycleLinkKind(link.Kind.String()), ReceiptId: api.Slug(link.ReceiptSlug),
		Identifier: link.Identifier, LinkedAt: link.LinkedAt, RetainUntil: link.RetainUntil,
		Status: api.ProofLifecycleLinkStatus(link.Status),
	}
}

// toAPIPipelineRunTriggerRetryAttempts maps safe retry diagnostics into API payloads.
func toAPIPipelineRunTriggerRetryAttempts(
	attempts []model.PipelineTriggerRetryAttempt,
) []api.PipelineRunTriggerRetryAttempt {
	result := make([]api.PipelineRunTriggerRetryAttempt, len(attempts))
	for i, attempt := range attempts {
		runSlug := attempt.RunSlug.String()
		result[i] = api.PipelineRunTriggerRetryAttempt{
			AttemptSlug:     attempt.AttemptSlug.String(),
			Status:          api.PipelineTriggerRetryAttemptStatus(attempt.Status.String()),
			DecisionReason:  attempt.DecisionReason,
			TriggerSlug:     attempt.TriggerSlug.String(),
			TriggerRevision: attempt.TriggerRevision,
			PipelineSlug:    attempt.PipelineSlug.String(),
			RootRunSlug:     attempt.RootRunSlug.String(),
			RetryOfRunSlug:  attempt.RetryOfRunSlug.String(),
			RunSlug:         &runSlug,
			AttemptNumber:   attempt.AttemptNumber,
			FailureKind:     toAPIPipelineTriggerRetryFailureKind(attempt.FailureKind),
			SourceEventSlug: nullableSlugValue(attempt.SourceEventSlug),
			ScheduledAt:     attempt.ScheduledAt,
			EnqueuedAt:      nullableTimestamp(attempt.EnqueuedAt),
			StartedAt:       nullableTimestamp(attempt.StartedAt),
			CompletedAt:     nullableTimestamp(attempt.CompletedAt),
			ErrorSummary:    attempt.ErrorSummary,
			CreatedAt:       attempt.CreatedAt,
			UpdatedAt:       attempt.UpdatedAt,
		}
	}
	return result
}

// toAPIPipelineTriggerRetryFailureKind narrows optional model failure kinds to the API enum.
func toAPIPipelineTriggerRetryFailureKind(
	value *model.PipelineTriggerRetryFailureKind,
) *api.PipelineTriggerRetryFailureKind {
	if value == nil || *value == model.PipelineTriggerRetryFailureKindUnknown {
		return nil
	}
	result := api.PipelineTriggerRetryFailureKind(value.String())
	return &result
}

func toAPIIncrementalSummary(
	summary pipelinerun.IncrementalRunSummary,
) api.PipelineRunIncrementalSummary {
	return api.PipelineRunIncrementalSummary{
		Mode:                api.PipelineRunMode(summary.Mode),
		CheckpointStatus:    api.PipelineRunCheckpointStatus(summary.CheckpointStatus),
		SourceStepSlug:      summary.SourceStepSlug,
		BlockedReason:       summary.BlockedReason,
		ReplaySafetyStatus:  api.PipelineRunReplaySafetyStatus(summary.ReplaySafetyStatus),
		ReplayBlockedReason: summary.ReplayBlockedReason,
		BackfillSeriesSlug:  summary.BackfillSeriesSlug,
		BackfillChunkIndex:  summary.BackfillChunkIndex,
		BackfillChunkCount:  summary.BackfillChunkCount,
	}
}

func toAPIIncrementalDiagnostics(
	diagnostics pipelinerun.IncrementalRunDiagnostics,
) api.PipelineRunIncrementalDiagnostics {
	sources := make([]api.PipelineRunIncrementalSourceDiagnostic, len(diagnostics.Sources))
	for i, source := range diagnostics.Sources {
		sources[i] = api.PipelineRunIncrementalSourceDiagnostic{
			SourceStepSlug:     source.SourceStepSlug,
			CheckpointKind:     source.CheckpointKind,
			PreviousCheckpoint: toAPICheckpointSummary(source.PreviousCheckpoint),
			Window:             toAPIWindowSummary(source.Window),
			ObservedCheckpoint: toAPICheckpointSummary(source.ObservedCheckpoint),
			CommitResult:       toAPICommitResult(source.CommitResult),
		}
	}
	return api.PipelineRunIncrementalDiagnostics{
		Sources: sources,
		ReplaySafety: api.PipelineRunReplaySafety{
			Status:        api.PipelineRunReplaySafetyStatus(diagnostics.ReplaySafety.Status),
			BlockedSink:   diagnostics.ReplaySafety.BlockedSink,
			BlockedReason: diagnostics.ReplaySafety.BlockedReason,
		},
	}
}

func toAPICheckpointSummary(
	summary pipelinerun.IncrementalCheckpointSummary,
) api.PipelineRunCheckpointSummary {
	return api.PipelineRunCheckpointSummary{
		Summary:         summary.Summary,
		DisplayMetadata: optionalAPIMetadata(summary.DisplayMetadata),
		RawValueExposed: summary.RawValueExposed,
	}
}

func toAPIWindowSummary(
	summary pipelinerun.IncrementalCheckpointWindowSummary,
) api.PipelineRunSourceWindowSummary {
	return api.PipelineRunSourceWindowSummary{
		Summary:         summary.Summary,
		LowerBound:      optionalAPIString(summary.LowerBound),
		UpperBound:      optionalAPIString(summary.UpperBound),
		DisplayMetadata: optionalAPIMetadata(summary.DisplayMetadata),
		RawValueExposed: summary.RawValueExposed,
	}
}

func toAPICommitResult(
	result pipelinerun.IncrementalCheckpointCommitResult,
) api.PipelineRunCheckpointCommitResult {
	return api.PipelineRunCheckpointCommitResult{
		Status:        api.PipelineRunCheckpointStatus(result.Status),
		BlockedByStep: result.BlockedByStep,
		BlockedReason: result.BlockedReason,
	}
}

func optionalAPIString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func optionalAPIMetadata(value map[string]any) *map[string]interface{} {
	if len(value) == 0 {
		return nil
	}
	return &value
}

func toAPIPipelineRunTimeline(timeline pipelinerun.RunTimeline) api.PipelineRunTimeline {
	events := make([]api.PipelineRunTimelineEvent, len(timeline.Events))
	for i, event := range timeline.Events {
		events[i] = api.PipelineRunTimelineEvent{
			At:     event.At,
			Type:   event.Type,
			Step:   optionalStepSlug(event.Step),
			Status: event.Status,
		}
	}

	snapshots := make([]api.PipelineRunStepSnapshot, len(timeline.Snapshots))
	for i, snapshot := range timeline.Snapshots {
		var operationType *string
		if snapshot.Input.OperationType != "" {
			op := snapshot.Input.OperationType
			operationType = &op
		}

		var operationRef *api.Slug
		if snapshot.Input.OperationRef != "" {
			ref := api.Slug(snapshot.Input.OperationRef)
			operationRef = &ref
		}

		var dataSourceSlug *api.Slug
		if snapshot.Input.DataSourceSlug != "" {
			value := api.Slug(snapshot.Input.DataSourceSlug)
			dataSourceSlug = &value
		}

		var usesInline *bool
		if snapshot.Input.UsesInline {
			useInline := true
			usesInline = &useInline
		}

		snapshots[i] = api.PipelineRunStepSnapshot{
			Step: string(snapshot.Step),
			Kind: optionalPipelineStepKind(snapshot.Kind),
			Input: api.PipelineRunStepInputSnapshot{
				OrganizationSlug:    string(snapshot.Input.OrganizationSlug),
				EnvironmentSlug:     string(snapshot.Input.EnvironmentSlug),
				DataSourceSlug:      dataSourceSlug,
				OperationType:       operationType,
				OperationRef:        operationRef,
				UsesInline:          usesInline,
				ParameterKeys:       snapshot.Input.ParameterKeys,
				EventInput:          toAPIPipelineRunEventInputSummary(snapshot.Input.EventInput),
				SinkReplayStrategy:  optionalSinkReplayStrategy(snapshot.Input.SinkReplayStrategy),
				SinkReplayMethod:    optionalAPIString(snapshot.Input.SinkReplayMethod),
				SinkReplayDecision:  optionalAPIString(snapshot.Input.SinkReplayDecision),
				SinkEffectState:     optionalSinkEffectState(snapshot.Input.SinkEffectState),
				SinkDeliveryOutcome: optionalSinkDeliveryOutcome(snapshot.Input.SinkDeliveryOutcome),
			},
			Output: toAPIPipelineRunStepOutputSnapshot(snapshot.Output),
		}
	}

	return api.PipelineRunTimeline{
		CompiledPlanDigest: optionalCheckString(timeline.CompiledPlanDigest), Revision: optionalCheckRevision(timeline.Revision),
		WorkerObservedAt:           timeline.WorkerObservedAt,
		DeploymentResolutionDigest: optionalCheckString(timeline.DeploymentResolutionDigest),
		StructuralChecks:           toAPIStructuralChecks(timeline.StructuralChecks),
		RunSlug:                    timeline.RunSlug,
		PipelineSlug:               string(timeline.PipelineSlug),
		EnvironmentSlug:            string(timeline.EnvironmentSlug),
		Status:                     api.PipelineRunTimelineStatus(timeline.Status),
		ReplayOfRunSlug:            ptr.StringPtrOrNil(timeline.ReplayOfRunSlug),
		Events:                     events,
		Snapshots:                  snapshots,
		Failure:                    toAPIPipelineRunFailureContext(timeline.Failure),
	}
}

func toAPIPipelineRunEventInputSummary(
	summary *pipelinerun.EventInputSummary,
) *api.PipelineRunEventInputSummary {
	if summary == nil {
		return nil
	}

	return &api.PipelineRunEventInputSummary{
		Keys:            append([]string(nil), summary.Keys...),
		Representation:  api.PipelineRunEventInputSummaryRepresentation(summary.Representation.String()),
		SizeBytes:       summary.SizeBytes,
		DigestAlgorithm: api.PipelineRunEventInputSummaryDigestAlgorithm(summary.DigestAlgorithm),
		Digest:          summary.Digest,
	}
}

func toAPIPipelineRunStepOutputSnapshot(
	output *pipelinerun.StepOutputSnapshot,
) *api.PipelineRunStepOutputSnapshot {
	if output == nil {
		return nil
	}

	return &api.PipelineRunStepOutputSnapshot{
		ResultType:    output.ResultType,
		VendorType:    output.VendorType,
		DurationMs:    output.DurationMs,
		OperationType: optionalString(output.OperationType),
		Details:       toAPIPipelineRunStepOutputDetails(output.Details),
	}
}

// toAPIPipelineRunStepOutputDetails maps typed runtime snapshot details into the OpenAPI union wrapper.
func toAPIPipelineRunStepOutputDetails(
	details *pipelinerun.StepOutputDetails,
) api.PipelineRunStepOutputDetails {
	var output api.PipelineRunStepOutputDetails
	if details == nil {
		_ = output.FromPipelineRunUnknownOutputDetails(api.PipelineRunUnknownOutputDetails{})
		return output
	}

	switch details.Kind {
	case pipelinerun.StepOutputDetailKindRows:
		_ = output.FromPipelineRunRowsOutputDetails(api.PipelineRunRowsOutputDetails{
			RowCount:    intValue(details.RowCount),
			ColumnNames: optionalStringSlice(details.ColumnNames),
		})
	case pipelinerun.StepOutputDetailKindDocuments:
		_ = output.FromPipelineRunDocumentsOutputDetails(api.PipelineRunDocumentsOutputDetails{
			DocumentCount: details.DocumentCount,
		})
	case pipelinerun.StepOutputDetailKindHTTPResponse:
		_ = output.FromPipelineRunHTTPResponseOutputDetails(api.PipelineRunHTTPResponseOutputDetails{
			StatusCode: intValue(details.StatusCode),
			File:       filePayloadMetadataToAPI(details.File),
		})
	case pipelinerun.StepOutputDetailKindFileRead:
		_ = output.FromPipelineRunFileReadOutputDetails(api.PipelineRunFileReadOutputDetails{
			TargetPath:  optionalStringPtr(details.TargetPath),
			RowCount:    details.RowCount,
			ColumnNames: optionalStringSlice(details.ColumnNames),
			File:        filePayloadMetadataToAPI(details.File),
		})
	case pipelinerun.StepOutputDetailKindFileWrite:
		_ = output.FromPipelineRunFileWriteOutputDetails(api.PipelineRunFileWriteOutputDetails{
			TargetPath:   stringValue(details.TargetPath),
			WrittenRows:  details.WrittenRows,
			WrittenBytes: details.WrittenBytes,
			File:         filePayloadMetadataToAPI(details.File),
		})
	default:
		_ = output.FromPipelineRunUnknownOutputDetails(api.PipelineRunUnknownOutputDetails{})
	}

	return output
}

// optionalStringPtr returns a copied string pointer only when the value is present and non-empty.
func optionalStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	return optionalString(*value)
}

// stringValue returns the pointed value, or an empty string for absent optional data.
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// optionalStringSlice returns a pointer only when the copied slice has values.
func optionalStringSlice(values []string) *[]string {
	if len(values) == 0 {
		return nil
	}
	copied := append([]string(nil), values...)
	return &copied
}

// intValue returns zero when a detail omits an integer that the public schema requires.
func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func toAPIPipelineRunFailureContext(
	failure *pipelinerun.FailureContext,
) *api.PipelineRunFailureContext {
	if failure == nil {
		return nil
	}

	return &api.PipelineRunFailureContext{
		Step:           string(failure.Step),
		Error:          failure.Error,
		At:             failure.At,
		Kind:           toAPIFailureKindValue(failure.Kind),
		Side:           toAPIFailureSideValue(failure.Side),
		ReasonCode:     toAPIFailureReasonCode(failure.ReasonCode),
		DataSourceSlug: optionalString(failure.DataSourceSlug),
		ActionHint:     optionalString(failure.ActionHint),
		Contract:       toAPIPipelineRunContractFailureDiagnostic(failure.Contract),
	}
}

func toAPIPipelineRunContractFailureDiagnostic(
	contract *pipelinerun.ContractFailureDiagnostic,
) *api.PipelineRunContractFailureDiagnostic {
	if contract == nil {
		return nil
	}

	return &api.PipelineRunContractFailureDiagnostic{
		ContractName:   contract.ContractName,
		FieldName:      contract.FieldName,
		Rule:           contract.Rule,
		ViolationCount: contract.ViolationCount,
		ReasonCode:     contract.ReasonCode.String(),
	}
}

func toAPIFailureKindValue(kind string) api.PipelineRunFailureKind {
	switch kind {
	case string(api.PipelineRunFailureKindDataSource):
		return api.PipelineRunFailureKindDataSource
	case string(api.PipelineRunFailureKindPlatform):
		return api.PipelineRunFailureKindPlatform
	default:
		return api.PipelineRunFailureKindPlatform
	}
}

func toAPIFailureSideValue(side string) api.PipelineRunFailureSide {
	switch side {
	case string(api.PipelineRunFailureSideSource):
		return api.PipelineRunFailureSideSource
	case string(api.PipelineRunFailureSideSink):
		return api.PipelineRunFailureSideSink
	case string(api.PipelineRunFailureSideTransform):
		return api.PipelineRunFailureSideTransform
	case string(api.PipelineRunFailureSideConnector):
		return api.PipelineRunFailureSideConnector
	default:
		return api.PipelineRunFailureSideTransform
	}
}

func toAPIFailureKind(kind *string) *api.PipelineRunFailureKind {
	if kind == nil {
		return nil
	}
	k := toAPIFailureKindValue(*kind)
	return &k
}

// toAPIFailureReasonCode narrows internal runtime reason codes to the public API enum.
func toAPIFailureReasonCode(
	reasonCode pipelinerun.FailureReasonCode,
) *api.PipelineRunFailureReasonCode {
	if reasonCode == pipelinerun.FailureReasonCodeUnknown || !reasonCode.IsAFailureReasonCode() {
		return nil
	}
	code := api.PipelineRunFailureReasonCode(reasonCode.String())
	return &code
}

func optionalStepSlug(step slug.Slug) *string {
	if step == "" {
		return nil
	}
	value := step.String()
	return &value
}

func nullableSlugValue(value *slug.Slug) *api.NullableSlug {
	if value == nil || *value == "" {
		return nil
	}
	ret := value.String()
	return &ret
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// optionalPipelineStepKind maps a captured runtime step kind into the generated API enum.
func optionalPipelineStepKind(value string) *api.PipelineStepKind {
	kind := api.PipelineStepKind(strings.TrimSpace(value))
	if !kind.Valid() {
		return nil
	}
	return &kind
}

func pipelineReconcileRequestToService(
	req api.PipelineReconcileRequest,
) (pipelinesvc.ReconcilePipelinesRequest, error) {
	serviceReq := pipelinesvc.ReconcilePipelinesRequest{}
	if expected := req.ExpectedSource; expected != nil {
		dataSourceSlug, err := slug.New(expected.DataSourceSlug)
		if err != nil {
			return serviceReq, xerror.InvalidInput(xerror.Slug)
		}
		serviceReq.ExpectedSource = &pipelinesvc.ReconcilePipelinesExpectedSource{
			DataSourceSlug: dataSourceSlug, Repo: expected.Repo, Ref: expected.Ref, Commit: expected.Commit,
		}
	}
	if req.DryRun != nil {
		serviceReq.DryRun = *req.DryRun
	}
	if req.PipelineDir != nil {
		serviceReq.PipelineDir = *req.PipelineDir
	}
	if req.PipelineSlugs != nil {
		pipelineSlugs := make([]slug.Slug, 0, len(*req.PipelineSlugs))
		for _, raw := range *req.PipelineSlugs {
			pipelineSlug, err := slug.New(raw)
			if err != nil {
				return pipelinesvc.ReconcilePipelinesRequest{}, xerror.InvalidInput(xerror.Slug).
					Detailf("invalid pipeline slug %q", raw)
			}
			pipelineSlugs = append(pipelineSlugs, pipelineSlug)
		}
		serviceReq.PipelineSlugs = pipelineSlugs
	}
	return serviceReq, nil
}

func toAPIPipelineReconcileResponse(
	result *pipelinesvc.ReconcilePipelinesResult,
) api.PipelineReconcileResponse {
	if result == nil {
		result = &pipelinesvc.ReconcilePipelinesResult{}
	}

	return api.PipelineReconcileResponse{
		DryRun:                 result.DryRun,
		PipelineDir:            result.PipelineDir,
		RequestedPipelineSlugs: toAPISlugs(result.RequestedPipelineSlugs),
		Source: api.PipelineReconcileSourceMetadata{
			DataSourceSlug: toAPINullableSlug(result.Source.DataSourceSlug),
			Repo:           result.Source.Repo,
			Ref:            result.Source.Ref,
			Commit:         result.Source.Commit,
		},
		Summary: api.PipelineReconcileSummary{
			Scanned:    result.Summary.Scanned,
			Synced:     result.Summary.Synced,
			Deleted:    result.Summary.Deleted,
			Skipped:    result.Summary.Skipped,
			Failed:     result.Summary.Failed,
			DriftReset: result.Summary.DriftReset,
		},
		Synced:      toAPIPipelineReconcileSynced(result.Synced),
		Deleted:     toAPIPipelineReconcileDeleted(result.Deleted),
		Skipped:     toAPIPipelineReconcileSkipped(result.Skipped),
		DriftResets: toAPIPipelineReconcileDriftResets(result.DriftResets),
		Failures:    toAPIPipelineReconcileFailures(result.Failures),
	}
}

func toAPIPipelineValidationResponse(
	result *pipelinesvc.PipelineValidationResult,
) api.PipelineValidationResponse {
	if result == nil {
		return api.PipelineValidationResponse{
			Valid:             false,
			Diagnostics:       []api.PipelineValidationDiagnostic{},
			DataSourceRefSync: emptyAPIPipelineDataSourceRefSyncResult(),
		}
	}

	return api.PipelineValidationResponse{
		Valid:             result.Valid,
		Diagnostics:       toAPIPipelineValidationDiagnostics(result.Diagnostics),
		ImpactReport:      toAPIDriftImpactReportPreviewPtr(result.ImpactReport),
		DataSourceRefSync: toAPIPipelineDataSourceRefSyncResult(result.DataSourceRefSync),
	}
}

func toAPIPipelineValidationResultRecord(
	record *pipelinesvc.PipelineValidationResultRecord,
) api.PipelineValidationResultRecord {
	if record == nil {
		return api.PipelineValidationResultRecord{
			Source:      api.PipelineValidationResultSource(pipelinesvc.PipelineValidationResultSourceUnknown.String()),
			Diagnostics: []api.PipelineValidationDiagnostic{},
		}
	}

	return api.PipelineValidationResultRecord{
		ValidationSlug:   record.Slug.String(),
		ProjectSlug:      record.ProjectSlug.String(),
		EnvironmentSlug:  record.EnvironmentSlug.String(),
		PipelineSlug:     record.PipelineSlug.String(),
		Source:           api.PipelineValidationResultSource(record.Source.String()),
		Valid:            record.Valid,
		SpecHash:         record.SpecHash,
		Diagnostics:      toAPIPipelineValidationDiagnostics(record.Diagnostics),
		ImpactReportSlug: nullableSlug(record.ImpactReportSlug),
		CreatedAt:        record.CreatedAt,
		UpdatedAt:        record.UpdatedAt,
	}
}

func toAPIPipelineValidationDiagnostics(
	input []pipelinesvc.PipelineValidationDiagnostic,
) []api.PipelineValidationDiagnostic {
	if input == nil {
		return []api.PipelineValidationDiagnostic{}
	}

	diagnostics := make([]api.PipelineValidationDiagnostic, len(input))
	for i, diagnostic := range input {
		diagnostics[i] = api.PipelineValidationDiagnostic{
			Severity: api.PipelineValidationDiagnosticSeverity(diagnostic.Severity),
			Code:     diagnostic.Code,
			Field:    diagnostic.Field,
			Message:  diagnostic.Message,
		}
		if diagnostic.StepSlug != nil {
			stepSlug := *diagnostic.StepSlug
			diagnostics[i].StepSlug = &stepSlug
		}
		if diagnostic.DataSourceRef != nil {
			dataSourceRef := *diagnostic.DataSourceRef
			diagnostics[i].DataSourceRef = &dataSourceRef
		}
		if diagnostic.DataSourceRefSlug != nil {
			refSlug := *diagnostic.DataSourceRefSlug
			diagnostics[i].DataSourceRefSlug = &refSlug
		}
		if diagnostic.ResolvedDataSourceSlug != nil {
			resolvedSlug := *diagnostic.ResolvedDataSourceSlug
			diagnostics[i].ResolvedDataSourceSlug = &resolvedSlug
		}
		if diagnostic.EnvironmentSlug != nil {
			environmentSlug := *diagnostic.EnvironmentSlug
			diagnostics[i].EnvironmentSlug = &environmentSlug
		}
		if diagnostic.DataSourceType != nil {
			dsType := api.DataSourceType(diagnostic.DataSourceType.String())
			diagnostics[i].DataSourceType = &dsType
		}
		if diagnostic.ExpectedDataSourceType != nil {
			dsType := api.DataSourceType(diagnostic.ExpectedDataSourceType.String())
			diagnostics[i].ExpectedDataSourceType = &dsType
		}
		if diagnostic.RequiredCapability != nil {
			capability := api.DataSourceRefCapability(diagnostic.RequiredCapability.String())
			diagnostics[i].RequiredCapability = &capability
		}
	}

	return diagnostics
}

func toAPIDriftImpactReportPreviewPtr(report *pipelinesvc.DriftImpactReport) *api.DriftImpactReportPreview {
	if report == nil {
		return nil
	}
	out := api.DriftImpactReportPreview{
		Severity: api.DriftImpactReportSeverity(report.Severity.String()),
		Summary:  report.Summary,
		Changes:  toAPIDriftImpactChanges(report.Changes),
		Impacts:  toAPIDriftImpactImpacts(report.Impacts),
	}
	return &out
}

func toAPIDriftImpactReports(reports []*pipelinesvc.DriftImpactReport) []api.DriftImpactReport {
	out := make([]api.DriftImpactReport, len(reports))
	for i, report := range reports {
		out[i] = toAPIDriftImpactReport(report)
	}
	return out
}

func toAPIDriftImpactReport(report *pipelinesvc.DriftImpactReport) api.DriftImpactReport {
	if report == nil {
		return api.DriftImpactReport{
			Changes: []api.DriftImpactChange{},
			Impacts: []api.DriftImpactImpact{},
		}
	}
	return api.DriftImpactReport{
		Slug:           report.Slug.String(),
		ReportType:     api.DriftImpactReportReportType(report.ReportType.String()),
		Status:         api.DriftImpactReportStatus(report.Status.String()),
		Severity:       api.DriftImpactReportSeverity(report.Severity.String()),
		Summary:        report.Summary,
		Changes:        toAPIDriftImpactChanges(report.Changes),
		Impacts:        toAPIDriftImpactImpacts(report.Impacts),
		ProjectSlug:    apiSlugValue(report.ProjectSlug),
		PipelineSlug:   apiSlugPtr(report.PipelineSlug),
		DataSourceSlug: apiSlugPtr(report.DataSourceSlug),
		CreatedAt:      report.CreatedAt,
		SupersededAt:   report.SupersededAt,
	}
}

func toAPIDriftImpactChanges(changes []pipelinedrift.Change) []api.DriftImpactChange {
	out := make([]api.DriftImpactChange, len(changes))
	for i, change := range changes {
		out[i] = api.DriftImpactChange{
			Kind:       change.Kind,
			Path:       change.Path,
			Before:     apiDriftImpactStringValue(change.Before),
			After:      apiDriftImpactStringValue(change.After),
			Severity:   api.DriftImpactReportSeverity(change.Severity.String()),
			ReasonCode: change.ReasonCode,
		}
	}
	return out
}

func apiDriftImpactStringValue(value string) *api.DriftImpactValue {
	out := api.DriftImpactValue{}
	_ = out.FromDriftImpactValue0(value)
	return &out
}

func toAPIDriftImpactImpacts(impacts []pipelinedrift.Impact) []api.DriftImpactImpact {
	out := make([]api.DriftImpactImpact, len(impacts))
	for i, impact := range impacts {
		out[i] = api.DriftImpactImpact{
			Kind:         api.DriftImpactImpactKind(impact.Kind),
			PipelineSlug: nullableStringSlug(optionalString(impact.PipelineSlug)),
			StepSlug:     nullableStringSlug(optionalString(impact.StepSlug)),
			EdgeSlug:     optionalString(impact.EdgeSlug),
			Severity:     api.DriftImpactReportSeverity(impact.Severity.String()),
			Message:      impact.Message,
		}
	}
	return out
}

func apiSlugValue(value *slug.Slug) api.Slug {
	if value == nil {
		return ""
	}
	return value.String()
}

func apiSlugPtr(value *slug.Slug) *api.Slug {
	if value == nil {
		return nil
	}
	out := value.String()
	return &out
}

func toAPISlugs(values []slug.Slug) []api.Slug {
	out := make([]api.Slug, len(values))
	for i, value := range values {
		out[i] = value.String()
	}
	return out
}

func toAPINullableSlug(value *slug.Slug) *api.NullableSlug {
	if value == nil {
		return nil
	}
	out := value.String()
	return &out
}

func toAPIPipelineReconcileSynced(
	items []pipelinesvc.ReconcilePipelinesSyncedPipeline,
) []api.PipelineReconcileSyncedPipeline {
	out := make([]api.PipelineReconcileSyncedPipeline, len(items))
	for i, item := range items {
		out[i] = api.PipelineReconcileSyncedPipeline{
			PipelineSlug: item.PipelineSlug.String(),
			SourcePath:   item.SourcePath,
			SpecHash:     item.SpecHash,
		}
	}
	return out
}

func toAPIPipelineReconcileDeleted(
	items []pipelinesvc.ReconcilePipelinesDeletedPipeline,
) []api.PipelineReconcileDeletedPipeline {
	out := make([]api.PipelineReconcileDeletedPipeline, len(items))
	for i, item := range items {
		out[i] = api.PipelineReconcileDeletedPipeline{
			PipelineSlug:       item.PipelineSlug.String(),
			PreviousSourcePath: item.PreviousSourcePath,
		}
	}
	return out
}

func toAPIPipelineReconcileSkipped(
	items []pipelinesvc.ReconcilePipelinesSkippedPipeline,
) []api.PipelineReconcileSkippedPipeline {
	out := make([]api.PipelineReconcileSkippedPipeline, len(items))
	for i, item := range items {
		out[i] = api.PipelineReconcileSkippedPipeline{
			PipelineSlug: item.PipelineSlug.String(),
			SourcePath:   item.SourcePath,
			Reason:       toAPIPipelineReconcileSkippedReason(item.Reason),
		}
	}
	return out
}

func toAPIPipelineReconcileSkippedReason(reason string) api.PipelineReconcileSkippedReason {
	switch reason {
	case string(api.PipelineReconcileSkippedReasonFilteredOut):
		return api.PipelineReconcileSkippedReasonFilteredOut
	case string(api.PipelineReconcileSkippedReasonNotGitManaged):
		return api.PipelineReconcileSkippedReasonNotGitManaged
	default:
		return api.PipelineReconcileSkippedReasonFilteredOut
	}
}

func toAPIPipelineReconcileDriftResets(
	items []pipelinesvc.ReconcilePipelinesDriftReset,
) []api.PipelineReconcileDriftReset {
	out := make([]api.PipelineReconcileDriftReset, len(items))
	for i, item := range items {
		out[i] = api.PipelineReconcileDriftReset{
			PipelineSlug:     item.PipelineSlug.String(),
			SourcePath:       item.SourcePath,
			PreviousSpecHash: item.PreviousSpecHash,
			AppliedSpecHash:  item.AppliedSpecHash,
		}
	}
	return out
}

func toAPIPipelineReconcileFailures(
	items []pipelinesvc.ReconcilePipelinesFailure,
) []api.PipelineReconcileFailure {
	out := make([]api.PipelineReconcileFailure, len(items))
	for i, item := range items {
		out[i] = api.PipelineReconcileFailure{
			PipelinePath: item.PipelinePath,
			PipelineSlug: toAPINullableSlug(item.PipelineSlug),
			Stage:        toAPIPipelineReconcileFailureStage(item.Stage),
			Message:      item.Message,
		}
	}
	return out
}

func toAPIPipelineReconcileFailureStage(stage string) api.PipelineReconcileFailureStage {
	switch stage {
	case string(api.PipelineReconcileFailureStageRead):
		return api.PipelineReconcileFailureStageRead
	case string(api.PipelineReconcileFailureStageLoad):
		return api.PipelineReconcileFailureStageLoad
	case string(api.PipelineReconcileFailureStageMarshal):
		return api.PipelineReconcileFailureStageMarshal
	case string(api.PipelineReconcileFailureStageSource):
		return api.PipelineReconcileFailureStageSource
	case string(api.PipelineReconcileFailureStageTransaction):
		return api.PipelineReconcileFailureStageTransaction
	default:
		return api.PipelineReconcileFailureStageTransaction
	}
}

func toAPIPipelineRef(item *pipelinesvc.PipelineSummary) (api.PipelineRef, error) {
	if item == nil {
		return api.PipelineRef{}, xerror.Unexpected().Detail("pipeline summary is missing")
	}
	sourcePath, sourceRef, lastAppliedCommit, err := pipelineGitProvenance(item)
	if err != nil {
		return api.PipelineRef{}, err
	}

	return api.PipelineRef{
		Slug:                   item.Slug.String(),
		Name:                   item.Name,
		SyncStatus:             api.PipelineSyncStatus(item.SyncStatus.String()),
		SourcePath:             sourcePath,
		SourceRef:              sourceRef,
		LastAppliedCommit:      lastAppliedCommit,
		CurrentVersionSpecHash: item.CurrentVersionSpecHash,
		HasDraft:               item.HasDraft,
		CreatedAt:              item.CreatedAt,
		UpdatedAt:              item.UpdatedAt,
	}, nil
}

func toAPIPipeline(orgSlug slug.Slug, item *pipelinesvc.PipelineDetail) (api.Pipeline, error) {
	if item == nil || item.Summary == nil || item.Spec == nil {
		return api.Pipeline{}, xerror.Unexpected().With(
			xerror.Unexpected().Detail("pipeline detail is incomplete"),
		)
	}

	spec, err := pipelineSpecToAPI(item.Spec)
	if err != nil {
		return api.Pipeline{}, err
	}
	sourcePath, sourceRef, lastAppliedCommit, err := pipelineGitProvenance(item.Summary)
	if err != nil {
		return api.Pipeline{}, err
	}

	var draft *api.PipelineDraft
	if item.Draft != nil {
		mappedDraft, mapErr := toAPIPipelineDraft(item.Draft)
		if mapErr != nil {
			return api.Pipeline{}, mapErr
		}
		draft = &mappedDraft
	}

	return api.Pipeline{
		Slug:                   item.Summary.Slug.String(),
		Name:                   item.Summary.Name,
		HasDraft:               item.Summary.HasDraft,
		SyncStatus:             api.PipelineSyncStatus(item.Summary.SyncStatus.String()),
		SourcePath:             sourcePath,
		SourceRef:              sourceRef,
		LastAppliedCommit:      lastAppliedCommit,
		CurrentVersionSpecHash: item.Summary.CurrentVersionSpecHash,
		CreatedAt:              item.Summary.CreatedAt,
		UpdatedAt:              item.Summary.UpdatedAt,
		Spec:                   spec,
		Draft:                  draft,
	}, nil
}

func pipelineGitProvenance(item *pipelinesvc.PipelineSummary) (string, string, string, error) {
	if item == nil || item.SourcePath == nil || strings.TrimSpace(*item.SourcePath) == "" ||
		item.SourceRef == nil || strings.TrimSpace(*item.SourceRef) == "" ||
		item.LastAppliedCommit == nil || strings.TrimSpace(*item.LastAppliedCommit) == "" {
		return "", "", "", xerror.Unexpected().Detail(
			"materialized pipeline is missing required Git provenance",
		)
	}
	return strings.TrimSpace(*item.SourcePath),
		strings.TrimSpace(*item.SourceRef),
		strings.TrimSpace(*item.LastAppliedCommit),
		nil
}

func requestBodyHasKey(body []byte, key string) bool {
	if len(body) == 0 {
		return false
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	_, ok := raw[key]
	return ok
}

func toAPIPipelineDeployment(item *pipelinesvc.PipelineDeploymentDetail) api.PipelineDeployment {
	if item == nil {
		return api.PipelineDeployment{
			DataSourceRefSync: emptyAPIPipelineDataSourceRefSyncResult(),
			ProofLink:         nil,
		}
	}

	return api.PipelineDeployment{
		Revision:                     item.Revision,
		CurrentResolutionDigest:      item.CurrentResolutionDigest,
		PreviousResolutionDigest:     item.PreviousResolutionDigest,
		PipelineSlug:                 item.PipelineSlug.String(),
		ProjectSlug:                  item.ProjectSlug.String(),
		EnvironmentSlug:              item.EnvironmentSlug.String(),
		CurrentVersionSpecHash:       item.CurrentVersionSpecHash,
		CurrentVersionShortSpecHash:  item.CurrentVersionShortSpecHash,
		PreviousVersionSpecHash:      item.PreviousVersionSpecHash,
		PreviousVersionShortSpecHash: item.PreviousVersionShortSpecHash,
		DeployedByActorType:          api.EventActorType(item.DeployedByActorType),
		DeployedByActorSlug:          nullableStringSlug(item.DeployedByActorSlug),
		DeployedAt:                   item.DeployedAt,
		UpdatedAt:                    item.UpdatedAt,
		DataSourceRefSync:            toAPIPipelineDataSourceRefSyncResult(item.DataSourceRefSync),
		ProofLink:                    toAPIProofLifecycleLink(item.ProofLink),
	}
}

// toAPIPipelineDataSourceRefSyncResult maps service ref sync evidence to API DTOs.
func toAPIPipelineDataSourceRefSyncResult(
	result *datasourcerefssvc.PipelineDataSourceRefSyncResult,
) api.PipelineDataSourceRefSyncResult {
	if result == nil {
		return emptyAPIPipelineDataSourceRefSyncResult()
	}
	refs := make([]api.PipelineDataSourceRefSyncItem, len(result.Items))
	for i, item := range result.Items {
		refs[i] = api.PipelineDataSourceRefSyncItem{
			DataSourceRefSlug:    item.RefSlug.String(),
			DataSourceType:       dataSourceTypeToNullableAPI(item.DataSourceType),
			PipelineSlug:         item.PipelineSlug.String(),
			RequiredCapabilities: dataSourceRefCapabilitiesToAPI(item.RequiredCapabilities),
			Status:               api.PipelineDataSourceRefSyncStatus(item.Status.String()),
		}
	}
	return api.PipelineDataSourceRefSyncResult{DataSourceRefs: refs}
}

// emptyAPIPipelineDataSourceRefSyncResult returns the required empty sync payload.
func emptyAPIPipelineDataSourceRefSyncResult() api.PipelineDataSourceRefSyncResult {
	return api.PipelineDataSourceRefSyncResult{
		DataSourceRefs: []api.PipelineDataSourceRefSyncItem{},
	}
}

func nullableStringSlug(value *string) *api.NullableSlug {
	if value == nil || *value == "" {
		return nil
	}
	ret := *value
	return &ret
}

func toAPIPipelineDraft(item *pipelinesvc.PipelineDraftDetail) (api.PipelineDraft, error) {
	if item == nil || item.Spec == nil {
		return api.PipelineDraft{}, xerror.Unexpected().Detail(
			"pipeline draft detail is incomplete",
		)
	}

	spec, err := pipelineSpecToAPI(item.Spec)
	if err != nil {
		return api.PipelineDraft{}, err
	}

	return api.PipelineDraft{
		PipelineSlug:          item.PipelineSlug.String(),
		InitiatingMember:      item.InitiatingMember.String(),
		BasedOnSpecHash:       item.BasedOnSpecHash,
		DraftState:            api.PipelineDraftDraftState(item.DraftState),
		DraftSpecHash:         item.DraftSpecHash,
		Repository:            item.Repository,
		ProposalSupported:     item.ProposalSupported,
		PublicationMode:       publicationModeToAPI(item.PublicationMode),
		ProviderActor:         item.ProviderActor,
		AuthoritySource:       item.AuthoritySource,
		BaseRef:               item.BaseRef,
		BaseCommitSha:         item.BaseCommitSHA,
		BranchName:            item.BranchName,
		BranchHeadSha:         item.BranchHeadSHA,
		ProposalProvider:      proposalProviderToAPI(item.ProposalProvider),
		ProposalId:            item.ProposalID,
		ProposalUrl:           item.ProposalURL,
		ProposalState:         proposalStateToAPI(item.ProposalState),
		ProposalIsStale:       item.ProposalIsStale,
		LastProposedDraftHash: item.LastProposedDraftHash,
		QueuedAt:              item.QueuedAt,
		ProposalUpdatedAt:     item.ProposalUpdatedAt,
		FailedAt:              item.FailedAt,
		FailureCode:           item.FailureCode,
		FailureMessage:        item.FailureMessage,
		Spec:                  spec,
		CreatedAt:             item.CreatedAt,
		UpdatedAt:             item.UpdatedAt,
	}, nil
}

func publicationModeToAPI(value *string) *api.PipelineDraftPublicationMode {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	mode := api.PipelineDraftPublicationMode(strings.TrimSpace(*value))
	return &mode
}

func proposalProviderToAPI(value *string) *api.PipelineDraftProposalProvider {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	provider := api.PipelineDraftProposalProvider(strings.TrimSpace(*value))
	return &provider
}

func proposalStateToAPI(value *string) *api.PipelineDraftProposalState {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	state := api.PipelineDraftProposalState(strings.TrimSpace(*value))
	return &state
}

func toAPIPipelineDraftConflictResponse(err error) (api.PipelineDraftConflictResponse, bool) {
	var conflict *pipelinesvc.PipelineDraftConflictError
	if !xerror.As(err, &conflict) || conflict == nil {
		return api.PipelineDraftConflictResponse{}, false
	}

	response := api.PipelineDraftConflictResponse{
		CanOverwrite:         conflict.CanOverwrite,
		CurrentSpecHash:      conflict.CurrentSpecHash,
		DraftBasedOnSpecHash: conflict.DraftBasedOnSpecHash,
		DraftPreserved:       conflict.DraftPreserved,
		Error: api.Error{
			Code:    pipelinesvc.ErrPipelineDraftConflict.Code(),
			Message: pipelinesvc.ErrPipelineDraftConflict.Message(),
		},
		ExpectedSpecHash: conflict.ExpectedSpecHash,
		PipelineSlug:     conflict.PipelineSlug.String(),
	}
	if detailedError := xerror.PublicDetail(err); detailedError != "" {
		response.Error.DetailedError = &detailedError
	}

	return response, true
}

func toAPIPipelineVersionRef(item *pipelinesvc.PipelineVersionSummary) api.PipelineVersionRef {
	if item == nil {
		return api.PipelineVersionRef{}
	}

	return api.PipelineVersionRef{
		SpecHash:      item.SpecHash,
		ShortSpecHash: item.ShortSpecHash,
		PipelineSlug:  item.PipelineSlug.String(),
		GitCommitSha:  item.GitCommitSHA,
		CreatedAt:     item.CreatedAt,
	}
}

func toAPIPipelineVersion(item *pipelinesvc.PipelineVersionDetail) (api.PipelineVersion, error) {
	if item == nil || item.Summary == nil || item.Spec == nil {
		return api.PipelineVersion{}, xerror.Unexpected().With(
			xerror.Unexpected().Detail("pipeline version detail is incomplete"),
		)
	}

	spec, err := pipelineSpecToAPI(item.Spec)
	if err != nil {
		return api.PipelineVersion{}, err
	}

	return api.PipelineVersion{
		SpecHash:      item.Summary.SpecHash,
		ShortSpecHash: item.Summary.ShortSpecHash,
		PipelineSlug:  item.Summary.PipelineSlug.String(),
		GitCommitSha:  item.Summary.GitCommitSHA,
		CreatedAt:     item.Summary.CreatedAt,
		Spec:          spec,
	}, nil
}

func toAPIPipelineSpecFormatDiagnostics(
	diagnostics []pipelineformat.Diagnostic,
) []api.PipelineSpecFormatDiagnostic {
	out := make([]api.PipelineSpecFormatDiagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		out = append(out, api.PipelineSpecFormatDiagnostic{
			Code:               api.PipelineSpecFormatDiagnosticCode(diagnostic.Code),
			Message:            diagnostic.Message,
			StepSlug:           diagnostic.StepSlug,
			ReferencedStepSlug: diagnostic.ReferencedStepSlug,
		})
	}
	return out
}

func pipelineSpecToAPI(spec *pipelinespec.Pipeline) (api.PipelineSpec, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return api.PipelineSpec{}, xerror.InvalidInput(xerror.JSON).With(err)
	}

	var out api.PipelineSpec
	if err := json.Unmarshal(data, &out); err != nil {
		return api.PipelineSpec{}, xerror.InvalidInput(xerror.JSON).With(err)
	}

	return out, nil
}

// rawPipelineSpecFromBody extracts the raw spec before generated API structs can
// drop unknown nested keys that the pipeline spec loader must reject.
func rawPipelineSpecFromBody(data []byte) (json.RawMessage, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, xerror.InvalidInput(xerror.JSON).With(err)
	}
	spec, ok := body["spec"]
	if !ok || len(spec) == 0 {
		return nil, xerror.InvalidInput(xerror.Pipeline).Detail("pipeline spec is required")
	}
	return spec, nil
}

// pipelineFromCreateBody loads a create request spec through the shared
// pipeline syntax and validation path.
func pipelineFromCreateBody(data []byte) (*pipelinespec.Pipeline, error) {
	spec, err := rawPipelineSpecFromBody(data)
	if err != nil {
		return nil, err
	}

	return loadPipelineFromRawSpec("pipeline.pipeline.json", spec)
}

// pipelineFromCreateBodyPermissive parses a create spec without strict validation.
// It is used in draft-save flows where incomplete specs must be accepted.
func pipelineFromCreateBodyPermissive(data []byte) (*pipelinespec.Pipeline, error) {
	spec, err := rawPipelineSpecFromBody(data)
	if err != nil {
		return nil, err
	}

	return decodePipelineFromRawSpec("pipeline.pipeline.json", spec)
}

// pipelineFromUpdateBody injects the route slug before loading an update request
// spec through the shared pipeline syntax and validation path.
func pipelineFromUpdateBody(data []byte, pipelineSlug slug.Slug) (*pipelinespec.Pipeline, error) {
	spec, err := rawUpdatePipelineSpecFromBody(data, pipelineSlug)
	if err != nil {
		return nil, err
	}

	return loadPipelineFromRawSpec(pipelineSlug.String()+".pipeline.json", spec)
}

// loadPipelineFromRawSpec maps low-level decode failures into client input
// errors while preserving existing validation xerrors from the shared loader.
func loadPipelineFromRawSpec(path string, data json.RawMessage) (*pipelinespec.Pipeline, error) {
	pipeline, err := pipelinevalidate.Load(path, data)
	if err == nil {
		return pipeline, nil
	}

	return nil, pipelineSpecInputError(err)
}

// decodePipelineFromRawSpec applies strict syntax decoding without semantic
// validation for API flows that intentionally accept incomplete draft specs.
func decodePipelineFromRawSpec(path string, data json.RawMessage) (*pipelinespec.Pipeline, error) {
	pipeline, err := pipelinespec.Load(path, data)
	if err == nil {
		return pipeline, nil
	}

	return nil, pipelineSpecInputError(err)
}

// pipelineSpecInputError preserves typed validation errors and maps parser
// failures from the shared spec loader to HTTP 400-compatible input errors.
func pipelineSpecInputError(err error) error {
	var xe *xerror.Error
	if xerror.As(err, &xe) {
		return err
	}
	return xerror.InvalidInput(xerror.Pipeline).With(err)
}

// pipelineFromUpdateBodyPermissive injects the route slug into an update request
// spec before permissive draft parsing.
func pipelineFromUpdateBodyPermissive(data []byte, pipelineSlug slug.Slug) (*pipelinespec.Pipeline, error) {
	spec, err := rawUpdatePipelineSpecFromBody(data, pipelineSlug)
	if err != nil {
		return nil, err
	}

	return decodePipelineFromRawSpec(pipelineSlug.String()+".pipeline.json", spec)
}

// rawUpdatePipelineSpecFromBody rewrites the request spec metadata slug from the
// route so update and draft-save requests cannot target a different pipeline.
func rawUpdatePipelineSpecFromBody(data []byte, pipelineSlug slug.Slug) (json.RawMessage, error) {
	spec, err := rawPipelineSpecFromBody(data)
	if err != nil {
		return nil, err
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(spec, &object); err != nil {
		return nil, xerror.InvalidInput(xerror.JSON).With(err)
	}

	metadata := make(map[string]json.RawMessage)
	if rawMetadata, ok := object["metadata"]; ok && len(rawMetadata) > 0 {
		if err := json.Unmarshal(rawMetadata, &metadata); err != nil {
			return nil, xerror.InvalidInput(xerror.JSON).With(err)
		}
	}
	slugData, err := json.Marshal(pipelineSlug.String())
	if err != nil {
		return nil, xerror.InvalidInput(xerror.JSON).With(err)
	}
	metadata["slug"] = slugData

	metadataData, err := json.Marshal(metadata)
	if err != nil {
		return nil, xerror.InvalidInput(xerror.JSON).With(err)
	}
	object["metadata"] = metadataData

	out, err := json.Marshal(object)
	if err != nil {
		return nil, xerror.InvalidInput(xerror.JSON).With(err)
	}
	return out, nil
}

func optionalSinkReplayStrategy(value string) *api.PipelineRunStepInputSnapshotSinkReplayStrategy {
	if value == "" {
		return nil
	}
	converted := api.PipelineRunStepInputSnapshotSinkReplayStrategy(value)
	return &converted
}

func optionalSinkEffectState(value pipelinerun.SinkEffectState) *api.PipelineRunStepInputSnapshotSinkEffectState {
	if value == pipelinerun.SinkEffectStateUnknown {
		return nil
	}
	converted := api.PipelineRunStepInputSnapshotSinkEffectState(value.String())
	return &converted
}

func optionalSinkDeliveryOutcome(value string) *api.PipelineRunStepInputSnapshotSinkDeliveryOutcome {
	if value == "" {
		return nil
	}
	converted := api.PipelineRunStepInputSnapshotSinkDeliveryOutcome(value)
	return &converted
}

// BootstrapPipelineCheckpoints seeds initial runtime progress without executing a pipeline.
func (s *server) BootstrapPipelineCheckpoints(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, environmentParam api.EnvironmentSlugParam, pipelineParam api.PipelineSlugParam) error {
	org, project, environment, pipeline, err := parseDeploymentSlugs(orgParam, projectParam, environmentParam, pipelineParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Update, model.ResourcePipelines); err != nil {
		return err
	}
	var req api.BootstrapPipelineCheckpointsRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	request, err := slug.New(req.RequestSlug)
	if err != nil {
		return apierror.ValidationFailed("Invalid bootstrap request slug")
	}
	var confirmation *slug.Slug
	if err := protectedConfirmationFromNullableSlug(req.ProtectedEnvironmentConfirmation, &confirmation); err != nil {
		return apierror.ValidationFailed("Invalid protected environment confirmation")
	}
	seeds := make(map[slug.Slug]json.RawMessage, len(req.SourceSeeds))
	for name, seed := range req.SourceSeeds {
		step, err := slug.New(name)
		if err != nil {
			return apierror.ValidationFailed("Invalid bootstrap source step slug")
		}
		seeds[step] = seed
	}
	var input map[string]any
	if req.Input != nil {
		input = *req.Input
	}
	if err := s.pipelinesService.BootstrapCheckpoints(c.Context(), org, project, environment, pipeline, request, confirmation, s.eventActor(c), input, seeds); err != nil {
		return mapServiceError(err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func optionalCheckRevision(revision int64) *int64 {
	if revision == 0 {
		return nil
	}
	return &revision
}
