package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"testing"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/instanceadmins"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/email"
	"github.com/Braidplane/braidplane-core/service/instanceadminsvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
	"github.com/Braidplane/braidplane-core/service/worker/consumer"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/Braidplane/braidplane-core/service/worker/producer"
)

func TestBootstrappedLocalAdministratorPasswordRecovery(t *testing.T) {
	pool, _ := roleFixture(t)
	ctx := t.Context()
	subject := must.Slug("recovery-owner")
	address := "recovery-owner@customer.example.test"
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "DELETE FROM river_job WHERE queue='bootstrap-recovery'")
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), "DELETE FROM instance_admin_audits WHERE target_user_slug=$1", subject)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), "DELETE FROM users WHERE slug=$1", subject)
		require.NoError(t, err)
	})
	txm := postgres.NewTransactionManager(pool)
	repo := users.NewRepository()
	hasher := crypto.NewArgon2WithParams(8, 1, 1, 16, 32)
	bootstrap, err := instanceadminsvc.NewBootstrapper(txm, instanceadmins.NewRepository(), repo, hasher)
	require.NoError(t, err)
	require.NoError(t, bootstrap.Bootstrap(ctx, subject, "Customer Owner", address, "initial-customer-password"))
	cipher, err := crypto.NewAES256(bytes.Repeat([]byte{0x42}, 32))
	require.NoError(t, err)
	client, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	mailProducer, err := producer.NewEmailProducerForQueue(queue.NewEnqueuer(client), cipher, "bootstrap-recovery")
	require.NoError(t, err)
	svc := userssvc.New(txm, hasher, repo, mailProducer, nil, "https://app.customer.example.test")
	user, err := svc.LoginUser(ctx, address, "initial-customer-password")
	require.NoError(t, err)
	require.Equal(t, model.RoleSuperAdmin, user.Role)
	require.NoError(t, svc.RequestPasswordReset(ctx, "unknown@customer.example.test"))
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM river_job WHERE queue='bootstrap-recovery'").Scan(&count))
	require.Zero(t, count, "unknown accounts must not enqueue a reset")
	require.NoError(t, svc.RequestPasswordReset(ctx, address))
	var raw []byte
	require.NoError(t, pool.QueryRow(ctx, "SELECT args FROM river_job WHERE queue='bootstrap-recovery'").Scan(&raw))
	var args jobs.EmailSendArgs
	require.NoError(t, json.Unmarshal(raw, &args))
	require.Equal(t, jobs.EmailTypePasswordReset, args.Type)
	require.NotEmpty(t, args.EncryptedToken)
	require.Equal(t, "https://app.customer.example.test/auth/reset-password", args.Link)

	// Pending reset jobs cannot cross an application-key change; reissue instead.
	replacementCipher, err := crypto.NewAES256(bytes.Repeat([]byte{0x43}, 32))
	require.NoError(t, err)
	var failureLog bytes.Buffer
	failedDelivery := &recoveryMail{}
	failedWorker := consumer.NewEmailSendWorker(failedDelivery, replacementCipher, slog.New(slog.NewTextHandler(&failureLog, nil)))
	require.Error(t, failedWorker.Work(ctx, &river.Job[jobs.EmailSendArgs]{Args: args}))
	require.Empty(t, failedDelivery.link)
	oldToken, err := cipher.Decrypt(args.EncryptedToken)
	require.NoError(t, err)
	require.NotContains(t, failureLog.String(), string(oldToken))
	require.NotContains(t, failureLog.String(), string(args.EncryptedToken))
	replacementProducer, err := producer.NewEmailProducerForQueue(queue.NewEnqueuer(client), replacementCipher, "bootstrap-recovery")
	require.NoError(t, err)
	svc = userssvc.New(txm, hasher, repo, replacementProducer, nil, "https://app.customer.example.test")
	require.NoError(t, svc.RequestPasswordReset(ctx, address))
	require.NoError(t, pool.QueryRow(ctx, "SELECT args FROM river_job WHERE queue='bootstrap-recovery' ORDER BY id DESC LIMIT 1").Scan(&raw))
	require.NoError(t, json.Unmarshal(raw, &args))
	delivery := &recoveryMail{}
	worker := consumer.NewEmailSendWorker(delivery, replacementCipher, slog.New(slog.DiscardHandler))
	require.NoError(t, worker.Work(ctx, &river.Job[jobs.EmailSendArgs]{Args: args}))
	link, err := url.Parse(delivery.link)
	require.NoError(t, err)
	token := link.Query().Get("token")
	require.NotEmpty(t, token)
	require.NotContains(t, string(raw), token, "reset token must not be stored in plaintext queue args")
	require.Equal(t, address, delivery.recipient)
	require.NoError(t, svc.ResetPassword(ctx, token, "replacement-customer-password"))
	_, err = svc.LoginUser(ctx, address, "initial-customer-password")
	require.Error(t, err)
	user, err = svc.LoginUser(ctx, address, "replacement-customer-password")
	require.NoError(t, err)
	require.Equal(t, subject, user.Slug)
	require.Equal(t, model.RoleSuperAdmin, user.Role)
	require.Error(t, svc.ResetPassword(ctx, token, "reused-token-password"))
}

type recoveryMail struct {
	email.Service
	link, recipient string
}

func (m *recoveryMail) PasswordReset(_ context.Context, user *model.User, link string) error {
	m.link, m.recipient = link, user.Email
	return nil
}
