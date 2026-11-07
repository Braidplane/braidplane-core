package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type Repository interface {
	ClaimConnectionDelivery(context.Context, postgres.DBTX, ClaimConnectionDeliveryParams) (ConnectionDeliveryClaim, error)
	FinishConnectionDelivery(context.Context, postgres.DBTX, FinishConnectionDeliveryParams) error
	CreateAlertDestination(
		ctx context.Context,
		tx postgres.DBTX,
		params CreateAlertDestinationParams,
	) (*AlertDestination, error)
	ListAlertDestinationsByOrganization(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		req model.ListRequest,
	) ([]*AlertDestination, error)
	FindAlertDestinationBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		destinationSlug slug.Slug,
	) (*AlertDestination, error)
	UpdateAlertDestination(
		ctx context.Context,
		tx postgres.DBTX,
		params UpdateAlertDestinationParams,
	) (*AlertDestination, error)
	DeleteAlertDestination(ctx context.Context, tx postgres.DBTX, orgSlug, destinationSlug slug.Slug) error
	CreateAlertPolicy(ctx context.Context, tx postgres.DBTX, params CreateAlertPolicyParams) (*AlertPolicy, error)
	ListAlertPoliciesByOrganization(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		req model.ListRequest,
	) ([]*AlertPolicy, error)
	FindAlertPolicyBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		policySlug slug.Slug,
	) (*AlertPolicy, error)
	UpdateAlertPolicy(ctx context.Context, tx postgres.DBTX, params UpdateAlertPolicyParams) (*AlertPolicy, error)
	ClearAlertPolicyDestinations(ctx context.Context, tx postgres.DBTX, orgSlug, policySlug slug.Slug) error
	BindAlertPolicyDestination(ctx context.Context, tx postgres.DBTX, params BindAlertPolicyDestinationParams) error
	DeleteAlertPolicy(ctx context.Context, tx postgres.DBTX, orgSlug, policySlug slug.Slug) error
	UpsertRecord(ctx context.Context, tx postgres.DBTX, params UpsertRecordParams) (*Record, error)
	ListRecordsByOrganization(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		filters ListRecordFilters,
		req model.ListRequest,
	) ([]*Record, error)
	FindRecordBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		recordSlug slug.Slug,
	) (*Record, error)
	AcknowledgeRecord(ctx context.Context, tx postgres.DBTX, params AcknowledgeRecordParams) (*Record, error)
	ResolveRecord(ctx context.Context, tx postgres.DBTX, params ResolveRecordParams) (*Record, error)
	ListDeliveryTargetsForRecord(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		recordSlug slug.Slug,
	) ([]DeliveryTarget, error)
	FindDeliveryWork(
		ctx context.Context,
		tx postgres.DBTX,
		params FindDeliveryWorkParams,
	) (*DeliveryWork, error)
	CreateAlertDeliveryAttempt(
		ctx context.Context,
		tx postgres.DBTX,
		params CreateAlertDeliveryAttemptParams,
	) (*AlertDeliveryAttempt, error)
	ListAlertDeliveryAttemptsForRecord(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		recordSlug slug.Slug,
		req model.ListRequest,
	) ([]*AlertDeliveryAttempt, error)
	ListRecentAlertDeliveryAttemptsForRecords(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		recordSlugs []slug.Slug,
		limitPerRecord int,
	) ([]*AlertDeliveryAttempt, error)
	MarkAlertDeliveryAttemptSent(
		ctx context.Context,
		tx postgres.DBTX,
		params MarkAlertDeliveryAttemptSentParams,
	) (*AlertDeliveryAttempt, error)
	MarkAlertDeliveryAttemptFailed(
		ctx context.Context,
		tx postgres.DBTX,
		params MarkAlertDeliveryAttemptFailedParams,
	) (*AlertDeliveryAttempt, error)
	UpdateRecordDispatchSummary(
		ctx context.Context,
		tx postgres.DBTX,
		params UpdateRecordDispatchSummaryParams,
	) (*Record, error)
}

type repository struct{}

// New creates a notification repository backed by SQLC queries.
func New() Repository {
	return &repository{}
}

// CreateAlertDestination stores a tenant-scoped alert destination.
func (r *repository) CreateAlertDestination(
	ctx context.Context,
	tx postgres.DBTX,
	params CreateAlertDestinationParams,
) (*AlertDestination, error) {
	row, err := dbgen.New(tx).CreateAlertDestination(
		ctx,
		dbgen.CreateAlertDestinationParams{
			Slug:            params.Slug,
			Name:            params.Name,
			Type:            params.Type.String(),
			NonSecretConfig: params.NonSecretConfig,
			Enabled:         params.Enabled,
			OrgSlug:         params.OrgSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDestinationInvalidInput.With(err)
		}
		if postgres.IsUniqueViolation(err, "uq_alert_destinations_org_slug") {
			return nil, ErrDestinationSlugTaken.With(err)
		}
		if postgres.IsForeignKeyViolation(err) {
			return nil, ErrDestinationInvalidInput.With(err)
		}
		return nil, xerror.Wrapf("create alert destination: %w", err)
	}

	destination, err := destinationFromCreateRow(row, params.OrgSlug)
	if err != nil {
		return nil, xerror.Wrapf("decode alert destination: %w", err)
	}
	return destination, nil
}

// ListAlertDestinationsByOrganization returns tenant-scoped destinations for management UI and API.
func (r *repository) ListAlertDestinationsByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListRequest,
) ([]*AlertDestination, error) {
	cursorSlug, cursorTimestamp, _, err := postgres.BuildCursorValues(req.Cursor, model.ListSortCreatedAt)
	if err != nil {
		return nil, err
	}

	rows, err := dbgen.New(tx).ListAlertDestinationsByOrganization(
		ctx,
		dbgen.ListAlertDestinationsByOrganizationParams{
			OrgSlug:         orgSlug,
			CursorSlug:      cursorSlug,
			OrderDir:        postgres.ToText(req.Order),
			CursorTimestamp: cursorTimestamp,
			LimitCount:      int64(req.Limit),
		},
	)
	if err != nil {
		return nil, xerror.Wrapf("list alert destinations: %w", err)
	}

	ret := make([]*AlertDestination, 0, len(rows))
	for _, row := range rows {
		destination, transformErr := destinationFromListRow(row, orgSlug)
		if transformErr != nil {
			return nil, xerror.Wrapf("decode alert destination: %w", transformErr)
		}
		ret = append(ret, destination)
	}
	return ret, nil
}

// FindAlertDestinationBySlug returns one tenant-scoped alert destination.
func (r *repository) FindAlertDestinationBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	destinationSlug slug.Slug,
) (*AlertDestination, error) {
	row, err := dbgen.New(tx).FindAlertDestinationBySlug(
		ctx,
		dbgen.FindAlertDestinationBySlugParams{
			OrgSlug:         orgSlug,
			DestinationSlug: destinationSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDestinationNotFound.With(err)
		}
		return nil, xerror.Wrapf("find alert destination: %w", err)
	}

	destination, err := destinationFromFindRow(row, orgSlug)
	if err != nil {
		return nil, xerror.Wrapf("decode alert destination: %w", err)
	}
	return destination, nil
}

// UpdateAlertDestination fully replaces mutable alert destination fields.
func (r *repository) UpdateAlertDestination(
	ctx context.Context,
	tx postgres.DBTX,
	params UpdateAlertDestinationParams,
) (*AlertDestination, error) {
	row, err := dbgen.New(tx).UpdateAlertDestination(
		ctx,
		dbgen.UpdateAlertDestinationParams{
			OrgSlug:         postgres.SlugToText(params.OrgSlug),
			DestinationSlug: postgres.SlugToText(params.Slug),
			Name:            postgres.ToText(params.Name),
			Type:            postgres.ToText(params.Type.String()),
			NonSecretConfig: params.NonSecretConfig,
			Enabled:         pgtype.Bool{Bool: params.Enabled, Valid: true},
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDestinationNotFound.With(err)
		}
		if postgres.IsForeignKeyViolation(err) {
			return nil, ErrDestinationInvalidInput.With(err)
		}
		return nil, xerror.Wrapf("update alert destination: %w", err)
	}

	destination, err := destinationFromUpdateRow(row, params.OrgSlug)
	if err != nil {
		return nil, xerror.Wrapf("decode alert destination: %w", err)
	}
	return destination, nil
}

// DeleteAlertDestination removes a tenant-scoped alert destination and bindings.
func (r *repository) DeleteAlertDestination(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	destinationSlug slug.Slug,
) error {
	_, err := dbgen.New(tx).DeleteAlertDestination(
		ctx,
		dbgen.DeleteAlertDestinationParams{
			OrgSlug:         orgSlug,
			DestinationSlug: destinationSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return ErrDestinationNotFound.With(err)
		}
		return xerror.Wrapf("delete alert destination: %w", err)
	}
	return nil
}

// CreateAlertPolicy stores one tenant-scoped alert policy.
func (r *repository) CreateAlertPolicy(
	ctx context.Context,
	tx postgres.DBTX,
	params CreateAlertPolicyParams,
) (*AlertPolicy, error) {
	row, err := dbgen.New(tx).CreateAlertPolicy(
		ctx,
		dbgen.CreateAlertPolicyParams{
			Slug:             postgres.SlugToText(params.Slug),
			Name:             postgres.ToText(params.Name),
			Scope:            postgres.ToText(params.Scope.String()),
			NotificationType: optionalNotificationTypeToText(params.NotificationType),
			Severity:         optionalNotificationSeverityToText(params.Severity),
			LifecycleState:   optionalNotificationLifecycleStateToText(params.LifecycleState),
			SourceKind:       optionalEventEntityTypeToText(params.SourceKind),
			SourceAction:     optionalEventActionToText(params.SourceAction),
			SourceRef:        postgres.OptionalSlugToText(params.SourceRef),
			Enabled:          pgtype.Bool{Bool: params.Enabled, Valid: true},
			ProjectSlug:      postgres.OptionalSlugToText(params.ProjectSlug),
			EnvironmentSlug:  postgres.OptionalSlugToText(params.EnvironmentSlug),
			OrgSlug:          postgres.SlugToText(params.OrgSlug),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrAlertPolicyInvalidInput.With(err)
		}
		if postgres.IsUniqueViolation(err, "uq_alert_policies_org_slug") {
			return nil, ErrAlertPolicySlugTaken.With(err)
		}
		if postgres.IsForeignKeyViolation(err) {
			return nil, ErrAlertPolicyInvalidInput.With(err)
		}
		return nil, xerror.Wrapf("create alert policy: %w", err)
	}

	policy, err := alertPolicyFromCreateRow(row, params.OrgSlug)
	if err != nil {
		return nil, xerror.Wrapf("decode alert policy: %w", err)
	}
	return policy, nil
}

// ListAlertPoliciesByOrganization returns tenant-scoped alert policies with destination bindings.
func (r *repository) ListAlertPoliciesByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListRequest,
) ([]*AlertPolicy, error) {
	cursorSlug, cursorTimestamp, _, err := postgres.BuildCursorValues(req.Cursor, model.ListSortCreatedAt)
	if err != nil {
		return nil, err
	}

	rows, err := dbgen.New(tx).ListAlertPoliciesByOrganization(
		ctx,
		dbgen.ListAlertPoliciesByOrganizationParams{
			OrgSlug:         orgSlug,
			CursorSlug:      cursorSlug,
			OrderDir:        postgres.ToText(req.Order),
			CursorTimestamp: cursorTimestamp,
			LimitCount:      int64(req.Limit),
		},
	)
	if err != nil {
		return nil, xerror.Wrapf("list alert policies: %w", err)
	}

	ret := make([]*AlertPolicy, 0, len(rows))
	for _, row := range rows {
		policy, transformErr := alertPolicyFromListRow(row, orgSlug)
		if transformErr != nil {
			return nil, xerror.Wrapf("decode alert policy: %w", transformErr)
		}
		ret = append(ret, policy)
	}
	return ret, nil
}

// FindAlertPolicyBySlug returns one tenant-scoped alert policy with destination bindings.
func (r *repository) FindAlertPolicyBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	policySlug slug.Slug,
) (*AlertPolicy, error) {
	row, err := dbgen.New(tx).FindAlertPolicyBySlug(
		ctx,
		dbgen.FindAlertPolicyBySlugParams{
			OrgSlug:    orgSlug,
			PolicySlug: policySlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrAlertPolicyNotFound.With(err)
		}
		return nil, xerror.Wrapf("find alert policy: %w", err)
	}

	policy, err := alertPolicyFromFindRow(row, orgSlug)
	if err != nil {
		return nil, xerror.Wrapf("decode alert policy: %w", err)
	}
	return policy, nil
}

// UpdateAlertPolicy fully replaces mutable alert policy fields before bindings are replaced.
func (r *repository) UpdateAlertPolicy(
	ctx context.Context,
	tx postgres.DBTX,
	params UpdateAlertPolicyParams,
) (*AlertPolicy, error) {
	row, err := dbgen.New(tx).UpdateAlertPolicy(
		ctx,
		dbgen.UpdateAlertPolicyParams{
			OrgSlug:          postgres.SlugToText(params.OrgSlug),
			PolicySlug:       postgres.SlugToText(params.Slug),
			Name:             postgres.ToText(params.Name),
			Scope:            postgres.ToText(params.Scope.String()),
			ProjectSlug:      postgres.OptionalSlugToText(params.ProjectSlug),
			EnvironmentSlug:  postgres.OptionalSlugToText(params.EnvironmentSlug),
			NotificationType: optionalNotificationTypeToText(params.NotificationType),
			Severity:         optionalNotificationSeverityToText(params.Severity),
			LifecycleState:   optionalNotificationLifecycleStateToText(params.LifecycleState),
			SourceKind:       optionalEventEntityTypeToText(params.SourceKind),
			SourceAction:     optionalEventActionToText(params.SourceAction),
			SourceRef:        postgres.OptionalSlugToText(params.SourceRef),
			Enabled:          pgtype.Bool{Bool: params.Enabled, Valid: true},
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrAlertPolicyInvalidInput.With(err)
		}
		if postgres.IsForeignKeyViolation(err) {
			return nil, ErrAlertPolicyInvalidInput.With(err)
		}
		return nil, xerror.Wrapf("update alert policy: %w", err)
	}

	policy, err := alertPolicyFromUpdateRow(row, params.OrgSlug)
	if err != nil {
		return nil, xerror.Wrapf("decode alert policy: %w", err)
	}
	return policy, nil
}

// ClearAlertPolicyDestinations removes every destination binding for one policy.
func (r *repository) ClearAlertPolicyDestinations(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	policySlug slug.Slug,
) error {
	_, err := dbgen.New(tx).ClearAlertPolicyDestinations(
		ctx,
		dbgen.ClearAlertPolicyDestinationsParams{
			OrgSlug:    orgSlug,
			PolicySlug: policySlug,
		},
	)
	if err != nil {
		return xerror.Wrapf("clear alert policy destinations: %w", err)
	}
	return nil
}

// BindAlertPolicyDestination stores one alert-policy-to-destination binding.
func (r *repository) BindAlertPolicyDestination(
	ctx context.Context,
	tx postgres.DBTX,
	params BindAlertPolicyDestinationParams,
) error {
	row, err := dbgen.New(tx).BindAlertPolicyDestination(
		ctx,
		dbgen.BindAlertPolicyDestinationParams{
			OrgSlug:         params.OrgSlug,
			AlertPolicySlug: params.AlertPolicySlug,
			DestinationSlug: params.DestinationSlug,
		},
	)
	if err != nil {
		return xerror.Wrapf("bind alert policy destination: %w", err)
	}
	if !row.PolicyExists {
		return ErrAlertPolicyNotFound
	}
	if !row.DestinationExists {
		return ErrDestinationNotFound
	}
	return nil
}

// DeleteAlertPolicy removes one tenant-scoped alert policy and its bindings.
func (r *repository) DeleteAlertPolicy(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	policySlug slug.Slug,
) error {
	_, err := dbgen.New(tx).DeleteAlertPolicy(
		ctx,
		dbgen.DeleteAlertPolicyParams{
			OrgSlug:    orgSlug,
			PolicySlug: policySlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return ErrAlertPolicyNotFound.With(err)
		}
		return xerror.Wrapf("delete alert policy: %w", err)
	}
	return nil
}

// UpsertRecord creates an open notification record or updates the existing
// unresolved record for the same dedupe fingerprint.
func (r *repository) UpsertRecord(
	ctx context.Context,
	tx postgres.DBTX,
	params UpsertRecordParams,
) (*Record, error) {
	row, err := dbgen.New(tx).UpsertNotificationRecord(
		ctx,
		dbgen.UpsertNotificationRecordParams{
			OrgSlug:           postgres.SlugToText(params.OrgSlug),
			ProjectSlug:       postgres.OptionalSlugToText(params.ProjectSlug),
			EnvironmentSlug:   postgres.OptionalSlugToText(params.EnvironmentSlug),
			Slug:              postgres.SlugToText(params.Slug),
			NotificationType:  postgres.ToText(params.NotificationType.String()),
			Severity:          postgres.ToText(params.Severity.String()),
			LifecycleState:    postgres.ToText(params.LifecycleState.String()),
			DedupeFingerprint: postgres.ToText(params.DedupeFingerprint),
			SourceEventSlug:   postgres.OptionalSlugToText(params.SourceEventSlug),
			SourceEntityType:  postgres.ToText(params.SourceEntityType.String()),
			SourceAction:      postgres.ToText(params.SourceAction.String()),
			SourceRefs:        params.SourceRefs,
			SafeMetadata:      params.SafeMetadata,
			ObservedAt:        postgres.ToTimestamptz(params.ObservedAt),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrRecordInvalidInput.With(err)
		}
		if postgres.IsUniqueViolation(err, "uq_notification_records_org_slug") {
			return nil, ErrRecordSlugTaken.With(err)
		}
		if postgres.IsForeignKeyViolation(err) {
			return nil, ErrRecordInvalidInput.With(err)
		}
		return nil, xerror.Wrapf("upsert notification record: %w", err)
	}

	record, err := recordFromUpsertRow(row, params.OrgSlug)
	if err != nil {
		return nil, xerror.Wrapf("decode notification record: %w", err)
	}
	return record, nil
}

// ListRecordsByOrganization returns tenant-scoped notification records for the operator UI.
func (r *repository) ListRecordsByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	filters ListRecordFilters,
	req model.ListRequest,
) ([]*Record, error) {
	cursorSlug, cursorTimestamp, _, err := postgres.BuildCursorValues(req.Cursor, model.ListSortLastSeenAt)
	if err != nil {
		return nil, err
	}

	var lifecycleState *string
	var notificationType *string
	var severity *string
	var sourceKind *string
	var sourceAction *string
	var sourceRef *string
	if filters.LifecycleState != nil {
		value := filters.LifecycleState.String()
		lifecycleState = &value
	}
	if filters.NotificationType != nil {
		value := filters.NotificationType.String()
		notificationType = &value
	}
	if filters.Severity != nil {
		value := filters.Severity.String()
		severity = &value
	}
	if filters.SourceKind != nil {
		value := filters.SourceKind.String()
		sourceKind = &value
	}
	if filters.SourceAction != nil {
		value := filters.SourceAction.String()
		sourceAction = &value
	}
	if filters.SourceRef != nil {
		value := filters.SourceRef.String()
		sourceRef = &value
	}

	rows, err := dbgen.New(tx).ListNotificationRecordsByOrganization(
		ctx,
		dbgen.ListNotificationRecordsByOrganizationParams{
			OrgSlug:          orgSlug,
			LifecycleState:   postgres.ToNullableText(lifecycleState),
			IncludeResolved:  filters.IncludeResolved,
			NotificationType: postgres.ToNullableText(notificationType),
			Severity:         postgres.ToNullableText(severity),
			SourceKind:       postgres.ToNullableText(sourceKind),
			SourceAction:     postgres.ToNullableText(sourceAction),
			SourceRef:        postgres.ToNullableText(sourceRef),
			CursorSlug:       cursorSlug,
			CursorTimestamp:  cursorTimestamp,
			OrderDir:         postgres.ToText(req.Order),
			LimitCount:       int64(req.Limit),
		},
	)
	if err != nil {
		return nil, xerror.Wrapf("list notification records: %w", err)
	}

	ret := make([]*Record, 0, len(rows))
	for _, row := range rows {
		record, err := recordFromListRow(row, orgSlug)
		if err != nil {
			return nil, xerror.Wrapf("decode notification record: %w", err)
		}
		ret = append(ret, record)
	}
	return ret, nil
}

// FindRecordBySlug returns one tenant-scoped notification record.
func (r *repository) FindRecordBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
) (*Record, error) {
	row, err := dbgen.New(tx).FindNotificationRecordBySlug(
		ctx,
		dbgen.FindNotificationRecordBySlugParams{
			OrgSlug:    orgSlug,
			RecordSlug: recordSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrRecordNotFound.With(err)
		}
		return nil, xerror.Wrapf("find notification record: %w", err)
	}

	record, err := recordFromFindRow(row, orgSlug)
	if err != nil {
		return nil, xerror.Wrapf("decode notification record: %w", err)
	}
	return record, nil
}

// AcknowledgeRecord records the first operator acknowledgement for one tenant-scoped notification.
func (r *repository) AcknowledgeRecord(
	ctx context.Context,
	tx postgres.DBTX,
	params AcknowledgeRecordParams,
) (*Record, error) {
	row, err := dbgen.New(tx).AcknowledgeNotificationRecord(
		ctx,
		dbgen.AcknowledgeNotificationRecordParams{
			OrgSlug:        postgres.SlugToText(params.OrgSlug),
			RecordSlug:     postgres.SlugToText(params.RecordSlug),
			AcknowledgedAt: postgres.ToTimestamptz(params.AcknowledgedAt),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrRecordNotFound.With(err)
		}
		return nil, xerror.Wrapf("acknowledge notification record: %w", err)
	}

	record, err := recordFromAcknowledgeRow(row, params.OrgSlug)
	if err != nil {
		return nil, xerror.Wrapf("decode notification record: %w", err)
	}
	return record, nil
}

// ResolveRecord closes one tenant-scoped notification and preserves its first resolution timestamp.
func (r *repository) ResolveRecord(
	ctx context.Context,
	tx postgres.DBTX,
	params ResolveRecordParams,
) (*Record, error) {
	row, err := dbgen.New(tx).ResolveNotificationRecord(
		ctx,
		dbgen.ResolveNotificationRecordParams{
			OrgSlug:    postgres.SlugToText(params.OrgSlug),
			RecordSlug: postgres.SlugToText(params.RecordSlug),
			ResolvedAt: postgres.ToTimestamptz(params.ResolvedAt),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrRecordNotFound.With(err)
		}
		return nil, xerror.Wrapf("resolve notification record: %w", err)
	}

	record, err := recordFromResolveRow(row, params.OrgSlug)
	if err != nil {
		return nil, xerror.Wrapf("decode notification record: %w", err)
	}
	return record, nil
}

// ListDeliveryTargetsForRecord returns enabled email destinations that have not
// already been dispatched for a notification record.
func (r *repository) ListDeliveryTargetsForRecord(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
) ([]DeliveryTarget, error) {
	rows, err := dbgen.New(tx).ListNotificationDeliveryTargetsForRecord(
		ctx,
		dbgen.ListNotificationDeliveryTargetsForRecordParams{
			OrgSlug:    orgSlug,
			RecordSlug: recordSlug,
		},
	)
	if err != nil {
		return nil, xerror.Wrapf("list notification delivery targets: %w", err)
	}

	ret := make([]DeliveryTarget, 0, len(rows))
	for _, row := range rows {
		target, err := deliveryTargetFromRow(row)
		if err != nil {
			return nil, xerror.Wrapf("decode notification delivery target: %w", err)
		}
		ret = append(ret, target)
	}
	return ret, nil
}

// FindDeliveryWork returns the safe record/destination context needed by the
// delivery adapter for one queued job.
func (r *repository) FindDeliveryWork(
	ctx context.Context,
	tx postgres.DBTX,
	params FindDeliveryWorkParams,
) (*DeliveryWork, error) {
	row, err := dbgen.New(tx).FindNotificationDeliveryWork(
		ctx,
		dbgen.FindNotificationDeliveryWorkParams{
			OrgSlug:         params.OrgSlug,
			RecordSlug:      params.RecordSlug,
			DestinationSlug: params.DestinationSlug,
			AlertPolicySlug: params.AlertPolicySlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDeliveryNotFound.With(err)
		}
		return nil, xerror.Wrapf("find notification delivery work: %w", err)
	}

	work, err := deliveryWorkFromRow(row)
	if err != nil {
		return nil, xerror.Wrapf("decode notification delivery work: %w", err)
	}
	return work, nil
}

// CreateAlertDeliveryAttempt records the alert-owned diagnostic row for a queued dispatch.
func (r *repository) CreateAlertDeliveryAttempt(
	ctx context.Context,
	tx postgres.DBTX,
	params CreateAlertDeliveryAttemptParams,
) (*AlertDeliveryAttempt, error) {
	if err := validateAlertDeliveryAttemptStatus(params.Status, model.AlertDeliveryAttemptStatusQueued); err != nil {
		return nil, err
	}
	providerAttempt, err := providerAttemptInt32(params.ProviderAttempt)
	if err != nil {
		return nil, err
	}

	row, err := dbgen.New(tx).CreateAlertDeliveryAttempt(
		ctx,
		dbgen.CreateAlertDeliveryAttemptParams{
			OrgSlug:                  postgres.SlugToText(params.OrgSlug),
			RecordSlug:               postgres.SlugToText(params.NotificationRecordSlug),
			AlertPolicySlug:          postgres.SlugToText(params.AlertPolicySlug),
			DestinationSlug:          postgres.SlugToText(params.DestinationSlug),
			Slug:                     postgres.ToText(params.Slug.String()),
			DestinationType:          postgres.ToText(params.DestinationType.String()),
			Status:                   postgres.ToText(params.Status.String()),
			ProviderAttempt:          pgtype.Int4{Int32: providerAttempt, Valid: true},
			JobKind:                  postgres.ToText(params.JobKind),
			JobQueue:                 postgres.ToText(params.JobQueue),
			JobState:                 postgres.ToText(params.JobState),
			ProviderResponseMetadata: normalizedRawJSON(params.ProviderResponseMetadata),
			QueuedAt:                 postgres.ToTimestamptz(params.QueuedAt),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) || postgres.IsForeignKeyViolation(err) {
			return nil, ErrDeliveryInvalidInput.With(err)
		}
		if postgres.IsUniqueViolation(err, "uq_alert_delivery_attempts_slug") {
			return nil, ErrDeliveryInvalidInput.With(err)
		}
		return nil, xerror.Wrapf("create alert delivery attempt: %w", err)
	}

	attempt, err := alertDeliveryAttemptFromCreateRow(row, params.OrgSlug)
	if err != nil {
		return nil, xerror.Wrapf("decode alert delivery attempt: %w", err)
	}
	return attempt, nil
}

// ListAlertDeliveryAttemptsForRecord returns tenant-scoped delivery diagnostics
// for one notification record.
func (r *repository) ListAlertDeliveryAttemptsForRecord(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
	req model.ListRequest,
) ([]*AlertDeliveryAttempt, error) {
	cursorSlug, cursorTimestamp, _, err := postgres.BuildCursorValues(req.Cursor, model.ListSortCreatedAt)
	if err != nil {
		return nil, err
	}

	rows, err := dbgen.New(tx).ListAlertDeliveryAttemptsForRecord(
		ctx,
		dbgen.ListAlertDeliveryAttemptsForRecordParams{
			OrgSlug:         orgSlug,
			RecordSlug:      recordSlug,
			CursorSlug:      cursorSlug,
			CursorTimestamp: cursorTimestamp,
			OrderDir:        postgres.ToText(req.Order),
			LimitCount:      int64(req.Limit),
		},
	)
	if err != nil {
		return nil, xerror.Wrapf("list alert delivery attempts: %w", err)
	}

	ret := make([]*AlertDeliveryAttempt, 0, len(rows))
	for _, row := range rows {
		attempt, transformErr := alertDeliveryAttemptFromListRow(row)
		if transformErr != nil {
			return nil, xerror.Wrapf("decode alert delivery attempt: %w", transformErr)
		}
		ret = append(ret, attempt)
	}
	return ret, nil
}

// ListRecentAlertDeliveryAttemptsForRecords returns capped tenant-scoped
// delivery diagnostics for multiple notification records.
func (r *repository) ListRecentAlertDeliveryAttemptsForRecords(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	recordSlugs []slug.Slug,
	limitPerRecord int,
) ([]*AlertDeliveryAttempt, error) {
	if len(recordSlugs) == 0 || limitPerRecord <= 0 {
		return []*AlertDeliveryAttempt{}, nil
	}
	if limitPerRecord > math.MaxInt32 {
		return nil, ErrDeliveryInvalidInput
	}
	// #nosec G115 -- limitPerRecord is bounded above before conversion.
	limitPerRecord32 := int32(limitPerRecord)

	rows, err := dbgen.New(tx).ListRecentAlertDeliveryAttemptsForRecords(
		ctx,
		dbgen.ListRecentAlertDeliveryAttemptsForRecordsParams{
			OrgSlug:        orgSlug,
			RecordSlugs:    slugStrings(recordSlugs),
			LimitPerRecord: limitPerRecord32,
		},
	)
	if err != nil {
		return nil, xerror.Wrapf("list recent alert delivery attempts: %w", err)
	}

	ret := make([]*AlertDeliveryAttempt, 0, len(rows))
	for _, row := range rows {
		attempt, transformErr := alertDeliveryAttemptFromRecentRow(row)
		if transformErr != nil {
			return nil, xerror.Wrapf("decode recent alert delivery attempt: %w", transformErr)
		}
		ret = append(ret, attempt)
	}
	return ret, nil
}

// MarkAlertDeliveryAttemptSent records successful provider execution for one attempt.
func (r *repository) MarkAlertDeliveryAttemptSent(
	ctx context.Context,
	tx postgres.DBTX,
	params MarkAlertDeliveryAttemptSentParams,
) (*AlertDeliveryAttempt, error) {
	providerAttempt, err := providerAttemptInt32(params.ProviderAttempt)
	if err != nil {
		return nil, err
	}

	row, err := dbgen.New(tx).MarkAlertDeliveryAttemptSent(
		ctx,
		dbgen.MarkAlertDeliveryAttemptSentParams{
			OrgSlug:                  postgres.SlugToText(params.OrgSlug),
			AttemptSlug:              postgres.SlugToText(params.AttemptSlug),
			ProviderAttempt:          pgtype.Int4{Int32: providerAttempt, Valid: true},
			JobState:                 postgres.ToText(params.JobState),
			ProviderResponseMetadata: normalizedRawJSON(params.ProviderResponseMetadata),
			SentAt:                   postgres.ToTimestamptz(params.SentAt),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDeliveryNotFound.With(err)
		}
		return nil, xerror.Wrapf("mark alert delivery attempt sent: %w", err)
	}

	attempt, err := alertDeliveryAttemptFromSentRow(row)
	if err != nil {
		return nil, xerror.Wrapf("decode sent alert delivery attempt: %w", err)
	}
	return attempt, nil
}

// MarkAlertDeliveryAttemptFailed records failed provider execution for one attempt.
func (r *repository) MarkAlertDeliveryAttemptFailed(
	ctx context.Context,
	tx postgres.DBTX,
	params MarkAlertDeliveryAttemptFailedParams,
) (*AlertDeliveryAttempt, error) {
	providerAttempt, err := providerAttemptInt32(params.ProviderAttempt)
	if err != nil {
		return nil, err
	}

	row, err := dbgen.New(tx).MarkAlertDeliveryAttemptFailed(
		ctx,
		dbgen.MarkAlertDeliveryAttemptFailedParams{
			OrgSlug:                  postgres.SlugToText(params.OrgSlug),
			AttemptSlug:              postgres.SlugToText(params.AttemptSlug),
			ProviderAttempt:          pgtype.Int4{Int32: providerAttempt, Valid: true},
			JobState:                 postgres.ToText(params.JobState),
			ProviderResponseMetadata: normalizedRawJSON(params.ProviderResponseMetadata),
			ErrorCategory:            optionalStringToText(params.ErrorCategory),
			ErrorMessage:             optionalStringToText(params.ErrorMessage),
			FailedAt:                 postgres.ToTimestamptz(params.FailedAt),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDeliveryNotFound.With(err)
		}
		return nil, xerror.Wrapf("mark alert delivery attempt failed: %w", err)
	}

	attempt, err := alertDeliveryAttemptFromFailedRow(row)
	if err != nil {
		return nil, xerror.Wrapf("decode failed alert delivery attempt: %w", err)
	}
	return attempt, nil
}

// UpdateRecordDispatchSummary updates the renderable per-destination dispatch
// snapshot stored on the notification record.
func (r *repository) UpdateRecordDispatchSummary(
	ctx context.Context,
	tx postgres.DBTX,
	params UpdateRecordDispatchSummaryParams,
) (*Record, error) {
	if err := validateDispatchSummaryStatus(params.Status); err != nil {
		return nil, err
	}

	row, err := dbgen.New(tx).UpdateNotificationRecordDispatchSummary(
		ctx,
		dbgen.UpdateNotificationRecordDispatchSummaryParams{
			OrgSlug:         postgres.SlugToText(params.OrgSlug),
			DestinationSlug: postgres.SlugToText(params.DestinationSlug),
			Status:          postgres.ToText(params.Status.String()),
			QueuedAt:        postgres.ToTimestamptz(params.QueuedAt),
			JobKind:         postgres.ToText(params.JobKind),
			JobQueue:        postgres.ToText(params.JobQueue),
			JobState:        postgres.ToText(params.JobState),
			RecordSlug:      postgres.SlugToText(params.RecordSlug),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrRecordNotFound.With(err)
		}
		return nil, xerror.Wrapf("update notification dispatch summary: %w", err)
	}

	record, err := recordFromDispatchSummaryRow(row, params.OrgSlug)
	if err != nil {
		return nil, xerror.Wrapf("decode notification dispatch summary record: %w", err)
	}
	return record, nil
}

func validateDispatchSummaryStatus(status model.NotificationDispatchStatus) error {
	if status != model.NotificationDispatchStatusQueued {
		return ErrDeliveryInvalidInput
	}
	return nil
}

// validateAlertDeliveryAttemptStatus enforces the repository-side status transition contract.
func validateAlertDeliveryAttemptStatus(status, allowed model.AlertDeliveryAttemptStatus) error {
	if status != allowed {
		return ErrDeliveryInvalidInput
	}
	return nil
}

// slugStrings converts typed slugs into SQL array parameters.
func slugStrings(values []slug.Slug) []string {
	ret := make([]string, 0, len(values))
	for _, value := range values {
		ret = append(ret, value.String())
	}
	return ret
}

// providerAttemptInt32 validates provider attempt values before storing them in an int4 column.
func providerAttemptInt32(value int) (int32, error) {
	if value < 1 || value > math.MaxInt32 {
		return 0, ErrDeliveryInvalidInput.Detail("provider attempt must fit PostgreSQL int4")
	}
	return int32(value), nil
}

// normalizedRawJSON returns an empty object when optional metadata is absent.
func normalizedRawJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

// optionalStringToText maps nullable strings into pgx text values.
func optionalStringToText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return postgres.ToText(*value)
}

// optionalEventEntityTypeToText maps optional event entity selectors into nullable SQL text.
func optionalEventEntityTypeToText(value *model.EventEntityType) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return postgres.ToText(value.String())
}

// optionalEventActionToText maps optional event action selectors into nullable SQL text.
func optionalEventActionToText(value *model.EventAction) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return postgres.ToText(value.String())
}

// optionalNotificationTypeToText maps optional notification type selectors into nullable SQL text.
func optionalNotificationTypeToText(value *model.NotificationType) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return postgres.ToText(value.String())
}

// optionalNotificationSeverityToText maps optional severity selectors into nullable SQL text.
func optionalNotificationSeverityToText(value *model.NotificationSeverity) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return postgres.ToText(value.String())
}

// optionalNotificationLifecycleStateToText maps optional lifecycle selectors into nullable SQL text.
func optionalNotificationLifecycleStateToText(value *model.NotificationLifecycleState) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return postgres.ToText(value.String())
}
