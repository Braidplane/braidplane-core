package notificationsvc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
)

// CreateAlertDestination validates and stores an alert destination with only
// non-secret adapter configuration outside the credential store.
func (s *service) CreateAlertDestination(
	ctx context.Context,
	principal AlertManagementPrincipal,
	params CreateAlertDestinationParams,
) (*model.AlertDestination, error) {
	if err := validateCreateAlertDestinationInput(params); err != nil {
		return nil, err
	}

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.AlertDestination, error) {
		destination, err := s.createAlertDestination(ctx, tx, params)
		if err != nil {
			return nil, err
		}
		if err := s.enqueueRequiredAlertManagementAuditEvent(
			ctx,
			tx,
			params.OrgSlug,
			principal.EventActor(),
			alertDestinationSecurityEvent{
				destinationSlug: destination.Slug,
				action:          model.EventActionCreate,
				destinationType: destination.Type,
				enabled:         destination.Enabled,
			},
		); err != nil {
			return nil, err
		}
		return destination, nil
	})
}

// createAlertDestination persists an alert destination inside an existing unit of work.
func (s *service) createAlertDestination(
	ctx context.Context,
	tx postgres.DBTX,
	params CreateAlertDestinationParams,
) (*model.AlertDestination, error) {
	if err := validateCreateAlertDestinationInput(params); err != nil {
		return nil, err
	}
	slugWasProvided := params.Slug != ""
	destinationSlug, err := params.Slug.ValidateOrGenerate(params.Name)
	if err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	nonSecretConfig, err := normalizeSafeMap(params.NonSecretConfig)
	if err != nil {
		return nil, xerror.Wrapf("normalize non-secret alert destination config: %w", err)
	}

	createParams := reponotifications.CreateAlertDestinationParams{
		OrgSlug:         params.OrgSlug,
		Slug:            destinationSlug,
		Name:            params.Name,
		Type:            params.Type,
		NonSecretConfig: nonSecretConfig,
		Enabled:         true,
	}
	destination, err := s.repo.CreateAlertDestination(ctx, tx, createParams)
	if err != nil && xerror.Is(err, ErrDestinationSlugTaken) && !slugWasProvided {
		// Generated slugs get one repo-standard collision fallback; explicit slugs report the conflict.
		createParams.Slug, err = slug.WithRandomSuffix(destinationSlug, 6)
		if err != nil {
			return nil, ErrInvalidInput.With(err)
		}
		destination, err = s.repo.CreateAlertDestination(ctx, tx, createParams)
	}
	if err != nil {
		return nil, xerror.Wrapf("create alert destination: %w", err)
	}

	return toDestination(destination)
}

// CreateAlertPolicy validates and stores an alert policy plus its destination
// bindings in one transaction.
func (s *service) CreateAlertPolicy(
	ctx context.Context,
	principal AlertManagementPrincipal,
	params CreateAlertPolicyParams,
) (*model.AlertPolicy, error) {
	params = normalizeCreateAlertPolicyParams(params)
	if err := validateCreateAlertPolicyInput(params); err != nil {
		return nil, err
	}

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.AlertPolicy, error) {
		policy, err := s.createAlertPolicy(ctx, tx, params)
		if err != nil {
			return nil, err
		}
		if err := s.enqueueRequiredAlertManagementAuditEvent(
			ctx,
			tx,
			params.OrgSlug,
			principal.EventActor(),
			alertPolicySecurityEvent{
				policySlug:       policy.Slug,
				action:           model.EventActionCreate,
				scope:            policy.Scope,
				enabled:          policy.Enabled,
				destinationCount: len(policy.DestinationSlugs),
			},
		); err != nil {
			return nil, err
		}
		return policy, nil
	})
}

// createAlertPolicy persists an alert policy inside an existing unit of work.
func (s *service) createAlertPolicy(
	ctx context.Context,
	tx postgres.DBTX,
	params CreateAlertPolicyParams,
) (*model.AlertPolicy, error) {
	params = normalizeCreateAlertPolicyParams(params)
	if err := validateCreateAlertPolicyInput(params); err != nil {
		return nil, err
	}
	slugWasProvided := params.Slug != ""
	policySlug, err := params.Slug.ValidateOrGenerate(params.Name)
	if err != nil {
		return nil, ErrInvalidInput.With(err)
	}

	createParams := reponotifications.CreateAlertPolicyParams{
		OrgSlug:          params.OrgSlug,
		Slug:             policySlug,
		Name:             params.Name,
		Scope:            params.Scope,
		ProjectSlug:      params.ProjectSlug,
		EnvironmentSlug:  params.EnvironmentSlug,
		NotificationType: params.NotificationType,
		Severity:         params.Severity,
		LifecycleState:   params.LifecycleState,
		SourceKind:       params.SourceKind,
		SourceAction:     params.SourceAction,
		SourceRef:        params.SourceRef,
		Enabled:          true,
	}
	policy, err := s.repo.CreateAlertPolicy(ctx, tx, createParams)
	if err != nil && xerror.Is(err, ErrAlertPolicySlugTaken) && !slugWasProvided {
		// Generated slugs get one repo-standard collision fallback; explicit slugs report the conflict.
		createParams.Slug, err = slug.WithRandomSuffix(policySlug, 6)
		if err != nil {
			return nil, ErrInvalidInput.With(err)
		}
		policySlug = createParams.Slug
		policy, err = s.repo.CreateAlertPolicy(ctx, tx, createParams)
	}
	if err != nil {
		return nil, xerror.Wrapf("create alert policy: %w", err)
	}

	for _, destinationSlug := range params.DestinationSlugs {
		if bindErr := s.repo.BindAlertPolicyDestination(ctx, tx, reponotifications.BindAlertPolicyDestinationParams{
			OrgSlug:         params.OrgSlug,
			AlertPolicySlug: policySlug,
			DestinationSlug: destinationSlug,
		}); bindErr != nil {
			return nil, xerror.Wrapf("bind alert policy destination: %w", bindErr)
		}
	}
	policy.DestinationSlugs = append([]slug.Slug(nil), params.DestinationSlugs...)

	return toAlertPolicy(policy)
}

// CreateCandidate stores a notification record for a producer observation,
// then evaluates alert policy for destination delivery handoff.
func (s *service) CreateCandidate(
	ctx context.Context,
	params CreateCandidateParams,
) (*CreateCandidateResult, error) {
	sourceRefs, safeMetadata, err := normalizeCreateCandidateInput(params)
	if err != nil {
		return nil, err
	}

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*CreateCandidateResult, error) {
		// A producer-supplied record slug identifies one durable observation.
		// Replaying it must preserve occurrence counts and manual resolution.
		if params.RecordSlug != nil {
			record, err := s.repo.FindRecordBySlug(ctx, tx, params.OrgSlug, *params.RecordSlug)
			if err != nil && !xerror.Is(err, ErrRecordNotFound) {
				return nil, err
			}
			if record != nil {
				converted, err := toRecord(record)
				if err != nil {
					return nil, err
				}
				return &CreateCandidateResult{Record: converted}, nil
			}
		}
		result, err := s.createCandidate(ctx, tx, params, sourceRefs, safeMetadata)
		if err != nil {
			return nil, err
		}
		if result != nil && result.Record != nil {
			if err := s.enqueueRecordDeliveries(ctx, tx, params.OrgSlug, result.Record.Slug); err != nil {
				return nil, err
			}
		}
		return result, nil
	})
}

// createCandidate persists an idempotent notification candidate inside an existing unit of work.
func (s *service) createCandidate(
	ctx context.Context,
	tx postgres.DBTX,
	params CreateCandidateParams,
	sourceRefs json.RawMessage,
	safeMetadata json.RawMessage,
) (*CreateCandidateResult, error) {
	recordSlug, err := candidateRecordSlug(params)
	if err != nil {
		return nil, err
	}
	lifecycle := params.LifecycleState
	if lifecycle == model.NotificationLifecycleStateUnknown {
		lifecycle = model.NotificationLifecycleStateOpen
	}
	observedAt := params.ObservedAt
	if observedAt.IsZero() {
		observedAt = s.clk.Now().UTC()
	}
	record, err := s.repo.UpsertRecord(ctx, tx, reponotifications.UpsertRecordParams{
		OrgSlug:           params.OrgSlug,
		Slug:              recordSlug,
		ProjectSlug:       params.ProjectSlug,
		EnvironmentSlug:   params.EnvironmentSlug,
		NotificationType:  params.NotificationType,
		Severity:          params.Severity,
		LifecycleState:    lifecycle,
		DedupeFingerprint: params.DedupeFingerprint,
		SourceEventSlug:   params.SourceEventSlug,
		SourceEntityType:  params.SourceEntityType,
		SourceAction:      params.SourceAction,
		SourceRefs:        sourceRefs,
		SafeMetadata:      safeMetadata,
		ObservedAt:        observedAt,
	})
	if err != nil {
		return nil, xerror.Wrapf("upsert notification candidate record: %w", err)
	}

	converted, err := toRecord(record)
	if err != nil {
		return nil, err
	}
	return &CreateCandidateResult{
		Record: converted,
	}, nil
}

// normalizeCreateCandidateInput validates pure request preconditions before any transaction starts.
func normalizeCreateCandidateInput(params CreateCandidateParams) (json.RawMessage, json.RawMessage, error) {
	if err := validateCreateCandidateInput(params); err != nil {
		return nil, nil, err
	}

	// Validate source evidence before persistence so every notification record is
	// held to the safe metadata contract independently from alert policy.
	sourceRefs, err := normalizeSourceRefs(params.SourceRefs)
	if err != nil {
		return nil, nil, xerror.Wrapf("normalize notification candidate source refs: %w", err)
	}
	safeMetadata, err := normalizeSafeMap(params.SafeMetadata)
	if err != nil {
		return nil, nil, xerror.Wrapf("normalize notification candidate metadata: %w", err)
	}
	return sourceRefs, safeMetadata, nil
}

// candidateRecordSlug returns a provided record slug or generates a readable notification slug.
func candidateRecordSlug(params CreateCandidateParams) (slug.Slug, error) {
	if params.RecordSlug != nil {
		return *params.RecordSlug, nil
	}

	base, err := slug.FromName(params.NotificationType.String())
	if err != nil {
		return "", xerror.Wrapf("generate notification candidate slug base: %w", err)
	}
	recordSlug, err := slug.WithRandomSuffix(base, 6)
	if err != nil {
		return "", xerror.Wrapf("generate notification candidate slug: %w", err)
	}
	return recordSlug, nil
}

// UpsertRecord writes a notification occurrence and deduplicates unresolved
// records by fingerprint.
func (s *service) UpsertRecord(
	ctx context.Context,
	params UpsertRecordParams,
) (*model.NotificationRecord, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.NotificationRecord, error) {
		record, err := s.upsertRecord(ctx, tx, params)
		if err != nil {
			return nil, err
		}
		if record != nil {
			if err := s.enqueueRecordDeliveries(ctx, tx, params.OrgSlug, record.Slug); err != nil {
				return nil, err
			}
		}
		return record, nil
	})
}

// upsertRecord persists a record occurrence inside an existing unit of work.
func (s *service) upsertRecord(
	ctx context.Context,
	tx postgres.DBTX,
	params UpsertRecordParams,
) (*model.NotificationRecord, error) {
	if err := validateUpsertRecordInput(params); err != nil {
		return nil, err
	}

	sourceRefs, err := normalizeSourceRefs(params.SourceRefs)
	if err != nil {
		return nil, xerror.Wrapf("normalize notification source refs: %w", err)
	}
	safeMetadata, err := normalizeSafeMap(params.SafeMetadata)
	if err != nil {
		return nil, xerror.Wrapf("normalize notification metadata: %w", err)
	}

	record, err := s.repo.UpsertRecord(ctx, tx, reponotifications.UpsertRecordParams{
		OrgSlug:           params.OrgSlug,
		Slug:              params.Slug,
		ProjectSlug:       params.ProjectSlug,
		EnvironmentSlug:   params.EnvironmentSlug,
		NotificationType:  params.NotificationType,
		Severity:          params.Severity,
		LifecycleState:    model.NotificationLifecycleStateOpen,
		DedupeFingerprint: params.DedupeFingerprint,
		SourceEventSlug:   params.SourceEventSlug,
		SourceEntityType:  params.SourceEntityType,
		SourceAction:      params.SourceAction,
		SourceRefs:        sourceRefs,
		SafeMetadata:      safeMetadata,
		ObservedAt:        s.clk.Now().UTC(),
	})
	if err != nil {
		return nil, xerror.Wrapf("upsert notification record: %w", err)
	}

	return toRecord(record)
}

// AcknowledgeNotification marks one active notification as seen by the current operator.
func (s *service) AcknowledgeNotification(
	ctx context.Context,
	principal AlertManagementPrincipal,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
) (*model.NotificationListItem, error) {
	if err := validateNotificationLifecycleActionInput(orgSlug, recordSlug); err != nil {
		return nil, err
	}

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.NotificationListItem, error) {
		actionAt := s.clk.Now().UTC()
		record, ackErr := s.repo.AcknowledgeRecord(ctx, tx, reponotifications.AcknowledgeRecordParams{
			OrgSlug:        orgSlug,
			RecordSlug:     recordSlug,
			AcknowledgedAt: actionAt,
		})
		if ackErr != nil {
			return nil, xerror.Wrapf("acknowledge notification record: %w", ackErr)
		}
		s.recordNotificationLifecycleEvent(ctx, tx, orgSlug, principal.EventActor(), record, model.EventActionAcknowledge, actionAt)
		converted, transformErr := toRecord(record)
		if transformErr != nil {
			return nil, transformErr
		}
		return toNotificationListItem(converted)
	})
}

// ResolveNotification closes one notification so future matching observations start a new active record.
func (s *service) ResolveNotification(
	ctx context.Context,
	principal AlertManagementPrincipal,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
) (*model.NotificationListItem, error) {
	if err := validateNotificationLifecycleActionInput(orgSlug, recordSlug); err != nil {
		return nil, err
	}

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.NotificationListItem, error) {
		actionAt := s.clk.Now().UTC()
		record, resolveErr := s.repo.ResolveRecord(ctx, tx, reponotifications.ResolveRecordParams{
			OrgSlug:    orgSlug,
			RecordSlug: recordSlug,
			ResolvedAt: actionAt,
		})
		if resolveErr != nil {
			return nil, xerror.Wrapf("resolve notification record: %w", resolveErr)
		}
		s.recordNotificationLifecycleEvent(ctx, tx, orgSlug, principal.EventActor(), record, model.EventActionResolve, actionAt)
		converted, transformErr := toRecord(record)
		if transformErr != nil {
			return nil, transformErr
		}
		return toNotificationListItem(converted)
	})
}

// recordNotificationLifecycleEvent enqueues the manual notification lifecycle
// action audit event without making audit enqueue failures block the primary
// lifecycle mutation.
func (s *service) recordNotificationLifecycleEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	record *reponotifications.Record,
	action model.EventAction,
	occurredAt time.Time,
) {
	if record == nil {
		return
	}
	s.eventProducer.EnqueueRecord(ctx, tx, orgSlug, actor, notificationLifecycleEvent{
		recordSlug:       record.Slug,
		action:           action,
		lifecycleState:   record.LifecycleState,
		notificationType: record.NotificationType,
		severity:         record.Severity,
		occurredAt:       occurredAt,
	})
}
