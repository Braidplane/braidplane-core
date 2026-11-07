package producer

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

// EmailProducer enqueues outbound email side effects for background execution.
type EmailProducer interface {
	EnqueueEmailVerification(
		ctx context.Context,
		tx postgres.DBTX,
		user *model.User,
		verificationURL string,
		token string,
	) (queue.QueuedJob, error)
	EnqueuePasswordReset(
		ctx context.Context,
		tx postgres.DBTX,
		user *model.User,
		resetURL string,
		token string,
	) (queue.QueuedJob, error)
	EnqueueMemberInvitation(
		ctx context.Context,
		tx postgres.DBTX,
		invitation model.MemberInvitationEmail,
		acceptanceURL string,
		token string,
	) (queue.QueuedJob, error)
	EnqueueSessionMFA(
		ctx context.Context,
		tx postgres.DBTX,
		user *model.User,
		code string,
	) (queue.QueuedJob, error)
	EnqueueMessage(
		ctx context.Context,
		tx postgres.DBTX,
		user *model.User,
		subject, body string,
	) (queue.QueuedJob, error)
	EnqueueNotification(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		recipient model.NotificationEmailRecipient,
		notification model.NotificationEmail,
		alertDeliveryAttemptSlug slug.Slug,
	) (queue.QueuedJob, error)
}

type emailProducer struct {
	enqueuer    queue.Enqueuer
	tokenCipher crypto.AES256
	queue       string
}

func NewEmailProducer(enqueuer queue.Enqueuer, tokenCipher crypto.AES256) (EmailProducer, error) {
	return NewEmailProducerForQueue(enqueuer, tokenCipher, jobs.EmailQueue)
}

func NewEmailProducerForQueue(enqueuer queue.Enqueuer, tokenCipher crypto.AES256, queueName string) (EmailProducer, error) {
	if enqueuer == nil {
		return nil, xerror.Unexpected().Detail("queue enqueuer is required")
	}
	if tokenCipher == nil {
		return nil, xerror.Unexpected().Detail("email token cipher is required")
	}
	if strings.TrimSpace(queueName) == "" {
		return nil, xerror.Unexpected().Detail("email queue is required")
	}

	return &emailProducer{enqueuer: enqueuer, tokenCipher: tokenCipher, queue: queueName}, nil
}

func (p *emailProducer) EnqueueEmailVerification(
	ctx context.Context,
	tx postgres.DBTX,
	user *model.User,
	verificationURL string,
	token string,
) (queue.QueuedJob, error) {
	encryptedToken, err := p.tokenCipher.Encrypt([]byte(token))
	if err != nil {
		return queue.QueuedJob{}, xerror.Wrapf("encrypt email verification token: %w", err)
	}

	return p.enqueueSend(ctx, tx, jobs.EmailSendArgs{
		Type:           jobs.EmailTypeVerification,
		User:           jobs.NewEmailRecipient(user),
		Link:           verificationURL,
		EncryptedToken: encryptedToken,
	})
}

func (p *emailProducer) EnqueuePasswordReset(
	ctx context.Context,
	tx postgres.DBTX,
	user *model.User,
	resetURL string,
	token string,
) (queue.QueuedJob, error) {
	encryptedToken, err := p.tokenCipher.Encrypt([]byte(token))
	if err != nil {
		return queue.QueuedJob{}, xerror.Wrapf("encrypt password reset token: %w", err)
	}

	return p.enqueueSend(ctx, tx, jobs.EmailSendArgs{
		Type:           jobs.EmailTypePasswordReset,
		User:           jobs.NewEmailRecipient(user),
		Link:           resetURL,
		EncryptedToken: encryptedToken,
	})
}

// EnqueueMemberInvitation persists an encrypted invitation token for asynchronous email delivery.
func (p *emailProducer) EnqueueMemberInvitation(
	ctx context.Context,
	tx postgres.DBTX,
	invitation model.MemberInvitationEmail,
	acceptanceURL string,
	token string,
) (queue.QueuedJob, error) {
	encryptedToken, err := p.tokenCipher.Encrypt([]byte(token))
	if err != nil {
		return queue.QueuedJob{}, xerror.Wrapf("encrypt member invitation token: %w", err)
	}

	return p.enqueueSend(ctx, tx, jobs.EmailSendArgs{
		Type:           jobs.EmailTypeInvitation,
		Invitation:     invitation,
		Link:           acceptanceURL,
		EncryptedToken: encryptedToken,
	})
}

func (p *emailProducer) EnqueueSessionMFA(
	ctx context.Context,
	tx postgres.DBTX,
	user *model.User,
	code string,
) (queue.QueuedJob, error) {
	encryptedCode, err := p.tokenCipher.Encrypt([]byte(code))
	if err != nil {
		return queue.QueuedJob{}, xerror.Wrapf("encrypt session mfa code: %w", err)
	}

	return p.enqueueSend(ctx, tx, jobs.EmailSendArgs{
		Type:          jobs.EmailTypeSessionMFA,
		User:          jobs.NewEmailRecipient(user),
		EncryptedCode: encryptedCode,
	})
}

func (p *emailProducer) EnqueueMessage(
	ctx context.Context,
	tx postgres.DBTX,
	user *model.User,
	subject, body string,
) (queue.QueuedJob, error) {
	if strings.TrimSpace(subject) == "" || strings.TrimSpace(body) == "" {
		return queue.QueuedJob{}, xerror.ValidationFailed().Detail("email subject and body are required")
	}
	return p.enqueueSend(ctx, tx, jobs.EmailSendArgs{
		Type:    jobs.EmailTypeMessage,
		User:    jobs.NewEmailRecipient(user),
		Subject: subject,
		Body:    body,
	})
}

func (p *emailProducer) EnqueueNotification(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	recipient model.NotificationEmailRecipient,
	notification model.NotificationEmail,
	alertDeliveryAttemptSlug slug.Slug,
) (queue.QueuedJob, error) {
	return p.enqueueSend(ctx, tx, jobs.EmailSendArgs{
		Type:                     jobs.EmailTypeNotification,
		NotificationRecipient:    recipient,
		Notification:             notification,
		OrganizationSlug:         orgSlug,
		AlertDeliveryAttemptSlug: alertDeliveryAttemptSlug,
	})
}

func (p *emailProducer) enqueueSend(
	ctx context.Context,
	tx postgres.DBTX,
	args jobs.EmailSendArgs,
) (queue.QueuedJob, error) {
	return p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Args:              args,
		Queue:             p.queue,
		InitiatingAccount: args.User.Slug,
	})
}
