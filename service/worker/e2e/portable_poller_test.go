package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	gitconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/git"
	"github.com/Braidplane/braidplane-core/service/executionbroker"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	workerconsumer "github.com/Braidplane/braidplane-core/service/worker/consumer"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

func TestPortablePollerPipelineGitWorkerJobExecutes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-Postgres River and Soft Serve E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openWorkerE2EPool(t)
	organizationSlug := slug.Slug(fmt.Sprintf("portable-poller-%d", time.Now().UnixNano()))
	_, err := pool.Exec(ctx, `INSERT INTO organizations (name, slug) VALUES ($1, $2)`, organizationSlug.String(), organizationSlug.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, err := pool.Exec(cleanupCtx, `DELETE FROM execution_requests WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, organizationSlug.String())
		require.NoError(t, err)
		_, err = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE slug = $1`, organizationSlug.String())
		require.NoError(t, err)
	})
	pipelineGitQueue := uniqueWorkerE2EQueue(jobs.PipelineGitQueue)
	path := fmt.Sprintf("pipelines/portable-poller-%d.pipeline.yaml", time.Now().UnixNano())
	secret := fmt.Sprintf("portable-poller-synthetic-secret-%d", time.Now().UnixNano())
	content := []byte("metadata:\n  slug: portable-poller\n# " + secret + "\n")
	argsFilter := map[string]any{"path": path}
	cleanupRiverJobsByArgs(t, pool, pipelineGitQueue, jobs.PipelineGitWriteFileArgs{}.Kind(), argsFilter)

	gitAuth := newDockerGitAuthResolver(t)
	fixturePrivateKey, err := os.ReadFile(gitAuth.keyPath)
	require.NoError(t, err)
	fixtureExecutionAuth, err := (portablePollerGitAuthResolver{resolver: gitAuth}).ResolveGitExecutionAuth(ctx, organizationSlug, slug.Slug("portable-poller-git"))
	require.NoError(t, err)
	preparedFixtureAuth, err := datasourcessvc.PrepareGitExecutionAuth(ctx, *fixtureExecutionAuth)
	require.NoError(t, err)
	defer preparedFixtureAuth.Cleanup()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	txm := postgres.NewTransactionManager(pool)
	apiBroker, err := executionbroker.New(
		txm,
		cipher,
		executionbroker.NewPollerDispatcher(),
		30*time.Second,
		nil,
		nil,
		testutil.FakeOperationAdmission{}, filestream.Profile{},
	)
	require.NoError(t, err)
	executorBroker, err := executionbroker.New(
		txm,
		cipher,
		executionbroker.NewPollerDispatcher(),
		30*time.Second,
		func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
			return nil, xerror.Unexpected().Detail("connector factory is not used for Git execution")
		},
		nil,
		testutil.FakeOperationAdmission{}, filestream.Profile{},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executorBroker.Close()) })
	require.NoError(t, executorBroker.Start(ctx, 1))

	fileExecutor, err := pipelinegit.NewRemoteFileExecutor(portablePollerGitAuthResolver{resolver: gitAuth}, apiBroker)
	require.NoError(t, err)
	pipelineGit, err := workerconsumer.NewPipelineGitExecutor(fileExecutor, noopPipelineGitProposalExecutor{})
	require.NoError(t, err)
	host, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
		Logger: slog.New(slog.DiscardHandler),
		Queues: map[string]river.QueueConfig{
			pipelineGitQueue: {MaxWorkers: 1},
		},
		Registrars: []queue.WorkerRegistrar{portablePollerWriteWorkerRegistrar{
			worker: workerconsumer.NewPipelineGitWriteFileWorker(pipelineGit, slog.New(slog.DiscardHandler)),
		}},
		RetryPolicy: fixedRetryPolicy{delay: time.Hour},
	})
	require.NoError(t, err)
	require.NoError(t, host.Start(ctx))
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		require.NoError(t, host.Stop(stopCtx))
	})
	producer, err := workerproducer.NewPipelineGitProducerForQueue(queue.NewEnqueuer(host), pipelineGitQueue)
	require.NoError(t, err)

	branch := "main"
	queuedAfter := time.Now().UTC()
	queued := enqueuePortablePollerGitWriteFileInTx(t, pool, producer, organizationSlug, &branch, path, content, secret)
	require.Equal(t, queue.QueuedJob{
		ID:    queued.ID,
		Kind:  jobs.PipelineGitWriteFileArgs{}.Kind(),
		Queue: pipelineGitQueue,
		State: "queued",
	}, queued)

	job := waitForRiverJobState(t, ctx, pool, pipelineGitQueue, jobs.PipelineGitWriteFileArgs{}.Kind(), argsFilter, "completed")
	require.Equal(t, 1, job.attempt)
	require.Equal(t, "[]", job.errorsJSON)

	request := portablePollerExecutionRequest(t, ctx, pool, organizationSlug, queuedAfter)
	require.Equal(t, "git_write", request.kind)
	require.Equal(t, "succeeded", request.state)
	require.False(t, bytes.Contains(request.ciphertext, content), "request ciphertext must not contain Git file content")
	require.False(t, bytes.Contains(request.ciphertext, []byte(secret)), "request ciphertext must not contain synthetic secret")
	require.False(t, bytes.Contains(request.ciphertext, fixturePrivateKey), "request ciphertext must not contain fixture SSH private key")

	remoteContent := readDockerGitFile(t, gitAuth, path)
	require.Equal(t, content, remoteContent)
}

type portablePollerGitAuthResolver struct {
	resolver *dockerGitAuthResolver
}

func (r portablePollerGitAuthResolver) ResolveGitExecutionAuth(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*datasourcessvc.GitExecutionAuth, error) {
	privateKey, err := os.ReadFile(r.resolver.keyPath)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	configJSON, err := json.Marshal(map[string]any{
		"repo_url":            r.resolver.repoURL,
		"default_branch":      "main",
		"allow_private_hosts": true,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	credentialsJSON, err := json.Marshal(map[string]any{
		"auth_type":       gitconnector.AuthTypeSSH,
		"ssh_private_key": string(privateKey),
		"ssh_known_hosts": r.resolver.knownHosts,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &datasourcessvc.GitExecutionAuth{
		GitWritable:         true,
		DataSourceType:      model.DataSourceTypeGit,
		ConfigJSON:          configJSON,
		CredentialsJSON:     credentialsJSON,
		CredentialReference: "portable-poller-fixture",
	}, nil
}

func enqueuePortablePollerGitWriteFileInTx(
	t *testing.T,
	pool *pgxpool.Pool,
	producer workerproducer.PipelineGitProducer,
	organizationSlug slug.Slug,
	branch *string,
	path string,
	content []byte,
	secret string,
) queue.QueuedJob {
	t.Helper()

	var queued queue.QueuedJob
	err := postgres.NewTransactionManager(pool).RunInTx(t.Context(), func(ctx context.Context, tx postgres.DBTX) error {
		var enqueueErr error
		queued, enqueueErr = producer.EnqueueWriteFile(
			ctx,
			tx,
			organizationSlug,
			slug.Slug("portable-poller-git"),
			slug.Slug("portable-poller-pipeline"),
			branch,
			path,
			content,
			"write "+secret,
			false,
		)
		return enqueueErr
	})
	require.NoError(t, err)

	return queued
}

type portablePollerRequest struct {
	id         string
	kind       string
	state      string
	ciphertext []byte
}

type portablePollerWriteWorkerRegistrar struct {
	worker river.Worker[jobs.PipelineGitWriteFileArgs]
}

func (r portablePollerWriteWorkerRegistrar) Register(workers *river.Workers) error {
	river.AddWorker(workers, r.worker)
	return nil
}

func portablePollerExecutionRequest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	organizationSlug slug.Slug,
	createdAfter time.Time,
) portablePollerRequest {
	t.Helper()

	var request portablePollerRequest
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT id::text, kind, state, request_ciphertext
		FROM execution_requests
		WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)
			AND kind = 'git_write'
			AND created_at >= $2
		ORDER BY created_at DESC
		LIMIT 1
	`, organizationSlug.String(), createdAfter).Scan(&request.id, &request.kind, &request.state, &request.ciphertext))
	return request
}
