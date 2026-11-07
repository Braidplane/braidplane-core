package notificationsvc

import (
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CreateAlertDestinationParams struct {
	OrgSlug         slug.Slug `validate:"required"`
	Slug            slug.Slug
	Name            string                     `validate:"required"`
	Type            model.AlertDestinationType `validate:"alertdestinationtype"`
	NonSecretConfig map[string]any
}

type UpdateAlertDestinationParams struct {
	OrgSlug         slug.Slug                  `validate:"required"`
	Slug            slug.Slug                  `validate:"required"`
	Name            string                     `validate:"required"`
	Type            model.AlertDestinationType `validate:"alertdestinationtype"`
	NonSecretConfig map[string]any
	Enabled         bool
}

type TestAlertDestinationParams struct {
	OrgSlug         slug.Slug `validate:"required"`
	DestinationSlug slug.Slug `validate:"required"`
}

type AlertDestinationTestJob struct {
	Kind  string
	Queue string
	State string
}

type AlertDestinationTestResult struct {
	DestinationSlug      slug.Slug
	DestinationType      model.AlertDestinationType
	RecipientCount       int
	DiagnosticsPersisted bool
	EnqueuedJobs         []AlertDestinationTestJob
}

type CreateAlertPolicyParams struct {
	OrgSlug         slug.Slug `validate:"required"`
	Slug            slug.Slug
	Name            string                 `validate:"required"`
	Scope           model.AlertPolicyScope `validate:"alertpolicyscope"`
	ProjectSlug     *slug.Slug
	EnvironmentSlug *slug.Slug
	// NotificationType and Severity are optional individually; policy validation requires at least one.
	NotificationType *model.NotificationType           `validate:"omitempty,notificationtype"`
	Severity         *model.NotificationSeverity       `validate:"omitempty,notificationseverity"`
	LifecycleState   *model.NotificationLifecycleState `validate:"omitempty,notificationlifecyclestate"`
	SourceKind       *model.EventEntityType            `validate:"omitempty,evententitytype"`
	SourceAction     *model.EventAction                `validate:"omitempty,eventaction"`
	SourceRef        *slug.Slug
	DestinationSlugs []slug.Slug
}

type UpdateAlertPolicyParams struct {
	OrgSlug         slug.Slug              `validate:"required"`
	Slug            slug.Slug              `validate:"required"`
	Name            string                 `validate:"required"`
	Scope           model.AlertPolicyScope `validate:"alertpolicyscope"`
	ProjectSlug     *slug.Slug
	EnvironmentSlug *slug.Slug
	// NotificationType and Severity are optional individually; policy validation requires at least one.
	NotificationType *model.NotificationType           `validate:"omitempty,notificationtype"`
	Severity         *model.NotificationSeverity       `validate:"omitempty,notificationseverity"`
	LifecycleState   *model.NotificationLifecycleState `validate:"omitempty,notificationlifecyclestate"`
	SourceKind       *model.EventEntityType            `validate:"omitempty,evententitytype"`
	SourceAction     *model.EventAction                `validate:"omitempty,eventaction"`
	SourceRef        *slug.Slug
	DestinationSlugs []slug.Slug
	Enabled          bool
}

// AlertManagementPrincipal identifies the authenticated actor for user-facing alert management.
type AlertManagementPrincipal struct {
	UserSlug       slug.Slug
	APIKeySlug     slug.Slug
	APIKeyOrgSlug  slug.Slug
	APIKeyRoleSlug slug.Slug
}

// EventActor returns the durable-event actor represented by the alert
// management principal.
func (p AlertManagementPrincipal) EventActor() model.EventActor {
	if p.UserSlug != "" {
		return model.UserEventActor(p.UserSlug)
	}
	if p.APIKeySlug != "" {
		return model.APIKeyEventActor(p.APIKeySlug)
	}
	return model.SystemEventActor()
}

type CreateCandidateParams = model.NotificationCandidate

type CreateCandidateResult struct {
	Record *model.NotificationRecord
}

type UpsertRecordParams struct {
	OrgSlug           slug.Slug `validate:"required"`
	Slug              slug.Slug `validate:"required"`
	ProjectSlug       *slug.Slug
	EnvironmentSlug   *slug.Slug
	NotificationType  model.NotificationType     `validate:"notificationtype"`
	Severity          model.NotificationSeverity `validate:"notificationseverity"`
	DedupeFingerprint string                     `validate:"required"`
	SourceEventSlug   *slug.Slug
	SourceEntityType  model.EventEntityType `validate:"evententitytype"`
	SourceAction      model.EventAction     `validate:"eventaction"`
	SourceRefs        map[string]string
	SafeMetadata      map[string]any
}

type NotificationDeliveryJobParams struct {
	Test            bool
	OrgSlug         slug.Slug `validate:"required"`
	RecordSlug      slug.Slug `validate:"required"`
	DestinationSlug slug.Slug `validate:"required"`
	AlertPolicySlug slug.Slug `validate:"required"`
}

type DeliverNotificationParams struct {
	Test            bool
	OrgSlug         slug.Slug `validate:"required"`
	RecordSlug      slug.Slug `validate:"required"`
	DestinationSlug slug.Slug `validate:"required"`
	AlertPolicySlug slug.Slug `validate:"required"`
}

type MarkAlertDeliveryAttemptSentParams struct {
	OrgSlug                  slug.Slug `validate:"required"`
	AttemptSlug              slug.Slug `validate:"required"`
	ProviderAttempt          int       `validate:"required,min=1"`
	ProviderResponseMetadata map[string]any
}

type MarkAlertDeliveryAttemptFailedParams struct {
	OrgSlug                  slug.Slug `validate:"required"`
	AttemptSlug              slug.Slug `validate:"required"`
	ProviderAttempt          int       `validate:"required,min=1"`
	ProviderResponseMetadata map[string]any
	ErrorCategory            string
	ErrorMessage             string
}

type ListNotificationsFilters struct {
	LifecycleState   *model.NotificationLifecycleState
	NotificationType *model.NotificationType
	Severity         *model.NotificationSeverity
	SourceKind       *model.EventEntityType
	SourceAction     *model.EventAction
	SourceRef        *slug.Slug
	IncludeResolved  bool
}
