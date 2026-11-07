package consumer

import (
	"context"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestEmailSendWorkerSendsVerificationEmail(t *testing.T) {
	t.Parallel()

	emailSvc := &recordingEmailService{}
	worker := NewEmailSendWorker(emailSvc, fakeEmailTokenCipher{}, slog.New(slog.DiscardHandler))

	args := jobs.EmailSendArgs{
		Type: jobs.EmailTypeVerification,
		User: jobs.EmailRecipient{
			Slug:     slug.Slug("alice-smith"),
			Email:    "alice@example.com",
			FullName: "Alice Smith",
			Locale:   "en",
		},
		Link:           "https://app.example.com/verify-email",
		EncryptedToken: []byte("encrypted-email-verification-token"),
	}

	err := worker.Work(context.Background(), &river.Job[jobs.EmailSendArgs]{Args: args})

	require.NoError(t, err)
	require.Equal(t, 1, emailSvc.verificationCalls)
	require.Equal(t, args.User.ToUser(), emailSvc.verificationUser)
	require.Equal(t, "https://app.example.com/verify-email?token=raw-email-verification-token", emailSvc.verificationLink)
	require.Equal(t, 0, emailSvc.passwordResetCalls)
}

func TestEmailSendWorkerSendsPasswordResetEmail(t *testing.T) {
	t.Parallel()

	emailSvc := &recordingEmailService{}
	worker := NewEmailSendWorker(emailSvc, fakeEmailTokenCipher{}, slog.New(slog.DiscardHandler))

	args := jobs.EmailSendArgs{
		Type: jobs.EmailTypePasswordReset,
		User: jobs.EmailRecipient{
			Slug:     slug.Slug("alice-smith"),
			Email:    "alice@example.com",
			FullName: "Alice Smith",
			Locale:   "en",
		},
		Link:           "https://app.example.com/reset-password",
		EncryptedToken: []byte("encrypted-password-reset-token"),
	}

	err := worker.Work(context.Background(), &river.Job[jobs.EmailSendArgs]{Args: args})

	require.NoError(t, err)
	require.Equal(t, 1, emailSvc.passwordResetCalls)
	require.Equal(t, args.User.ToUser(), emailSvc.passwordResetUser)
	require.Equal(t, "https://app.example.com/reset-password?token=raw-password-reset-token", emailSvc.passwordResetLink)
	require.Equal(t, 0, emailSvc.verificationCalls)
}

func TestEmailSendWorkerSendsPersistedMessage(t *testing.T) {
	t.Parallel()

	emailSvc := &recordingEmailService{}
	worker := NewEmailSendWorker(emailSvc, fakeEmailTokenCipher{}, slog.New(slog.DiscardHandler))
	args := jobs.EmailSendArgs{
		Type: jobs.EmailTypeMessage,
		User: jobs.EmailRecipient{
			Slug:     slug.Slug("alice-smith"),
			Email:    "alice@example.com",
			FullName: "Alice Smith",
			Locale:   "en",
		},
		Subject: "Synthetic subject",
		Body:    "Synthetic body",
	}

	err := worker.Work(context.Background(), &river.Job[jobs.EmailSendArgs]{Args: args})

	require.NoError(t, err)
	require.Equal(t, 1, emailSvc.messageCalls)
	require.Equal(t, args.User.ToUser(), emailSvc.messageUser)
	require.Equal(t, args.Subject, emailSvc.messageSubject)
	require.Equal(t, args.Body, emailSvc.messageBody)
}

func TestEmailSendWorkerRejectsIncompleteMessage(t *testing.T) {
	t.Parallel()

	emailSvc := &recordingEmailService{}
	worker := NewEmailSendWorker(emailSvc, fakeEmailTokenCipher{}, slog.New(slog.DiscardHandler))

	err := worker.Work(context.Background(), &river.Job[jobs.EmailSendArgs]{Args: jobs.EmailSendArgs{
		Type: jobs.EmailTypeMessage,
		User: jobs.EmailRecipient{Email: "alice@example.com"},
	}})

	require.ErrorIs(t, err, xerror.ValidationFailed())
	require.Zero(t, emailSvc.messageCalls)
}

func TestEmailSendWorkerSendsMemberInvitationEmail(t *testing.T) {
	t.Parallel()

	emailSvc := &recordingEmailService{}
	worker := NewEmailSendWorker(emailSvc, fakeEmailTokenCipher{}, slog.New(slog.DiscardHandler))
	invitation := model.MemberInvitationEmail{
		Email:            "invitee@example.com",
		OrganizationName: "Acme",
		InviterName:      "Alice Smith",
		InviterEmail:     "alice@example.com",
		Role:             slug.Slug("viewer"),
		Locale:           "en",
	}
	args := jobs.EmailSendArgs{
		Type:           jobs.EmailTypeInvitation,
		Invitation:     invitation,
		Link:           "https://app.example.com/member-invitations/accept",
		EncryptedToken: []byte("encrypted-member-invitation-token"),
	}

	err := worker.Work(context.Background(), &river.Job[jobs.EmailSendArgs]{Args: args})

	require.NoError(t, err)
	require.Equal(t, 1, emailSvc.memberInvitationCalls)
	require.Equal(t, invitation, emailSvc.memberInvitation)
	require.Equal(t, "https://app.example.com/member-invitations/accept?token=raw-member-invitation-token", emailSvc.memberInvitationLink)
	require.Equal(t, 0, emailSvc.verificationCalls)
}

func TestEmailSendWorkerSendsNotificationEmail(t *testing.T) {
	t.Parallel()

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
	sendErr := xerror.Unexpected().Detail("smtp unavailable with token=secret")
	orgSlug := slug.Slug("acme")
	attemptSlug := slug.Slug("alert-delivery-abc123")

	tests := []struct {
		name              string
		notificationErr   error
		recorderSentErr   error
		recorderFailedErr error
		wantErr           error
	}{
		{
			name: "SendsNotificationEmail",
		},
		{
			name:            "DoesNotRetrySentNotificationWhenSuccessDiagnosticFails",
			recorderSentErr: xerror.Unexpected().Detail("database unavailable"),
		},
		{
			name:            "ReturnsNotificationEmailErrorForRetry",
			notificationErr: sendErr,
			wantErr:         sendErr,
		},
		{
			name:              "ReturnsNotificationEmailErrorWhenFailureDiagnosticFails",
			notificationErr:   sendErr,
			recorderFailedErr: xerror.Unexpected().Detail("database unavailable"),
			wantErr:           sendErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			emailSvc := &recordingEmailService{notificationErr: tc.notificationErr}
			recorder := &recordingAlertDeliveryAttemptRecorder{
				sentErr:   tc.recorderSentErr,
				failedErr: tc.recorderFailedErr,
			}
			worker := NewEmailSendWorker(
				emailSvc,
				fakeEmailTokenCipher{},
				slog.New(slog.DiscardHandler),
				WithAlertDeliveryAttemptRecorder(recorder),
			)

			err := worker.Work(context.Background(), &river.Job[jobs.EmailSendArgs]{
				JobRow: &rivertype.JobRow{Attempt: 3},
				Args: jobs.EmailSendArgs{
					Type:                     jobs.EmailTypeNotification,
					NotificationRecipient:    recipient,
					Notification:             notification,
					OrganizationSlug:         orgSlug,
					AlertDeliveryAttemptSlug: attemptSlug,
				},
			})

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, emailSvc.notificationCalls)
			require.Equal(t, recipient, emailSvc.notificationRecipient)
			require.Equal(t, notification, emailSvc.notification)
			require.Equal(t, 0, emailSvc.verificationCalls)
			if tc.wantErr != nil {
				require.Equal(t, 0, recorder.sentCalls)
				require.Equal(t, 1, recorder.failedCalls)
				require.Equal(t, orgSlug, recorder.failedParams.OrgSlug)
				require.Equal(t, attemptSlug, recorder.failedParams.AttemptSlug)
				require.Equal(t, 3, recorder.failedParams.ProviderAttempt)
				require.NotEmpty(t, recorder.failedParams.ErrorCategory)
				require.Equal(t, notificationEmailDeliveryFailedMessage, recorder.failedParams.ErrorMessage)
				require.NotContains(t, recorder.failedParams.ErrorMessage, "token=secret")
			} else {
				require.Equal(t, 1, recorder.sentCalls)
				require.Equal(t, 0, recorder.failedCalls)
				require.Equal(t, orgSlug, recorder.sentParams.OrgSlug)
				require.Equal(t, attemptSlug, recorder.sentParams.AttemptSlug)
				require.Equal(t, 3, recorder.sentParams.ProviderAttempt)
			}
		})
	}
}

func TestEmailSendWorkerSkipsDiagnosticsForEphemeralNotificationEmail(t *testing.T) {
	t.Parallel()

	recipient := model.NotificationEmailRecipient{Email: "ops@example.com", Locale: "en"}
	notification := model.NotificationEmail{
		OrganizationName:      "Acme",
		RecordSlug:            slug.Slug("alert-destination-test"),
		NotificationType:      model.NotificationTypePipelineRunFailed,
		NotificationTypeLabel: "alert destination test",
		Severity:              model.NotificationSeverityInfo,
		Summary:               "Test notification for alert destination ops-email.",
		SourceEntityType:      model.EventEntityTypeOrganization,
		SourceAction:          model.EventActionTestConnection,
		OccurrenceCount:       1,
	}
	emailSvc := &recordingEmailService{}
	recorder := &recordingAlertDeliveryAttemptRecorder{}
	worker := NewEmailSendWorker(
		emailSvc,
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		WithAlertDeliveryAttemptRecorder(recorder),
	)

	err := worker.Work(context.Background(), &river.Job[jobs.EmailSendArgs]{
		Args: jobs.EmailSendArgs{
			Type:                  jobs.EmailTypeNotification,
			NotificationRecipient: recipient,
			Notification:          notification,
			OrganizationSlug:      slug.Slug("acme"),
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, emailSvc.notificationCalls)
	require.Equal(t, recipient, emailSvc.notificationRecipient)
	require.Equal(t, notification, emailSvc.notification)
	require.Equal(t, 0, recorder.sentCalls)
	require.Equal(t, 0, recorder.failedCalls)
}

func TestEmailSendWorkerReturnsEmailServiceErrorForRetry(t *testing.T) {
	t.Parallel()

	sendErr := xerror.Unexpected().Detail("smtp unavailable")
	emailSvc := &recordingEmailService{verificationErr: sendErr}
	worker := NewEmailSendWorker(emailSvc, fakeEmailTokenCipher{}, slog.New(slog.DiscardHandler))

	err := worker.Work(context.Background(), &river.Job[jobs.EmailSendArgs]{
		Args: jobs.EmailSendArgs{
			Type: jobs.EmailTypeVerification,
			User: jobs.EmailRecipient{
				Slug:   slug.Slug("alice-smith"),
				Email:  "alice@example.com",
				Locale: "en",
			},
			Link:           "https://app.example.com/verify-email",
			EncryptedToken: []byte("encrypted-email-verification-token"),
		},
	})

	require.ErrorIs(t, err, sendErr)
}

type fakeEmailTokenCipher struct{}

func (fakeEmailTokenCipher) Encrypt(plaintext []byte) ([]byte, error) {
	return append([]byte("encrypted-"), plaintext...), nil
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

type recordingEmailService struct {
	verificationCalls int
	verificationUser  model.User
	verificationLink  string
	verificationErr   error

	passwordResetCalls int
	passwordResetUser  model.User
	passwordResetLink  string
	passwordResetErr   error

	sessionMFACalls int
	sessionMFAUser  model.User
	sessionMFACode  string
	sessionMFAErr   error

	memberInvitationCalls int
	memberInvitation      model.MemberInvitationEmail
	memberInvitationLink  string
	memberInvitationErr   error

	notificationCalls     int
	notificationRecipient model.NotificationEmailRecipient
	notification          model.NotificationEmail
	notificationErr       error

	messageCalls   int
	messageUser    model.User
	messageSubject string
	messageBody    string
	messageErr     error
}

func (s *recordingEmailService) Message(
	_ context.Context,
	user *model.User,
	subject, body string,
) error {
	s.messageCalls++
	s.messageUser = *user
	s.messageSubject = subject
	s.messageBody = body
	return s.messageErr
}

type recordingAlertDeliveryAttemptRecorder struct {
	sentCalls    int
	sentParams   notificationsvc.MarkAlertDeliveryAttemptSentParams
	sentErr      error
	failedCalls  int
	failedParams notificationsvc.MarkAlertDeliveryAttemptFailedParams
	failedErr    error
}

func (r *recordingAlertDeliveryAttemptRecorder) MarkAlertDeliveryAttemptSent(
	_ context.Context,
	params notificationsvc.MarkAlertDeliveryAttemptSentParams,
) error {
	r.sentCalls++
	r.sentParams = params
	return r.sentErr
}

func (r *recordingAlertDeliveryAttemptRecorder) MarkAlertDeliveryAttemptFailed(
	_ context.Context,
	params notificationsvc.MarkAlertDeliveryAttemptFailedParams,
) error {
	r.failedCalls++
	r.failedParams = params
	return r.failedErr
}

func (s *recordingEmailService) EmailVerification(
	_ context.Context,
	user *model.User,
	verificationLink string,
) error {
	s.verificationCalls++
	s.verificationUser = *user
	s.verificationLink = verificationLink
	return s.verificationErr
}

func (s *recordingEmailService) PasswordReset(
	_ context.Context,
	user *model.User,
	resetLink string,
) error {
	s.passwordResetCalls++
	s.passwordResetUser = *user
	s.passwordResetLink = resetLink
	return s.passwordResetErr
}

func (s *recordingEmailService) SessionMFA(
	_ context.Context,
	user *model.User,
	code string,
) error {
	s.sessionMFACalls++
	s.sessionMFAUser = *user
	s.sessionMFACode = code
	return s.sessionMFAErr
}

func (s *recordingEmailService) MemberInvitation(
	_ context.Context,
	invitation model.MemberInvitationEmail,
	acceptanceLink string,
) error {
	s.memberInvitationCalls++
	s.memberInvitation = invitation
	s.memberInvitationLink = acceptanceLink
	return s.memberInvitationErr
}

func (s *recordingEmailService) Notification(
	_ context.Context,
	recipient model.NotificationEmailRecipient,
	notification model.NotificationEmail,
) error {
	s.notificationCalls++
	s.notificationRecipient = recipient
	s.notification = notification
	return s.notificationErr
}
