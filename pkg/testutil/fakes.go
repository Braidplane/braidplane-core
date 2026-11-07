package testutil

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

// FakeHasher is a test double for crypto.Hasher.
// By default it returns the input unchanged (identity hash).
type FakeHasher struct {
	HashFn        func(input string) (string, error)
	CompareHashFn func(input, hash string) (bool, error)
}

func (f *FakeHasher) Hash(input string) (string, error) {
	if f.HashFn != nil {
		return f.HashFn(input)
	}
	return input, nil
}

func (f *FakeHasher) CompareHash(input, hash string) (bool, error) {
	if f.CompareHashFn != nil {
		return f.CompareHashFn(input, hash)
	}
	return input == hash, nil
}

// FakeEmailService is a test double for email.Service.
// By default EmailVerification succeeds without side effects.
type FakeEmailService struct {
	EmailVerificationFn func(ctx context.Context, user *model.User, verificationLink string) error
	PasswordResetFn     func(ctx context.Context, user *model.User, resetLink string) error
	SessionMFAFn        func(ctx context.Context, user *model.User, code string) error
	MemberInvitationFn  func(ctx context.Context, invitation model.MemberInvitationEmail, acceptanceLink string) error
	NotificationFn      func(ctx context.Context, recipient model.NotificationEmailRecipient, notification model.NotificationEmail) error
	MessageFn           func(ctx context.Context, user *model.User, subject, body string) error
}

func (f *FakeEmailService) Message(
	ctx context.Context,
	user *model.User,
	subject, body string,
) error {
	if f.MessageFn != nil {
		return f.MessageFn(ctx, user, subject, body)
	}
	return nil
}

func (f *FakeEmailService) EmailVerification(
	ctx context.Context,
	user *model.User,
	verificationLink string,
) error {
	if f.EmailVerificationFn != nil {
		return f.EmailVerificationFn(ctx, user, verificationLink)
	}
	return nil
}

func (f *FakeEmailService) PasswordReset(
	ctx context.Context,
	user *model.User,
	resetLink string,
) error {
	if f.PasswordResetFn != nil {
		return f.PasswordResetFn(ctx, user, resetLink)
	}
	return nil
}

func (f *FakeEmailService) SessionMFA(
	ctx context.Context,
	user *model.User,
	code string,
) error {
	if f.SessionMFAFn != nil {
		return f.SessionMFAFn(ctx, user, code)
	}
	return nil
}

func (f *FakeEmailService) MemberInvitation(
	ctx context.Context,
	invitation model.MemberInvitationEmail,
	acceptanceLink string,
) error {
	if f.MemberInvitationFn != nil {
		return f.MemberInvitationFn(ctx, invitation, acceptanceLink)
	}
	return nil
}

func (f *FakeEmailService) Notification(
	ctx context.Context,
	recipient model.NotificationEmailRecipient,
	notification model.NotificationEmail,
) error {
	if f.NotificationFn != nil {
		return f.NotificationFn(ctx, recipient, notification)
	}
	return nil
}

// FakeEmailProducer is a test double for workerproducer.EmailProducer.
// By default enqueue methods record their inputs and return a queued outcome.
type FakeEmailProducer struct {
	EnqueueEmailVerificationFn func(ctx context.Context, tx postgres.DBTX, user *model.User, verificationURL, token string) (queue.QueuedJob, error)
	EnqueuePasswordResetFn     func(ctx context.Context, tx postgres.DBTX, user *model.User, resetURL, token string) (queue.QueuedJob, error)
	EnqueueSessionMFAFn        func(ctx context.Context, tx postgres.DBTX, user *model.User, code string) (queue.QueuedJob, error)
	EnqueueMemberInvitationFn  func(ctx context.Context, tx postgres.DBTX, invitation model.MemberInvitationEmail, acceptanceURL, token string) (queue.QueuedJob, error)
	EnqueueNotificationFn      func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, recipient model.NotificationEmailRecipient, notification model.NotificationEmail, alertDeliveryAttemptSlug slug.Slug) (queue.QueuedJob, error)
	EnqueueMessageFn           func(ctx context.Context, tx postgres.DBTX, user *model.User, subject, body string) (queue.QueuedJob, error)

	EnqueueEmailVerificationCalls int
	LastEmailVerificationTx       postgres.DBTX
	LastEmailVerificationUser     *model.User
	LastEmailVerificationLink     string
	LastEmailVerificationURL      string
	LastEmailVerificationToken    string

	EnqueuePasswordResetCalls int
	LastPasswordResetTx       postgres.DBTX
	LastPasswordResetUser     *model.User
	LastPasswordResetLink     string
	LastPasswordResetURL      string
	LastPasswordResetToken    string

	EnqueueMemberInvitationCalls int
	LastMemberInvitationTx       postgres.DBTX
	LastMemberInvitation         model.MemberInvitationEmail
	LastMemberInvitationLink     string
	LastMemberInvitationURL      string
	LastMemberInvitationToken    string

	EnqueueSessionMFACalls int
	LastSessionMFATx       postgres.DBTX
	LastSessionMFAUser     *model.User
	LastSessionMFACode     string

	EnqueueMessageCalls int
	LastMessageTx       postgres.DBTX
	LastMessageUser     *model.User
	LastMessageSubject  string
	LastMessageBody     string

	EnqueueNotificationCalls     int
	LastNotificationTx           postgres.DBTX
	LastNotificationOrgSlug      slug.Slug
	LastNotificationRecipient    model.NotificationEmailRecipient
	LastNotification             model.NotificationEmail
	LastAlertDeliveryAttemptSlug slug.Slug
}

func (f *FakeEmailProducer) EnqueueMessage(
	ctx context.Context,
	tx postgres.DBTX,
	user *model.User,
	subject, body string,
) (queue.QueuedJob, error) {
	f.EnqueueMessageCalls++
	f.LastMessageTx = tx
	f.LastMessageUser = user
	f.LastMessageSubject = subject
	f.LastMessageBody = body
	if f.EnqueueMessageFn != nil {
		return f.EnqueueMessageFn(ctx, tx, user, subject, body)
	}
	return queue.QueuedJob{State: "queued"}, nil
}

func (f *FakeEmailProducer) EnqueueEmailVerification(
	ctx context.Context,
	tx postgres.DBTX,
	user *model.User,
	verificationURL string,
	token string,
) (queue.QueuedJob, error) {
	f.EnqueueEmailVerificationCalls++
	f.LastEmailVerificationTx = tx
	f.LastEmailVerificationUser = user
	f.LastEmailVerificationURL = verificationURL
	f.LastEmailVerificationToken = token
	f.LastEmailVerificationLink = verificationURL + "?token=" + token
	if f.EnqueueEmailVerificationFn != nil {
		return f.EnqueueEmailVerificationFn(ctx, tx, user, verificationURL, token)
	}
	return queue.QueuedJob{State: "queued"}, nil
}

func (f *FakeEmailProducer) EnqueuePasswordReset(
	ctx context.Context,
	tx postgres.DBTX,
	user *model.User,
	resetURL string,
	token string,
) (queue.QueuedJob, error) {
	f.EnqueuePasswordResetCalls++
	f.LastPasswordResetTx = tx
	f.LastPasswordResetUser = user
	f.LastPasswordResetURL = resetURL
	f.LastPasswordResetToken = token
	f.LastPasswordResetLink = resetURL + "?token=" + token
	if f.EnqueuePasswordResetFn != nil {
		return f.EnqueuePasswordResetFn(ctx, tx, user, resetURL, token)
	}
	return queue.QueuedJob{State: "queued"}, nil
}

func (f *FakeEmailProducer) EnqueueMemberInvitation(
	ctx context.Context,
	tx postgres.DBTX,
	invitation model.MemberInvitationEmail,
	acceptanceURL string,
	token string,
) (queue.QueuedJob, error) {
	f.EnqueueMemberInvitationCalls++
	f.LastMemberInvitationTx = tx
	f.LastMemberInvitation = invitation
	f.LastMemberInvitationURL = acceptanceURL
	f.LastMemberInvitationToken = token
	f.LastMemberInvitationLink = acceptanceURL + "?token=" + token
	if f.EnqueueMemberInvitationFn != nil {
		return f.EnqueueMemberInvitationFn(ctx, tx, invitation, acceptanceURL, token)
	}
	return queue.QueuedJob{State: "queued"}, nil
}

func (f *FakeEmailProducer) EnqueueSessionMFA(
	ctx context.Context,
	tx postgres.DBTX,
	user *model.User,
	code string,
) (queue.QueuedJob, error) {
	f.EnqueueSessionMFACalls++
	f.LastSessionMFATx = tx
	f.LastSessionMFAUser = user
	f.LastSessionMFACode = code
	if f.EnqueueSessionMFAFn != nil {
		return f.EnqueueSessionMFAFn(ctx, tx, user, code)
	}
	return queue.QueuedJob{State: "queued"}, nil
}

func (f *FakeEmailProducer) EnqueueNotification(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	recipient model.NotificationEmailRecipient,
	notification model.NotificationEmail,
	alertDeliveryAttemptSlug slug.Slug,
) (queue.QueuedJob, error) {
	f.EnqueueNotificationCalls++
	f.LastNotificationTx = tx
	f.LastNotificationOrgSlug = orgSlug
	f.LastNotificationRecipient = recipient
	f.LastNotification = notification
	f.LastAlertDeliveryAttemptSlug = alertDeliveryAttemptSlug
	if f.EnqueueNotificationFn != nil {
		return f.EnqueueNotificationFn(ctx, tx, orgSlug, recipient, notification, alertDeliveryAttemptSlug)
	}
	return queue.QueuedJob{State: "queued"}, nil
}
