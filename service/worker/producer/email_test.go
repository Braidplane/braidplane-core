package producer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestEmailProducerEnqueuesEmailVerification(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{
		queued: queue.QueuedJob{
			Kind:  "email_send",
			Queue: jobs.EmailQueue,
			State: "queued",
		},
	}
	producer, err := NewEmailProducer(enqueuer, fakeEmailTokenCipher{})
	require.NoError(t, err)
	user := &model.User{
		Slug:        slug.Slug("alice-smith"),
		Email:       "alice@example.com",
		FullName:    "Alice Smith",
		Role:        model.RoleUser,
		Locale:      "en",
		PhoneNumber: "+48123123123",
	}

	queued, err := producer.EnqueueEmailVerification(
		context.Background(),
		nil,
		user,
		"https://app.example.com/verify-email",
		"raw-email-verification-token",
	)

	require.NoError(t, err)
	require.Equal(t, enqueuer.queued, queued)
	require.Equal(t, 1, enqueuer.calls)
	require.Nil(t, enqueuer.tx)
	require.Equal(t, jobs.EmailQueue, enqueuer.req.Queue)
	require.Equal(t, user.Slug, enqueuer.req.InitiatingAccount)
	require.Equal(t, jobs.EmailSendArgs{
		Type:           jobs.EmailTypeVerification,
		User:           jobs.NewEmailRecipient(user),
		Link:           "https://app.example.com/verify-email",
		EncryptedToken: []byte("encrypted-email-verification-token"),
	}, enqueuer.req.Args)

	payload, err := json.Marshal(enqueuer.req.Args)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "+48123123123")
	require.NotContains(t, string(payload), "1 Main Street")
	require.NotContains(t, string(payload), "00-001")
	require.NotContains(t, string(payload), "raw-email-verification-token")
	require.NotContains(t, string(payload), "token=raw-email-verification-token")
}

func TestEmailProducerEnqueuesPasswordReset(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{
		queued: queue.QueuedJob{
			Kind:  "email_send",
			Queue: jobs.EmailQueue,
			State: "queued",
		},
	}
	producer, err := NewEmailProducer(enqueuer, fakeEmailTokenCipher{})
	require.NoError(t, err)
	user := &model.User{
		Slug:        slug.Slug("alice-smith"),
		Email:       "alice@example.com",
		FullName:    "Alice Smith",
		Role:        model.RoleUser,
		Locale:      "en",
		PhoneNumber: "+48123123123",
	}

	queued, err := producer.EnqueuePasswordReset(
		context.Background(),
		nil,
		user,
		"https://app.example.com/reset-password",
		"raw-password-reset-token",
	)

	require.NoError(t, err)
	require.Equal(t, enqueuer.queued, queued)
	require.Equal(t, 1, enqueuer.calls)
	require.Nil(t, enqueuer.tx)
	require.Equal(t, jobs.EmailQueue, enqueuer.req.Queue)
	require.Equal(t, jobs.EmailSendArgs{
		Type:           jobs.EmailTypePasswordReset,
		User:           jobs.NewEmailRecipient(user),
		Link:           "https://app.example.com/reset-password",
		EncryptedToken: []byte("encrypted-password-reset-token"),
	}, enqueuer.req.Args)

	payload, err := json.Marshal(enqueuer.req.Args)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "+48123123123")
	require.NotContains(t, string(payload), "1 Main Street")
	require.NotContains(t, string(payload), "00-001")
	require.NotContains(t, string(payload), "raw-password-reset-token")
	require.NotContains(t, string(payload), "token=raw-password-reset-token")
}

func TestEmailProducerEnqueuesMessage(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{queued: queue.QueuedJob{Kind: "email_send", Queue: jobs.EmailQueue, State: "queued"}}
	producer, err := NewEmailProducer(enqueuer, fakeEmailTokenCipher{})
	require.NoError(t, err)
	user := &model.User{Slug: slug.Slug("alice-smith"), Email: "alice@example.com", FullName: "Alice Smith", Locale: "en"}

	queued, err := producer.EnqueueMessage(
		context.Background(),
		nil,
		user,
		"Synthetic subject",
		"Synthetic body",
	)

	require.NoError(t, err)
	require.Equal(t, enqueuer.queued, queued)
	require.Equal(t, jobs.EmailSendArgs{
		Type:    jobs.EmailTypeMessage,
		User:    jobs.NewEmailRecipient(user),
		Subject: "Synthetic subject",
		Body:    "Synthetic body",
	}, enqueuer.req.Args)
}

func TestEmailProducerRejectsIncompleteMessage(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{}
	producer, err := NewEmailProducer(enqueuer, fakeEmailTokenCipher{})
	require.NoError(t, err)

	_, err = producer.EnqueueMessage(
		context.Background(),
		nil,
		&model.User{Email: "alice@example.com"},
		"",
		"Synthetic body",
	)

	require.ErrorIs(t, err, xerror.ValidationFailed())
	require.Zero(t, enqueuer.calls)
}

func TestEmailProducerEnqueuesMemberInvitation(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{
		queued: queue.QueuedJob{
			Kind:  "email_send",
			Queue: jobs.EmailQueue,
			State: "queued",
		},
	}
	producer, err := NewEmailProducer(enqueuer, fakeEmailTokenCipher{})
	require.NoError(t, err)
	invitation := model.MemberInvitationEmail{
		Email:            "invitee@example.com",
		OrganizationName: "Acme",
		InviterName:      "Alice Smith",
		InviterEmail:     "alice@example.com",
		Role:             slug.Slug("viewer"),
		Locale:           "en",
	}

	queued, err := producer.EnqueueMemberInvitation(
		context.Background(),
		nil,
		invitation,
		"https://app.example.com/member-invitations/accept",
		"raw-member-invitation-token",
	)

	require.NoError(t, err)
	require.Equal(t, enqueuer.queued, queued)
	require.Equal(t, 1, enqueuer.calls)
	require.Nil(t, enqueuer.tx)
	require.Equal(t, jobs.EmailQueue, enqueuer.req.Queue)
	require.Equal(t, jobs.EmailSendArgs{
		Type:           jobs.EmailTypeInvitation,
		Invitation:     invitation,
		Link:           "https://app.example.com/member-invitations/accept",
		EncryptedToken: []byte("encrypted-member-invitation-token"),
	}, enqueuer.req.Args)

	payload, err := json.Marshal(enqueuer.req.Args)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "raw-member-invitation-token")
	require.NotContains(t, string(payload), "token=raw-member-invitation-token")
}

func TestEmailProducerEnqueuesSessionMFAWithoutProfilePII(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{
		queued: queue.QueuedJob{
			Kind:  "email_send",
			Queue: jobs.EmailQueue,
			State: "queued",
		},
	}
	producer, err := NewEmailProducer(enqueuer, fakeEmailTokenCipher{})
	require.NoError(t, err)
	user := &model.User{
		Slug:        slug.Slug("alice-smith"),
		Email:       "alice@example.com",
		FullName:    "Alice Smith",
		Locale:      "en",
		PhoneNumber: "+48123123123",
	}

	queued, err := producer.EnqueueSessionMFA(context.Background(), nil, user, "123456")

	require.NoError(t, err)
	require.Equal(t, enqueuer.queued, queued)
	require.Equal(t, jobs.EmailSendArgs{
		Type:          jobs.EmailTypeSessionMFA,
		User:          jobs.NewEmailRecipient(user),
		EncryptedCode: []byte("encrypted-123456"),
	}, enqueuer.req.Args)

	payload, err := json.Marshal(enqueuer.req.Args)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "+48123123123")
	require.NotContains(t, string(payload), "1 Main Street")
	require.NotContains(t, string(payload), "00-001")
	require.NotContains(t, string(payload), "123456")
}

func TestEmailProducerEnqueuesNotification(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{
		queued: queue.QueuedJob{
			Kind:  "email_send",
			Queue: jobs.EmailQueue,
			State: "queued",
		},
	}
	producer, err := NewEmailProducer(enqueuer, fakeEmailTokenCipher{})
	require.NoError(t, err)
	recipient := model.NotificationEmailRecipient{
		Email:    "ops@example.com",
		FullName: "Ops Team",
		Locale:   "en",
	}
	notification := model.NotificationEmail{
		OrganizationName: "Acme",
		RecordSlug:       slug.Slug("pipeline-run-failed"),
		NotificationType: model.NotificationTypePipelineRunFailed,
		Severity:         model.NotificationSeverityCritical,
		Summary:          "Pipeline failed",
		SourceEntityType: model.EventEntityTypePipelineRun,
		SourceAction:     model.EventActionExecute,
		OccurrenceCount:  2,
	}
	attemptSlug := slug.Slug("alert-delivery-abc123")
	orgSlug := slug.Slug("acme")

	queued, err := producer.EnqueueNotification(context.Background(), nil, orgSlug, recipient, notification, attemptSlug)

	require.NoError(t, err)
	require.Equal(t, enqueuer.queued, queued)
	require.Equal(t, 1, enqueuer.calls)
	require.Nil(t, enqueuer.tx)
	require.Equal(t, jobs.EmailQueue, enqueuer.req.Queue)
	require.Equal(t, jobs.EmailSendArgs{
		Type:                     jobs.EmailTypeNotification,
		NotificationRecipient:    recipient,
		Notification:             notification,
		OrganizationSlug:         orgSlug,
		AlertDeliveryAttemptSlug: attemptSlug,
	}, enqueuer.req.Args)
}

func TestEmailProducerReturnsEnqueueError(t *testing.T) {
	t.Parallel()

	enqueueErr := xerror.Unexpected().Detail("queue unavailable")
	producer, err := NewEmailProducer(&recordingEnqueuer{err: enqueueErr}, fakeEmailTokenCipher{})
	require.NoError(t, err)

	queued, err := producer.EnqueueEmailVerification(context.Background(), nil, &model.User{}, "", "")

	require.Empty(t, queued)
	require.ErrorIs(t, err, enqueueErr)
}

func TestNewEmailProducerRequiresEnqueuer(t *testing.T) {
	t.Parallel()

	producer, err := NewEmailProducer(nil, fakeEmailTokenCipher{})

	require.Nil(t, producer)
	require.ErrorContains(t, err, "queue enqueuer is required")
}

func TestNewEmailProducerRequiresTokenCipher(t *testing.T) {
	t.Parallel()

	producer, err := NewEmailProducer(&recordingEnqueuer{}, nil)

	require.Nil(t, producer)
	require.ErrorContains(t, err, "email token cipher is required")
}

type fakeEmailTokenCipher struct{}

func (fakeEmailTokenCipher) Encrypt(plaintext []byte) ([]byte, error) {
	if string(plaintext) == "raw-email-verification-token" {
		return []byte("encrypted-email-verification-token"), nil
	}
	if string(plaintext) == "raw-password-reset-token" {
		return []byte("encrypted-password-reset-token"), nil
	}
	if string(plaintext) == "raw-member-invitation-token" {
		return []byte("encrypted-member-invitation-token"), nil
	}
	return []byte("encrypted-" + string(plaintext)), nil
}

func (fakeEmailTokenCipher) Decrypt(ciphertext []byte) ([]byte, error) {
	if string(ciphertext) == "encrypted-email-verification-token" {
		return []byte("raw-email-verification-token"), nil
	}
	if string(ciphertext) == "encrypted-password-reset-token" {
		return []byte("raw-password-reset-token"), nil
	}
	if string(ciphertext) == "encrypted-member-invitation-token" {
		return []byte("raw-member-invitation-token"), nil
	}
	return ciphertext, nil
}
