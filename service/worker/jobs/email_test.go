package jobs

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestEmailSendArgsExposeStableQueueAndKind(t *testing.T) {
	t.Parallel()

	var args queue.JobArgs = EmailSendArgs{}

	require.Equal(t, "email", EmailQueue)
	require.Equal(t, "email_send", args.Kind())

	opts := EmailSendArgs{}.InsertOpts()
	require.Equal(t, EmailQueue, opts.Queue)
	require.False(t, opts.UniqueOpts.ByArgs)
}

func TestEmailSendArgsCarriesOutboundEmailPayload(t *testing.T) {
	t.Parallel()

	args := EmailSendArgs{
		Type: EmailTypeVerification,
		User: EmailRecipient{
			Slug:     slug.Slug("alice-smith"),
			Email:    "alice@example.com",
			FullName: "Alice Smith",
			Locale:   "en",
		},
		Link:           "https://app.example.com/verify-email",
		EncryptedToken: []byte("encrypted-email-verification-token"),
	}

	require.Equal(t, EmailTypeVerification, args.Type)
	require.Equal(t, slug.Slug("alice-smith"), args.User.Slug)
	require.Equal(t, "alice@example.com", args.User.Email)
	require.Equal(t, "Alice Smith", args.User.FullName)
	require.Equal(t, "en", args.User.Locale)
	require.Equal(t, "https://app.example.com/verify-email", args.Link)
	require.Equal(t, []byte("encrypted-email-verification-token"), args.EncryptedToken)
}

func TestNewEmailRecipientOmitsProfilePII(t *testing.T) {
	t.Parallel()

	recipient := NewEmailRecipient(&model.User{
		Slug:            slug.Slug("alice-smith"),
		Email:           "alice@example.com",
		FullName:        "Alice Smith",
		Role:            model.RoleUser,
		Locale:          "en",
		PhoneNumber:     "+48123123123",
		MFAEmailEnabled: true,
	})

	require.Equal(t, EmailRecipient{
		Slug:     slug.Slug("alice-smith"),
		Email:    "alice@example.com",
		FullName: "Alice Smith",
		Locale:   "en",
	}, recipient)
}
