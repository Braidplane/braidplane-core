package views

import (
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

type AccountPageState struct {
	User                 *api.User
	ActiveTab            AccountTab
	ProfileForm          AccountProfileFormData
	PATCreateForm        AccountPATCreateFormData
	PATTokenNotice       *AccountPATTokenNotice
	PersonalAccessTokens []api.PersonalAccessToken
	PATsCursor           string
	PATsDirection        string
	PATsHasMore          bool
	PATsNextCursor       string
	PATsPrevCursor       string
	ProfileError         string
	ProfileErrorDetails  string
	ProfileSuccess       string
	SecurityError        string
	SecurityDetails      string
	SecuritySuccess      string
	PendingInvitations   []api.MemberInvitation
	InvitationsSuccess   string
	InvitationsError     string
	InvitationsDetails   string
}

type AccountTab string

const (
	AccountTabProfile     AccountTab = "profile"
	AccountTabInvitations AccountTab = "invitations"
	AccountTabSecurity    AccountTab = "security"

	ariaStateFalse = "false"
	ariaStateTrue  = "true"
)

type AccountProfileFormData struct {
	FullName        string
	Locale          string
	AvatarURL       string
	PhoneNumber     string
	JobTitle        string
	Timezone        string
	MFAEmailEnabled bool
}

// AccountTabFromString normalizes account tab query values to a supported page tab.
func AccountTabFromString(value string) AccountTab {
	switch AccountTab(value) {
	case AccountTabInvitations:
		return AccountTabInvitations
	case AccountTabSecurity:
		return AccountTabSecurity
	default:
		return AccountTabProfile
	}
}

// AccountTabClass returns the DaisyUI tab class for the selected account section.
func AccountTabClass(activeTab, tab AccountTab) string {
	if AccountTabFromString(string(activeTab)) == tab {
		return tabActiveClass
	}
	return tabClass
}

// AccountTabID returns the DOM ID used to associate each account tab with its panel.
func AccountTabID(tab AccountTab) string {
	switch tab {
	case AccountTabInvitations:
		return "account-invitations-tab-control"
	case AccountTabSecurity:
		return "account-security-tab-control"
	default:
		return "account-profile-tab-control"
	}
}

// AccountTabPanelID returns the DOM ID for the tabpanel associated with an account tab.
func AccountTabPanelID(tab AccountTab) string {
	switch tab {
	case AccountTabInvitations:
		return "account-invitations-tab"
	case AccountTabSecurity:
		return "account-security-tab"
	default:
		return "account-profile-tab"
	}
}

// AccountTabAriaSelected returns the WAI-ARIA selected state for an account tab.
func AccountTabAriaSelected(activeTab, tab AccountTab) string {
	if AccountTabFromString(string(activeTab)) == tab {
		return ariaStateTrue
	}
	return ariaStateFalse
}

// AccountTabHref returns the stable account page URL for a tab.
func AccountTabHref(tab AccountTab) string {
	switch tab {
	case AccountTabInvitations, AccountTabSecurity:
		return "/account?tab=" + string(tab)
	default:
		return "/account"
	}
}

// AccountTabPanelClass hides inactive account tab content while keeping SSR state available.
func AccountTabPanelClass(activeTab, tab AccountTab) string {
	if AccountTabFromString(string(activeTab)) == tab {
		return "space-y-6"
	}
	return "hidden"
}

type AccountPATCreateFormData struct {
	Slug                          string
	Name                          string
	Description                   string
	ExpiresAt                     string
	CanManagePersonalAccessTokens bool
}

type AccountPATTokenNotice struct {
	Action string
	Name   string
	Slug   string
	Token  string
}

// AccountCanManagePassword reports whether the password card should render an active form.
func AccountCanManagePassword(user *api.User) bool {
	return user != nil && user.HasLocalPassword
}

// AccountProfileFormDataFromUser copies persisted user profile fields into the account form state.
func AccountProfileFormDataFromUser(user *api.User) AccountProfileFormData {
	if user == nil {
		return AccountProfileFormData{}
	}

	form := AccountProfileFormData{
		FullName:        user.FullName,
		Locale:          user.Locale,
		AvatarURL:       optionalAPIString(user.AvatarUrl),
		PhoneNumber:     optionalAPIString(user.PhoneNumber),
		JobTitle:        optionalAPIString(user.JobTitle),
		Timezone:        optionalAPIString(user.Timezone),
		MFAEmailEnabled: user.MfaEmailEnabled,
	}
	return form
}

// AccountPATStatusLabel returns the operator-facing lifecycle label for a personal access token.
func AccountPATStatusLabel(token api.PersonalAccessToken) string {
	if token.RevokedAt != nil {
		return "Revoked"
	}
	if AccountPATExpired(token) {
		return viewStatusExpired
	}
	return viewStatusActive
}

// AccountPATStatusBadgeClass returns a DaisyUI badge treatment for a personal access token state.
func AccountPATStatusBadgeClass(token api.PersonalAccessToken) string {
	if token.RevokedAt != nil {
		return eventBadgeOutline
	}
	if AccountPATExpired(token) {
		return "badge badge-warning"
	}
	return "badge badge-success"
}

// AccountPATExpired reports whether the token can no longer authenticate because its expiry is in the past.
func AccountPATExpired(token api.PersonalAccessToken) bool {
	return token.ExpiresAt != nil && !token.ExpiresAt.After(time.Now())
}

// AccountPATCanRotate reports whether the UI should offer rotation for a token the backend can still rotate.
func AccountPATCanRotate(token api.PersonalAccessToken) bool {
	return token.RevokedAt == nil && !AccountPATExpired(token)
}

// AccountPATCanEdit reports whether editable metadata controls should be shown for a token.
func AccountPATCanEdit(token api.PersonalAccessToken) bool {
	return token.RevokedAt == nil
}

// AccountPATCanRevoke reports whether the UI should offer revocation for a token that is not already revoked.
func AccountPATCanRevoke(token api.PersonalAccessToken) bool {
	return token.RevokedAt == nil
}

// AccountPATCanDelete reports whether the UI should offer permanent metadata removal.
func AccountPATCanDelete(token api.PersonalAccessToken) bool {
	return token.RevokedAt != nil
}

// FormatOptionalAccountPATTime formats nullable token timestamps for dense account tables.
func FormatOptionalAccountPATTime(value *api.NullableTimestamp) string {
	if value == nil {
		return "Never"
	}
	return value.Format("2006-01-02 15:04 MST")
}

// FormatAccountPATDateTimeLocal converts nullable API timestamps into browser datetime-local values.
func FormatAccountPATDateTimeLocal(value *api.NullableTimestamp) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format("2006-01-02T15:04")
}

// AccountPATDescriptionValue returns a stable empty-string fallback for optional descriptions in forms.
func AccountPATDescriptionValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// AccountPATEmptyMessage describes whether the PAT list is globally empty or just the selected page.
func AccountPATEmptyMessage(state AccountPageState) string {
	if state.PATsCursor != "" {
		return "No personal access tokens on this page."
	}
	return "No personal access tokens yet."
}

// AccountPATEditPanelID builds the per-row metadata edit panel identifier.
func AccountPATEditPanelID(token api.PersonalAccessToken) string {
	return "account-pat-" + token.Slug + "-edit"
}

// optionalAPIString returns an empty form value when an API string pointer is absent.
func optionalAPIString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
