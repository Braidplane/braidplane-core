package i18n

import "github.com/nicksnyder/go-i18n/v2/i18n"

var EmailVerification = i18n.Message{
	ID:          "email_verification",
	Description: "Email verification message sent to users to verify their email address.",
	Other:       "Please verify your email address by clicking the link below: {{.VerificationLink}}",
}

var EmailVerificationSubject = i18n.Message{
	ID:          "email_verification_subject",
	Description: "Subject line for the email verification message.",
	Other:       "Please verify your email address",
}

var PasswordReset = i18n.Message{
	ID:          "password_reset",
	Description: "Password reset message sent to users to reset their password.",
	Other:       "Reset your password by clicking the link below: {{.ResetLink}}",
}

var PasswordResetSubject = i18n.Message{
	ID:          "password_reset_subject",
	Description: "Subject line for the password reset message.",
	Other:       "Reset your password",
}

var MemberInvitation = i18n.Message{
	ID:          "member_invitation",
	Description: "Organization member invitation message sent to invited users.",
	Other:       "{{.InviterName}} ({{.InviterEmail}}) invited you to join {{.OrganizationName}} as {{.Role}}. This invitation expires at {{.ExpiresAt}}. Accept the invitation here: {{.AcceptanceLink}}",
}

var MemberInvitationSubject = i18n.Message{
	ID:          "member_invitation_subject",
	Description: "Subject line for the organization member invitation message.",
	Other:       "You're invited to join {{.OrganizationName}}",
}

var SessionMFA = i18n.Message{
	ID:          "session_mfa",
	Description: "Email MFA code sent to users during sign-in.",
	Other:       "Use this code to finish signing in: {{.Code}}",
}

var SessionMFASubject = i18n.Message{
	ID:          "session_mfa_subject",
	Description: "Subject line for the email MFA code message.",
	Other:       "Your sign-in code",
}

var NotificationDelivery = i18n.Message{
	ID:          "notification_delivery",
	Description: "Generic notification email sent to configured operator recipients.",
	Other:       "{{.OrganizationName}} has a {{.Severity}} {{.NotificationType}} notification. {{.Summary}} Occurrences: {{.OccurrenceCount}}. Source: {{.SourceEntityType}}/{{.SourceAction}}. Notification: {{.RecordSlug}}",
}

var NotificationDeliverySubject = i18n.Message{
	ID:          "notification_delivery_subject",
	Description: "Subject line for a generic notification email.",
	Other:       "[{{.Severity}}] {{.NotificationType}} in {{.OrganizationName}}",
}

var NotificationTypePipelineRunFailedLabel = i18n.Message{
	ID:          "notification_type_pipeline_run_failed_label",
	Description: "Display label for pipeline run failed notifications.",
	Other:       "pipeline run failed",
}

var NotificationTypePipelineRetryExhaustedLabel = i18n.Message{
	ID:          "notification_type_pipeline_retry_exhausted_label",
	Description: "Display label for pipeline retry exhausted notifications.",
	Other:       "pipeline retries exhausted",
}

var NotificationTypePipelineScheduledRunDelayedLabel = i18n.Message{
	ID:          "notification_type_pipeline_scheduled_run_delayed_label",
	Description: "Display label for delayed scheduled pipeline run notifications.",
	Other:       "scheduled pipeline run delayed",
}

var NotificationTypePipelineScheduledRunMissingLabel = i18n.Message{
	ID:          "notification_type_pipeline_scheduled_run_missing_label",
	Description: "Display label for missing scheduled pipeline run notifications.",
	Other:       "scheduled pipeline run missing",
}

var NotificationTypePipelineFreshnessSLABreachedLabel = i18n.Message{
	ID:          "notification_type_pipeline_freshness_sla_breached_label",
	Description: "Display label for pipeline freshness SLA breach notifications.",
	Other:       "pipeline freshness SLA breached",
}

var NotificationTypePipelineContractFailureLabel = i18n.Message{
	ID:          "notification_type_pipeline_contract_failure_label",
	Description: "Display label for pipeline contract failure notifications.",
	Other:       "pipeline contract failure",
}

var NotificationSeverityInfoLabel = i18n.Message{
	ID:          "notification_severity_info_label",
	Description: "Display label for informational notifications.",
	Other:       "info",
}

var NotificationSeverityWarningLabel = i18n.Message{
	ID:          "notification_severity_warning_label",
	Description: "Display label for warning notifications.",
	Other:       "warning",
}

var NotificationSeverityCriticalLabel = i18n.Message{
	ID:          "notification_severity_critical_label",
	Description: "Display label for critical notifications.",
	Other:       "critical",
}

var EventEntityPipelineRunLabel = i18n.Message{
	ID:          "event_entity_pipeline_run_label",
	Description: "Display label for pipeline run event sources.",
	Other:       "pipeline run",
}

var EventActionExecuteLabel = i18n.Message{
	ID:          "event_action_execute_label",
	Description: "Display label for execute event actions.",
	Other:       "execute",
}

var NotificationTypePipelineRunSucceededLabel = i18n.Message{ID: "notification_type_pipeline_run_succeeded_label", Other: "Pipeline run succeeded"}
