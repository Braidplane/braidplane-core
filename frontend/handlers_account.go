package frontend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	emailNotVerifiedErrorCode = "EMAIL_NOT_VERIFIED"
	//nolint:gosec // API error code, not a credential or secret value.
	invalidCredentialsErrorCode = "INVALID_CREDENTIALS"
)

const (
	accountPATCreateError         = "Personal access token could not be created."
	accountPATUpdateError         = "Personal access token could not be updated."
	validationNameRequiredMessage = "Validation: name is required."
	accountPATDatetimeLocalLayout = "2006-01-02T15:04"
	accountPasswordUpdateError    = "Password could not be updated."
)

func (r *Router) handleAccountPage(c fiber.Ctx) error {
	state, status, errorResponse, err := r.loadAccountPageState(c)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = fmt.Sprintf("Failed to load account settings (status %d).", status)
		}
		state.ProfileError = message
		state.ProfileErrorDetails = backendErrorDetails("load account profile", status, errorResponse, err)
		c.Status(status)
	}

	return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
}

func (r *Router) handleAccountProfileSubmit(c fiber.Ctx) error {
	form := accountProfileFormFromRequest(c)
	body := api.UpdateUserProfile{
		FullName:    apiNamePointer(form.FullName),
		Locale:      optionalStringPointer(form.Locale),
		AvatarUrl:   optionalStringPointer(form.AvatarURL),
		PhoneNumber: optionalStringPointer(form.PhoneNumber),
		JobTitle:    optionalStringPointer(form.JobTitle),
		Timezone:    optionalStringPointer(form.Timezone),
	}

	updated, status, errorResponse, err := r.updateProfileViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		body,
	)
	state := views.AccountPageState{
		User:        updated,
		ActiveTab:   views.AccountTabProfile,
		ProfileForm: form,
	}
	if err != nil {
		if loadedState, _, _, loadErr := r.loadAccountPageState(c); loadErr == nil {
			state = loadedState
			form.MFAEmailEnabled = loadedState.ProfileForm.MFAEmailEnabled
			state.ProfileForm = form
		}
		state.ProfileError = messageFromErrorResponse(errorResponse)
		if state.ProfileError == "" {
			state.ProfileError = fmt.Sprintf("Failed to update profile (status %d).", status)
		}
		state.ProfileErrorDetails = backendErrorDetails("update account profile", status, errorResponse, err)
		setHTMXAwareErrorStatus(c, status)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	state.User = updated
	state.ProfileForm = views.AccountProfileFormDataFromUser(updated)
	state.ProfileSuccess = "Profile saved."
	r.hydrateAccountInvitations(c, &state)
	r.hydrateAccountPATs(c, &state)
	return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
}

func (r *Router) handleAccountSecuritySubmit(c fiber.Ctx) error {
	enabled := c.FormValue("mfa_email_enabled") == booleanTrueString
	body := api.UpdateUserProfile{MfaEmailEnabled: &enabled}

	updated, status, errorResponse, err := r.updateProfileViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		body,
	)
	state := views.AccountPageState{User: updated, ActiveTab: views.AccountTabSecurity}
	if updated != nil {
		state.ProfileForm = views.AccountProfileFormDataFromUser(updated)
	}
	if err != nil {
		loadedState, _, _, loadErr := r.loadAccountPageState(c)
		if loadErr == nil {
			state = loadedState
		}
		state.ActiveTab = views.AccountTabSecurity
		state.SecurityError = messageFromErrorResponse(errorResponse)
		if state.SecurityError == "" {
			state.SecurityError = fmt.Sprintf("Failed to update security settings (status %d).", status)
		}
		state.SecurityDetails = backendErrorDetails("update account security", status, errorResponse, err)
		setHTMXAwareErrorStatus(c, status)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	state.SecuritySuccess = "Security settings saved."
	r.hydrateAccountInvitations(c, &state)
	r.hydrateAccountPATs(c, &state)
	return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
}

// handleAccountPasswordSubmit changes the session user's local password after browser-side validation.
func (r *Router) handleAccountPasswordSubmit(c fiber.Ctx) error {
	form := accountPasswordFormFromRequest(c)
	state, status, errorResponse, loadErr := r.loadAccountPasswordPageState(c)
	if loadErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, views.AccountTabHref(views.AccountTabSecurity))
		}
		state.ActiveTab = views.AccountTabSecurity
		state.SecurityError = accountPasswordUpdateError
		state.SecurityDetails = backendErrorDetails("load account security", status, errorResponse, loadErr)
		setHTMXAwareErrorStatus(c, status)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}
	state.ActiveTab = views.AccountTabSecurity

	if state.User != nil && !views.AccountCanManagePassword(state.User) {
		state.SecurityError = accountPasswordUpdateError
		state.SecurityDetails = "Local password sign-in is not enabled for this account."
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}
	if message := validateAccountPasswordForm(form); message != "" {
		state.SecurityError = accountPasswordUpdateError
		state.SecurityDetails = message
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	status, errorResponse, err := r.updateUserPasswordViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		api.UpdateUserPasswordJSONRequestBody{
			CurrentPassword: form.CurrentPassword,
			NewPassword:     form.NewPassword,
		},
	)
	if err != nil {
		if status == fiber.StatusUnauthorized && !isInvalidCredentialsResponse(errorResponse) {
			return redirectToLogin(c, views.AccountTabHref(views.AccountTabSecurity))
		}

		state.SecurityError = accountPasswordUpdateError
		state.SecurityDetails = accountPasswordErrorDetails(status, errorResponse, err)
		setHTMXAwareErrorStatus(c, status)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	return r.handleLogout(c)
}

// handleCreateAccountPATSubmit creates a session user's PAT and renders the one-time raw token.
func (r *Router) handleCreateAccountPATSubmit(c fiber.Ctx) error {
	form := accountPATCreateFormDataFromRequest(c)

	state, _, _, loadErr := r.loadAccountPageState(c)
	if loadErr != nil {
		state = views.AccountPageState{}
	}
	state.ActiveTab = views.AccountTabSecurity
	state.PATCreateForm = form

	customSlug, slugError := optionalAPISlug(form.Slug, "token")
	if slugError != "" {
		state.SecurityError = accountPATCreateError
		state.SecurityDetails = slugError
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}
	if form.Name == "" {
		state.SecurityError = accountPATCreateError
		state.SecurityDetails = validationNameRequiredMessage
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	expiresAt, err := parseAccountPATExpiresAt(form.ExpiresAt)
	if err != nil {
		state.SecurityError = accountPATCreateError
		state.SecurityDetails = "Validation: expiration must be a valid date and time."
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	var description *string
	if form.Description != "" {
		description = &form.Description
	}

	created, status, errorResponse, createErr := r.createPersonalAccessTokenViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		api.CreatePersonalAccessTokenJSONRequestBody{
			Slug:                          customSlug,
			Name:                          form.Name,
			Description:                   description,
			ExpiresAt:                     expiresAt.value,
			CanManagePersonalAccessTokens: &form.CanManagePersonalAccessTokens,
		},
	)
	if createErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		state.SecurityError = accountPATCreateError
		state.SecurityDetails = backendErrorDetails("create personal access token", status, errorResponse, createErr)
		setHTMXAwareErrorStatus(c, status)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	state.PATCreateForm = views.AccountPATCreateFormData{}
	state.PATTokenNotice = &views.AccountPATTokenNotice{
		Action: "Created",
		Name:   created.Name,
		Slug:   created.Slug,
		Token:  created.Token,
	}
	state.SecuritySuccess = "Personal access token created."
	r.hydrateAccountPATs(c, &state)
	return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
}

// handleUpdateAccountPATSubmit updates session-owned PAT metadata from the account security tab.
func (r *Router) handleUpdateAccountPATSubmit(c fiber.Ctx) error {
	patSlug, err := accountPATSlugFromRequest(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	name := strings.TrimSpace(c.FormValue("name"))
	state, _, _, loadErr := r.loadAccountPageState(c)
	if loadErr != nil {
		state = views.AccountPageState{}
	}
	state.ActiveTab = views.AccountTabSecurity

	if name == "" {
		state.SecurityError = accountPATUpdateError
		state.SecurityDetails = validationNameRequiredMessage
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	expiresAt, err := parseAccountPATExpiresAt(c.FormValue("expires_at"))
	if err != nil {
		state.SecurityError = accountPATUpdateError
		state.SecurityDetails = "Validation: expiration must be a valid date and time."
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	descriptionValue := strings.TrimSpace(c.FormValue("description"))
	var description *string
	if descriptionValue != "" {
		description = &descriptionValue
	}

	_, status, errorResponse, updateErr := r.updatePersonalAccessTokenViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		patSlug.String(),
		api.UpdatePersonalAccessTokenJSONRequestBody{
			Name:                          name,
			Description:                   description,
			ExpiresAt:                     expiresAt.value,
			CanManagePersonalAccessTokens: personalAccessTokenManagementCapabilityFromRequest(c),
		},
	)
	if updateErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		state.SecurityError = accountPATUpdateError
		state.SecurityDetails = backendErrorDetails("update personal access token", status, errorResponse, updateErr)
		setHTMXAwareErrorStatus(c, status)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	state.SecuritySuccess = "Personal access token updated."
	r.hydrateAccountPATs(c, &state)
	return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
}

// handleRotateAccountPATSubmit rotates a session user's PAT and renders the one-time replacement token.
func (r *Router) handleRotateAccountPATSubmit(c fiber.Ctx) error {
	patSlug, err := accountPATSlugFromRequest(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	state, _, _, loadErr := r.loadAccountPageState(c)
	if loadErr != nil {
		state = views.AccountPageState{}
	}
	state.ActiveTab = views.AccountTabSecurity

	rotated, status, errorResponse, rotateErr := r.rotatePersonalAccessTokenViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		patSlug.String(),
	)
	if rotateErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		state.SecurityError = "Personal access token could not be rotated."
		state.SecurityDetails = backendErrorDetails("rotate personal access token", status, errorResponse, rotateErr)
		setHTMXAwareErrorStatus(c, status)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	state.PATTokenNotice = &views.AccountPATTokenNotice{
		Action: "Rotated",
		Name:   rotated.Name,
		Slug:   rotated.Slug,
		Token:  rotated.Token,
	}
	state.SecuritySuccess = "Personal access token rotated."
	r.hydrateAccountPATs(c, &state)
	return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
}

// handleRevokeAccountPATSubmit revokes a session user's PAT and refreshes account security state.
func (r *Router) handleRevokeAccountPATSubmit(c fiber.Ctx) error {
	patSlug, err := accountPATSlugFromRequest(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	status, errorResponse, deleteErr := r.deletePersonalAccessTokenViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		patSlug.String(),
	)
	state, _, _, loadErr := r.loadAccountPageState(c)
	if loadErr != nil {
		state = views.AccountPageState{}
	}
	state.ActiveTab = views.AccountTabSecurity
	if deleteErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		state.SecurityError = "Personal access token could not be revoked."
		state.SecurityDetails = backendErrorDetails("revoke personal access token", status, errorResponse, deleteErr)
		setHTMXAwareErrorStatus(c, status)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	state.SecuritySuccess = "Personal access token revoked."
	return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
}

// handleDeleteAccountPATSubmit deletes revoked PAT metadata and refreshes account security state.
func (r *Router) handleDeleteAccountPATSubmit(c fiber.Ctx) error {
	patSlug, err := accountPATSlugFromRequest(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	status, errorResponse, deleteErr := r.deleteRevokedPersonalAccessTokenViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		patSlug.String(),
	)
	state, _, _, loadErr := r.loadAccountPageState(c)
	if loadErr != nil {
		state = views.AccountPageState{}
	}
	state.ActiveTab = views.AccountTabSecurity
	if deleteErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		state.SecurityError = "Personal access token could not be removed."
		state.SecurityDetails = backendErrorDetails("delete revoked personal access token", status, errorResponse, deleteErr)
		setHTMXAwareErrorStatus(c, status)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	state.SecuritySuccess = "Personal access token removed."
	return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
}

func (r *Router) handleAcceptAccountMemberInvitationSubmit(c fiber.Ctx) error {
	orgSlug, invitationSlug, err := memberInvitationActionParams(c)
	if err != nil {
		return err
	}

	accepted, status, errorResponse, acceptErr := r.acceptMyMemberInvitationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		invitationSlug.String(),
	)
	if acceptErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		state, _, _, loadErr := r.loadAccountPageState(c)
		if loadErr != nil {
			state = views.AccountPageState{}
		}
		state.ActiveTab = views.AccountTabInvitations
		state.InvitationsError = "Something went wrong while accepting the invitation."
		state.InvitationsDetails = backendErrorDetails("accept my member invitation", status, errorResponse, acceptErr)
		setHTMXAwareErrorStatus(c, status)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	redirectSlug := orgSlug.String()
	if accepted != nil {
		redirectSlug = accepted.Invitation.OrganizationSlug
	}
	return redirectHTMXOrSeeOther(c, "/organizations/"+redirectSlug)
}

func (r *Router) handleDeclineAccountMemberInvitationSubmit(c fiber.Ctx) error {
	orgSlug, invitationSlug, err := memberInvitationActionParams(c)
	if err != nil {
		return err
	}

	declined, status, errorResponse, declineErr := r.declineMyMemberInvitationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		invitationSlug.String(),
	)
	state, _, _, loadErr := r.loadAccountPageState(c)
	if loadErr != nil {
		state = views.AccountPageState{}
	}
	state.ActiveTab = views.AccountTabInvitations
	if declineErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		state.InvitationsError = "Something went wrong while declining the invitation."
		state.InvitationsDetails = backendErrorDetails("decline my member invitation", status, errorResponse, declineErr)
		setHTMXAwareErrorStatus(c, status)
		return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
	}

	state.InvitationsSuccess = "Invitation declined."
	if declined != nil && declined.OrganizationSlug != "" {
		state.InvitationsSuccess = fmt.Sprintf("Invitation to %s declined.", declined.OrganizationSlug)
	}
	return renderTempl(c, views.AccountPage(r.accountShellData(c, ""), state))
}

func (r *Router) loadAccountPageState(
	c fiber.Ctx,
) (views.AccountPageState, int, *api.ErrorResponse, error) {
	user, status, errorResponse, err := r.getProfileViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
	)
	state := views.AccountPageState{User: user, ActiveTab: accountTabFromRequest(c)}
	if user != nil {
		state.ProfileForm = views.AccountProfileFormDataFromUser(user)
	}
	if err != nil {
		return state, status, errorResponse, err
	}

	invitationStatus, invitationErrorResponse, invitationErr := r.loadAccountInvitations(c, &state)
	if invitationErr != nil {
		return state, invitationStatus, invitationErrorResponse, invitationErr
	}

	patStatus, patErrorResponse, patErr := r.loadAccountPATs(c, &state)
	if patErr != nil {
		return state, patStatus, patErrorResponse, patErr
	}

	return state, status, errorResponse, nil
}

// loadAccountPasswordPageState fetches only the account fields needed before mutating a local password.
func (r *Router) loadAccountPasswordPageState(
	c fiber.Ctx,
) (views.AccountPageState, int, *api.ErrorResponse, error) {
	user, status, errorResponse, err := r.getProfileViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
	)
	state := views.AccountPageState{User: user, ActiveTab: views.AccountTabSecurity}
	if user != nil {
		state.ProfileForm = views.AccountProfileFormDataFromUser(user)
	}
	if err != nil {
		return state, status, errorResponse, err
	}

	return state, status, errorResponse, nil
}

// hydrateAccountInvitations refreshes invitation state for partial account renders.
func (r *Router) hydrateAccountInvitations(c fiber.Ctx, state *views.AccountPageState) {
	status, errorResponse, err := r.loadAccountInvitations(c, state)
	if err == nil {
		return
	}

	state.InvitationsError = messageFromErrorResponse(errorResponse)
	if state.InvitationsError == "" {
		state.InvitationsError = "Failed to load pending invitations."
	}
	state.InvitationsDetails = backendErrorDetails("list my member invitations", status, errorResponse, err)
}

// hydrateAccountPATs refreshes personal access token state for partial account renders.
func (r *Router) hydrateAccountPATs(c fiber.Ctx, state *views.AccountPageState) {
	status, errorResponse, err := r.loadAccountPATs(c, state)
	if err == nil {
		return
	}

	state.SecurityError = messageFromErrorResponse(errorResponse)
	if state.SecurityError == "" {
		state.SecurityError = "Failed to load personal access tokens."
	}
	state.SecurityDetails = backendErrorDetails("list personal access tokens", status, errorResponse, err)
}

// loadAccountPATs fetches safe personal access token metadata into account page state.
func (r *Router) loadAccountPATs(
	c fiber.Ctx,
	state *views.AccountPageState,
) (int, *api.ErrorResponse, error) {
	page := accountPATPageFromRequest(c)
	tokenList, status, errorResponse, err := r.loadAccountPATPage(c, page)
	if err != nil {
		return applyAccountPATListError(state, status, errorResponse, err)
	}

	if prevPage, ok := accountPATPreviousPageForEmptyResult(page, tokenList, accountPATPageValue(c, "pat_prev_cursor")); ok {
		prevTokenList, prevStatus, prevErrorResponse, prevErr := r.loadAccountPATPage(c, prevPage)
		if prevErr != nil {
			return applyAccountPATListError(state, prevStatus, prevErrorResponse, prevErr)
		}
		page = prevPage
		tokenList = prevTokenList
	}

	applyAccountPATListState(state, page, tokenList)
	return http.StatusOK, nil, nil
}

type accountPATPage struct {
	Cursor    string
	Direction string
}

// accountPATPageFromRequest reads PAT pagination from account links or submitted row actions.
func accountPATPageFromRequest(c fiber.Ctx) accountPATPage {
	direction := accountPATPageValue(c, "pat_direction")
	switch direction {
	case pageDirectionPrev, pageDirectionNext:
	default:
		direction = ""
	}

	return accountPATPage{
		Cursor:    accountPATPageValue(c, "pat_cursor"),
		Direction: direction,
	}
}

// loadAccountPATPage fetches one safe PAT metadata page through the profile API.
func (r *Router) loadAccountPATPage(
	c fiber.Ctx,
	page accountPATPage,
) (*api.PersonalAccessTokenListResponse, int, *api.ErrorResponse, error) {
	params := &api.ListPersonalAccessTokensParams{}
	limit := api.LimitParam(20)
	params.Limit = &limit
	if page.Cursor != "" {
		params.Cursor = &page.Cursor
	}
	switch page.Direction {
	case pageDirectionPrev:
		d := api.ListPersonalAccessTokensParamsDirection(api.PageDirectionParamPrev)
		params.Direction = &d
	case pageDirectionNext:
		d := api.ListPersonalAccessTokensParamsDirection(api.PageDirectionParamNext)
		params.Direction = &d
	}

	return r.listPersonalAccessTokensViaAPI(c.Context(), c.Get(fiber.HeaderCookie), params)
}

// accountPATPreviousPageForEmptyResult selects the previous page after an empty paginated refresh.
func accountPATPreviousPageForEmptyResult(
	page accountPATPage,
	tokenList *api.PersonalAccessTokenListResponse,
	fallbackPrevCursor string,
) (accountPATPage, bool) {
	if page.Cursor == "" || tokenList == nil || len(tokenList.PersonalAccessTokens) > 0 {
		return accountPATPage{}, false
	}
	prevCursor := ""
	if tokenList.PrevCursor != nil {
		prevCursor = strings.TrimSpace(*tokenList.PrevCursor)
	}
	if prevCursor == "" {
		prevCursor = strings.TrimSpace(fallbackPrevCursor)
	}
	if prevCursor == "" {
		return accountPATPage{}, false
	}
	return accountPATPage{Cursor: prevCursor, Direction: pageDirectionPrev}, true
}

// applyAccountPATListState copies a successful PAT page into explicit render state.
func applyAccountPATListState(
	state *views.AccountPageState,
	page accountPATPage,
	tokenList *api.PersonalAccessTokenListResponse,
) {
	state.PersonalAccessTokens = nil
	state.PATsCursor = page.Cursor
	state.PATsDirection = page.Direction
	state.PATsHasMore = false
	state.PATsNextCursor = ""
	state.PATsPrevCursor = ""
	if tokenList == nil {
		return
	}
	state.PersonalAccessTokens = tokenList.PersonalAccessTokens
	state.PATsHasMore = tokenList.HasMore
	if tokenList.NextCursor != nil {
		state.PATsNextCursor = *tokenList.NextCursor
	}
	if tokenList.PrevCursor != nil {
		state.PATsPrevCursor = *tokenList.PrevCursor
	}
}

// applyAccountPATListError maps PAT list load failures into account security render state.
func applyAccountPATListError(
	state *views.AccountPageState,
	status int,
	errorResponse *api.ErrorResponse,
	err error,
) (int, *api.ErrorResponse, error) {
	if status == fiber.StatusUnauthorized {
		return status, errorResponse, err
	}

	state.SecurityError = messageFromErrorResponse(errorResponse)
	if state.SecurityError == "" {
		state.SecurityError = "Failed to load personal access tokens."
	}
	state.SecurityDetails = backendErrorDetails("list personal access tokens", status, errorResponse, err)
	return http.StatusOK, nil, nil
}

// accountPATPageValue reads one PAT pagination value from account links or submitted row actions.
func accountPATPageValue(c fiber.Ctx, name string) string {
	value := strings.TrimSpace(c.Query(name))
	if value != "" {
		return value
	}
	return strings.TrimSpace(c.FormValue(name))
}

// loadAccountInvitations fetches pending invitations into account page state.
func (r *Router) loadAccountInvitations(
	c fiber.Ctx,
	state *views.AccountPageState,
) (int, *api.ErrorResponse, error) {
	invitations, invitationStatus, invitationErrorResponse, invitationErr := r.listMyMemberInvitationsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		defaultMyMemberInvitationListParams(),
	)
	if invitationErr != nil {
		if isEmailNotVerifiedResponse(invitationStatus, invitationErrorResponse) {
			state.InvitationsError = "Verify your email address to view pending invitations."
			state.InvitationsDetails = backendErrorDetails(
				"list my member invitations",
				invitationStatus,
				invitationErrorResponse,
				invitationErr,
			)
			return http.StatusOK, nil, nil
		}

		if invitationStatus == fiber.StatusUnauthorized {
			return invitationStatus, invitationErrorResponse, invitationErr
		}

		state.InvitationsError = messageFromErrorResponse(invitationErrorResponse)
		if state.InvitationsError == "" {
			state.InvitationsError = "Failed to load pending invitations."
		}
		state.InvitationsDetails = backendErrorDetails(
			"list my member invitations",
			invitationStatus,
			invitationErrorResponse,
			invitationErr,
		)
		return http.StatusOK, nil, nil
	}
	if invitations != nil {
		state.PendingInvitations = invitations.Invitations
	}

	return http.StatusOK, nil, nil
}

// isEmailNotVerifiedResponse identifies invitation-list failures that should render inline.
func isEmailNotVerifiedResponse(status int, response *api.ErrorResponse) bool {
	return status == fiber.StatusUnauthorized &&
		response != nil &&
		response.Error.Code == emailNotVerifiedErrorCode
}

func (r *Router) accountShellData(c fiber.Ctx, pageError string) views.AuthenticatedShellData {
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
	shellData := r.buildShellDataForDashboard(c, "Account", organizations, pageError, "")
	shellData = r.withProjectShellData(c, shellData, c.Get(fiber.HeaderCookie), "")
	shellData.Subtitle = "Manage your profile, invitations, sign-in security, and personal access tokens."
	return shellData
}

// accountTabFromRequest maps account tab query values to the rendered account section.
func accountTabFromRequest(c fiber.Ctx) views.AccountTab {
	return views.AccountTabFromString(strings.TrimSpace(c.Query("tab")))
}

func accountProfileFormFromRequest(c fiber.Ctx) views.AccountProfileFormData {
	return views.AccountProfileFormData{
		FullName:    strings.TrimSpace(c.FormValue("full_name")),
		Locale:      strings.TrimSpace(c.FormValue("locale")),
		AvatarURL:   strings.TrimSpace(c.FormValue("avatar_url")),
		PhoneNumber: strings.TrimSpace(c.FormValue("phone_number")),
		JobTitle:    strings.TrimSpace(c.FormValue("job_title")),
		Timezone:    strings.TrimSpace(c.FormValue("timezone")),
	}
}

// accountPATCreateFormDataFromRequest copies personal access token create fields from a browser form.
func accountPATCreateFormDataFromRequest(c fiber.Ctx) views.AccountPATCreateFormData {
	return views.AccountPATCreateFormData{
		Slug:                          strings.TrimSpace(c.FormValue("slug")),
		Name:                          strings.TrimSpace(c.FormValue("name")),
		Description:                   strings.TrimSpace(c.FormValue("description")),
		ExpiresAt:                     strings.TrimSpace(c.FormValue("expires_at")),
		CanManagePersonalAccessTokens: formCheckboxEnabled(c, "can_manage_personal_access_tokens"),
	}
}

// personalAccessTokenManagementCapabilityFromRequest preserves an explicit false value when a checkbox is unchecked.
func personalAccessTokenManagementCapabilityFromRequest(c fiber.Ctx) *bool {
	value := formCheckboxEnabled(c, "can_manage_personal_access_tokens")
	return &value
}

type accountPasswordFormData struct {
	CurrentPassword string
	NewPassword     string
	ConfirmPassword string
}

// accountPasswordFormFromRequest copies password fields without storing them in page state.
func accountPasswordFormFromRequest(c fiber.Ctx) accountPasswordFormData {
	return accountPasswordFormData{
		CurrentPassword: c.FormValue("current_password"),
		NewPassword:     c.FormValue("new_password"),
		ConfirmPassword: c.FormValue("confirm_password"),
	}
}

// validateAccountPasswordForm applies local password checks before forwarding to the API.
func validateAccountPasswordForm(form accountPasswordFormData) string {
	if form.CurrentPassword == "" {
		return "Current password is required."
	}
	if form.NewPassword == "" {
		return "New password is required."
	}
	if len(form.NewPassword) < 8 {
		return "New password must be at least 8 characters."
	}
	if form.ConfirmPassword == "" {
		return "Password confirmation is required."
	}
	if form.NewPassword != form.ConfirmPassword {
		return "New password and confirmation must match."
	}
	return ""
}

// accountPasswordErrorDetails maps password-change backend failures into user-safe form feedback.
func accountPasswordErrorDetails(status int, response *api.ErrorResponse, err error) string {
	if isInvalidCredentialsResponse(response) {
		return "Current password is incorrect."
	}
	details := detailedErrorFromErrorResponse(response)
	if details != "" {
		return details
	}
	message := messageFromErrorResponse(response)
	if message != "" {
		return message
	}
	return backendErrorDetails("update account password", status, response, err)
}

// isInvalidCredentialsResponse identifies current-password verification failures.
func isInvalidCredentialsResponse(response *api.ErrorResponse) bool {
	return response != nil && response.Error.Code == invalidCredentialsErrorCode
}

type accountPATExpirationInput struct {
	value *api.NullableTimestamp
}

// parseAccountPATExpiresAt accepts API-style RFC3339 values and browser datetime-local values.
func parseAccountPATExpiresAt(value string) (accountPATExpirationInput, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return accountPATExpirationInput{}, nil
	}

	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return accountPATExpirationInput{value: &parsed}, nil
	}

	parsed, err := time.ParseInLocation(accountPATDatetimeLocalLayout, value, time.UTC)
	if err != nil {
		return accountPATExpirationInput{}, xerror.ValidationFailed().Detailf("parse personal access token expiration: %w", err)
	}
	return accountPATExpirationInput{value: &parsed}, nil
}

// accountPATSlugFromRequest validates the token slug from account PAT action routes.
func accountPATSlugFromRequest(c fiber.Ctx) (slug.Slug, error) {
	patSlug, err := slug.New(c.Params("patSlug"))
	if err != nil {
		return "", xerror.InvalidSlugFormat().Detail("invalid personal access token slug")
	}

	return patSlug, nil
}

func apiNamePointer(value string) *api.Name {
	name := strings.TrimSpace(value)
	return &name
}

func optionalStringPointer(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func (r *Router) getProfileViaAPI(
	ctx context.Context,
	cookieHeader string,
) (*api.User, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.GetUsersProfileWithResponse(
		ctx,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, err
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, resp.StatusCode(), parseAPIErrorResponse(resp.Body), xerror.Unexpected().Detailf(
			"backend returned status %d",
			resp.StatusCode(),
		)
	}
	return resp.JSON200, resp.StatusCode(), nil, nil
}

func (r *Router) updateProfileViaAPI(
	ctx context.Context,
	cookieHeader string,
	body api.UpdateUserProfile,
) (*api.User, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.PatchUsersProfileWithResponse(
		ctx,
		body,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, err
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, resp.StatusCode(), parseAPIErrorResponse(resp.Body), xerror.Unexpected().Detailf(
			"backend returned status %d",
			resp.StatusCode(),
		)
	}
	return resp.JSON200, resp.StatusCode(), nil, nil
}

// updateUserPasswordViaAPI changes the session user's password through the API client.
func (r *Router) updateUserPasswordViaAPI(
	ctx context.Context,
	cookieHeader string,
	body api.UpdateUserPasswordJSONRequestBody,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.UpdateUserPasswordWithResponse(
		ctx,
		body,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, err
	}
	if resp.StatusCode() != http.StatusNoContent {
		return resp.StatusCode(), parseAPIErrorResponse(resp.Body), xerror.Unexpected().Detailf(
			"backend returned status %d",
			resp.StatusCode(),
		)
	}
	return resp.StatusCode(), nil, nil
}

// listPersonalAccessTokensViaAPI loads safe PAT metadata through the profile API.
func (r *Router) listPersonalAccessTokensViaAPI(
	ctx context.Context,
	cookieHeader string,
	params *api.ListPersonalAccessTokensParams,
) (*api.PersonalAccessTokenListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListPersonalAccessTokensWithResponse(
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

	return nil, resp.StatusCode(), parseAPIErrorResponse(resp.Body), xerror.Unexpected().Detailf(
		"list personal access tokens api returned status %d",
		resp.StatusCode(),
	)
}

// createPersonalAccessTokenViaAPI creates a PAT and returns the one-time raw token from the API response.
func (r *Router) createPersonalAccessTokenViaAPI(
	ctx context.Context,
	cookieHeader string,
	body api.CreatePersonalAccessTokenJSONRequestBody,
) (*api.PersonalAccessTokenWithToken, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.CreatePersonalAccessTokenWithResponse(
		ctx,
		body,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}
	if resp.StatusCode() == http.StatusCreated && resp.JSON201 != nil {
		return resp.JSON201, http.StatusCreated, nil, nil
	}

	return nil, resp.StatusCode(), parseAPIErrorResponse(resp.Body), xerror.Unexpected().Detailf(
		"create personal access token api returned status %d",
		resp.StatusCode(),
	)
}

// updatePersonalAccessTokenViaAPI updates safe PAT metadata through the profile API.
func (r *Router) updatePersonalAccessTokenViaAPI(
	ctx context.Context,
	cookieHeader string,
	patSlug string,
	body api.UpdatePersonalAccessTokenJSONRequestBody,
) (*api.PersonalAccessToken, int, *api.ErrorResponse, error) {
	payload, err := json.Marshal(map[string]any{
		"name":                              body.Name,
		"description":                       body.Description,
		"expires_at":                        body.ExpiresAt,
		"can_manage_personal_access_tokens": body.CanManagePersonalAccessTokens,
	})
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	resp, err := r.apiClient.UpdatePersonalAccessTokenWithBodyWithResponse(
		ctx,
		patSlug,
		"application/json",
		bytes.NewReader(payload),
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}
	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	return nil, resp.StatusCode(), parseAPIErrorResponse(resp.Body), xerror.Unexpected().Detailf(
		"update personal access token api returned status %d",
		resp.StatusCode(),
	)
}

// rotatePersonalAccessTokenViaAPI rotates a PAT and returns the one-time raw replacement token.
func (r *Router) rotatePersonalAccessTokenViaAPI(
	ctx context.Context,
	cookieHeader string,
	patSlug string,
) (*api.PersonalAccessTokenWithToken, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.RotatePersonalAccessTokenWithResponse(
		ctx,
		patSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}
	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	return nil, resp.StatusCode(), parseAPIErrorResponse(resp.Body), xerror.Unexpected().Detailf(
		"rotate personal access token api returned status %d",
		resp.StatusCode(),
	)
}

// deletePersonalAccessTokenViaAPI revokes a PAT through the profile API.
func (r *Router) deletePersonalAccessTokenViaAPI(
	ctx context.Context,
	cookieHeader string,
	patSlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.DeletePersonalAccessTokenWithResponse(
		ctx,
		patSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}
	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	return resp.StatusCode(), parseAPIErrorResponse(resp.Body), xerror.Unexpected().Detailf(
		"delete personal access token api returned status %d",
		resp.StatusCode(),
	)
}

// deleteRevokedPersonalAccessTokenViaAPI calls the backend hard-delete operation for revoked PAT metadata.
func (r *Router) deleteRevokedPersonalAccessTokenViaAPI(
	ctx context.Context,
	cookieHeader string,
	patSlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.DeleteRevokedPersonalAccessTokenWithResponse(
		ctx,
		patSlug,
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
		"delete revoked personal access token api returned status %d",
		resp.StatusCode(),
	)
}
