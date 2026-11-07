package notificationsvc

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
)

const alertPolicySourceRefMaxLength = 100

// validateCreateAlertDestinationInput enforces the alert destination service contract before persistence.
func validateCreateAlertDestinationInput(params CreateAlertDestinationParams) error {
	if err := validate.Default().Struct(params); err != nil {
		return ErrInvalidInput.With(err)
	}
	if err := validateAlertDestinationConfig(params.Type, params.NonSecretConfig); err != nil {
		return err
	}
	return nil
}

// validateUpdateAlertDestinationInput enforces the alert destination update contract before persistence.
func validateUpdateAlertDestinationInput(params UpdateAlertDestinationParams) error {
	if err := validate.Default().Struct(params); err != nil {
		return ErrInvalidInput.With(err)
	}
	if err := validateAlertDestinationConfig(params.Type, params.NonSecretConfig); err != nil {
		return err
	}
	return nil
}

// validateCreateAlertPolicyInput enforces alert policy scope and destination binding rules.
func validateCreateAlertPolicyInput(params CreateAlertPolicyParams) error {
	params = normalizeCreateAlertPolicyParams(params)
	if err := validate.Default().Struct(params); err != nil {
		return ErrInvalidInput.With(err)
	}
	if err := validateAlertPolicyPrimarySelectors(params.NotificationType, params.Severity, params.SourceKind); err != nil {
		return err
	}
	if err := validateAlertPolicyScopeFields(params.Scope, params.ProjectSlug, params.EnvironmentSlug); err != nil {
		return err
	}
	if err := validateDestinationSlugs(params.DestinationSlugs); err != nil {
		return err
	}
	if err := validateAlertPolicySourceSelectors(params.SourceKind, params.SourceAction, params.SourceRef); err != nil {
		return err
	}
	return nil
}

// validateUpdateAlertPolicyInput enforces alert policy scope and destination binding rules.
func validateUpdateAlertPolicyInput(params UpdateAlertPolicyParams) error {
	params = normalizeUpdateAlertPolicyParams(params)
	if err := validate.Default().Struct(params); err != nil {
		return ErrInvalidInput.With(err)
	}
	if err := validateAlertPolicyPrimarySelectors(params.NotificationType, params.Severity, params.SourceKind); err != nil {
		return err
	}
	if err := validateAlertPolicyScopeFields(params.Scope, params.ProjectSlug, params.EnvironmentSlug); err != nil {
		return err
	}
	if err := validateDestinationSlugs(params.DestinationSlugs); err != nil {
		return err
	}
	if err := validateAlertPolicySourceSelectors(params.SourceKind, params.SourceAction, params.SourceRef); err != nil {
		return err
	}
	return nil
}

// validateUpsertRecordInput enforces record dedupe and source classification rules.
func validateUpsertRecordInput(params UpsertRecordParams) error {
	if err := validate.Default().Struct(params); err != nil {
		return ErrInvalidInput.With(err)
	}
	if params.ProjectSlug != nil && *params.ProjectSlug == "" {
		return ErrInvalidInput.Detail("notification project_slug is required when provided")
	}
	if params.EnvironmentSlug != nil && *params.EnvironmentSlug == "" {
		return ErrInvalidInput.Detail("notification environment_slug is required when provided")
	}
	if params.EnvironmentSlug != nil && params.ProjectSlug == nil {
		return ErrInvalidInput.Detail("notification environment_slug requires project_slug")
	}
	if params.SourceEventSlug != nil && *params.SourceEventSlug == "" {
		return ErrInvalidInput.Detail("notification source_event_slug is required when provided")
	}
	return nil
}

// validateNotificationLifecycleActionInput enforces the tenant and record scope for operator lifecycle actions.
func validateNotificationLifecycleActionInput(orgSlug, recordSlug slug.Slug) error {
	if orgSlug == "" {
		return ErrInvalidInput.Detail("notification organization slug is required")
	}
	if recordSlug == "" {
		return ErrInvalidInput.Detail("notification record slug is required")
	}
	return nil
}

// validateTestAlertDestinationInput enforces the tenant and destination scope for operator test actions.
func validateTestAlertDestinationInput(params TestAlertDestinationParams) error {
	if err := validate.Default().Struct(params); err != nil {
		return ErrInvalidInput.With(err)
	}
	return nil
}

// validateDeliverNotificationInput enforces the durable worker payload contract.
func validateDeliverNotificationInput(params DeliverNotificationParams) error {
	if err := validate.Default().Struct(params); err != nil {
		return ErrInvalidInput.With(err)
	}
	return nil
}

// validateCreateCandidateInput enforces producer scope and source evidence rules.
func validateCreateCandidateInput(params CreateCandidateParams) error {
	if params.LifecycleState != model.NotificationLifecycleStateUnknown && params.LifecycleState != model.NotificationLifecycleStateOpen && params.LifecycleState != model.NotificationLifecycleStateResolved {
		return ErrInvalidInput.Detail("notification producer lifecycle must be open or resolved")
	}
	if err := validate.Default().Struct(params); err != nil {
		return ErrInvalidInput.With(err)
	}
	if params.RecordSlug != nil && *params.RecordSlug == "" {
		return ErrInvalidInput.Detail("notification candidate record_slug is required when provided")
	}
	if params.ProjectSlug != nil && *params.ProjectSlug == "" {
		return ErrInvalidInput.Detail("notification candidate project_slug is required when provided")
	}
	if params.EnvironmentSlug != nil && *params.EnvironmentSlug == "" {
		return ErrInvalidInput.Detail("notification candidate environment_slug is required when provided")
	}
	if params.SourceEventSlug != nil && *params.SourceEventSlug == "" {
		return ErrInvalidInput.Detail("notification candidate source_event_slug is required when provided")
	}
	if params.EnvironmentSlug != nil && params.ProjectSlug == nil {
		return ErrInvalidInput.Detail("notification candidate environment_slug requires project_slug")
	}
	if params.SourceEventSlug == nil && len(params.SourceRefs) == 0 {
		return ErrInvalidInput.Detail("notification candidate requires source_event_slug or source refs")
	}
	return nil
}

// ValidateCandidate applies the same safe-evidence contract before queueing and persistence.
func ValidateCandidate(params CreateCandidateParams) error {
	_, _, err := normalizeCreateCandidateInput(params)
	return err
}

// normalizeCreateAlertPolicyParams applies internal defaults before validation.
func normalizeCreateAlertPolicyParams(params CreateAlertPolicyParams) CreateAlertPolicyParams {
	params.SourceRef = normalizeOptionalSlug(params.SourceRef)
	return params
}

// normalizeUpdateAlertPolicyParams applies internal defaults before validation.
func normalizeUpdateAlertPolicyParams(params UpdateAlertPolicyParams) UpdateAlertPolicyParams {
	params.SourceRef = normalizeOptionalSlug(params.SourceRef)
	return params
}

// normalizeOptionalSlug trims optional selector slugs while preserving nil as unset.
func normalizeOptionalSlug(value *slug.Slug) *slug.Slug {
	if value == nil {
		return nil
	}
	trimmed := slug.Slug(strings.TrimSpace(value.String()))
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// validateAlertPolicyScopeFields checks that scope-specific project and environment slugs are coherent.
func validateAlertPolicyScopeFields(
	scope model.AlertPolicyScope,
	projectSlug *slug.Slug,
	environmentSlug *slug.Slug,
) error {
	switch scope {
	case model.AlertPolicyScopeOrganization:
		if projectSlug != nil || environmentSlug != nil {
			return ErrInvalidInput.Detail("organization alert policy scope must not include project or environment")
		}
	case model.AlertPolicyScopeProject:
		if projectSlug == nil || *projectSlug == "" {
			return ErrInvalidInput.Detail("project alert policy scope requires project_slug")
		}
		if environmentSlug != nil {
			return ErrInvalidInput.Detail("project alert policy scope must not include environment")
		}
	case model.AlertPolicyScopeProjectEnvironment:
		if projectSlug == nil || *projectSlug == "" || environmentSlug == nil || *environmentSlug == "" {
			return ErrInvalidInput.Detail("project_environment alert policy scope requires project_slug and environment_slug")
		}
	default:
		return ErrInvalidInput.Detail("alert policy scope is invalid")
	}
	return nil
}

// validateAlertPolicyPrimarySelectors requires an alert policy to select a notification population.
func validateAlertPolicyPrimarySelectors(
	notificationType *model.NotificationType,
	severity *model.NotificationSeverity,
	sourceKind *model.EventEntityType,
) error {
	if notificationType == nil && severity == nil && sourceKind == nil {
		return ErrInvalidInput.Detail("alert policy requires notification_type, severity, or source_kind")
	}
	return nil
}

// validateDestinationSlugs ensures alert policy destination bindings are explicit and unique.
func validateDestinationSlugs(destinationSlugs []slug.Slug) error {
	if len(destinationSlugs) == 0 {
		return ErrInvalidInput.Detail("alert policy requires at least one destination")
	}

	seen := make(map[slug.Slug]struct{}, len(destinationSlugs))
	for _, destinationSlug := range destinationSlugs {
		if destinationSlug == "" {
			return ErrInvalidInput.Detail("alert policy destination_slug is required")
		}
		if _, ok := seen[destinationSlug]; ok {
			return ErrInvalidInput.Detailf("alert policy destination %q is duplicated", destinationSlug.String())
		}
		seen[destinationSlug] = struct{}{}
	}
	return nil
}

// validateAlertDestinationConfig enforces adapter-specific safe config before persistence.
func validateAlertDestinationConfig(destinationType model.AlertDestinationType, nonSecretConfig map[string]any) error {
	switch destinationType {
	case model.AlertDestinationTypeSlack:
		if len(nonSecretConfig) != 1 {
			return ErrInvalidInput.Detail("Slack destination requires only connection_slug")
		}
		raw, ok := nonSecretConfig["connection_slug"].(string)
		if !ok {
			return ErrInvalidInput.Detail("Slack destination requires connection_slug")
		}
		if _, err := slug.New(raw); err != nil {
			return ErrInvalidInput.With(err).Detail("Slack connection_slug is invalid")
		}
		return nil
	case model.AlertDestinationTypeEmail:
		rawConfig, err := json.Marshal(nonSecretConfig)
		if err != nil {
			return ErrInvalidInput.With(err)
		}
		if _, err := parseNotificationEmailRecipients(rawConfig); err != nil {
			return err
		}
		return nil
	default:
		return ErrInvalidInput.Detail("alert destination type is invalid")
	}
}

// validateAlertPolicySourceSelectors checks optional source selector bounds.
func validateAlertPolicySourceSelectors(
	sourceKind *model.EventEntityType,
	sourceAction *model.EventAction,
	sourceRef *slug.Slug,
) error {
	if sourceAction != nil && sourceKind == nil {
		return ErrInvalidInput.Detail("alert policy source_kind is required when source_action is provided")
	}
	if sourceKind != nil {
		if sourceAction == nil {
			if !model.AlertPolicySupportsEventSource(*sourceKind, nil) {
				return ErrInvalidInput.Detail("alert policy source_kind is not supported")
			}
		} else if !model.AlertPolicySupportsEventSource(*sourceKind, sourceAction) {
			return ErrInvalidInput.Detail("alert policy source_action is not supported for source_kind")
		}
	}
	if sourceRef != nil {
		if sourceKind == nil {
			return ErrInvalidInput.Detail("alert policy source_kind is required when source_ref is provided")
		}
		sourceRefValue := strings.TrimSpace(sourceRef.String())
		if sourceRefValue == "" {
			return ErrInvalidInput.Detail("alert policy source_ref is required when provided")
		}
		if utf8.RuneCountInString(sourceRefValue) > alertPolicySourceRefMaxLength {
			return ErrInvalidInput.Detailf("alert policy source_ref must be at most %d characters", alertPolicySourceRefMaxLength)
		}
		if _, err := slug.New(sourceRefValue); err != nil {
			return ErrInvalidInput.With(err)
		}
	}
	return nil
}
