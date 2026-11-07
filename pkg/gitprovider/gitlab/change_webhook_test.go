package gitlab

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
)

func TestVerifyChangeWebhookAuthenticatesStoredAuthority(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"object_kind":"merge_request","event_type":"merge_request","project":{"id":42},"object_attributes":{"iid":7,"state":"opened","action":"update","source_branch":"feature","target_branch":"main","last_commit":{"id":"A123456789012345678901234567890123456789"}}}`)
	token := "whsec_" + base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	timestamp := strconv.FormatInt(now.Unix(), 10)
	signature := standardSignature(token, "delivery-1", timestamp, body)

	event, err := VerifyChangeWebhook(token, "delivery-1", timestamp, signature, "Merge Request Hook", body, now, WebhookAuthority{ProviderConnectionID: "gitlab-ds", RepositoryID: "42"})
	require.NoError(t, err)
	require.Equal(t, "gitlab-ds", event.ProviderConnectionID)
	require.Equal(t, "42", event.RepositoryID)
	require.Equal(t, "a123456789012345678901234567890123456789", event.HeadSHA)
	require.Equal(t, "refs/heads/main", event.TargetRef)

	_, err = VerifyChangeWebhook(token, "delivery-1", timestamp, signature, "Merge Request Hook", body, now, WebhookAuthority{ProviderConnectionID: "gitlab-ds", RepositoryID: "99"})
	require.ErrorIs(t, err, gitprovider.ErrChangeAuthentication)
}

func TestVerifyChangeWebhookRejectsInvalidOrStaleSignatures(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"object_kind":"merge_request","event_type":"merge_request","project":{"id":42}}`)
	token := "whsec_" + base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	for _, timestamp := range []string{strconv.FormatInt(now.Add(-6*time.Minute).Unix(), 10), "invalid"} {
		_, err := VerifyChangeWebhook(token, "delivery-1", timestamp, standardSignature(token, "delivery-1", timestamp, body), "Merge Request Hook", body, now, WebhookAuthority{ProviderConnectionID: "gitlab-ds", RepositoryID: "42"})
		require.ErrorIs(t, err, gitprovider.ErrChangeAuthentication)
	}
	_, err := VerifyChangeWebhook(token, "delivery-1", strconv.FormatInt(now.Unix(), 10), "v1,bad", "Merge Request Hook", body, now, WebhookAuthority{ProviderConnectionID: "gitlab-ds", RepositoryID: "42"})
	require.ErrorIs(t, err, gitprovider.ErrChangeAuthentication)
}

func TestVerifyChangeWebhookRejectsOversizedPayloadBeforeAuthentication(t *testing.T) {
	t.Parallel()
	_, err := VerifyChangeWebhook("", "", "", "", "", make([]byte, gitprovider.MaxChangeWebhookBytes+1), time.Now(), WebhookAuthority{})
	require.ErrorIs(t, err, gitprovider.ErrChangePayloadTooLarge)
}

func TestVerifyChangeWebhookReplayDigest(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	timestamp := strconv.FormatInt(now.Unix(), 10)
	token := "whsec_" + base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	body := `{"object_kind":"merge_request","event_type":"merge_request","project":{"id":42},"object_attributes":{"iid":7,"state":"opened","action":"update","source_branch":"feature","target_branch":"main","last_commit":{"id":"a123456789012345678901234567890123456789"}},"extra":{"id":9007199254740992,"labels":["a","b"]}}`
	replay := `{ "extra": {"labels":["a","b"],"id":9007199254740992}, "object_attributes":{"last_commit":{"id":"a123456789012345678901234567890123456789"},"target_branch":"main","source_branch":"feature","action":"update","state":"opened","iid":7},"project":{"id":42},"event_type":"merge_request","object_kind":"merge_request" }`
	verify := func(body, signature string) (gitprovider.VerifiedChangeEvent, error) {
		return VerifyChangeWebhook(token, "delivery-1", timestamp, signature, "Merge Request Hook", []byte(body), now, WebhookAuthority{ProviderConnectionID: "gitlab-ds", RepositoryID: "42"})
	}
	sign := func(body string) string { return standardSignature(token, "delivery-1", timestamp, []byte(body)) }
	original, err := verify(body, sign(body))
	require.NoError(t, err)
	replayed, err := verify(replay, sign(replay))
	require.NoError(t, err)
	require.Equal(t, original, replayed)
	_, err = verify(replay, sign(body))
	require.ErrorIs(t, err, gitprovider.ErrChangeAuthentication)

	// Duplicate objects must not merge fields absent from the hashed representation.
	duplicate := strings.Replace(body, `,"extra":`, `,"object_attributes":{"action":"update"},"extra":`, 1)
	_, err = verify(duplicate, sign(duplicate))
	require.Error(t, err)

	// GitLab saves redacted emails, then signs those changed bytes on resend.
	withEmails := strings.Replace(body, `"extra":`, `"user":{"email":"fixture@example.test"},"assignees":[{"email":"fixture@example.test"}],"extra":`, 1)
	withEmails = strings.Replace(withEmails, `"last_commit":{"id":`, `"last_commit":{"author":{"email":"fixture@example.test"},"id":`, 1)
	redacted := strings.ReplaceAll(withEmails, "fixture@example.test", "[REDACTED]")
	for _, metadata := range []string{
		withEmails, redacted,
		strings.Replace(body, "9007199254740992", "9007199254740993", 1),
		strings.Replace(body, `["a","b"]`, `["b","a"]`, 1),
	} {
		event, err := verify(metadata, sign(metadata))
		require.NoError(t, err)
		require.Equal(t, original, event)
	}
	_, err = verify(redacted, sign(withEmails))
	require.ErrorIs(t, err, gitprovider.ErrChangeAuthentication)

	for _, changed := range []string{
		strings.Replace(body, `"iid":7`, `"iid":8`, 1),
		strings.Replace(body, `"action":"update"`, `"action":"reopen"`, 1),
		strings.Replace(body, `"state":"opened"`, `"state":"reopened"`, 1),
		strings.Replace(body, `"source_branch":"feature"`, `"source_branch":"other"`, 1),
		strings.Replace(body, "a123456789012345678901234567890123456789", "b123456789012345678901234567890123456789", 1),
		strings.Replace(body, `"target_branch":"main"`, `"target_branch":"other"`, 1),
	} {
		event, err := verify(changed, sign(changed))
		require.NoError(t, err)
		require.NotEqual(t, original.BodySHA256, event.BodySHA256)
	}
}

func standardSignature(token, deliveryID, timestamp string, body []byte) string {
	key, _ := base64.StdEncoding.DecodeString(token[len("whsec_"):])
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(deliveryID + "." + timestamp + "."))
	_, _ = mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
