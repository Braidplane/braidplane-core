package consumer

import (
	"context"
	"log/slog"
	"net/url"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/email"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

const notificationEmailDeliveryFailedMessage = "Email delivery failed. Check worker logs for provider details."

// AlertDeliveryAttemptRecorder persists notification email delivery diagnostics.
type AlertDeliveryAttemptRecorder interface {
	MarkAlertDeliveryAttemptSent(ctx context.Context, params notificationsvc.MarkAlertDeliveryAttemptSentParams) error
	MarkAlertDeliveryAttemptFailed(ctx context.Context, params notificationsvc.MarkAlertDeliveryAttemptFailedParams) error
}

// EmailSendWorkerOption customizes optional worker collaborators.
type EmailSendWorkerOption func(*emailSendWorker)

// WithAlertDeliveryAttemptRecorder records notification email delivery diagnostics.
func WithAlertDeliveryAttemptRecorder(recorder AlertDeliveryAttemptRecorder) EmailSendWorkerOption {
	return func(w *emailSendWorker) {
		w.alertDeliveryAttemptRecorder = recorder
	}
}

func NewEmailSendWorker(
	emailSvc email.Service,
	tokenCipher crypto.AES256,
	logger *slog.Logger,
	options ...EmailSendWorkerOption,
) river.Worker[jobs.EmailSendArgs] {
	worker := &emailSendWorker{
		emailSvc:    emailSvc,
		tokenCipher: tokenCipher,
		logger:      logger,
	}
	for _, option := range options {
		if option != nil {
			option(worker)
		}
	}
	return worker
}

type emailSendWorker struct {
	river.WorkerDefaults[jobs.EmailSendArgs]

	emailSvc                     email.Service
	tokenCipher                  crypto.AES256
	alertDeliveryAttemptRecorder AlertDeliveryAttemptRecorder
	logger                       *slog.Logger
}

func (w *emailSendWorker) Work(
	ctx context.Context,
	job *river.Job[jobs.EmailSendArgs],
) error {
	args := job.Args
	user := args.User.ToUser()
	w.logger.InfoContext(
		ctx,
		"email send job started",
		slog.String("type", args.Type.String()),
		slog.String("user_slug", user.Slug.String()),
	)

	var err error
	switch args.Type {
	case jobs.EmailTypeVerification:
		verificationLink, linkErr := w.tokenizedLink(args, "encrypted email verification token is required", "decrypt email verification token")
		if linkErr != nil {
			err = linkErr
			break
		}
		err = w.emailSvc.EmailVerification(ctx, &user, verificationLink)
	case jobs.EmailTypePasswordReset:
		resetLink, linkErr := w.tokenizedLink(args, "encrypted password reset token is required", "decrypt password reset token")
		if linkErr != nil {
			err = linkErr
			break
		}
		err = w.emailSvc.PasswordReset(ctx, &user, resetLink)
	case jobs.EmailTypeSessionMFA:
		code, codeErr := w.decryptedCode(args)
		if codeErr != nil {
			err = codeErr
			break
		}
		err = w.emailSvc.SessionMFA(ctx, &user, code)
	case jobs.EmailTypeInvitation:
		acceptanceLink, linkErr := w.tokenizedLink(args, "encrypted member invitation token is required", "decrypt member invitation token")
		if linkErr != nil {
			err = linkErr
			break
		}
		err = w.emailSvc.MemberInvitation(ctx, args.Invitation, acceptanceLink)
	case jobs.EmailTypeNotification:
		err = w.emailSvc.Notification(ctx, args.NotificationRecipient, args.Notification)
	case jobs.EmailTypeMessage:
		if args.Subject == "" || args.Body == "" {
			err = xerror.ValidationFailed().Detail("email subject and body are required")
			break
		}
		err = w.emailSvc.Message(ctx, &user, args.Subject, args.Body)
	default:
		err = xerror.ValidationFailed().Detailf("unsupported email type %q", args.Type)
	}
	if err != nil {
		if markErr := w.recordAlertDeliveryAttemptFailed(ctx, job, args, err); markErr != nil {
			w.logger.ErrorContext(ctx, "alert delivery attempt failure diagnostic update failed", slog.Any("error", markErr))
		}
		w.logger.ErrorContext(ctx, "email send job failed", slog.Any("error", err))
		return err
	}

	if err := w.recordAlertDeliveryAttemptSent(ctx, job, args); err != nil {
		w.logger.ErrorContext(ctx, "alert delivery attempt success diagnostic update failed", slog.Any("error", err))
	}
	w.logger.InfoContext(ctx, "email send job succeeded")
	return nil
}

// recordAlertDeliveryAttemptSent marks successful notification email execution as best-effort diagnostics.
func (w *emailSendWorker) recordAlertDeliveryAttemptSent(
	ctx context.Context,
	job *river.Job[jobs.EmailSendArgs],
	args jobs.EmailSendArgs,
) error {
	if args.Type != jobs.EmailTypeNotification || args.AlertDeliveryAttemptSlug == "" {
		return nil
	}
	if w.alertDeliveryAttemptRecorder == nil {
		return xerror.Unexpected().Detail("alert delivery attempt recorder is required")
	}
	return w.alertDeliveryAttemptRecorder.MarkAlertDeliveryAttemptSent(ctx, notificationsvc.MarkAlertDeliveryAttemptSentParams{
		OrgSlug:         args.OrganizationSlug,
		AttemptSlug:     args.AlertDeliveryAttemptSlug,
		ProviderAttempt: riverProviderAttempt(job),
	})
}

// recordAlertDeliveryAttemptFailed records failure diagnostics while preserving the original retry error.
func (w *emailSendWorker) recordAlertDeliveryAttemptFailed(
	ctx context.Context,
	job *river.Job[jobs.EmailSendArgs],
	args jobs.EmailSendArgs,
	cause error,
) error {
	if args.Type != jobs.EmailTypeNotification || args.AlertDeliveryAttemptSlug == "" {
		return nil
	}
	if w.alertDeliveryAttemptRecorder == nil {
		return xerror.Unexpected().Detail("alert delivery attempt recorder is required")
	}
	return w.alertDeliveryAttemptRecorder.MarkAlertDeliveryAttemptFailed(ctx, notificationsvc.MarkAlertDeliveryAttemptFailedParams{
		OrgSlug:         args.OrganizationSlug,
		AttemptSlug:     args.AlertDeliveryAttemptSlug,
		ProviderAttempt: riverProviderAttempt(job),
		ErrorCategory:   errorCategory(cause),
		ErrorMessage:    notificationEmailDeliveryFailedMessage,
	})
}

// riverProviderAttempt returns River's attempt count with a safe first-attempt fallback.
func riverProviderAttempt(job *river.Job[jobs.EmailSendArgs]) int {
	if job == nil || job.JobRow == nil || job.Attempt <= 0 {
		return 1
	}
	return job.Attempt
}

// errorCategory maps typed application errors into safe diagnostic categories.
func errorCategory(err error) string {
	var typed *xerror.Error
	if xerror.As(err, &typed) {
		return typed.Code()
	}
	return "EMAIL_DELIVERY_FAILED"
}

func (w *emailSendWorker) tokenizedLink(
	args jobs.EmailSendArgs,
	missingTokenDetail string,
	decryptContext string,
) (string, error) {
	if len(args.EncryptedToken) == 0 {
		return "", xerror.ValidationFailed().Detail(missingTokenDetail)
	}

	token, err := w.tokenCipher.Decrypt(args.EncryptedToken)
	if err != nil {
		return "", xerror.Wrapf("%s: %w", decryptContext, err)
	}

	linkURL, err := url.Parse(args.Link)
	if err != nil {
		return "", xerror.ValidationFailed().With(err)
	}
	values := linkURL.Query()
	values.Set("token", string(token))
	linkURL.RawQuery = values.Encode()
	return linkURL.String(), nil
}

func (w *emailSendWorker) decryptedCode(args jobs.EmailSendArgs) (string, error) {
	if len(args.EncryptedCode) == 0 {
		return "", xerror.ValidationFailed().Detail("encrypted mfa code is required")
	}

	code, err := w.tokenCipher.Decrypt(args.EncryptedCode)
	if err != nil {
		return "", xerror.Wrapf("decrypt mfa code: %w", err)
	}
	return string(code), nil
}
