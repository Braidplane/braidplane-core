package notificationsvc

import (
	"context"
	"fmt"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
)

const (
	alertDestinationTestRecordSlug slug.Slug = "alert-destination-test"
)

// ListAlertDestinations returns tenant-scoped alert destinations for management.
func (s *service) ListAlertDestinations(
	ctx context.Context,
	_ AlertManagementPrincipal,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[*model.AlertDestination], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	ret, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) ([]*model.AlertDestination, error) {
		destinations, listErr := s.repo.ListAlertDestinationsByOrganization(ctx, db, orgSlug, queryReq)
		if listErr != nil {
			return nil, xerror.Wrapf("list alert destinations: %w", listErr)
		}

		converted := make([]*model.AlertDestination, 0, len(destinations))
		for _, destination := range destinations {
			item, transformErr := toDestination(destination)
			if transformErr != nil {
				return nil, transformErr
			}
			converted = append(converted, item)
		}
		return converted, nil
	})
	if err != nil {
		return model.ListResponse[*model.AlertDestination]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		func(item *model.AlertDestination, _ string) (value, slugValue string) {
			return item.CreatedAt.Format(time.RFC3339Nano), item.Slug.String()
		},
	)

	return model.ListResponse[*model.AlertDestination]{
		Items:      ret,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

// GetAlertDestination returns one tenant-scoped alert destination.
func (s *service) GetAlertDestination(
	ctx context.Context,
	_ AlertManagementPrincipal,
	orgSlug slug.Slug,
	destinationSlug slug.Slug,
) (*model.AlertDestination, error) {
	return postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*model.AlertDestination, error) {
		destination, err := s.repo.FindAlertDestinationBySlug(ctx, db, orgSlug, destinationSlug)
		if err != nil {
			return nil, xerror.Wrapf("get alert destination: %w", err)
		}
		return toDestination(destination)
	})
}

// TestAlertDestination enqueues an ephemeral notification delivery through one enabled destination.
func (s *service) TestAlertDestination(
	ctx context.Context,
	_ AlertManagementPrincipal,
	params TestAlertDestinationParams,
) (*AlertDestinationTestResult, error) {
	if err := validateTestAlertDestinationInput(params); err != nil {
		return nil, err
	}

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*AlertDestinationTestResult, error) {
		destination, err := s.repo.FindAlertDestinationBySlug(ctx, tx, params.OrgSlug, params.DestinationSlug)
		if err != nil {
			return nil, xerror.Wrapf("get alert destination for test: %w", err)
		}

		if !destination.Enabled {
			return nil, ErrDestinationDisabled
		}
		if destination.Type == model.AlertDestinationTypeSlack {
			testSlug, err := newAlertDeliveryAttemptSlug()
			if err != nil {
				return nil, err
			}
			job, err := s.deliveryProducer.EnqueueNotificationDelivery(ctx, tx, NotificationDeliveryJobParams{OrgSlug: params.OrgSlug, DestinationSlug: params.DestinationSlug, RecordSlug: testSlug, Test: true})
			if err != nil {
				return nil, err
			}
			return &AlertDestinationTestResult{DestinationSlug: params.DestinationSlug, DestinationType: destination.Type, RecipientCount: 1, DiagnosticsPersisted: false, EnqueuedJobs: []AlertDestinationTestJob{{Kind: job.Kind, Queue: job.Queue, State: job.State}}}, nil
		}
		if destination.Type != model.AlertDestinationTypeEmail {
			return nil, ErrDestinationTestUnsupported.Detailf("%s", destination.Type.String())
		}

		recipients, err := parseNotificationEmailRecipients(destination.NonSecretConfig)
		if err != nil {
			return nil, xerror.Wrapf("parse alert destination test recipients: %w", err)
		}

		notification := alertDestinationTestNotification(params.OrgSlug, params.DestinationSlug)
		jobs := make([]AlertDestinationTestJob, 0, len(recipients))
		for _, recipient := range recipients {
			queued, enqueueErr := s.emailProducer.EnqueueNotification(
				ctx,
				tx,
				params.OrgSlug,
				recipient,
				notification,
				"",
			)
			if enqueueErr != nil {
				return nil, alertDestinationTestEnqueueError(enqueueErr)
			}
			jobs = append(jobs, AlertDestinationTestJob{
				Kind:  queued.Kind,
				Queue: queued.Queue,
				State: queued.State,
			})
		}

		return &AlertDestinationTestResult{
			DestinationSlug:      params.DestinationSlug,
			DestinationType:      destination.Type,
			RecipientCount:       len(recipients),
			DiagnosticsPersisted: false,
			EnqueuedJobs:         jobs,
		}, nil
	})
}

// UpdateAlertDestination replaces mutable destination fields, including enabled state.
func (s *service) UpdateAlertDestination(
	ctx context.Context,
	principal AlertManagementPrincipal,
	params UpdateAlertDestinationParams,
) (*model.AlertDestination, error) {
	if err := validateUpdateAlertDestinationInput(params); err != nil {
		return nil, err
	}
	nonSecretConfig, err := normalizeSafeMap(params.NonSecretConfig)
	if err != nil {
		return nil, xerror.Wrapf("normalize non-secret alert destination config: %w", err)
	}

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.AlertDestination, error) {
		destination, updateErr := s.repo.UpdateAlertDestination(ctx, tx, reponotifications.UpdateAlertDestinationParams{
			OrgSlug:         params.OrgSlug,
			Slug:            params.Slug,
			Name:            params.Name,
			Type:            params.Type,
			NonSecretConfig: nonSecretConfig,
			Enabled:         params.Enabled,
		})
		if updateErr != nil {
			return nil, xerror.Wrapf("update alert destination: %w", updateErr)
		}
		converted, err := toDestination(destination)
		if err != nil {
			return nil, err
		}
		if err := s.enqueueRequiredAlertManagementAuditEvent(
			ctx,
			tx,
			params.OrgSlug,
			principal.EventActor(),
			alertDestinationSecurityEvent{
				destinationSlug: converted.Slug,
				action:          model.EventActionUpdate,
				destinationType: converted.Type,
				enabled:         converted.Enabled,
			},
		); err != nil {
			return nil, err
		}
		return converted, nil
	})
}

// DeleteAlertDestination removes one destination and its policy bindings.
func (s *service) DeleteAlertDestination(
	ctx context.Context,
	principal AlertManagementPrincipal,
	orgSlug slug.Slug,
	destinationSlug slug.Slug,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		destination, err := s.repo.FindAlertDestinationBySlug(ctx, tx, orgSlug, destinationSlug)
		if err != nil {
			return xerror.Wrapf("get alert destination for deletion audit: %w", err)
		}
		if err := s.repo.DeleteAlertDestination(ctx, tx, orgSlug, destinationSlug); err != nil {
			return xerror.Wrapf("delete alert destination: %w", err)
		}
		return s.enqueueRequiredAlertManagementAuditEvent(
			ctx,
			tx,
			orgSlug,
			principal.EventActor(),
			alertDestinationSecurityEvent{
				destinationSlug: destination.Slug,
				action:          model.EventActionDelete,
				destinationType: destination.Type,
				enabled:         destination.Enabled,
			},
		)
	})
}

// alertDestinationTestNotification builds the safe in-memory email payload for destination tests.
func alertDestinationTestNotification(orgSlug, destinationSlug slug.Slug) model.NotificationEmail {
	return model.NotificationEmail{
		OrganizationName:      orgSlug.String(),
		RecordSlug:            alertDestinationTestRecordSlug,
		NotificationType:      model.NotificationTypePipelineRunFailed,
		NotificationTypeLabel: "alert destination test",
		Severity:              model.NotificationSeverityInfo,
		Summary:               fmt.Sprintf("Test notification for alert destination %s.", destinationSlug.String()),
		SourceEntityType:      model.EventEntityTypeOrganization,
		SourceAction:          model.EventActionTestConnection,
		OccurrenceCount:       1,
	}
}

// alertDestinationTestEnqueueError preserves typed queue errors and normalizes unexpected dependency failures.
func alertDestinationTestEnqueueError(err error) error {
	var xe *xerror.Error
	if xerror.As(err, &xe) {
		return xerror.Wrapf("enqueue alert destination test email job: %w", err)
	}
	return xerror.Unavailable(xerror.AlertDestination).With(err)
}

// ListAlertPolicies returns tenant-scoped alert policies for management.
func (s *service) ListAlertPolicies(
	ctx context.Context,
	_ AlertManagementPrincipal,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[*model.AlertPolicy], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	ret, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) ([]*model.AlertPolicy, error) {
		policies, listErr := s.repo.ListAlertPoliciesByOrganization(ctx, db, orgSlug, queryReq)
		if listErr != nil {
			return nil, xerror.Wrapf("list alert policies: %w", listErr)
		}

		converted := make([]*model.AlertPolicy, 0, len(policies))
		for _, policy := range policies {
			item, transformErr := toAlertPolicy(policy)
			if transformErr != nil {
				return nil, transformErr
			}
			converted = append(converted, item)
		}
		return converted, nil
	})
	if err != nil {
		return model.ListResponse[*model.AlertPolicy]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		func(item *model.AlertPolicy, _ string) (value, slugValue string) {
			return item.CreatedAt.Format(time.RFC3339Nano), item.Slug.String()
		},
	)

	return model.ListResponse[*model.AlertPolicy]{
		Items:      ret,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

// GetAlertPolicy returns one tenant-scoped alert policy and its destination bindings.
func (s *service) GetAlertPolicy(
	ctx context.Context,
	_ AlertManagementPrincipal,
	orgSlug slug.Slug,
	policySlug slug.Slug,
) (*model.AlertPolicy, error) {
	return postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*model.AlertPolicy, error) {
		policy, err := s.repo.FindAlertPolicyBySlug(ctx, db, orgSlug, policySlug)
		if err != nil {
			return nil, xerror.Wrapf("get alert policy: %w", err)
		}
		return toAlertPolicy(policy)
	})
}

// UpdateAlertPolicy replaces mutable policy fields and destination bindings together.
func (s *service) UpdateAlertPolicy(
	ctx context.Context,
	principal AlertManagementPrincipal,
	params UpdateAlertPolicyParams,
) (*model.AlertPolicy, error) {
	params = normalizeUpdateAlertPolicyParams(params)
	if err := validateUpdateAlertPolicyInput(params); err != nil {
		return nil, err
	}

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.AlertPolicy, error) {
		policy, err := s.repo.UpdateAlertPolicy(ctx, tx, reponotifications.UpdateAlertPolicyParams{
			OrgSlug:          params.OrgSlug,
			Slug:             params.Slug,
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
			Enabled:          params.Enabled,
		})
		if err != nil {
			return nil, xerror.Wrapf("update alert policy: %w", err)
		}
		if err := s.repo.ClearAlertPolicyDestinations(ctx, tx, params.OrgSlug, params.Slug); err != nil {
			return nil, xerror.Wrapf("clear alert policy destinations: %w", err)
		}
		for _, destinationSlug := range params.DestinationSlugs {
			if bindErr := s.repo.BindAlertPolicyDestination(ctx, tx, reponotifications.BindAlertPolicyDestinationParams{
				OrgSlug:         params.OrgSlug,
				AlertPolicySlug: params.Slug,
				DestinationSlug: destinationSlug,
			}); bindErr != nil {
				return nil, xerror.Wrapf("bind alert policy destination: %w", bindErr)
			}
		}
		policy.DestinationSlugs = append([]slug.Slug(nil), params.DestinationSlugs...)
		converted, err := toAlertPolicy(policy)
		if err != nil {
			return nil, err
		}
		if err := s.enqueueRequiredAlertManagementAuditEvent(
			ctx,
			tx,
			params.OrgSlug,
			principal.EventActor(),
			alertPolicySecurityEvent{
				policySlug:       converted.Slug,
				action:           model.EventActionUpdate,
				scope:            converted.Scope,
				enabled:          converted.Enabled,
				destinationCount: len(converted.DestinationSlugs),
			},
		); err != nil {
			return nil, err
		}
		return converted, nil
	})
}

// DeleteAlertPolicy removes one tenant-scoped alert policy and its bindings.
func (s *service) DeleteAlertPolicy(
	ctx context.Context,
	principal AlertManagementPrincipal,
	orgSlug slug.Slug,
	policySlug slug.Slug,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		policy, err := s.repo.FindAlertPolicyBySlug(ctx, tx, orgSlug, policySlug)
		if err != nil {
			return xerror.Wrapf("get alert policy for deletion audit: %w", err)
		}
		if err := s.repo.DeleteAlertPolicy(ctx, tx, orgSlug, policySlug); err != nil {
			return xerror.Wrapf("delete alert policy: %w", err)
		}
		return s.enqueueRequiredAlertManagementAuditEvent(
			ctx,
			tx,
			orgSlug,
			principal.EventActor(),
			alertPolicySecurityEvent{
				policySlug:       policy.Slug,
				action:           model.EventActionDelete,
				scope:            policy.Scope,
				enabled:          policy.Enabled,
				destinationCount: len(policy.DestinationSlugs),
			},
		)
	})
}
