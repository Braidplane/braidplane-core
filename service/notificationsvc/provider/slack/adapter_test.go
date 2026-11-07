package slack

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/must"
	protocol "github.com/Braidplane/braidplane-core/pkg/slack"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/stretchr/testify/require"
)

type resolverFunc func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error)

func (f resolverFunc) ResolveRuntimeCredential(ctx context.Context, p credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
	return f(ctx, p)
}

type senderFunc func(context.Context, string, string) error

func (f senderFunc) SendWebhook(ctx context.Context, url, text string) error {
	return f(ctx, url, text)
}

func TestSlackNotificationAdapterResolvesTenantCredentialAndClassifiesSafeErrors(t *testing.T) {
	for _, tc := range []struct {
		name          string
		providerError error
		retry         bool
		delay         time.Duration
	}{
		{name: "success"},
		{name: "rate_limit", providerError: &protocol.RateLimitError{RetryAfter: 42 * time.Second}, retry: true, delay: 42 * time.Second},
		{name: "revoked_webhook", providerError: xerror.InvalidCredentials(), retry: false},
		{name: "timeout", providerError: fmt.Errorf("secret-webhook-url-and-token"), retry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secret := []byte(`{"webhook_url":"https://hooks.slack.com/services/TTEST/BTEST/local-test-secret"}`)
			adapter := New(resolverFunc(func(_ context.Context, p credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
				require.Equal(t, must.Slug("acme"), p.Owner.OrgSlug)
				require.Equal(t, must.Slug("ops"), p.Owner.NotificationConnectionSlug)
				require.Empty(t, p.Owner.DataSourceSlug)
				return credentialssvc.RuntimeCredential{PlaintextJSON: secret}, nil
			}), senderFunc(func(_ context.Context, url, text string) error {
				require.Contains(t, url, "local-test-secret")
				return tc.providerError
			}))
			err := adapter.Send(t.Context(), must.Slug("acme"), must.Slug("ops"), "Run failed")
			require.Equal(t, make([]byte, len(secret)), secret, "temporary credential buffer cleared")
			if tc.providerError == nil {
				require.NoError(t, err)
				return
			}
			var safe *notificationsvc.DeliveryError
			require.ErrorAs(t, err, &safe)
			require.Equal(t, tc.retry, safe.Retryable)
			require.Equal(t, tc.delay, safe.RetryAfter)
			require.NotContains(t, fmt.Sprintf("%+v", err), "secret")
		})
	}
}
