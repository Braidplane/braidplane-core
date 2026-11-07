package http

import (
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// CreateMemberInvitation creates a pending invitation and queues its email delivery.
func (s *server) CreateMemberInvitation(c fiber.Ctx, slugParam api.SlugParam) error {
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
	var inviterUserSlug slug.Slug
	if principal, ok := reqctx.GetUserPrincipal(c, s.sessionStore); ok {
		inviterUserSlug = principal.Slug
	}

	var req api.NewMemberInvitation
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	roleSlug, err := slug.New(req.Role)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid role slug: %v", err))
	}

	invitation, err := s.membersService.CreateMemberInvitation(
		c.Context(),
		orgSlug,
		inviterUserSlug,
		s.eventActor(c),
		model.NewMemberInvitation{
			Email: string(req.Email),
			Role:  roleSlug,
		},
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.Status(fiber.StatusCreated).JSON(memberInvitationToHTTP(*invitation))
}

// ListMemberInvitations returns organization invitations separate from active members.
func (s *server) ListMemberInvitations(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ListMemberInvitationsParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceMembers); err != nil {
		return err
	}

	invitations, err := s.membersService.ListMemberInvitations(
		c.Context(),
		orgSlug,
		toMemberInvitationsListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	apiInvitations := make([]api.MemberInvitation, 0, len(invitations.Items))
	for _, invitation := range invitations.Items {
		apiInvitations = append(apiInvitations, memberInvitationToHTTP(invitation))
	}

	return c.JSON(api.MemberInvitationListResponse{
		Invitations: apiInvitations,
		HasMore:     invitations.HasMore,
		NextCursor:  invitations.NextCursor,
		PrevCursor:  invitations.PrevCursor,
	})
}

// GetMemberInvitation returns one organization invitation without token material.
func (s *server) GetMemberInvitation(
	c fiber.Ctx,
	slugParam api.SlugParam,
	invitationSlugParam api.InvitationSlugParam,
) error {
	orgSlug, invitationSlug, err := parseInvitationPath(slugParam, invitationSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceMembers); err != nil {
		return err
	}

	invitation, err := s.membersService.GetMemberInvitation(c.Context(), orgSlug, invitationSlug)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(memberInvitationToHTTP(*invitation))
}

// ResendMemberInvitation rotates a pending invitation token and queues a new email.
func (s *server) ResendMemberInvitation(
	c fiber.Ctx,
	slugParam api.SlugParam,
	invitationSlugParam api.InvitationSlugParam,
) error {
	orgSlug, invitationSlug, err := parseInvitationPath(slugParam, invitationSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceMembers); err != nil {
		return err
	}

	if err := s.checkMemberAdmission(c.Context(), orgSlug); err != nil {
		return mapServiceError(err)
	}

	invitation, err := s.membersService.ResendMemberInvitation(
		c.Context(),
		orgSlug,
		invitationSlug,
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(memberInvitationToHTTP(*invitation))
}

// RevokeMemberInvitation marks a pending invitation revoked.
func (s *server) RevokeMemberInvitation(
	c fiber.Ctx,
	slugParam api.SlugParam,
	invitationSlugParam api.InvitationSlugParam,
) error {
	orgSlug, invitationSlug, err := parseInvitationPath(slugParam, invitationSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceMembers); err != nil {
		return err
	}

	invitation, err := s.membersService.RevokeMemberInvitation(
		c.Context(),
		orgSlug,
		invitationSlug,
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(memberInvitationToHTTP(*invitation))
}

// ListMyMemberInvitations returns pending invitations addressed to the authenticated user's verified email.
func (s *server) ListMyMemberInvitations(c fiber.Ctx, params api.ListMyMemberInvitationsParams) error {
	userSlug, err := invitationUserPrincipal(c, s.sessionStore)
	if err != nil {
		return err
	}

	invitations, err := s.membersService.ListMyMemberInvitations(
		c.Context(),
		userSlug,
		toMyMemberInvitationsListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	apiInvitations := make([]api.MemberInvitation, 0, len(invitations.Items))
	for _, invitation := range invitations.Items {
		apiInvitations = append(apiInvitations, memberInvitationToHTTP(invitation))
	}

	return c.JSON(api.MemberInvitationListResponse{
		Invitations: apiInvitations,
		HasMore:     invitations.HasMore,
		NextCursor:  invitations.NextCursor,
		PrevCursor:  invitations.PrevCursor,
	})
}

// AcceptMemberInvitation accepts a pending invitation for the authenticated user principal.
func (s *server) AcceptMemberInvitation(c fiber.Ctx) error {
	userSlug, err := invitationUserPrincipal(c, s.sessionStore)
	if err != nil {
		return err
	}

	var req api.AcceptMemberInvitationJSONRequestBody
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	accepted, err := s.membersService.AcceptMemberInvitation(
		c.Context(),
		userSlug,
		req.Token,
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(api.MemberInvitationAcceptance{
		Invitation: memberInvitationToHTTP(accepted.Invitation),
		Member:     memberToHTTP(accepted.Member),
	})
}

// AcceptMyMemberInvitation accepts an invitation selected from the authenticated user's pending list.
func (s *server) AcceptMyMemberInvitation(
	c fiber.Ctx,
	slugParam api.SlugParam,
	invitationSlugParam api.InvitationSlugParam,
) error {
	userSlug, err := invitationUserPrincipal(c, s.sessionStore)
	if err != nil {
		return err
	}

	orgSlug, invitationSlug, err := parseInvitationPath(slugParam, invitationSlugParam)
	if err != nil {
		return err
	}

	accepted, err := s.membersService.AcceptMyMemberInvitation(
		c.Context(),
		userSlug,
		orgSlug,
		invitationSlug,
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(api.MemberInvitationAcceptance{
		Invitation: memberInvitationToHTTP(accepted.Invitation),
		Member:     memberToHTTP(accepted.Member),
	})
}

// DeclineMemberInvitation declines a pending invitation for the authenticated user principal.
func (s *server) DeclineMemberInvitation(c fiber.Ctx) error {
	userSlug, err := invitationUserPrincipal(c, s.sessionStore)
	if err != nil {
		return err
	}

	var req api.MemberInvitationTokenRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	invitation, err := s.membersService.DeclineMemberInvitation(
		c.Context(),
		userSlug,
		req.Token,
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(memberInvitationToHTTP(*invitation))
}

// DeclineMyMemberInvitation declines an invitation selected from the authenticated user's pending list.
func (s *server) DeclineMyMemberInvitation(
	c fiber.Ctx,
	slugParam api.SlugParam,
	invitationSlugParam api.InvitationSlugParam,
) error {
	userSlug, err := invitationUserPrincipal(c, s.sessionStore)
	if err != nil {
		return err
	}

	orgSlug, invitationSlug, err := parseInvitationPath(slugParam, invitationSlugParam)
	if err != nil {
		return err
	}

	invitation, err := s.membersService.DeclineMyMemberInvitation(
		c.Context(),
		userSlug,
		orgSlug,
		invitationSlug,
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(memberInvitationToHTTP(*invitation))
}

func invitationUserPrincipal(c fiber.Ctx, store *session.Store) (slug.Slug, error) {
	if _, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		return "", apierror.UserPrincipalRequired()
	}

	principal, ok := reqctx.GetUserPrincipal(c, store)
	if !ok {
		return "", apierror.Unauthorized()
	}

	return principal.Slug, nil
}

func parseInvitationPath(slugParam api.SlugParam, invitationSlugParam api.InvitationSlugParam) (slug.Slug, slug.Slug, error) {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	invitationSlug, err := slug.New(invitationSlugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid invitation slug: %v", err))
	}

	return orgSlug, invitationSlug, nil
}

func memberInvitationToHTTP(invitation model.MemberInvitation) api.MemberInvitation {
	return api.MemberInvitation{
		Slug:              invitation.Slug.String(),
		OrganizationSlug:  invitation.OrganizationSlug.String(),
		Email:             api.Email(invitation.Email),
		Role:              api.MemberRole(invitation.Role),
		Status:            api.MemberInvitationStatus(invitation.Status.String()),
		InvitedByUserSlug: slugPtrToHTTP(invitation.InvitedByUserSlug),
		AcceptedUserSlug:  slugPtrToHTTP(invitation.AcceptedUserSlug),
		ExpiresAt:         invitation.ExpiresAt,
		LastSentAt:        invitation.LastSentAt,
		AcceptedAt:        timePtrToHTTP(invitation.AcceptedAt),
		DeclinedAt:        timePtrToHTTP(invitation.DeclinedAt),
		RevokedAt:         timePtrToHTTP(invitation.RevokedAt),
		ExpiredAt:         timePtrToHTTP(invitation.ExpiredAt),
		CreatedAt:         invitation.CreatedAt,
		UpdatedAt:         invitation.UpdatedAt,
	}
}

func slugPtrToHTTP(value *slug.Slug) *api.Slug {
	if value == nil {
		return nil
	}
	converted := value.String()
	return &converted
}

func timePtrToHTTP(value *time.Time) *api.NullableTimestamp {
	if value == nil {
		return nil
	}
	converted := *value
	return &converted
}
