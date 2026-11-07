package slack

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slack"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
)

type CredentialResolver interface {
	ResolveRuntimeCredential(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error)
}

type WebhookSender interface {
	SendWebhook(context.Context, string, string) error
}

type Adapter struct {
	credentials CredentialResolver
	sender      WebhookSender
}

func New(credentials CredentialResolver, sender WebhookSender) *Adapter {
	if credentials == nil || sender == nil {
		panic("Slack notification credentials and webhook sender are required")
	}
	return &Adapter{credentials: credentials, sender: sender}
}

func (a *Adapter) Send(ctx context.Context, org, connection slug.Slug, text string) error {
	credential, err := a.credentials.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{
		Owner: credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindNotificationConnection, OrgSlug: org, NotificationConnectionSlug: connection},
	})
	if err != nil {
		if xerror.Is(err, credentialssvc.ErrProviderGrantRevoked) || xerror.Is(err, credentialssvc.ErrCredentialNotFound) {
			return &notificationsvc.DeliveryError{Category: "connection_disconnected", Message: "Reconnect the notification connection before delivering alerts"}
		}
		return &notificationsvc.DeliveryError{Category: "credential_unavailable", Message: "Notification credentials are unavailable", Retryable: true}
	}
	defer clear(credential.PlaintextJSON)
	var payload struct {
		WebhookURL string `json:"webhook_url"`
	}
	if err := json.Unmarshal(credential.PlaintextJSON, &payload); err != nil || payload.WebhookURL == "" {
		return &notificationsvc.DeliveryError{Category: "invalid_credentials", Message: "Reconnect the notification connection"}
	}
	if err := a.sender.SendWebhook(ctx, payload.WebhookURL, text); err != nil {
		var rateLimit *slack.RateLimitError
		if xerror.As(err, &rateLimit) {
			delay := rateLimit.RetryAfter
			if delay <= 0 {
				delay = 30 * time.Second
			}
			return &notificationsvc.DeliveryError{Category: "rate_limited", Message: "Slack rate limit reached", Retryable: true, RetryAfter: delay}
		}
		var typed *xerror.Error
		if xerror.As(err, &typed) && (typed.Kind() == xerror.KindInvalidInput || typed.Kind() == xerror.KindUnauthorized || typed.Kind() == xerror.KindForbidden) {
			return &notificationsvc.DeliveryError{Category: "provider_rejected", Message: "Slack rejected delivery; check the connection and channel"}
		}
		return &notificationsvc.DeliveryError{Category: "provider_unavailable", Message: "Slack delivery is temporarily unavailable", Retryable: true}
	}
	return nil
}

var _ notificationsvc.ConnectionProvider = (*Adapter)(nil)
