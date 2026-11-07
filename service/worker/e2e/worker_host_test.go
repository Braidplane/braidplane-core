package e2e

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/config"
	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/fake"
	"github.com/Braidplane/braidplane-core/pkg/i18n"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repoevents "github.com/Braidplane/braidplane-core/postgres/events"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	emailsvc "github.com/Braidplane/braidplane-core/service/email"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelineschedulesvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	workerhostsvc "github.com/Braidplane/braidplane-core/service/worker"
	workerconsumer "github.com/Braidplane/braidplane-core/service/worker/consumer"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

const workerE2EPipelineContent = "metadata:\n  slug: worker-e2e-pipeline\n"

var workerE2EConfig struct {
	once sync.Once
	cfg  *backendconfig.Config
	err  error
}

func TestWorkerHostRiverSchemaMigrationVersionsPresent(t *testing.T) { // Real River/Postgres E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	pool := openWorkerE2EPool(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx, `SELECT line, version FROM river_migration ORDER BY version`)
	require.NoError(t, err)
	defer rows.Close()

	type migrationVersion struct {
		line    string
		version int64
	}

	var versions []migrationVersion
	for rows.Next() {
		var version migrationVersion
		require.NoError(t, rows.Scan(&version.line, &version.version))
		versions = append(versions, version)
	}
	require.NoError(t, rows.Err())

	require.Equal(t, []migrationVersion{
		{line: "main", version: 1},
		{line: "main", version: 2},
		{line: "main", version: 3},
		{line: "main", version: 4},
		{line: "main", version: 5},
		{line: "main", version: 6},
	}, versions)
}

func TestWorkerHostEmailProducerCommittedJobExecutes(t *testing.T) { // Real River/Postgres/SMTP E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	emailQueue := uniqueWorkerE2EQueue(jobs.EmailQueue)
	link := uniqueWorkerE2ELink("verify")
	token := "worker-e2e-email-verification-token"
	argsFilter := map[string]any{"link": link}
	cleanupRiverJobsByArgs(t, pool, emailQueue, jobs.EmailSendArgs{}.Kind(), argsFilter)
	emailSvc := newReachableRealEmailService(t)
	_, emailProducer, _ := newWorkerE2EHarness(t, pool, emailSvc, noopPipelineGitExecutor{}, workerE2EQueues{
		email: emailQueue,
	})

	user := workerE2EUser(t, pool, "email-commit")
	queued := enqueueEmailVerificationInTx(t, pool, emailProducer, user, link, token, false)

	require.Positive(t, queued.ID)
	require.Equal(t, queue.QueuedJob{
		ID:    queued.ID,
		Kind:  jobs.EmailSendArgs{}.Kind(),
		Queue: emailQueue,
		State: "queued",
	}, queued)

	job := waitForRiverJobState(t, ctx, pool, emailQueue, jobs.EmailSendArgs{}.Kind(), argsFilter, "completed")
	require.Equal(t, "completed", job.state)
	require.Equal(t, 1, job.attempt)
	require.Equal(t, "[]", job.errorsJSON)
}

func TestWorkerHostEmailPasswordResetProducerCommittedJobExecutes(t *testing.T) { // Real River/Postgres/SMTP E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	emailQueue := uniqueWorkerE2EQueue(jobs.EmailQueue)
	link := uniqueWorkerE2ELink("reset")
	token := "worker-e2e-password-reset-token"
	argsFilter := map[string]any{"link": link, "type": jobs.EmailTypePasswordReset.String()}
	cleanupRiverJobsByArgs(t, pool, emailQueue, jobs.EmailSendArgs{}.Kind(), argsFilter)
	emailSvc := newReachableRealEmailService(t)
	_, emailProducer, _ := newWorkerE2EHarness(t, pool, emailSvc, noopPipelineGitExecutor{}, workerE2EQueues{
		email: emailQueue,
	})

	user := workerE2EUser(t, pool, "email-password-reset")
	queued := enqueuePasswordResetInTx(t, pool, emailProducer, user, link, token)

	require.Positive(t, queued.ID)
	require.Equal(t, queue.QueuedJob{
		ID:    queued.ID,
		Kind:  jobs.EmailSendArgs{}.Kind(),
		Queue: emailQueue,
		State: "queued",
	}, queued)

	job := waitForRiverJobState(t, ctx, pool, emailQueue, jobs.EmailSendArgs{}.Kind(), argsFilter, "completed")
	require.Equal(t, "completed", job.state)
	require.Equal(t, 1, job.attempt)
	require.Equal(t, "[]", job.errorsJSON)
}

func TestWorkerHostUsesRegistrarQueuesWhenQueuesAreNotExplicitlyConfigured(t *testing.T) { // Real River/Postgres/SMTP E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	emailQueue := uniqueWorkerE2EQueue(jobs.EmailQueue)
	link := uniqueWorkerE2ELink("registrar-queues")
	token := "worker-e2e-registrar-verification-token"
	argsFilter := map[string]any{"link": link}
	cleanupRiverJobsByArgs(t, pool, emailQueue, jobs.EmailSendArgs{}.Kind(), argsFilter)
	emailSvc := newReachableRealEmailService(t)
	_, emailProducer := newWorkerE2EHarnessWithoutExplicitQueues(
		t,
		pool,
		emailSvc,
		noopPipelineGitExecutor{},
		workerE2EQueues{
			email: emailQueue,
		},
	)

	user := workerE2EUser(t, pool, "email-registrar-queues")
	queued := enqueueEmailVerificationInTx(t, pool, emailProducer, user, link, token, false)
	require.Equal(t, "queued", queued.State)

	job := waitForRiverJobState(t, ctx, pool, emailQueue, jobs.EmailSendArgs{}.Kind(), argsFilter, "completed")
	require.Equal(t, "completed", job.state)
}

func TestWorkerHostEmailProducerRollbackPreventsJobVisibility(t *testing.T) { // Real River/Postgres/SMTP E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	emailQueue := uniqueWorkerE2EQueue(jobs.EmailQueue)
	link := uniqueWorkerE2ELink("rollback")
	token := "worker-e2e-rollback-verification-token"
	argsFilter := map[string]any{"link": link}
	cleanupRiverJobsByArgs(t, pool, emailQueue, jobs.EmailSendArgs{}.Kind(), argsFilter)
	emailSvc := newRealEmailService(t)
	_, emailProducer, _ := newWorkerE2EHarness(t, pool, emailSvc, noopPipelineGitExecutor{}, workerE2EQueues{
		email: emailQueue,
	})

	user := workerE2EUser(t, pool, "email-rollback")
	_ = enqueueEmailVerificationInTx(t, pool, emailProducer, user, link, token, true)

	count, err := countRiverJobsByArgs(ctx, pool, emailQueue, jobs.EmailSendArgs{}.Kind(), argsFilter)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestWorkerHostEmailWorkerFailureLeavesJobRetryable(t *testing.T) { // Real River/Postgres/SMTP E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	emailQueue := uniqueWorkerE2EQueue(jobs.EmailQueue)
	link := uniqueWorkerE2ELink("failure")
	token := "worker-e2e-failure-verification-token"
	argsFilter := map[string]any{"link": link}
	cleanupRiverJobsByArgs(t, pool, emailQueue, jobs.EmailSendArgs{}.Kind(), argsFilter)
	emailSvc := newUnreachableRealEmailService(t)
	_, emailProducer, _ := newWorkerE2EHarness(t, pool, emailSvc, noopPipelineGitExecutor{}, workerE2EQueues{
		email: emailQueue,
	})

	user := workerE2EUser(t, pool, "email-failure")
	queued := enqueueEmailVerificationInTx(t, pool, emailProducer, user, link, token, false)
	require.Equal(t, "queued", queued.State)

	job := waitForRiverJobState(t, ctx, pool, emailQueue, jobs.EmailSendArgs{}.Kind(), argsFilter, "retryable")
	require.Equal(t, "retryable", job.state)
	require.Equal(t, 1, job.attempt)
	require.Contains(t, job.errorsJSON, "connect")
}

func TestWorkerHostPipelineGitProducerCommittedJobExecutes(t *testing.T) { // Real River/Postgres/SMTP E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	pipelineGitQueue := uniqueWorkerE2EQueue(jobs.PipelineGitQueue)
	path := fmt.Sprintf("pipelines/worker-fixture-%d.pipeline.yaml", time.Now().UnixNano())
	argsFilter := map[string]any{"path": path}
	cleanupRiverJobsByArgs(t, pool, pipelineGitQueue, jobs.PipelineGitWriteFileArgs{}.Kind(), argsFilter)
	gitAuth := newDockerGitAuthResolver(t)
	pipelineGit := newDockerPipelineGitExecutor(t, gitAuth)
	_, _, pipelineGitProducer := newWorkerE2EHarness(t, pool, newRealEmailService(t), pipelineGit, workerE2EQueues{
		pipelineGit: pipelineGitQueue,
	})

	branch := "main"
	queued := enqueuePipelineGitWriteFileInTx(t, pool, pipelineGitProducer, &branch, path)
	require.Positive(t, queued.ID)
	require.Equal(t, queue.QueuedJob{
		ID:    queued.ID,
		Kind:  jobs.PipelineGitWriteFileArgs{}.Kind(),
		Queue: pipelineGitQueue,
		State: "queued",
	}, queued)

	job := waitForRiverJobState(t, ctx, pool, pipelineGitQueue, jobs.PipelineGitWriteFileArgs{}.Kind(), argsFilter, "completed")
	require.Equal(t, "completed", job.state)
	require.Equal(t, 1, job.attempt)
	require.Equal(t, "[]", job.errorsJSON)

	content := readDockerGitFile(t, gitAuth, path)
	require.Equal(t, workerE2EPipelineContent, string(content))
}

func TestWorkerHostPipelineGitDeleteProducerCommittedJobExecutes(t *testing.T) { // Real River/Postgres/SMTP E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	pipelineGitQueue := uniqueWorkerE2EQueue(jobs.PipelineGitQueue)
	path := fmt.Sprintf("pipelines/worker-fixture-delete-%d.pipeline.yaml", time.Now().UnixNano())
	argsFilter := map[string]any{"path": path}
	cleanupRiverJobsByArgs(t, pool, pipelineGitQueue, jobs.PipelineGitDeleteFileArgs{}.Kind(), argsFilter)
	gitAuth := newDockerGitAuthResolver(t)
	pipelineGit := newDockerPipelineGitExecutor(t, gitAuth)
	_, _, pipelineGitProducer := newWorkerE2EHarness(t, pool, newRealEmailService(t), pipelineGit, workerE2EQueues{
		pipelineGit: pipelineGitQueue,
	})

	require.NoError(t, pipelineGit.WriteFile(ctx, jobs.PipelineGitWriteFileArgs{
		OrganizationSlug: slug.Slug("worker-e2e-org"),
		DataSourceSlug:   slug.Slug("worker-e2e-git"),
		PipelineSlug:     slug.Slug("worker-e2e-pipeline"),
		Path:             path,
		Content:          []byte(workerE2EPipelineContent),
		CommitMessage:    "seed worker e2e delete target",
	}))
	require.Equal(t, workerE2EPipelineContent, string(readDockerGitFile(t, gitAuth, path)))

	queued := enqueuePipelineGitDeleteFileInTx(t, pool, pipelineGitProducer, path)
	require.Positive(t, queued.ID)
	require.Equal(t, queue.QueuedJob{
		ID:    queued.ID,
		Kind:  jobs.PipelineGitDeleteFileArgs{}.Kind(),
		Queue: pipelineGitQueue,
		State: "queued",
	}, queued)

	job := waitForRiverJobState(t, ctx, pool, pipelineGitQueue, jobs.PipelineGitDeleteFileArgs{}.Kind(), argsFilter, "completed")
	require.Equal(t, "completed", job.state)
	require.Equal(t, 1, job.attempt)
	require.Equal(t, "[]", job.errorsJSON)

	_, err := readDockerGitFileResult(t, gitAuth, path)
	require.ErrorIs(t, err, repostore.ErrFileNotFound)
}

func TestWorkerHostPipelineGitPublishProposalPersistsMetadata(t *testing.T) { // Real River/Postgres E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)

	unique := time.Now().UnixNano()
	orgSlug := slug.Slug(fmt.Sprintf("worker-proposal-org-%d", unique))
	projectSlug := slug.Slug("default")
	pipelineSlug := slug.Slug("worker-proposal-pipeline")
	memberSlug := slug.Slug(fmt.Sprintf("worker-proposal-user-%d", unique))
	branchName := fmt.Sprintf("braidplane/default/%s/%s", pipelineSlug, memberSlug)
	publishAttemptID := fmt.Sprintf("attempt-%d", unique)
	pipelineGitQueue := fmt.Sprintf("pipeline_git_proposal_%d", unique)
	seedWorkerE2EProposalDraft(t, pool, orgSlug, projectSlug, pipelineSlug, memberSlug, branchName, publishAttemptID)

	argsFilter := map[string]any{"branch_name": branchName}
	cleanupRiverJobsByArgs(t, pool, pipelineGitQueue, jobs.PipelineGitPublishProposalArgs{}.Kind(), argsFilter)
	pipelineGit := &proposalE2EPipelineGitExecutor{
		result: &pipelinegit.ProposalResult{
			Provider:      "github",
			ProposalID:    "42",
			ProposalURL:   "https://github.com/acme/pipelines/pull/42",
			ProposalState: "open",
			BranchName:    branchName,
			BranchHeadSHA: "def456",
		},
	}
	_, _, pipelineGitProducer := newWorkerE2EHarnessWithProposalRecorder(
		t,
		pool,
		newRealEmailService(t),
		pipelineGit,
		workerE2EQueues{pipelineGit: pipelineGitQueue},
		store,
	)

	queued := enqueuePipelineGitPublishProposalInTx(t, pool, pipelineGitProducer, jobs.PipelineGitPublishProposalArgs{
		OrganizationSlug: orgSlug,
		ProjectSlug:      projectSlug,
		DataSourceSlug:   slug.Slug("worker-e2e-git"),
		PipelineSlug:     pipelineSlug,
		MemberSlug:       memberSlug,
		BaseRef:          ptrString("refs/heads/main"),
		BaseCommitSHA:    ptrString("abc123"),
		BranchName:       branchName,
		Path:             "pipelines/worker-proposal-pipeline.pipeline.yaml",
		Content:          []byte(workerE2EPipelineContent),
		CommitMessage:    "propose worker proposal pipeline",
		ProposalTitle:    "Propose worker proposal pipeline",
		ProposalBody:     "Pipeline draft proposed by Braidplane.",
		DraftSpecHash:    "draft-hash",
		PublishAttemptID: publishAttemptID,
	})
	require.Positive(t, queued.ID)
	require.Equal(t, queue.QueuedJob{
		ID:    queued.ID,
		Kind:  jobs.PipelineGitPublishProposalArgs{}.Kind(),
		Queue: pipelineGitQueue,
		State: "queued",
	}, queued)

	job := waitForRiverJobState(t, ctx, pool, pipelineGitQueue, jobs.PipelineGitPublishProposalArgs{}.Kind(), argsFilter, "completed")
	require.Equal(t, "completed", job.state)
	require.Equal(t, 1, job.attempt)
	require.Equal(t, "[]", job.errorsJSON)

	var draftState string
	var proposalProvider string
	var proposalID string
	var proposalURL string
	var proposalState string
	var branchHeadSHA string
	var lastProposedDraftHash string
	var canonicalSpecHash string
	var currentVersionID int64
	err = pool.QueryRow(ctx, `
		SELECT
			d.draft_state,
			d.proposal_provider,
			d.proposal_id,
			d.proposal_url,
			d.proposal_state,
			d.branch_head_sha,
			d.last_proposed_draft_hash,
			ps.spec_hash,
			ps.current_version_id
		FROM pipeline_drafts d
		JOIN projects p ON p.id = d.project_id
		JOIN organizations o ON o.id = p.organization_id
		JOIN organization_members om ON om.id = d.member_id
		JOIN users u ON u.id = om.user_id
		JOIN pipeline_specs ps ON ps.project_id = p.id AND ps.slug = d.pipeline_slug
		WHERE o.slug = $1 AND p.slug = $2 AND d.pipeline_slug = $3 AND u.slug = $4
	`, orgSlug.String(), projectSlug.String(), pipelineSlug.String(), memberSlug.String()).Scan(
		&draftState,
		&proposalProvider,
		&proposalID,
		&proposalURL,
		&proposalState,
		&branchHeadSHA,
		&lastProposedDraftHash,
		&canonicalSpecHash,
		&currentVersionID,
	)
	require.NoError(t, err)
	require.Equal(t, "proposal_open", draftState)
	require.Equal(t, "github", proposalProvider)
	require.Equal(t, "42", proposalID)
	require.Equal(t, "https://github.com/acme/pipelines/pull/42", proposalURL)
	require.Equal(t, "open", proposalState)
	require.Equal(t, "def456", branchHeadSHA)
	require.Equal(t, "draft-hash", lastProposedDraftHash)
	require.Equal(t, "canonical-hash", canonicalSpecHash)
	require.NotZero(t, currentVersionID)

	newerAttemptID := publishAttemptID + "-newer"
	_, err = pool.Exec(ctx, `
		UPDATE pipeline_drafts d
		SET publish_attempt_id = $5, proposal_id = '43', proposal_url = 'https://github.com/acme/pipelines/pull/43',
			branch_head_sha = 'newer-head', updated_at = NOW()
		FROM projects p, organizations o, organization_members om, users u
		WHERE d.project_id = p.id AND p.organization_id = o.id AND d.member_id = om.id AND om.user_id = u.id
			AND o.slug = $1 AND p.slug = $2 AND d.pipeline_slug = $3 AND u.slug = $4
	`, orgSlug.String(), projectSlug.String(), pipelineSlug.String(), memberSlug.String(), newerAttemptID)
	require.NoError(t, err)

	err = store.CompleteProposal(ctx, pipelinegit.ProposalCompletion{
		OrganizationSlug: orgSlug,
		ProjectSlug:      projectSlug,
		PipelineSlug:     pipelineSlug,
		MemberSlug:       memberSlug,
		Provider:         "github",
		ProposalID:       "42",
		ProposalURL:      "https://github.com/acme/pipelines/pull/42",
		ProposalState:    "open",
		BranchName:       branchName,
		BranchHeadSHA:    "stale-head",
		DraftSpecHash:    "stale-draft-hash",
		PublishAttemptID: publishAttemptID,
	})
	require.NoError(t, err)

	var persistedAttemptID string
	err = pool.QueryRow(ctx, `
		SELECT d.publish_attempt_id, d.proposal_id, d.branch_head_sha
		FROM pipeline_drafts d
		JOIN projects p ON p.id = d.project_id
		JOIN organizations o ON o.id = p.organization_id
		JOIN organization_members om ON om.id = d.member_id
		JOIN users u ON u.id = om.user_id
		WHERE o.slug = $1 AND p.slug = $2 AND d.pipeline_slug = $3 AND u.slug = $4
	`, orgSlug.String(), projectSlug.String(), pipelineSlug.String(), memberSlug.String()).Scan(
		&persistedAttemptID, &proposalID, &branchHeadSHA,
	)
	require.NoError(t, err)
	require.Equal(t, newerAttemptID, persistedAttemptID)
	require.Equal(t, "43", proposalID)
	require.Equal(t, "newer-head", branchHeadSHA)

	firstRefresh, err := store.PrepareGitProposalRefresh(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug)
	require.NoError(t, err)
	secondRefresh, err := store.PrepareGitProposalRefresh(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug)
	require.NoError(t, err)
	require.NotEqual(t, firstRefresh.RefreshAttemptID, secondRefresh.RefreshAttemptID)

	err = store.CompleteProposalRefresh(ctx, pipelinegit.ProposalRefreshCompletion{
		OrganizationSlug: orgSlug, ProjectSlug: projectSlug, PipelineSlug: pipelineSlug, MemberSlug: memberSlug,
		Provider: "github", ProposalID: "43", ProposalState: "merged", BranchName: branchName,
		BranchHeadSHA: "merged-head", RefreshAttemptID: secondRefresh.RefreshAttemptID,
	})
	require.NoError(t, err)
	err = store.CompleteProposalRefresh(ctx, pipelinegit.ProposalRefreshCompletion{
		OrganizationSlug: orgSlug, ProjectSlug: projectSlug, PipelineSlug: pipelineSlug, MemberSlug: memberSlug,
		Provider: "github", ProposalID: "43", ProposalState: "open", BranchName: branchName,
		BranchHeadSHA: "older-open-head", RefreshAttemptID: firstRefresh.RefreshAttemptID,
	})
	require.NoError(t, err)

	err = pool.QueryRow(ctx, `
		SELECT d.proposal_state, d.branch_head_sha
		FROM pipeline_drafts d
		JOIN projects p ON p.id = d.project_id
		JOIN organizations o ON o.id = p.organization_id
		JOIN organization_members om ON om.id = d.member_id
		JOIN users u ON u.id = om.user_id
		WHERE o.slug = $1 AND p.slug = $2 AND d.pipeline_slug = $3 AND u.slug = $4
	`, orgSlug.String(), projectSlug.String(), pipelineSlug.String(), memberSlug.String()).Scan(&proposalState, &branchHeadSHA)
	require.NoError(t, err)
	require.Equal(t, "merged", proposalState)
	require.Equal(t, "merged-head", branchHeadSHA)

	for _, preservedState := range []string{"proposal_closed", "proposal_open"} {
		_, err = pool.Exec(ctx, `
			UPDATE pipeline_drafts d SET draft_state = $5
			FROM projects p, organizations o, organization_members om, users u
			WHERE d.project_id = p.id AND p.organization_id = o.id AND d.member_id = om.id AND om.user_id = u.id
				AND o.slug = $1 AND p.slug = $2 AND d.pipeline_slug = $3 AND u.slug = $4
		`, orgSlug.String(), projectSlug.String(), pipelineSlug.String(), memberSlug.String(), preservedState)
		require.NoError(t, err)
		refresh, refreshErr := store.PrepareGitProposalRefresh(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug)
		require.NoError(t, refreshErr)
		require.NoError(t, store.FailProposalRefresh(ctx, pipelinegit.ProposalRefreshFailure{
			OrganizationSlug: orgSlug, ProjectSlug: projectSlug, PipelineSlug: pipelineSlug, MemberSlug: memberSlug,
			ProposalID: "43", RefreshAttemptID: refresh.RefreshAttemptID,
			FailureCode: "provider_unavailable", FailureMessage: "Git proposal provider operation failed; retry is available.",
		}))
		var actualState, failureCode string
		err = pool.QueryRow(ctx, `
			SELECT d.draft_state, d.failure_code
			FROM pipeline_drafts d
			JOIN projects p ON p.id = d.project_id
			JOIN organizations o ON o.id = p.organization_id
			JOIN organization_members om ON om.id = d.member_id
			JOIN users u ON u.id = om.user_id
			WHERE o.slug = $1 AND p.slug = $2 AND d.pipeline_slug = $3 AND u.slug = $4
		`, orgSlug.String(), projectSlug.String(), pipelineSlug.String(), memberSlug.String()).Scan(&actualState, &failureCode)
		require.NoError(t, err)
		require.Equal(t, preservedState, actualState)
		require.Equal(t, "provider_unavailable", failureCode)
	}
}

func TestWorkerHostPipelineScheduleDispatchJobRunsScheduler(t *testing.T) { // Real River/Postgres worker E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	scheduleQueue := uniqueWorkerE2EQueue(jobs.PipelineScheduleQueue)
	kind := jobs.PipelineScheduleDispatchArgs{}.Kind()
	cleanupRiverJobsByArgs(t, pool, scheduleQueue, kind, map[string]any{})
	eventsSvc := eventssvc.New(postgres.NewTransactionManager(pool), repoevents.New(), clock.New())
	dispatcher := &recordingWorkerE2EScheduleDispatcher{}
	workerSvc, err := workerhostsvc.New(
		noopPipelineGitExecutor{},
		noopPipelineGitProposalRecorder{},
		noopPipelineGitProposalAuthorizer{},
		newRealEmailService(t),
		eventsSvc,
		newWorkerE2ERetentionService(pool),
		dispatcher,
		noopPipelineRunExecutor{},
		noopSchemaImportExecutor{},
		noopNotificationDeliveryService{},
		workerE2ETokenCipher(t),
		slog.New(slog.DiscardHandler),
		workerE2EQueueConcurrency(),
		noopPipelineWebhookExecutor{},
	)

	require.NoError(t, err)

	client, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
		Logger: slog.New(slog.DiscardHandler),
		Queues: map[string]river.QueueConfig{
			scheduleQueue: {MaxWorkers: 1},
		},
		Registrars:  []queue.WorkerRegistrar{workerSvc},
		RetryPolicy: fixedRetryPolicy{delay: time.Hour},
	})
	require.NoError(t, err)
	require.NoError(t, client.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, client.Stop(stopCtx))
	})

	afterID := maxRiverJobIDForKind(t, ctx, pool, scheduleQueue, kind)
	queued := enqueuePipelineScheduleDispatchInTx(t, pool, queue.NewEnqueuer(client), scheduleQueue)
	require.Positive(t, queued.ID)
	require.Equal(t, queue.QueuedJob{
		ID:    queued.ID,
		Kind:  kind,
		Queue: scheduleQueue,
		State: "queued",
	}, queued)

	job := waitForRiverJobStateAfterID(t, ctx, pool, afterID, scheduleQueue, kind, "completed")
	require.Equal(t, "completed", job.state)
	require.Equal(t, 1, job.attempt)
	require.Equal(t, "[]", job.errorsJSON)

	calls, lastNow := dispatcher.Snapshot()
	require.Equal(t, 1, calls)
	require.False(t, lastNow.IsZero())
}

func TestWorkerHostPipelineScheduleDispatchJobHandsOffToFireExecutionJob(t *testing.T) { // Real River/Postgres worker E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	scheduleQueue := uniqueWorkerE2EQueue(jobs.PipelineScheduleQueue)
	dispatchKind := jobs.PipelineScheduleDispatchArgs{}.Kind()
	fireArgs := jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: slug.Slug("worker-e2e-org"),
		ProjectSlug:      slug.Slug("default"),
		EnvironmentSlug:  slug.Slug("prod"),
		TriggerSlug:      slug.Slug("daily-users"),
		TriggerRevision:  7,
		ScheduledFireAt:  time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC),
	}
	fireKind := fireArgs.Kind()
	fireArgsFilter := pipelineScheduleFireExecutionArgsFilter(fireArgs)
	cleanupRiverJobsByArgs(t, pool, scheduleQueue, dispatchKind, map[string]any{})
	cleanupRiverJobsByArgs(t, pool, scheduleQueue, fireKind, fireArgsFilter)
	eventsSvc := eventssvc.New(postgres.NewTransactionManager(pool), repoevents.New(), clock.New())
	scheduleSvc := &handoffWorkerE2EScheduleService{
		txm:       postgres.NewTransactionManager(pool),
		queueName: scheduleQueue,
		fireArgs:  fireArgs,
	}
	workerSvc, err := workerhostsvc.New(
		noopPipelineGitExecutor{},
		noopPipelineGitProposalRecorder{},
		noopPipelineGitProposalAuthorizer{},
		newRealEmailService(t),
		eventsSvc,
		newWorkerE2ERetentionService(pool),
		scheduleSvc,
		noopPipelineRunExecutor{},
		noopSchemaImportExecutor{},
		noopNotificationDeliveryService{},
		workerE2ETokenCipher(t),
		slog.New(slog.DiscardHandler),
		workerE2EQueueConcurrency(),
		noopPipelineWebhookExecutor{},
	)

	require.NoError(t, err)

	client, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
		Logger: slog.New(slog.DiscardHandler),
		Queues: map[string]river.QueueConfig{
			scheduleQueue: {MaxWorkers: 2},
		},
		Registrars:  []queue.WorkerRegistrar{workerSvc},
		RetryPolicy: fixedRetryPolicy{delay: time.Hour},
	})
	require.NoError(t, err)
	scheduleSvc.enqueuer = queue.NewEnqueuer(client)
	require.NoError(t, client.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, client.Stop(stopCtx))
	})

	queued := enqueuePipelineScheduleDispatchInTx(t, pool, queue.NewEnqueuer(client), scheduleQueue)
	require.Positive(t, queued.ID)
	require.Equal(t, queue.QueuedJob{
		ID:    queued.ID,
		Kind:  dispatchKind,
		Queue: scheduleQueue,
		State: "queued",
	}, queued)

	dispatchJob := waitForRiverJobState(t, ctx, pool, scheduleQueue, dispatchKind, map[string]any{}, "completed")
	require.Equal(t, "completed", dispatchJob.state)
	fireJob := waitForRiverJobState(t, ctx, pool, scheduleQueue, fireKind, fireArgsFilter, "completed")
	require.Equal(t, "completed", fireJob.state)

	dispatchCalls, executionCalls, lastArgs := scheduleSvc.Snapshot()
	require.Equal(t, 1, dispatchCalls)
	require.Equal(t, 1, executionCalls)
	require.Equal(t, fireArgs, lastArgs)
}

func TestWorkerHostPipelineRetryJobRunsRetryAttempt(t *testing.T) { // Real River/Postgres worker E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	retryQueue := uniqueWorkerE2EQueue(jobs.PipelineRetryQueue)
	retryArgs := jobs.PipelineTriggerRetryExecutionArgs{
		OrganizationSlug: slug.Slug("worker-e2e-org"),
		ProjectSlug:      slug.Slug("default"),
		EnvironmentSlug:  slug.Slug("prod"),
		RetryAttemptSlug: slug.Slug("retry-fire-daily-users-20260609t070000z-2"),
	}
	kind := retryArgs.Kind()
	argsFilter := pipelineTriggerRetryExecutionArgsFilter(retryArgs)
	cleanupRiverJobsByArgs(t, pool, retryQueue, kind, argsFilter)
	eventsSvc := eventssvc.New(postgres.NewTransactionManager(pool), repoevents.New(), clock.New())
	dispatcher := &recordingWorkerE2ERetryDispatcher{}
	workerSvc, err := workerhostsvc.New(
		noopPipelineGitExecutor{},
		noopPipelineGitProposalRecorder{},
		noopPipelineGitProposalAuthorizer{},
		newRealEmailService(t),
		eventsSvc,
		newWorkerE2ERetentionService(pool),
		dispatcher,
		noopPipelineRunExecutor{},
		noopSchemaImportExecutor{},
		noopNotificationDeliveryService{},
		workerE2ETokenCipher(t),
		slog.New(slog.DiscardHandler),
		workerE2EQueueConcurrency(),
		noopPipelineWebhookExecutor{},
	)

	require.NoError(t, err)

	client, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
		Logger: slog.New(slog.DiscardHandler),
		Queues: map[string]river.QueueConfig{
			retryQueue: {MaxWorkers: 1},
		},
		Registrars:  []queue.WorkerRegistrar{workerSvc},
		RetryPolicy: fixedRetryPolicy{delay: time.Hour},
	})
	require.NoError(t, err)
	require.NoError(t, client.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, client.Stop(stopCtx))
	})
	producer, err := workerproducer.NewPipelineRetryProducerForQueue(queue.NewEnqueuer(client), retryQueue)
	require.NoError(t, err)

	queued := enqueuePipelineTriggerRetryExecutionInTx(t, pool, producer, retryArgs)
	require.Positive(t, queued.ID)
	require.Equal(t, queue.QueuedJob{
		ID:    queued.ID,
		Kind:  kind,
		Queue: retryQueue,
		State: "queued",
	}, queued)

	job := waitForRiverJobState(t, ctx, pool, retryQueue, kind, argsFilter, "completed")
	require.Equal(t, "completed", job.state)
	require.Equal(t, 1, job.attempt)
	require.Equal(t, "[]", job.errorsJSON)

	calls, lastArgs := dispatcher.Snapshot()
	require.Equal(t, 1, calls)
	require.Equal(t, retryArgs, lastArgs)
}

func newWorkerE2EHarness(
	t *testing.T,
	pool *pgxpool.Pool,
	emailSvc emailsvc.Service,
	pipelineGit workerconsumer.PipelineGitExecutor,
	queues workerE2EQueues,
) (*queue.Client, workerproducer.EmailProducer, workerproducer.PipelineGitProducer) {
	t.Helper()

	return newWorkerE2EHarnessWithProposalRecorder(
		t,
		pool,
		emailSvc,
		pipelineGit,
		queues,
		noopPipelineGitProposalRecorder{},
	)
}

func newWorkerE2EHarnessWithProposalRecorder(
	t *testing.T,
	pool *pgxpool.Pool,
	emailSvc emailsvc.Service,
	pipelineGit workerconsumer.PipelineGitExecutor,
	queues workerE2EQueues,
	recorder workerconsumer.PipelineGitProposalRecorder,
) (*queue.Client, workerproducer.EmailProducer, workerproducer.PipelineGitProducer) {
	t.Helper()

	queues = queues.withDefaults()
	tokenCipher := workerE2ETokenCipher(t)
	eventsSvc := eventssvc.New(postgres.NewTransactionManager(pool), repoevents.New(), clock.New())
	workerSvc, err := workerhostsvc.New(
		pipelineGit,
		recorder,
		noopPipelineGitProposalAuthorizer{},
		emailSvc,
		eventsSvc,
		newWorkerE2ERetentionService(pool),
		noopScheduleDispatcher{},
		noopPipelineRunExecutor{},
		noopSchemaImportExecutor{},
		noopNotificationDeliveryService{},
		tokenCipher,
		slog.New(slog.DiscardHandler),
		workerE2EQueueConcurrency(),
		noopPipelineWebhookExecutor{},
	)

	require.NoError(t, err)

	client, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
		Logger: slog.New(slog.DiscardHandler),
		Queues: queues.config(),
		Registrars: []queue.WorkerRegistrar{
			workerSvc,
		},
		RetryPolicy: fixedRetryPolicy{delay: time.Hour},
	})
	require.NoError(t, err)
	require.NoError(t, client.Start(context.Background()))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, client.Stop(stopCtx))
	})

	enqueuer := queue.NewEnqueuer(client)
	emailProducer, err := workerproducer.NewEmailProducerForQueue(enqueuer, tokenCipher, queues.email)
	require.NoError(t, err)
	pipelineGitProducer, err := workerproducer.NewPipelineGitProducerForQueue(enqueuer, queues.pipelineGit)
	require.NoError(t, err)

	return client, emailProducer, pipelineGitProducer
}

func newWorkerE2EHarnessWithoutExplicitQueues(
	t *testing.T,
	pool *pgxpool.Pool,
	emailSvc emailsvc.Service,
	pipelineGit workerconsumer.PipelineGitExecutor,
	queues workerE2EQueues,
) (*queue.Client, workerproducer.EmailProducer) {
	t.Helper()

	queues = queues.withDefaults()
	tokenCipher := workerE2ETokenCipher(t)
	eventsSvc := eventssvc.New(postgres.NewTransactionManager(pool), repoevents.New(), clock.New())
	workerSvc, err := workerhostsvc.New(
		pipelineGit,
		noopPipelineGitProposalRecorder{},
		noopPipelineGitProposalAuthorizer{},
		emailSvc,
		eventsSvc,
		newWorkerE2ERetentionService(pool),
		noopScheduleDispatcher{},
		noopPipelineRunExecutor{},
		noopSchemaImportExecutor{},
		noopNotificationDeliveryService{},
		tokenCipher,
		slog.New(slog.DiscardHandler),
		workerE2EQueueConcurrency(),
		noopPipelineWebhookExecutor{},
	)

	require.NoError(t, err)

	client, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
		Logger: slog.New(slog.DiscardHandler),
		Registrars: []queue.WorkerRegistrar{
			workerE2EQueueRegistrar{
				WorkerRegistrar: workerSvc,
				queues:          queues.config(),
			},
		},
		RetryPolicy: fixedRetryPolicy{delay: time.Hour},
	})
	require.NoError(t, err)
	require.NoError(t, client.Start(context.Background()))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, client.Stop(stopCtx))
	})

	emailProducer, err := workerproducer.NewEmailProducerForQueue(queue.NewEnqueuer(client), tokenCipher, queues.email)
	require.NoError(t, err)

	return client, emailProducer
}

type workerE2EQueues struct {
	email       string
	pipelineGit string
}

func (q workerE2EQueues) withDefaults() workerE2EQueues {
	if q.email == "" {
		q.email = jobs.EmailQueue
	}
	if q.pipelineGit == "" {
		q.pipelineGit = jobs.PipelineGitQueue
	}
	return q
}

func (q workerE2EQueues) config() map[string]river.QueueConfig {
	return map[string]river.QueueConfig{
		q.email:       {MaxWorkers: 1},
		q.pipelineGit: {MaxWorkers: 1},
	}
}

type workerE2EQueueRegistrar struct {
	queue.WorkerRegistrar
	queues map[string]river.QueueConfig
}

func (r workerE2EQueueRegistrar) Queues() map[string]river.QueueConfig {
	return r.queues
}

func enqueueEmailVerificationInTx(
	t *testing.T,
	pool *pgxpool.Pool,
	producer workerproducer.EmailProducer,
	user *model.User,
	link string,
	token string,
	rollback bool,
) queue.QueuedJob {
	t.Helper()

	var queued queue.QueuedJob
	err := postgres.NewTransactionManager(pool).RunInTx(t.Context(), func(ctx context.Context, tx postgres.DBTX) error {
		var enqueueErr error
		queued, enqueueErr = producer.EnqueueEmailVerification(ctx, tx, user, link, token)
		if enqueueErr != nil {
			return enqueueErr
		}
		if rollback {
			return xerror.Unexpected().Detail("rollback requested")
		}
		return nil
	})
	if rollback {
		require.ErrorContains(t, err, "rollback requested")
		return queued
	}
	require.NoError(t, err)

	return queued
}

func enqueuePasswordResetInTx(
	t *testing.T,
	pool *pgxpool.Pool,
	producer workerproducer.EmailProducer,
	user *model.User,
	resetURL string,
	token string,
) queue.QueuedJob {
	t.Helper()

	var queued queue.QueuedJob
	err := postgres.NewTransactionManager(pool).RunInTx(t.Context(), func(ctx context.Context, tx postgres.DBTX) error {
		var enqueueErr error
		queued, enqueueErr = producer.EnqueuePasswordReset(ctx, tx, user, resetURL, token)
		return enqueueErr
	})
	require.NoError(t, err)

	return queued
}

func workerE2ETokenCipher(t *testing.T) crypto.AES256 {
	t.Helper()

	keyBytes, err := hex.DecodeString(loadWorkerE2EBackendConfig(t).Security.EncryptionKeyHex)
	require.NoError(t, err)
	cipher, err := crypto.NewAES256(keyBytes)
	require.NoError(t, err)
	return cipher
}

func enqueuePipelineGitWriteFileInTx(
	t *testing.T,
	pool *pgxpool.Pool,
	producer workerproducer.PipelineGitProducer,
	branch *string,
	path string,
) queue.QueuedJob {
	t.Helper()

	var queued queue.QueuedJob
	err := postgres.NewTransactionManager(pool).RunInTx(t.Context(), func(ctx context.Context, tx postgres.DBTX) error {
		var enqueueErr error
		queued, enqueueErr = producer.EnqueueWriteFile(
			ctx,
			tx,
			slug.Slug("worker-e2e-org"),
			slug.Slug("worker-e2e-git"),
			slug.Slug("worker-e2e-pipeline"),
			branch,
			path,
			[]byte(workerE2EPipelineContent),
			"export worker e2e pipeline",
			false,
		)
		return enqueueErr
	})
	require.NoError(t, err)

	return queued
}

func enqueuePipelineScheduleDispatchInTx(
	t *testing.T,
	pool *pgxpool.Pool,
	enqueuer queue.Enqueuer,
	queueName string,
) queue.QueuedJob {
	t.Helper()

	var queued queue.QueuedJob
	err := postgres.NewTransactionManager(pool).RunInTx(t.Context(), func(ctx context.Context, tx postgres.DBTX) error {
		var enqueueErr error
		queued, enqueueErr = enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
			Args:  jobs.PipelineScheduleDispatchArgs{},
			Queue: queueName,
		})
		return enqueueErr
	})
	require.NoError(t, err)

	return queued
}

func pipelineScheduleFireExecutionArgsFilter(args jobs.PipelineScheduleFireExecutionArgs) map[string]any {
	return map[string]any{
		"organization_slug": args.OrganizationSlug.String(),
		"project_slug":      args.ProjectSlug.String(),
		"environment_slug":  args.EnvironmentSlug.String(),
		"trigger_slug":      args.TriggerSlug.String(),
		"trigger_revision":  args.TriggerRevision,
		"scheduled_fire_at": args.ScheduledFireAt.Format(time.RFC3339Nano),
	}
}

func enqueuePipelineTriggerRetryExecutionInTx(
	t *testing.T,
	pool *pgxpool.Pool,
	producer workerproducer.PipelineRetryProducer,
	args jobs.PipelineTriggerRetryExecutionArgs,
) queue.QueuedJob {
	t.Helper()

	var queued queue.QueuedJob
	err := postgres.NewTransactionManager(pool).RunInTx(t.Context(), func(ctx context.Context, tx postgres.DBTX) error {
		var enqueueErr error
		queued, enqueueErr = producer.EnqueueTriggerRetryExecution(ctx, tx, args)
		return enqueueErr
	})
	require.NoError(t, err)

	return queued
}

func pipelineTriggerRetryExecutionArgsFilter(args jobs.PipelineTriggerRetryExecutionArgs) map[string]any {
	return map[string]any{
		"organization_slug":  args.OrganizationSlug.String(),
		"project_slug":       args.ProjectSlug.String(),
		"environment_slug":   args.EnvironmentSlug.String(),
		"retry_attempt_slug": args.RetryAttemptSlug.String(),
	}
}

func enqueuePipelineGitDeleteFileInTx(
	t *testing.T,
	pool *pgxpool.Pool,
	producer workerproducer.PipelineGitProducer,
	path string,
) queue.QueuedJob {
	t.Helper()

	var queued queue.QueuedJob
	err := postgres.NewTransactionManager(pool).RunInTx(t.Context(), func(ctx context.Context, tx postgres.DBTX) error {
		var enqueueErr error
		queued, enqueueErr = producer.EnqueueDeleteFile(
			ctx,
			tx,
			slug.Slug("worker-e2e-org"),
			slug.Slug("worker-e2e-git"),
			slug.Slug("worker-e2e-pipeline"),
			nil,
			path,
			"remove worker e2e pipeline",
		)
		return enqueueErr
	})
	require.NoError(t, err)

	return queued
}

func enqueuePipelineGitPublishProposalInTx(
	t *testing.T,
	pool *pgxpool.Pool,
	producer workerproducer.PipelineGitProducer,
	args jobs.PipelineGitPublishProposalArgs,
) queue.QueuedJob {
	t.Helper()

	var queued queue.QueuedJob
	err := postgres.NewTransactionManager(pool).RunInTx(t.Context(), func(ctx context.Context, tx postgres.DBTX) error {
		var enqueueErr error
		queued, enqueueErr = producer.EnqueuePublishProposal(ctx, tx, args)
		return enqueueErr
	})
	require.NoError(t, err)

	return queued
}

func seedWorkerE2EProposalDraft(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	pipelineSlug slug.Slug,
	memberSlug slug.Slug,
	branchName string,
	publishAttemptID string,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, `INSERT INTO organizations (name, slug) VALUES ($1, $2)`, "Worker Proposal E2E Org", orgSlug.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO projects (organization_id, name, slug)
		SELECT id, 'Default project', $2
		FROM organizations
		WHERE slug = $1
	`, orgSlug.String(), projectSlug.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		WITH org_row AS (
			SELECT id FROM organizations WHERE slug = $1
		),
		user_row AS (
			INSERT INTO users (slug, email, full_name, role, locale)
			VALUES ($2, $3, 'Worker Proposal User', 'user', 'en')
			RETURNING id
		),
		role_row AS (
			INSERT INTO roles (organization_id, slug, name, is_default)
			SELECT org_row.id, 'owner', 'Owner', true FROM org_row
			RETURNING id
		)
		INSERT INTO organization_members (organization_id, user_id, role_id)
		SELECT org_row.id, user_row.id, role_row.id
		FROM org_row, user_row, role_row
	`, orgSlug.String(), memberSlug.String(), fmt.Sprintf("%s@example.com", memberSlug))
	require.NoError(t, err)

	canonicalSpec := fmt.Sprintf(`{"metadata":{"slug":"%s","name":"Canonical Pipeline"},"steps":[]}`, pipelineSlug)
	draftSpec := fmt.Sprintf(`{"metadata":{"slug":"%s","name":"Draft Pipeline"},"steps":[]}`, pipelineSlug)
	_, err = pool.Exec(ctx, `
		INSERT INTO pipeline_specs (
			project_id,
			slug,
			name,
			spec_jsonb,
			spec_hash,
				sync_status,
			source_path,
			source_ref,
			last_applied_commit
		)
			SELECT p.id, $3, 'Canonical Pipeline', $4::jsonb, 'canonical-hash', 'synced',
			'pipelines/worker-proposal-pipeline.pipeline.yaml', 'refs/heads/main', 'abc123'
		FROM organizations o
		JOIN projects p ON p.organization_id = o.id
		WHERE o.slug = $1 AND p.slug = $2
	`, orgSlug.String(), projectSlug.String(), pipelineSlug.String(), canonicalSpec)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO pipeline_versions (project_id, pipeline_slug, git_commit_sha, spec_jsonb, spec_hash)
		SELECT p.id, $3, 'abc123', $4::jsonb, 'canonical-hash'
		FROM organizations o
		JOIN projects p ON p.organization_id = o.id
		WHERE o.slug = $1 AND p.slug = $2
	`, orgSlug.String(), projectSlug.String(), pipelineSlug.String(), canonicalSpec)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		UPDATE pipeline_specs ps
		SET current_version_id = pv.id
		FROM organizations o
		JOIN projects p ON p.organization_id = o.id
		JOIN pipeline_versions pv ON pv.project_id = p.id AND pv.pipeline_slug = $3
		WHERE ps.project_id = p.id AND ps.slug = $3 AND o.slug = $1 AND p.slug = $2
	`, orgSlug.String(), projectSlug.String(), pipelineSlug.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO pipeline_references (project_id, slug, name, current_version_id, has_draft)
		SELECT p.id, ps.slug, ps.name, ps.current_version_id, true
		FROM organizations o
		JOIN projects p ON p.organization_id = o.id
		JOIN pipeline_specs ps ON ps.project_id = p.id AND ps.slug = $3
		WHERE o.slug = $1 AND p.slug = $2
	`, orgSlug.String(), projectSlug.String(), pipelineSlug.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO pipeline_drafts (
			organization_id,
			project_id,
			pipeline_slug,
			member_id,
			spec_jsonb,
			draft_state,
			base_ref,
			base_commit_sha,
			branch_name,
			publish_attempt_id,
			queued_at
		)
		SELECT o.id, p.id, $3, om.id, $6::jsonb, 'publish_queued', 'refs/heads/main', 'abc123', $5, $7, NOW()
		FROM organizations o
		JOIN projects p ON p.organization_id = o.id
		JOIN organization_members om ON om.organization_id = o.id
		JOIN users u ON u.id = om.user_id
		WHERE o.slug = $1 AND p.slug = $2 AND u.slug = $4
	`, orgSlug.String(), projectSlug.String(), pipelineSlug.String(), memberSlug.String(), branchName, draftSpec, publishAttemptID)
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()

		_, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE slug = $1`, orgSlug.String())
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM users WHERE slug = $1`, memberSlug.String())
		require.NoError(t, cleanupErr)
	})
}

func openWorkerE2EPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connString := "postgres://postgres:postgres@localhost:35432/braidplane?sslmode=disable"
	if envConnString := os.Getenv("DATABASE_URL"); envConnString != "" {
		connString = envConnString
	}

	pool, err := pgxpool.New(ctx, connString)
	require.NoError(t, err, "real-Postgres River worker E2E requires DATABASE_URL to be reachable")
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		require.NoError(t, err, "real-Postgres River worker E2E requires DATABASE_URL to be reachable")
	}
	if !hasRiverWorkerE2ESchema(ctx, t, pool) {
		pool.Close()
		require.Fail(t, "real-Postgres River worker E2E requires River schema to be present in DATABASE_URL")
	}

	t.Cleanup(pool.Close)
	return pool
}

func newReachableRealEmailService(t *testing.T) emailsvc.Service {
	t.Helper()

	cfg := loadWorkerE2EBackendConfig(t)
	smtpCfg := cfg.SMTP
	dialer := net.Dialer{Timeout: 1500 * time.Millisecond}
	conn, err := dialer.DialContext(t.Context(), "tcp", smtpCfg.Host)
	require.NoErrorf(t, err, "real-Postgres River worker E2E requires SMTP server to be reachable at %s", smtpCfg.Host)
	require.NoError(t, conn.Close())

	return newEmailServiceFromSMTP(t, smtpCfg)
}

func newRealEmailService(t *testing.T) emailsvc.Service {
	t.Helper()

	cfg := loadWorkerE2EBackendConfig(t)
	return newEmailServiceFromSMTP(t, cfg.SMTP)
}

func newUnreachableRealEmailService(t *testing.T) emailsvc.Service {
	t.Helper()

	cfg := loadWorkerE2EBackendConfig(t)
	smtpCfg := cfg.SMTP
	smtpCfg.Host = "127.0.0.1:1"
	smtpCfg.User = ""
	smtpCfg.Password = ""
	smtpCfg.CACert = ""
	smtpCfg.CACertFile = ""

	return newEmailServiceFromSMTP(t, smtpCfg)
}

func loadWorkerE2EBackendConfig(t *testing.T) *backendconfig.Config {
	t.Helper()

	workerE2EConfig.once.Do(func() {
		if os.Getenv("ENCRYPTION_KEY_HEX") == "" {
			t.Setenv("ENCRYPTION_KEY_HEX", "0202020202020202020202020202020202020202020202020202020202020202")
		}
		workerE2EConfig.cfg, workerE2EConfig.err = backendconfig.Load("../../../config.toml")
	})
	require.NoError(t, workerE2EConfig.err)

	return workerE2EConfig.cfg
}

func newEmailServiceFromSMTP(t *testing.T, smtpCfg config.SMTP) emailsvc.Service {
	t.Helper()

	bundle, err := i18n.NewBundle("en")
	require.NoError(t, err)
	emailSvc, err := emailsvc.NewService(smtpCfg, bundle)
	require.NoError(t, err)

	return emailSvc
}

func hasRiverWorkerE2ESchema(ctx context.Context, t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()

	var present bool
	err := pool.QueryRow(
		ctx,
		`SELECT
			to_regclass('public.river_migration') IS NOT NULL
			AND to_regclass('public.river_queue') IS NOT NULL
			AND to_regclass('public.river_leader') IS NOT NULL
			AND to_regclass('public.river_job') IS NOT NULL`,
	).Scan(&present)
	require.NoError(t, err)

	return present
}

func cleanupRiverJobsByArgs(
	t *testing.T,
	pool *pgxpool.Pool,
	queueName string,
	kind string,
	argsFilter map[string]any,
) {
	t.Helper()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		payload := mustMarshalJSON(t, argsFilter)
		_, err := pool.Exec(
			ctx,
			`DELETE FROM river_job WHERE queue = $1 AND kind = $2 AND args @> $3::jsonb`,
			queueName,
			kind,
			payload,
		)
		require.NoError(t, err)
	})
}

type riverJobSnapshot struct {
	state      string
	attempt    int
	errorsJSON string
}

func waitForRiverJobState(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	queueName string,
	kind string,
	argsFilter map[string]any,
	wantState string,
) riverJobSnapshot {
	t.Helper()

	waitCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var last riverJobSnapshot
	for {
		job, found, err := findRiverJobByArgs(waitCtx, pool, queueName, kind, argsFilter)
		if waitCtx.Err() != nil {
			failRiverJobStateTimeout(t, queueName, kind, wantState, last)
		}
		require.NoError(t, err)
		if found {
			last = job
			if job.state == wantState {
				return job
			}
		}

		select {
		case <-waitCtx.Done():
			failRiverJobStateTimeout(t, queueName, kind, wantState, last)
		case <-ticker.C:
		}
	}
}

func failRiverJobStateTimeout(
	t *testing.T,
	queueName string,
	kind string,
	wantState string,
	last riverJobSnapshot,
) {
	t.Helper()

	t.Fatalf(
		"timed out waiting for River job %s/%s to reach %q\nlast state=%q\nlast attempt=%d\nlast errors=%s",
		queueName,
		kind,
		wantState,
		last.state,
		last.attempt,
		strings.ReplaceAll(last.errorsJSON, `, "`, ",\n\""),
	)
}

func findRiverJobByArgs(
	ctx context.Context,
	pool *pgxpool.Pool,
	queueName string,
	kind string,
	argsFilter map[string]any,
) (riverJobSnapshot, bool, error) {
	payload, err := json.Marshal(argsFilter)
	if err != nil {
		return riverJobSnapshot{}, false, err
	}

	var job riverJobSnapshot
	err = pool.QueryRow(
		ctx,
		`SELECT state::text, attempt::int, COALESCE(array_to_json(errors)::text, '[]')
		 FROM river_job
		 WHERE queue = $1 AND kind = $2 AND args @> $3::jsonb
		 ORDER BY id DESC
		 LIMIT 1`,
		queueName,
		kind,
		payload,
	).Scan(&job.state, &job.attempt, &job.errorsJSON)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return riverJobSnapshot{}, false, nil
		}
		return riverJobSnapshot{}, false, err
	}

	return job, true, nil
}

func countRiverJobsByArgs(
	ctx context.Context,
	pool *pgxpool.Pool,
	queueName string,
	kind string,
	argsFilter map[string]any,
) (int, error) {
	payload, err := json.Marshal(argsFilter)
	if err != nil {
		return 0, err
	}

	var count int
	err = pool.QueryRow(
		ctx,
		`SELECT count(*) FROM river_job WHERE queue = $1 AND kind = $2 AND args @> $3::jsonb`,
		queueName,
		kind,
		payload,
	).Scan(&count)

	return count, err
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()

	payload, err := json.Marshal(v)
	require.NoError(t, err)

	return payload
}

type fixedRetryPolicy struct {
	delay time.Duration
}

func (p fixedRetryPolicy) NextRetry(*rivertype.JobRow) time.Time {
	return time.Now().Add(p.delay)
}

type noopScheduleDispatcher struct{}

func (noopScheduleDispatcher) DispatchDueSchedules(
	context.Context,
	time.Time,
) (pipelineschedulesvc.DispatchResult, error) {
	return pipelineschedulesvc.DispatchResult{}, nil
}

func (noopScheduleDispatcher) ExecuteClaimedScheduleFire(
	context.Context,
	jobs.PipelineScheduleFireExecutionArgs,
) error {
	return nil
}

func (noopScheduleDispatcher) ExecuteTriggerRetryAttempt(
	context.Context,
	jobs.PipelineTriggerRetryExecutionArgs,
) error {
	return nil
}

type noopPipelineRunExecutor struct{}

func (noopPipelineRunExecutor) ExecuteQueuedPipelineRun(
	context.Context,
	jobs.PipelineRunExecutionArgs,
) error {
	return nil
}

type noopPipelineWebhookExecutor struct{}

func (noopPipelineWebhookExecutor) ExecuteDelivery(
	context.Context,
	jobs.PipelineWebhookDeliveryArgs,
) error {
	return nil
}

type noopSchemaImportExecutor struct{}

func (noopSchemaImportExecutor) ExecuteQueuedSchemaImport(
	context.Context,
	jobs.DataSourceSchemaImportArgs,
) (*datasourcessvc.SchemaImportResult, error) {
	return &datasourcessvc.SchemaImportResult{Skipped: true}, nil
}

type recordingWorkerE2EScheduleDispatcher struct {
	mu      sync.Mutex
	calls   int
	lastNow time.Time
}

func (d *recordingWorkerE2EScheduleDispatcher) DispatchDueSchedules(
	_ context.Context,
	now time.Time,
) (pipelineschedulesvc.DispatchResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.calls++
	d.lastNow = now
	return pipelineschedulesvc.DispatchResult{Scanned: 1}, nil
}

func (d *recordingWorkerE2EScheduleDispatcher) ExecuteClaimedScheduleFire(
	context.Context,
	jobs.PipelineScheduleFireExecutionArgs,
) error {
	return nil
}

func (d *recordingWorkerE2EScheduleDispatcher) ExecuteTriggerRetryAttempt(
	context.Context,
	jobs.PipelineTriggerRetryExecutionArgs,
) error {
	return nil
}

func (d *recordingWorkerE2EScheduleDispatcher) Snapshot() (int, time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.calls, d.lastNow
}

type recordingWorkerE2ERetryDispatcher struct {
	mu       sync.Mutex
	calls    int
	lastArgs jobs.PipelineTriggerRetryExecutionArgs
}

func (d *recordingWorkerE2ERetryDispatcher) DispatchDueSchedules(
	context.Context,
	time.Time,
) (pipelineschedulesvc.DispatchResult, error) {
	return pipelineschedulesvc.DispatchResult{}, nil
}

func (d *recordingWorkerE2ERetryDispatcher) ExecuteClaimedScheduleFire(
	context.Context,
	jobs.PipelineScheduleFireExecutionArgs,
) error {
	return nil
}

func (d *recordingWorkerE2ERetryDispatcher) ExecuteTriggerRetryAttempt(
	_ context.Context,
	args jobs.PipelineTriggerRetryExecutionArgs,
) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.calls++
	d.lastArgs = args
	return nil
}

func (d *recordingWorkerE2ERetryDispatcher) Snapshot() (int, jobs.PipelineTriggerRetryExecutionArgs) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.calls, d.lastArgs
}

type handoffWorkerE2EScheduleService struct {
	mu        sync.Mutex
	txm       postgres.TransactionManager
	enqueuer  queue.Enqueuer
	queueName string
	fireArgs  jobs.PipelineScheduleFireExecutionArgs

	dispatchCalls  int
	executionCalls int
	lastArgs       jobs.PipelineScheduleFireExecutionArgs
}

func (s *handoffWorkerE2EScheduleService) DispatchDueSchedules(
	ctx context.Context,
	_ time.Time,
) (pipelineschedulesvc.DispatchResult, error) {
	s.mu.Lock()
	s.dispatchCalls++
	s.mu.Unlock()

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		_, enqueueErr := s.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
			Args:  s.fireArgs,
			Queue: s.queueName,
		})
		return enqueueErr
	})
	if err != nil {
		return pipelineschedulesvc.DispatchResult{Scanned: 1, Claimed: 1}, err
	}
	return pipelineschedulesvc.DispatchResult{Scanned: 1, Claimed: 1, Enqueued: 1}, nil
}

func (s *handoffWorkerE2EScheduleService) ExecuteClaimedScheduleFire(
	_ context.Context,
	args jobs.PipelineScheduleFireExecutionArgs,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.executionCalls++
	s.lastArgs = args
	return nil
}

func (s *handoffWorkerE2EScheduleService) ExecuteTriggerRetryAttempt(
	context.Context,
	jobs.PipelineTriggerRetryExecutionArgs,
) error {
	return nil
}

func (s *handoffWorkerE2EScheduleService) Snapshot() (int, int, jobs.PipelineScheduleFireExecutionArgs) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dispatchCalls, s.executionCalls, s.lastArgs
}

type noopPipelineGitExecutor struct{}

func (noopPipelineGitExecutor) WriteFile(
	context.Context,
	jobs.PipelineGitWriteFileArgs,
) error {
	return nil
}

func (noopPipelineGitExecutor) DeleteFile(
	context.Context,
	jobs.PipelineGitDeleteFileArgs,
) error {
	return nil
}

func (noopPipelineGitExecutor) PublishProposal(
	context.Context,
	jobs.PipelineGitPublishProposalArgs,
) (*pipelinegit.ProposalResult, error) {
	return &pipelinegit.ProposalResult{}, nil
}

func (noopPipelineGitExecutor) RefreshProposal(
	context.Context, jobs.PipelineGitRefreshProposalArgs,
) (*pipelinegit.ProposalResult, error) {
	return &pipelinegit.ProposalResult{}, nil
}

type proposalE2EPipelineGitExecutor struct {
	result *pipelinegit.ProposalResult
	err    error
}

func (e *proposalE2EPipelineGitExecutor) WriteFile(
	context.Context,
	jobs.PipelineGitWriteFileArgs,
) error {
	return nil
}

func (e *proposalE2EPipelineGitExecutor) DeleteFile(
	context.Context,
	jobs.PipelineGitDeleteFileArgs,
) error {
	return nil
}

func (e *proposalE2EPipelineGitExecutor) PublishProposal(
	context.Context,
	jobs.PipelineGitPublishProposalArgs,
) (*pipelinegit.ProposalResult, error) {
	return e.result, e.err
}

func (e *proposalE2EPipelineGitExecutor) RefreshProposal(
	context.Context, jobs.PipelineGitRefreshProposalArgs,
) (*pipelinegit.ProposalResult, error) {
	return e.result, e.err
}

type noopPipelineGitProposalRecorder struct{}

type noopPipelineGitProposalAuthorizer struct{}

func (noopPipelineGitProposalAuthorizer) AuthorizeProposalPublish(
	context.Context, jobs.PipelineGitPublishProposalArgs,
) error {
	return nil
}

func (noopPipelineGitProposalRecorder) CompleteProposal(
	context.Context,
	pipelinegit.ProposalCompletion,
) error {
	return nil
}

func (noopPipelineGitProposalRecorder) FailProposal(
	context.Context,
	pipelinegit.ProposalFailure,
) error {
	return nil
}

func (noopPipelineGitProposalRecorder) CompleteProposalRefresh(
	context.Context, pipelinegit.ProposalRefreshCompletion,
) error {
	return nil
}

func (noopPipelineGitProposalRecorder) FailProposalRefresh(
	context.Context, pipelinegit.ProposalRefreshFailure,
) error {
	return nil
}

type dockerGitAuthResolver struct {
	repoURL    string
	knownHosts string
	keyPath    string
}

func newDockerPipelineGitExecutor(
	t *testing.T,
	resolver *dockerGitAuthResolver,
) workerconsumer.PipelineGitExecutor {
	t.Helper()

	fileExecutor, err := pipelinegit.NewFileExecutor(resolver)
	require.NoError(t, err)
	executor, err := workerconsumer.NewPipelineGitExecutor(fileExecutor, noopPipelineGitProposalExecutor{})
	require.NoError(t, err)

	return executor
}

type noopPipelineGitProposalExecutor struct{}

func (noopPipelineGitProposalExecutor) PublishProposal(
	context.Context,
	pipelinegit.ProposalOperation,
) (*pipelinegit.ProposalResult, error) {
	return &pipelinegit.ProposalResult{}, nil
}

func (noopPipelineGitProposalExecutor) RefreshProposal(
	context.Context, pipelinegit.ProposalRefreshOperation,
) (*pipelinegit.ProposalResult, error) {
	return &pipelinegit.ProposalResult{}, nil
}

func ptrString(value string) *string {
	return &value
}

func newDockerGitAuthResolver(t *testing.T) *dockerGitAuthResolver {
	t.Helper()

	require.Truef(
		t,
		isReachable("127.0.0.1", depsports.GitSSHPort()),
		"soft-serve is not reachable at 127.0.0.1:%d",
		depsports.GitSSHPort(),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	repoURL, cleanupGitFixture, err := fake.BootstrapIsolatedSeededGitFixture(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		require.NoError(t, cleanupGitFixture(cleanupCtx))
	})
	knownHosts, err := scanSoftServeKnownHosts(ctx)
	require.NoError(t, err)

	return &dockerGitAuthResolver{
		repoURL:    repoURL,
		knownHosts: knownHosts,
		keyPath:    depsports.GitTestPrivateKeyPath(),
	}
}

func (r *dockerGitAuthResolver) ResolveGitAuth(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*datasourcessvc.GitDataSourceAuth, error) {
	knownHostsPath, err := writeKnownHostsFile(r.knownHosts)
	if err != nil {
		return nil, err
	}

	gitSSHCommand := fmt.Sprintf(
		"ssh -i %s -o StrictHostKeyChecking=yes -o UserKnownHostsFile=%s -o IdentitiesOnly=yes -o ConnectTimeout=10 -o BatchMode=yes",
		r.keyPath,
		knownHostsPath,
	)

	return &datasourcessvc.GitDataSourceAuth{
		Env:           []string{"GIT_SSH_COMMAND=" + gitSSHCommand},
		RepoURL:       r.repoURL,
		DefaultBranch: "main",
		Cleanup: func() {
			_ = os.Remove(knownHostsPath)
		},
	}, nil
}

func readDockerGitFile(
	t *testing.T,
	resolver *dockerGitAuthResolver,
	path string,
) []byte {
	t.Helper()

	content, err := readDockerGitFileResult(t, resolver, path)
	require.NoError(t, err)

	return content
}

func readDockerGitFileResult(
	t *testing.T,
	resolver *dockerGitAuthResolver,
	path string,
) ([]byte, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	auth, err := resolver.ResolveGitAuth(ctx, slug.Slug("worker-e2e-org"), slug.Slug("worker-e2e-git"))
	require.NoError(t, err)
	defer auth.Cleanup()

	store, _, cleanup, err := repostore.CloneRemote(ctx, auth.RepoURL, auth.DefaultBranch, auth.Env)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	return store.ReadFile(ctx, path, 0)
}

func scanSoftServeKnownHosts(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(
		ctx,
		"ssh-keyscan",
		"-p",
		fmt.Sprintf("%d", depsports.GitSSHPort()),
		"127.0.0.1",
	).CombinedOutput()
	if err != nil {
		return "", xerror.Wrapf("scan Soft Serve host key: %s: %w", strings.TrimSpace(string(output)), err)
	}

	knownHosts := strings.TrimSpace(string(output))
	if knownHosts == "" {
		return "", xerror.Unexpected().Detail("scan Soft Serve host key: empty known_hosts output")
	}

	return knownHosts + "\n", nil
}

func writeKnownHostsFile(knownHosts string) (string, error) {
	tmpFile, err := os.CreateTemp("", "braidplane-soft-serve-known-hosts-*")
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	if _, err := tmpFile.WriteString(knownHosts); err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", xerror.Unexpected().With(err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", xerror.Unexpected().With(err)
	}

	return tmpFile.Name(), nil
}

func isReachable(host string, port int) bool {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(context.Background(), "tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func workerE2EUser(t *testing.T, pool *pgxpool.Pool, suffix string) *model.User {
	t.Helper()
	normalized := fmt.Sprintf("%s-%d", strings.ReplaceAll(suffix, "_", "-"), time.Now().UnixNano())
	user := &model.User{
		Slug:     slug.Slug("worker-e2e-" + normalized),
		Email:    fmt.Sprintf("worker-e2e-%s@example.com", normalized),
		FullName: "Worker E2E " + normalized,
		Role:     model.RoleUser,
		Locale:   "en",
	}
	_, err := pool.Exec(t.Context(), `INSERT INTO users (slug, email, full_name, role, locale) VALUES ($1,$2,$3,'user','en')`, user.Slug, user.Email, user.FullName)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM users WHERE slug=$1`, user.Slug)
		require.NoError(t, err)
	})
	return user
}

func uniqueWorkerE2ELink(kind string) string {
	return fmt.Sprintf("https://app.example.com/%s?token=worker-fixture-%d", kind, time.Now().UnixNano())
}

func uniqueWorkerE2EQueue(base string) string {
	return fmt.Sprintf("%s_e2e_%d", base, time.Now().UnixNano())
}
