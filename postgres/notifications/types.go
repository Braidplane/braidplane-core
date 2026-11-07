package notifications

import (
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CreateAlertDestinationParams struct {
	OrgSlug         slug.Slug
	Slug            slug.Slug
	Name            string
	Type            model.AlertDestinationType
	NonSecretConfig json.RawMessage
	Enabled         bool
}

type UpdateAlertDestinationParams struct {
	OrgSlug         slug.Slug
	Slug            slug.Slug
	Name            string
	Type            model.AlertDestinationType
	NonSecretConfig json.RawMessage
	Enabled         bool
}

type CreateAlertPolicyParams struct {
	OrgSlug          slug.Slug
	Slug             slug.Slug
	Name             string
	Scope            model.AlertPolicyScope
	ProjectSlug      *slug.Slug
	EnvironmentSlug  *slug.Slug
	NotificationType *model.NotificationType
	Severity         *model.NotificationSeverity
	LifecycleState   *model.NotificationLifecycleState
	SourceKind       *model.EventEntityType
	SourceAction     *model.EventAction
	SourceRef        *slug.Slug
	Enabled          bool
}

type UpdateAlertPolicyParams struct {
	OrgSlug          slug.Slug
	Slug             slug.Slug
	Name             string
	Scope            model.AlertPolicyScope
	ProjectSlug      *slug.Slug
	EnvironmentSlug  *slug.Slug
	NotificationType *model.NotificationType
	Severity         *model.NotificationSeverity
	LifecycleState   *model.NotificationLifecycleState
	SourceKind       *model.EventEntityType
	SourceAction     *model.EventAction
	SourceRef        *slug.Slug
	Enabled          bool
}

type BindAlertPolicyDestinationParams struct {
	OrgSlug         slug.Slug
	AlertPolicySlug slug.Slug
	DestinationSlug slug.Slug
}

type UpsertRecordParams struct {
	OrgSlug           slug.Slug
	Slug              slug.Slug
	ProjectSlug       *slug.Slug
	EnvironmentSlug   *slug.Slug
	NotificationType  model.NotificationType
	Severity          model.NotificationSeverity
	LifecycleState    model.NotificationLifecycleState
	DedupeFingerprint string
	SourceEventSlug   *slug.Slug
	SourceEntityType  model.EventEntityType
	SourceAction      model.EventAction
	SourceRefs        json.RawMessage
	SafeMetadata      json.RawMessage
	ObservedAt        time.Time
}

type AcknowledgeRecordParams struct {
	OrgSlug        slug.Slug
	RecordSlug     slug.Slug
	AcknowledgedAt time.Time
}

type ResolveRecordParams struct {
	OrgSlug    slug.Slug
	RecordSlug slug.Slug
	ResolvedAt time.Time
}

type ListRecordFilters struct {
	LifecycleState   *model.NotificationLifecycleState
	NotificationType *model.NotificationType
	Severity         *model.NotificationSeverity
	SourceKind       *model.EventEntityType
	SourceAction     *model.EventAction
	SourceRef        *slug.Slug
	IncludeResolved  bool
}

type FindDeliveryWorkParams struct {
	OrgSlug         slug.Slug
	RecordSlug      slug.Slug
	DestinationSlug slug.Slug
	AlertPolicySlug slug.Slug
}

type CreateAlertDeliveryAttemptParams struct {
	OrgSlug                  slug.Slug
	Slug                     slug.Slug
	NotificationRecordSlug   slug.Slug
	AlertPolicySlug          slug.Slug
	DestinationSlug          slug.Slug
	DestinationType          model.AlertDestinationType
	Status                   model.AlertDeliveryAttemptStatus
	ProviderAttempt          int
	JobKind                  string
	JobQueue                 string
	JobState                 string
	ProviderResponseMetadata json.RawMessage
	QueuedAt                 time.Time
}

type MarkAlertDeliveryAttemptSentParams struct {
	OrgSlug                  slug.Slug
	AttemptSlug              slug.Slug
	ProviderAttempt          int
	JobState                 string
	ProviderResponseMetadata json.RawMessage
	SentAt                   time.Time
}

type MarkAlertDeliveryAttemptFailedParams struct {
	OrgSlug                  slug.Slug
	AttemptSlug              slug.Slug
	ProviderAttempt          int
	JobState                 string
	ProviderResponseMetadata json.RawMessage
	ErrorCategory            *string
	ErrorMessage             *string
	FailedAt                 time.Time
}

type UpdateRecordDispatchSummaryParams struct {
	OrgSlug         slug.Slug
	RecordSlug      slug.Slug
	DestinationSlug slug.Slug
	Status          model.NotificationDispatchStatus
	QueuedAt        time.Time
	JobKind         string
	JobQueue        string
	JobState        string
}

type AlertDestination struct {
	OrgSlug         slug.Slug
	Slug            slug.Slug
	Name            string
	Type            model.AlertDestinationType
	NonSecretConfig json.RawMessage
	Enabled         bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type AlertPolicy struct {
	OrgSlug          slug.Slug
	Slug             slug.Slug
	Name             string
	Scope            model.AlertPolicyScope
	ProjectSlug      *slug.Slug
	EnvironmentSlug  *slug.Slug
	NotificationType *model.NotificationType
	Severity         *model.NotificationSeverity
	LifecycleState   *model.NotificationLifecycleState
	SourceKind       *model.EventEntityType
	SourceAction     *model.EventAction
	SourceRef        *slug.Slug
	DestinationSlugs []slug.Slug
	Enabled          bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Record struct {
	OrgSlug           slug.Slug
	Slug              slug.Slug
	ProjectSlug       *slug.Slug
	EnvironmentSlug   *slug.Slug
	NotificationType  model.NotificationType
	Severity          model.NotificationSeverity
	LifecycleState    model.NotificationLifecycleState
	DedupeFingerprint string
	SourceEventSlug   *slug.Slug
	SourceEntityType  model.EventEntityType
	SourceAction      model.EventAction
	SourceRefs        json.RawMessage
	SafeMetadata      json.RawMessage
	FirstSeenAt       time.Time
	LastSeenAt        time.Time
	OccurrenceCount   int
	LastOccurrenceAt  time.Time
	DispatchSummary   json.RawMessage
	AcknowledgedAt    *time.Time
	ResolvedAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type DeliveryTarget struct {
	OrgSlug          slug.Slug
	OrganizationName string
	RecordSlug       slug.Slug
	AlertPolicySlug  slug.Slug
	DestinationSlug  slug.Slug
	DestinationName  string
	DestinationType  model.AlertDestinationType
	NonSecretConfig  json.RawMessage
	NotificationType model.NotificationType
	Severity         model.NotificationSeverity
	SourceEntityType model.EventEntityType
	SourceAction     model.EventAction
	SourceRefs       json.RawMessage
	SafeMetadata     json.RawMessage
	OccurrenceCount  int
}

type DeliveryWork struct {
	OrganizationName string
	OrgSlug          slug.Slug
	RecordSlug       slug.Slug
	AlertPolicySlug  slug.Slug
	DestinationSlug  slug.Slug
	DestinationName  string
	DestinationType  model.AlertDestinationType
	NonSecretConfig  json.RawMessage
	NotificationType model.NotificationType
	Severity         model.NotificationSeverity
	SourceEntityType model.EventEntityType
	SourceAction     model.EventAction
	SourceRefs       json.RawMessage
	SafeMetadata     json.RawMessage
	OccurrenceCount  int
}

type AlertDeliveryAttempt struct {
	OrgSlug                  slug.Slug
	Slug                     slug.Slug
	NotificationRecordSlug   slug.Slug
	AlertPolicySlug          *slug.Slug
	DestinationSlug          *slug.Slug
	DestinationName          *string
	DestinationType          model.AlertDestinationType
	Status                   model.AlertDeliveryAttemptStatus
	ProviderAttempt          int
	JobKind                  string
	JobQueue                 string
	JobState                 string
	ProviderResponseMetadata json.RawMessage
	ErrorCategory            *string
	ErrorMessage             *string
	QueuedAt                 time.Time
	SentAt                   *time.Time
	FailedAt                 *time.Time
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

type ClaimConnectionDeliveryParams struct {
	OrgSlug, RecordSlug, DestinationSlug, AlertPolicySlug, AttemptSlug slug.Slug
	Now, LeaseUntil                                                    time.Time
}

type ConnectionDeliveryClaim struct {
	Slug                     slug.Slug
	ProviderAttempt          int
	Status                   model.AlertDeliveryAttemptStatus
	JobState                 string
	LeaseUntil, RetryAfterAt *time.Time
	Acquired                 bool
}

type FinishConnectionDeliveryParams struct {
	OrgSlug, AttemptSlug        slug.Slug
	ProviderAttempt             int
	Status                      model.AlertDeliveryAttemptStatus
	JobState                    string
	ErrorCategory, ErrorMessage *string
	RetryAfterAt                *time.Time
	Now                         time.Time
	ProviderResponseMetadata    json.RawMessage
}
