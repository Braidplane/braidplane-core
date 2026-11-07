package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	s3connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

// Native River rescue takes an hour and must not be shortened.
// This exercises the real Runner/store/ledger/connector and queue, with a bounded
// test worker adapter instead of application HTTP admission or hosted workers.
func TestWorkerLossNativeCrashRehearsal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires native crash/rescue rehearsal and -timeout=80m")
	}
	ctx := t.Context()
	pool := openIntegrationPool(t)
	minio, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
		Image: "minio/minio:RELEASE.2025-04-22T22-12-26Z", ExposedPorts: []string{"9000/tcp"},
		Env: map[string]string{"MINIO_ROOT_USER": "fixture-access", "MINIO_ROOT_PASSWORD": "fixture-secret-for-local-tests"},
		Cmd: []string{"server", "/data"}, WaitingFor: wait.ForHTTP("/minio/health/ready").WithPort("9000/tcp"),
	}, Started: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, minio.Terminate(context.Background())) })
	endpoint, err := minio.Endpoint(ctx, "http")
	require.NoError(t, err)
	oracle := awss3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: awscredentials.NewStaticCredentialsProvider("fixture-access", "fixture-secret-for-local-tests", "")}, func(o *awss3.Options) { o.BaseEndpoint = &endpoint; o.UsePathStyle = true })
	_, err = oracle.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String("repair-fixture")})
	require.NoError(t, err)
	accepted := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var held atomic.Bool
	upstream, err := url.Parse(endpoint)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.Request.Method == http.MethodPut && strings.HasSuffix(response.Request.URL.Path, "/after-acceptance.json") && response.StatusCode == http.StatusOK && held.CompareAndSwap(false, true) {
			accepted <- struct{}{}
			select {
			case <-release:
			case <-response.Request.Context().Done():
			}
		}
		return nil
	}
	observer := httptest.NewServer(proxy)
	defer observer.Close()
	org, project, environment := slug.Slug("native-repair"), defaultProjectSlug, slug.Slug("prod")
	seedOrganization(t, pool, org, "Native repair fixture")
	seedPipelineDeploymentEnvironment(t, pool, org, project, environment)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, org)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = $1`, org)
		require.NoError(t, err)
	})
	_, err = pool.Exec(ctx, `CREATE TABLE worker_loss_rehearsal_source(id integer PRIMARY KEY, label text NOT NULL); INSERT INTO worker_loss_rehearsal_source VALUES (1,'original'); CREATE TABLE worker_loss_rehearsal_reads(run_slug text PRIMARY KEY, reads integer NOT NULL)`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DROP TABLE worker_loss_rehearsal_source, worker_loss_rehearsal_reads`)
		require.NoError(t, err)
	})
	pipeline := nativeRepairPipeline()
	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	materializePipelines(t, ctx, store, org, project, pipeline)
	meta, err := store.FindSpec(ctx, org, project, pipeline.Metadata.Slug)
	require.NoError(t, err)
	require.NotNil(t, meta.CurrentVersionID)
	client, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	config := nativeRepairConfig{Endpoint: observer.URL, Queue: "native-sink-repair"}
	jobs := map[string]int64{}
	crashedAt := map[string]time.Time{}
	for _, cut := range []string{"before-write", "after-acceptance"} {
		args := nativeRepairArgs{Run: cut, Version: *meta.CurrentVersionID}
		require.NoError(t, txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
			job, err := queue.NewEnqueuer(client).EnqueueTx(ctx, db, queue.EnqueueRequest{Args: args, Queue: config.Queue})
			if err != nil {
				return err
			}
			jobs[cut] = job.ID
			_, err = pipelineruns.New().CreateRun(ctx, db, org, project, environment, &pipelineruns.CreateRunParams{PipelineVersionID: args.Version, PipelineSlug: "repair", RunSlug: cut, Status: "queued", StartedAt: time.Now().UTC(), WorkerJobID: &job.ID, Timeline: []byte(`{"events":[]}`)})
			return err
		}))
		command, gate := startNativeRepairWorker(t, config)
		if cut == "before-write" {
			select {
			case <-gate:
			case <-time.After(20 * time.Second):
				var diagnostic string
				_ = pool.QueryRow(ctx, `SELECT json_build_object('run_status',pr.status,'failure',pr.timeline->'failure','job_state',j.state,'errors',j.errors)::text FROM pipeline_runs pr JOIN river_job j ON j.id=pr.worker_job_id WHERE pr.run_slug=$1`, cut).Scan(&diagnostic)
				t.Log(diagnostic)
				t.Fatal("worker did not reach pre-write claim gate")
			}
		} else {
			select {
			case <-accepted:
			case <-time.After(20 * time.Second):
				t.Fatal("MinIO did not accept the object before crash")
			}
			assertNativeRepairObject(t, oracle, cut)
		}
		runStore := pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New())
		checkpoint, err := runStore.GetCheckpoint(ctx, org, project, environment, cut)
		require.NoError(t, err)
		require.NotNil(t, checkpoint.Results["read"])
		timeline, err := runStore.Get(ctx, org, project, environment, cut)
		require.NoError(t, err)
		require.Equal(t, "running", timeline.Status, "crash gate must precede terminal timeout")
		require.ErrorIs(t, pipelineruns.New().LockRecoveryFence(ctx, pool, org, project, environment, cut), pipelineruns.ErrRunNotFound)
		crashedAt[cut] = time.Now().UTC()
		require.NoError(t, command.Process.Kill())
		require.Error(t, command.Wait(), "verified child worker must die at the held gate")
		t.Logf("SIGKILL cut=%s at=%s pid=%d; retained inputs present, original run running", cut, crashedAt[cut].Format(time.RFC3339Nano), command.Process.Pid)
	}
	// The held acknowledgement belongs to a dead worker, so later retries can proceed.
	releaseOnce.Do(func() { close(release) })
	_, err = pool.Exec(ctx, `UPDATE worker_loss_rehearsal_source SET label='changed-after-crash'`)
	require.NoError(t, err)
	restarted, _ := startNativeRepairWorker(t, config)
	defer func() { _ = restarted.Process.Kill(); _ = restarted.Wait() }()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(72 * time.Minute)
	defer deadline.Stop()
	pending := map[string]int64{"before-write": jobs["before-write"], "after-acceptance": jobs["after-acceptance"]}
	for len(pending) > 0 {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatal("native rescue did not finish within 72 minutes")
		case <-ticker.C:
			for cut, id := range pending {
				var state string
				var attempt int
				require.NoError(t, pool.QueryRow(ctx, `SELECT state::text,attempt FROM river_job WHERE id=$1`, id).Scan(&state, &attempt))
				if state != "completed" {
					continue
				}
				require.Equal(t, 2, attempt)
				t.Logf("native rescue cut=%s observed=%s elapsed_since_kill=%s attempt=%d state=%s", cut, time.Now().UTC().Format(time.RFC3339Nano), time.Since(crashedAt[cut]), attempt, state)
				delete(pending, cut)
			}
		}
	}
	runner := newNativeRepairRunner(ctx, t, pool, config, "recovery", false)
	runStore := pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New())
	for cut := range jobs {
		original, err := runStore.Get(ctx, org, project, environment, cut)
		require.NoError(t, err)
		require.Equal(t, "failed", original.Status)
		require.Equal(t, pipelinerun.FailureReasonCodeWorkerOwnershipLost, original.Failure.ReasonCode)
		require.NoError(t, txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
			return pipelineruns.New().LockRecoveryFence(ctx, db, org, project, environment, cut)
		}))
		for index := range 2 {
			outcome, err := runner.Recover(ctx, pipelineruntime.ReplayRequest{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, SourceRunSlug: cut, ReplayRunSlug: fmt.Sprintf("%s-repair-%d", cut, index), PipelineVersionID: *meta.CurrentVersionID})
			require.NoError(t, err)
			require.Equal(t, "succeeded", outcome.Metadata.Status)
			require.Equal(t, cut, *outcome.Metadata.ReplayOfRunSlug)
			if index == 1 {
				require.Equal(t, "skip_ledger_succeeded", outcome.Timeline.Snapshots[1].Input.SinkReplayDecision)
			}
			assertNativeRepairObject(t, oracle, cut)
		}
		retained, err := runStore.Get(ctx, org, project, environment, cut)
		require.NoError(t, err)
		require.Equal(t, original, retained, "repair must retain the original failed history")
		var reads int
		require.NoError(t, pool.QueryRow(ctx, `SELECT reads FROM worker_loss_rehearsal_reads WHERE run_slug=$1`, cut).Scan(&reads))
		require.Equal(t, 1, reads, "native redelivery and repair must not reread changed source rows")
	}
	objects, err := oracle.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: aws.String("repair-fixture")})
	require.NoError(t, err)
	require.Len(t, objects.Contents, 2, "one deterministic object per original effect identity")
	uploads, err := oracle.ListMultipartUploads(ctx, &awss3.ListMultipartUploadsInput{Bucket: aws.String("repair-fixture")})
	require.NoError(t, err)
	require.Empty(t, uploads.Uploads)
}

func assertNativeRepairObject(t *testing.T, oracle *awss3.Client, cut string) {
	t.Helper()
	object, err := oracle.GetObject(t.Context(), &awss3.GetObjectInput{Bucket: aws.String("repair-fixture"), Key: aws.String(cut + ".json")})
	require.NoError(t, err)
	defer func() { require.NoError(t, object.Body.Close()) }()
	body, err := io.ReadAll(object.Body)
	require.NoError(t, err)
	require.JSONEq(t, `[{"id":1,"label":"original"}]`, string(body), "independent direct MinIO readback must retain original data")
}

type (
	nativeRepairConfig struct{ Endpoint, Queue string }
	nativeRepairArgs   struct {
		Run     string
		Version int64
	}
)

func (nativeRepairArgs) Kind() string                 { return "worker_loss_native_repair" }
func (nativeRepairArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{} }

type nativeRepairWorker struct {
	river.WorkerDefaults[nativeRepairArgs]
	t      *testing.T
	pool   *pgxpool.Pool
	config nativeRepairConfig
}

func (w *nativeRepairWorker) Register(workers *river.Workers) error {
	return river.AddWorkerSafely(workers, w)
}

func (w *nativeRepairWorker) Work(ctx context.Context, job *river.Job[nativeRepairArgs]) error {
	w.t.Logf("working run=%s attempt=%d", job.Args.Run, job.Attempt)
	runner := newNativeRepairRunner(ctx, w.t, w.pool, w.config, job.Args.Run, job.Attempt == 1 && job.Args.Run == "before-write")
	_, err := runner.Run(ctx, pipelineruntime.RunRequest{OrgSlug: "native-repair", ProjectSlug: defaultProjectSlug, EnvironmentSlug: "prod", RunSlug: job.Args.Run, PipelineVersionID: job.Args.Version, Pipeline: nativeRepairPipeline()})
	w.t.Logf("run=%s returned error=%v", job.Args.Run, err)
	var appErr *xerror.Error
	if xerror.As(err, &appErr) && appErr.Code() == "RUN_ATTEMPT_INTERRUPTED" {
		return nil
	}
	return err
}

func TestNativeRepairWorkerProcess(t *testing.T) {
	if os.Getenv("BP_SINK_CRASH_WORKER") != "1" {
		return
	}
	var config nativeRepairConfig
	require.NoError(t, json.Unmarshal([]byte(os.Getenv("BP_SINK_CRASH_CONFIG")), &config))
	pool := openIntegrationPool(t)
	worker := &nativeRepairWorker{t: t, pool: pool, config: config}
	host, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{Queues: map[string]river.QueueConfig{config.Queue: {MaxWorkers: 1}}, Registrars: []queue.WorkerRegistrar{worker}, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)
	require.NoError(t, host.Start(t.Context()))
	<-t.Context().Done()
}

func startNativeRepairWorker(t *testing.T, config nativeRepairConfig) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	encoded, err := json.Marshal(config)
	require.NoError(t, err)
	binary, err := os.Executable()
	require.NoError(t, err)
	// #nosec G204 G702 -- re-executes this test binary against its own isolated fixture.
	command := exec.CommandContext(t.Context(), binary, "-test.run=^TestNativeRepairWorkerProcess$", "-test.timeout=78m", "-test.v")
	command.Env = append(os.Environ(), "BP_SINK_CRASH_WORKER=1", "BP_SINK_CRASH_CONFIG="+string(encoded))
	command.Stderr = os.Stderr
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	t.Cleanup(func() { _ = command.Process.Kill() })
	gate := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			t.Logf("owned worker: %s", scanner.Text())
			if scanner.Text() == "BP_SINK_BEFORE_WRITE" {
				gate <- struct{}{}
			}
		}
	}()
	return command, gate
}

type nativeRepairSource struct {
	noopQueryExecutor
	pool *pgxpool.Pool
	run  string
}

func (nativeRepairSource) GetExpectedInputContract(context.Context, slug.Slug, slug.Slug, slug.Slug) (*payload.Contract, error) {
	return nativeRepairPipeline().Steps[1].Sink.InputContract, nil
}

func (s nativeRepairSource) Execute(ctx context.Context, _, _, _ slug.Slug, _ map[string]any) (*result.ExecutionResult, error) {
	_, err := s.pool.Exec(ctx, `INSERT INTO worker_loss_rehearsal_reads VALUES($1,1) ON CONFLICT(run_slug) DO UPDATE SET reads=worker_loss_rehearsal_reads.reads+1`, s.run)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	var id int
	var label string
	if err := s.pool.QueryRow(ctx, `SELECT id,label FROM worker_loss_rehearsal_source`).Scan(&id, &label); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": id, "label": label}}, ColumnNames: []string{"id", "label"}, RowCount: 1}, 0, nil), nil
}

type nativeRepairSink struct{ connector *s3connector.Connector }

func (s nativeRepairSink) Execute(ctx context.Context, _, _, _ slug.Slug, _ model.EventActor, params map[string]any, _ ...queriessvc.ExecuteOption) (*result.ExecutionResult, error) {
	typed := make(map[string]types.TypedParam, len(params))
	for key, value := range params {
		typed[key] = types.TypedParam{Value: value}
	}
	return s.connector.Execute(ctx, []byte(`{"type":"s3","operation":"write","content":"{{input}}","key":"{{key}}","format":"json","overwrite":true}`), typed)
}

type nativeRepairGateLedger struct {
	pipelinerun.SinkEffectLedger
	hold bool
}

func (l nativeRepairGateLedger) BeginOrGet(ctx context.Context, org, project, environment slug.Slug, attempt pipelinerun.SinkEffectAttempt) (*pipelinerun.SinkEffect, error) {
	effect, err := l.SinkEffectLedger.BeginOrGet(ctx, org, project, environment, attempt)
	if err == nil && l.hold {
		fmt.Println("BP_SINK_BEFORE_WRITE")
		<-ctx.Done()
		return nil, xerror.Unexpected().With(ctx.Err())
	}
	return effect, err
}

func newNativeRepairRunner(ctx context.Context, t *testing.T, pool *pgxpool.Pool, config nativeRepairConfig, run string, hold bool) *pipelineruntime.Runner {
	t.Helper()
	txm := postgres.NewTransactionManager(pool)
	connector, err := s3connector.New(transportsecurity.WithPlaintext(ctx), s3connector.Config{Endpoint: config.Endpoint, Region: "us-east-1", Bucket: "repair-fixture", ForcePathStyle: true, AllowPrivateHosts: true}, s3connector.Credentials{AccessKeyID: "fixture-access", SecretAccessKey: "fixture-secret-for-local-tests"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connector.Close()) })
	sink := pipelineruntime.NewSinkRuntime(nativeRepairSink{connector}, nativeRepairGateLedger{testSinkEffectLedger(t, txm), hold})
	runner, err := pipelineruntime.NewRunner(nativeRepairSource{pool: pool, run: run}, clock.New(), pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()), nativeRepairSources{}, sink)
	require.NoError(t, err)
	return runner
}

type nativeRepairSources struct{ datasourcessvc.Service }

func (nativeRepairSources) FindBySlug(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error) {
	return &datasourcessvc.DataSource{Type: model.DataSourceTypeS3}, nil
}

func nativeRepairPipeline() *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "repair", Name: "Native repair"}, Steps: []pipelinespec.Step{
		{Kind: "query", Slug: "read", Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OperationType: "sql", OperationRef: "read"}},
		{Kind: "sink", Slug: "write", Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: "read"}, DataSourceSlug: "objects", OperationType: "s3", OperationRef: "write", IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent, Config: map[string]any{"key": "{{runtime.sink.replay_identity}}.json", "format": "json", "overwrite": true}, InputContract: &payload.Contract{Name: "rows", Representation: model.ContractRepresentationRows, Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}, {Name: "label", Type: model.ContractFieldTypeValueString, Required: true}}}}},
	}}
}
