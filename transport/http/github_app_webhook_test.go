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

	githubprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/github"
)

func TestGitHubAppWebhookAppliesAuthenticatedLifecycleEvents(t *testing.T) {
	t.Parallel()
	secret := []byte("01234567890123456789012345678901")
	body := []byte(`{"action":"suspend","installation":{"id":9001}}`)
	acceptor := &recordingGitHubAppLifecycleAcceptor{}
	app := fiber.New()
	require.NoError(t, RegisterGitHubAppWebhookRoute(app, acceptor, secret, slog.New(slog.DiscardHandler)))

	req := httptest.NewRequestWithContext(t.Context(), nethttp.MethodPost, GitHubAppEndpointPath, bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", githubAppWebhookSignature(secret, body))
	req.Header.Set("X-GitHub-Event", "installation")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	require.Equal(t, nethttp.StatusNoContent, resp.StatusCode)
	require.Len(t, acceptor.events, 1)
	require.Equal(t, int64(9001), acceptor.events[0].InstallationID)

	req = httptest.NewRequestWithContext(t.Context(), nethttp.MethodPost, GitHubAppEndpointPath, bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256=bad")
	req.Header.Set("X-GitHub-Event", "installation")
	req.Header.Set("X-GitHub-Delivery", "delivery-2")
	resp, err = app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	require.Equal(t, nethttp.StatusUnauthorized, resp.StatusCode)
	require.Len(t, acceptor.events, 1)
}

func githubAppWebhookSignature(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type recordingGitHubAppLifecycleAcceptor struct {
	events []githubprovider.InstallationLifecycleEvent
}

func (a *recordingGitHubAppLifecycleAcceptor) ApplyLifecycle(_ context.Context, event githubprovider.InstallationLifecycleEvent) error {
	a.events = append(a.events, event)
	return nil
}
