package notifications

import (
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

// destinationFromCreateRow maps a generated SQLC alert destination row to the
// repository domain shape.
func destinationFromCreateRow(row dbgen.AlertDestination, orgSlug slug.Slug) (*AlertDestination, error) {
	destinationType, err := notificationDestinationType(row.Type)
	if err != nil {
		return nil, err
	}
	return &AlertDestination{
		OrgSlug:         orgSlug,
		Slug:            row.Slug,
		Name:            row.Name,
		Type:            destinationType,
		NonSecretConfig: rawJSON(row.NonSecretConfig),
		Enabled:         row.Enabled,
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
	}, nil
}

// destinationFromListRow maps a generated SQLC alert destination list row to the repository shape.
func destinationFromListRow(row dbgen.AlertDestination, orgSlug slug.Slug) (*AlertDestination, error) {
	destinationType, err := notificationDestinationType(row.Type)
	if err != nil {
		return nil, err
	}
	return &AlertDestination{
		OrgSlug:         orgSlug,
		Slug:            row.Slug,
		Name:            row.Name,
		Type:            destinationType,
		NonSecretConfig: rawJSON(row.NonSecretConfig),
		Enabled:         row.Enabled,
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
	}, nil
}

// destinationFromFindRow maps a generated SQLC alert destination lookup row to the repository shape.
func destinationFromFindRow(row dbgen.AlertDestination, orgSlug slug.Slug) (*AlertDestination, error) {
	destinationType, err := notificationDestinationType(row.Type)
	if err != nil {
		return nil, err
	}
	return &AlertDestination{
		OrgSlug:         orgSlug,
		Slug:            row.Slug,
		Name:            row.Name,
		Type:            destinationType,
		NonSecretConfig: rawJSON(row.NonSecretConfig),
		Enabled:         row.Enabled,
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
	}, nil
}

// destinationFromUpdateRow maps a generated SQLC alert destination update row to the repository shape.
func destinationFromUpdateRow(row dbgen.UpdateAlertDestinationRow, orgSlug slug.Slug) (*AlertDestination, error) {
	destinationType, err := notificationDestinationType(row.Type)
	if err != nil {
		return nil, err
	}
	return &AlertDestination{
		OrgSlug:         orgSlug,
		Slug:            row.Slug,
		Name:            row.Name,
		Type:            destinationType,
		NonSecretConfig: rawJSON(row.NonSecretConfig),
		Enabled:         row.Enabled,
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
	}, nil
}

// alertPolicyFromCreateRow maps a generated SQLC alert policy row to the repository domain shape.
func alertPolicyFromCreateRow(row dbgen.CreateAlertPolicyRow, orgSlug slug.Slug) (*AlertPolicy, error) {
	scope, notificationType, severity, lifecycleState, sourceAction, err := parseAlertPolicyEnums(
		row.Scope,
		row.NotificationType,
		row.Severity,
		row.LifecycleState,
		row.SourceAction,
	)
	if err != nil {
		return nil, err
	}
	sourceKindValue, sourceKindSet, err := optionalEventEntityTypeFromText(row.SourceKind)
	if err != nil {
		return nil, err
	}
	var sourceKind *model.EventEntityType
	if sourceKindSet {
		sourceKind = &sourceKindValue
	}
	sourceRefValue, sourceRefSet, err := optionalAlertPolicySourceRefFromText(row.SourceRef)
	if err != nil {
		return nil, err
	}
	var sourceRef *slug.Slug
	if sourceRefSet {
		sourceRef = &sourceRefValue
	}
	return &AlertPolicy{
		OrgSlug:          orgSlug,
		Slug:             row.Slug,
		Name:             row.Name,
		Scope:            scope,
		ProjectSlug:      optionalSlugFromString(row.ProjectSlug),
		EnvironmentSlug:  optionalSlugFromString(row.EnvironmentSlug),
		NotificationType: notificationType,
		Severity:         severity,
		LifecycleState:   lifecycleState,
		SourceKind:       sourceKind,
		SourceAction:     sourceAction,
		SourceRef:        sourceRef,
		Enabled:          row.Enabled,
		CreatedAt:        row.CreatedAt.Time,
		UpdatedAt:        row.UpdatedAt.Time,
	}, nil
}

// alertPolicyFromListRow maps a generated SQLC alert policy list row to the repository shape.
func alertPolicyFromListRow(row dbgen.ListAlertPoliciesByOrganizationRow, orgSlug slug.Slug) (*AlertPolicy, error) {
	scope, notificationType, severity, lifecycleState, sourceAction, err := parseAlertPolicyEnums(
		row.Scope,
		row.NotificationType,
		row.Severity,
		row.LifecycleState,
		row.SourceAction,
	)
	if err != nil {
		return nil, err
	}
	sourceKindValue, sourceKindSet, err := optionalEventEntityTypeFromText(row.SourceKind)
	if err != nil {
		return nil, err
	}
	var sourceKind *model.EventEntityType
	if sourceKindSet {
		sourceKind = &sourceKindValue
	}
	sourceRefValue, sourceRefSet, err := optionalAlertPolicySourceRefFromText(row.SourceRef)
	if err != nil {
		return nil, err
	}
	var sourceRef *slug.Slug
	if sourceRefSet {
		sourceRef = &sourceRefValue
	}
	return &AlertPolicy{
		OrgSlug:          orgSlug,
		Slug:             row.Slug,
		Name:             row.Name,
		Scope:            scope,
		ProjectSlug:      optionalSlugFromString(row.ProjectSlug),
		EnvironmentSlug:  optionalSlugFromString(row.EnvironmentSlug),
		NotificationType: notificationType,
		Severity:         severity,
		LifecycleState:   lifecycleState,
		DestinationSlugs: slugsFromStrings(row.DestinationSlugs),
		SourceKind:       sourceKind,
		SourceAction:     sourceAction,
		SourceRef:        sourceRef,
		Enabled:          row.Enabled,
		CreatedAt:        row.CreatedAt.Time,
		UpdatedAt:        row.UpdatedAt.Time,
	}, nil
}

// alertPolicyFromFindRow maps a generated SQLC alert policy lookup row to the repository shape.
func alertPolicyFromFindRow(row dbgen.FindAlertPolicyBySlugRow, orgSlug slug.Slug) (*AlertPolicy, error) {
	scope, notificationType, severity, lifecycleState, sourceAction, err := parseAlertPolicyEnums(
		row.Scope,
		row.NotificationType,
		row.Severity,
		row.LifecycleState,
		row.SourceAction,
	)
	if err != nil {
		return nil, err
	}
	sourceKindValue, sourceKindSet, err := optionalEventEntityTypeFromText(row.SourceKind)
	if err != nil {
		return nil, err
	}
	var sourceKind *model.EventEntityType
	if sourceKindSet {
		sourceKind = &sourceKindValue
	}
	sourceRefValue, sourceRefSet, err := optionalAlertPolicySourceRefFromText(row.SourceRef)
	if err != nil {
		return nil, err
	}
	var sourceRef *slug.Slug
	if sourceRefSet {
		sourceRef = &sourceRefValue
	}
	return &AlertPolicy{
		OrgSlug:          orgSlug,
		Slug:             row.Slug,
		Name:             row.Name,
		Scope:            scope,
		ProjectSlug:      optionalSlugFromString(row.ProjectSlug),
		EnvironmentSlug:  optionalSlugFromString(row.EnvironmentSlug),
		NotificationType: notificationType,
		Severity:         severity,
		LifecycleState:   lifecycleState,
		DestinationSlugs: slugsFromStrings(row.DestinationSlugs),
		SourceKind:       sourceKind,
		SourceAction:     sourceAction,
		SourceRef:        sourceRef,
		Enabled:          row.Enabled,
		CreatedAt:        row.CreatedAt.Time,
		UpdatedAt:        row.UpdatedAt.Time,
	}, nil
}

// alertPolicyFromUpdateRow maps a generated SQLC alert policy update row to the repository shape.
func alertPolicyFromUpdateRow(row dbgen.UpdateAlertPolicyRow, orgSlug slug.Slug) (*AlertPolicy, error) {
	scope, notificationType, severity, lifecycleState, sourceAction, err := parseAlertPolicyEnums(
		row.Scope,
		row.NotificationType,
		row.Severity,
		row.LifecycleState,
		row.SourceAction,
	)
	if err != nil {
		return nil, err
	}
	sourceKindValue, sourceKindSet, err := optionalEventEntityTypeFromText(row.SourceKind)
	if err != nil {
		return nil, err
	}
	var sourceKind *model.EventEntityType
	if sourceKindSet {
		sourceKind = &sourceKindValue
	}
	sourceRefValue, sourceRefSet, err := optionalAlertPolicySourceRefFromText(row.SourceRef)
	if err != nil {
		return nil, err
	}
	var sourceRef *slug.Slug
	if sourceRefSet {
		sourceRef = &sourceRefValue
	}
	return &AlertPolicy{
		OrgSlug:          orgSlug,
		Slug:             row.Slug,
		Name:             row.Name,
		Scope:            scope,
		ProjectSlug:      optionalSlugFromString(row.ProjectSlug),
		EnvironmentSlug:  optionalSlugFromString(row.EnvironmentSlug),
		NotificationType: notificationType,
		Severity:         severity,
		LifecycleState:   lifecycleState,
		DestinationSlugs: slugsFromStrings(row.DestinationSlugs),
		SourceKind:       sourceKind,
		SourceAction:     sourceAction,
		SourceRef:        sourceRef,
		Enabled:          row.Enabled,
		CreatedAt:        row.CreatedAt.Time,
		UpdatedAt:        row.UpdatedAt.Time,
	}, nil
}

// recordFromUpsertRow maps a generated SQLC record row to the repository domain shape.
func recordFromUpsertRow(row dbgen.UpsertNotificationRecordRow, orgSlug slug.Slug) (*Record, error) {
	notificationType, severity, lifecycleState, sourceEntityType, sourceAction, err := parseRecordEnums(
		row.NotificationType,
		row.Severity,
		row.LifecycleState,
		row.SourceEntityType,
		row.SourceAction,
	)
	if err != nil {
		return nil, err
	}
	return &Record{
		OrgSlug:           orgSlug,
		Slug:              row.Slug,
		ProjectSlug:       optionalSlugFromString(row.ProjectSlug),
		EnvironmentSlug:   optionalSlugFromString(row.EnvironmentSlug),
		NotificationType:  notificationType,
		Severity:          severity,
		LifecycleState:    lifecycleState,
		DedupeFingerprint: row.DedupeFingerprint,
		SourceEventSlug:   optionalSlugFromText(row.SourceEventSlug),
		SourceEntityType:  sourceEntityType,
		SourceAction:      sourceAction,
		SourceRefs:        rawJSON(row.SourceRefs),
		SafeMetadata:      rawJSON(row.SafeMetadata),
		FirstSeenAt:       row.FirstSeenAt.Time,
		LastSeenAt:        row.LastSeenAt.Time,
		OccurrenceCount:   int(row.OccurrenceCount),
		LastOccurrenceAt:  row.LastOccurrenceAt.Time,
		DispatchSummary:   rawJSON(row.DispatchSummary),
		AcknowledgedAt:    postgres.FromNullableTimestamptz(row.AcknowledgedAt),
		ResolvedAt:        postgres.FromNullableTimestamptz(row.ResolvedAt),
		CreatedAt:         row.CreatedAt.Time,
		UpdatedAt:         row.UpdatedAt.Time,
	}, nil
}

// recordFromListRow maps a generated SQLC list row to the repository domain shape.
func recordFromListRow(row dbgen.ListNotificationRecordsByOrganizationRow, orgSlug slug.Slug) (*Record, error) {
	notificationType, severity, lifecycleState, sourceEntityType, sourceAction, err := parseRecordEnums(
		row.NotificationType,
		row.Severity,
		row.LifecycleState,
		row.SourceEntityType,
		row.SourceAction,
	)
	if err != nil {
		return nil, err
	}
	return &Record{
		OrgSlug:           orgSlug,
		Slug:              row.Slug,
		ProjectSlug:       optionalSlugFromString(row.ProjectSlug),
		EnvironmentSlug:   optionalSlugFromString(row.EnvironmentSlug),
		NotificationType:  notificationType,
		Severity:          severity,
		LifecycleState:    lifecycleState,
		DedupeFingerprint: row.DedupeFingerprint,
		SourceEventSlug:   optionalSlugFromText(row.SourceEventSlug),
		SourceEntityType:  sourceEntityType,
		SourceAction:      sourceAction,
		SourceRefs:        rawJSON(row.SourceRefs),
		SafeMetadata:      rawJSON(row.SafeMetadata),
		FirstSeenAt:       row.FirstSeenAt.Time,
		LastSeenAt:        row.LastSeenAt.Time,
		OccurrenceCount:   int(row.OccurrenceCount),
		LastOccurrenceAt:  row.LastOccurrenceAt.Time,
		DispatchSummary:   rawJSON(row.DispatchSummary),
		AcknowledgedAt:    postgres.FromNullableTimestamptz(row.AcknowledgedAt),
		ResolvedAt:        postgres.FromNullableTimestamptz(row.ResolvedAt),
		CreatedAt:         row.CreatedAt.Time,
		UpdatedAt:         row.UpdatedAt.Time,
	}, nil
}

// recordFromFindRow maps a generated SQLC record lookup row to the repository domain shape.
func recordFromFindRow(row dbgen.FindNotificationRecordBySlugRow, orgSlug slug.Slug) (*Record, error) {
	notificationType, severity, lifecycleState, sourceEntityType, sourceAction, err := parseRecordEnums(
		row.NotificationType,
		row.Severity,
		row.LifecycleState,
		row.SourceEntityType,
		row.SourceAction,
	)
	if err != nil {
		return nil, err
	}
	return &Record{
		OrgSlug:           orgSlug,
		Slug:              row.Slug,
		ProjectSlug:       optionalSlugFromString(row.ProjectSlug),
		EnvironmentSlug:   optionalSlugFromString(row.EnvironmentSlug),
		NotificationType:  notificationType,
		Severity:          severity,
		LifecycleState:    lifecycleState,
		DedupeFingerprint: row.DedupeFingerprint,
		SourceEventSlug:   optionalSlugFromText(row.SourceEventSlug),
		SourceEntityType:  sourceEntityType,
		SourceAction:      sourceAction,
		SourceRefs:        rawJSON(row.SourceRefs),
		SafeMetadata:      rawJSON(row.SafeMetadata),
		FirstSeenAt:       row.FirstSeenAt.Time,
		LastSeenAt:        row.LastSeenAt.Time,
		OccurrenceCount:   int(row.OccurrenceCount),
		LastOccurrenceAt:  row.LastOccurrenceAt.Time,
		DispatchSummary:   rawJSON(row.DispatchSummary),
		AcknowledgedAt:    postgres.FromNullableTimestamptz(row.AcknowledgedAt),
		ResolvedAt:        postgres.FromNullableTimestamptz(row.ResolvedAt),
		CreatedAt:         row.CreatedAt.Time,
		UpdatedAt:         row.UpdatedAt.Time,
	}, nil
}

// recordFromAcknowledgeRow maps an acknowledged record row to repository shape.
func recordFromAcknowledgeRow(row dbgen.AcknowledgeNotificationRecordRow, orgSlug slug.Slug) (*Record, error) {
	notificationType, severity, lifecycleState, sourceEntityType, sourceAction, err := parseRecordEnums(
		row.NotificationType,
		row.Severity,
		row.LifecycleState,
		row.SourceEntityType,
		row.SourceAction,
	)
	if err != nil {
		return nil, err
	}
	return &Record{
		OrgSlug:           orgSlug,
		Slug:              row.Slug,
		ProjectSlug:       optionalSlugFromString(row.ProjectSlug),
		EnvironmentSlug:   optionalSlugFromString(row.EnvironmentSlug),
		NotificationType:  notificationType,
		Severity:          severity,
		LifecycleState:    lifecycleState,
		DedupeFingerprint: row.DedupeFingerprint,
		SourceEventSlug:   optionalSlugFromText(row.SourceEventSlug),
		SourceEntityType:  sourceEntityType,
		SourceAction:      sourceAction,
		SourceRefs:        rawJSON(row.SourceRefs),
		SafeMetadata:      rawJSON(row.SafeMetadata),
		FirstSeenAt:       row.FirstSeenAt.Time,
		LastSeenAt:        row.LastSeenAt.Time,
		OccurrenceCount:   int(row.OccurrenceCount),
		LastOccurrenceAt:  row.LastOccurrenceAt.Time,
		DispatchSummary:   rawJSON(row.DispatchSummary),
		AcknowledgedAt:    postgres.FromNullableTimestamptz(row.AcknowledgedAt),
		ResolvedAt:        postgres.FromNullableTimestamptz(row.ResolvedAt),
		CreatedAt:         row.CreatedAt.Time,
		UpdatedAt:         row.UpdatedAt.Time,
	}, nil
}

// recordFromResolveRow maps a resolved record row to repository shape.
func recordFromResolveRow(row dbgen.ResolveNotificationRecordRow, orgSlug slug.Slug) (*Record, error) {
	notificationType, severity, lifecycleState, sourceEntityType, sourceAction, err := parseRecordEnums(
		row.NotificationType,
		row.Severity,
		row.LifecycleState,
		row.SourceEntityType,
		row.SourceAction,
	)
	if err != nil {
		return nil, err
	}
	return &Record{
		OrgSlug:           orgSlug,
		Slug:              row.Slug,
		ProjectSlug:       optionalSlugFromString(row.ProjectSlug),
		EnvironmentSlug:   optionalSlugFromString(row.EnvironmentSlug),
		NotificationType:  notificationType,
		Severity:          severity,
		LifecycleState:    lifecycleState,
		DedupeFingerprint: row.DedupeFingerprint,
		SourceEventSlug:   optionalSlugFromText(row.SourceEventSlug),
		SourceEntityType:  sourceEntityType,
		SourceAction:      sourceAction,
		SourceRefs:        rawJSON(row.SourceRefs),
		SafeMetadata:      rawJSON(row.SafeMetadata),
		FirstSeenAt:       row.FirstSeenAt.Time,
		LastSeenAt:        row.LastSeenAt.Time,
		OccurrenceCount:   int(row.OccurrenceCount),
		LastOccurrenceAt:  row.LastOccurrenceAt.Time,
		DispatchSummary:   rawJSON(row.DispatchSummary),
		AcknowledgedAt:    postgres.FromNullableTimestamptz(row.AcknowledgedAt),
		ResolvedAt:        postgres.FromNullableTimestamptz(row.ResolvedAt),
		CreatedAt:         row.CreatedAt.Time,
		UpdatedAt:         row.UpdatedAt.Time,
	}, nil
}

// deliveryTargetFromRow maps a generated SQLC delivery-target row to repository shape.
func deliveryTargetFromRow(row dbgen.ListNotificationDeliveryTargetsForRecordRow) (DeliveryTarget, error) {
	destinationType, err := notificationDestinationType(row.DestinationType)
	if err != nil {
		return DeliveryTarget{}, err
	}
	notificationType, err := notificationType(row.NotificationType)
	if err != nil {
		return DeliveryTarget{}, err
	}
	severity, err := notificationSeverity(row.Severity)
	if err != nil {
		return DeliveryTarget{}, err
	}
	sourceEntityType, err := eventEntityType(row.SourceEntityType)
	if err != nil {
		return DeliveryTarget{}, err
	}
	sourceAction, err := eventAction(row.SourceAction)
	if err != nil {
		return DeliveryTarget{}, err
	}
	return DeliveryTarget{
		OrgSlug:          slug.Slug(row.OrgSlug),
		OrganizationName: row.OrganizationName,
		RecordSlug:       slug.Slug(row.RecordSlug),
		AlertPolicySlug:  slug.Slug(row.AlertPolicySlug),
		DestinationSlug:  slug.Slug(row.DestinationSlug),
		DestinationName:  row.DestinationName,
		DestinationType:  destinationType,
		NonSecretConfig:  rawJSON(row.NonSecretConfig),
		NotificationType: notificationType,
		Severity:         severity,
		SourceEntityType: sourceEntityType,
		SourceAction:     sourceAction,
		SourceRefs:       rawJSON(row.SourceRefs),
		SafeMetadata:     rawJSON(row.SafeMetadata),
		OccurrenceCount:  int(row.OccurrenceCount),
	}, nil
}

// deliveryWorkFromRow maps safe record/destination context for delivery execution.
func deliveryWorkFromRow(row dbgen.FindNotificationDeliveryWorkRow) (*DeliveryWork, error) {
	destinationType, err := notificationDestinationType(row.DestinationType)
	if err != nil {
		return nil, err
	}
	notificationType, err := notificationType(row.NotificationType)
	if err != nil {
		return nil, err
	}
	severity, err := notificationSeverity(row.Severity)
	if err != nil {
		return nil, err
	}
	sourceEntityType, err := eventEntityType(row.SourceEntityType)
	if err != nil {
		return nil, err
	}
	sourceAction, err := eventAction(row.SourceAction)
	if err != nil {
		return nil, err
	}

	return &DeliveryWork{
		OrganizationName: row.OrganizationName,
		OrgSlug:          row.OrgSlug,
		RecordSlug:       row.RecordSlug,
		AlertPolicySlug:  row.AlertPolicySlug,
		DestinationSlug:  row.DestinationSlug,
		DestinationName:  row.DestinationName,
		DestinationType:  destinationType,
		NonSecretConfig:  rawJSON(row.NonSecretConfig),
		NotificationType: notificationType,
		Severity:         severity,
		SourceEntityType: sourceEntityType,
		SourceAction:     sourceAction,
		SourceRefs:       row.SourceRefs,
		SafeMetadata:     rawJSON(row.SafeMetadata),
		OccurrenceCount:  int(row.OccurrenceCount),
	}, nil
}

// alertDeliveryAttemptFromCreateRow maps a queued attempt row to repository shape.
func alertDeliveryAttemptFromCreateRow(
	row dbgen.CreateAlertDeliveryAttemptRow,
	orgSlug slug.Slug,
) (*AlertDeliveryAttempt, error) {
	return alertDeliveryAttemptFromFields(alertDeliveryAttemptFields{
		orgSlug:                  orgSlug,
		slug:                     row.Slug,
		notificationRecordSlug:   row.NotificationRecordSlug,
		alertPolicySlug:          row.AlertPolicySlug,
		destinationSlug:          row.DestinationSlug,
		destinationName:          row.DestinationName,
		destinationType:          row.DestinationType,
		status:                   row.Status,
		providerAttempt:          row.ProviderAttempt,
		jobKind:                  row.JobKind,
		jobQueue:                 row.JobQueue,
		jobState:                 row.JobState,
		providerResponseMetadata: row.ProviderResponseMetadata,
		errorCategory:            row.ErrorCategory,
		errorMessage:             row.ErrorMessage,
		queuedAt:                 row.QueuedAt,
		sentAt:                   row.SentAt,
		failedAt:                 row.FailedAt,
		createdAt:                row.CreatedAt,
		updatedAt:                row.UpdatedAt,
	})
}

// alertDeliveryAttemptFromSentRow maps a sent attempt row to repository shape.
func alertDeliveryAttemptFromSentRow(row dbgen.MarkAlertDeliveryAttemptSentRow) (*AlertDeliveryAttempt, error) {
	return alertDeliveryAttemptFromFields(alertDeliveryAttemptFields{
		orgSlug:                  row.OrgSlug,
		slug:                     row.Slug,
		notificationRecordSlug:   row.NotificationRecordSlug,
		alertPolicySlug:          row.AlertPolicySlug,
		destinationSlug:          row.DestinationSlug,
		destinationName:          row.DestinationName,
		destinationType:          row.DestinationType,
		status:                   row.Status,
		providerAttempt:          row.ProviderAttempt,
		jobKind:                  row.JobKind,
		jobQueue:                 row.JobQueue,
		jobState:                 row.JobState,
		providerResponseMetadata: row.ProviderResponseMetadata,
		errorCategory:            row.ErrorCategory,
		errorMessage:             row.ErrorMessage,
		queuedAt:                 row.QueuedAt,
		sentAt:                   row.SentAt,
		failedAt:                 row.FailedAt,
		createdAt:                row.CreatedAt,
		updatedAt:                row.UpdatedAt,
	})
}

// alertDeliveryAttemptFromFailedRow maps a failed attempt row to repository shape.
func alertDeliveryAttemptFromFailedRow(row dbgen.MarkAlertDeliveryAttemptFailedRow) (*AlertDeliveryAttempt, error) {
	return alertDeliveryAttemptFromFields(alertDeliveryAttemptFields{
		orgSlug:                  row.OrgSlug,
		slug:                     row.Slug,
		notificationRecordSlug:   row.NotificationRecordSlug,
		alertPolicySlug:          row.AlertPolicySlug,
		destinationSlug:          row.DestinationSlug,
		destinationName:          row.DestinationName,
		destinationType:          row.DestinationType,
		status:                   row.Status,
		providerAttempt:          row.ProviderAttempt,
		jobKind:                  row.JobKind,
		jobQueue:                 row.JobQueue,
		jobState:                 row.JobState,
		providerResponseMetadata: row.ProviderResponseMetadata,
		errorCategory:            row.ErrorCategory,
		errorMessage:             row.ErrorMessage,
		queuedAt:                 row.QueuedAt,
		sentAt:                   row.SentAt,
		failedAt:                 row.FailedAt,
		createdAt:                row.CreatedAt,
		updatedAt:                row.UpdatedAt,
	})
}

// alertDeliveryAttemptFromListRow maps a listed attempt row to repository shape.
func alertDeliveryAttemptFromListRow(row dbgen.ListAlertDeliveryAttemptsForRecordRow) (*AlertDeliveryAttempt, error) {
	return alertDeliveryAttemptFromFields(alertDeliveryAttemptFields{
		orgSlug:                  row.OrgSlug,
		slug:                     row.Slug,
		notificationRecordSlug:   row.NotificationRecordSlug,
		alertPolicySlug:          row.AlertPolicySlug,
		destinationSlug:          row.DestinationSlug,
		destinationName:          row.DestinationName,
		destinationType:          row.DestinationType,
		status:                   row.Status,
		providerAttempt:          row.ProviderAttempt,
		jobKind:                  row.JobKind,
		jobQueue:                 row.JobQueue,
		jobState:                 row.JobState,
		providerResponseMetadata: row.ProviderResponseMetadata,
		errorCategory:            row.ErrorCategory,
		errorMessage:             row.ErrorMessage,
		queuedAt:                 row.QueuedAt,
		sentAt:                   row.SentAt,
		failedAt:                 row.FailedAt,
		createdAt:                row.CreatedAt,
		updatedAt:                row.UpdatedAt,
	})
}

// alertDeliveryAttemptFromRecentRow maps a capped recent attempt row to repository shape.
func alertDeliveryAttemptFromRecentRow(row dbgen.ListRecentAlertDeliveryAttemptsForRecordsRow) (*AlertDeliveryAttempt, error) {
	return alertDeliveryAttemptFromFields(alertDeliveryAttemptFields{
		orgSlug:                  slug.FromString(row.OrgSlug),
		slug:                     row.Slug,
		notificationRecordSlug:   slug.FromString(row.NotificationRecordSlug),
		alertPolicySlug:          row.AlertPolicySlug,
		destinationSlug:          row.DestinationSlug,
		destinationName:          row.DestinationName,
		destinationType:          row.DestinationType,
		status:                   row.Status,
		providerAttempt:          row.ProviderAttempt,
		jobKind:                  row.JobKind,
		jobQueue:                 row.JobQueue,
		jobState:                 row.JobState,
		providerResponseMetadata: row.ProviderResponseMetadata,
		errorCategory:            row.ErrorCategory,
		errorMessage:             row.ErrorMessage,
		queuedAt:                 row.QueuedAt,
		sentAt:                   row.SentAt,
		failedAt:                 row.FailedAt,
		createdAt:                row.CreatedAt,
		updatedAt:                row.UpdatedAt,
	})
}

type alertDeliveryAttemptFields struct {
	orgSlug                  slug.Slug
	slug                     slug.Slug
	notificationRecordSlug   slug.Slug
	alertPolicySlug          string
	destinationSlug          string
	destinationName          string
	destinationType          string
	status                   string
	providerAttempt          int32
	jobKind                  string
	jobQueue                 string
	jobState                 string
	providerResponseMetadata []byte
	errorCategory            pgtype.Text
	errorMessage             pgtype.Text
	queuedAt                 pgtype.Timestamptz
	sentAt                   pgtype.Timestamptz
	failedAt                 pgtype.Timestamptz
	createdAt                pgtype.Timestamptz
	updatedAt                pgtype.Timestamptz
}

func alertDeliveryAttemptFromFields(fields alertDeliveryAttemptFields) (*AlertDeliveryAttempt, error) {
	destinationType, err := notificationDestinationType(fields.destinationType)
	if err != nil {
		return nil, err
	}
	status, err := alertDeliveryAttemptStatus(fields.status)
	if err != nil {
		return nil, err
	}
	return &AlertDeliveryAttempt{
		OrgSlug:                  fields.orgSlug,
		Slug:                     fields.slug,
		NotificationRecordSlug:   fields.notificationRecordSlug,
		AlertPolicySlug:          optionalSlugFromString(fields.alertPolicySlug),
		DestinationSlug:          optionalSlugFromString(fields.destinationSlug),
		DestinationName:          optionalStringFromString(fields.destinationName),
		DestinationType:          destinationType,
		Status:                   status,
		ProviderAttempt:          int(fields.providerAttempt),
		JobKind:                  fields.jobKind,
		JobQueue:                 fields.jobQueue,
		JobState:                 fields.jobState,
		ProviderResponseMetadata: rawJSON(fields.providerResponseMetadata),
		ErrorCategory:            optionalStringFromText(fields.errorCategory),
		ErrorMessage:             optionalStringFromText(fields.errorMessage),
		QueuedAt:                 fields.queuedAt.Time,
		SentAt:                   postgres.FromNullableTimestamptz(fields.sentAt),
		FailedAt:                 postgres.FromNullableTimestamptz(fields.failedAt),
		CreatedAt:                fields.createdAt.Time,
		UpdatedAt:                fields.updatedAt.Time,
	}, nil
}

// recordFromDispatchSummaryRow maps an updated record row to repository shape.
func recordFromDispatchSummaryRow(row dbgen.UpdateNotificationRecordDispatchSummaryRow, orgSlug slug.Slug) (*Record, error) {
	notificationType, severity, lifecycleState, sourceEntityType, sourceAction, err := parseRecordEnums(
		row.NotificationType,
		row.Severity,
		row.LifecycleState,
		row.SourceEntityType,
		row.SourceAction,
	)
	if err != nil {
		return nil, err
	}
	return &Record{
		OrgSlug:           orgSlug,
		Slug:              row.Slug,
		ProjectSlug:       optionalSlugFromString(row.ProjectSlug),
		EnvironmentSlug:   optionalSlugFromString(row.EnvironmentSlug),
		NotificationType:  notificationType,
		Severity:          severity,
		LifecycleState:    lifecycleState,
		DedupeFingerprint: row.DedupeFingerprint,
		SourceEventSlug:   optionalSlugFromText(row.SourceEventSlug),
		SourceEntityType:  sourceEntityType,
		SourceAction:      sourceAction,
		SourceRefs:        rawJSON(row.SourceRefs),
		SafeMetadata:      rawJSON(row.SafeMetadata),
		FirstSeenAt:       row.FirstSeenAt.Time,
		LastSeenAt:        row.LastSeenAt.Time,
		OccurrenceCount:   int(row.OccurrenceCount),
		LastOccurrenceAt:  row.LastOccurrenceAt.Time,
		DispatchSummary:   rawJSON(row.DispatchSummary),
		AcknowledgedAt:    postgres.FromNullableTimestamptz(row.AcknowledgedAt),
		ResolvedAt:        postgres.FromNullableTimestamptz(row.ResolvedAt),
		CreatedAt:         row.CreatedAt.Time,
		UpdatedAt:         row.UpdatedAt.Time,
	}, nil
}

// notificationDestinationType parses stored alert destination type text into the model enum.
func notificationDestinationType(raw string) (model.AlertDestinationType, error) {
	value, err := model.AlertDestinationTypeString(raw)
	if err != nil || value == model.AlertDestinationTypeUnknown {
		return model.AlertDestinationTypeUnknown, invalidStoredEnum("alert destination type", raw)
	}
	return value, nil
}

// alertPolicyScope parses stored policy scope text into the model enum.
func alertPolicyScope(raw string) (model.AlertPolicyScope, error) {
	value, err := model.AlertPolicyScopeString(raw)
	if err != nil || value == model.AlertPolicyScopeUnknown {
		return model.AlertPolicyScopeUnknown, invalidStoredEnum("alert policy scope", raw)
	}
	return value, nil
}

// notificationSeverity parses stored severity text into the model enum.
func notificationSeverity(raw string) (model.NotificationSeverity, error) {
	value, err := model.NotificationSeverityString(raw)
	if err != nil || value == model.NotificationSeverityUnknown {
		return model.NotificationSeverityUnknown, invalidStoredEnum("notification severity", raw)
	}
	return value, nil
}

// notificationType parses stored notification type text into the model enum.
func notificationType(raw string) (model.NotificationType, error) {
	value, err := model.NotificationTypeString(raw)
	if err != nil || value == model.NotificationTypeUnknown {
		return model.NotificationTypeUnknown, invalidStoredEnum("notification type", raw)
	}
	return value, nil
}

// notificationLifecycleState parses stored lifecycle state text into the model enum.
func notificationLifecycleState(raw string) (model.NotificationLifecycleState, error) {
	value, err := model.NotificationLifecycleStateString(raw)
	if err != nil || value == model.NotificationLifecycleStateUnknown {
		return model.NotificationLifecycleStateUnknown, invalidStoredEnum("notification lifecycle state", raw)
	}
	return value, nil
}

// alertDeliveryAttemptStatus parses stored attempt status text into the model enum.
func alertDeliveryAttemptStatus(raw string) (model.AlertDeliveryAttemptStatus, error) {
	value, err := model.AlertDeliveryAttemptStatusString(raw)
	if err != nil || value == model.AlertDeliveryAttemptStatusUnknown {
		return model.AlertDeliveryAttemptStatusUnknown, invalidStoredEnum("alert delivery attempt status", raw)
	}
	return value, nil
}

// eventEntityType parses stored source entity type text into the model enum.
func eventEntityType(raw string) (model.EventEntityType, error) {
	value, err := model.EventEntityTypeString(raw)
	if err != nil || value == model.EventEntityTypeUnknown {
		return model.EventEntityTypeUnknown, invalidStoredEnum("event entity type", raw)
	}
	return value, nil
}

// eventAction parses stored source action text into the model enum.
func eventAction(raw string) (model.EventAction, error) {
	value, err := model.EventActionString(raw)
	if err != nil || value == model.EventActionUnknown {
		return model.EventActionUnknown, invalidStoredEnum("event action", raw)
	}
	return value, nil
}

// parseAlertPolicyEnums decodes the finite-domain fields stored on alert policy rows.
func parseAlertPolicyEnums(
	rawScope string,
	rawNotificationType pgtype.Text,
	rawSeverity pgtype.Text,
	rawLifecycleState pgtype.Text,
	rawSourceAction pgtype.Text,
) (
	model.AlertPolicyScope,
	*model.NotificationType,
	*model.NotificationSeverity,
	*model.NotificationLifecycleState,
	*model.EventAction,
	error,
) {
	scope, err := alertPolicyScope(rawScope)
	if err != nil {
		return scope, nil, nil, nil, nil, err
	}
	notificationTypeValue, hasNotificationType, err := optionalNotificationTypeFromText(rawNotificationType)
	var notificationType *model.NotificationType
	if hasNotificationType {
		notificationType = &notificationTypeValue
	}
	if err != nil {
		return scope, notificationType, nil, nil, nil, err
	}
	severityValue, hasSeverity, err := optionalNotificationSeverityFromText(rawSeverity)
	var severity *model.NotificationSeverity
	if hasSeverity {
		severity = &severityValue
	}
	if err != nil {
		return scope, notificationType, severity, nil, nil, err
	}
	lifecycleStateValue, hasLifecycleState, err := optionalNotificationLifecycleStateFromText(rawLifecycleState)
	var lifecycleState *model.NotificationLifecycleState
	if hasLifecycleState {
		lifecycleState = &lifecycleStateValue
	}
	if err != nil {
		return scope, notificationType, severity, lifecycleState, nil, err
	}
	sourceActionValue, hasSourceAction, err := optionalEventActionFromText(rawSourceAction)
	var sourceAction *model.EventAction
	if hasSourceAction {
		sourceAction = &sourceActionValue
	}
	return scope, notificationType, severity, lifecycleState, sourceAction, err
}

// parseRecordEnums decodes the finite-domain fields stored on record rows.
func parseRecordEnums(
	rawNotificationType string,
	rawSeverity string,
	rawLifecycleState string,
	rawSourceEntityType string,
	rawSourceAction string,
) (
	model.NotificationType,
	model.NotificationSeverity,
	model.NotificationLifecycleState,
	model.EventEntityType,
	model.EventAction,
	error,
) {
	notificationType, err := notificationType(rawNotificationType)
	if err != nil {
		return notificationType, model.NotificationSeverityUnknown, model.NotificationLifecycleStateUnknown, model.EventEntityTypeUnknown, model.EventActionUnknown, err
	}
	severity, err := notificationSeverity(rawSeverity)
	if err != nil {
		return notificationType, severity, model.NotificationLifecycleStateUnknown, model.EventEntityTypeUnknown, model.EventActionUnknown, err
	}
	lifecycleState, err := notificationLifecycleState(rawLifecycleState)
	if err != nil {
		return notificationType, severity, lifecycleState, model.EventEntityTypeUnknown, model.EventActionUnknown, err
	}
	sourceEntityType, err := eventEntityType(rawSourceEntityType)
	if err != nil {
		return notificationType, severity, lifecycleState, sourceEntityType, model.EventActionUnknown, err
	}
	sourceAction, err := eventAction(rawSourceAction)
	return notificationType, severity, lifecycleState, sourceEntityType, sourceAction, err
}

// invalidStoredEnum returns the shared dependency-error shape for corrupt
// persisted enum values.
func invalidStoredEnum(label, raw string) error {
	return xerror.Unexpected().Detailf("invalid persisted %s %q", label, raw)
}

// optionalEventEntityTypeFromText parses nullable event entity selector text.
func optionalEventEntityTypeFromText(value pgtype.Text) (model.EventEntityType, bool, error) {
	if !value.Valid {
		return model.EventEntityTypeUnknown, false, nil
	}
	parsed, err := model.EventEntityTypeString(value.String)
	if err != nil || parsed == model.EventEntityTypeUnknown {
		return model.EventEntityTypeUnknown, false, invalidStoredEnum("alert policy source_kind", value.String)
	}
	return parsed, true, nil
}

// optionalEventActionFromText parses nullable event action selector text.
func optionalEventActionFromText(value pgtype.Text) (model.EventAction, bool, error) {
	if !value.Valid {
		return model.EventActionUnknown, false, nil
	}
	parsed, err := model.EventActionString(value.String)
	if err != nil || parsed == model.EventActionUnknown {
		return model.EventActionUnknown, false, invalidStoredEnum("alert policy source_action", value.String)
	}
	return parsed, true, nil
}

// optionalNotificationTypeFromText parses nullable notification type selector text.
func optionalNotificationTypeFromText(value pgtype.Text) (model.NotificationType, bool, error) {
	if !value.Valid {
		return model.NotificationTypeUnknown, false, nil
	}
	parsed, err := model.NotificationTypeString(value.String)
	if err != nil || parsed == model.NotificationTypeUnknown {
		return model.NotificationTypeUnknown, false, invalidStoredEnum("alert policy notification_type", value.String)
	}
	return parsed, true, nil
}

// optionalNotificationSeverityFromText parses nullable severity selector text.
func optionalNotificationSeverityFromText(value pgtype.Text) (model.NotificationSeverity, bool, error) {
	if !value.Valid {
		return model.NotificationSeverityUnknown, false, nil
	}
	parsed, err := model.NotificationSeverityString(value.String)
	if err != nil || parsed == model.NotificationSeverityUnknown {
		return model.NotificationSeverityUnknown, false, invalidStoredEnum("alert policy severity", value.String)
	}
	return parsed, true, nil
}

// optionalNotificationLifecycleStateFromText parses nullable lifecycle selector text.
func optionalNotificationLifecycleStateFromText(value pgtype.Text) (model.NotificationLifecycleState, bool, error) {
	if !value.Valid {
		return model.NotificationLifecycleStateUnknown, false, nil
	}
	parsed, err := model.NotificationLifecycleStateString(value.String)
	if err != nil || parsed == model.NotificationLifecycleStateUnknown {
		return model.NotificationLifecycleStateUnknown, false, invalidStoredEnum("alert policy lifecycle_state", value.String)
	}
	return parsed, true, nil
}

// optionalAlertPolicySourceRefFromText parses nullable policy source refs into slugs.
func optionalAlertPolicySourceRefFromText(value pgtype.Text) (slug.Slug, bool, error) {
	if !value.Valid {
		return "", false, nil
	}
	parsed, err := slug.New(value.String)
	if err != nil {
		return "", false, invalidStoredEnum("alert policy source_ref", value.String)
	}
	return parsed, true, nil
}

// optionalSlugFromString parses non-empty slug text into a slug pointer.
func optionalSlugFromString(raw string) *slug.Slug {
	if raw == "" {
		return nil
	}
	parsed := slug.FromString(raw)
	return &parsed
}

// slugsFromStrings converts SQL text arrays into repository slug values.
func slugsFromStrings(raw []string) []slug.Slug {
	ret := make([]slug.Slug, 0, len(raw))
	for _, value := range raw {
		ret = append(ret, slug.FromString(value))
	}
	return ret
}

// optionalSlugFromText parses nullable slug text into a slug pointer.
func optionalSlugFromText(value pgtype.Text) *slug.Slug {
	if !value.Valid {
		return nil
	}
	return optionalSlugFromString(value.String)
}

// optionalStringFromText converts nullable SQL text into an optional string.
func optionalStringFromText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

// optionalStringFromString converts empty denormalized SQL text into nil.
func optionalStringFromString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// rawJSON clones generated JSONB bytes into a RawMessage.
func rawJSON(value []byte) []byte {
	return append([]byte(nil), value...)
}
