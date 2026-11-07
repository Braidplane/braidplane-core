package http

import (
	"fmt"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func (s *server) ListMembers(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ListMembersParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceMembers); err != nil {
		return err
	}

	listReq := toMembersListRequest(params)

	ms, err := s.membersService.ListMembers(c.Context(), orgSlug, listReq)
	if err != nil {
		return mapServiceError(err)
	}

	apiMembers := make([]api.Member, 0, len(ms.Items))
	for _, m := range ms.Items {
		apiMembers = append(apiMembers, memberToHTTP(m))
	}

	return c.JSON(api.MemberListResponse{
		Members:    apiMembers,
		HasMore:    ms.HasMore,
		NextCursor: ms.NextCursor,
		PrevCursor: ms.PrevCursor,
	})
}

func (s *server) CreateMember(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Create, model.ResourceMembers); err != nil {
		return err
	}
	if err := s.checkMemberAdmission(c.Context(), orgSlug); err != nil {
		return mapServiceError(err)
	}
	if err := s.admissionService.CheckMemberAdmission(c.Context(), orgSlug); err != nil {
		return mapServiceError(err)
	}

	var req api.NewMember
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	userSlug, err := slug.New(req.UserSlug)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid user slug: %v", err))
	}

	roleSlug, err := slug.New(req.Role)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid role slug: %v", err))
	}

	m, err := s.membersService.CreateMember(
		c.Context(),
		orgSlug,
		s.eventActor(c),
		model.NewMember{
			UserSlug: userSlug,
			Role:     roleSlug,
		},
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.Status(fiber.StatusCreated).JSON(memberToHTTP(*m))
}

func (s *server) UpdateMember(
	c fiber.Ctx,
	slugParam api.SlugParam,
	userSlugParam api.UserSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	userSlug, err := slug.New(userSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid user slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceMembers); err != nil {
		return err
	}

	var req api.UpdateMember
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	roleSlug, err := slug.New(req.Role)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid role slug: %v", err))
	}

	_, err = s.membersService.UpdateMember(
		c.Context(),
		orgSlug,
		userSlug,
		s.eventActor(c),
		model.UpdateMember{Role: roleSlug},
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) RemoveMember(
	c fiber.Ctx,
	slugParam api.SlugParam,
	userSlugParam api.UserSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	userSlug, err := slug.New(userSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid user slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceMembers); err != nil {
		return err
	}

	err = s.membersService.RemoveMember(c.Context(), orgSlug, userSlug, s.eventActor(c))
	if err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func memberToHTTP(m model.Member) api.Member {
	return api.Member{
		UserSlug:  m.UserSlug.String(),
		Email:     api.Email(m.Email),
		Role:      api.MemberRole(m.Role),
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}
