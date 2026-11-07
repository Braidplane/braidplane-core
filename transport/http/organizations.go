package http

import (
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/retentionsvc"
)

func (s *server) ListOrganizations(c fiber.Ctx, params api.ListOrganizationsParams) error {
	if err := s.can(c, model.Read, model.ResourceOrganizations); err != nil {
		return err
	}

	listReq := toOrganizationListRequest(params)

	userSlug := reqctx.GetUserSlug(c, s.sessionStore)
	orgs, err := s.orgService.ListOrganizations(c.Context(), userSlug, listReq)
	if err != nil {
		return mapServiceError(err)
	}

	apiOrgs := make([]api.OrganizationWithRole, 0, len(orgs.Items))
	for _, org := range orgs.Items {
		apiOrgs = append(apiOrgs, orgWithRoleToHTTP(org))
	}

	return c.JSON(api.OrganizationListResponse{
		Organizations: apiOrgs,
		HasMore:       orgs.HasMore,
		NextCursor:    orgs.NextCursor,
		PrevCursor:    orgs.PrevCursor,
	})
}

func (s *server) DeleteOrganization(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed("invalid organization slug")
	}
	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceOrganizations); err != nil {
		return err
	}
	var request api.DeleteOrganizationJSONBody
	if err := c.Bind().Body(&request); err != nil {
		return apierror.InvalidRequestBody()
	}
	if request.Confirmation != orgSlug.String() {
		return apierror.ValidationFailed("type the organization slug to confirm deletion")
	}
	if err := s.orgService.DeleteOrganization(c.Context(), orgSlug); err != nil {
		return mapServiceError(err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) CreateOrganization(c fiber.Ctx) error {
	if err := s.can(c, model.Create, model.ResourceOrganizations); err != nil {
		return err
	}

	var req api.NewOrganization
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	userSlug := reqctx.GetUserSlug(c, s.sessionStore)

	var orgSlug slug.Slug
	if req.Slug != nil {
		var err error
		orgSlug, err = slug.New(*req.Slug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
		}
	}

	org, err := s.orgService.CreateOrganization(
		c.Context(),
		userSlug,
		s.eventActor(c),
		model.NewOrganization{
			Name: req.Name,
			Slug: orgSlug,
		},
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.Status(fiber.StatusCreated).JSON(orgToHTTP(org))
}

func (s *server) GetOrganization(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(
		c,
		orgSlug,
		model.Read,
		model.ResourceOrganizations,
	); err != nil {
		return err
	}

	org, err := s.orgService.GetOrganization(c.Context(), orgSlug)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(orgToHTTP(org))
}

func (s *server) UpdateOrganization(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(
		c,
		orgSlug,
		model.Update,
		model.ResourceOrganizations,
	); err != nil {
		return err
	}

	var req api.UpdateOrganization
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	_, err = s.orgService.UpdateOrganization(
		c.Context(),
		orgSlug,
		s.eventActor(c),
		model.UpdateOrganization{Name: req.Name},
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) GetOrganizationRetentionPolicy(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceOrganizations); err != nil {
		return err
	}
	policy, err := s.orgService.GetRetentionPolicy(c.Context(), orgSlug)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(retentionPolicyToHTTP(policy))
}

func (s *server) UpdateOrganizationRetentionPolicy(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}
	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceOrganizations); err != nil {
		return err
	}
	var req api.OrganizationRetentionPolicy
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	policy, err := s.orgService.UpdateRetentionPolicy(
		c.Context(), orgSlug, s.eventActor(c), retentionPolicyFromHTTP(req),
	)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(retentionPolicyToHTTP(policy))
}

func (s *server) ListOrganizationRetentionRuns(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceOrganizations); err != nil {
		return err
	}

	if _, err := s.orgService.GetOrganization(c.Context(), orgSlug); err != nil {
		return mapServiceError(err)
	}
	runs, err := s.retentionService.ListMaintenanceRuns(c.Context(), orgSlug)
	if err != nil {
		return mapServiceError(err)
	}

	apiRuns := make([]api.OrganizationRetentionRun, 0, len(runs))
	for _, run := range runs {
		apiRuns = append(apiRuns, retentionRunToHTTP(run))
	}
	return c.JSON(api.OrganizationRetentionRunList{
		Schedule: api.OrganizationRetentionSchedule{
			Cadence:   api.OrganizationRetentionScheduleCadenceDaily,
			Timezone:  api.OrganizationRetentionScheduleTimezoneUTC,
			NextRunAt: retentionsvc.NextRunAt(time.Now()),
		},
		Runs: apiRuns,
	})
}

func retentionPolicyToHTTP(policy model.OrganizationRetentionPolicy) api.OrganizationRetentionPolicy {
	return api.OrganizationRetentionPolicy{
		QueryExecutionRetentionDays:   policy.QueryExecutionRetentionDays,
		ReplayCheckpointRetentionDays: policy.ReplayCheckpointRetentionDays,
		RunHistoryRetentionDays:       policy.RunHistoryRetentionDays,
		EventLogRetentionDays:         policy.EventLogRetentionDays,
	}
}

func retentionRunToHTTP(run model.OrganizationRetentionRun) api.OrganizationRetentionRun {
	status := api.OrganizationRetentionRunStatusFailed
	if run.Status == model.OrganizationRetentionRunStatusSucceeded {
		status = api.OrganizationRetentionRunStatusSucceeded
	}

	var policy *api.OrganizationRetentionPolicy
	if run.PolicySnapshot != nil {
		mapped := retentionPolicyToHTTP(*run.PolicySnapshot)
		policy = &mapped
	}

	var failureCode *api.OrganizationRetentionRunFailureCode
	if run.FailureCode != "" {
		code := api.OrganizationRetentionRunFailureCodeMaintenanceFailed
		if run.FailureCode == model.OrganizationRetentionRunFailureCodeInvalidPolicy {
			code = api.OrganizationRetentionRunFailureCodeInvalidPolicy
		}
		failureCode = &code
	}

	return api.OrganizationRetentionRun{
		Slug:           run.Slug.String(),
		Status:         status,
		Attempt:        run.Attempt,
		PolicySnapshot: policy,
		Outcome: api.OrganizationRetentionOutcome{
			QueryExecutionsDeleted:   run.QueryExecutionsDeleted,
			ReplayCheckpointsDeleted: run.ReplayCheckpointsDeleted,
			PipelineRunsDeleted:      run.PipelineRunsDeleted,
			EventLogsDeleted:         run.EventsDeleted,
		},
		FailureCode: failureCode,
		StartedAt:   run.StartedAt,
		CompletedAt: run.CompletedAt,
	}
}

func retentionPolicyFromHTTP(policy api.OrganizationRetentionPolicy) model.OrganizationRetentionPolicy {
	return model.OrganizationRetentionPolicy{
		QueryExecutionRetentionDays:   policy.QueryExecutionRetentionDays,
		ReplayCheckpointRetentionDays: policy.ReplayCheckpointRetentionDays,
		RunHistoryRetentionDays:       policy.RunHistoryRetentionDays,
		EventLogRetentionDays:         policy.EventLogRetentionDays,
	}
}

func orgToHTTP(org *model.Organization) api.Organization {
	return api.Organization{
		Name:      org.Name,
		Slug:      org.Slug.String(),
		CreatedAt: org.CreatedAt,
		UpdatedAt: org.UpdatedAt,
	}
}

func orgWithRoleToHTTP(org model.OrganizationWithRole) api.OrganizationWithRole {
	return api.OrganizationWithRole{
		Name:       org.Name,
		Slug:       org.Slug.String(),
		MemberRole: org.MemberRole,
		CreatedAt:  org.CreatedAt,
		UpdatedAt:  org.UpdatedAt,
	}
}
