package email

import (
	"context"
	"net"
	"strconv"
	"strings"

	"github.com/wneessen/go-mail"

	"github.com/Braidplane/braidplane-core/config"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/i18n"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type Service interface {
	EmailVerification(ctx context.Context, user *model.User, verificationLink string) error
	PasswordReset(ctx context.Context, user *model.User, resetLink string) error
	SessionMFA(ctx context.Context, user *model.User, code string) error
	MemberInvitation(ctx context.Context, invitation model.MemberInvitationEmail, acceptanceLink string) error
	Notification(ctx context.Context, recipient model.NotificationEmailRecipient, notification model.NotificationEmail) error
	Message(ctx context.Context, user *model.User, subject, body string) error
}

type mailClient interface {
	DialAndSendWithContext(ctx context.Context, messages ...*mail.Msg) error
}

type service struct {
	client      mailClient
	bundle      *i18n.Bundle
	noReplyAddr string
}

func NewService(smtpCfg config.SMTP, bundle *i18n.Bundle) (Service, error) {
	if err := smtpCfg.ValidateCredentials(); err != nil {
		return nil, err
	}
	host, port, err := net.SplitHostPort(smtpCfg.Host)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	tlsConfig, err := ConfigTLS(smtpCfg.ToTLSConfig(), host)
	if err != nil {
		return nil, xerror.Wrapf("configure SMTP TLS: %w", err)
	}

	intPort, err := strconv.Atoi(port)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	// Build client options
	opts := []mail.Option{
		mail.WithPort(intPort),
	}
	if tlsConfig != nil {
		opts = append(opts, mail.WithTLSConfig(tlsConfig))
	}

	// Only add auth if credentials are provided
	if smtpCfg.User != "" && smtpCfg.Password != "" {
		opts = append(opts,
			mail.WithSMTPAuth(mail.SMTPAuthPlain),
			mail.WithUsername(smtpCfg.User),
			mail.WithPassword(smtpCfg.Password),
		)
	}

	client, err := mail.NewClient(host, opts...)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &service{
		client:      client,
		bundle:      bundle,
		noReplyAddr: smtpCfg.NoReplyAddress,
	}, nil
}

func (s service) EmailVerification(
	ctx context.Context,
	user *model.User,
	verificationLink string,
) error {
	message := mail.NewMsg()

	err := message.To(user.Email)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	err = message.From(s.noReplyAddr)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	localizer := s.bundle.NewLocalizer(user.Locale)

	message.Subject(localizer.Localize(i18n.EmailVerificationSubject.ID, nil))

	body := localizer.Localize(i18n.EmailVerification.ID, map[string]any{
		"VerificationLink": verificationLink,
	})

	message.SetBodyString(mail.TypeTextPlain, body)

	err = s.client.DialAndSendWithContext(ctx, message)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

func (s service) PasswordReset(
	ctx context.Context,
	user *model.User,
	resetLink string,
) error {
	message := mail.NewMsg()

	err := message.To(user.Email)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	err = message.From(s.noReplyAddr)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	localizer := s.bundle.NewLocalizer(user.Locale)

	message.Subject(localizer.Localize(i18n.PasswordResetSubject.ID, nil))

	body := localizer.Localize(i18n.PasswordReset.ID, map[string]any{
		"ResetLink": resetLink,
	})

	message.SetBodyString(mail.TypeTextPlain, body)

	err = s.client.DialAndSendWithContext(ctx, message)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

func (s service) SessionMFA(ctx context.Context, user *model.User, code string) error {
	message := mail.NewMsg()

	err := message.To(user.Email)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	err = message.From(s.noReplyAddr)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	localizer := s.bundle.NewLocalizer(user.Locale)

	message.Subject(localizer.Localize(i18n.SessionMFASubject.ID, nil))

	body := localizer.Localize(i18n.SessionMFA.ID, map[string]any{
		"Code": code,
	})

	message.SetBodyString(mail.TypeTextPlain, body)

	err = s.client.DialAndSendWithContext(ctx, message)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

// Message sends a plain-text message prepared by the caller.
func (s service) Message(
	ctx context.Context,
	user *model.User,
	subject, body string,
) error {
	message := mail.NewMsg()
	if err := message.To(user.Email); err != nil {
		return xerror.Unexpected().With(err)
	}
	if err := message.From(s.noReplyAddr); err != nil {
		return xerror.Unexpected().With(err)
	}

	message.Subject(subject)
	message.SetBodyString(mail.TypeTextPlain, body)

	if err := s.client.DialAndSendWithContext(ctx, message); err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

// MemberInvitation sends an organization invitation email to an address that may not have a user yet.
func (s service) MemberInvitation(
	ctx context.Context,
	invitation model.MemberInvitationEmail,
	acceptanceLink string,
) error {
	message := mail.NewMsg()

	err := message.To(invitation.Email)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	err = message.From(s.noReplyAddr)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	localizer := s.bundle.NewLocalizer(invitation.Locale)

	message.Subject(localizer.Localize(i18n.MemberInvitationSubject.ID, map[string]any{
		"OrganizationName": invitation.OrganizationName,
	}))

	body := localizer.Localize(i18n.MemberInvitation.ID, map[string]any{
		"OrganizationName": invitation.OrganizationName,
		"InviterName":      invitation.InviterName,
		"InviterEmail":     invitation.InviterEmail,
		"Role":             invitation.Role.String(),
		"ExpiresAt":        invitation.ExpiresAt.Format("2006-01-02 15:04 MST"),
		"AcceptanceLink":   acceptanceLink,
	})

	message.SetBodyString(mail.TypeTextPlain, body)

	err = s.client.DialAndSendWithContext(ctx, message)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

// Notification sends a generic product notification email to a configured recipient.
func (s service) Notification(
	ctx context.Context,
	recipient model.NotificationEmailRecipient,
	notification model.NotificationEmail,
) error {
	message := mail.NewMsg()

	err := message.To(recipient.Email)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	err = message.From(s.noReplyAddr)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	localizer := s.bundle.NewLocalizer(recipient.Locale)
	templateData := notificationEmailTemplateData(localizer, notification)

	message.Subject(localizer.Localize(i18n.NotificationDeliverySubject.ID, templateData))
	message.SetBodyString(mail.TypeTextPlain, localizer.Localize(i18n.NotificationDelivery.ID, templateData))

	err = s.client.DialAndSendWithContext(ctx, message)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

func notificationEmailTemplateData(
	localizer i18n.Localizer,
	notification model.NotificationEmail,
) map[string]any {
	notificationType := localizedNotificationType(localizer, notification.NotificationType)
	if label := strings.TrimSpace(notification.NotificationTypeLabel); label != "" {
		notificationType = label
	}

	return map[string]any{
		"OrganizationName": notification.OrganizationName,
		"RecordSlug":       notification.RecordSlug.String(),
		"NotificationType": notificationType,
		"Severity":         localizedNotificationSeverity(localizer, notification.Severity),
		"Summary":          notification.Summary,
		"SourceEntityType": localizedEventEntityType(localizer, notification.SourceEntityType),
		"SourceAction":     localizedEventAction(localizer, notification.SourceAction),
		"OccurrenceCount":  notification.OccurrenceCount,
	}
}

func localizedNotificationType(localizer i18n.Localizer, value model.NotificationType) string {
	switch value {
	case model.NotificationTypePipelineRunSucceeded:
		return localizedOrEnum(localizer, i18n.NotificationTypePipelineRunSucceededLabel.ID, value.String())
	case model.NotificationTypePipelineRunFailed:
		return localizedOrEnum(localizer, i18n.NotificationTypePipelineRunFailedLabel.ID, value.String())
	case model.NotificationTypePipelineRetryExhausted:
		return localizedOrEnum(localizer, i18n.NotificationTypePipelineRetryExhaustedLabel.ID, value.String())
	case model.NotificationTypePipelineScheduledRunDelayed:
		return localizedOrEnum(localizer, i18n.NotificationTypePipelineScheduledRunDelayedLabel.ID, value.String())
	case model.NotificationTypePipelineScheduledRunMissing:
		return localizedOrEnum(localizer, i18n.NotificationTypePipelineScheduledRunMissingLabel.ID, value.String())
	case model.NotificationTypePipelineFreshnessSLABreached:
		return localizedOrEnum(localizer, i18n.NotificationTypePipelineFreshnessSLABreachedLabel.ID, value.String())
	case model.NotificationTypePipelineContractFailure:
		return localizedOrEnum(localizer, i18n.NotificationTypePipelineContractFailureLabel.ID, value.String())
	default:
		return value.String()
	}
}

func localizedNotificationSeverity(localizer i18n.Localizer, value model.NotificationSeverity) string {
	switch value {
	case model.NotificationSeverityInfo:
		return localizedOrEnum(localizer, i18n.NotificationSeverityInfoLabel.ID, value.String())
	case model.NotificationSeverityWarning:
		return localizedOrEnum(localizer, i18n.NotificationSeverityWarningLabel.ID, value.String())
	case model.NotificationSeverityCritical:
		return localizedOrEnum(localizer, i18n.NotificationSeverityCriticalLabel.ID, value.String())
	default:
		return value.String()
	}
}

func localizedEventEntityType(localizer i18n.Localizer, value model.EventEntityType) string {
	switch value {
	case model.EventEntityTypePipelineRun:
		return localizedOrEnum(localizer, i18n.EventEntityPipelineRunLabel.ID, value.String())
	default:
		return value.String()
	}
}

func localizedEventAction(localizer i18n.Localizer, value model.EventAction) string {
	switch value {
	case model.EventActionExecute:
		return localizedOrEnum(localizer, i18n.EventActionExecuteLabel.ID, value.String())
	default:
		return value.String()
	}
}

func localizedOrEnum(localizer i18n.Localizer, messageID, fallback string) string {
	localized := localizer.Localize(messageID, nil)
	if localized == messageID {
		return fallback
	}
	return localized
}
