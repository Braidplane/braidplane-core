package notificationsvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
)

type Service interface {
	CreateAlertDestination(
		ctx context.Context,
		principal AlertManagementPrincipal,
		params CreateAlertDestinationParams,
	) (*model.AlertDestination, error)
	ListAlertDestinations(
		ctx context.Context,
		principal AlertManagementPrincipal,
		orgSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[*model.AlertDestination], error)
	GetAlertDestination(
		ctx context.Context,
		principal AlertManagementPrincipal,
		orgSlug slug.Slug,
		destinationSlug slug.Slug,
	) (*model.AlertDestination, error)
	TestAlertDestination(
		ctx context.Context,
		principal AlertManagementPrincipal,
		params TestAlertDestinationParams,
	) (*AlertDestinationTestResult, error)
	UpdateAlertDestination(
		ctx context.Context,
		principal AlertManagementPrincipal,
		params UpdateAlertDestinationParams,
	) (*model.AlertDestination, error)
	DeleteAlertDestination(
		ctx context.Context,
		principal AlertManagementPrincipal,
		orgSlug slug.Slug,
		destinationSlug slug.Slug,
	) error
	CreateAlertPolicy(ctx context.Context, principal AlertManagementPrincipal, params CreateAlertPolicyParams) (*model.AlertPolicy, error)
	ListAlertPolicies(
		ctx context.Context,
		principal AlertManagementPrincipal,
		orgSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[*model.AlertPolicy], error)
	GetAlertPolicy(ctx context.Context, principal AlertManagementPrincipal, orgSlug, policySlug slug.Slug) (*model.AlertPolicy, error)
	UpdateAlertPolicy(ctx context.Context, principal AlertManagementPrincipal, params UpdateAlertPolicyParams) (*model.AlertPolicy, error)
	DeleteAlertPolicy(ctx context.Context, principal AlertManagementPrincipal, orgSlug, policySlug slug.Slug) error
	CreateCandidate(ctx context.Context, params CreateCandidateParams) (*CreateCandidateResult, error)
	UpsertRecord(ctx context.Context, params UpsertRecordParams) (*model.NotificationRecord, error)
	ListNotifications(
		ctx context.Context,
		orgSlug slug.Slug,
		filters ListNotificationsFilters,
		req model.ListRequest,
	) (model.ListResponse[*model.NotificationListItem], error)
	GetNotification(
		ctx context.Context,
		orgSlug slug.Slug,
		recordSlug slug.Slug,
	) (*model.NotificationListItem, error)
	AcknowledgeNotification(
		ctx context.Context,
		principal AlertManagementPrincipal,
		orgSlug slug.Slug,
		recordSlug slug.Slug,
	) (*model.NotificationListItem, error)
	ResolveNotification(
		ctx context.Context,
		principal AlertManagementPrincipal,
		orgSlug slug.Slug,
		recordSlug slug.Slug,
	) (*model.NotificationListItem, error)
	ListAlertDeliveryAttempts(
		ctx context.Context,
		orgSlug slug.Slug,
		recordSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[*model.AlertDeliveryAttempt], error)
	ListRecentAlertDeliveryAttemptsForRecords(
		ctx context.Context,
		orgSlug slug.Slug,
		recordSlugs []slug.Slug,
		limitPerRecord int,
	) ([]*model.AlertDeliveryAttempt, error)
	DeliverNotification(ctx context.Context, params DeliverNotificationParams) error
	MarkAlertDeliveryAttemptSent(ctx context.Context, params MarkAlertDeliveryAttemptSentParams) error
	MarkAlertDeliveryAttemptFailed(ctx context.Context, params MarkAlertDeliveryAttemptFailedParams) error
}

type NotificationDeliveryProducer interface {
	EnqueueNotificationDelivery(
		ctx context.Context,
		tx postgres.DBTX,
		params NotificationDeliveryJobParams,
	) (queue.QueuedJob, error)
}

// CandidateProducer durably hands any notification observation to the same
// candidate worker. It joins the source transaction; provider I/O stays downstream.
type CandidateProducer interface {
	EnqueueCandidate(context.Context, postgres.DBTX, CreateCandidateParams) error
}

type NotificationEmailProducer interface {
	EnqueueNotification(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		recipient model.NotificationEmailRecipient,
		notification model.NotificationEmail,
		alertDeliveryAttemptSlug slug.Slug,
	) (queue.QueuedJob, error)
}

type service struct {
	frontendURL         string
	txm                 postgres.TransactionManager
	repo                reponotifications.Repository
	clk                 clock.Clock
	deliveryProducer    NotificationDeliveryProducer
	emailProducer       NotificationEmailProducer
	eventProducer       LifecycleEventProducer
	connectionProviders ConnectionProviders
}

// New creates a notification service with repository-backed persistence.
func New(
	txm postgres.TransactionManager,
	repo reponotifications.Repository,
	clk clock.Clock,
	deliveryProducer NotificationDeliveryProducer,
	emailProducer NotificationEmailProducer,
	eventProducer LifecycleEventProducer,
	connectionProviders ConnectionProviders,
	frontendURL string,
) Service {
	if txm == nil {
		panic("nil TransactionManager passed to notificationsvc.New")
	}
	if repo == nil {
		panic("nil notifications Repository passed to notificationsvc.New")
	}
	if clk == nil {
		panic("nil Clock passed to notificationsvc.New")
	}
	if deliveryProducer == nil {
		panic("nil NotificationDeliveryProducer passed to notificationsvc.New")
	}
	if emailProducer == nil {
		panic("nil NotificationEmailProducer passed to notificationsvc.New")
	}
	if connectionProviders == nil {
		panic("nil ConnectionProviders passed to notificationsvc.New")
	}
	for _, provider := range connectionProviders {
		if provider == nil {
			panic("nil ConnectionProvider passed to notificationsvc.New")
		}
	}
	if eventProducer == nil {
		panic("nil EventProducer passed to notificationsvc.New")
	}

	return &service{
		frontendURL:         frontendURL,
		txm:                 txm,
		repo:                repo,
		clk:                 clk,
		deliveryProducer:    deliveryProducer,
		emailProducer:       emailProducer,
		eventProducer:       eventProducer,
		connectionProviders: connectionProviders,
	}
}
