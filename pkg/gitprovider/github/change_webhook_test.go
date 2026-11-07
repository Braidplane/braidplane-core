package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
)

func TestVerifyChangeWebhookAuthenticatesAndNormalizes(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"synchronize","installation":{"id":9001},"repository":{"id":42},"pull_request":{"number":7,"state":"open","head":{"sha":"A123456789012345678901234567890123456789","ref":"feature"},"base":{"ref":"main"}}}`)
	change, err := VerifyChangeWebhook([]byte("secret"), sign([]byte("secret"), body), "pull_request", "delivery-1", body)
	require.NoError(t, err)
	require.Equal(t, "9001", change.ProviderConnectionID)
	require.Equal(t, "42", change.RepositoryID)
	require.Equal(t, "a123456789012345678901234567890123456789", change.HeadSHA)
	require.Equal(t, "refs/heads/main", change.TargetRef)

	_, err = VerifyChangeWebhook([]byte("secret"), "sha256=bad", "pull_request", "delivery-1", body)
	require.ErrorIs(t, err, gitprovider.ErrChangeAuthentication)
}

func TestVerifyChangeWebhookMapsClosedActionToCloseChange(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"closed","installation":{"id":9001},"repository":{"id":42},"pull_request":{"number":7,"state":"closed","head":{"sha":"a123456789012345678901234567890123456789","ref":"feature"},"base":{"ref":"main"}}}`)
	change, err := VerifyChangeWebhook([]byte("secret"), sign([]byte("secret"), body), "pull_request", "delivery-2", body)
	require.NoError(t, err)
	require.Equal(t, gitprovider.ChangeActionClose, change.Action)
}

func TestVerifyChangeWebhookNormalizesProofRerequest(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"requested_action","requested_action":{"identifier":"rerequest"},"installation":{"id":9001},"repository":{"id":42},"check_run":{"head_sha":"a123456789012345678901234567890123456789","pull_requests":[{"number":7,"head":{"ref":"feature"},"base":{"ref":"main"}}]}}`)
	change, err := VerifyChangeWebhook([]byte("secret"), sign([]byte("secret"), body), "check_run", "delivery-rerequest", body)
	require.NoError(t, err)
	require.Equal(t, gitprovider.ChangeActionUpsert, change.Action)
	require.Equal(t, "7", change.ChangeRequestID)
	require.Equal(t, "a123456789012345678901234567890123456789", change.HeadSHA)
	require.True(t, change.Rerequest)
}

func TestVerifyChangeWebhookAcceptsStandardRerequest(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"rerequested","installation":{"id":9001},"repository":{"id":42},"check_run":{"head_sha":"a123456789012345678901234567890123456789","pull_requests":[{"number":7,"head":{"ref":"feature"},"base":{"ref":"main"}}]}}`)
	change, err := VerifyChangeWebhook([]byte("secret"), sign([]byte("secret"), body), "check_run", "delivery-rerequest-standard", body)
	require.NoError(t, err)
	require.True(t, change.Rerequest)
}

func TestVerifyChangeWebhookRejectsUpsertOnClosedChange(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"synchronize","installation":{"id":9001},"repository":{"id":42},"pull_request":{"number":7,"state":"closed","head":{"sha":"a123456789012345678901234567890123456789","ref":"feature"},"base":{"ref":"main"}}}`)
	_, err := VerifyChangeWebhook([]byte("secret"), sign([]byte("secret"), body), "pull_request", "delivery-2b", body)
	require.ErrorIs(t, err, gitprovider.ErrChangeClosed)
}

func TestVerifyChangeWebhookRejectsOversizedSignedPayload(t *testing.T) {
	t.Parallel()
	body := make([]byte, gitprovider.MaxChangeWebhookBytes+1)
	_, err := VerifyChangeWebhook([]byte("secret"), sign([]byte("secret"), body), "pull_request", "delivery-3", body)
	require.ErrorContains(t, err, "payload is too large")
}

func TestVerifyChangeWebhookIgnoresUnneededAction(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"labeled"}`)
	_, err := VerifyChangeWebhook([]byte("secret"), sign([]byte("secret"), body), "pull_request", "delivery-4", body)
	require.ErrorIs(t, err, gitprovider.ErrChangeActionIgnored)
}

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
