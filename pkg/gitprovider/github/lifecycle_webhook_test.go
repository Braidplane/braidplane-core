package github

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifyInstallationLifecycleWebhookNormalizesRepositoryChanges(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"added","installation":{"id":9001},"repositories_added":[{"id":42,"full_name":"acme/pipelines","name":"pipelines","owner":{"login":"acme"}}],"repositories_removed":[]}`)
	event, err := VerifyInstallationLifecycleWebhook([]byte("secret"), signLifecycle([]byte("secret"), body), "installation_repositories", "delivery-5", body)
	require.NoError(t, err)
	require.Equal(t, int64(9001), event.InstallationID)
	require.Equal(t, "acme/pipelines", event.Added[0].FullName)

	_, err = VerifyInstallationLifecycleWebhook([]byte("secret"), "sha256=bad", "installation_repositories", "delivery-5", body)
	require.ErrorIs(t, err, ErrInstallationLifecycleAuthentication)

	_, err = VerifyInstallationLifecycleWebhook([]byte("secret"), signLifecycle([]byte("secret"), body), "installation_repositories", "unsafe\nvalue", body)
	require.Error(t, err)

	oversized := bytes.Repeat([]byte("x"), maxInstallationLifecycleWebhookBytes+1)
	_, err = VerifyInstallationLifecycleWebhook([]byte("secret"), signLifecycle([]byte("secret"), oversized), "installation_repositories", "delivery-6", oversized)
	require.ErrorIs(t, err, ErrInstallationLifecycleTooLarge)
}

func signLifecycle(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
