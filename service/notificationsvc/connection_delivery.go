package notificationsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
)

// ConnectionProvider sends through a managed notification connection, without exposing its credentials.
type ConnectionProvider interface {
	Send(context.Context, slug.Slug, slug.Slug, string) error
}

type ConnectionProviders map[model.AlertDestinationType]ConnectionProvider

// DeliveryError contains only safe diagnostics and the provider's retry instruction.
type DeliveryError struct {
	Category   string
	Message    string
	Retryable  bool
	RetryAfter time.Duration
}

func (e *DeliveryError) Error() string { return e.Message }

func connectionSlug(raw json.RawMessage) (slug.Slug, error) {
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		return "", ErrInvalidInput.Detail("Invalid notification connection configuration")
	}
	if err := validateAlertDestinationConfig(model.AlertDestinationTypeSlack, config); err != nil {
		return "", err
	}
	return slug.New(config["connection_slug"].(string))
}

func (s *service) sendConnectionMessage(ctx context.Context, org slug.Slug, destinationType model.AlertDestinationType, raw json.RawMessage, text string) error {
	connection, err := connectionSlug(raw)
	if err != nil {
		return &DeliveryError{Category: "invalid_configuration", Message: "Notification connection configuration is invalid"}
	}
	provider, ok := s.connectionProviders[destinationType]
	if !ok {
		return &DeliveryError{Category: "provider_not_configured", Message: "Notification provider is not configured"}
	}
	return provider.Send(ctx, org, connection, text)
}

func (s *service) deliverConnectionNotification(ctx context.Context, work *reponotifications.DeliveryWork) error {
	text := fmt.Sprintf("%s: %s\n%s\nNotification: %s", work.OrganizationName, work.NotificationType.String(), notificationSummary(work.SafeMetadata, work.RecordSlug), work.RecordSlug)
	refs, err := decodeStringMap(work.SourceRefs, "notification source refs")
	if err != nil {
		return err
	}
	switch {
	case refs["project_slug"] != "" && refs["environment_slug"] != "" && refs["run_slug"] != "":
		runURL := strings.TrimRight(s.frontendURL, "/") + "/orgs/" + url.PathEscape(work.OrgSlug.String()) + "/projects/" + url.PathEscape(refs["project_slug"]) + "/runs/" + url.PathEscape(refs["run_slug"]) + "?environment_slug=" + url.QueryEscape(refs["environment_slug"])
		text += "\nView run: " + runURL
	case refs["project_slug"] != "" && refs["environment_slug"] != "" && refs["pipeline_slug"] != "" && refs["validation_result_slug"] != "":
		validationURL := strings.TrimRight(s.frontendURL, "/") + "/orgs/" + url.PathEscape(work.OrgSlug.String()) + "/projects/" + url.PathEscape(refs["project_slug"]) + "/pipelines/" + url.PathEscape(refs["pipeline_slug"]) + "?environment_slug=" + url.QueryEscape(refs["environment_slug"]) + "&section=deployments#pipeline-validation"
		text += "\nValidation result: " + refs["validation_result_slug"] + "\nView validation history: " + validationURL
	case work.NotificationType == model.NotificationTypePipelineFreshnessSLABreached && refs["project_slug"] != "" && refs["environment_slug"] != "" && refs["pipeline_slug"] != "":
		recencyURL := strings.TrimRight(s.frontendURL, "/") + "/orgs/" + url.PathEscape(work.OrgSlug.String()) + "/projects/" + url.PathEscape(refs["project_slug"]) + "/pipelines/" + url.PathEscape(refs["pipeline_slug"]) + "/environments/" + url.PathEscape(refs["environment_slug"]) + "/recency"
		text += "\nView successful-run recency: " + recencyURL
	}

	attemptSlug, err := newAlertDeliveryAttemptSlug()
	if err != nil {
		return err
	}
	now := s.clk.Now().UTC()
	claim, err := postgres.InTx(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (reponotifications.ConnectionDeliveryClaim, error) {
		return s.repo.ClaimConnectionDelivery(ctx, db, reponotifications.ClaimConnectionDeliveryParams{
			OrgSlug: work.OrgSlug, RecordSlug: work.RecordSlug, DestinationSlug: work.DestinationSlug,
			AlertPolicySlug: work.AlertPolicySlug, AttemptSlug: attemptSlug, Now: now, LeaseUntil: now.Add(time.Minute),
		})
	})
	if err != nil {
		return err
	}
	if claim.Status == model.AlertDeliveryAttemptStatusSent || claim.JobState == "cancelled" {
		return nil
	}
	if !claim.Acquired {
		delay := time.Second
		for _, deadline := range []*time.Time{claim.LeaseUntil, claim.RetryAfterAt} {
			if deadline != nil && deadline.Sub(now) > delay {
				delay = deadline.Sub(now)
			}
		}
		return &DeliveryError{Category: "delivery_pending", Message: "Notification delivery is waiting for its next attempt", Retryable: true, RetryAfter: delay}
	}
	var deliveryErr error
	if claim.ProviderAttempt > 10 {
		deliveryErr = &DeliveryError{Category: "retry_exhausted", Message: "Notification delivery retry limit reached"}
	} else {
		// Provider I/O runs after the claim commits; no database transaction spans the request.
		deliveryErr = s.sendConnectionMessage(ctx, work.OrgSlug, work.DestinationType, work.NonSecretConfig, text)
	}
	finished := reponotifications.FinishConnectionDeliveryParams{
		OrgSlug: work.OrgSlug, AttemptSlug: claim.Slug, ProviderAttempt: claim.ProviderAttempt,
		Status: model.AlertDeliveryAttemptStatusSent, Now: s.clk.Now().UTC(), JobState: "completed", ProviderResponseMetadata: json.RawMessage(`{}`),
	}
	if deliveryErr != nil {
		safe := &DeliveryError{Category: "provider_unavailable", Message: "Notification provider is unavailable", Retryable: true}
		var classified *DeliveryError
		if xerror.As(deliveryErr, &classified) {
			safe = classified
		}
		if claim.ProviderAttempt >= 10 {
			safe = &DeliveryError{Category: "retry_exhausted", Message: "Notification delivery retry limit reached"}
		}
		deliveryErr = safe
		finished.Status = model.AlertDeliveryAttemptStatusFailed
		finished.JobState = "cancelled"
		finished.ErrorCategory, finished.ErrorMessage = &safe.Category, &safe.Message
		if safe.Retryable {
			finished.JobState = "retryable"
			delay := safe.RetryAfter
			if delay <= 0 {
				delay = time.Duration(claim.ProviderAttempt) * 30 * time.Second
			}
			retryAt := finished.Now.Add(delay)
			finished.RetryAfterAt = &retryAt
			safe.RetryAfter = delay
			finished.ProviderResponseMetadata, err = json.Marshal(map[string]string{"retry_at": retryAt.Format(time.RFC3339)})
			if err != nil {
				return xerror.Wrapf("encode delivery retry metadata: %w", err)
			}
		}
	}
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.txm.RunInTx(finalizeCtx, func(ctx context.Context, db postgres.DBTX) error {
		return s.repo.FinishConnectionDelivery(ctx, db, finished)
	}); err != nil {
		return err
	}
	return deliveryErr
}

func (s *service) deliverConnectionTest(ctx context.Context, params DeliverNotificationParams) error {
	destination, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*reponotifications.AlertDestination, error) {
		return s.repo.FindAlertDestinationBySlug(ctx, db, params.OrgSlug, params.DestinationSlug)
	})
	if xerror.Is(err, ErrDestinationNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !destination.Enabled || destination.Type != model.AlertDestinationTypeSlack {
		return nil
	}
	return s.sendConnectionMessage(ctx, params.OrgSlug, destination.Type, destination.NonSecretConfig, "Braidplane alert destination test: "+destination.Name)
}
