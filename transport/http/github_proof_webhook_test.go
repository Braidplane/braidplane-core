package http

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	githubprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/github"
	"github.com/Braidplane/braidplane-core/pkg/queue"
)

func TestGitHubProofWebhookAcceptsOnlyAuthenticatedBoundedDeliveries(t *testing.T) {
	t.Parallel()
	secret := []byte("01234567890123456789012345678901")
	validBody := githubPullRequestFixture("synchronize")
	tests := []struct {
		name       string
		body       []byte
		signature  string
		event      string
		deliveryID string
		wantStatus int
		wantCalls  int
		fiberLimit bool
	}{
		{name: "Accepted", body: validBody, signature: webhookSignature(secret, validBody), event: "pull_request", deliveryID: "delivery-1", wantStatus: nethttp.StatusAccepted, wantCalls: 1},
		{name: "InvalidSignature", body: validBody, signature: "sha256=bad", event: "pull_request", deliveryID: "delivery-2", wantStatus: nethttp.StatusUnauthorized},
		{name: "WrongEvent", body: validBody, signature: webhookSignature(secret, validBody), event: "push", deliveryID: "delivery-3", wantStatus: nethttp.StatusBadRequest},
		{name: "InvalidDeliveryID", body: validBody, signature: webhookSignature(secret, validBody), event: "pull_request", deliveryID: "unsafe\nvalue", wantStatus: nethttp.StatusBadRequest},
		{name: "IgnoredAction", body: githubPullRequestFixture("labeled"), event: "pull_request", deliveryID: "delivery-4", wantStatus: nethttp.StatusNoContent},
		{name: "Oversized", body: bytes.Repeat([]byte("x"), gitprovider.MaxChangeWebhookBytes+1), event: "pull_request", deliveryID: "delivery-5", fiberLimit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.signature == "" {
				tt.signature = webhookSignature(secret, tt.body)
			}
			acceptor := &recordingProofWebhookAcceptor{}
			var logs bytes.Buffer
			app := fiber.New(fiber.Config{BodyLimit: gitprovider.MaxChangeWebhookBytes})
			require.NoError(t, RegisterGitHubProofWebhookRoute(app, acceptor, acceptor, secret, slog.New(slog.NewJSONHandler(&logs, nil))))
			req := httptest.NewRequestWithContext(t.Context(), nethttp.MethodPost, GitHubProofWebhookPath, bytes.NewReader(tt.body))
			req.Header.Set("X-Hub-Signature-256", tt.signature)
			req.Header.Set("X-GitHub-Event", tt.event)
			req.Header.Set("X-GitHub-Delivery", tt.deliveryID)
			resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
			if tt.fiberLimit {
				require.ErrorIs(t, err, fasthttp.ErrBodyTooLarge)
				require.Nil(t, resp)
				require.Empty(t, acceptor.events)
				require.NotContains(t, logs.String(), gitprovider.ErrChangePayloadTooLarge.Code())
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
			require.Equal(t, tt.wantStatus, resp.StatusCode)
			require.Len(t, acceptor.events, tt.wantCalls)
		})
	}
}

func TestGitHubProofWebhookPersistsOnlyNormalizedIdentity(t *testing.T) {
	t.Parallel()
	secret := []byte("01234567890123456789012345678901")
	body := githubPullRequestFixture("opened")
	acceptor := &recordingProofWebhookAcceptor{}
	var logs bytes.Buffer
	app := fiber.New(fiber.Config{BodyLimit: gitprovider.MaxChangeWebhookBytes})
	require.NoError(t, RegisterGitHubProofWebhookRoute(app, acceptor, acceptor, secret, slog.New(slog.NewJSONHandler(&logs, nil))))
	req := httptest.NewRequestWithContext(t.Context(), nethttp.MethodPost, GitHubProofWebhookPath, bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", webhookSignature(secret, body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "delivery-safe")
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	require.Equal(t, nethttp.StatusAccepted, resp.StatusCode)
	require.Len(t, acceptor.events, 1)
	require.Equal(t, "9001", acceptor.events[0].ProviderConnectionID)
	require.Equal(t, "42", acceptor.events[0].RepositoryID)
	require.Equal(t, "delivery-safe", acceptor.events[0].DeliveryID)
	require.Contains(t, logs.String(), "delivery-safe")
	require.NotContains(t, logs.String(), "request_secret")
	require.NotContains(t, logs.String(), "must-not-survive")
	require.NotContains(t, logs.String(), "X-Hub-Signature-256")
}

func TestGitHubProofWebhookLogsOnlySafeEnqueueFailureCode(t *testing.T) {
	t.Parallel()
	secret := []byte("01234567890123456789012345678901")
	body := githubPullRequestFixture("opened")
	acceptor := &recordingProofWebhookAcceptor{err: gitprovider.ErrChangeUnavailable.Detail("must-not-log")}
	var logs bytes.Buffer
	app := fiber.New(fiber.Config{BodyLimit: gitprovider.MaxChangeWebhookBytes})
	require.NoError(t, RegisterGitHubProofWebhookRoute(app, acceptor, acceptor, secret, slog.New(slog.NewJSONHandler(&logs, nil))))
	req := httptest.NewRequestWithContext(t.Context(), nethttp.MethodPost, GitHubProofWebhookPath, bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", webhookSignature(secret, body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "delivery-safe")
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	require.Equal(t, nethttp.StatusServiceUnavailable, resp.StatusCode)
	require.Contains(t, logs.String(), gitprovider.ErrChangeUnavailable.Code())
	require.NotContains(t, logs.String(), "must-not-log")
}

func githubPullRequestFixture(action string) []byte {
	return []byte(`{"action":"` + action + `","request_secret":"must-not-survive","installation":{"id":9001},"repository":{"id":42},"pull_request":{"number":7,"state":"open","head":{"sha":"a123456789012345678901234567890123456789","ref":"feature"},"base":{"ref":"main"}}}`)
}

func webhookSignature(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type recordingProofWebhookAcceptor struct {
	events []gitprovider.VerifiedChangeEvent
	err    error
}

func (a *recordingProofWebhookAcceptor) Accept(_ context.Context, event gitprovider.VerifiedChangeEvent) (queue.QueuedJob, error) {
	a.events = append(a.events, event)
	return queue.QueuedJob{Kind: "proof_webhook", Queue: "pipeline_git", State: "queued"}, a.err
}

func (*recordingProofWebhookAcceptor) ApplyLifecycle(context.Context, githubprovider.InstallationLifecycleEvent) error {
	return nil
}
