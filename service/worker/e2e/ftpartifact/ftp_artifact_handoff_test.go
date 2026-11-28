package ftpartifact

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
	"github.com/Braidplane/braidplane-core/postgres/pipelinesinkeffects"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/ftp"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/executionbroker"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/Braidplane/braidplane-core/service/worker/consumer"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/Braidplane/braidplane-core/service/worker/producer"
)

type loseArtifactCommitReply struct {
	postgres.TransactionManager
	lose       atomic.Bool
	unresolved atomic.Bool
}

type artifactTransactionKey struct{}

func (txm *loseArtifactCommitReply) RunInTx(ctx context.Context, f func(context.Context, postgres.DBTX) error) error {
	outermost := ctx.Value(artifactTransactionKey{}) == nil
	err := txm.TransactionManager.RunInTx(context.WithValue(ctx, artifactTransactionKey{}, true), func(ctx context.Context, tx postgres.DBTX) error {
		err := f(ctx, tx)
		if err == nil && outermost && txm.unresolved.Swap(false) {
			return xerror.Unexpected().Detail("fixture withheld the transaction outcome")
		}
		return err
	})
	if err == nil && outermost && txm.lose.Swap(false) {
		return xerror.New(xerror.KindInternal, "FIXTURE_COMMIT_REPLY_LOST", "fixture simulated loss of transaction commit response")
	}
	return err
}

type inspectArtifactDispatch func(context.Context, postgres.DBTX, uuid.UUID) error

func (f inspectArtifactDispatch) Dispatch(ctx context.Context, tx postgres.DBTX, id uuid.UUID) error {
	return f(ctx, tx, id)
}

type artifactAdmissionGate struct {
	testutil.FakeOperationAdmission
	next atomic.Pointer[artifactGate]
}

type artifactGate struct {
	entered chan struct{}
	release chan struct{}
}

func (a *artifactAdmissionGate) WithOperation(ctx context.Context, _, _ slug.Slug, execute func(context.Context) error) error {
	if gate := a.next.Swap(nil); gate != nil {
		close(gate.entered)
		select {
		case <-gate.release:
		case <-ctx.Done():
		}
	}
	return execute(ctx)
}

// The fixture can expire the caller's wait after the broker worker reaches admission.
type artifactCallerDeadline struct {
	context.Context
	elapsed chan struct{}
}

func (ctx artifactCallerDeadline) Done() <-chan struct{} { return ctx.elapsed }

func (ctx artifactCallerDeadline) Err() error {
	select {
	case <-ctx.elapsed:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func TestFTPArtifactHandoff(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL and FTP")
	}
	pool := e2edb.OpenPostgresPool(t, "FTP artifact broker")
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	provider := ftp.Config{Host: "127.0.0.1", Port: depsports.FTPConnectorPort(), BasePath: "/", ConnectTimeout: 5, AllowPrivateHosts: true}
	proxy := startFTPRenameProxy(t, provider.Port)
	proxied := provider
	proxied.Port = proxy.port
	config, err := json.Marshal(proxied)
	require.NoError(t, err)
	digest, err := connector.PublicConfigDigest(model.DataSourceTypeFTP, config)
	require.NoError(t, err)
	const write = `{"operation":"write","format":"csv","path":"orders.csv"}`
	const read = `{"operation":"read","format":"csv","path":"orders.csv"}`
	for _, mode := range []string{"poller", "river"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			org := slug.Slug("artifact-" + mode)
			var projectID, envID, writeID, readID, versionID int64
			var credRevision, writeRevision, readRevision time.Time
			require.NoError(t, pool.QueryRow(ctx, `
				WITH org AS (INSERT INTO organizations (slug,name) VALUES ($1,$1) RETURNING id),
				project AS (INSERT INTO projects (organization_id,slug,name) SELECT id,'orders','Orders' FROM org RETURNING id),
				env AS (INSERT INTO environments (organization_id,slug,name) SELECT id,'test','Test' FROM org RETURNING id),
				scope AS (INSERT INTO project_environments (organization_id,project_id,environment_id)
					SELECT org.id,project.id,env.id FROM org,project,env RETURNING id),
				cred AS (INSERT INTO credentials (organization_id,slug,kind,credential_type,status,encrypted_payload)
					SELECT id,'ftp-credential','static_secret','ftp','active',decode('00','hex') FROM org RETURNING id,organization_id,updated_at),
				source AS (INSERT INTO data_sources (organization_id,credential_id,slug,name,type,config,is_active)
					SELECT organization_id,id,'ftp-source','FTP','ftp',$2,true FROM cred RETURNING id),
				w AS (INSERT INTO queries (data_source_id,slug,name,data)
					SELECT id,'publish','Publish',$3 FROM source RETURNING id,updated_at),
				r AS (INSERT INTO queries (data_source_id,slug,name,data)
					SELECT id,'verify','Verify',$4 FROM source RETURNING id,updated_at),
				spec AS (INSERT INTO pipeline_specs (project_id,slug,name,spec_jsonb,spec_hash,source_path,source_ref,last_applied_commit)
					SELECT id,'orders','Orders','{}','synthetic-spec','orders.yaml','refs/heads/main',repeat('a',40) FROM project
					RETURNING project_id,slug),
				v AS (INSERT INTO pipeline_versions (project_id,pipeline_slug,spec_jsonb,spec_hash)
					SELECT project_id,slug,'{}','synthetic-version' FROM spec RETURNING id)
				SELECT project.id,scope.id,cred.updated_at,w.id,w.updated_at,r.id,r.updated_at,v.id
				FROM project,scope,cred,w,r,v
			`, org, config, write, read).Scan(&projectID, &envID, &credRevision, &writeID, &writeRevision, &readID, &readRevision, &versionID))

			txm := postgres.NewTransactionManager(pool)
			apiTx := &loseArtifactCommitReply{TransactionManager: txm}
			executorTx := &loseArtifactCommitReply{TransactionManager: txm}
			var providerConnections atomic.Int64
			var failTransport atomic.Bool
			admission := &artifactAdmissionGate{}
			base := connector.NewConnectorWithTracing(connector.NewConnectorWithTransportPolicy(connector.NewConnector, true))
			executor, err := executionbroker.New(executorTx, cipher, executionbroker.NewPollerDispatcher(), 8*time.Second, func(ctx context.Context, kind model.DataSourceType, cfg, cred []byte) (connector.Connector, error) {
				providerConnections.Add(1)
				if failTransport.Swap(false) {
					return nil, &net.OpError{Op: "read", Net: "tcp", Err: io.ErrUnexpectedEOF}
				}
				conn, err := base(ctx, kind, cfg, cred)
				if err != nil {
					t.Logf("synthetic FTP factory: %v", err)
				}
				return conn, err
			}, nil, admission)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, executor.Close()) })
			dispatcher := executionbroker.NewPollerDispatcher()
			if mode == "poller" {
				require.NoError(t, executor.Start(ctx, 1))
			} else {
				workers := river.NewWorkers()
				river.AddWorker(workers, consumer.NewExecutionRequestWorker(executor))
				host, err := queue.New(pool, &queue.Config{Workers: workers, Queues: map[string]river.QueueConfig{jobs.ExecutionQueue: {MaxWorkers: 1}}})
				require.NoError(t, err)
				require.NoError(t, host.Start(ctx))
				defer func() { require.NoError(t, host.Stop(context.WithoutCancel(ctx))) }()
				dispatcher, err = producer.NewExecutionDispatcher(queue.NewEnqueuer(host))
				require.NoError(t, err)
			}

			delegate := dispatcher
			rejectDispatch := xerror.New(xerror.KindInternal, "FIXTURE_DISPATCH_REJECTED", "fixture rejected dispatch after insertion")
			var rollbackDispatch atomic.Bool
			dispatcher = inspectArtifactDispatch(func(ctx context.Context, tx postgres.DBTX, id uuid.UUID) error {
				if err := delegate.Dispatch(ctx, tx, id); err != nil {
					return err
				}
				var visible bool
				err := pool.QueryRow(ctx, `SELECT
					EXISTS(SELECT 1 FROM execution_requests WHERE id=$1) OR
					EXISTS(SELECT 1 FROM pipeline_artifact_deliveries WHERE publish_request_id=$1 AND submitted_at IS NOT NULL) OR
					EXISTS(SELECT 1 FROM river_job WHERE args->>'request_id'=$2)`, id, id.String()).Scan(&visible)
				if err != nil {
					return err
				}
				if visible {
					return xerror.Unexpected().Detail("artifact dispatch became visible before commit")
				}
				if rollbackDispatch.Swap(false) {
					return rejectDispatch
				}
				return nil
			})

			api, err := executionbroker.New(apiTx, cipher, dispatcher, 8*time.Second, nil, nil, testutil.FakeOperationAdmission{})
			require.NoError(t, err)
			req := executionbroker.FTPArtifactRequest{
				ConnectorExecutionRequest: queriessvc.ConnectorExecutionRequest{
					OrganizationSlug: org, DataSourceSlug: "ftp-source", DataSourceType: model.DataSourceTypeFTP,
					PinnedConfigDigest: digest, ConfigJSON: config, CredentialsJSON: []byte(`{"username":"braidplane","password":"braidplanepass"}`),
					CredentialReference: "ftp-credential/" + credRevision.Format(time.RFC3339Nano), StatementJSON: json.RawMessage(write),
					SinkExecution: true, ConcurrencyLimit: 1, ConcurrencyQueueLimit: 4, ConcurrencyQueueTimeout: 4 * time.Second,
					Limits: types.ExecutionLimits{MaxResultRows: 10, MaxFileBytes: 1024, MaxResponseBytes: 1024},
				},
				ArtifactEvidence: ftp.ArtifactEvidence{DeliveryID: uuid.New()}, Directory: "nested", Content: json.RawMessage(`[{"id":"102"}]`),
				PublishRequestID: uuid.New(), ProjectEnvironmentID: envID,
			}
			evidence, err := api.PrepareFTPArtifact(ctx, req)
			require.NoError(t, err)
			require.Zero(t, providerConnections.Load())
			req.ArtifactEvidence = evidence
			seedIntent := func(req executionbroker.FTPArtifactRequest, runSlug string) {
				t.Helper()
				evidence := req.ArtifactEvidence
				require.NoError(t, txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
					_, err := tx.Exec(ctx, `
						WITH run AS (
							INSERT INTO pipeline_runs (project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,
								pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,deployment_id,deployment_resolution_digest)
							VALUES ($1,$2,$3,'synthetic-version','v1','orders',$4,'test','running',0,'sink','{}',9,'resolution')
							RETURNING project_environment_id,run_slug
						)
						INSERT INTO pipeline_sink_effects (project_environment_id,run_slug,sink_step_slug,idempotency_key,idempotency_class,replay_method,replay_decision)
						SELECT project_environment_id,run_slug,'sink','effect','none','none','refused' FROM run
					`, projectID, envID, versionID, runSlug)
					if err != nil {
						return err
					}
					created, err := pipelinesinkeffects.CreateFTPArtifactIntent(ctx, tx, dbgen.CreateFTPArtifactIntentParams{
						DeliveryID:       pgtype.UUID{Bytes: evidence.DeliveryID.UUID, Valid: true},
						PublishRequestID: pgtype.UUID{Bytes: req.PublishRequestID.UUID, Valid: true},
						OrgSlug:          org.String(), ProjectEnvironmentID: envID, AttemptSlug: runSlug, SinkStepSlug: "sink", EffectKey: "effect",
						SourceSlug: "ftp-source", ExpectedConfig: config, CredentialSlug: "ftp-credential",
						CredentialRevision: pgtype.Timestamptz{Time: credRevision, Valid: true}, WriteQueryID: writeID,
						WriteQueryRevision: pgtype.Timestamptz{Time: writeRevision, Valid: true}, Statement: []byte(write),
						ConfigDigest: digest, FinalPath: evidence.FinalPath, StagingPath: evidence.StagingPath, SizeBytes: evidence.SizeBytes, Sha256: evidence.SHA256,
					})
					if err == nil && !created {
						return xerror.Unexpected().Detail("scoped artifact intent unexpectedly refused")
					}
					return err
				}))
			}
			seedIntent(req, "produce")

			unknownID := uuid.New()
			unknownPayload, err := json.Marshal(req)
			require.NoError(t, err)
			unknownCiphertext, err := cipher.Encrypt(unknownPayload)
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `INSERT INTO execution_requests(id,organization_id,kind,request_ciphertext,deadline_at,expires_at)
				SELECT $1,id,'fixture_unrecognized_artifact_kind',$2,clock_timestamp()+interval '30 seconds',clock_timestamp()+interval '1 hour'
				FROM organizations WHERE slug=$3`, unknownID, unknownCiphertext, org)
			require.NoError(t, err)
			_, err = executor.Process(ctx, unknownID)
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				var state string
				err := pool.QueryRow(ctx, `SELECT state FROM execution_requests WHERE id=$1`, unknownID).Scan(&state)
				return err == nil && state == "failed"
			}, time.Second, 25*time.Millisecond)
			require.Zero(t, providerConnections.Load())

			rollbackDispatch.Store(true)
			require.ErrorIs(t, api.PublishFTPArtifact(ctx, req), rejectDispatch)
			var rolledBack bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT
				NOT EXISTS(SELECT 1 FROM execution_requests WHERE id=$1) AND
				NOT EXISTS(SELECT 1 FROM river_job WHERE args->>'request_id'=$2) AND
				EXISTS(SELECT 1 FROM pipeline_artifact_deliveries WHERE publish_request_id=$1 AND submitted_at IS NULL)`,
				req.PublishRequestID, req.PublishRequestID.String()).Scan(&rolledBack))
			require.True(t, rolledBack)
			require.Zero(t, providerConnections.Load())

			apiTx.unresolved.Store(true)
			require.ErrorIs(t, api.PublishFTPArtifact(ctx, req), executionbroker.ErrAmbiguousExecution)
			require.Zero(t, providerConnections.Load())
			beforePublish := proxy.renames.Load()
			apiTx.lose.Store(true)
			publishErr := api.PublishFTPArtifact(ctx, req)
			if publishErr != nil {
				var spent bool
				require.NoError(t, pool.QueryRow(ctx, `SELECT permit_spent_at IS NOT NULL FROM pipeline_artifact_deliveries WHERE id=$1`, req.DeliveryID).Scan(&spent))
				t.Logf("publish permit spent=%t, provider connections=%d", spent, providerConnections.Load())
			}
			require.NoError(t, publishErr)
			require.NoError(t, api.PublishFTPArtifact(ctx, req))
			_, err = executor.Process(ctx, req.PublishRequestID)
			require.NoError(t, err)
			require.EqualValues(t, 1, providerConnections.Load())
			require.Equal(t, beforePublish+1, proxy.renames.Load())

			abortConfirmation := xerror.New(xerror.KindInternal, "FIXTURE_CONFIRMATION_ABORTED", "fixture aborted receipt confirmation")
			require.ErrorIs(t, txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
				ok, err := pipelinesinkeffects.ConfirmFTPArtifactDelivery(ctx, tx, org, envID, evidence.DeliveryID, req.PublishRequestID)
				if err != nil {
					return err
				}
				if !ok {
					return xerror.Unexpected().Detail("expected confirmation inside the disposable transaction")
				}
				return abortConfirmation
			}), abortConfirmation)
			var effectState string
			require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM pipeline_sink_effects WHERE project_environment_id=$1 AND run_slug='produce'`, envID).Scan(&effectState))
			require.Equal(t, "in_progress", effectState)
			beforeConfirmation, err := pipelinesinkeffects.FindFTPArtifactIntent(ctx, pool, org, envID, evidence.DeliveryID)
			require.NoError(t, err)
			require.False(t, beforeConfirmation.ConfirmedAt.Valid)

			confirmed, err := pipelinesinkeffects.ConfirmFTPArtifactDelivery(ctx, pool, org, envID, evidence.DeliveryID, req.PublishRequestID)
			require.NoError(t, err)
			require.True(t, confirmed)
			_, err = pool.Exec(ctx, `UPDATE pipeline_runs SET status='succeeded' WHERE project_environment_id=$1 AND run_slug='produce'`, envID)
			require.NoError(t, err)
			req.SinkExecution, req.Content = false, nil
			req.StatementJSON, req.ReadQueryID, req.ReadQueryUpdatedAt = json.RawMessage(read), readID, readRevision
			rows, err := api.ReadFTPArtifact(ctx, req)
			require.NoError(t, err)
			require.Equal(t, []map[string]any{{"id": "102"}}, rows.Rows)
			require.Empty(t, rows.TargetPath)
			require.EqualValues(t, 2, providerConnections.Load())

			failTransport.Store(true)
			rows, err = api.ReadFTPArtifact(ctx, req)
			require.Nil(t, rows)
			var typed executionbroker.FTPArtifactError
			require.ErrorAs(t, err, &typed)
			require.True(t, typed.RetryableRead())

			readGate := &artifactGate{entered: make(chan struct{}), release: make(chan struct{})}
			admission.next.Store(readGate)
			waitCtx := artifactCallerDeadline{Context: ctx, elapsed: make(chan struct{})}
			waitResult := make(chan error, 1)
			go func(ctx context.Context) { //nolint:contextcheck // The controlled fixture deadline inherits the enclosing test context.
				_, err := api.ReadFTPArtifact(ctx, req)
				waitResult <- err
			}(waitCtx)
			select {
			case <-readGate.entered:
			case <-ctx.Done():
				close(waitCtx.elapsed)
				close(readGate.release)
				t.Fatal("verified read did not reach the executor")
			}
			close(waitCtx.elapsed)
			select {
			case err := <-waitResult:
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.NotErrorAs(t, err, &typed)
			case <-ctx.Done():
				t.Fatal("broker did not return the caller's timeout")
			}
			close(readGate.release)

			providerCalls := providerConnections.Load()
			require.EqualValues(t, 3, providerCalls)

			var encrypted bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT position($2::bytea IN request_ciphertext)=0 AND position($2::bytea IN result_ciphertext)=0
				FROM execution_requests WHERE id=$1`, req.PublishRequestID, []byte(evidence.FinalPath)).Scan(&encrypted))
			require.True(t, encrypted)

			var jobCount int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE args->>'request_id'=$1`, req.PublishRequestID.String()).Scan(&jobCount))
			if mode == "river" {
				require.Equal(t, 1, jobCount)
				var args string
				require.NoError(t, pool.QueryRow(ctx, `SELECT args::text FROM river_job WHERE args->>'request_id'=$1`, req.PublishRequestID.String()).Scan(&args))
				require.JSONEq(t, `{"request_id":"`+req.PublishRequestID.String()+`"}`, args)
			} else {
				require.Zero(t, jobCount)
			}
			_, err = pool.Exec(ctx, `DELETE FROM execution_requests WHERE id=$1`, req.PublishRequestID)
			require.NoError(t, err)
			req.SinkExecution, req.Content, req.StatementJSON = true, json.RawMessage(`[{"id":"102"}]`), json.RawMessage(write)
			require.Error(t, api.PublishFTPArtifact(ctx, req))
			require.Equal(t, providerCalls, providerConnections.Load())

			for _, failure := range []string{"cancel", "lease-expiry", "permit-commit-reply"} {
				failed := req
				failed.DeliveryID, failed.PublishRequestID = uuid.New(), uuid.New()
				failed.ArtifactEvidence, err = api.PrepareFTPArtifact(ctx, failed)
				require.NoError(t, err)
				seedIntent(failed, failure)
				gate := &artifactGate{entered: make(chan struct{}), release: make(chan struct{})}
				admission.next.Store(gate)
				callCtx, cancelCall := context.WithCancel(ctx)
				published := make(chan error, 1)
				go func() { published <- api.PublishFTPArtifact(callCtx, failed) }()
				select {
				case <-gate.entered:
				case <-ctx.Done():
					cancelCall()
					t.Fatal("executor did not claim artifact")
				}
				switch failure {
				case "cancel":
					cancelCall()
				case "permit-commit-reply":
					executorTx.lose.Store(true)
					close(gate.release)
				case "lease-expiry":
					_, err = pool.Exec(ctx, `UPDATE execution_requests SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, failed.PublishRequestID)
					require.NoError(t, err)
					_, err = executor.Process(ctx, failed.PublishRequestID)
					require.NoError(t, err)
				}
				select {
				case err := <-published:
					if failure == "permit-commit-reply" {
						require.ErrorContains(t, err, "artifact_publication_uncertain")
					} else {
						require.ErrorIs(t, err, executionbroker.ErrAmbiguousExecution)
					}
				case <-ctx.Done():
					t.Fatal("broker did not return an ambiguous outcome")
				}
				cancelCall()
				if failure != "permit-commit-reply" {
					close(gate.release)
				}
				require.Eventually(t, func() bool {
					var state string
					var spent bool
					err := pool.QueryRow(ctx, `SELECT r.state, d.permit_spent_at IS NOT NULL
						FROM execution_requests r JOIN pipeline_artifact_deliveries d ON d.publish_request_id=r.id WHERE r.id=$1`,
						failed.PublishRequestID).Scan(&state, &spent)
					if failure == "permit-commit-reply" {
						return err == nil && state == "failed" && spent
					}
					return err == nil && state == "ambiguous" && !spent
				}, 3*time.Second, 25*time.Millisecond)
				require.Equal(t, providerCalls, providerConnections.Load())
			}

			duplicate := req
			duplicate.DeliveryID, duplicate.PublishRequestID = uuid.New(), uuid.New()
			duplicate.ArtifactEvidence, err = api.PrepareFTPArtifact(ctx, duplicate)
			require.NoError(t, err)
			seedIntent(duplicate, "duplicate-waiter")
			publishGate := &artifactGate{entered: make(chan struct{}), release: make(chan struct{})}
			admission.next.Store(publishGate)
			published := make(chan error, 1)
			go func() { published <- api.PublishFTPArtifact(ctx, duplicate) }()
			select {
			case <-publishGate.entered:
			case <-ctx.Done():
				close(publishGate.release)
				t.Fatal("publication did not reach the executor")
			}
			duplicateAPI, err := executionbroker.New(txm, cipher, dispatcher, 200*time.Millisecond, nil, nil, testutil.FakeOperationAdmission{})
			require.NoError(t, err)
			require.ErrorIs(t, duplicateAPI.PublishFTPArtifact(ctx, duplicate), executionbroker.ErrAmbiguousExecution)
			var requestStillLive bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT state='running' AND cancel_requested_at IS NULL FROM execution_requests WHERE id=$1`, duplicate.PublishRequestID).Scan(&requestStillLive))
			require.True(t, requestStillLive)
			close(publishGate.release)
			select {
			case err := <-published:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("original publication did not finish")
			}
			providerCalls++

			uncertain := req
			uncertain.DeliveryID, uncertain.PublishRequestID = uuid.New(), uuid.New()
			uncertain.ArtifactEvidence, err = api.PrepareFTPArtifact(ctx, uncertain)
			require.NoError(t, err)
			seedIntent(uncertain, "rename-reply")
			renames := proxy.renames.Load()
			proxy.dropNextRename.Store(true)
			require.ErrorContains(t, api.PublishFTPArtifact(ctx, uncertain), "artifact_publication_uncertain")
			require.Equal(t, renames+1, proxy.renames.Load())
			require.ErrorContains(t, api.PublishFTPArtifact(ctx, uncertain), "artifact_publication_uncertain")
			require.Equal(t, renames+1, proxy.renames.Load())
			require.Equal(t, providerCalls+1, providerConnections.Load())
			confirmed, err = pipelinesinkeffects.ConfirmFTPArtifactDelivery(ctx, pool, org, envID, uncertain.DeliveryID, uncertain.PublishRequestID)
			require.NoError(t, err)
			require.False(t, confirmed)
			receipt, err := pipelinesinkeffects.FindFTPArtifactIntent(ctx, pool, org, envID, uncertain.DeliveryID)
			require.NoError(t, err)
			require.True(t, receipt.PermitSpentAt.Valid)
			require.False(t, receipt.ConfirmedAt.Valid)
			direct, err := ftp.New(transportsecurity.WithPlaintext(ctx), provider, ftp.Credentials{Username: "braidplane", Password: "braidplanepass"})
			require.NoError(t, err)
			t.Cleanup(func() { _ = direct.Close() })
			rows, err = direct.FetchArtifactCSV(ctx, uncertain.ArtifactEvidence, 1024, filedata.ParseCSVOptions{})
			require.NoError(t, err)
			require.Equal(t, []map[string]any{{"id": "102"}}, rows.Rows)
		})
	}
}
