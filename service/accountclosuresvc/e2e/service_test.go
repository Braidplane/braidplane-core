package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/accountclosures"
	"github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/postgres/executionrequests"
	"github.com/Braidplane/braidplane-core/postgres/members"
	"github.com/Braidplane/braidplane-core/postgres/pats"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/accountclosuresvc"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
	"github.com/Braidplane/braidplane-core/service/worker"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

var errAuditUnavailable = xerror.Unexpected().Detail("injected audit failure")

func TestAccountDeletionReregistrationCannotReclaimIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	for _, method := range []string{"Password", "OIDC"} {
		t.Run(method, func(t *testing.T) {
			ctx := t.Context()
			pool := e2edb.OpenPostgresPool(t, "account identity reuse")
			oldSlug := slug.Slug(fmt.Sprintf("deleted-person-%d", time.Now().UnixNano()))
			email := oldSlug.String() + "@example.com"
			_, err := pool.Exec(ctx, `INSERT INTO users (slug, email, full_name, role, oidc_provider, oidc_provider_id)
				VALUES ($1, $2, $1, 'user', 'google', $1)`, oldSlug, email)
			require.NoError(t, err)
			txm := postgres.NewTransactionManager(pool)
			queueClient, err := queue.NewEnqueueOnly(pool)
			require.NoError(t, err)
			closure, err := accountclosuresvc.New(txm, accountclosures.Repository{}, eventtest.NewProducer(eventssvc.New(txm, events.New(), nil)), queueClient)
			require.NoError(t, err)
			registration := userssvc.New(txm, &testutil.FakeHasher{}, users.NewRepository(), &testutil.FakeEmailProducer{}, nil, "https://app.example.com")
			t.Cleanup(func() {
				_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM users WHERE email = $1`, email)
				require.NoError(t, cleanupErr)
			})
			result, err := closure.Request(ctx, oldSlug)
			require.NoError(t, err)
			require.Equal(t, accountclosuresvc.StateCompleted, result.State)
			t.Cleanup(func() {
				_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM account_deletion_requests WHERE slug = $1`, result.Slug)
				require.NoError(t, cleanupErr)
			})
			var replacement *model.User
			if method == "Password" {
				replacement, err = registration.CreateUser(ctx, model.NewUser{FullName: oldSlug.String(), Email: email, Password: "test-password", Role: model.RoleUser})
			} else {
				profile := &model.OIDCUserInfo{Name: oldSlug.String(), Email: email, Provider: "google", ProviderID: oldSlug.String()}
				replacement, err = registration.LoginOrCreateOIDCUser(ctx, profile)
				require.NoError(t, err)
				again, loginErr := registration.LoginOrCreateOIDCUser(ctx, profile)
				require.NoError(t, loginErr)
				require.Equal(t, replacement.Slug, again.Slug)
			}
			require.NoError(t, err)
			require.Regexp(t, `^user-[a-z0-9]{32}$`, replacement.Slug.String())
			require.NotEqual(t, oldSlug, replacement.Slug)
			enqueuer := queue.NewEnqueuer(queueClient)
			err = txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
				_, enqueueErr := enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{Args: jobs.EmailSendArgs{User: jobs.EmailRecipient{Slug: oldSlug, Email: email}}, InitiatingAccount: oldSlug})
				return enqueueErr
			})
			require.ErrorIs(t, err, executiondelivery.ErrAccountUnavailable)
			_, err = registration.GetUserBySlug(ctx, replacement.Slug)
			require.NoError(t, err)
		})
	}
}

type auditRecorder struct {
	eventssvc.Service
	fail    bool
	entered chan struct{}
	release chan struct{}
}

func (r *auditRecorder) CreateEvent(ctx context.Context, params eventssvc.CreateEventParams) (*model.Event, error) {
	event, err := r.Service.CreateEvent(ctx, params)
	if err != nil {
		return nil, err
	}
	if r.fail {
		return nil, errAuditUnavailable
	}
	if r.entered != nil {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return event, nil
}

func TestAccountClosureIsScopedRecoverableAndPreservesEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	for _, scenario := range []string{"Normal", "LastOwner", "SoleMember", "AuditFailure", "ConcurrentOwners"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			pool := e2edb.OpenPostgresPool(t, "account closure")
			prefix := slug.Slug(fmt.Sprintf("closure-%d", time.Now().UnixNano()))
			alice, bob, org := prefix+"-alice", prefix+"-bob", prefix+"-org"
			var aliceID, bobID int32
			for name, id := range map[slug.Slug]*int32{alice: &aliceID, bob: &bobID} {
				err := pool.QueryRow(ctx, `INSERT INTO users (slug, email, full_name, password, role, oidc_provider, oidc_provider_id,
					phone_number, job_title)
					VALUES ($1, $2, 'Test Person', 'test-only-hash', 'user', 'test-oidc', $1, '123', 'Engineer') RETURNING id`,
					name, name.String()+"@example.com").Scan(id)
				require.NoError(t, err)
			}
			_, err := pool.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1)`, org)
			require.NoError(t, err)
			t.Cleanup(func() {
				for _, query := range []string{
					`DELETE FROM account_deletion_requests WHERE user_id IN ($1, $2)`,
				} {
					_, cleanupErr := pool.Exec(context.Background(), query, aliceID, bobID)
					require.NoError(t, cleanupErr)
				}
				_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM river_job WHERE args->>'organization_slug' = $1`, org)
				require.NoError(t, cleanupErr)
				_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, org)
				require.NoError(t, cleanupErr)
				_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = $1`, org)
				require.NoError(t, cleanupErr)
				_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1, $2)`, aliceID, bobID)
				require.NoError(t, cleanupErr)
			})
			_, err = pool.Exec(ctx, `INSERT INTO roles (organization_id, slug, name) SELECT id, role, role FROM organizations CROSS JOIN unnest(ARRAY['admin', 'viewer']) role WHERE slug = $1`, org)
			require.NoError(t, err)
			bobRole := slug.Slug("admin")
			if scenario == "LastOwner" {
				bobRole = "viewer"
			}
			for user, role := range map[slug.Slug]slug.Slug{alice: "admin", bob: bobRole} {
				if scenario == "SoleMember" && user == bob {
					continue
				}
				_, err := members.NewRepository().Create(ctx, pool, &members.CreateParams{OrganizationSlug: org, UserSlug: user, RoleSlug: role})
				require.NoError(t, err)
			}
			_, err = pool.Exec(ctx, `INSERT INTO personal_access_tokens (user_id, slug, name, token_hash, token_prefix) VALUES ($1, 'token', 'test', $2, 'test-only')`, aliceID, prefix.String())
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `INSERT INTO user_mfa_challenges (user_id, challenge_token_hash, code_hash, expires_at) VALUES ($1, $2, 'test-only', NOW()+INTERVAL '1 hour')`, aliceID, prefix.String())
			require.NoError(t, err)

			_, err = pool.Exec(ctx, `INSERT INTO organization_member_invitations (organization_id, slug, email, role_id, token_hash, status, expires_at, accepted_by_user_id, accepted_at)
				SELECT o.id, invitation, $2, r.id, $3 || invitation, CASE WHEN invitation = 'accepted' THEN 'accepted' ELSE 'pending' END,
				NOW()+INTERVAL '1 day', CASE WHEN invitation = 'accepted' THEN $4::integer ELSE NULL END, CASE WHEN invitation = 'accepted' THEN NOW() ELSE NULL END
				FROM organizations o JOIN roles r ON r.organization_id = o.id AND r.slug = 'admin'
				CROSS JOIN unnest(ARRAY['accepted', 'pending']) invitation WHERE o.slug = $1`, org, alice.String()+"@example.com", prefix, aliceID)
			require.NoError(t, err)
			executions := executionrequests.New()
			pending, running, other := uuid.New(), uuid.New(), uuid.New()
			for id, user := range map[uuid.UUID]slug.Slug{pending: alice, running: alice, other: bob} {
				require.NoError(t, executions.Create(ctx, pool, executionrequests.Request{ID: id, OrganizationSlug: org.String(), Kind: "transform", RequestCiphertext: []byte("test-only"), DeadlineAt: time.Now().Add(time.Hour)}, time.Now().Add(time.Hour)))
				require.NoError(t, executions.BindUser(ctx, pool, id, org.String(), user))
			}
			_, err = pool.Exec(ctx, `UPDATE execution_requests SET state = 'running' WHERE id = $1`, running)
			require.NoError(t, err)
			txm := postgres.NewTransactionManager(pool)
			audit := &auditRecorder{Service: eventssvc.New(txm, events.New(), nil), fail: scenario == "AuditFailure"}
			queueClient, err := queue.NewEnqueueOnly(pool)
			require.NoError(t, err)
			enqueuer := queue.NewEnqueuer(queueClient)
			gitArgs := []queue.JobArgs{
				jobs.PipelineGitWriteFileArgs{InitiatingUserSlug: alice, OrganizationSlug: org},
				jobs.PipelineGitDeleteFileArgs{InitiatingUserSlug: alice, OrganizationSlug: org},
				jobs.PipelineGitPublishProposalArgs{MemberSlug: alice, OrganizationSlug: org, PublishAttemptID: prefix.String()},
				jobs.PipelineGitRefreshProposalArgs{MemberSlug: alice, OrganizationSlug: org},
				jobs.PipelineRunExecutionArgs{Actor: model.UserEventActor(alice), OrganizationSlug: org},
				jobs.EmailSendArgs{User: jobs.EmailRecipient{Slug: alice, Email: alice.String() + "@example.com"}, OrganizationSlug: org},
				jobs.PipelineGitWriteFileArgs{InitiatingUserSlug: bob, OrganizationSlug: org},
				jobs.PipelineGitWriteFileArgs{OrganizationSlug: org},
			}
			require.NoError(t, txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
				for _, args := range gitArgs {
					if _, err := enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{Args: args}); err != nil {
						return err
					}
				}
				return nil
			}))
			_, err = pool.Exec(ctx, `UPDATE river_job SET state = 'completed', finalized_at = NOW() WHERE args->>'organization_slug' = $1 AND kind = 'pipeline_git_write_file' AND COALESCE(args->>'initiating_user_slug','') = ''`, org)
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `UPDATE river_job SET state = 'running', attempted_at = NOW(), attempt = 1
				WHERE args->>'organization_slug' = $1 AND kind = 'pipeline_git_refresh_proposal'`, org)
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `UPDATE river_job SET state = CASE
				WHEN kind = 'pipeline_git_delete_file' THEN 'scheduled'::river_job_state ELSE 'retryable'::river_job_state END,
				scheduled_at = NOW() + INTERVAL '1 hour'
				WHERE args->>'organization_slug' = $1 AND kind IN ('pipeline_git_delete_file', 'pipeline_git_publish_proposal')`, org)
			require.NoError(t, err)
			assertGitJobs := func(cancelled bool) {
				t.Helper()
				var cancelledCount, runningSignalled, untouchedCount int
				require.NoError(t, pool.QueryRow(ctx, `SELECT
					count(*) FILTER (WHERE state = 'cancelled'),
					count(*) FILTER (WHERE state = 'running' AND metadata ? 'cancel_attempted_at'),
					count(*) FILTER (WHERE state IN ('available', 'scheduled', 'retryable', 'completed'))
					FROM river_job WHERE args->>'organization_slug' = $1`, org).Scan(&cancelledCount, &runningSignalled, &untouchedCount))
				if cancelled {
					require.Equal(t, 5, cancelledCount)
					require.Equal(t, 1, runningSignalled)
					require.Equal(t, 2, untouchedCount, "other-account and unattributed jobs must remain untouched")
				} else {
					require.Zero(t, cancelledCount)
					require.Zero(t, runningSignalled)
					require.Equal(t, 7, untouchedCount)
				}
			}
			svc, err := accountclosuresvc.New(txm, accountclosures.Repository{}, eventtest.NewProducer(audit), queueClient)
			require.NoError(t, err)
			if scenario == "Normal" {
				admission, err := pool.Begin(ctx)
				require.NoError(t, err)
				defer func() { _ = admission.Rollback(context.Background()) }()
				_, err = enqueuer.EnqueueTx(ctx, admission, queue.EnqueueRequest{Args: gitArgs[0], InitiatingAccount: alice})
				require.NoError(t, err)
				blocked, cancelBlocked := context.WithTimeout(ctx, 150*time.Millisecond)
				_, blockedErr := svc.Request(blocked, alice)
				cancelBlocked()
				require.ErrorIs(t, blockedErr, context.DeadlineExceeded)
				require.NoError(t, admission.Rollback(ctx))
			}
			var result accountclosuresvc.Result
			if scenario == "ConcurrentOwners" {
				audit.entered, audit.release = make(chan struct{}), make(chan struct{})
				defer func() {
					select {
					case <-audit.release:
					default:
						close(audit.release)
					}
				}()
				type outcome struct {
					result accountclosuresvc.Result
					err    error
				}
				finished := make(chan outcome, 1)
				go func() {
					result, err := svc.Request(ctx, alice)
					finished <- outcome{result: result, err: err}
				}()
				select {
				case <-audit.entered:
				case <-ctx.Done():
					t.Fatal("first closure did not reach its audit stage")
				}
				blocked, cancelBlocked := context.WithTimeout(ctx, 150*time.Millisecond)
				_, blockedErr := svc.Request(blocked, bob)
				cancelBlocked()
				require.ErrorIs(t, blockedErr, context.DeadlineExceeded)
				blockedEnqueue, cancelEnqueue := context.WithTimeout(ctx, 150*time.Millisecond)
				enqueueErr := txm.RunInTx(blockedEnqueue, func(ctx context.Context, tx postgres.DBTX) error {
					_, err := enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{Args: gitArgs[0], InitiatingAccount: alice})
					return err
				})
				cancelEnqueue()
				require.ErrorIs(t, enqueueErr, context.DeadlineExceeded)
				close(audit.release)
				first := <-finished
				result, err = first.result, first.err
				blockedResult, blockedErr := svc.Request(ctx, bob)
				require.NoError(t, blockedErr)
				require.Equal(t, accountclosuresvc.StateOwnershipRequired, blockedResult.State)
				require.Nil(t, blockedResult.CompletedAt)
			} else {
				result, err = svc.Request(ctx, alice)
			}
			if scenario == "AuditFailure" {
				assertGitJobs(false)
				require.ErrorIs(t, err, errAuditUnavailable)
				require.Empty(t, result.Slug)
				var untouched bool
				require.NoError(t, pool.QueryRow(ctx, `SELECT deleted_at IS NULL AND full_name = 'Test Person' FROM users WHERE id = $1`, aliceID).Scan(&untouched))
				require.True(t, untouched)
				var credentials, tenantEvents int
				require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM personal_access_tokens WHERE user_id = $1), (SELECT count(*) FROM events WHERE organization_id = (SELECT id FROM organizations WHERE slug = $2))`, aliceID, org).Scan(&credentials, &tenantEvents))
				require.Equal(t, 1, credentials)
				require.Zero(t, tenantEvents)
				audit.fail = false
				result, err = svc.Request(ctx, alice)
			}
			require.NoError(t, err)
			if scenario == "LastOwner" {
				assertGitJobs(false)
				require.Equal(t, accountclosuresvc.StateOwnershipRequired, result.State)
				require.Equal(t, []slug.Slug{org}, result.OwnershipRequired)
				require.Nil(t, result.CompletedAt)
				require.NoError(t, members.NewRepository().Update(ctx, pool, org, bob, "admin"))
				result, err = svc.Request(ctx, alice)
				require.NoError(t, err)
			}
			if scenario == "SoleMember" {
				assertGitJobs(false)
				require.Equal(t, accountclosuresvc.StateOwnershipRequired, result.State)
				require.Equal(t, []slug.Slug{org}, result.OrganizationRemovalRequired)
				require.Empty(t, result.OwnershipRequired)
				require.Nil(t, result.CompletedAt)
				var active bool
				require.NoError(t, pool.QueryRow(ctx, `SELECT deleted_at IS NULL FROM users WHERE id = $1`, aliceID).Scan(&active))
				require.True(t, active)
				var membersRemaining int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM organization_members m JOIN organizations o ON o.id = m.organization_id WHERE o.slug = $1`, org).Scan(&membersRemaining))
				require.Equal(t, 1, membersRemaining)
				return
			}
			t.Cleanup(func() {
				_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM river_job WHERE kind = 'account_deletion' AND args->>'request' = $1`, result.Slug)
				require.NoError(t, cleanupErr)
				_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM account_deletion_requests WHERE slug = $1`, result.Slug)
				require.NoError(t, cleanupErr)
			})
			require.Equal(t, accountclosuresvc.StatePending, result.State)
			require.Nil(t, result.CompletedAt)
			assertGitJobs(true)
			for _, args := range gitArgs[:6] {
				err := txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
					_, err := enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{Args: args, InitiatingAccount: alice})
					return err
				})
				require.ErrorIs(t, err, executiondelivery.ErrAccountUnavailable)
			}
			var deleted, credentialsCleared bool
			var name, email string
			require.NoError(t, pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL, password IS NULL AND oidc_provider IS NULL AND oidc_provider_id IS NULL, full_name, email FROM users WHERE id = $1`, aliceID).Scan(&deleted, &credentialsCleared, &name, &email))
			require.True(t, deleted)
			require.True(t, credentialsCleared)
			require.Equal(t, "Test Person", name)
			require.Equal(t, alice.String()+"@example.com", email)
			for _, table := range []string{"personal_access_tokens", "user_mfa_challenges", "organization_members"} {
				var count int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE user_id = $1`, aliceID).Scan(&count))
				require.Zero(t, count, table)
			}
			_, err = pats.NewRepository().Create(ctx, pool, pats.CreateParams{UserSlug: alice, Slug: "late", Name: "test", TokenHash: prefix.String() + "-late", TokenPrefix: "test-only"})
			require.ErrorIs(t, err, pgx.ErrNoRows)
			err = users.NewRepository().CreateMFAChallenge(ctx, pool, alice, prefix.String()+"-late", "test-only", time.Now().Add(time.Hour))
			require.ErrorIs(t, err, pgx.ErrNoRows)
			_, err = members.NewRepository().Create(ctx, pool, &members.CreateParams{OrganizationSlug: org, UserSlug: alice, RoleSlug: "admin"})
			var memberError *pgconn.PgError
			require.ErrorAs(t, err, &memberError)
			require.Equal(t, "23502", memberError.Code)
			require.Equal(t, "user_id", memberError.ColumnName)
			var remainingMember, tenantEvents int
			require.NoError(t, pool.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM organization_members WHERE user_id = $1),
				(SELECT count(*) FROM events WHERE organization_id = (SELECT id FROM organizations WHERE slug = $2))`, bobID, org).Scan(&remainingMember, &tenantEvents))
			require.Equal(t, 1, remainingMember)
			require.Equal(t, 1, tenantEvents)
			var invitationState string
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*)::text FROM organization_member_invitations WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1) AND slug = 'pending'`, org).Scan(&invitationState))
			require.Equal(t, "0", invitationState)
			for id, want := range map[uuid.UUID]string{pending: "cancelled", running: "running", other: "pending"} {
				var state string
				var cancellationRequested bool
				require.NoError(t, pool.QueryRow(ctx, `SELECT state, cancel_requested_at IS NOT NULL FROM execution_requests WHERE id = $1`, id).Scan(&state, &cancellationRequested))
				require.Equal(t, want, state)
				require.Equal(t, id != other, cancellationRequested)
			}
			retry, err := svc.Request(ctx, alice)
			require.NoError(t, err)
			require.Equal(t, result, retry)
			completed, err := svc.Complete(ctx, result.Slug)
			require.NoError(t, err)
			require.False(t, completed, "running processing must block deletion")
			deletionWorker := &worker.AccountDeletionWorker{Service: svc}
			require.NoError(t, deletionWorker.Register(river.NewWorkers()))
			job := &river.Job[jobs.AccountDeletionArgs]{Args: jobs.AccountDeletionArgs{Request: result.Slug}}
			var snooze *river.JobSnoozeError
			require.ErrorAs(t, deletionWorker.Work(ctx, job), &snooze)
			_, err = pool.Exec(ctx, `UPDATE river_job SET state='cancelled', finalized_at=NOW()-INTERVAL '30 days' WHERE args->>'organization_slug'=$1 AND kind='pipeline_git_refresh_proposal'`, org)
			require.NoError(t, err)
			pruned, err := pool.Exec(ctx, `DELETE FROM river_job WHERE args->>'organization_slug'=$1 AND kind='pipeline_git_refresh_proposal' AND state='cancelled'`, org)
			require.NoError(t, err)
			require.Zero(t, pruned.RowsAffected(), "queue retention must preserve unresolved processing for a pending deletion")
			require.ErrorAs(t, deletionWorker.Work(ctx, job), &snooze)
			_, err = pool.Exec(ctx, `UPDATE execution_requests SET state = 'succeeded' WHERE id = $1`, running)
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `UPDATE river_job SET state = 'completed', finalized_at = NOW() WHERE args->>'organization_slug' = $1 AND kind = 'pipeline_git_refresh_proposal'`, org)
			require.NoError(t, err)
			for _, state := range []string{"ambiguous", "cancelled"} {
				_, err = pool.Exec(ctx, `UPDATE execution_requests SET state=$2, started_at=NOW(), expires_at=NOW()-INTERVAL '1 day', result_ciphertext=NULL WHERE id=$1`, running, state)
				require.NoError(t, err)
				completed, err = svc.Complete(ctx, result.Slug)
				require.NoError(t, err)
				require.False(t, completed, "expiry is not evidence that processing stopped")
			}
			_, err = pool.Exec(ctx, `UPDATE execution_requests SET state='succeeded' WHERE id=$1`, running)
			require.NoError(t, err)
			require.NoError(t, deletionWorker.Work(ctx, job))
			completed, err = svc.Complete(ctx, result.Slug)
			require.NoError(t, err)
			require.True(t, completed, "completion is idempotent")
			var remaining int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = $1`, aliceID).Scan(&remaining))
			require.Zero(t, remaining)
			var detached bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT user_id IS NULL AND state='completed' AND completed_at IS NOT NULL AND failure_code IS NULL FROM account_deletion_requests WHERE slug=$1`, result.Slug).Scan(&detached))
			require.True(t, detached)
		})
	}
}
