package notificationsvc

import (
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
)

// toDestination maps a repository alert destination into the service model.
func toDestination(destination *reponotifications.AlertDestination) (*model.AlertDestination, error) {
	if destination == nil {
		return nil, xerror.Unexpected().Detail("alert destination is required")
	}

	nonSecretConfig, err := decodeMap(destination.NonSecretConfig, "alert destination non-secret config")
	if err != nil {
		return nil, err
	}

	return &model.AlertDestination{
		Slug:            destination.Slug,
		Name:            destination.Name,
		Type:            destination.Type,
		NonSecretConfig: nonSecretConfig,
		Enabled:         destination.Enabled,
		CreatedAt:       destination.CreatedAt,
		UpdatedAt:       destination.UpdatedAt,
	}, nil
}

// toAlertPolicy maps a repository alert policy into the service model.
func toAlertPolicy(policy *reponotifications.AlertPolicy) (*model.AlertPolicy, error) {
	if policy == nil {
		return nil, xerror.Unexpected().Detail("alert policy is required")
	}

	return &model.AlertPolicy{
		Slug:             policy.Slug,
		Name:             policy.Name,
		Scope:            policy.Scope,
		ProjectSlug:      policy.ProjectSlug,
		EnvironmentSlug:  policy.EnvironmentSlug,
		NotificationType: policy.NotificationType,
		Severity:         policy.Severity,
		LifecycleState:   policy.LifecycleState,
		SourceKind:       policy.SourceKind,
		SourceAction:     policy.SourceAction,
		SourceRef:        policy.SourceRef,
		DestinationSlugs: append([]slug.Slug(nil), policy.DestinationSlugs...),
		Enabled:          policy.Enabled,
		CreatedAt:        policy.CreatedAt,
		UpdatedAt:        policy.UpdatedAt,
	}, nil
}

// toRecord maps a repository record into the service model.
func toRecord(record *reponotifications.Record) (*model.NotificationRecord, error) {
	if record == nil {
		return nil, xerror.Unexpected().Detail("notification record is required")
	}

	sourceRefs, err := decodeStringMap(record.SourceRefs, "notification source refs")
	if err != nil {
		return nil, err
	}
	safeMetadata, err := decodeMap(record.SafeMetadata, "notification metadata")
	if err != nil {
		return nil, err
	}
	dispatchSummary, err := decodeMap(record.DispatchSummary, "notification dispatch summary")
	if err != nil {
		return nil, err
	}

	return &model.NotificationRecord{
		Slug:              record.Slug,
		ProjectSlug:       record.ProjectSlug,
		EnvironmentSlug:   record.EnvironmentSlug,
		NotificationType:  record.NotificationType,
		Severity:          record.Severity,
		LifecycleState:    record.LifecycleState,
		DedupeFingerprint: record.DedupeFingerprint,
		SourceEventSlug:   record.SourceEventSlug,
		SourceEntityType:  record.SourceEntityType,
		SourceAction:      record.SourceAction,
		SourceRefs:        sourceRefs,
		SafeMetadata:      safeMetadata,
		FirstSeenAt:       record.FirstSeenAt,
		LastSeenAt:        record.LastSeenAt,
		OccurrenceCount:   record.OccurrenceCount,
		LastOccurrenceAt:  record.LastOccurrenceAt,
		DispatchSummary:   dispatchSummary,
		AcknowledgedAt:    record.AcknowledgedAt,
		ResolvedAt:        record.ResolvedAt,
		CreatedAt:         record.CreatedAt,
		UpdatedAt:         record.UpdatedAt,
	}, nil
}

// toAlertDeliveryAttempt maps a repository delivery attempt into the service model.
func toAlertDeliveryAttempt(attempt *reponotifications.AlertDeliveryAttempt) (*model.AlertDeliveryAttempt, error) {
	if attempt == nil {
		return nil, xerror.Unexpected().Detail("alert delivery attempt is required")
	}

	providerResponseMetadata, err := decodeMap(
		attempt.ProviderResponseMetadata,
		"alert delivery attempt provider response metadata",
	)
	if err != nil {
		return nil, err
	}

	return &model.AlertDeliveryAttempt{
		Slug:                     attempt.Slug,
		NotificationRecordSlug:   attempt.NotificationRecordSlug,
		AlertPolicySlug:          attempt.AlertPolicySlug,
		DestinationSlug:          attempt.DestinationSlug,
		DestinationName:          attempt.DestinationName,
		DestinationType:          attempt.DestinationType,
		Status:                   attempt.Status,
		ProviderAttempt:          attempt.ProviderAttempt,
		ProviderResponseMetadata: providerResponseMetadata,
		ErrorCategory:            attempt.ErrorCategory,
		ErrorMessage:             attempt.ErrorMessage,
		QueuedAt:                 attempt.QueuedAt,
		SentAt:                   attempt.SentAt,
		FailedAt:                 attempt.FailedAt,
		CreatedAt:                attempt.CreatedAt,
		UpdatedAt:                attempt.UpdatedAt,
	}, nil
}

// decodeMap decodes JSONB object bytes into a display metadata map.
func decodeMap(raw json.RawMessage, label string) (map[string]any, error) {
	ret := map[string]any{}
	if len(raw) == 0 {
		return ret, nil
	}
	if err := json.Unmarshal(raw, &ret); err != nil {
		return nil, xerror.Wrapf("decode %s: %w", label, xerror.Unexpected().With(err))
	}
	return ret, nil
}

// decodeStringMap decodes JSONB object bytes into source reference strings.
func decodeStringMap(raw json.RawMessage, label string) (map[string]string, error) {
	ret := map[string]string{}
	if len(raw) == 0 {
		return ret, nil
	}
	if err := json.Unmarshal(raw, &ret); err != nil {
		return nil, xerror.Wrapf("decode %s: %w", label, xerror.Unexpected().With(err))
	}
	return ret, nil
}
