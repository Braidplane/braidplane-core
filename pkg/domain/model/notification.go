//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=AlertDestinationType -transform=snake -output=alert_destination_type_string.go -text -json -linecomment
//go:generate enumer -type=AlertPolicyScope -transform=snake -output=alert_policy_scope_string.go -text -json -linecomment
//go:generate enumer -type=NotificationSeverity -transform=snake -output=notification_severity_string.go -text -json -linecomment
//go:generate enumer -type=NotificationType -output=notification_type_string.go -text -json -linecomment
//go:generate enumer -type=NotificationLifecycleState -transform=snake -output=notification_lifecycle_state_string.go -text -json -linecomment
//go:generate enumer -type=NotificationDispatchStatus -transform=snake -output=notification_dispatch_status_string.go -text -json -linecomment
//go:generate enumer -type=AlertDeliveryAttemptStatus -transform=snake -output=alert_delivery_attempt_status_string.go -text -json -linecomment
package model

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type AlertDestinationType int

const (
	AlertDestinationTypeUnknown AlertDestinationType = iota // unknown
	AlertDestinationTypeEmail                               // email
	AlertDestinationTypeSlack                               // slack
)

type AlertPolicyScope int

const (
	AlertPolicyScopeUnknown            AlertPolicyScope = iota // unknown
	AlertPolicyScopeOrganization                               // organization
	AlertPolicyScopeProject                                    // project
	AlertPolicyScopeProjectEnvironment                         // project_environment
)

type NotificationSeverity int

const (
	NotificationSeverityUnknown  NotificationSeverity = iota // unknown
	NotificationSeverityInfo                                 // info
	NotificationSeverityWarning                              // warning
	NotificationSeverityCritical                             // critical
)

type NotificationType int

const (
	NotificationTypeUnknown                      NotificationType = iota // unknown
	NotificationTypePipelineRunFailed                                    // pipeline.run_failed
	NotificationTypePipelineRetryExhausted                               // pipeline.retry_exhausted
	NotificationTypePipelineScheduledRunDelayed                          // pipeline.scheduled_run_delayed
	NotificationTypePipelineScheduledRunMissing                          // pipeline.scheduled_run_missing
	NotificationTypePipelineFreshnessSLABreached                         // pipeline.freshness_sla_breached
	NotificationTypePipelineContractFailure                              // pipeline.contract_failure
	NotificationTypePipelineRunSucceeded                                 // pipeline.run_succeeded
)

func ProducerBackedNotificationTypes() []NotificationType {
	return []NotificationType{NotificationTypePipelineRunSucceeded, NotificationTypePipelineRetryExhausted, NotificationTypePipelineContractFailure, NotificationTypePipelineRunFailed, NotificationTypePipelineScheduledRunDelayed, NotificationTypePipelineScheduledRunMissing, NotificationTypePipelineFreshnessSLABreached}
}

type NotificationLifecycleState int

const (
	NotificationLifecycleStateUnknown      NotificationLifecycleState = iota // unknown
	NotificationLifecycleStateOpen                                           // open
	NotificationLifecycleStateAcknowledged                                   // acknowledged
	NotificationLifecycleStateResolved                                       // resolved
)

type NotificationDispatchStatus int

const (
	NotificationDispatchStatusUnknown NotificationDispatchStatus = iota // unknown
	NotificationDispatchStatusQueued                                    // queued
)

type AlertDeliveryAttemptStatus int

const (
	AlertDeliveryAttemptStatusUnknown AlertDeliveryAttemptStatus = iota // unknown
	AlertDeliveryAttemptStatusQueued                                    // queued
	AlertDeliveryAttemptStatusSent                                      // sent
	AlertDeliveryAttemptStatusFailed                                    // failed
)

type AlertDestination struct {
	Slug            slug.Slug
	Name            string
	Type            AlertDestinationType
	NonSecretConfig map[string]any
	Enabled         bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type AlertPolicy struct {
	Slug             slug.Slug
	Name             string
	Scope            AlertPolicyScope
	ProjectSlug      *slug.Slug
	EnvironmentSlug  *slug.Slug
	NotificationType *NotificationType
	Severity         *NotificationSeverity
	LifecycleState   *NotificationLifecycleState
	SourceKind       *EventEntityType
	SourceAction     *EventAction
	SourceRef        *slug.Slug
	DestinationSlugs []slug.Slug
	Enabled          bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type NotificationRecord struct {
	Slug              slug.Slug
	ProjectSlug       *slug.Slug
	EnvironmentSlug   *slug.Slug
	NotificationType  NotificationType
	Severity          NotificationSeverity
	LifecycleState    NotificationLifecycleState
	DedupeFingerprint string
	SourceEventSlug   *slug.Slug
	SourceEntityType  EventEntityType
	SourceAction      EventAction
	SourceRefs        map[string]string
	SafeMetadata      map[string]any
	FirstSeenAt       time.Time
	LastSeenAt        time.Time
	OccurrenceCount   int
	LastOccurrenceAt  time.Time
	DispatchSummary   map[string]any
	AcknowledgedAt    *time.Time
	ResolvedAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// NotificationCandidate is safe source evidence, shared by producers and the
// generic durable candidate worker. It contains no destination or provider data.
type NotificationCandidate struct {
	OrgSlug           slug.Slug `validate:"required"`
	RecordSlug        *slug.Slug
	ProjectSlug       *slug.Slug
	EnvironmentSlug   *slug.Slug
	NotificationType  NotificationType     `validate:"notificationtype"`
	Severity          NotificationSeverity `validate:"notificationseverity"`
	LifecycleState    NotificationLifecycleState
	ObservedAt        time.Time
	DedupeFingerprint string `validate:"required"`
	SourceEventSlug   *slug.Slug
	SourceEntityType  EventEntityType `validate:"evententitytype"`
	SourceAction      EventAction     `validate:"eventaction"`
	SourceRefs        map[string]string
	SafeMetadata      map[string]any
}

type NotificationDispatchSummary struct {
	DestinationSlug slug.Slug
	Status          NotificationDispatchStatus
	QueuedAt        *time.Time
}

type NotificationListItem struct {
	Slug             slug.Slug
	NotificationType NotificationType
	Severity         NotificationSeverity
	LifecycleState   NotificationLifecycleState
	Summary          string
	SourceEventSlug  *slug.Slug
	SourceEntityType EventEntityType
	SourceAction     EventAction
	SourceRefs       map[string]string
	SafeMetadata     map[string]any
	OccurrenceCount  int
	FirstSeenAt      time.Time
	LastSeenAt       time.Time
	LastOccurrenceAt time.Time
	DispatchStatuses []NotificationDispatchSummary
	AcknowledgedAt   *time.Time
	ResolvedAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type NotificationEmailRecipient struct {
	Email    string
	FullName string
	Locale   string
}

type NotificationEmail struct {
	OrganizationName      string
	RecordSlug            slug.Slug
	NotificationType      NotificationType
	NotificationTypeLabel string
	Severity              NotificationSeverity
	Summary               string
	SourceEntityType      EventEntityType
	SourceAction          EventAction
	OccurrenceCount       int
}

type AlertDeliveryAttempt struct {
	Slug                     slug.Slug
	NotificationRecordSlug   slug.Slug
	AlertPolicySlug          *slug.Slug
	DestinationSlug          *slug.Slug
	DestinationName          *string
	DestinationType          AlertDestinationType
	Status                   AlertDeliveryAttemptStatus
	ProviderAttempt          int
	ProviderResponseMetadata map[string]any
	ErrorCategory            *string
	ErrorMessage             *string
	QueuedAt                 time.Time
	SentAt                   *time.Time
	FailedAt                 *time.Time
	CreatedAt                time.Time
	UpdatedAt                time.Time
}
