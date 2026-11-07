package views

import (
	"net/url"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

// MemberInvitationActionState stores the authenticated invitation landing outcome.
type MemberInvitationActionState struct {
	Token         string
	Success       string
	ErrorSummary  string
	ErrorDetails  string
	Invitation    *api.MemberInvitation
	Member        *api.Member
	Declined      bool
	MissingToken  bool
	InvalidReason string
}

// MemberInvitationActionURL keeps the token in redirectable URLs for invitation forms.
func MemberInvitationActionURL(action, token string) string {
	path := "/member-invitations/" + action
	token = strings.TrimSpace(token)
	if token == "" {
		return path
	}

	return path + "?token=" + url.QueryEscape(token)
}
