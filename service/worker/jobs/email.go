//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=EmailType -output=email_type_string.go -text -json -linecomment
package jobs

import (
	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

const EmailQueue = "email"

type EmailType int

const (
	EmailTypeUnknown       EmailType = iota // unknown
	EmailTypeVerification                   // verification
	EmailTypePasswordReset                  // password_reset
	EmailTypeSessionMFA                     // session_mfa
	EmailTypeInvitation                     // invitation
	EmailTypeNotification                   // notification
	EmailTypeMessage                        // message
)

// EmailRecipient is the minimal durable identity needed to deliver an email.
type EmailRecipient struct {
	Slug     slug.Slug `json:"slug"`
	Email    string    `json:"email"`
	FullName string    `json:"full_name"`
	Locale   string    `json:"locale"`
}

// NewEmailRecipient strips profile-only PII before an email job is persisted.
func NewEmailRecipient(user *model.User) EmailRecipient {
	if user == nil {
		return EmailRecipient{}
	}
	return EmailRecipient{
		Slug:     user.Slug,
		Email:    user.Email,
		FullName: user.FullName,
		Locale:   user.Locale,
	}
}

// ToUser converts the recipient shape back to the email service contract.
func (r EmailRecipient) ToUser() model.User {
	return model.User{
		Slug:     r.Slug,
		Email:    r.Email,
		FullName: r.FullName,
		Locale:   r.Locale,
	}
}

// EmailSendArgs is the durable River payload for sending outbound email.
type EmailSendArgs struct {
	Type                     EmailType                        `json:"type"`
	User                     EmailRecipient                   `json:"user"`
	Invitation               model.MemberInvitationEmail      `json:"invitation,omitempty"`
	NotificationRecipient    model.NotificationEmailRecipient `json:"notification_recipient,omitempty"`
	Notification             model.NotificationEmail          `json:"notification,omitempty"`
	OrganizationSlug         slug.Slug                        `json:"organization_slug,omitempty"`
	AlertDeliveryAttemptSlug slug.Slug                        `json:"alert_delivery_attempt_slug,omitempty"`
	Link                     string                           `json:"link"`
	EncryptedToken           []byte                           `json:"encrypted_token,omitempty"`
	EncryptedCode            []byte                           `json:"encrypted_code,omitempty"`
	Subject                  string                           `json:"subject,omitempty"`
	Body                     string                           `json:"body,omitempty"`
}

func (EmailSendArgs) Kind() string {
	return "email_send"
}

func (EmailSendArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: EmailQueue}
}
