package notificationsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

var errUnexpectedNotificationsRepoCall = fmt.Errorf("unexpected notifications repository call")

func testSlugPtr(value slug.Slug) *slug.Slug {
	return &value
}

func testStringPtr(value string) *string {
	return &value
}

func testNotificationTypePtr(value model.NotificationType) *model.NotificationType {
	return &value
}

func testNotificationSeverityPtr(value model.NotificationSeverity) *model.NotificationSeverity {
	return &value
}

type fakeNotificationsRepo struct {
	claimConnectionDeliveryFn  func(context.Context, postgres.DBTX, reponotifications.ClaimConnectionDeliveryParams) (reponotifications.ConnectionDeliveryClaim, error)
	finishConnectionDeliveryFn func(context.Context, postgres.DBTX, reponotifications.FinishConnectionDeliveryParams) error
	createDestinationFn        func(
		ctx context.Context,
		tx postgres.DBTX,
		params reponotifications.CreateAlertDestinationParams,
	) (*reponotifications.AlertDestination, error)
	listAlertDestinationsByOrganizationFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		req model.ListRequest,
	) ([]*reponotifications.AlertDestination, error)
	findAlertDestinationBySlugFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		destinationSlug slug.Slug,
	) (*reponotifications.AlertDestination, error)
	updateAlertDestinationFn func(
		ctx context.Context,
		tx postgres.DBTX,
		params reponotifications.UpdateAlertDestinationParams,
	) (*reponotifications.AlertDestination, error)
	deleteAlertDestinationFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		destinationSlug slug.Slug,
	) error
	createAlertPolicyFn func(
		ctx context.Context,
		tx postgres.DBTX,
		params reponotifications.CreateAlertPolicyParams,
	) (*reponotifications.AlertPolicy, error)
	listAlertPoliciesByOrganizationFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		req model.ListRequest,
	) ([]*reponotifications.AlertPolicy, error)
	findAlertPolicyBySlugFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		policySlug slug.Slug,
	) (*reponotifications.AlertPolicy, error)
	updateAlertPolicyFn func(
		ctx context.Context,
		tx postgres.DBTX,
		params reponotifications.UpdateAlertPolicyParams,
	) (*reponotifications.AlertPolicy, error)
	clearAlertPolicyDestinationsFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		policySlug slug.Slug,
	) error
	bindAlertPolicyDestinationFn func(
		ctx context.Context,
		tx postgres.DBTX,
		params reponotifications.BindAlertPolicyDestinationParams,
	) error
	deleteAlertPolicyFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		policySlug slug.Slug,
	) error
	upsertRecordFn func(
		ctx context.Context,
		tx postgres.DBTX,
		params reponotifications.UpsertRecordParams,
	) (*reponotifications.Record, error)
	listRecordsByOrganizationFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		filters reponotifications.ListRecordFilters,
		req model.ListRequest,
	) ([]*reponotifications.Record, error)
	findRecordBySlugFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		recordSlug slug.Slug,
	) (*reponotifications.Record, error)
	acknowledgeRecordFn func(
		ctx context.Context,
		tx postgres.DBTX,
		params reponotifications.AcknowledgeRecordParams,
	) (*reponotifications.Record, error)
	resolveRecordFn func(
		ctx context.Context,
		tx postgres.DBTX,
		params reponotifications.ResolveRecordParams,
	) (*reponotifications.Record, error)
	listDeliveryTargetsForRecordFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		recordSlug slug.Slug,
	) ([]reponotifications.DeliveryTarget, error)
	findDeliveryWorkFn func(
		ctx context.Context,
		tx postgres.DBTX,
		params reponotifications.FindDeliveryWorkParams,
	) (*reponotifications.DeliveryWork, error)
	createAlertDeliveryAttemptFn func(
		ctx context.Context,
		tx postgres.DBTX,
		params reponotifications.CreateAlertDeliveryAttemptParams,
	) (*reponotifications.AlertDeliveryAttempt, error)
	listAlertDeliveryAttemptsForRecordFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		recordSlug slug.Slug,
		req model.ListRequest,
	) ([]*reponotifications.AlertDeliveryAttempt, error)
	listRecentAlertDeliveryAttemptsForRecordsFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		recordSlugs []slug.Slug,
		limitPerRecord int,
	) ([]*reponotifications.AlertDeliveryAttempt, error)
	markAlertDeliveryAttemptSentFn func(
		ctx context.Context,
		tx postgres.DBTX,
		params reponotifications.MarkAlertDeliveryAttemptSentParams,
	) (*reponotifications.AlertDeliveryAttempt, error)
	markAlertDeliveryAttemptFailedFn func(
		ctx context.Context,
		tx postgres.DBTX,
		params reponotifications.MarkAlertDeliveryAttemptFailedParams,
	) (*reponotifications.AlertDeliveryAttempt, error)
	updateRecordDispatchSummaryFn func(
		ctx context.Context,
		tx postgres.DBTX,
		params reponotifications.UpdateRecordDispatchSummaryParams,
	) (*reponotifications.Record, error)
}

type fakeNotificationDeliveryProducer struct {
	enqueueFn func(
		ctx context.Context,
		tx postgres.DBTX,
		params NotificationDeliveryJobParams,
	) (queue.QueuedJob, error)
	calls []NotificationDeliveryJobParams
}

type fakeNotificationEventRecorder struct {
	calls       []eventssvc.CreateEventParams
	requiredErr error
}

func (f *fakeNotificationEventRecorder) EnqueueRecord(
	_ context.Context,
	_ postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) {
	params, err := eventrecorder.ParamsFor(orgSlug, actor, event)
	if err != nil {
		return
	}
	f.calls = append(f.calls, params)
}

func (f *fakeNotificationEventRecorder) EnqueueRequired(
	_ context.Context,
	_ postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) error {
	params, err := eventrecorder.ParamsFor(orgSlug, actor, event)
	if err != nil {
		return err
	}
	f.calls = append(f.calls, params)
	return f.requiredErr
}

func (f *fakeNotificationsRepo) CreateAlertDestination(
	ctx context.Context,
	tx postgres.DBTX,
	params reponotifications.CreateAlertDestinationParams,
) (*reponotifications.AlertDestination, error) {
	if f.createDestinationFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.createDestinationFn(ctx, tx, params)
}

func (f *fakeNotificationsRepo) ListAlertDestinationsByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListRequest,
) ([]*reponotifications.AlertDestination, error) {
	if f.listAlertDestinationsByOrganizationFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.listAlertDestinationsByOrganizationFn(ctx, tx, orgSlug, req)
}

func (f *fakeNotificationsRepo) FindAlertDestinationBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	destinationSlug slug.Slug,
) (*reponotifications.AlertDestination, error) {
	if f.findAlertDestinationBySlugFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.findAlertDestinationBySlugFn(ctx, tx, orgSlug, destinationSlug)
}

func (f *fakeNotificationsRepo) UpdateAlertDestination(
	ctx context.Context,
	tx postgres.DBTX,
	params reponotifications.UpdateAlertDestinationParams,
) (*reponotifications.AlertDestination, error) {
	if f.updateAlertDestinationFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.updateAlertDestinationFn(ctx, tx, params)
}

func (f *fakeNotificationsRepo) DeleteAlertDestination(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	destinationSlug slug.Slug,
) error {
	if f.deleteAlertDestinationFn == nil {
		return errUnexpectedNotificationsRepoCall
	}
	return f.deleteAlertDestinationFn(ctx, tx, orgSlug, destinationSlug)
}

func (f *fakeNotificationsRepo) CreateAlertPolicy(
	ctx context.Context,
	tx postgres.DBTX,
	params reponotifications.CreateAlertPolicyParams,
) (*reponotifications.AlertPolicy, error) {
	if f.createAlertPolicyFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.createAlertPolicyFn(ctx, tx, params)
}

func (f *fakeNotificationsRepo) ListAlertPoliciesByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListRequest,
) ([]*reponotifications.AlertPolicy, error) {
	if f.listAlertPoliciesByOrganizationFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.listAlertPoliciesByOrganizationFn(ctx, tx, orgSlug, req)
}

func (f *fakeNotificationsRepo) FindAlertPolicyBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	policySlug slug.Slug,
) (*reponotifications.AlertPolicy, error) {
	if f.findAlertPolicyBySlugFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.findAlertPolicyBySlugFn(ctx, tx, orgSlug, policySlug)
}

func (f *fakeNotificationsRepo) UpdateAlertPolicy(
	ctx context.Context,
	tx postgres.DBTX,
	params reponotifications.UpdateAlertPolicyParams,
) (*reponotifications.AlertPolicy, error) {
	if f.updateAlertPolicyFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.updateAlertPolicyFn(ctx, tx, params)
}

func (f *fakeNotificationsRepo) ClearAlertPolicyDestinations(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	policySlug slug.Slug,
) error {
	if f.clearAlertPolicyDestinationsFn == nil {
		return errUnexpectedNotificationsRepoCall
	}
	return f.clearAlertPolicyDestinationsFn(ctx, tx, orgSlug, policySlug)
}

func (f *fakeNotificationsRepo) BindAlertPolicyDestination(
	ctx context.Context,
	tx postgres.DBTX,
	params reponotifications.BindAlertPolicyDestinationParams,
) error {
	if f.bindAlertPolicyDestinationFn == nil {
		return errUnexpectedNotificationsRepoCall
	}
	return f.bindAlertPolicyDestinationFn(ctx, tx, params)
}

func (f *fakeNotificationsRepo) DeleteAlertPolicy(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	policySlug slug.Slug,
) error {
	if f.deleteAlertPolicyFn == nil {
		return errUnexpectedNotificationsRepoCall
	}
	return f.deleteAlertPolicyFn(ctx, tx, orgSlug, policySlug)
}

func (f *fakeNotificationsRepo) UpsertRecord(
	ctx context.Context,
	tx postgres.DBTX,
	params reponotifications.UpsertRecordParams,
) (*reponotifications.Record, error) {
	if f.upsertRecordFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.upsertRecordFn(ctx, tx, params)
}

func (f *fakeNotificationsRepo) ListRecordsByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	filters reponotifications.ListRecordFilters,
	req model.ListRequest,
) ([]*reponotifications.Record, error) {
	if f.listRecordsByOrganizationFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.listRecordsByOrganizationFn(ctx, tx, orgSlug, filters, req)
}

func (f *fakeNotificationsRepo) ListDeliveryTargetsForRecord(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
) ([]reponotifications.DeliveryTarget, error) {
	if f.listDeliveryTargetsForRecordFn == nil {
		return nil, nil
	}
	return f.listDeliveryTargetsForRecordFn(ctx, tx, orgSlug, recordSlug)
}

func (f *fakeNotificationsRepo) FindRecordBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
) (*reponotifications.Record, error) {
	if f.findRecordBySlugFn == nil {
		return nil, ErrRecordNotFound
	}
	return f.findRecordBySlugFn(ctx, tx, orgSlug, recordSlug)
}

func (f *fakeNotificationsRepo) AcknowledgeRecord(
	ctx context.Context,
	tx postgres.DBTX,
	params reponotifications.AcknowledgeRecordParams,
) (*reponotifications.Record, error) {
	if f.acknowledgeRecordFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.acknowledgeRecordFn(ctx, tx, params)
}

func (f *fakeNotificationsRepo) ResolveRecord(
	ctx context.Context,
	tx postgres.DBTX,
	params reponotifications.ResolveRecordParams,
) (*reponotifications.Record, error) {
	if f.resolveRecordFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.resolveRecordFn(ctx, tx, params)
}

func (f *fakeNotificationsRepo) FindDeliveryWork(
	ctx context.Context,
	tx postgres.DBTX,
	params reponotifications.FindDeliveryWorkParams,
) (*reponotifications.DeliveryWork, error) {
	if f.findDeliveryWorkFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.findDeliveryWorkFn(ctx, tx, params)
}

func (f *fakeNotificationsRepo) CreateAlertDeliveryAttempt(
	ctx context.Context,
	tx postgres.DBTX,
	params reponotifications.CreateAlertDeliveryAttemptParams,
) (*reponotifications.AlertDeliveryAttempt, error) {
	if f.createAlertDeliveryAttemptFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.createAlertDeliveryAttemptFn(ctx, tx, params)
}

func (f *fakeNotificationsRepo) ListAlertDeliveryAttemptsForRecord(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
	req model.ListRequest,
) ([]*reponotifications.AlertDeliveryAttempt, error) {
	if f.listAlertDeliveryAttemptsForRecordFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.listAlertDeliveryAttemptsForRecordFn(ctx, tx, orgSlug, recordSlug, req)
}

func (f *fakeNotificationsRepo) ListRecentAlertDeliveryAttemptsForRecords(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	recordSlugs []slug.Slug,
	limitPerRecord int,
) ([]*reponotifications.AlertDeliveryAttempt, error) {
	if f.listRecentAlertDeliveryAttemptsForRecordsFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.listRecentAlertDeliveryAttemptsForRecordsFn(ctx, tx, orgSlug, recordSlugs, limitPerRecord)
}

func (f *fakeNotificationsRepo) MarkAlertDeliveryAttemptSent(
	ctx context.Context,
	tx postgres.DBTX,
	params reponotifications.MarkAlertDeliveryAttemptSentParams,
) (*reponotifications.AlertDeliveryAttempt, error) {
	if f.markAlertDeliveryAttemptSentFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.markAlertDeliveryAttemptSentFn(ctx, tx, params)
}

func (f *fakeNotificationsRepo) MarkAlertDeliveryAttemptFailed(
	ctx context.Context,
	tx postgres.DBTX,
	params reponotifications.MarkAlertDeliveryAttemptFailedParams,
) (*reponotifications.AlertDeliveryAttempt, error) {
	if f.markAlertDeliveryAttemptFailedFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.markAlertDeliveryAttemptFailedFn(ctx, tx, params)
}

func (f *fakeNotificationsRepo) UpdateRecordDispatchSummary(
	ctx context.Context,
	tx postgres.DBTX,
	params reponotifications.UpdateRecordDispatchSummaryParams,
) (*reponotifications.Record, error) {
	if f.updateRecordDispatchSummaryFn == nil {
		return nil, errUnexpectedNotificationsRepoCall
	}
	return f.updateRecordDispatchSummaryFn(ctx, tx, params)
}

func (f *fakeNotificationDeliveryProducer) EnqueueNotificationDelivery(
	ctx context.Context,
	tx postgres.DBTX,
	params NotificationDeliveryJobParams,
) (queue.QueuedJob, error) {
	f.calls = append(f.calls, params)
	if f.enqueueFn != nil {
		return f.enqueueFn(ctx, tx, params)
	}
	return queue.QueuedJob{Kind: "notification_delivery", Queue: "notification_delivery", State: "queued"}, nil
}

func newTestService(repo *fakeNotificationsRepo) Service {
	return newTestServiceWithDeps(repo, testutil.FakeClock{}, &fakeNotificationDeliveryProducer{}, &testutil.FakeEmailProducer{})
}

func newTestServiceWithClock(repo *fakeNotificationsRepo, clk testutil.FakeClock) Service {
	return newTestServiceWithDeps(repo, clk, &fakeNotificationDeliveryProducer{}, &testutil.FakeEmailProducer{})
}

func newTestServiceWithDeliveryProducer(
	repo *fakeNotificationsRepo,
	clk testutil.FakeClock,
	producer *fakeNotificationDeliveryProducer,
) Service {
	return newTestServiceWithDeps(repo, clk, producer, &testutil.FakeEmailProducer{})
}

func newTestServiceWithEmailProducer(
	repo *fakeNotificationsRepo,
	emailProducer NotificationEmailProducer,
) Service {
	return newTestServiceWithDeps(repo, testutil.FakeClock{}, &fakeNotificationDeliveryProducer{}, emailProducer)
}

func newTestServiceWithDeps(
	repo *fakeNotificationsRepo,
	clk testutil.FakeClock,
	deliveryProducer NotificationDeliveryProducer,
	emailProducer NotificationEmailProducer,
) Service {
	return newTestServiceWithEventProducer(
		repo,
		clk,
		deliveryProducer,
		emailProducer,
		LifecycleEventProducerFunc(func(context.Context, postgres.DBTX, slug.Slug, model.EventActor, eventrecorder.Event) {}),
	)
}

func newTestServiceWithEventProducer(
	repo *fakeNotificationsRepo,
	clk testutil.FakeClock,
	deliveryProducer NotificationDeliveryProducer,
	emailProducer NotificationEmailProducer,
	eventProducer LifecycleEventProducer,
) Service {
	return New(testutil.FakeTransactionManager{}, repo, clk, deliveryProducer, emailProducer, eventProducer, ConnectionProviders{}, "https://app.example")
}

func testAlertPrincipal() AlertManagementPrincipal {
	return AlertManagementPrincipal{UserSlug: must.Slug("alice")}
}

func TestAlertManagementSecurityEventContracts(t *testing.T) {
	t.Parallel()

	destinationSlug := must.Slug("ops-email")
	policySlug := must.Slug("critical-failures")

	tests := []struct {
		name         string
		event        eventrecorder.Event
		wantPrefix   slug.Slug
		wantEntity   model.EventEntityType
		wantSlug     slug.Slug
		wantAction   model.EventAction
		wantMetadata map[string]any
	}{
		{
			name: "DestinationCreate",
			event: alertDestinationSecurityEvent{
				destinationSlug: destinationSlug,
				action:          model.EventActionCreate,
				destinationType: model.AlertDestinationTypeEmail,
				enabled:         true,
			},
			wantPrefix: "evt-alert-destination-create",
			wantEntity: model.EventEntityTypeAlertDestination,
			wantSlug:   destinationSlug,
			wantAction: model.EventActionCreate,
			wantMetadata: map[string]any{
				"type":    "email",
				"enabled": true,
			},
		},
		{
			name: "DestinationUpdate",
			event: alertDestinationSecurityEvent{
				destinationSlug: destinationSlug,
				action:          model.EventActionUpdate,
				destinationType: model.AlertDestinationTypeEmail,
				enabled:         false,
			},
			wantPrefix: "evt-alert-destination-update",
			wantEntity: model.EventEntityTypeAlertDestination,
			wantSlug:   destinationSlug,
			wantAction: model.EventActionUpdate,
			wantMetadata: map[string]any{
				"type":    "email",
				"enabled": false,
			},
		},
		{
			name: "DestinationDelete",
			event: alertDestinationSecurityEvent{
				destinationSlug: destinationSlug,
				action:          model.EventActionDelete,
				destinationType: model.AlertDestinationTypeEmail,
				enabled:         false,
			},
			wantPrefix: "evt-alert-destination-delete",
			wantEntity: model.EventEntityTypeAlertDestination,
			wantSlug:   destinationSlug,
			wantAction: model.EventActionDelete,
			wantMetadata: map[string]any{
				"type":    "email",
				"enabled": false,
			},
		},
		{
			name: "PolicyCreate",
			event: alertPolicySecurityEvent{
				policySlug:       policySlug,
				action:           model.EventActionCreate,
				scope:            model.AlertPolicyScopeOrganization,
				enabled:          true,
				destinationCount: 2,
			},
			wantPrefix: "evt-alert-policy-create",
			wantEntity: model.EventEntityTypeAlertPolicy,
			wantSlug:   policySlug,
			wantAction: model.EventActionCreate,
			wantMetadata: map[string]any{
				"scope":             "organization",
				"enabled":           true,
				"destination_count": 2,
			},
		},
		{
			name: "PolicyUpdate",
			event: alertPolicySecurityEvent{
				policySlug:       policySlug,
				action:           model.EventActionUpdate,
				scope:            model.AlertPolicyScopeProject,
				enabled:          false,
				destinationCount: 1,
			},
			wantPrefix: "evt-alert-policy-update",
			wantEntity: model.EventEntityTypeAlertPolicy,
			wantSlug:   policySlug,
			wantAction: model.EventActionUpdate,
			wantMetadata: map[string]any{
				"scope":             "project",
				"enabled":           false,
				"destination_count": 1,
			},
		},
		{
			name: "PolicyDelete",
			event: alertPolicySecurityEvent{
				policySlug:       policySlug,
				action:           model.EventActionDelete,
				scope:            model.AlertPolicyScopeProjectEnvironment,
				enabled:          false,
				destinationCount: 0,
			},
			wantPrefix: "evt-alert-policy-delete",
			wantEntity: model.EventEntityTypeAlertPolicy,
			wantSlug:   policySlug,
			wantAction: model.EventActionDelete,
			wantMetadata: map[string]any{
				"scope":             "project_environment",
				"enabled":           false,
				"destination_count": 0,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			record, err := tc.event.EventRecord()
			require.NoError(t, err)
			require.Equal(t, tc.wantPrefix, record.SlugPrefix)
			require.Equal(t, model.EventClassSecurityAudit, record.EventClass)
			require.Equal(t, model.CurrentEventSchemaVersion, record.SchemaVersion)
			require.Equal(t, tc.wantEntity, record.EntityType)
			require.Equal(t, tc.wantSlug, record.EntitySlug)
			require.Equal(t, tc.wantAction, record.Action)
			require.Equal(t, tc.wantMetadata, record.Metadata)
			require.NotContains(t, record.Metadata, "config")
			require.NotContains(t, record.Metadata, "recipients")
		})
	}
}

func TestService_NewPanicsForMissingRequiredDependencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func()
	}{
		{
			name: "NilTransactionManager",
			run: func() {
				New(nil, &fakeNotificationsRepo{}, testutil.FakeClock{}, &fakeNotificationDeliveryProducer{}, &testutil.FakeEmailProducer{}, &fakeNotificationEventRecorder{}, ConnectionProviders{}, "https://app.example")
			},
		},
		{
			name: "NilRepository",
			run: func() {
				New(testutil.FakeTransactionManager{}, nil, testutil.FakeClock{}, &fakeNotificationDeliveryProducer{}, &testutil.FakeEmailProducer{}, &fakeNotificationEventRecorder{}, ConnectionProviders{}, "https://app.example")
			},
		},
		{
			name: "NilClock",
			run: func() {
				New(testutil.FakeTransactionManager{}, &fakeNotificationsRepo{}, nil, &fakeNotificationDeliveryProducer{}, &testutil.FakeEmailProducer{}, &fakeNotificationEventRecorder{}, ConnectionProviders{}, "https://app.example")
			},
		},
		{
			name: "NilDeliveryProducer",
			run: func() {
				New(testutil.FakeTransactionManager{}, &fakeNotificationsRepo{}, testutil.FakeClock{}, nil, &testutil.FakeEmailProducer{}, &fakeNotificationEventRecorder{}, ConnectionProviders{}, "https://app.example")
			},
		},
		{
			name: "NilEmailProducer",
			run: func() {
				New(testutil.FakeTransactionManager{}, &fakeNotificationsRepo{}, testutil.FakeClock{}, &fakeNotificationDeliveryProducer{}, nil, &fakeNotificationEventRecorder{}, ConnectionProviders{}, "https://app.example")
			},
		},
		{
			name: "NilEventProducer",
			run: func() {
				New(testutil.FakeTransactionManager{}, &fakeNotificationsRepo{}, testutil.FakeClock{}, &fakeNotificationDeliveryProducer{}, &testutil.FakeEmailProducer{}, nil, ConnectionProviders{}, "https://app.example")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Panics(t, tc.run)
		})
	}
}

func TestService_ListNotificationsReturnsDisplayProjection(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	recordSlug := must.Slug("notif-pipeline-run-failed")
	eventSlug := must.Slug("evt-run-failed")
	destinationSlug := must.Slug("ops-email")
	now := time.Date(2026, 6, 30, 10, 15, 0, 0, time.UTC)

	svc := newTestService(&fakeNotificationsRepo{
		listRecordsByOrganizationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			filters reponotifications.ListRecordFilters,
			req model.ListRequest,
		) ([]*reponotifications.Record, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Nil(t, filters.LifecycleState)
			require.False(t, filters.IncludeResolved)
			require.Equal(t, model.ListRequest{
				Limit:     2,
				SortBy:    model.ListSortLastSeenAt,
				Order:     model.ListOrderDesc,
				Direction: model.ListDirectionNext,
			}, req)

			return []*reponotifications.Record{
				{
					OrgSlug:          orgSlug,
					Slug:             recordSlug,
					NotificationType: model.NotificationTypePipelineRunFailed,
					Severity:         model.NotificationSeverityCritical,
					LifecycleState:   model.NotificationLifecycleStateOpen,
					SourceEventSlug:  &eventSlug,
					SourceEntityType: model.EventEntityTypePipelineRun,
					SourceAction:     model.EventActionExecute,
					SourceRefs: json.RawMessage(
						`{"project_slug":"ops","environment_slug":"prod","run_slug":"run-001"}`,
					),
					SafeMetadata: json.RawMessage(
						`{"summary":"Pipeline users-sync failed in prod","step":"load-users"}`,
					),
					FirstSeenAt:      now.Add(-2 * time.Hour),
					LastSeenAt:       now,
					OccurrenceCount:  3,
					LastOccurrenceAt: now.Add(-5 * time.Minute),
					DispatchSummary: json.RawMessage(
						`{"ops-email":{"status":"queued","queued_at":"2026-06-30T10:16:00Z","job_kind":"notification_delivery","job_queue":"notification_delivery","job_state":"queued"}}`,
					),
					CreatedAt: now.Add(-2 * time.Hour),
					UpdatedAt: now,
				},
				{
					OrgSlug:          orgSlug,
					Slug:             must.Slug("notif-extra"),
					NotificationType: model.NotificationTypePipelineRunFailed,
					Severity:         model.NotificationSeverityWarning,
					LifecycleState:   model.NotificationLifecycleStateOpen,
					SourceEntityType: model.EventEntityTypePipelineRun,
					SourceAction:     model.EventActionExecute,
					SourceRefs:       json.RawMessage(`{}`),
					SafeMetadata:     json.RawMessage(`{}`),
					FirstSeenAt:      now.Add(-3 * time.Hour),
					LastSeenAt:       now.Add(-1 * time.Hour),
					OccurrenceCount:  1,
					LastOccurrenceAt: now.Add(-1 * time.Hour),
					DispatchSummary: json.RawMessage(
						`{}`,
					),
					CreatedAt: now.Add(-3 * time.Hour),
					UpdatedAt: now.Add(-1 * time.Hour),
				},
			}, nil
		},
	})

	got, err := svc.ListNotifications(context.Background(), orgSlug, ListNotificationsFilters{}, model.ListRequest{Limit: 1})
	require.NoError(t, err)
	require.True(t, got.HasMore)
	require.NotNil(t, got.NextCursor)
	require.Contains(t, *got.NextCursor, recordSlug.String())
	require.Len(t, got.Items, 1)

	item := got.Items[0]
	require.Equal(t, recordSlug, item.Slug)
	require.Equal(t, "Pipeline users-sync failed in prod", item.Summary)
	require.Equal(t, map[string]string{
		"project_slug":     "ops",
		"environment_slug": "prod",
		"run_slug":         "run-001",
	}, item.SourceRefs)
	require.Equal(t, 3, item.OccurrenceCount)
	require.Len(t, item.DispatchStatuses, 1)
	require.Equal(t, destinationSlug, item.DispatchStatuses[0].DestinationSlug)
	require.Equal(t, model.NotificationDispatchStatusQueued, item.DispatchStatuses[0].Status)
	require.NotNil(t, item.DispatchStatuses[0].QueuedAt)
}

func TestService_ListNotificationsForwardsSourceFilters(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	recordSlug := must.Slug("notif-pipeline-run-failed")
	sourceKind := model.EventEntityTypePipelineRun
	sourceAction := model.EventActionExecute
	sourceRef := must.Slug("users-sync")
	now := time.Date(2026, 7, 3, 10, 15, 0, 0, time.UTC)

	svc := newTestService(&fakeNotificationsRepo{
		listRecordsByOrganizationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			filters reponotifications.ListRecordFilters,
			req model.ListRequest,
		) ([]*reponotifications.Record, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, &sourceKind, filters.SourceKind)
			require.Equal(t, &sourceAction, filters.SourceAction)
			require.Equal(t, &sourceRef, filters.SourceRef)
			require.False(t, filters.IncludeResolved)
			require.Equal(t, model.ListRequest{
				Limit:     2,
				SortBy:    model.ListSortLastSeenAt,
				Order:     model.ListOrderDesc,
				Direction: model.ListDirectionNext,
			}, req)

			return []*reponotifications.Record{{
				OrgSlug:          orgSlug,
				Slug:             recordSlug,
				NotificationType: model.NotificationTypePipelineRunFailed,
				Severity:         model.NotificationSeverityCritical,
				LifecycleState:   model.NotificationLifecycleStateOpen,
				SourceEntityType: sourceKind,
				SourceAction:     sourceAction,
				SourceRefs:       json.RawMessage(`{"pipeline_slug":"users-sync"}`),
				SafeMetadata:     json.RawMessage(`{"summary":"Pipeline users-sync failed in prod"}`),
				FirstSeenAt:      now.Add(-time.Hour),
				LastSeenAt:       now,
				OccurrenceCount:  1,
				LastOccurrenceAt: now,
				DispatchSummary:  json.RawMessage(`{}`),
				CreatedAt:        now.Add(-time.Hour),
				UpdatedAt:        now,
			}}, nil
		},
	})

	got, err := svc.ListNotifications(
		context.Background(),
		orgSlug,
		ListNotificationsFilters{SourceKind: &sourceKind, SourceAction: &sourceAction, SourceRef: &sourceRef},
		model.ListRequest{Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	require.Equal(t, recordSlug, got.Items[0].Slug)
}

func TestService_GetNotificationReturnsDisplayProjection(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	recordSlug := must.Slug("notif-pipeline-run-failed")
	destinationSlug := must.Slug("ops-email")
	now := time.Date(2026, 7, 3, 10, 15, 0, 0, time.UTC)

	tests := []struct {
		name    string
		record  *reponotifications.Record
		repoErr error
		wantErr error
		assert  func(*testing.T, *model.NotificationListItem)
	}{
		{
			name: "ReturnsDisplayProjection",
			record: &reponotifications.Record{
				OrgSlug:          orgSlug,
				Slug:             recordSlug,
				NotificationType: model.NotificationTypePipelineRunFailed,
				Severity:         model.NotificationSeverityCritical,
				LifecycleState:   model.NotificationLifecycleStateOpen,
				SourceEntityType: model.EventEntityTypePipelineRun,
				SourceAction:     model.EventActionExecute,
				SourceRefs:       json.RawMessage(`{"pipeline_slug":"users-sync"}`),
				SafeMetadata:     json.RawMessage(`{"summary":"Pipeline users-sync failed in prod"}`),
				FirstSeenAt:      now.Add(-time.Hour),
				LastSeenAt:       now,
				OccurrenceCount:  1,
				LastOccurrenceAt: now,
				DispatchSummary:  json.RawMessage(`{"ops-email":{"status":"queued","queued_at":"2026-07-03T10:15:00Z"}}`),
				CreatedAt:        now.Add(-time.Hour),
				UpdatedAt:        now,
			},
			assert: func(t *testing.T, got *model.NotificationListItem) {
				t.Helper()

				require.Equal(t, recordSlug, got.Slug)
				require.Equal(t, "Pipeline users-sync failed in prod", got.Summary)
				require.Equal(t, map[string]string{"pipeline_slug": "users-sync"}, got.SourceRefs)
				require.Len(t, got.DispatchStatuses, 1)
				require.Equal(t, destinationSlug, got.DispatchStatuses[0].DestinationSlug)
				require.Equal(t, model.NotificationDispatchStatusQueued, got.DispatchStatuses[0].Status)
				require.NotNil(t, got.DispatchStatuses[0].QueuedAt)
			},
		},
		{
			name:    "WrapsRepositoryNotFound",
			repoErr: ErrRecordNotFound,
			wantErr: ErrRecordNotFound,
		},
		{
			name:    "WrapsRepositoryError",
			repoErr: xerror.Unexpected().Detail("repository failed"),
			wantErr: xerror.Unexpected(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newTestService(&fakeNotificationsRepo{
				findRecordBySlugFn: func(
					_ context.Context,
					_ postgres.DBTX,
					gotOrgSlug slug.Slug,
					gotRecordSlug slug.Slug,
				) (*reponotifications.Record, error) {
					require.Equal(t, orgSlug, gotOrgSlug)
					require.Equal(t, recordSlug, gotRecordSlug)
					if tt.repoErr != nil {
						return nil, tt.repoErr
					}
					return tt.record, nil
				},
			})

			got, err := svc.GetNotification(context.Background(), orgSlug, recordSlug)
			if tt.wantErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tt.wantErr)
				require.Contains(t, err.Error(), "find notification record")
				require.Nil(t, got)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)
			tt.assert(t, got)
		})
	}
}

func TestService_AcknowledgeNotificationRecordsTimestamp(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	recordSlug := must.Slug("notif-001")
	userSlug := must.Slug("alice")
	destinationSlug := must.Slug("ops-email")
	now := time.Date(2026, 6, 30, 16, 0, 0, 0, time.UTC)
	events := &fakeNotificationEventRecorder{}

	svc := newTestServiceWithEventProducer(
		&fakeNotificationsRepo{
			acknowledgeRecordFn: func(
				_ context.Context,
				_ postgres.DBTX,
				params reponotifications.AcknowledgeRecordParams,
			) (*reponotifications.Record, error) {
				require.Equal(t, orgSlug, params.OrgSlug)
				require.Equal(t, recordSlug, params.RecordSlug)
				require.Equal(t, now, params.AcknowledgedAt)
				return &reponotifications.Record{
					OrgSlug:          orgSlug,
					Slug:             recordSlug,
					NotificationType: model.NotificationTypePipelineRunFailed,
					Severity:         model.NotificationSeverityCritical,
					LifecycleState:   model.NotificationLifecycleStateAcknowledged,
					SourceEntityType: model.EventEntityTypePipelineRun,
					SourceAction:     model.EventActionExecute,
					SourceRefs:       json.RawMessage(`{}`),
					SafeMetadata:     json.RawMessage(`{"summary":"Pipeline users-sync failed in prod"}`),
					FirstSeenAt:      now.Add(-time.Hour),
					LastSeenAt:       now,
					OccurrenceCount:  1,
					LastOccurrenceAt: now,
					DispatchSummary:  json.RawMessage(`{"ops-email":{"status":"queued","queued_at":"2026-06-30T16:00:00Z"}}`),
					AcknowledgedAt:   &now,
					CreatedAt:        now.Add(-time.Hour),
					UpdatedAt:        now,
				}, nil
			},
		},
		testutil.FakeClock{NowFn: func() time.Time { return now }},
		&fakeNotificationDeliveryProducer{},
		&testutil.FakeEmailProducer{},
		events,
	)

	got, err := svc.AcknowledgeNotification(context.Background(), AlertManagementPrincipal{UserSlug: userSlug}, orgSlug, recordSlug)
	require.NoError(t, err)
	require.Equal(t, model.NotificationLifecycleStateAcknowledged, got.LifecycleState)
	require.Equal(t, "Pipeline users-sync failed in prod", got.Summary)
	require.Equal(t, &now, got.AcknowledgedAt)
	require.Len(t, got.DispatchStatuses, 1)
	require.Equal(t, destinationSlug, got.DispatchStatuses[0].DestinationSlug)
	require.Len(t, events.calls, 1)
	gotEvent := events.calls[0]
	require.Equal(t, orgSlug, gotEvent.OrgSlug)
	require.Regexp(t, "^evt-notification-acknowledge-", gotEvent.Slug.String())
	require.Equal(t, model.EventActorTypeUser, gotEvent.ActorType)
	require.NotNil(t, gotEvent.ActorSlug)
	require.Equal(t, userSlug, *gotEvent.ActorSlug)
	require.Equal(t, model.EventEntityTypeNotification, gotEvent.EntityType)
	require.NotNil(t, gotEvent.EntitySlug)
	require.Equal(t, recordSlug, *gotEvent.EntitySlug)
	require.Equal(t, model.EventActionAcknowledge, gotEvent.Action)
	require.Equal(t, now, gotEvent.OccurredAt)
	require.Equal(t, map[string]any{
		"lifecycle_state":   "acknowledged",
		"notification_type": "pipeline.run_failed",
		"severity":          "critical",
	}, gotEvent.Metadata)
}

func TestService_ResolveNotificationRecordsAPIKeyActor(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	recordSlug := must.Slug("notif-001")
	apiKeySlug := must.Slug("ops-automation")
	apiKeyRoleSlug := must.Slug("operator")
	now := time.Date(2026, 6, 30, 17, 0, 0, 0, time.UTC)
	events := &fakeNotificationEventRecorder{}

	svc := newTestServiceWithEventProducer(
		&fakeNotificationsRepo{
			resolveRecordFn: func(
				_ context.Context,
				_ postgres.DBTX,
				params reponotifications.ResolveRecordParams,
			) (*reponotifications.Record, error) {
				require.Equal(t, orgSlug, params.OrgSlug)
				require.Equal(t, recordSlug, params.RecordSlug)
				require.Equal(t, now, params.ResolvedAt)
				return &reponotifications.Record{
					OrgSlug:          orgSlug,
					Slug:             recordSlug,
					NotificationType: model.NotificationTypePipelineRunFailed,
					Severity:         model.NotificationSeverityCritical,
					LifecycleState:   model.NotificationLifecycleStateResolved,
					SourceEntityType: model.EventEntityTypePipelineRun,
					SourceAction:     model.EventActionExecute,
					SourceRefs:       json.RawMessage(`{}`),
					SafeMetadata:     json.RawMessage(`{"summary":"Pipeline users-sync failed in prod"}`),
					FirstSeenAt:      now.Add(-time.Hour),
					LastSeenAt:       now,
					OccurrenceCount:  1,
					LastOccurrenceAt: now,
					DispatchSummary:  json.RawMessage(`{}`),
					ResolvedAt:       &now,
					CreatedAt:        now.Add(-time.Hour),
					UpdatedAt:        now,
				}, nil
			},
		},
		testutil.FakeClock{NowFn: func() time.Time { return now }},
		&fakeNotificationDeliveryProducer{},
		&testutil.FakeEmailProducer{},
		events,
	)

	got, err := svc.ResolveNotification(
		context.Background(),
		AlertManagementPrincipal{
			APIKeySlug:     apiKeySlug,
			APIKeyOrgSlug:  orgSlug,
			APIKeyRoleSlug: apiKeyRoleSlug,
		},
		orgSlug,
		recordSlug,
	)
	require.NoError(t, err)
	require.Equal(t, model.NotificationLifecycleStateResolved, got.LifecycleState)
	require.Equal(t, &now, got.ResolvedAt)
	require.Len(t, events.calls, 1)
	gotEvent := events.calls[0]
	require.Equal(t, orgSlug, gotEvent.OrgSlug)
	require.Regexp(t, "^evt-notification-resolve-", gotEvent.Slug.String())
	require.Equal(t, model.EventActorTypeAPIKey, gotEvent.ActorType)
	require.NotNil(t, gotEvent.ActorSlug)
	require.Equal(t, apiKeySlug, *gotEvent.ActorSlug)
	require.Equal(t, model.EventEntityTypeNotification, gotEvent.EntityType)
	require.NotNil(t, gotEvent.EntitySlug)
	require.Equal(t, recordSlug, *gotEvent.EntitySlug)
	require.Equal(t, model.EventActionResolve, gotEvent.Action)
	require.Equal(t, now, gotEvent.OccurredAt)
	require.Equal(t, map[string]any{
		"lifecycle_state":   "resolved",
		"notification_type": "pipeline.run_failed",
		"severity":          "critical",
	}, gotEvent.Metadata)
}

func TestService_ListAlertDeliveryAttemptsReturnsNotificationDiagnostics(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	recordSlug := must.Slug("notif-001")
	attemptSlug := must.Slug("alert-delivery-001")
	destinationSlug := must.Slug("ops-email")
	policySlug := must.Slug("critical-pipeline-email")
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	sentAt := now.Add(2 * time.Minute)
	errorCategory := "EMAIL_DELIVERY_FAILED"
	errorMessage := "SMTP delivery failed"

	svc := newTestService(&fakeNotificationsRepo{
		findRecordBySlugFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			gotRecordSlug slug.Slug,
		) (*reponotifications.Record, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, recordSlug, gotRecordSlug)
			return &reponotifications.Record{OrgSlug: orgSlug, Slug: recordSlug}, nil
		},
		listAlertDeliveryAttemptsForRecordFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			gotRecordSlug slug.Slug,
			req model.ListRequest,
		) ([]*reponotifications.AlertDeliveryAttempt, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, recordSlug, gotRecordSlug)
			require.Equal(t, model.ListRequest{
				Limit:     2,
				Cursor:    "page-1",
				SortBy:    model.ListSortCreatedAt,
				Order:     model.ListOrderDesc,
				Direction: model.ListDirectionNext,
			}, req)

			return []*reponotifications.AlertDeliveryAttempt{
				{
					OrgSlug:                  orgSlug,
					Slug:                     attemptSlug,
					NotificationRecordSlug:   recordSlug,
					AlertPolicySlug:          testSlugPtr(policySlug),
					DestinationSlug:          testSlugPtr(destinationSlug),
					DestinationName:          testStringPtr("Operations Email"),
					DestinationType:          model.AlertDestinationTypeEmail,
					Status:                   model.AlertDeliveryAttemptStatusSent,
					ProviderAttempt:          2,
					JobKind:                  "email_send",
					JobQueue:                 "email_send",
					JobState:                 "completed",
					ProviderResponseMetadata: json.RawMessage(`{"message_id_present":true}`),
					ErrorCategory:            &errorCategory,
					ErrorMessage:             &errorMessage,
					QueuedAt:                 now,
					SentAt:                   &sentAt,
					CreatedAt:                now,
					UpdatedAt:                sentAt,
				},
				{
					OrgSlug:                  orgSlug,
					Slug:                     must.Slug("alert-delivery-extra"),
					NotificationRecordSlug:   recordSlug,
					AlertPolicySlug:          testSlugPtr(policySlug),
					DestinationSlug:          testSlugPtr(destinationSlug),
					DestinationName:          testStringPtr("Operations Email"),
					DestinationType:          model.AlertDestinationTypeEmail,
					Status:                   model.AlertDeliveryAttemptStatusQueued,
					ProviderAttempt:          1,
					JobKind:                  "email_send",
					JobQueue:                 "email_send",
					JobState:                 "queued",
					ProviderResponseMetadata: json.RawMessage(`{}`),
					QueuedAt:                 now.Add(-time.Minute),
					CreatedAt:                now.Add(-time.Minute),
					UpdatedAt:                now.Add(-time.Minute),
				},
			}, nil
		},
	})

	got, err := svc.ListAlertDeliveryAttempts(
		context.Background(),
		orgSlug,
		recordSlug,
		model.ListRequest{Limit: 1, Cursor: "page-1"},
	)
	require.NoError(t, err)
	require.True(t, got.HasMore)
	require.NotNil(t, got.NextCursor)
	require.Contains(t, *got.NextCursor, attemptSlug.String())
	require.Len(t, got.Items, 1)

	item := got.Items[0]
	require.Equal(t, attemptSlug, item.Slug)
	require.Equal(t, recordSlug, item.NotificationRecordSlug)
	require.NotNil(t, item.AlertPolicySlug)
	require.Equal(t, policySlug, *item.AlertPolicySlug)
	require.NotNil(t, item.DestinationSlug)
	require.Equal(t, destinationSlug, *item.DestinationSlug)
	require.NotNil(t, item.DestinationName)
	require.Equal(t, "Operations Email", *item.DestinationName)
	require.Equal(t, model.AlertDestinationTypeEmail, item.DestinationType)
	require.Equal(t, model.AlertDeliveryAttemptStatusSent, item.Status)
	require.Equal(t, 2, item.ProviderAttempt)
	require.Equal(t, map[string]any{"message_id_present": true}, item.ProviderResponseMetadata)
	require.Equal(t, &errorCategory, item.ErrorCategory)
	require.Equal(t, &errorMessage, item.ErrorMessage)
	require.Equal(t, &sentAt, item.SentAt)
}

func TestService_ListRecentAlertDeliveryAttemptsForRecordsReturnsDiagnostics(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	recordSlug := must.Slug("notif-001")
	policySlug := must.Slug("critical-pipeline-email")
	destinationSlug := must.Slug("ops-email")
	attemptSlug := must.Slug("alert-delivery-001")
	now := time.Date(2026, 6, 30, 14, 20, 0, 0, time.UTC)

	repo := &fakeNotificationsRepo{
		listRecentAlertDeliveryAttemptsForRecordsFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			gotRecordSlugs []slug.Slug,
			gotLimitPerRecord int,
		) ([]*reponotifications.AlertDeliveryAttempt, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, []slug.Slug{recordSlug}, gotRecordSlugs)
			require.Equal(t, 5, gotLimitPerRecord)

			return []*reponotifications.AlertDeliveryAttempt{{
				OrgSlug:                  orgSlug,
				Slug:                     attemptSlug,
				NotificationRecordSlug:   recordSlug,
				AlertPolicySlug:          testSlugPtr(policySlug),
				DestinationSlug:          testSlugPtr(destinationSlug),
				DestinationName:          testStringPtr("Operations Email"),
				DestinationType:          model.AlertDestinationTypeEmail,
				Status:                   model.AlertDeliveryAttemptStatusQueued,
				ProviderAttempt:          1,
				JobKind:                  "email_send",
				JobQueue:                 "email",
				JobState:                 "queued",
				ProviderResponseMetadata: json.RawMessage(`{}`),
				QueuedAt:                 now,
				CreatedAt:                now,
				UpdatedAt:                now,
			}}, nil
		},
	}
	svc := newTestService(repo)

	got, err := svc.ListRecentAlertDeliveryAttemptsForRecords(
		context.Background(),
		orgSlug,
		[]slug.Slug{recordSlug},
		5,
	)

	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, attemptSlug, got[0].Slug)
	require.Equal(t, recordSlug, got[0].NotificationRecordSlug)
	require.NotNil(t, got[0].DestinationSlug)
	require.Equal(t, destinationSlug, *got[0].DestinationSlug)
	require.Equal(t, model.AlertDeliveryAttemptStatusQueued, got[0].Status)
}

func TestService_ListRecentAlertDeliveryAttemptsForRecordsRejectsInvalidLimit(t *testing.T) {
	t.Parallel()

	svc := newTestService(&fakeNotificationsRepo{})

	_, err := svc.ListRecentAlertDeliveryAttemptsForRecords(
		context.Background(),
		must.Slug("acme"),
		[]slug.Slug{must.Slug("notif-001")},
		11,
	)

	require.ErrorIs(t, err, ErrInvalidInput)
	require.Contains(t, err.Error(), "limit per notification must be 10 or less")
}

func TestService_ListNotificationsResolvedFilterIncludesResolvedRecords(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	resolved := model.NotificationLifecycleStateResolved

	svc := newTestService(&fakeNotificationsRepo{
		listRecordsByOrganizationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			filters reponotifications.ListRecordFilters,
			req model.ListRequest,
		) ([]*reponotifications.Record, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, &resolved, filters.LifecycleState)
			require.True(t, filters.IncludeResolved)
			require.Equal(t, model.ListSortLastSeenAt, req.SortBy)
			return []*reponotifications.Record{}, nil
		},
	})

	got, err := svc.ListNotifications(
		context.Background(),
		orgSlug,
		ListNotificationsFilters{LifecycleState: &resolved},
		model.ListRequest{},
	)
	require.NoError(t, err)
	require.Empty(t, got.Items)
	require.False(t, got.HasMore)
}

func TestService_ListNotificationsPrevDirectionBuildsForwardCursor(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	newerSlug := must.Slug("notif-newer")
	olderSlug := must.Slug("notif-older")
	now := time.Date(2026, 6, 30, 10, 15, 0, 0, time.UTC)

	svc := newTestService(&fakeNotificationsRepo{
		listRecordsByOrganizationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			_ reponotifications.ListRecordFilters,
			req model.ListRequest,
		) ([]*reponotifications.Record, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, model.ListRequest{
				Limit:     3,
				Cursor:    now.Add(-2*time.Hour).Format(time.RFC3339Nano) + "_notif-cursor",
				SortBy:    model.ListSortLastSeenAt,
				Order:     model.ListOrderAsc,
				Direction: model.ListDirectionPrev,
			}, req)

			return []*reponotifications.Record{
				{
					OrgSlug:          orgSlug,
					Slug:             olderSlug,
					NotificationType: model.NotificationTypePipelineRunFailed,
					Severity:         model.NotificationSeverityWarning,
					LifecycleState:   model.NotificationLifecycleStateOpen,
					SourceEntityType: model.EventEntityTypePipelineRun,
					SourceAction:     model.EventActionExecute,
					SourceRefs:       json.RawMessage(`{}`),
					SafeMetadata:     json.RawMessage(`{}`),
					FirstSeenAt:      now.Add(-90 * time.Minute),
					LastSeenAt:       now.Add(-30 * time.Minute),
					OccurrenceCount:  1,
					LastOccurrenceAt: now.Add(-30 * time.Minute),
					DispatchSummary: json.RawMessage(
						`{}`,
					),
					CreatedAt: now.Add(-90 * time.Minute),
					UpdatedAt: now.Add(-30 * time.Minute),
				},
				{
					OrgSlug:          orgSlug,
					Slug:             newerSlug,
					NotificationType: model.NotificationTypePipelineRunFailed,
					Severity:         model.NotificationSeverityCritical,
					LifecycleState:   model.NotificationLifecycleStateOpen,
					SourceEntityType: model.EventEntityTypePipelineRun,
					SourceAction:     model.EventActionExecute,
					SourceRefs:       json.RawMessage(`{}`),
					SafeMetadata:     json.RawMessage(`{}`),
					FirstSeenAt:      now.Add(-2 * time.Hour),
					LastSeenAt:       now,
					OccurrenceCount:  1,
					LastOccurrenceAt: now,
					DispatchSummary: json.RawMessage(
						`{}`,
					),
					CreatedAt: now.Add(-2 * time.Hour),
					UpdatedAt: now,
				},
			}, nil
		},
	})

	got, err := svc.ListNotifications(
		context.Background(),
		orgSlug,
		ListNotificationsFilters{},
		model.ListRequest{
			Limit:     2,
			Cursor:    now.Add(-2*time.Hour).Format(time.RFC3339Nano) + "_notif-cursor",
			Direction: model.ListDirectionPrev,
		},
	)

	require.NoError(t, err)
	require.True(t, got.HasMore)
	require.Len(t, got.Items, 2)
	require.Equal(t, newerSlug, got.Items[0].Slug)
	require.Equal(t, olderSlug, got.Items[1].Slug)
	require.NotNil(t, got.NextCursor)
	require.Contains(t, *got.NextCursor, olderSlug.String())
	require.Nil(t, got.PrevCursor)
}

func TestService_CreateAlertDestinationPersistsNonSecretConfigOnly(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	destinationSlug := must.Slug("ops-email")
	now := time.Date(2026, time.June, 29, 16, 15, 0, 0, time.UTC)

	svc := newTestServiceWithClock(&fakeNotificationsRepo{
		createDestinationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.CreateAlertDestinationParams,
		) (*reponotifications.AlertDestination, error) {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, destinationSlug, params.Slug)
			require.Equal(t, model.AlertDestinationTypeEmail, params.Type)
			require.Equal(t, "Operations Email", params.Name)
			require.JSONEq(t, `{"recipients":["ops@example.com"]}`, string(params.NonSecretConfig))
			require.NotContains(t, string(params.NonSecretConfig), "token")
			require.NotContains(t, string(params.NonSecretConfig), "password")

			return &reponotifications.AlertDestination{
				OrgSlug:         params.OrgSlug,
				Slug:            params.Slug,
				Name:            params.Name,
				Type:            params.Type,
				NonSecretConfig: params.NonSecretConfig,
				Enabled:         true,
				CreatedAt:       now,
				UpdatedAt:       now,
			}, nil
		},
	}, testutil.FakeClock{NowFn: func() time.Time { return now }})

	got, err := svc.CreateAlertDestination(context.Background(), testAlertPrincipal(), CreateAlertDestinationParams{
		OrgSlug: orgSlug,
		Slug:    destinationSlug,
		Name:    "Operations Email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []string{"ops@example.com"},
		},
	})

	require.NoError(t, err)
	require.Equal(t, destinationSlug, got.Slug)
	require.Equal(t, model.AlertDestinationTypeEmail, got.Type)
	require.Equal(t, "ops@example.com", got.NonSecretConfig["recipients"].([]any)[0])
}

func TestService_CreateAlertDestinationReturnsRequiredAuditEnqueueError(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	destinationSlug := must.Slug("ops-email")
	userSlug := must.Slug("alice")
	auditErr := xerror.Unavailable(xerror.Event).Detail("audit queue unavailable")
	events := &fakeNotificationEventRecorder{requiredErr: auditErr}
	repoCreateCalls := 0

	svc := newTestServiceWithEventProducer(
		&fakeNotificationsRepo{
			createDestinationFn: func(
				_ context.Context,
				_ postgres.DBTX,
				params reponotifications.CreateAlertDestinationParams,
			) (*reponotifications.AlertDestination, error) {
				repoCreateCalls++
				return &reponotifications.AlertDestination{
					OrgSlug:         params.OrgSlug,
					Slug:            params.Slug,
					Name:            params.Name,
					Type:            params.Type,
					NonSecretConfig: params.NonSecretConfig,
					Enabled:         true,
				}, nil
			},
		},
		testutil.FakeClock{},
		&fakeNotificationDeliveryProducer{},
		&testutil.FakeEmailProducer{},
		events,
	)

	got, err := svc.CreateAlertDestination(
		context.Background(),
		AlertManagementPrincipal{UserSlug: userSlug},
		CreateAlertDestinationParams{
			OrgSlug: orgSlug,
			Slug:    destinationSlug,
			Name:    "Operations Email",
			Type:    model.AlertDestinationTypeEmail,
			NonSecretConfig: map[string]any{
				"recipients": []string{"ops@example.com"},
			},
		},
	)

	require.ErrorIs(t, err, auditErr)
	require.Nil(t, got)
	require.Equal(t, 1, repoCreateCalls)
	require.Len(t, events.calls, 1)
	attempted := events.calls[0]
	require.Equal(t, orgSlug, attempted.OrgSlug)
	require.Equal(t, model.EventClassSecurityAudit, attempted.EventClass)
	require.Equal(t, model.CurrentEventSchemaVersion, attempted.SchemaVersion)
	require.Equal(t, model.EventActorTypeUser, attempted.ActorType)
	require.NotNil(t, attempted.ActorSlug)
	require.Equal(t, userSlug, *attempted.ActorSlug)
	require.Equal(t, model.EventEntityTypeAlertDestination, attempted.EntityType)
	require.NotNil(t, attempted.EntitySlug)
	require.Equal(t, destinationSlug, *attempted.EntitySlug)
	require.Equal(t, model.EventActionCreate, attempted.Action)
	require.Equal(t, map[string]any{
		"type":    "email",
		"enabled": true,
	}, attempted.Metadata)
}

func TestService_CreateAlertDestinationGeneratesSlugWhenOmitted(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	now := time.Date(2026, time.June, 29, 16, 0, 0, 0, time.UTC)

	svc := newTestService(&fakeNotificationsRepo{
		createDestinationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.CreateAlertDestinationParams,
		) (*reponotifications.AlertDestination, error) {
			require.Equal(t, must.Slug("operations-email"), params.Slug)
			return &reponotifications.AlertDestination{
				OrgSlug:         params.OrgSlug,
				Slug:            params.Slug,
				Name:            params.Name,
				Type:            params.Type,
				NonSecretConfig: params.NonSecretConfig,
				Enabled:         true,
				CreatedAt:       now,
				UpdatedAt:       now,
			}, nil
		},
	})

	got, err := svc.CreateAlertDestination(context.Background(), testAlertPrincipal(), CreateAlertDestinationParams{
		OrgSlug: orgSlug,
		Name:    "Operations Email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []string{"ops@example.com"},
		},
	})

	require.NoError(t, err)
	require.Equal(t, must.Slug("operations-email"), got.Slug)
}

func TestService_CreateAlertDestinationRetriesGeneratedSlugCollisionOnce(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	now := time.Date(2026, time.June, 29, 16, 5, 0, 0, time.UTC)
	createCalls := 0

	svc := newTestService(&fakeNotificationsRepo{
		createDestinationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.CreateAlertDestinationParams,
		) (*reponotifications.AlertDestination, error) {
			createCalls++
			if createCalls == 1 {
				require.Equal(t, must.Slug("operations-email"), params.Slug)
				return nil, ErrDestinationSlugTaken
			}
			require.Contains(t, params.Slug.String(), "operations-email-")
			require.NotEqual(t, must.Slug("operations-email"), params.Slug)
			return &reponotifications.AlertDestination{
				OrgSlug:         params.OrgSlug,
				Slug:            params.Slug,
				Name:            params.Name,
				Type:            params.Type,
				NonSecretConfig: params.NonSecretConfig,
				Enabled:         true,
				CreatedAt:       now,
				UpdatedAt:       now,
			}, nil
		},
	})

	got, err := svc.CreateAlertDestination(context.Background(), testAlertPrincipal(), CreateAlertDestinationParams{
		OrgSlug: orgSlug,
		Name:    "Operations Email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []string{"ops@example.com"},
		},
	})

	require.NoError(t, err)
	require.Equal(t, 2, createCalls)
	require.Contains(t, got.Slug.String(), "operations-email-")
}

func TestService_CreateAlertDestinationRejectsSecretBearingNonSecretConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		nonSecretConfig map[string]any
	}{
		{
			name: "RejectsNestedToken",
			nonSecretConfig: map[string]any{
				"recipients": []map[string]string{
					{"email": "ops@example.com", "token": "plaintext-secret"},
				},
			},
		},
		{
			name: "RejectsAPIKey",
			nonSecretConfig: map[string]any{
				"api_key": "plaintext-secret",
			},
		},
		{
			name: "RejectsCamelCaseAccessKey",
			nonSecretConfig: map[string]any{
				"accessKey": "plaintext-secret",
			},
		},
		{
			name: "RejectsPrivateKey",
			nonSecretConfig: map[string]any{
				"private_key": "plaintext-secret",
			},
		},
		{
			name: "RejectsAuthorizationHeader",
			nonSecretConfig: map[string]any{
				"authorization_header": "Bearer plaintext-secret",
			},
		},
		{
			name: "RejectsGenericNestedStringKeyedMap",
			nonSecretConfig: map[string]any{
				"routing": map[string][]map[string]string{
					"primary": {
						{"label": "ops", "token": "plaintext-secret"},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := newTestService(&fakeNotificationsRepo{})

			got, err := svc.CreateAlertDestination(context.Background(), testAlertPrincipal(), CreateAlertDestinationParams{
				OrgSlug:         must.Slug("acme"),
				Slug:            must.Slug("ops-email"),
				Name:            "Operations Email",
				Type:            model.AlertDestinationTypeEmail,
				NonSecretConfig: tc.nonSecretConfig,
			})

			require.ErrorIs(t, err, ErrInvalidInput)
			require.Nil(t, got)
		})
	}
}

func TestService_CreateAlertDestinationRejectsMissingEmailRecipients(t *testing.T) {
	t.Parallel()

	svc := newTestService(&fakeNotificationsRepo{})

	got, err := svc.CreateAlertDestination(context.Background(), testAlertPrincipal(), CreateAlertDestinationParams{
		OrgSlug:         must.Slug("acme"),
		Slug:            must.Slug("ops-email"),
		Name:            "Operations Email",
		Type:            model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{},
	})

	require.ErrorIs(t, err, ErrInvalidInput)
	require.Nil(t, got)
}

func TestService_CreateAlertPolicyPersistsExplicitScopeAndDestinationBindings(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("warehouse")
	environmentSlug := must.Slug("prod")
	policySlug := must.Slug("pipeline-failure-critical")
	destinationSlug := must.Slug("ops-email")
	now := time.Date(2026, time.June, 29, 16, 20, 0, 0, time.UTC)
	bindCalls := 0

	svc := newTestServiceWithClock(&fakeNotificationsRepo{
		createAlertPolicyFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.CreateAlertPolicyParams,
		) (*reponotifications.AlertPolicy, error) {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, policySlug, params.Slug)
			require.Equal(t, model.AlertPolicyScopeProjectEnvironment, params.Scope)
			require.Equal(t, projectSlug, *params.ProjectSlug)
			require.Equal(t, environmentSlug, *params.EnvironmentSlug)
			require.Equal(t, testNotificationTypePtr(model.NotificationTypePipelineRunFailed), params.NotificationType)
			require.Equal(t, testNotificationSeverityPtr(model.NotificationSeverityCritical), params.Severity)
			require.Nil(t, params.LifecycleState)

			return &reponotifications.AlertPolicy{
				OrgSlug:          params.OrgSlug,
				Slug:             params.Slug,
				Name:             params.Name,
				Scope:            params.Scope,
				ProjectSlug:      params.ProjectSlug,
				EnvironmentSlug:  params.EnvironmentSlug,
				NotificationType: params.NotificationType,
				Severity:         params.Severity,
				LifecycleState:   params.LifecycleState,
				Enabled:          true,
				CreatedAt:        now,
				UpdatedAt:        now,
			}, nil
		},
		bindAlertPolicyDestinationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.BindAlertPolicyDestinationParams,
		) error {
			bindCalls++
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, policySlug, params.AlertPolicySlug)
			require.Equal(t, destinationSlug, params.DestinationSlug)
			return nil
		},
	}, testutil.FakeClock{NowFn: func() time.Time { return now }})

	got, err := svc.CreateAlertPolicy(context.Background(), testAlertPrincipal(), CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             "Pipeline failure critical",
		Scope:            model.AlertPolicyScopeProjectEnvironment,
		ProjectSlug:      &projectSlug,
		EnvironmentSlug:  &environmentSlug,
		NotificationType: testNotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         testNotificationSeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})

	require.NoError(t, err)
	require.Equal(t, policySlug, got.Slug)
	require.Equal(t, model.AlertPolicyScopeProjectEnvironment, got.Scope)
	require.Equal(t, testNotificationTypePtr(model.NotificationTypePipelineRunFailed), got.NotificationType)
	require.Nil(t, got.LifecycleState)
	require.Equal(t, []slug.Slug{destinationSlug}, got.DestinationSlugs)
	require.Equal(t, 1, bindCalls)
}

func TestService_CreateAlertPolicySourceSelectors(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	policySlug := must.Slug("pipeline-failure-critical")
	destinationSlug := must.Slug("ops-email")
	sourceKind := model.EventEntityTypePipelineRun
	sourceAction := model.EventActionExecute
	projectSourceKind := model.EventEntityTypeProject
	projectSourceAction := model.EventActionCreate
	unsupportedSourceKind := model.EventEntityTypePipelineCheckpoint
	unsupportedSourceAction := model.EventActionExecute
	sourceRef := must.Slug("daily-ledger")
	invalidSourceRef := slug.Slug("Daily Ledger")
	now := time.Date(2026, time.June, 29, 16, 25, 0, 0, time.UTC)

	tests := []struct {
		name             string
		sourceKind       *model.EventEntityType
		sourceAction     *model.EventAction
		sourceRef        *slug.Slug
		clearPrimary     bool
		wantSourceKind   *model.EventEntityType
		wantSourceAction *model.EventAction
		wantSourceRef    *slug.Slug
		wantErr          string
	}{
		{
			name:             "PersistsPipelineRunSourceSelectors",
			sourceKind:       &sourceKind,
			sourceAction:     &sourceAction,
			sourceRef:        &sourceRef,
			wantSourceKind:   &sourceKind,
			wantSourceAction: &sourceAction,
			wantSourceRef:    &sourceRef,
		},
		{
			name: "PersistsUnsetSelectors",
		},
		{
			name:             "PersistsProjectSourceOnlySelectors",
			sourceKind:       &projectSourceKind,
			sourceAction:     &projectSourceAction,
			sourceRef:        &sourceRef,
			clearPrimary:     true,
			wantSourceKind:   &projectSourceKind,
			wantSourceAction: &projectSourceAction,
			wantSourceRef:    &sourceRef,
		},
		{
			name:         "RejectsSourceActionWithoutSourceKind",
			sourceAction: &sourceAction,
			wantErr:      "source_kind is required",
		},
		{
			name:      "RejectsSourceRefWithoutSourceKind",
			sourceRef: &sourceRef,
			wantErr:   "source_kind is required",
		},
		{
			name:       "RejectsUnsupportedSourceKind",
			sourceKind: &unsupportedSourceKind,
			wantErr:    "source_kind is not supported",
		},
		{
			name:         "RejectsUnsupportedSourceActionForSourceKind",
			sourceKind:   &projectSourceKind,
			sourceAction: &unsupportedSourceAction,
			wantErr:      "source_action is not supported",
		},
		{
			name:       "RejectsInvalidPipelineRunSourceRef",
			sourceKind: &sourceKind,
			sourceRef:  &invalidSourceRef,
			wantErr:    "slug",
		},
		{
			name:         "RejectsMissingPrimarySelector",
			clearPrimary: true,
			wantErr:      "requires notification_type, severity, or source_kind",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			createCalls := 0
			bindCalls := 0
			svc := newTestServiceWithClock(&fakeNotificationsRepo{
				createAlertPolicyFn: func(
					_ context.Context,
					_ postgres.DBTX,
					params reponotifications.CreateAlertPolicyParams,
				) (*reponotifications.AlertPolicy, error) {
					if tc.wantErr != "" {
						t.Fatalf("CreateAlertPolicy repository call should not run for invalid selector")
					}
					createCalls++
					require.Equal(t, orgSlug, params.OrgSlug)
					require.Equal(t, policySlug, params.Slug)
					require.Equal(t, tc.wantSourceKind, params.SourceKind)
					require.Equal(t, tc.wantSourceAction, params.SourceAction)
					require.Equal(t, tc.wantSourceRef, params.SourceRef)

					return &reponotifications.AlertPolicy{
						OrgSlug:          params.OrgSlug,
						Slug:             params.Slug,
						Name:             params.Name,
						Scope:            params.Scope,
						NotificationType: params.NotificationType,
						Severity:         params.Severity,
						LifecycleState:   params.LifecycleState,
						SourceKind:       params.SourceKind,
						SourceAction:     params.SourceAction,
						SourceRef:        params.SourceRef,
						Enabled:          true,
						CreatedAt:        now,
						UpdatedAt:        now,
					}, nil
				},
				bindAlertPolicyDestinationFn: func(
					_ context.Context,
					_ postgres.DBTX,
					params reponotifications.BindAlertPolicyDestinationParams,
				) error {
					if tc.wantErr != "" {
						t.Fatalf("BindAlertPolicyDestination repository call should not run for invalid selector")
					}
					bindCalls++
					require.Equal(t, orgSlug, params.OrgSlug)
					require.Equal(t, policySlug, params.AlertPolicySlug)
					require.Equal(t, destinationSlug, params.DestinationSlug)
					return nil
				},
			}, testutil.FakeClock{NowFn: func() time.Time { return now }})

			params := CreateAlertPolicyParams{
				OrgSlug:          orgSlug,
				Slug:             policySlug,
				Name:             "Pipeline failure critical",
				Scope:            model.AlertPolicyScopeOrganization,
				NotificationType: testNotificationTypePtr(model.NotificationTypePipelineRunFailed),
				Severity:         testNotificationSeverityPtr(model.NotificationSeverityCritical),
				DestinationSlugs: []slug.Slug{destinationSlug},
				SourceKind:       tc.sourceKind,
				SourceAction:     tc.sourceAction,
				SourceRef:        tc.sourceRef,
			}
			if tc.clearPrimary {
				params.NotificationType = nil
				params.Severity = nil
			}

			got, err := svc.CreateAlertPolicy(context.Background(), testAlertPrincipal(), params)

			if tc.wantErr != "" {
				require.ErrorIs(t, err, ErrInvalidInput)
				require.ErrorContains(t, err, tc.wantErr)
				require.Nil(t, got)
				require.Equal(t, 0, createCalls)
				require.Equal(t, 0, bindCalls)
				return
			}

			require.NoError(t, err)
			require.Equal(t, policySlug, got.Slug)
			require.Equal(t, tc.wantSourceKind, got.SourceKind)
			require.Equal(t, tc.wantSourceAction, got.SourceAction)
			require.Equal(t, tc.wantSourceRef, got.SourceRef)
			require.Equal(t, []slug.Slug{destinationSlug}, got.DestinationSlugs)
			require.Equal(t, 1, createCalls)
			require.Equal(t, 1, bindCalls)
		})
	}
}

func TestService_CreateAlertPolicyGeneratesSlugWhenOmitted(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	destinationSlug := must.Slug("ops-email")
	now := time.Date(2026, time.June, 29, 16, 30, 0, 0, time.UTC)

	svc := newTestService(&fakeNotificationsRepo{
		createAlertPolicyFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.CreateAlertPolicyParams,
		) (*reponotifications.AlertPolicy, error) {
			require.Equal(t, must.Slug("critical-pipeline-failures"), params.Slug)
			return &reponotifications.AlertPolicy{
				OrgSlug:          params.OrgSlug,
				Slug:             params.Slug,
				Name:             params.Name,
				Scope:            params.Scope,
				NotificationType: params.NotificationType,
				Severity:         params.Severity,
				LifecycleState:   params.LifecycleState,
				Enabled:          true,
				CreatedAt:        now,
				UpdatedAt:        now,
			}, nil
		},
		bindAlertPolicyDestinationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.BindAlertPolicyDestinationParams,
		) error {
			require.Equal(t, must.Slug("critical-pipeline-failures"), params.AlertPolicySlug)
			require.Equal(t, destinationSlug, params.DestinationSlug)
			return nil
		},
	})

	got, err := svc.CreateAlertPolicy(context.Background(), testAlertPrincipal(), CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Name:             "Critical pipeline failures",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: testNotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         testNotificationSeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})

	require.NoError(t, err)
	require.Equal(t, must.Slug("critical-pipeline-failures"), got.Slug)
	require.Equal(t, []slug.Slug{destinationSlug}, got.DestinationSlugs)
}

func TestService_CreateAlertPolicyRetriesGeneratedSlugCollisionOnce(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	destinationSlug := must.Slug("ops-email")
	now := time.Date(2026, time.June, 29, 16, 35, 0, 0, time.UTC)
	createCalls := 0
	var retriedPolicySlug slug.Slug

	svc := newTestService(&fakeNotificationsRepo{
		createAlertPolicyFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.CreateAlertPolicyParams,
		) (*reponotifications.AlertPolicy, error) {
			createCalls++
			if createCalls == 1 {
				require.Equal(t, must.Slug("critical-pipeline-failures"), params.Slug)
				return nil, ErrAlertPolicySlugTaken
			}
			require.Contains(t, params.Slug.String(), "critical-pipeline-failures-")
			require.NotEqual(t, must.Slug("critical-pipeline-failures"), params.Slug)
			retriedPolicySlug = params.Slug
			return &reponotifications.AlertPolicy{
				OrgSlug:          params.OrgSlug,
				Slug:             params.Slug,
				Name:             params.Name,
				Scope:            params.Scope,
				NotificationType: params.NotificationType,
				Severity:         params.Severity,
				LifecycleState:   params.LifecycleState,
				Enabled:          true,
				CreatedAt:        now,
				UpdatedAt:        now,
			}, nil
		},
		bindAlertPolicyDestinationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.BindAlertPolicyDestinationParams,
		) error {
			require.Equal(t, retriedPolicySlug, params.AlertPolicySlug)
			require.Equal(t, destinationSlug, params.DestinationSlug)
			return nil
		},
	})

	got, err := svc.CreateAlertPolicy(context.Background(), testAlertPrincipal(), CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Name:             "Critical pipeline failures",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: testNotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         testNotificationSeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})

	require.NoError(t, err)
	require.Equal(t, 2, createCalls)
	require.Equal(t, retriedPolicySlug, got.Slug)
	require.Equal(t, []slug.Slug{destinationSlug}, got.DestinationSlugs)
}

func TestService_ListAlertDestinationsReturnsManagementPage(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	destinationSlug := must.Slug("ops-email")
	now := time.Date(2026, time.June, 29, 17, 0, 0, 0, time.UTC)

	svc := newTestService(&fakeNotificationsRepo{
		listAlertDestinationsByOrganizationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			req model.ListRequest,
		) ([]*reponotifications.AlertDestination, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, model.ListRequest{
				Limit:     2,
				SortBy:    model.ListSortCreatedAt,
				Order:     model.ListOrderDesc,
				Direction: model.ListDirectionNext,
			}, req)

			return []*reponotifications.AlertDestination{
				{
					OrgSlug:         orgSlug,
					Slug:            destinationSlug,
					Name:            "Operations Email",
					Type:            model.AlertDestinationTypeEmail,
					NonSecretConfig: json.RawMessage(`{"to":"ops@example.com"}`),
					Enabled:         true,
					CreatedAt:       now,
					UpdatedAt:       now,
				},
				{
					OrgSlug:         orgSlug,
					Slug:            must.Slug("platform-email"),
					Name:            "Platform Email",
					Type:            model.AlertDestinationTypeEmail,
					NonSecretConfig: json.RawMessage(`{}`),
					Enabled:         false,
					CreatedAt:       now.Add(-time.Minute),
					UpdatedAt:       now.Add(-time.Minute),
				},
			}, nil
		},
	})

	got, err := svc.ListAlertDestinations(context.Background(), testAlertPrincipal(), orgSlug, model.ListRequest{Limit: 1})

	require.NoError(t, err)
	require.True(t, got.HasMore)
	require.NotNil(t, got.NextCursor)
	require.Contains(t, *got.NextCursor, destinationSlug.String())
	require.Len(t, got.Items, 1)
	require.Equal(t, destinationSlug, got.Items[0].Slug)
	require.Equal(t, "ops@example.com", got.Items[0].NonSecretConfig["to"])
}

func TestService_TestAlertDestinationQueuesEphemeralEmailJobs(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	destinationSlug := must.Slug("ops-email")
	recipients := []string{}
	notifications := []model.NotificationEmail{}

	svc := newTestServiceWithEmailProducer(&fakeNotificationsRepo{
		findAlertDestinationBySlugFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			gotDestinationSlug slug.Slug,
		) (*reponotifications.AlertDestination, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, destinationSlug, gotDestinationSlug)
			return &reponotifications.AlertDestination{
				OrgSlug: orgSlug,
				Slug:    destinationSlug,
				Name:    "Ops email",
				Type:    model.AlertDestinationTypeEmail,
				NonSecretConfig: json.RawMessage(`{
					"recipients": [
						"Ops <ops@example.com>",
						{"email": "backup@example.com", "locale": "pl"}
					]
				}`),
				Enabled: true,
			}, nil
		},
	}, &testutil.FakeEmailProducer{
		EnqueueNotificationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			recipient model.NotificationEmailRecipient,
			notification model.NotificationEmail,
			alertDeliveryAttemptSlug slug.Slug,
		) (queue.QueuedJob, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Empty(t, alertDeliveryAttemptSlug, "test delivery must not create alert delivery diagnostics")
			recipients = append(recipients, recipient.Email)
			notifications = append(notifications, notification)
			return queue.QueuedJob{Kind: "email_send", Queue: "email", State: "queued"}, nil
		},
	})

	got, err := svc.TestAlertDestination(context.Background(), testAlertPrincipal(), TestAlertDestinationParams{
		OrgSlug:         orgSlug,
		DestinationSlug: destinationSlug,
	})

	require.NoError(t, err)
	require.Equal(t, destinationSlug, got.DestinationSlug)
	require.Equal(t, model.AlertDestinationTypeEmail, got.DestinationType)
	require.Equal(t, 2, got.RecipientCount)
	require.False(t, got.DiagnosticsPersisted)
	require.Equal(t, []AlertDestinationTestJob{
		{Kind: "email_send", Queue: "email", State: "queued"},
		{Kind: "email_send", Queue: "email", State: "queued"},
	}, got.EnqueuedJobs)
	require.Equal(t, []string{"ops@example.com", "backup@example.com"}, recipients)
	require.Len(t, notifications, 2)
	require.Equal(t, "acme", notifications[0].OrganizationName)
	require.Equal(t, must.Slug("alert-destination-test"), notifications[0].RecordSlug)
	require.Equal(t, model.NotificationSeverityInfo, notifications[0].Severity)
	require.Equal(t, "alert destination test", notifications[0].NotificationTypeLabel)
	require.Equal(t, "Test notification for alert destination ops-email.", notifications[0].Summary)
	require.Equal(t, 1, notifications[0].OccurrenceCount)
}

func TestService_TestAlertDestinationFailurePaths(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	destinationSlug := must.Slug("ops-email")
	queueErr := xerror.Unexpected().Detail("email queue unavailable")

	tests := []struct {
		name        string
		destination *reponotifications.AlertDestination
		repoErr     error
		enqueueErr  error
		wantErr     error
	}{
		{
			name:    "ReturnsNotFoundForMissingOrCrossTenantDestination",
			repoErr: ErrDestinationNotFound,
			wantErr: ErrDestinationNotFound,
		},
		{
			name: "RejectsDisabledDestination",
			destination: &reponotifications.AlertDestination{
				OrgSlug:         orgSlug,
				Slug:            destinationSlug,
				Type:            model.AlertDestinationTypeEmail,
				NonSecretConfig: json.RawMessage(`{"recipients":["ops@example.com"]}`),
				Enabled:         false,
			},
			wantErr: ErrDestinationDisabled,
		},
		{
			name: "RejectsUnsupportedDestinationType",
			destination: &reponotifications.AlertDestination{
				OrgSlug:         orgSlug,
				Slug:            destinationSlug,
				Type:            model.AlertDestinationTypeUnknown,
				NonSecretConfig: json.RawMessage(`{"recipients":["ops@example.com"]}`),
				Enabled:         true,
			},
			wantErr: ErrDestinationTestUnsupported,
		},
		{
			name: "RejectsInvalidEmailDestinationConfig",
			destination: &reponotifications.AlertDestination{
				OrgSlug:         orgSlug,
				Slug:            destinationSlug,
				Type:            model.AlertDestinationTypeEmail,
				NonSecretConfig: json.RawMessage(`{}`),
				Enabled:         true,
			},
			wantErr: ErrInvalidInput,
		},
		{
			name: "ReturnsEnqueueFailure",
			destination: &reponotifications.AlertDestination{
				OrgSlug:         orgSlug,
				Slug:            destinationSlug,
				Type:            model.AlertDestinationTypeEmail,
				NonSecretConfig: json.RawMessage(`{"recipients":["ops@example.com"]}`),
				Enabled:         true,
			},
			enqueueErr: queueErr,
			wantErr:    queueErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			emailJobs := 0
			svc := newTestServiceWithEmailProducer(&fakeNotificationsRepo{
				findAlertDestinationBySlugFn: func(
					_ context.Context,
					_ postgres.DBTX,
					gotOrgSlug slug.Slug,
					gotDestinationSlug slug.Slug,
				) (*reponotifications.AlertDestination, error) {
					require.Equal(t, orgSlug, gotOrgSlug)
					require.Equal(t, destinationSlug, gotDestinationSlug)
					if tc.repoErr != nil {
						return nil, tc.repoErr
					}
					return tc.destination, nil
				},
			}, &testutil.FakeEmailProducer{
				EnqueueNotificationFn: func(
					context.Context,
					postgres.DBTX,
					slug.Slug,
					model.NotificationEmailRecipient,
					model.NotificationEmail,
					slug.Slug,
				) (queue.QueuedJob, error) {
					emailJobs++
					if tc.enqueueErr != nil {
						return queue.QueuedJob{}, tc.enqueueErr
					}
					return queue.QueuedJob{Kind: "email_send", Queue: "email", State: "queued"}, nil
				},
			})

			got, err := svc.TestAlertDestination(context.Background(), testAlertPrincipal(), TestAlertDestinationParams{
				OrgSlug:         orgSlug,
				DestinationSlug: destinationSlug,
			})

			require.Error(t, err)
			require.ErrorIs(t, err, tc.wantErr)
			require.Nil(t, got)
			if tc.enqueueErr == nil {
				require.Equal(t, 0, emailJobs)
			} else {
				require.Equal(t, 1, emailJobs)
			}
		})
	}
}

func TestService_UpdateAlertPolicyReplacesDestinationBindings(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	policySlug := must.Slug("pipeline-failure-critical")
	destinationSlug := must.Slug("ops-email")
	nextDestinationSlug := must.Slug("platform-email")
	now := time.Date(2026, time.June, 29, 17, 15, 0, 0, time.UTC)
	clearCalls := 0
	var bound []slug.Slug

	svc := newTestService(&fakeNotificationsRepo{
		updateAlertPolicyFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.UpdateAlertPolicyParams,
		) (*reponotifications.AlertPolicy, error) {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, policySlug, params.Slug)
			require.Equal(t, "Critical pipeline failures", params.Name)
			require.Equal(t, model.AlertPolicyScopeOrganization, params.Scope)
			require.Equal(t, testNotificationTypePtr(model.NotificationTypePipelineRunFailed), params.NotificationType)
			require.Equal(t, testNotificationSeverityPtr(model.NotificationSeverityCritical), params.Severity)
			require.Nil(t, params.LifecycleState)
			require.False(t, params.Enabled)

			return &reponotifications.AlertPolicy{
				OrgSlug:          params.OrgSlug,
				Slug:             params.Slug,
				Name:             params.Name,
				Scope:            params.Scope,
				NotificationType: params.NotificationType,
				Severity:         params.Severity,
				LifecycleState:   params.LifecycleState,
				Enabled:          params.Enabled,
				CreatedAt:        now.Add(-time.Hour),
				UpdatedAt:        now,
			}, nil
		},
		clearAlertPolicyDestinationsFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotPolicySlug slug.Slug) error {
			clearCalls++
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, policySlug, gotPolicySlug)
			return nil
		},
		bindAlertPolicyDestinationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.BindAlertPolicyDestinationParams,
		) error {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, policySlug, params.AlertPolicySlug)
			bound = append(bound, params.DestinationSlug)
			return nil
		},
	})

	got, err := svc.UpdateAlertPolicy(context.Background(), testAlertPrincipal(), UpdateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             "Critical pipeline failures",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: testNotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         testNotificationSeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug, nextDestinationSlug},
		Enabled:          false,
	})

	require.NoError(t, err)
	require.Equal(t, policySlug, got.Slug)
	require.False(t, got.Enabled)
	require.Nil(t, got.LifecycleState)
	require.Equal(t, 1, clearCalls)
	require.Equal(t, []slug.Slug{destinationSlug, nextDestinationSlug}, bound)
	require.Equal(t, []slug.Slug{destinationSlug, nextDestinationSlug}, got.DestinationSlugs)
}

func TestService_UpdateAlertPolicyPersistsSourceSelectors(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	policySlug := must.Slug("pipeline-failure-critical")
	destinationSlug := must.Slug("ops-email")
	sourceKind := model.EventEntityTypePipelineRun
	sourceAction := model.EventActionExecute
	sourceRef := must.Slug("daily-ledger")
	now := time.Date(2026, time.June, 29, 17, 20, 0, 0, time.UTC)

	svc := newTestService(&fakeNotificationsRepo{
		updateAlertPolicyFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.UpdateAlertPolicyParams,
		) (*reponotifications.AlertPolicy, error) {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, policySlug, params.Slug)
			require.NotNil(t, params.SourceKind)
			require.Equal(t, sourceKind, *params.SourceKind)
			require.NotNil(t, params.SourceAction)
			require.Equal(t, sourceAction, *params.SourceAction)
			require.NotNil(t, params.SourceRef)
			require.Equal(t, sourceRef, *params.SourceRef)

			return &reponotifications.AlertPolicy{
				OrgSlug:          params.OrgSlug,
				Slug:             params.Slug,
				Name:             params.Name,
				Scope:            params.Scope,
				NotificationType: params.NotificationType,
				Severity:         params.Severity,
				LifecycleState:   params.LifecycleState,
				SourceKind:       params.SourceKind,
				SourceAction:     params.SourceAction,
				SourceRef:        params.SourceRef,
				Enabled:          params.Enabled,
				CreatedAt:        now.Add(-time.Hour),
				UpdatedAt:        now,
			}, nil
		},
		clearAlertPolicyDestinationsFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotPolicySlug slug.Slug) error {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, policySlug, gotPolicySlug)
			return nil
		},
		bindAlertPolicyDestinationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.BindAlertPolicyDestinationParams,
		) error {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, policySlug, params.AlertPolicySlug)
			require.Equal(t, destinationSlug, params.DestinationSlug)
			return nil
		},
	})

	got, err := svc.UpdateAlertPolicy(context.Background(), testAlertPrincipal(), UpdateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             "Pipeline failure critical",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: testNotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         testNotificationSeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
		Enabled:          true,
		SourceKind:       &sourceKind,
		SourceAction:     &sourceAction,
		SourceRef:        &sourceRef,
	})

	require.NoError(t, err)
	require.NotNil(t, got.SourceKind)
	require.Equal(t, sourceKind, *got.SourceKind)
	require.NotNil(t, got.SourceAction)
	require.Equal(t, sourceAction, *got.SourceAction)
	require.NotNil(t, got.SourceRef)
	require.Equal(t, sourceRef, *got.SourceRef)
	require.Equal(t, []slug.Slug{destinationSlug}, got.DestinationSlugs)
}

func TestService_UpdateAlertPolicyReturnsDestinationBindingErrors(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	policySlug := must.Slug("pipeline-failure-critical")
	destinationSlug := must.Slug("ops-email")
	errClear := xerror.Unexpected().Detail("clear failed")
	errBind := xerror.Unexpected().Detail("bind failed")
	now := time.Date(2026, time.June, 29, 17, 30, 0, 0, time.UTC)

	tests := []struct {
		name    string
		setup   func(*fakeNotificationsRepo)
		wantErr error
	}{
		{
			name: "ClearDestinationBindingsFails",
			setup: func(repo *fakeNotificationsRepo) {
				repo.clearAlertPolicyDestinationsFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) error {
					return errClear
				}
			},
			wantErr: errClear,
		},
		{
			name: "BindDestinationFails",
			setup: func(repo *fakeNotificationsRepo) {
				repo.clearAlertPolicyDestinationsFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) error {
					return nil
				}
				repo.bindAlertPolicyDestinationFn = func(context.Context, postgres.DBTX, reponotifications.BindAlertPolicyDestinationParams) error {
					return errBind
				}
			},
			wantErr: errBind,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := &fakeNotificationsRepo{
				updateAlertPolicyFn: func(
					_ context.Context,
					_ postgres.DBTX,
					params reponotifications.UpdateAlertPolicyParams,
				) (*reponotifications.AlertPolicy, error) {
					return &reponotifications.AlertPolicy{
						OrgSlug:          params.OrgSlug,
						Slug:             params.Slug,
						Name:             params.Name,
						Scope:            params.Scope,
						NotificationType: params.NotificationType,
						Severity:         params.Severity,
						LifecycleState:   params.LifecycleState,
						Enabled:          params.Enabled,
						CreatedAt:        now.Add(-time.Hour),
						UpdatedAt:        now,
					}, nil
				},
			}
			tc.setup(repo)
			svc := newTestService(repo)

			got, err := svc.UpdateAlertPolicy(context.Background(), testAlertPrincipal(), UpdateAlertPolicyParams{
				OrgSlug:          orgSlug,
				Slug:             policySlug,
				Name:             "Critical pipeline failures",
				Scope:            model.AlertPolicyScopeOrganization,
				NotificationType: testNotificationTypePtr(model.NotificationTypePipelineRunFailed),
				Severity:         testNotificationSeverityPtr(model.NotificationSeverityCritical),
				DestinationSlugs: []slug.Slug{destinationSlug},
				Enabled:          true,
			})

			require.ErrorIs(t, err, tc.wantErr)
			require.Nil(t, got)
		})
	}
}

func TestService_CreateAlertPolicyRejectsInvalidScopeAndDestinationBindings(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	policySlug := must.Slug("pipeline-failure-critical")
	destinationSlug := must.Slug("ops-email")
	projectSlug := must.Slug("warehouse")
	environmentSlug := must.Slug("prod")
	emptySlug := slug.Slug("")

	tests := []struct {
		name   string
		params CreateAlertPolicyParams
	}{
		{
			name: "ProjectScopeRejectsEmptyProjectSlug",
			params: CreateAlertPolicyParams{
				OrgSlug:          orgSlug,
				Slug:             policySlug,
				Name:             "Pipeline failure critical",
				Scope:            model.AlertPolicyScopeProject,
				ProjectSlug:      &emptySlug,
				NotificationType: testNotificationTypePtr(model.NotificationTypePipelineRunFailed),
				Severity:         testNotificationSeverityPtr(model.NotificationSeverityCritical),
				DestinationSlugs: []slug.Slug{destinationSlug},
			},
		},
		{
			name: "ProjectScopeRejectsEnvironmentSlug",
			params: CreateAlertPolicyParams{
				OrgSlug:          orgSlug,
				Slug:             policySlug,
				Name:             "Pipeline failure critical",
				Scope:            model.AlertPolicyScopeProject,
				ProjectSlug:      &projectSlug,
				EnvironmentSlug:  &environmentSlug,
				NotificationType: testNotificationTypePtr(model.NotificationTypePipelineRunFailed),
				Severity:         testNotificationSeverityPtr(model.NotificationSeverityCritical),
				DestinationSlugs: []slug.Slug{destinationSlug},
			},
		},
		{
			name: "ProjectEnvironmentScopeRejectsEmptyEnvironmentSlug",
			params: CreateAlertPolicyParams{
				OrgSlug:          orgSlug,
				Slug:             policySlug,
				Name:             "Pipeline failure critical",
				Scope:            model.AlertPolicyScopeProjectEnvironment,
				ProjectSlug:      &projectSlug,
				EnvironmentSlug:  &emptySlug,
				NotificationType: testNotificationTypePtr(model.NotificationTypePipelineRunFailed),
				Severity:         testNotificationSeverityPtr(model.NotificationSeverityCritical),
				DestinationSlugs: []slug.Slug{destinationSlug},
			},
		},
		{
			name: "OrganizationScopeRejectsProjectSlug",
			params: CreateAlertPolicyParams{
				OrgSlug:          orgSlug,
				Slug:             policySlug,
				Name:             "Pipeline failure critical",
				Scope:            model.AlertPolicyScopeOrganization,
				ProjectSlug:      &projectSlug,
				NotificationType: testNotificationTypePtr(model.NotificationTypePipelineRunFailed),
				Severity:         testNotificationSeverityPtr(model.NotificationSeverityCritical),
				DestinationSlugs: []slug.Slug{destinationSlug},
			},
		},
		{
			name: "RejectsMissingDestinationBinding",
			params: CreateAlertPolicyParams{
				OrgSlug:          orgSlug,
				Slug:             policySlug,
				Name:             "Pipeline failure critical",
				Scope:            model.AlertPolicyScopeOrganization,
				NotificationType: testNotificationTypePtr(model.NotificationTypePipelineRunFailed),
				Severity:         testNotificationSeverityPtr(model.NotificationSeverityCritical),
			},
		},
		{
			name: "RejectsEmptyDestinationBinding",
			params: CreateAlertPolicyParams{
				OrgSlug:          orgSlug,
				Slug:             policySlug,
				Name:             "Pipeline failure critical",
				Scope:            model.AlertPolicyScopeOrganization,
				NotificationType: testNotificationTypePtr(model.NotificationTypePipelineRunFailed),
				Severity:         testNotificationSeverityPtr(model.NotificationSeverityCritical),
				DestinationSlugs: []slug.Slug{""},
			},
		},
		{
			name: "RejectsDuplicateDestinationBinding",
			params: CreateAlertPolicyParams{
				OrgSlug:          orgSlug,
				Slug:             policySlug,
				Name:             "Pipeline failure critical",
				Scope:            model.AlertPolicyScopeOrganization,
				NotificationType: testNotificationTypePtr(model.NotificationTypePipelineRunFailed),
				Severity:         testNotificationSeverityPtr(model.NotificationSeverityCritical),
				DestinationSlugs: []slug.Slug{destinationSlug, destinationSlug},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := newTestService(&fakeNotificationsRepo{})

			got, err := svc.CreateAlertPolicy(context.Background(), testAlertPrincipal(), tc.params)

			require.ErrorIs(t, err, ErrInvalidInput)
			require.Nil(t, got)
		})
	}
}

func TestService_CreateCandidatePersistsRecordAndQueuesAlertSelectedDispatch(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("warehouse")
	environmentSlug := must.Slug("prod")
	recordSlug := must.Slug("pipeline-run-failed-daily-ledger")
	alertPolicySlug := must.Slug("pipeline-failure-critical")
	destinationSlug := must.Slug("ops-email")
	sourceEventSlug := must.Slug("evt-pipeline-run-failed-001")
	now := time.Date(2026, time.June, 29, 17, 0, 0, 0, time.UTC)
	producer := &fakeNotificationDeliveryProducer{}
	upsertCalls := 0
	listCalls := 0
	summaryCalls := 0

	svc := newTestServiceWithDeliveryProducer(&fakeNotificationsRepo{
		upsertRecordFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.UpsertRecordParams,
		) (*reponotifications.Record, error) {
			upsertCalls++
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, recordSlug, params.Slug)
			require.Equal(t, projectSlug, *params.ProjectSlug)
			require.Equal(t, environmentSlug, *params.EnvironmentSlug)
			require.Equal(t, model.NotificationLifecycleStateOpen, params.LifecycleState)
			require.Equal(t, "pipeline:run_failed:warehouse:prod:daily-ledger", params.DedupeFingerprint)
			require.Equal(t, sourceEventSlug, *params.SourceEventSlug)
			require.Equal(t, model.EventEntityTypePipelineRun, params.SourceEntityType)
			require.Equal(t, model.EventActionExecute, params.SourceAction)
			require.JSONEq(t, `{"pipeline_slug":"daily-ledger","run_slug":"run-001"}`, string(params.SourceRefs))
			require.JSONEq(t, `{"failure_kind":"contract","summary":"contract failed"}`, string(params.SafeMetadata))
			require.Equal(t, now, params.ObservedAt)

			return &reponotifications.Record{
				OrgSlug:           params.OrgSlug,
				Slug:              params.Slug,
				ProjectSlug:       params.ProjectSlug,
				EnvironmentSlug:   params.EnvironmentSlug,
				NotificationType:  params.NotificationType,
				Severity:          params.Severity,
				LifecycleState:    params.LifecycleState,
				DedupeFingerprint: params.DedupeFingerprint,
				SourceEntityType:  params.SourceEntityType,
				SourceAction:      params.SourceAction,
				SourceRefs:        params.SourceRefs,
				SafeMetadata:      params.SafeMetadata,
				FirstSeenAt:       now,
				LastSeenAt:        now,
				OccurrenceCount:   1,
				LastOccurrenceAt:  now,
				DispatchSummary:   json.RawMessage(`{}`),
				CreatedAt:         now,
				UpdatedAt:         now,
			}, nil
		},
		listDeliveryTargetsForRecordFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			gotRecordSlug slug.Slug,
		) ([]reponotifications.DeliveryTarget, error) {
			listCalls++
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, recordSlug, gotRecordSlug)
			return []reponotifications.DeliveryTarget{{
				OrgSlug:          orgSlug,
				OrganizationName: "Acme",
				RecordSlug:       recordSlug,
				AlertPolicySlug:  alertPolicySlug,
				DestinationSlug:  destinationSlug,
				DestinationName:  "Ops email",
				DestinationType:  model.AlertDestinationTypeEmail,
				NonSecretConfig:  json.RawMessage(`{"recipients":["ops@example.com"]}`),
				NotificationType: model.NotificationTypePipelineRunFailed,
				Severity:         model.NotificationSeverityCritical,
				SourceEntityType: model.EventEntityTypePipelineRun,
				SourceAction:     model.EventActionExecute,
				SafeMetadata:     json.RawMessage(`{"summary":"contract failed"}`),
				OccurrenceCount:  1,
			}}, nil
		},
		updateRecordDispatchSummaryFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.UpdateRecordDispatchSummaryParams,
		) (*reponotifications.Record, error) {
			summaryCalls++
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, recordSlug, params.RecordSlug)
			require.Equal(t, destinationSlug, params.DestinationSlug)
			require.Equal(t, model.NotificationDispatchStatusQueued, params.Status)
			require.Equal(t, now, params.QueuedAt)
			require.Equal(t, "notification_delivery", params.JobKind)
			require.Equal(t, "notification_delivery", params.JobQueue)
			require.Equal(t, "queued", params.JobState)
			return &reponotifications.Record{OrgSlug: orgSlug, Slug: recordSlug}, nil
		},
	}, testutil.FakeClock{NowFn: func() time.Time { return now }}, producer)

	got, err := svc.CreateCandidate(context.Background(), CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &recordSlug,
		ProjectSlug:       &projectSlug,
		EnvironmentSlug:   &environmentSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: "pipeline:run_failed:warehouse:prod:daily-ledger",
		SourceEventSlug:   &sourceEventSlug,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-001",
		},
		SafeMetadata: map[string]any{
			"failure_kind": "contract",
			"summary":      "contract failed",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, got.Record)
	require.Equal(t, recordSlug, got.Record.Slug)
	require.Equal(t, projectSlug, *got.Record.ProjectSlug)
	require.Equal(t, environmentSlug, *got.Record.EnvironmentSlug)
	require.Equal(t, 1, upsertCalls)
	require.Equal(t, 1, listCalls)
	require.Equal(t, 1, summaryCalls)
	require.Len(t, producer.calls, 1)
	require.Equal(t, orgSlug, producer.calls[0].OrgSlug)
	require.Equal(t, recordSlug, producer.calls[0].RecordSlug)
	require.Equal(t, destinationSlug, producer.calls[0].DestinationSlug)
	require.Equal(t, alertPolicySlug, producer.calls[0].AlertPolicySlug)
}

func TestService_CreateCandidateReturnsErrorWhenProducerEnqueueFails(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	recordSlug := must.Slug("pipeline-run-failed-daily-ledger")
	alertPolicySlug := must.Slug("pipeline-failure-critical")
	destinationSlug := must.Slug("ops-email")
	now := time.Date(2026, time.June, 29, 17, 12, 0, 0, time.UTC)
	producer := &fakeNotificationDeliveryProducer{
		enqueueFn: func(context.Context, postgres.DBTX, NotificationDeliveryJobParams) (queue.QueuedJob, error) {
			return queue.QueuedJob{}, fmt.Errorf("river unavailable")
		},
	}
	summaryCalls := 0

	svc := newTestServiceWithDeliveryProducer(&fakeNotificationsRepo{
		upsertRecordFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.UpsertRecordParams,
		) (*reponotifications.Record, error) {
			return &reponotifications.Record{
				OrgSlug:           params.OrgSlug,
				Slug:              params.Slug,
				NotificationType:  params.NotificationType,
				Severity:          params.Severity,
				LifecycleState:    params.LifecycleState,
				DedupeFingerprint: params.DedupeFingerprint,
				SourceEntityType:  params.SourceEntityType,
				SourceAction:      params.SourceAction,
				SourceRefs:        params.SourceRefs,
				SafeMetadata:      params.SafeMetadata,
				FirstSeenAt:       now,
				LastSeenAt:        now,
				OccurrenceCount:   1,
				LastOccurrenceAt:  now,
				DispatchSummary:   json.RawMessage(`{}`),
				CreatedAt:         now,
				UpdatedAt:         now,
			}, nil
		},
		listDeliveryTargetsForRecordFn: func(
			_ context.Context,
			_ postgres.DBTX,
			_ slug.Slug,
			_ slug.Slug,
		) ([]reponotifications.DeliveryTarget, error) {
			return []reponotifications.DeliveryTarget{{
				OrgSlug:          orgSlug,
				OrganizationName: "Acme",
				RecordSlug:       recordSlug,
				AlertPolicySlug:  alertPolicySlug,
				DestinationSlug:  destinationSlug,
				DestinationName:  "Ops email",
				DestinationType:  model.AlertDestinationTypeEmail,
				NonSecretConfig:  json.RawMessage(`{"recipients":["ops@example.com"]}`),
				NotificationType: model.NotificationTypePipelineRunFailed,
				Severity:         model.NotificationSeverityCritical,
				SourceEntityType: model.EventEntityTypePipelineRun,
				SourceAction:     model.EventActionExecute,
				SafeMetadata:     json.RawMessage(`{"summary":"contract failed"}`),
				OccurrenceCount:  1,
			}}, nil
		},
		updateRecordDispatchSummaryFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.UpdateRecordDispatchSummaryParams,
		) (*reponotifications.Record, error) {
			summaryCalls++
			return nil, errUnexpectedNotificationsRepoCall
		},
	}, testutil.FakeClock{NowFn: func() time.Time { return now }}, producer)

	got, err := svc.CreateCandidate(context.Background(), CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &recordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: "pipeline:run_failed:warehouse:prod:daily-ledger",
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
		},
		SafeMetadata: map[string]any{
			"summary": "contract failed",
		},
	})

	require.ErrorContains(t, err, "enqueue notification delivery job")
	require.ErrorContains(t, err, "river unavailable")
	require.Nil(t, got)
	require.Len(t, producer.calls, 1)
	require.Equal(t, 0, summaryCalls)
}

func TestService_CreateCandidatePersistsRecordWithoutAlertDeliveryTargets(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("warehouse")
	recordSlug := must.Slug("pipeline-run-failed-daily-ledger")
	now := time.Date(2026, time.June, 29, 17, 14, 0, 0, time.UTC)
	producer := &fakeNotificationDeliveryProducer{}
	upsertCalls := 0
	listCalls := 0

	svc := newTestServiceWithDeliveryProducer(&fakeNotificationsRepo{
		upsertRecordFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.UpsertRecordParams,
		) (*reponotifications.Record, error) {
			upsertCalls++
			require.Equal(t, projectSlug, *params.ProjectSlug)
			return &reponotifications.Record{
				OrgSlug:           params.OrgSlug,
				Slug:              params.Slug,
				ProjectSlug:       params.ProjectSlug,
				NotificationType:  params.NotificationType,
				Severity:          params.Severity,
				LifecycleState:    params.LifecycleState,
				DedupeFingerprint: params.DedupeFingerprint,
				SourceEntityType:  params.SourceEntityType,
				SourceAction:      params.SourceAction,
				SourceRefs:        params.SourceRefs,
				SafeMetadata:      params.SafeMetadata,
				FirstSeenAt:       now,
				LastSeenAt:        now,
				OccurrenceCount:   1,
				LastOccurrenceAt:  now,
				DispatchSummary:   json.RawMessage(`{}`),
				CreatedAt:         now,
				UpdatedAt:         now,
			}, nil
		},
		listDeliveryTargetsForRecordFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			gotRecordSlug slug.Slug,
		) ([]reponotifications.DeliveryTarget, error) {
			listCalls++
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, recordSlug, gotRecordSlug)
			return []reponotifications.DeliveryTarget{}, nil
		},
	}, testutil.FakeClock{NowFn: func() time.Time { return now }}, producer)

	got, err := svc.CreateCandidate(context.Background(), CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &recordSlug,
		ProjectSlug:       &projectSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityWarning,
		DedupeFingerprint: "pipeline:run_failed:warehouse:prod:daily-ledger",
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
		},
		SafeMetadata: map[string]any{
			"summary": "contract failed",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, got.Record)
	require.Equal(t, recordSlug, got.Record.Slug)
	require.Equal(t, 1, upsertCalls)
	require.Equal(t, 1, listCalls)
	require.Empty(t, producer.calls)
}

func TestService_CreateCandidateRejectsUnsafeMetadataAndInvalidScopeHints(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	environmentSlug := must.Slug("prod")
	emptySlug := slug.Slug("")

	tests := []struct {
		name            string
		params          CreateCandidateParams
		wantErrContains string
	}{
		{
			name: "RejectsEmptyRecordSlug",
			params: CreateCandidateParams{
				OrgSlug:           orgSlug,
				RecordSlug:        &emptySlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "pipeline:run_failed:prod:daily-ledger",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
				SourceRefs: map[string]string{
					"pipeline_slug": "daily-ledger",
				},
			},
			wantErrContains: "record_slug is required",
		},
		{
			name: "RejectsEmptyProjectSlug",
			params: CreateCandidateParams{
				OrgSlug:           orgSlug,
				ProjectSlug:       &emptySlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "pipeline:run_failed:prod:daily-ledger",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
				SourceRefs: map[string]string{
					"pipeline_slug": "daily-ledger",
				},
			},
			wantErrContains: "project_slug is required",
		},
		{
			name: "RejectsEmptyEnvironmentSlug",
			params: CreateCandidateParams{
				OrgSlug:           orgSlug,
				ProjectSlug:       &orgSlug,
				EnvironmentSlug:   &emptySlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "pipeline:run_failed:prod:daily-ledger",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
				SourceRefs: map[string]string{
					"pipeline_slug": "daily-ledger",
				},
			},
			wantErrContains: "environment_slug is required",
		},
		{
			name: "RejectsEmptySourceEventSlug",
			params: CreateCandidateParams{
				OrgSlug:           orgSlug,
				SourceEventSlug:   &emptySlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "pipeline:run_failed:prod:daily-ledger",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
				SourceRefs: map[string]string{
					"pipeline_slug": "daily-ledger",
				},
			},
			wantErrContains: "source_event_slug is required",
		},
		{
			name: "RejectsEnvironmentWithoutProject",
			params: CreateCandidateParams{
				OrgSlug:           orgSlug,
				EnvironmentSlug:   &environmentSlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "pipeline:run_failed:prod:daily-ledger",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
				SourceRefs: map[string]string{
					"pipeline_slug": "daily-ledger",
				},
			},
			wantErrContains: "environment_slug requires project_slug",
		},
		{
			name: "RejectsRawPayloadMetadata",
			params: CreateCandidateParams{
				OrgSlug:           orgSlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "unsafe:raw-payload",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
				SourceRefs: map[string]string{
					"pipeline_slug": "daily-ledger",
				},
				SafeMetadata: map[string]any{
					"raw_payload": map[string]any{"email": "customer@example.com"},
				},
			},
			wantErrContains: `notification config key "raw_payload"`,
		},
		{
			name: "RejectsTokenSourceRef",
			params: CreateCandidateParams{
				OrgSlug:           orgSlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "unsafe:token",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
				SourceRefs: map[string]string{
					"token": "plaintext-secret",
				},
			},
			wantErrContains: `notification source ref "token"`,
		},
		{
			name: "RejectsMissingSourceEvidence",
			params: CreateCandidateParams{
				OrgSlug:           orgSlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "unsafe:missing-source",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
			},
			wantErrContains: "requires source_event_slug or source refs",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := newTestService(&fakeNotificationsRepo{})

			got, err := svc.CreateCandidate(context.Background(), tc.params)

			require.ErrorIs(t, err, ErrInvalidInput)
			require.ErrorContains(t, err, tc.wantErrContains)
			require.Nil(t, got)
		})
	}
}

func TestService_UpsertRecordDeduplicatesUnresolvedCondition(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	recordSlug := must.Slug("pipeline-run-failed-load-users")
	sourceEventSlug := must.Slug("evt-run-failed-001")
	projectSlug := must.Slug("warehouse")
	environmentSlug := must.Slug("prod")
	pipelineSlug := must.Slug("load-users")
	runSlug := must.Slug("run-20260629-001")
	firstSeenAt := time.Date(2026, time.June, 29, 16, 0, 0, 0, time.UTC)
	lastSeenAt := time.Date(2026, time.June, 29, 16, 25, 0, 0, time.UTC)

	svc := newTestServiceWithClock(&fakeNotificationsRepo{
		upsertRecordFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.UpsertRecordParams,
		) (*reponotifications.Record, error) {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, recordSlug, params.Slug)
			require.Equal(t, projectSlug, *params.ProjectSlug)
			require.Equal(t, environmentSlug, *params.EnvironmentSlug)
			require.Equal(t, model.NotificationTypePipelineRunFailed, params.NotificationType)
			require.Equal(t, model.NotificationSeverityCritical, params.Severity)
			require.Equal(t, model.NotificationLifecycleStateOpen, params.LifecycleState)
			require.Equal(t, "pipeline:run_failed:warehouse:prod:load-users", params.DedupeFingerprint)
			require.Equal(t, sourceEventSlug, *params.SourceEventSlug)
			require.Equal(t, model.EventEntityTypePipelineRun, params.SourceEntityType)
			require.Equal(t, model.EventActionExecute, params.SourceAction)
			require.JSONEq(t, `{"pipeline_slug":"load-users","run_slug":"run-20260629-001"}`, string(params.SourceRefs))
			require.JSONEq(t, `{"failure_kind":"contract","summary":"contract failed"}`, string(params.SafeMetadata))

			return &reponotifications.Record{
				OrgSlug:           params.OrgSlug,
				Slug:              params.Slug,
				ProjectSlug:       params.ProjectSlug,
				EnvironmentSlug:   params.EnvironmentSlug,
				NotificationType:  params.NotificationType,
				Severity:          params.Severity,
				LifecycleState:    params.LifecycleState,
				DedupeFingerprint: params.DedupeFingerprint,
				SourceEventSlug:   params.SourceEventSlug,
				SourceEntityType:  params.SourceEntityType,
				SourceAction:      params.SourceAction,
				SourceRefs:        params.SourceRefs,
				SafeMetadata:      params.SafeMetadata,
				FirstSeenAt:       firstSeenAt,
				LastSeenAt:        lastSeenAt,
				OccurrenceCount:   2,
				LastOccurrenceAt:  lastSeenAt,
				DispatchSummary:   json.RawMessage(`{"pending":1}`),
				CreatedAt:         firstSeenAt,
				UpdatedAt:         lastSeenAt,
			}, nil
		},
	}, testutil.FakeClock{NowFn: func() time.Time { return lastSeenAt }})

	got, err := svc.UpsertRecord(context.Background(), UpsertRecordParams{
		OrgSlug:           orgSlug,
		Slug:              recordSlug,
		ProjectSlug:       &projectSlug,
		EnvironmentSlug:   &environmentSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: "pipeline:run_failed:warehouse:prod:load-users",
		SourceEventSlug:   &sourceEventSlug,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": pipelineSlug.String(),
			"run_slug":      runSlug.String(),
		},
		SafeMetadata: map[string]any{
			"failure_kind": "contract",
			"summary":      "contract failed",
		},
	})

	require.NoError(t, err)
	require.Equal(t, recordSlug, got.Slug)
	require.Equal(t, model.NotificationLifecycleStateOpen, got.LifecycleState)
	require.Equal(t, 2, got.OccurrenceCount)
	require.Equal(t, firstSeenAt, got.FirstSeenAt)
	require.Equal(t, lastSeenAt, got.LastSeenAt)
	require.Equal(t, sourceEventSlug, *got.SourceEventSlug)
	require.Equal(t, "run-20260629-001", got.SourceRefs["run_slug"])
}

func TestService_UpsertRecordPreservesResolvedRecordReturnedByRepository(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	recordSlug := must.Slug("pipeline-run-failed-load-users-resolved")
	resolvedAt := time.Date(2026, time.June, 29, 16, 30, 0, 0, time.UTC)
	firstSeenAt := time.Date(2026, time.June, 29, 16, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, time.June, 29, 16, 31, 0, 0, time.UTC)

	svc := newTestServiceWithClock(&fakeNotificationsRepo{
		upsertRecordFn: func(
			_ context.Context,
			_ postgres.DBTX,
			params reponotifications.UpsertRecordParams,
		) (*reponotifications.Record, error) {
			require.Equal(t, model.NotificationLifecycleStateOpen, params.LifecycleState)
			return &reponotifications.Record{
				OrgSlug:           params.OrgSlug,
				Slug:              recordSlug,
				NotificationType:  params.NotificationType,
				Severity:          params.Severity,
				LifecycleState:    model.NotificationLifecycleStateResolved,
				DedupeFingerprint: params.DedupeFingerprint,
				SourceEntityType:  params.SourceEntityType,
				SourceAction:      params.SourceAction,
				SourceRefs:        json.RawMessage(`{"pipeline_slug":"load-users","run_slug":"run-resolved"}`),
				SafeMetadata:      json.RawMessage(`{"summary":"resolved failure"}`),
				FirstSeenAt:       firstSeenAt,
				LastSeenAt:        resolvedAt,
				OccurrenceCount:   3,
				LastOccurrenceAt:  resolvedAt,
				DispatchSummary:   json.RawMessage(`{"delivered":1}`),
				ResolvedAt:        &resolvedAt,
				CreatedAt:         firstSeenAt,
				UpdatedAt:         updatedAt,
			}, nil
		},
	}, testutil.FakeClock{NowFn: func() time.Time { return updatedAt }})

	got, err := svc.UpsertRecord(context.Background(), UpsertRecordParams{
		OrgSlug:           orgSlug,
		Slug:              must.Slug("pipeline-run-failed-load-users-new"),
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: "pipeline:run_failed:warehouse:prod:load-users",
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "load-users",
			"run_slug":      "run-new",
		},
		SafeMetadata: map[string]any{
			"summary": "new failure",
		},
	})

	require.NoError(t, err)
	require.Equal(t, recordSlug, got.Slug)
	require.Equal(t, model.NotificationLifecycleStateResolved, got.LifecycleState)
	require.Equal(t, firstSeenAt, got.FirstSeenAt)
	require.Equal(t, resolvedAt, got.LastSeenAt)
	require.Equal(t, 3, got.OccurrenceCount)
	require.Equal(t, resolvedAt, *got.ResolvedAt)
	require.Equal(t, "run-resolved", got.SourceRefs["run_slug"])
	require.Equal(t, "resolved failure", got.SafeMetadata["summary"])
}

func TestService_UpsertRecordRejectsUnsafeMetadataAndSourceRefs(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	recordSlug := must.Slug("unsafe-notification")
	emptySlug := slug.Slug("")

	tests := []struct {
		name   string
		params UpsertRecordParams
	}{
		{
			name: "RejectsEmptyProjectSlug",
			params: UpsertRecordParams{
				OrgSlug:           orgSlug,
				Slug:              recordSlug,
				ProjectSlug:       &emptySlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "unsafe:empty-project",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
			},
		},
		{
			name: "RejectsEmptyEnvironmentSlug",
			params: UpsertRecordParams{
				OrgSlug:           orgSlug,
				Slug:              recordSlug,
				ProjectSlug:       &orgSlug,
				EnvironmentSlug:   &emptySlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "unsafe:empty-environment",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
			},
		},
		{
			name: "RejectsEnvironmentWithoutProject",
			params: UpsertRecordParams{
				OrgSlug:           orgSlug,
				Slug:              recordSlug,
				EnvironmentSlug:   &orgSlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "unsafe:environment-without-project",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
			},
		},
		{
			name: "RejectsEmptySourceEventSlug",
			params: UpsertRecordParams{
				OrgSlug:           orgSlug,
				Slug:              recordSlug,
				SourceEventSlug:   &emptySlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "unsafe:empty-source-event",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
			},
		},
		{
			name: "RejectsRawPayloadMetadata",
			params: UpsertRecordParams{
				OrgSlug:           orgSlug,
				Slug:              recordSlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "unsafe:raw-payload",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
				SafeMetadata: map[string]any{
					"raw_payload": map[string]any{"email": "customer@example.com"},
				},
			},
		},
		{
			name: "RejectsCredentialSourceRef",
			params: UpsertRecordParams{
				OrgSlug:           orgSlug,
				Slug:              recordSlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "unsafe:credential",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
				SourceRefs: map[string]string{
					"credential": "plaintext-secret",
				},
			},
		},
		{
			name: "RejectsRequestHeaderMetadata",
			params: UpsertRecordParams{
				OrgSlug:           orgSlug,
				Slug:              recordSlug,
				NotificationType:  model.NotificationTypePipelineRunFailed,
				Severity:          model.NotificationSeverityCritical,
				DedupeFingerprint: "unsafe:request-headers",
				SourceEntityType:  model.EventEntityTypePipelineRun,
				SourceAction:      model.EventActionExecute,
				SafeMetadata: map[string]any{
					"request_headers": map[string]any{"authorization": "Bearer token"},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := newTestService(&fakeNotificationsRepo{})

			got, err := svc.UpsertRecord(context.Background(), tc.params)

			require.ErrorIs(t, err, ErrInvalidInput)
			require.Nil(t, got)
		})
	}
}

func TestService_DeliverNotificationOutcomes(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	recordSlug := must.Slug("pipeline-run-failed-daily-ledger")
	alertPolicySlug := must.Slug("critical-pipeline-alerts")
	destinationSlug := must.Slug("ops-email")
	enqueueErr := fmt.Errorf("email queue unavailable")

	tests := []struct {
		name             string
		nonSecretConfig  json.RawMessage
		occurrenceCount  int
		severity         model.NotificationSeverity
		findErr          error
		enqueueErrAtCall int
		wantErr          error
		wantEmailJobs    int
		wantAttempts     int
	}{
		{
			name:            "QueuesEmail",
			nonSecretConfig: json.RawMessage(`{"recipients":[{"email":"Ops <ops@example.com>","locale":"en"}]}`),
			occurrenceCount: 2,
			severity:        model.NotificationSeverityCritical,
			wantEmailJobs:   1,
			wantAttempts:    1,
		},
		{
			name:            "SkipsInvalidConfigWithoutRetry",
			nonSecretConfig: json.RawMessage(`{}`),
			occurrenceCount: 1,
			severity:        model.NotificationSeverityCritical,
		},
		{
			name:            "QueuesInfoSeverityWhenAlertPolicySelectedDestination",
			nonSecretConfig: json.RawMessage(`{"recipients":["ops@example.com"]}`),
			occurrenceCount: 1,
			severity:        model.NotificationSeverityInfo,
			wantEmailJobs:   1,
			wantAttempts:    1,
		},
		{
			name:            "QueuesOneAttemptPerRecipient",
			nonSecretConfig: json.RawMessage(`{"recipients":["ops@example.com","backup@example.com"]}`),
			occurrenceCount: 1,
			severity:        model.NotificationSeverityCritical,
			wantEmailJobs:   2,
			wantAttempts:    2,
		},
		{
			name:             "ReturnsFirstEnqueueFailureForRiverRetry",
			nonSecretConfig:  json.RawMessage(`{"recipients":["ops@example.com"]}`),
			occurrenceCount:  1,
			severity:         model.NotificationSeverityCritical,
			enqueueErrAtCall: 1,
			wantErr:          enqueueErr,
			wantEmailJobs:    1,
			wantAttempts:     0,
		},
		{
			name:             "ReturnsLaterEnqueueFailureForTransactionalRollback",
			nonSecretConfig:  json.RawMessage(`{"recipients":["ops@example.com","backup@example.com"]}`),
			occurrenceCount:  1,
			severity:         model.NotificationSeverityCritical,
			enqueueErrAtCall: 2,
			wantErr:          enqueueErr,
			wantEmailJobs:    2,
			wantAttempts:     1,
		},
		{
			name:    "IgnoresMissingWork",
			findErr: reponotifications.ErrDeliveryNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			emailJobs := 0
			createdAttempts := 0
			queuedAttemptSlugs := map[slug.Slug]struct{}{}

			svc := newTestServiceWithEmailProducer(&fakeNotificationsRepo{
				findDeliveryWorkFn: func(
					_ context.Context,
					_ postgres.DBTX,
					params reponotifications.FindDeliveryWorkParams,
				) (*reponotifications.DeliveryWork, error) {
					require.Equal(t, orgSlug, params.OrgSlug)
					require.Equal(t, recordSlug, params.RecordSlug)
					require.Equal(t, destinationSlug, params.DestinationSlug)
					require.Equal(t, alertPolicySlug, params.AlertPolicySlug)
					if tc.findErr != nil {
						return nil, tc.findErr
					}
					return &reponotifications.DeliveryWork{
						OrganizationName: "Acme",
						OrgSlug:          orgSlug,
						RecordSlug:       recordSlug,
						AlertPolicySlug:  alertPolicySlug,
						DestinationSlug:  destinationSlug,
						DestinationName:  "Ops email",
						DestinationType:  model.AlertDestinationTypeEmail,
						NonSecretConfig:  tc.nonSecretConfig,
						NotificationType: model.NotificationTypePipelineRunFailed,
						Severity:         tc.severity,
						SourceEntityType: model.EventEntityTypePipelineRun,
						SourceAction:     model.EventActionExecute,
						SafeMetadata:     json.RawMessage(`{"summary":"contract failed"}`),
						OccurrenceCount:  tc.occurrenceCount,
					}, nil
				},
				createAlertDeliveryAttemptFn: func(
					_ context.Context,
					_ postgres.DBTX,
					params reponotifications.CreateAlertDeliveryAttemptParams,
				) (*reponotifications.AlertDeliveryAttempt, error) {
					createdAttempts++
					require.Equal(t, orgSlug, params.OrgSlug)
					require.NotEmpty(t, params.Slug)
					require.Equal(t, recordSlug, params.NotificationRecordSlug)
					require.Equal(t, alertPolicySlug, params.AlertPolicySlug)
					require.Equal(t, destinationSlug, params.DestinationSlug)
					require.Equal(t, model.AlertDestinationTypeEmail, params.DestinationType)
					require.Equal(t, model.AlertDeliveryAttemptStatusQueued, params.Status)
					require.Equal(t, 1, params.ProviderAttempt)
					require.Equal(t, "email_send", params.JobKind)
					require.Equal(t, "email", params.JobQueue)
					require.Equal(t, "queued", params.JobState)
					require.JSONEq(t, `{}`, string(params.ProviderResponseMetadata))
					_, queued := queuedAttemptSlugs[params.Slug]
					require.True(t, queued, "attempt must be created for the email job slug")
					return &reponotifications.AlertDeliveryAttempt{Slug: params.Slug}, nil
				},
			}, &testutil.FakeEmailProducer{
				EnqueueNotificationFn: func(
					_ context.Context,
					_ postgres.DBTX,
					gotOrgSlug slug.Slug,
					recipient model.NotificationEmailRecipient,
					notification model.NotificationEmail,
					alertDeliveryAttemptSlug slug.Slug,
				) (queue.QueuedJob, error) {
					emailJobs++
					require.Equal(t, orgSlug, gotOrgSlug)
					require.NotEmpty(t, alertDeliveryAttemptSlug)
					if emailJobs == 1 {
						require.Equal(t, "ops@example.com", recipient.Email)
					}
					if emailJobs == 2 {
						require.Equal(t, "backup@example.com", recipient.Email)
					}
					require.Equal(t, "Acme", notification.OrganizationName)
					require.Equal(t, recordSlug, notification.RecordSlug)
					require.Equal(t, "contract failed", notification.Summary)
					require.Equal(t, tc.occurrenceCount, notification.OccurrenceCount)
					if tc.enqueueErrAtCall == emailJobs {
						return queue.QueuedJob{}, enqueueErr
					}
					queuedAttemptSlugs[alertDeliveryAttemptSlug] = struct{}{}
					return queue.QueuedJob{Kind: "email_send", Queue: "email", State: "queued"}, nil
				},
			})

			err := svc.DeliverNotification(context.Background(), DeliverNotificationParams{
				OrgSlug:         orgSlug,
				RecordSlug:      recordSlug,
				DestinationSlug: destinationSlug,
				AlertPolicySlug: alertPolicySlug,
			})

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantEmailJobs, emailJobs)
			require.Equal(t, tc.wantAttempts, createdAttempts)
		})
	}
}

func TestDecodeNotificationJSONCorruptionIsInternalError(t *testing.T) {
	t.Parallel()

	decodedMap, err := decodeMap(json.RawMessage(`{`), "notification metadata")
	require.ErrorIs(t, err, xerror.Unexpected())
	require.Nil(t, decodedMap)

	decodedStringMap, err := decodeStringMap(json.RawMessage(`{`), "notification source refs")
	require.ErrorIs(t, err, xerror.Unexpected())
	require.Nil(t, decodedStringMap)
}

func (f *fakeNotificationsRepo) ClaimConnectionDelivery(ctx context.Context, db postgres.DBTX, p reponotifications.ClaimConnectionDeliveryParams) (reponotifications.ConnectionDeliveryClaim, error) {
	if f.claimConnectionDeliveryFn == nil {
		return reponotifications.ConnectionDeliveryClaim{}, errUnexpectedNotificationsRepoCall
	}
	return f.claimConnectionDeliveryFn(ctx, db, p)
}

func (f *fakeNotificationsRepo) FinishConnectionDelivery(ctx context.Context, db postgres.DBTX, p reponotifications.FinishConnectionDeliveryParams) error {
	if f.finishConnectionDeliveryFn == nil {
		return errUnexpectedNotificationsRepoCall
	}
	return f.finishConnectionDeliveryFn(ctx, db, p)
}
