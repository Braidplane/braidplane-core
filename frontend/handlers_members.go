package frontend

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func (r *Router) handleMembersPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	summary := strings.TrimSpace(c.Query("error"))
	details := ""

	params := &api.ListMembersParams{}
	limit := api.LimitParam(20)
	params.Limit = &limit

	cursor := strings.TrimSpace(c.Query("cursor"))
	if cursor != "" {
		cursorParam := cursor
		params.Cursor = &cursorParam
	}
	invitationParams := defaultMemberInvitationListParams()
	invitationCursor := strings.TrimSpace(c.Query("invitation_cursor"))
	if invitationCursor != "" {
		cursorParam := invitationCursor
		invitationParams.Cursor = &cursorParam
	}
	invitationDirection := strings.TrimSpace(c.Query("direction"))
	switch invitationDirection {
	case pageDirectionPrev:
		direction := api.ListMemberInvitationsParamsDirection(api.PageDirectionParamPrev)
		invitationParams.Direction = &direction
	case pageDirectionNext:
		direction := api.ListMemberInvitationsParamsDirection(api.PageDirectionParamNext)
		invitationParams.Direction = &direction
	}

	memberList, status, errorResponse, listErr := r.listMembersViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		params,
	)
	if listErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		if summary == "" {
			if status == fiber.StatusForbidden {
				summary = "You do not have access to this organization's members."
			} else {
				summary = "Something went wrong while loading members."
			}
		}
		details = backendErrorDetails("list members", status, errorResponse, listErr)
		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Members",
			orgSlug.String(),
			summary,
			details,
			c.Get(fiber.HeaderCookie),
		)
		permissions := r.resolveOrganizationPermissions(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
		)

		c.Status(status)
		return renderTempl(
			c,
			views.MembersPage(
				shellData,
				[]api.Member{},
				[]api.MemberInvitation{},
				false,
				"",
				"",
				false,
				"",
				"",
				shellData.ActiveOrganizationSlug,
				views.DefaultMemberInviteFormData(),
				views.MemberRoleFormData{},
				permissions,
				r.memberInvitationsEnabled(),
			),
		)
	}

	invitationList, invitationStatus, invitationErrorResponse, invitationListErr := r.listMemberInvitationsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		invitationParams,
	)
	if invitationListErr != nil {
		if invitationStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		if summary == "" {
			if invitationStatus == fiber.StatusForbidden {
				summary = "You do not have access to this organization's invitations."
			} else {
				summary = "Something went wrong while loading invitations."
			}
		}
		details = backendErrorDetails(
			"list member invitations",
			invitationStatus,
			invitationErrorResponse,
			invitationListErr,
		)
		c.Status(invitationStatus)
	}
	invitations := []api.MemberInvitation{}
	invitationHasMore := false
	invitationNextCursor := ""
	invitationPrevCursor := ""
	if invitationList != nil {
		invitations = pendingMemberInvitations(invitationList.Invitations)
		invitationHasMore = invitationList.HasMore
		if invitationList.NextCursor != nil {
			invitationNextCursor = *invitationList.NextCursor
		}
		if invitationList.PrevCursor != nil {
			invitationPrevCursor = *invitationList.PrevCursor
		}
	}

	nextCursor := ""
	prevCursor := ""
	if memberList.NextCursor != nil {
		nextCursor = *memberList.NextCursor
	}
	if memberList.PrevCursor != nil {
		prevCursor = *memberList.PrevCursor
	}

	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Members",
		orgSlug.String(),
		summary,
		details,
		c.Get(fiber.HeaderCookie),
	)
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)

	return renderTempl(
		c,
		views.MembersPage(
			shellData,
			memberList.Members,
			invitations,
			memberList.HasMore,
			nextCursor,
			prevCursor,
			invitationHasMore,
			invitationNextCursor,
			invitationPrevCursor,
			shellData.ActiveOrganizationSlug,
			views.DefaultMemberInviteFormData(),
			views.MemberRoleFormData{},
			permissions,
			r.memberInvitationsEnabled(),
		),
	)
}

func (r *Router) handleCreateMemberSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	emailInput := strings.TrimSpace(c.FormValue("email"))
	role := strings.TrimSpace(c.FormValue("role"))
	inviteFormData := memberInviteFormDataFromRequest(c)
	if validationMessage := validateMemberInviteInput(emailInput, role); validationMessage != "" {
		if isHTMXRequest(c) {
			c.Set("HX-Retarget", "#members-content")
			c.Set("HX-Reswap", "outerHTML")
			return r.renderMembersContent(
				c,
				orgSlug,
				http.StatusOK,
				inviteFormData,
				"",
				"Something went wrong while inviting a member.",
				"Validation: "+validationMessage,
				nil,
				nil,
			)
		}

		return r.renderMembersPageWithError(
			c,
			orgSlug,
			http.StatusBadRequest,
			"Something went wrong while inviting a member.",
			"Validation: "+validationMessage,
			inviteFormData,
		)
	}

	createdInvitation, status, errorResponse, createErr := r.createMemberInvitationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		api.CreateMemberInvitationJSONRequestBody{
			Email: api.Email(emailInput),
			Role:  role,
		},
	)
	if createErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		if isHTMXRequest(c) {
			return r.renderMembersContent(
				c,
				orgSlug,
				http.StatusOK,
				inviteFormData,
				"",
				"Something went wrong while inviting a member.",
				backendErrorDetails("create member invitation", status, errorResponse, createErr),
				nil,
				nil,
			)
		}

		return r.renderMembersPageWithError(
			c,
			orgSlug,
			status,
			"Something went wrong while inviting a member.",
			backendErrorDetails("create member invitation", status, errorResponse, createErr),
			inviteFormData,
		)
	}

	if isHTMXRequest(c) {
		successMessage := "Member invited successfully."
		if createdInvitation != nil && createdInvitation.Email != "" {
			successMessage = fmt.Sprintf("Invitation sent to %s.", createdInvitation.Email)
		}

		return r.renderMembersContent(
			c,
			orgSlug,
			http.StatusOK,
			views.DefaultMemberInviteFormData(),
			successMessage,
			"",
			"",
			nil,
			createdInvitation,
		)
	}

	return redirectHTMXOrSeeOther(c, organizationMembersPath(orgSlug))
}

func (r *Router) handleResendMemberInvitationSubmit(c fiber.Ctx) error {
	orgSlug, invitationSlug, err := memberInvitationActionParams(c)
	if err != nil {
		return err
	}

	invitation, status, errorResponse, resendErr := r.resendMemberInvitationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		invitationSlug.String(),
	)
	if resendErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		if isHTMXRequest(c) {
			return r.renderMembersContent(
				c,
				orgSlug,
				http.StatusOK,
				views.DefaultMemberInviteFormData(),
				"",
				"Something went wrong while resending an invitation.",
				backendErrorDetails("resend member invitation", status, errorResponse, resendErr),
				nil,
				nil,
			)
		}

		return r.renderMembersPageWithError(
			c,
			orgSlug,
			status,
			"Something went wrong while resending an invitation.",
			backendErrorDetails("resend member invitation", status, errorResponse, resendErr),
			views.DefaultMemberInviteFormData(),
		)
	}

	if isHTMXRequest(c) {
		successMessage := "Invitation resent successfully."
		if invitation != nil && invitation.Email != "" {
			successMessage = fmt.Sprintf("Invitation resent to %s.", invitation.Email)
		}

		return r.renderMembersContent(
			c,
			orgSlug,
			http.StatusOK,
			views.DefaultMemberInviteFormData(),
			successMessage,
			"",
			"",
			nil,
			invitation,
		)
	}

	return redirectHTMXOrSeeOther(c, organizationMembersPath(orgSlug))
}

func (r *Router) handleRevokeMemberInvitationSubmit(c fiber.Ctx) error {
	orgSlug, invitationSlug, err := memberInvitationActionParams(c)
	if err != nil {
		return err
	}

	invitation, status, errorResponse, revokeErr := r.revokeMemberInvitationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		invitationSlug.String(),
	)
	if revokeErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		if isHTMXRequest(c) {
			return r.renderMembersContent(
				c,
				orgSlug,
				http.StatusOK,
				views.DefaultMemberInviteFormData(),
				"",
				"Something went wrong while revoking an invitation.",
				backendErrorDetails("revoke member invitation", status, errorResponse, revokeErr),
				nil,
				nil,
			)
		}

		return r.renderMembersPageWithError(
			c,
			orgSlug,
			status,
			"Something went wrong while revoking an invitation.",
			backendErrorDetails("revoke member invitation", status, errorResponse, revokeErr),
			views.DefaultMemberInviteFormData(),
		)
	}

	if isHTMXRequest(c) {
		successMessage := "Invitation revoked successfully."
		if invitation != nil && invitation.Email != "" {
			successMessage = fmt.Sprintf("Invitation for %s revoked.", invitation.Email)
		}

		return r.renderMembersContent(
			c,
			orgSlug,
			http.StatusOK,
			views.DefaultMemberInviteFormData(),
			successMessage,
			"",
			"",
			nil,
			invitation,
		)
	}

	return redirectHTMXOrSeeOther(c, organizationMembersPath(orgSlug))
}

func (r *Router) handleUpdateMemberRoleSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	userSlug, err := slug.New(c.Params("userSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid user slug")
	}

	role := strings.TrimSpace(c.FormValue("role"))
	if role == "" {
		if isHTMXRequest(c) {
			c.Set("HX-Retarget", "#members-content")
			c.Set("HX-Reswap", "outerHTML")
			return r.renderMembersContent(
				c,
				orgSlug,
				http.StatusOK,
				views.DefaultMemberInviteFormData(),
				"",
				"Something went wrong while updating a member.",
				"Validation: role is required.",
				nil,
				nil,
			)
		}

		return r.renderMembersPageWithError(
			c,
			orgSlug,
			http.StatusBadRequest,
			"Something went wrong while updating a member.",
			"Validation: role is required.",
			views.DefaultMemberInviteFormData(),
		)
	}

	memberRole := role

	status, errorResponse, updateErr := r.updateMemberViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		userSlug.String(),
		api.UpdateMemberJSONRequestBody{Role: role},
	)
	if updateErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		if isHTMXRequest(c) {
			c.Set("HX-Retarget", "#members-content")
			c.Set("HX-Reswap", "outerHTML")
			return r.renderMembersContent(
				c,
				orgSlug,
				http.StatusOK,
				views.DefaultMemberInviteFormData(),
				"",
				"Something went wrong while updating a member.",
				backendErrorDetails("update member", status, errorResponse, updateErr),
				nil,
				nil,
			)
		}

		return r.renderMembersPageWithError(
			c,
			orgSlug,
			status,
			"Something went wrong while updating a member.",
			backendErrorDetails("update member", status, errorResponse, updateErr),
			views.DefaultMemberInviteFormData(),
		)
	}

	if isHTMXRequest(c) {
		return renderTempl(
			c,
			views.MemberRoleCell(
				api.Member{UserSlug: userSlug.String(), Role: memberRole},
			),
		)
	}

	return redirectHTMXOrSeeOther(c, organizationMembersPath(orgSlug))
}

func (r *Router) handleRemoveMemberSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	userSlug, err := slug.New(c.Params("userSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid user slug")
	}

	status, errorResponse, removeErr := r.removeMemberViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		userSlug.String(),
	)
	if removeErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		if isHTMXRequest(c) {
			return r.renderMembersContent(
				c,
				orgSlug,
				http.StatusOK,
				views.DefaultMemberInviteFormData(),
				"",
				"Something went wrong while removing a member.",
				backendErrorDetails("remove member", status, errorResponse, removeErr),
				nil,
				nil,
			)
		}

		return r.renderMembersPageWithError(
			c,
			orgSlug,
			status,
			"Something went wrong while removing a member.",
			backendErrorDetails("remove member", status, errorResponse, removeErr),
			views.DefaultMemberInviteFormData(),
		)
	}

	if isHTMXRequest(c) {
		return r.renderMembersContent(
			c,
			orgSlug,
			http.StatusOK,
			views.DefaultMemberInviteFormData(),
			"Member removed successfully.",
			"",
			"",
			nil,
			nil,
		)
	}

	return redirectHTMXOrSeeOther(c, organizationMembersPath(orgSlug))
}

// organizationMembersPath returns the canonical organization settings members route.
func organizationMembersPath(orgSlug slug.Slug) string {
	return "/organizations/" + orgSlug.String() + "/members"
}

func (r *Router) listMembersViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	params *api.ListMembersParams,
) (*api.MemberListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListMembersWithResponse(
		ctx,
		organizationSlug,
		params,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"list members api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) listMemberInvitationsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	params *api.ListMemberInvitationsParams,
) (*api.MemberInvitationListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListMemberInvitationsWithResponse(
		ctx,
		organizationSlug,
		params,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"list member invitations api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) createMemberInvitationViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	payload api.CreateMemberInvitationJSONRequestBody,
) (*api.MemberInvitation, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.CreateMemberInvitationWithResponse(
		ctx,
		organizationSlug,
		payload,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusCreated && resp.JSON201 != nil {
		return resp.JSON201, http.StatusCreated, nil, nil
	}

	if resp.StatusCode() == http.StatusCreated {
		return nil, http.StatusCreated, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"create member invitation api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) resendMemberInvitationViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	invitationSlug string,
) (*api.MemberInvitation, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ResendMemberInvitationWithResponse(
		ctx,
		organizationSlug,
		invitationSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"resend member invitation api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) revokeMemberInvitationViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	invitationSlug string,
) (*api.MemberInvitation, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.RevokeMemberInvitationWithResponse(
		ctx,
		organizationSlug,
		invitationSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"revoke member invitation api returned status %d",
		resp.StatusCode(),
	)
}

func defaultMemberInvitationListParams() *api.ListMemberInvitationsParams {
	params := &api.ListMemberInvitationsParams{}
	limit := api.LimitParam(100)
	sortBy := api.ListMemberInvitationsParamsSortByInvitationCreatedAt
	order := api.ListMemberInvitationsParamsOrderDesc
	status := api.MemberInvitationStatusPending
	params.Limit = &limit
	params.SortBy = &sortBy
	params.Order = &order
	params.Status = &status
	return params
}

func (r *Router) updateMemberViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	userSlug string,
	payload api.UpdateMemberJSONRequestBody,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.UpdateMemberWithResponse(
		ctx,
		organizationSlug,
		userSlug,
		payload,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"update member api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) removeMemberViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	userSlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.RemoveMemberWithResponse(
		ctx,
		organizationSlug,
		userSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"remove member api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) renderMembersPageWithError(
	c fiber.Ctx,
	orgSlug slug.Slug,
	status int,
	summary string,
	details string,
	inviteFormData views.MemberInviteFormData,
) error {
	if status < 400 || status >= 600 {
		status = http.StatusInternalServerError
	}
	setHTMLResponseStatus(c, status)

	params := &api.ListMembersParams{}
	limit := api.LimitParam(20)
	params.Limit = &limit

	memberList, listStatus, errorResponse, listErr := r.listMembersViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		params,
	)
	if listErr != nil {
		if listStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		fallbackDetails := backendErrorDetails(
			"reload members after failed action",
			listStatus,
			errorResponse,
			listErr,
		)
		details = mergeErrorDetails(details, fallbackDetails)

		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Members",
			orgSlug.String(),
			summary,
			details,
			c.Get(fiber.HeaderCookie),
		)
		permissions := r.resolveOrganizationPermissions(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
		)

		return renderTempl(
			c,
			views.MembersPage(
				shellData,
				[]api.Member{},
				[]api.MemberInvitation{},
				false,
				"",
				"",
				false,
				"",
				"",
				shellData.ActiveOrganizationSlug,
				inviteFormData,
				views.MemberRoleFormData{UserSlug: c.Params("userSlug"), Role: strings.TrimSpace(c.FormValue("role"))},
				permissions,
				r.memberInvitationsEnabled(),
			),
		)
	}

	invitationList, invitationStatus, invitationErrorResponse, invitationListErr := r.listMemberInvitationsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		defaultMemberInvitationListParams(),
	)
	invitations := []api.MemberInvitation{}
	invitationHasMore := false
	invitationNextCursor := ""
	invitationPrevCursor := ""
	if invitationListErr != nil {
		if invitationStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		details = mergeErrorDetails(
			details,
			backendErrorDetails(
				"reload invitations after failed action",
				invitationStatus,
				invitationErrorResponse,
				invitationListErr,
			),
		)
	} else if invitationList != nil {
		invitations = pendingMemberInvitations(invitationList.Invitations)
		invitationHasMore = invitationList.HasMore
		if invitationList.NextCursor != nil {
			invitationNextCursor = *invitationList.NextCursor
		}
		if invitationList.PrevCursor != nil {
			invitationPrevCursor = *invitationList.PrevCursor
		}
	}

	nextCursor := ""
	prevCursor := ""
	if memberList.NextCursor != nil {
		nextCursor = *memberList.NextCursor
	}
	if memberList.PrevCursor != nil {
		prevCursor = *memberList.PrevCursor
	}

	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Members",
		orgSlug.String(),
		summary,
		details,
		c.Get(fiber.HeaderCookie),
	)
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)

	return renderTempl(
		c,
		views.MembersPage(
			shellData,
			memberList.Members,
			invitations,
			memberList.HasMore,
			nextCursor,
			prevCursor,
			invitationHasMore,
			invitationNextCursor,
			invitationPrevCursor,
			shellData.ActiveOrganizationSlug,
			inviteFormData,
			views.MemberRoleFormData{UserSlug: c.Params("userSlug"), Role: strings.TrimSpace(c.FormValue("role"))},
			permissions,
			r.memberInvitationsEnabled(),
		),
	)
}

func (r *Router) renderMembersContent(
	c fiber.Ctx,
	orgSlug slug.Slug,
	status int,
	inviteFormData views.MemberInviteFormData,
	inviteSuccessMessage string,
	inviteErrorSummary string,
	inviteErrorDetails string,
	optimisticMember *api.Member,
	optimisticInvitation *api.MemberInvitation,
) error {
	if status < 200 || status >= 600 {
		status = http.StatusOK
	}
	setHTMLResponseStatus(c, status)

	params := &api.ListMembersParams{}
	limit := api.LimitParam(20)
	params.Limit = &limit

	memberList, listStatus, errorResponse, listErr := r.listMembersViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		params,
	)
	if listErr != nil {
		if listStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		return r.renderMembersPageWithError(
			c,
			orgSlug,
			listStatus,
			"Something went wrong while loading members.",
			backendErrorDetails("list members", listStatus, errorResponse, listErr),
			inviteFormData,
		)
	}

	invitationList, invitationStatus, invitationErrorResponse, invitationListErr := r.listMemberInvitationsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		defaultMemberInvitationListParams(),
	)
	invitations := []api.MemberInvitation{}
	invitationHasMore := false
	invitationNextCursor := ""
	invitationPrevCursor := ""
	if invitationListErr != nil {
		if invitationStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		return r.renderMembersPageWithError(
			c,
			orgSlug,
			invitationStatus,
			"Something went wrong while loading invitations.",
			backendErrorDetails(
				"list member invitations",
				invitationStatus,
				invitationErrorResponse,
				invitationListErr,
			),
			inviteFormData,
		)
	}
	if invitationList != nil {
		invitations = pendingMemberInvitations(invitationList.Invitations)
		invitationHasMore = invitationList.HasMore
		if invitationList.NextCursor != nil {
			invitationNextCursor = *invitationList.NextCursor
		}
		if invitationList.PrevCursor != nil {
			invitationPrevCursor = *invitationList.PrevCursor
		}
	}

	nextCursor := ""
	prevCursor := ""
	if memberList.NextCursor != nil {
		nextCursor = *memberList.NextCursor
	}
	if memberList.PrevCursor != nil {
		prevCursor = *memberList.PrevCursor
	}

	members := memberList.Members
	if optimisticMember != nil {
		found := false
		for _, existing := range members {
			if existing.UserSlug == optimisticMember.UserSlug {
				found = true
				break
			}
		}

		if !found {
			members = append([]api.Member{*optimisticMember}, members...)
		}
	}
	if optimisticInvitation != nil {
		found := false
		for _, existing := range invitations {
			if existing.Slug == optimisticInvitation.Slug {
				found = true
				break
			}
		}

		if !found && optimisticInvitation.Status == api.MemberInvitationStatusPending {
			invitations = append([]api.MemberInvitation{*optimisticInvitation}, invitations...)
		}
	}

	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)

	return renderTempl(
		c,
		views.MembersPageContent(
			members,
			invitations,
			memberList.HasMore,
			nextCursor,
			prevCursor,
			invitationHasMore,
			invitationNextCursor,
			invitationPrevCursor,
			orgSlug.String(),
			inviteFormData,
			views.MemberRoleFormData{UserSlug: c.Params("userSlug"), Role: strings.TrimSpace(c.FormValue("role"))},
			inviteSuccessMessage,
			inviteErrorSummary,
			inviteErrorDetails,
			permissions,
			r.memberInvitationsEnabled(),
		),
	)
}

func memberInviteFormDataFromRequest(c fiber.Ctx) views.MemberInviteFormData {
	formData := views.DefaultMemberInviteFormData()
	formData.Email = strings.TrimSpace(c.FormValue("email"))
	role := strings.TrimSpace(c.FormValue("role"))
	if role != "" {
		formData.Role = role
	}
	return formData
}

func pendingMemberInvitations(invitations []api.MemberInvitation) []api.MemberInvitation {
	pending := make([]api.MemberInvitation, 0, len(invitations))
	for _, invitation := range invitations {
		if invitation.Status == api.MemberInvitationStatusPending {
			pending = append(pending, invitation)
		}
	}
	return pending
}

func memberInvitationActionParams(c fiber.Ctx) (slug.Slug, slug.Slug, error) {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return "", "", c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	invitationSlug, err := slug.New(c.Params("invitationSlug"))
	if err != nil {
		return "", "", c.Status(fiber.StatusBadRequest).SendString("invalid invitation slug")
	}

	return orgSlug, invitationSlug, nil
}
