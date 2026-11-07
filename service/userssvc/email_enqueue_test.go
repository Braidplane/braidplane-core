package userssvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/users"
)

func newEmailProducerFromService(emailSvc *testutil.FakeEmailService) *emailServiceBackedProducer {
	return &emailServiceBackedProducer{emailSvc: emailSvc}
}

type emailServiceBackedProducer struct {
	emailSvc *testutil.FakeEmailService
}

func (p *emailServiceBackedProducer) EnqueueEmailVerification(
	ctx context.Context,
	_ postgres.DBTX,
	user *model.User,
	verificationURL string,
	token string,
) (queue.QueuedJob, error) {
	verificationLink := verificationURL + "?token=" + token
	return queue.QueuedJob{State: "queued"}, p.emailSvc.EmailVerification(
		ctx,
		user,
		verificationLink,
	)
}

func (p *emailServiceBackedProducer) EnqueuePasswordReset(
	ctx context.Context,
	_ postgres.DBTX,
	user *model.User,
	resetURL string,
	token string,
) (queue.QueuedJob, error) {
	resetLink := resetURL + "?token=" + token
	return queue.QueuedJob{State: "queued"}, p.emailSvc.PasswordReset(ctx, user, resetLink)
}

func (p *emailServiceBackedProducer) EnqueueSessionMFA(
	ctx context.Context,
	_ postgres.DBTX,
	user *model.User,
	code string,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{State: "queued"}, p.emailSvc.SessionMFA(ctx, user, code)
}

func (p *emailServiceBackedProducer) EnqueueMemberInvitation(
	ctx context.Context,
	_ postgres.DBTX,
	invitation model.MemberInvitationEmail,
	acceptanceURL string,
	token string,
) (queue.QueuedJob, error) {
	acceptanceLink := acceptanceURL + "?token=" + token
	return queue.QueuedJob{State: "queued"}, p.emailSvc.MemberInvitation(ctx, invitation, acceptanceLink)
}

func (p *emailServiceBackedProducer) EnqueueNotification(
	_ context.Context,
	_ postgres.DBTX,
	_ slug.Slug,
	_ model.NotificationEmailRecipient,
	_ model.NotificationEmail,
	_ slug.Slug,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{State: "queued"}, nil
}

func TestCreateUserEnqueuesVerificationEmail(t *testing.T) {
	t.Parallel()

	repo := &testutil.FakeUsersRepo{}
	repo.IsSlugReservedFn = func(_ context.Context, _ postgres.DBTX, userSlug slug.Slug) (bool, error) {
		require.Regexp(t, `^user-[a-z0-9]{32}$`, userSlug.String())
		return false, nil
	}
	repo.CreateUserFn = func(_ context.Context, _ postgres.DBTX, p *users.CreateParams) (*users.UserPublic, error) {
		require.Regexp(t, `^user-[a-z0-9]{32}$`, p.Slug.String())
		return testutil.StubUserPublic(
			must.Slug("alice-smith"),
			"Alice Smith",
			"alice@example.com",
			model.RoleUser,
		), nil
	}
	repo.CreateEmailVerificationTokenFn = func(_ context.Context, _ postgres.DBTX, params users.CreateEmailVerificationTokenParams) error {
		require.Equal(t, must.Slug("alice-smith"), params.Slug)
		return nil
	}
	emailProducer := &recordingEmailProducer{}
	svc := New(
		testutil.FakeTransactionManager{},
		&testutil.FakeHasher{},
		repo,
		emailProducer,
		nil,
		"https://app.example.com",
	)

	got, err := svc.CreateUser(context.Background(), model.NewUser{
		Email:    "alice@example.com",
		FullName: "Alice Smith",
		Password: "securepass123",
		Role:     model.RoleUser,
		Locale:   "en",
	})

	require.NoError(t, err)
	require.Equal(t, must.Slug("alice-smith"), got.Slug)
	require.Equal(t, 1, emailProducer.verificationCalls)
	require.Nil(t, emailProducer.verificationTx)
	require.Equal(t, must.Slug("alice-smith"), emailProducer.verificationUser.Slug)
	require.Equal(t, "alice@example.com", emailProducer.verificationUser.Email)
	require.Equal(t, "Alice Smith", emailProducer.verificationUser.FullName)
	require.Equal(t, model.RoleUser, emailProducer.verificationUser.Role)
	require.Equal(t, "en", emailProducer.verificationUser.Locale)
	require.Equal(t, "https://app.example.com/verify-email", emailProducer.verificationURL)
	require.NotEmpty(t, emailProducer.verificationToken)
	require.NotContains(t, emailProducer.verificationURL, emailProducer.verificationToken)
	require.NotContains(t, emailProducer.verificationURL, "token=")
	require.Zero(t, emailProducer.messageCalls)
}

func TestCreateUserReturnsEmailEnqueueError(t *testing.T) {
	t.Parallel()

	repo := &testutil.FakeUsersRepo{}
	repo.IsSlugReservedFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug) (bool, error) {
		return false, nil
	}
	repo.CreateUserFn = func(_ context.Context, _ postgres.DBTX, _ *users.CreateParams) (*users.UserPublic, error) {
		return testutil.StubUserPublic(
			must.Slug("alice-smith"),
			"Alice Smith",
			"alice@example.com",
			model.RoleUser,
		), nil
	}
	repo.CreateEmailVerificationTokenFn = func(_ context.Context, _ postgres.DBTX, _ users.CreateEmailVerificationTokenParams) error {
		return nil
	}
	enqueueErr := xerror.Unexpected().Detail("enqueue email failed")
	emailProducer := &recordingEmailProducer{err: enqueueErr}
	svc := New(
		testutil.FakeTransactionManager{},
		&testutil.FakeHasher{},
		repo,
		emailProducer,
		nil,
		"https://app.example.com",
	)

	got, err := svc.CreateUser(context.Background(), model.NewUser{
		Email:    "alice@example.com",
		FullName: "Alice Smith",
		Password: "securepass123",
		Role:     model.RoleUser,
		Locale:   "en",
	})

	require.Nil(t, got)
	require.ErrorIs(t, err, enqueueErr)
}

func TestRequestPasswordResetEnqueuesPasswordResetEmail(t *testing.T) {
	t.Parallel()

	repo := &testutil.FakeUsersRepo{}
	repo.FindByEmailFn = func(_ context.Context, _ postgres.DBTX, email string) (*users.UserPublic, error) {
		require.Equal(t, "alice@example.com", email)
		return testutil.StubUserPublic(
			must.Slug("alice-smith"),
			"Alice Smith",
			email,
			model.RoleUser,
		), nil
	}
	repo.CreatePasswordResetTokenFn = func(_ context.Context, _ postgres.DBTX, params users.CreatePasswordResetTokenParams) error {
		require.Equal(t, must.Slug("alice-smith"), params.Slug)
		return nil
	}
	emailProducer := &recordingEmailProducer{}
	svc := New(
		testutil.FakeTransactionManager{},
		&testutil.FakeHasher{},
		repo,
		emailProducer,
		nil,
		"https://app.example.com",
	)

	err := svc.RequestPasswordReset(context.Background(), "alice@example.com")

	require.NoError(t, err)
	require.Equal(t, 1, emailProducer.passwordResetCalls)
	require.Equal(t, must.Slug("alice-smith"), emailProducer.passwordResetUser.Slug)
	require.Equal(t, "alice@example.com", emailProducer.passwordResetUser.Email)
	require.Equal(t, "https://app.example.com/auth/reset-password", emailProducer.passwordResetURL)
	require.NotEmpty(t, emailProducer.passwordResetToken)
	require.NotContains(t, emailProducer.passwordResetURL, emailProducer.passwordResetToken)
	require.NotContains(t, emailProducer.passwordResetURL, "token=")
}

func TestResendVerificationEmailEnqueuesVerificationEmail(t *testing.T) {
	t.Parallel()

	repo := &testutil.FakeUsersRepo{}
	repo.FindByEmailFn = func(_ context.Context, _ postgres.DBTX, email string) (*users.UserPublic, error) {
		require.Equal(t, "alice@example.com", email)
		return testutil.StubUserPublic(
			must.Slug("alice-smith"),
			"Alice Smith",
			email,
			model.RoleUser,
		), nil
	}
	repo.CreateEmailVerificationTokenFn = func(_ context.Context, _ postgres.DBTX, params users.CreateEmailVerificationTokenParams) error {
		require.Equal(t, must.Slug("alice-smith"), params.Slug)
		return nil
	}
	emailProducer := &recordingEmailProducer{}
	svc := New(
		testutil.FakeTransactionManager{},
		&testutil.FakeHasher{},
		repo,
		emailProducer,
		nil,
		"https://app.example.com",
	)

	err := svc.ResendVerificationEmail(context.Background(), "alice@example.com")

	require.NoError(t, err)
	require.Equal(t, 1, emailProducer.verificationCalls)
	require.Equal(t, must.Slug("alice-smith"), emailProducer.verificationUser.Slug)
	require.Equal(t, "alice@example.com", emailProducer.verificationUser.Email)
	require.Equal(t, "https://app.example.com/verify-email", emailProducer.verificationURL)
	require.NotEmpty(t, emailProducer.verificationToken)
	require.NotContains(t, emailProducer.verificationURL, emailProducer.verificationToken)
	require.NotContains(t, emailProducer.verificationURL, "token=")
}

type recordingEmailProducer struct {
	messageCalls      int
	verificationCalls int
	verificationTx    postgres.DBTX
	verificationUser  model.User
	verificationURL   string
	verificationToken string

	passwordResetCalls int
	passwordResetTx    postgres.DBTX
	passwordResetUser  model.User
	passwordResetURL   string
	passwordResetToken string

	sessionMFACalls int
	sessionMFATx    postgres.DBTX
	sessionMFAUser  model.User
	sessionMFACode  string

	err error
}

func (p *recordingEmailProducer) EnqueueEmailVerification(
	_ context.Context,
	tx postgres.DBTX,
	user *model.User,
	verificationURL string,
	token string,
) (queue.QueuedJob, error) {
	p.verificationCalls++
	p.verificationTx = tx
	p.verificationUser = *user
	p.verificationURL = verificationURL
	p.verificationToken = token
	return queue.QueuedJob{State: "queued"}, p.err
}

func (p *recordingEmailProducer) EnqueuePasswordReset(
	_ context.Context,
	tx postgres.DBTX,
	user *model.User,
	resetURL string,
	token string,
) (queue.QueuedJob, error) {
	p.passwordResetCalls++
	p.passwordResetTx = tx
	p.passwordResetUser = *user
	p.passwordResetURL = resetURL
	p.passwordResetToken = token
	return queue.QueuedJob{State: "queued"}, p.err
}

func (p *recordingEmailProducer) EnqueueSessionMFA(
	_ context.Context,
	tx postgres.DBTX,
	user *model.User,
	code string,
) (queue.QueuedJob, error) {
	p.sessionMFACalls++
	p.sessionMFATx = tx
	p.sessionMFAUser = *user
	p.sessionMFACode = code
	return queue.QueuedJob{State: "queued"}, p.err
}

func (p *recordingEmailProducer) EnqueueMemberInvitation(
	_ context.Context,
	_ postgres.DBTX,
	_ model.MemberInvitationEmail,
	_ string,
	_ string,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{State: "queued"}, p.err
}

func (p *recordingEmailProducer) EnqueueNotification(
	_ context.Context,
	_ postgres.DBTX,
	_ slug.Slug,
	_ model.NotificationEmailRecipient,
	_ model.NotificationEmail,
	_ slug.Slug,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{State: "queued"}, p.err
}

func (p *emailServiceBackedProducer) EnqueueMessage(ctx context.Context, _ postgres.DBTX, user *model.User, subject, body string) (queue.QueuedJob, error) {
	return queue.QueuedJob{State: "queued"}, p.emailSvc.Message(ctx, user, subject, body)
}

func (p *recordingEmailProducer) EnqueueMessage(context.Context, postgres.DBTX, *model.User, string, string) (queue.QueuedJob, error) {
	p.messageCalls++
	return queue.QueuedJob{State: "queued"}, p.err
}
