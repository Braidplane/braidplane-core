package frontend

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func (r *Router) handleMemberInvitationLandingPage(c fiber.Ctx) error {
	token := strings.TrimSpace(c.Query("token"))
	state := r.newInvitationActionState(token)
	if token == "" {
		state.MissingToken = true
		state.ErrorSummary = "Invitation link is missing a token."
		state.InvalidReason = "Invitation link is missing a token."
		c.Status(fiber.StatusBadRequest)
	}

	return renderTempl(c, views.MemberInvitationActionPage(r.memberInvitationShellData(c, ""), state))
}

func (r *Router) handleAcceptMemberInvitationSubmit(c fiber.Ctx) error {
	token := invitationTokenFromRequest(c)
	if token == "" {
		state := r.invitationActionErrorState(
			token,
			"Invitation link is missing a token.",
			"Validation: invitation token is required.",
		)
		state.MissingToken = true
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(c, views.MemberInvitationActionPage(r.memberInvitationShellData(c, ""), state))
	}

	accepted, status, errorResponse, err := r.acceptMemberInvitationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		api.AcceptMemberInvitationJSONRequestBody{
			Token: token,
		},
	)
	if err != nil {
		if isEmailNotVerifiedResponse(status, errorResponse) {
			state := r.invitationActionErrorState(
				token,
				"Verify your email address before accepting this invitation.",
				backendErrorDetails("accept member invitation", status, errorResponse, err),
			)
			setHTMXAwareErrorStatus(c, status)
			return renderTempl(c, views.MemberInvitationActionPage(r.memberInvitationShellData(c, ""), state))
		}
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, memberInvitationLandingURL(token))
		}

		state := r.invitationActionErrorState(
			token,
			"Something went wrong while accepting the invitation.",
			backendErrorDetails("accept member invitation", status, errorResponse, err),
		)
		state.InvalidReason = unusableInvitationMessage(status)
		setHTMXAwareErrorStatus(c, status)
		return renderTempl(c, views.MemberInvitationActionPage(r.memberInvitationShellData(c, ""), state))
	}

	state := r.newInvitationActionState(token)
	state.Success = "Invitation accepted."
	state.Invitation = &accepted.Invitation
	state.Member = &accepted.Member
	return renderTempl(c, views.MemberInvitationActionPage(r.memberInvitationShellData(c, ""), state))
}

func (r *Router) handleDeclineMemberInvitationSubmit(c fiber.Ctx) error {
	token := invitationTokenFromRequest(c)
	if token == "" {
		state := r.invitationActionErrorState(
			token,
			"Invitation link is missing a token.",
			"Validation: invitation token is required.",
		)
		state.MissingToken = true
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(c, views.MemberInvitationActionPage(r.memberInvitationShellData(c, ""), state))
	}

	invitation, status, errorResponse, err := r.declineMemberInvitationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		api.DeclineMemberInvitationJSONRequestBody{Token: token},
	)
	if err != nil {
		if isEmailNotVerifiedResponse(status, errorResponse) {
			state := r.invitationActionErrorState(
				token,
				"Verify your email address before declining this invitation.",
				backendErrorDetails("decline member invitation", status, errorResponse, err),
			)
			setHTMXAwareErrorStatus(c, status)
			return renderTempl(c, views.MemberInvitationActionPage(r.memberInvitationShellData(c, ""), state))
		}
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, memberInvitationLandingURL(token))
		}

		state := r.invitationActionErrorState(
			token,
			"Something went wrong while declining the invitation.",
			backendErrorDetails("decline member invitation", status, errorResponse, err),
		)
		state.InvalidReason = unusableInvitationMessage(status)
		setHTMXAwareErrorStatus(c, status)
		return renderTempl(c, views.MemberInvitationActionPage(r.memberInvitationShellData(c, ""), state))
	}

	state := r.newInvitationActionState(token)
	state.Success = "Invitation declined."
	state.Invitation = invitation
	state.Declined = true
	return renderTempl(c, views.MemberInvitationActionPage(r.memberInvitationShellData(c, ""), state))
}

// invitationTokenFromRequest accepts token values from form bodies or redirect-safe URLs.
func invitationTokenFromRequest(c fiber.Ctx) string {
	token := strings.TrimSpace(c.FormValue("token"))
	if token != "" {
		return token
	}
	return strings.TrimSpace(c.Query("token"))
}

// memberInvitationLandingURL builds the GET landing route used after login redirects.
func memberInvitationLandingURL(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return "/member-invitations/accept"
	}
	return "/member-invitations/accept?token=" + url.QueryEscape(token)
}

func (r *Router) listMyMemberInvitationsViaAPI(
	ctx context.Context,
	cookieHeader string,
	params *api.ListMyMemberInvitationsParams,
) (*api.MemberInvitationListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListMyMemberInvitationsWithResponse(
		ctx,
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
		"list my member invitations api returned status %d",
		resp.StatusCode(),
	)
}

func defaultMyMemberInvitationListParams() *api.ListMyMemberInvitationsParams {
	params := &api.ListMyMemberInvitationsParams{}
	limit := api.LimitParam(100)
	sortBy := api.ListMyMemberInvitationsParamsSortByInvitationCreatedAt
	order := api.ListMyMemberInvitationsParamsOrderDesc
	params.Limit = &limit
	params.SortBy = &sortBy
	params.Order = &order
	return params
}

func (r *Router) acceptMemberInvitationViaAPI(
	ctx context.Context,
	cookieHeader string,
	payload api.AcceptMemberInvitationJSONRequestBody,
) (*api.MemberInvitationAcceptance, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.AcceptMemberInvitationWithResponse(
		ctx,
		payload,
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
		"accept member invitation api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) acceptMyMemberInvitationViaAPI(
	ctx context.Context,
	cookieHeader string,
	orgSlug string,
	invitationSlug string,
) (*api.MemberInvitationAcceptance, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.AcceptMyMemberInvitationWithResponse(
		ctx,
		orgSlug,
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
		"accept my member invitation api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) declineMemberInvitationViaAPI(
	ctx context.Context,
	cookieHeader string,
	payload api.DeclineMemberInvitationJSONRequestBody,
) (*api.MemberInvitation, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.DeclineMemberInvitationWithResponse(
		ctx,
		payload,
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
		"decline member invitation api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) declineMyMemberInvitationViaAPI(
	ctx context.Context,
	cookieHeader string,
	orgSlug string,
	invitationSlug string,
) (*api.MemberInvitation, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.DeclineMyMemberInvitationWithResponse(
		ctx,
		orgSlug,
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
		"decline my member invitation api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) memberInvitationShellData(c fiber.Ctx, pageError string) views.AuthenticatedShellData {
	params := &api.ListOrganizationsParams{}
	limit := api.LimitParam(100)
	params.Limit = &limit
	orgList, _, _, err := r.listOrganizationsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		params,
	)
	organizations := []api.OrganizationWithRole{}
	if err == nil && orgList != nil {
		organizations = orgList.Organizations
	}
	return r.buildShellDataForDashboard(c, "Invitation", organizations, pageError, "")
}

func (r *Router) newInvitationActionState(token string) views.MemberInvitationActionState {
	return views.MemberInvitationActionState{
		Token: token,
	}
}

func (r *Router) invitationActionErrorState(token, summary, details string) views.MemberInvitationActionState {
	state := r.newInvitationActionState(token)
	state.ErrorSummary = summary
	state.ErrorDetails = details
	state.InvalidReason = summary
	return state
}

func unusableInvitationMessage(status int) string {
	if status == http.StatusNotFound {
		return "This invitation link is expired, revoked, declined, already accepted, or not available for this account."
	}
	return "The invitation could not be processed."
}
