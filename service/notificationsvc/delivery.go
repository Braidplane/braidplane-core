package notificationsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	stdmail "net/mail"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
)

const (
	defaultNotificationEmailLocale = "en"
)

// enqueueRecordDeliveries queues one dispatch for each eligible destination and
// records only the notification-owned handoff state inside the caller's unit of work.
func (s *service) enqueueRecordDeliveries(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
) error {
	targets, err := s.repo.ListDeliveryTargetsForRecord(ctx, tx, orgSlug, recordSlug)
	if err != nil {
		return xerror.Wrapf("list notification delivery targets: %w", err)
	}

	for _, target := range targets {
		queuedAt := s.clk.Now().UTC()
		queued, err := s.deliveryProducer.EnqueueNotificationDelivery(ctx, tx, NotificationDeliveryJobParams{
			OrgSlug:         orgSlug,
			RecordSlug:      target.RecordSlug,
			DestinationSlug: target.DestinationSlug,
			AlertPolicySlug: target.AlertPolicySlug,
		})
		if err != nil {
			return xerror.Wrapf("enqueue notification delivery job: %w", err)
		}

		if _, err = s.repo.UpdateRecordDispatchSummary(ctx, tx, reponotifications.UpdateRecordDispatchSummaryParams{
			OrgSlug:         orgSlug,
			RecordSlug:      target.RecordSlug,
			DestinationSlug: target.DestinationSlug,
			Status:          model.NotificationDispatchStatusQueued,
			QueuedAt:        queuedAt,
			JobKind:         queued.Kind,
			JobQueue:        queued.Queue,
			JobState:        queued.State,
		}); err != nil {
			return xerror.Wrapf("update queued notification dispatch summary: %w", err)
		}
	}

	return nil
}

// DeliverNotification hands one queued notification dispatch to the matching
// adapter. Managed provider requests run after the dispatch transaction commits.
func (s *service) DeliverNotification(ctx context.Context, params DeliverNotificationParams) error {
	if params.Test {
		if params.OrgSlug == "" || params.DestinationSlug == "" {
			return ErrInvalidInput
		}
		return s.deliverConnectionTest(ctx, params)
	}
	if err := validateDeliverNotificationInput(params); err != nil {
		return err
	}

	work, err := postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (reponotifications.DeliveryWork, error) {
		work, err := s.repo.FindDeliveryWork(ctx, tx, reponotifications.FindDeliveryWorkParams{
			OrgSlug:         params.OrgSlug,
			RecordSlug:      params.RecordSlug,
			DestinationSlug: params.DestinationSlug,
			AlertPolicySlug: params.AlertPolicySlug,
		})
		if err != nil {
			if xerror.Is(err, reponotifications.ErrDeliveryNotFound) {
				return reponotifications.DeliveryWork{}, nil
			}
			return reponotifications.DeliveryWork{}, xerror.Wrapf("find notification delivery work: %w", err)
		}

		if work.DestinationType == model.AlertDestinationTypeSlack {
			return *work, nil
		}
		if work.DestinationType != model.AlertDestinationTypeEmail {
			s.logNotificationDeliverySkipped(ctx, work, "unsupported_destination_type")
			return reponotifications.DeliveryWork{}, nil
		}

		recipients, err := parseNotificationEmailRecipients(work.NonSecretConfig)
		if err != nil {
			slog.LogAttrs(ctx, slog.LevelWarn, "notification email destination config invalid",
				append(deliveryWorkLogAttrs(work), slog.Any("error", err))...)
			return reponotifications.DeliveryWork{}, nil
		}

		notification := notificationEmailFromWork(work)
		for _, recipient := range recipients {
			queuedAt := s.clk.Now().UTC()
			attemptSlug, slugErr := newAlertDeliveryAttemptSlug()
			if slugErr != nil {
				return reponotifications.DeliveryWork{}, xerror.Wrapf("generate alert delivery attempt slug: %w", slugErr)
			}
			queued, enqueueErr := s.emailProducer.EnqueueNotification(
				ctx,
				tx,
				work.OrgSlug,
				recipient,
				notification,
				attemptSlug,
			)
			if enqueueErr != nil {
				return reponotifications.DeliveryWork{}, xerror.Wrapf("enqueue notification email job: %w", enqueueErr)
			}

			if _, createErr := s.repo.CreateAlertDeliveryAttempt(ctx, tx, reponotifications.CreateAlertDeliveryAttemptParams{
				OrgSlug:                  work.OrgSlug,
				Slug:                     attemptSlug,
				NotificationRecordSlug:   work.RecordSlug,
				AlertPolicySlug:          work.AlertPolicySlug,
				DestinationSlug:          work.DestinationSlug,
				DestinationType:          work.DestinationType,
				Status:                   model.AlertDeliveryAttemptStatusQueued,
				ProviderAttempt:          1,
				JobKind:                  queued.Kind,
				JobQueue:                 queued.Queue,
				JobState:                 queued.State,
				ProviderResponseMetadata: json.RawMessage(`{}`),
				QueuedAt:                 queuedAt,
			}); createErr != nil {
				return reponotifications.DeliveryWork{}, xerror.Wrapf("create queued alert delivery attempt: %w", createErr)
			}
		}

		return reponotifications.DeliveryWork{}, nil
	})
	if err != nil {
		return err
	}
	if work.DestinationType == model.AlertDestinationTypeSlack {
		return s.deliverConnectionNotification(ctx, &work)
	}
	return nil
}

func (s *service) logNotificationDeliverySkipped(
	ctx context.Context,
	work *reponotifications.DeliveryWork,
	reason string,
) {
	slog.LogAttrs(ctx, slog.LevelWarn, "notification delivery skipped",
		append(deliveryWorkLogAttrs(work), slog.String("reason", reason))...)
}

func deliveryWorkLogAttrs(work *reponotifications.DeliveryWork) []slog.Attr {
	return []slog.Attr{
		slog.String("organization_slug", work.OrgSlug.String()),
		slog.String("record_slug", work.RecordSlug.String()),
		slog.String("destination_slug", work.DestinationSlug.String()),
	}
}

// parseNotificationEmailRecipients returns recipients from normalized destination config.
func parseNotificationEmailRecipients(raw json.RawMessage) ([]model.NotificationEmailRecipient, error) {
	var config struct {
		Recipients json.RawMessage `json:"recipients"`
	}
	if len(raw) == 0 {
		return nil, ErrInvalidInput.Detail("notification email destination config is required")
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	if len(config.Recipients) == 0 || string(config.Recipients) == "null" {
		return nil, ErrInvalidInput.Detail("notification email destination recipients are required")
	}

	var entries []json.RawMessage
	if err := json.Unmarshal(config.Recipients, &entries); err != nil {
		return nil, ErrInvalidInput.With(err)
	}

	ret := make([]model.NotificationEmailRecipient, 0, len(entries))
	for _, entry := range entries {
		recipient, err := parseNotificationEmailRecipient(entry)
		if err != nil {
			return nil, err
		}
		ret = append(ret, recipient)
	}
	if len(ret) == 0 {
		return nil, ErrInvalidInput.Detail("notification email destination recipients are required")
	}
	return ret, nil
}

// parseNotificationEmailRecipient accepts the string and object recipient shapes.
func parseNotificationEmailRecipient(raw json.RawMessage) (model.NotificationEmailRecipient, error) {
	var recipientString string
	if err := json.Unmarshal(raw, &recipientString); err == nil {
		return notificationEmailRecipientFromAddress(recipientString, "", "")
	}

	var recipientObject struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		Locale   string `json:"locale"`
	}
	if err := json.Unmarshal(raw, &recipientObject); err != nil {
		return model.NotificationEmailRecipient{}, ErrInvalidInput.With(err)
	}
	fullName := strings.TrimSpace(recipientObject.FullName)
	if fullName == "" {
		fullName = strings.TrimSpace(recipientObject.Name)
	}
	return notificationEmailRecipientFromAddress(recipientObject.Email, fullName, recipientObject.Locale)
}

// notificationEmailRecipientFromAddress validates an email address and applies safe defaults.
func notificationEmailRecipientFromAddress(
	rawEmail string,
	fullName string,
	locale string,
) (model.NotificationEmailRecipient, error) {
	address, err := stdmail.ParseAddress(strings.TrimSpace(rawEmail))
	if err != nil {
		return model.NotificationEmailRecipient{}, ErrInvalidInput.With(err)
	}
	name := strings.TrimSpace(fullName)
	if name == "" {
		name = strings.TrimSpace(address.Name)
	}
	locale = strings.TrimSpace(locale)
	if locale == "" {
		locale = defaultNotificationEmailLocale
	}
	return model.NotificationEmailRecipient{
		Email:    address.Address,
		FullName: name,
		Locale:   locale,
	}, nil
}

// notificationEmailFromWork builds the render contract from safe persisted fields.
func notificationEmailFromWork(work *reponotifications.DeliveryWork) model.NotificationEmail {
	return model.NotificationEmail{
		OrganizationName: work.OrganizationName,
		RecordSlug:       work.RecordSlug,
		NotificationType: work.NotificationType,
		Severity:         work.Severity,
		Summary:          notificationSummary(work.SafeMetadata, work.RecordSlug),
		SourceEntityType: work.SourceEntityType,
		SourceAction:     work.SourceAction,
		OccurrenceCount:  work.OccurrenceCount,
	}
}

// notificationSummary uses safe metadata when present and falls back to the record slug.
func notificationSummary(raw json.RawMessage, recordSlug slug.Slug) string {
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err == nil {
		if summary, ok := metadata["summary"].(string); ok && strings.TrimSpace(summary) != "" {
			return strings.TrimSpace(summary)
		}
	}
	return fmt.Sprintf("Open notification %s", recordSlug)
}

// newAlertDeliveryAttemptSlug generates a safe unique identity for one alert delivery attempt.
func newAlertDeliveryAttemptSlug() (slug.Slug, error) {
	return slug.WithRandomSuffix(slug.Slug("alert-delivery"), 8)
}
