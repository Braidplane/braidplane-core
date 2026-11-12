package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	credentialsrepo "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	queriesrepo "github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	s3connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	"github.com/Braidplane/braidplane-core/service/executionbroker"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

func TestNativeFileStreaming(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL and S3 fixtures")
	}
	endpoint := depsports.S3ConnectorEndpoint()
	fixtureURL, err := url.Parse(endpoint)
	require.NoError(t, err)
	const size = int64(9<<20) + 17
	ctx, cancel := context.WithTimeout(transportsecurity.WithPlaintext(t.Context()), 3*time.Minute)
	defer cancel()
	pool := openE2EPool(t)
	org := slug.Slug(fmt.Sprintf("native-file-%d", time.Now().UnixNano()))
	project, environment := defaultProjectSlug, slug.Slug("prod")
	seedOrganization(t, pool, org, "Native file transfer")
	seedPipelineDeploymentEnvironment(t, pool, org, project, environment)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), `DELETE FROM project_environments WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug=$1`, org)
		require.NoError(t, err)
	})
	txm := postgres.NewTransactionManager(pool)
	encryption, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	credentialCipher, err := crypto.NewCredentialKeyring("fixture", map[string][]byte{"fixture": make([]byte, 32)}, nil)
	require.NoError(t, err)
	credentialsJSON := []byte(`{"access_key_id":"braidplane","secret_access_key":"braidplanepass"}`)
	encrypted, err := credentialCipher.Seal(crypto.CredentialScope{Tenant: org.String(), Resource: "fixture-credential", Purpose: "static-secret", SchemaVersion: 1}, credentialsJSON)
	require.NoError(t, err)
	config, err := json.Marshal(s3connector.Config{Region: "us-east-1", Bucket: "braidplane-fixtures", Endpoint: endpoint, ForcePathStyle: true, AllowPrivateHosts: true, TimeoutSeconds: 300})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `WITH cred AS (INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload) SELECT id,'fixture-credential','static_secret','s3','active',$2 FROM organizations WHERE slug=$1 RETURNING id,organization_id) INSERT INTO data_sources(organization_id,credential_id,name,slug,type,config,is_active,read_only) SELECT organization_id,id,'Native files','objects','s3',$3,true,false FROM cred`, org, encrypted, config)
	require.NoError(t, err)
	sources := &fakes.DataSourceService{FindBySlugFn: func(_ context.Context, o, ds slug.Slug) (*datasourcessvc.DataSource, error) {
		require.Equal(t, org, o)
		require.Equal(t, slug.Slug("objects"), ds)
		return &datasourcessvc.DataSource{Slug: ds, Type: model.DataSourceTypeS3, Config: config, IsActive: true}, nil
	}}
	credentials, err := credentialssvc.New(txm, credentialsrepo.New(), credentialCipher, credentialssvc.ProviderRegistry{}, clock.New(), true)
	require.NoError(t, err)
	factory := func(ctx context.Context, kind model.DataSourceType, config, credentials []byte) (connector.Connector, error) {
		return connector.NewConnector(transportsecurity.WithPlaintext(ctx), kind, config, credentials)
	}
	broker, err := executionbroker.New(txm, encryption, executionbroker.NewPollerDispatcher(), 5*time.Minute, factory, slog.New(slog.DiscardHandler), testutil.FakeOperationAdmission{})
	require.NoError(t, err)
	defer func() { require.NoError(t, broker.Close()) }()
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	require.NoError(t, broker.Start(workerCtx, 2))
	limits := queriessvc.ExecutionLimits{MaxFileBytes: size, QueryTimeout: 5 * time.Minute, ConnectorTimeout: 5 * time.Minute, MaxResultRows: 100, MaxResponseBytes: 1 << 20, ConnectorConcurrencyPerDataSource: 2, ConnectorConcurrencyQueueTimeout: time.Minute, ConnectorConcurrencyMaxQueued: 4, HTTPTimeout: 5 * time.Minute, HTTPMaxRedirects: 3, HTTPMaxRequestBytes: 1 << 20, HTTPMaxResponseBytes: 1 << 20, HTTPRetryAttempts: 1}
	queries, err := queriessvc.NewWithExecutionLimits(txm, queriesrepo.New(), sources, broker, credentials, clock.New(), limits, eventtest.NewProducer(nil))
	require.NoError(t, err)

	content := make([]byte, size)
	_, err = rand.Read(content)
	require.NoError(t, err)
	seed := bytes.NewReader(content)
	sourceHash := sha256.Sum256(content)
	independent := awss3.New(awss3.Options{Region: "us-east-1", BaseEndpoint: aws.String(endpoint), UsePathStyle: true, Credentials: awscredentials.NewStaticCredentialsProvider("braidplane", "braidplanepass", "")})
	prefix := org.String() + "/"
	sourceKey, destinationKey := prefix+"source.bin", prefix+"destination.bin"
	for _, key := range []string{sourceKey, destinationKey} {
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cleanupCancel()
			_, err := independent.DeleteObject(cleanupCtx, &awss3.DeleteObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: aws.String(key)})
			require.NoError(t, err)
		})
	}
	_, err = independent.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: aws.String(sourceKey), Body: seed, ContentLength: aws.Int64(size)})
	require.NoError(t, err)
	fileContract := &payload.Contract{Name: "native-file", Representation: model.ContractRepresentationFile}
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "native-transfer", Name: "Native file transfer"}, Steps: []pipelinespec.Step{
		{Kind: "query", Slug: "read", Query: &pipelinespec.QueryRef{DataSourceSlug: "objects", OperationType: "s3", OperationInline: map[string]any{"type": "s3", "operation": "read", "key": sourceKey, "format": "file"}, OutputContract: fileContract}},
		{Kind: "sink", Slug: "write", Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: "read"}, InputContract: fileContract, DataSourceSlug: "objects", OperationType: "s3", OperationInline: map[string]any{"type": "s3", "operation": "write", "key": destinationKey, "format": "file", "content": "{{input}}", "overwrite": true}, Config: map[string]any{"key": destinationKey, "format": "file", "overwrite": true}, IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent}},
	}}
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	materializePipelines(t, ctx, store, org, project, pipeline)
	spec, err := store.FindSpec(ctx, org, project, pipeline.Metadata.Slug)
	require.NoError(t, err)
	require.NotNil(t, spec.CurrentVersionID)
	runs := pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New())
	runner, err := pipelineruntime.NewRunner(fileTransferQueryAdapter{queries}, clock.New(), runs, sources, pipelineruntime.NewSinkRuntime(queries, testSinkEffectLedger(t, txm)))
	require.NoError(t, err)
	run, err := runner.Run(ctx, pipelineruntime.RunRequest{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: "file-transfer", Pipeline: pipeline, PipelineVersionID: *spec.CurrentVersionID})
	require.NoError(t, err)
	require.NotNil(t, run)
	require.Equal(t, "succeeded", run.Timeline.Status)
	got, err := independent.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: aws.String(destinationKey)})
	require.NoError(t, err)
	destinationHash := sha256.New()
	readBytes, err := io.CopyBuffer(destinationHash, got.Body, make([]byte, 64<<10))
	require.NoError(t, err)
	require.NoError(t, got.Body.Close())
	require.Equal(t, size, readBytes)
	require.Equal(t, sourceHash[:], destinationHash.Sum(nil))
	var maxChunk, envelopeBytes, plainBytes, chunks int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT COALESCE(max(octet_length(chunk.ciphertext)),0) FROM retained_file_streams stream JOIN retained_file_chunks chunk ON chunk.request_id=stream.request_id WHERE stream.organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org).Scan(&maxChunk))
	require.NoError(t, pool.QueryRow(ctx, `SELECT COALESCE(sum(size_bytes),0),COALESCE(sum(chunk_count),0) FROM retained_file_streams WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND sealed`, org).Scan(&plainBytes, &chunks))
	require.Equal(t, size, plainBytes)
	require.Equal(t, (size+filestream.ChunkBytes-1)/filestream.ChunkBytes, chunks)
	require.NoError(t, pool.QueryRow(ctx, `SELECT COALESCE(max(octet_length(request_ciphertext)+COALESCE(octet_length(result_ciphertext),0)),0) FROM execution_requests WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org).Scan(&envelopeBytes))
	require.LessOrEqual(t, maxChunk, int64(filestream.ChunkBytes+1024))
	require.Less(t, envelopeBytes, int64(1<<20))
	checkpoint, err := runs.GetCheckpoint(ctx, org, project, environment, "file-transfer")
	require.NoError(t, err)
	checkpointJSON, err := json.Marshal(checkpoint)
	require.NoError(t, err)
	require.Less(t, len(checkpointJSON), 1<<20)
	t.Run("checkpoint recovery refuses missing retained file input", func(t *testing.T) {
		var before, after int
		const countRequests = `SELECT count(*) FROM execution_requests WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`
		require.NoError(t, pool.QueryRow(ctx, countRequests, org).Scan(&before))
		_, err := runner.Replay(ctx, pipelineruntime.ReplayRequest{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, SourceRunSlug: "file-transfer", ReplayRunSlug: "refused", FromStep: "read", PipelineVersionID: *spec.CurrentVersionID})
		require.ErrorIs(t, err, pipelineruntime.ErrReplayUnsupportedMode)
		require.Contains(t, xerror.PublicDetail(err), "Retained file bytes are unavailable")
		require.NoError(t, pool.QueryRow(ctx, countRequests, org).Scan(&after))
		require.Equal(t, before, after, "recovery must not reread the source or redispatch the sink")
	})

	t.Run("effective byte limits preserve prior good output", func(t *testing.T) {
		// A request can choose a smaller allowance than the operator's ceiling.
		// Keep these boundary cases tiny while exercising the same full pipeline.
		boundedLimits := limits
		boundedLimits.MaxFileBytes = 16
		boundedQueries, err := queriessvc.NewWithExecutionLimits(txm, queriesrepo.New(), sources, broker, credentials, clock.New(), boundedLimits, eventtest.NewProducer(nil))
		require.NoError(t, err)
		boundedRunner, err := pipelineruntime.NewRunner(fileTransferQueryAdapter{boundedQueries}, clock.New(), runs, sources, pipelineruntime.NewSinkRuntime(boundedQueries, testSinkEffectLedger(t, txm)))
		require.NoError(t, err)
		for _, tc := range []struct {
			name string
			size int
		}{{"below", 15}, {"at", 16}, {"above", 17}} {
			t.Run(tc.name, func(t *testing.T) {
				input := bytes.Repeat([]byte("x"), tc.size)
				priorGood := []byte("prior-good")
				inputKey, outputKey := prefix+tc.name+"-source.bin", prefix+tc.name+"-destination.bin"
				for _, object := range []struct {
					key  string
					body []byte
				}{{inputKey, input}, {outputKey, priorGood}} {
					_, err := independent.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: aws.String(object.key), Body: bytes.NewReader(object.body)})
					require.NoError(t, err)
					t.Cleanup(func() {
						cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
						defer cleanupCancel()
						_, err := independent.DeleteObject(cleanupCtx, &awss3.DeleteObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: aws.String(object.key)})
						require.NoError(t, err)
					})
				}
				pipeline.Metadata.Slug = slug.Slug("limit-" + tc.name)
				pipeline.Steps[0].Query.OperationInline["key"] = inputKey
				pipeline.Steps[1].Sink.OperationInline["key"] = outputKey
				pipeline.Steps[1].Sink.Config["key"] = outputKey
				materializePipelines(t, ctx, store, org, project, pipeline)
				version, err := store.FindSpec(ctx, org, project, pipeline.Metadata.Slug)
				require.NoError(t, err)
				outcome, err := boundedRunner.Run(ctx, pipelineruntime.RunRequest{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: "limit-" + tc.name, Pipeline: pipeline, PipelineVersionID: *version.CurrentVersionID})
				expected := input
				if tc.size > 16 {
					require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
					require.Equal(t, "failed", outcome.Timeline.Status)
					expected = priorGood
				} else {
					require.NoError(t, err)
					require.Equal(t, "succeeded", outcome.Timeline.Status)
				}
				readback, err := independent.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: aws.String(outputKey)})
				require.NoError(t, err)
				body, err := io.ReadAll(io.LimitReader(readback.Body, 32))
				require.NoError(t, err)
				require.NoError(t, readback.Body.Close())
				require.Equal(t, expected, body)
			})
		}
		var incomplete, active int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM retained_file_streams WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND NOT sealed`, org).Scan(&incomplete))
		require.Zero(t, incomplete)
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM execution_requests WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND state='running'`, org).Scan(&active))
		require.Zero(t, active)
	})

	t.Run("BrokenStreamsNeverImplySuccessfulDelivery", func(t *testing.T) {
		faultSize := size
		var cutRead, loseCompletion, holdPart atomic.Bool
		var sourceReads, completions, partRequests atomic.Int32
		var holdPartNumber atomic.Int32
		holdPartNumber.Store(1)
		partBlocked, releasePart := make(chan struct{}, 1), make(chan struct{})
		proxy := httputil.NewSingleHostReverseProxy(fixtureURL) // #nosec G704 -- test entry restricts this operator-selected fixture to HTTP loopback.
		proxy.ModifyResponse = func(response *http.Response) error {
			if response.Request.Method == http.MethodGet && response.StatusCode == http.StatusOK {
				sourceReads.Add(1)
				if cutRead.Load() {
					// Keep the declared full length while cutting the actual socket body.
					response.Body = &cutFileResponseBody{Reader: io.LimitReader(response.Body, min(int64(2<<20), faultSize/2)), Closer: response.Body}
				}
			}
			if response.Request.Method == http.MethodPost && response.Request.URL.Query().Has("uploadId") && response.StatusCode == http.StatusOK {
				completions.Add(1)
				if loseCompletion.Load() {
					_, err := io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					if err != nil {
						return err
					}
					return io.ErrUnexpectedEOF
				}
			}
			return nil
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
		}
		faultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut && r.URL.Query().Has("partNumber") {
				partRequests.Add(1)
			}
			if holdPart.Load() && r.Method == http.MethodPut && r.URL.Query().Get("partNumber") == strconv.Itoa(int(holdPartNumber.Load())) {
				select {
				case partBlocked <- struct{}{}:
				default:
				}
				select {
				case <-r.Context().Done():
				case <-releasePart:
				}
				return
			}
			proxy.ServeHTTP(w, r)
		}))
		defer func() {
			close(releasePart)
			faultServer.Close()
		}()
		config, err = json.Marshal(s3connector.Config{Region: "us-east-1", Bucket: "braidplane-fixtures", Endpoint: faultServer.URL, ForcePathStyle: true, AllowPrivateHosts: true, TimeoutSeconds: 300})
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `UPDATE data_sources SET config=$2 WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug='objects'`, org, config)
		require.NoError(t, err)
		// At the standard sample sizes, the cut follows two persisted chunks.
		// Keep fault payloads small independently of the capacity sample.
		_, err = independent.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: aws.String(sourceKey), Body: io.NewSectionReader(seed, 0, faultSize), ContentLength: aws.Int64(faultSize)})
		require.NoError(t, err)
		priorGood := []byte("prior-good-after-interruption")
		_, err = independent.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: aws.String(destinationKey), Body: bytes.NewReader(priorGood)})
		require.NoError(t, err)
		pipeline.Metadata.Slug = "stream-faults"
		pipeline.Steps[0].Query.OperationInline["key"] = sourceKey
		pipeline.Steps[1].Sink.OperationInline["key"] = destinationKey
		pipeline.Steps[1].Sink.Config["key"] = destinationKey
		materializePipelines(t, ctx, store, org, project, pipeline)
		version, err := store.FindSpec(ctx, org, project, pipeline.Metadata.Slug)
		require.NoError(t, err)
		cutRead.Store(true)
		failed, err := runner.Run(ctx, pipelineruntime.RunRequest{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: "cut-source", Pipeline: pipeline, PipelineVersionID: *version.CurrentVersionID})
		require.Error(t, err)
		require.Equal(t, "failed", failed.Timeline.Status)
		require.EqualValues(t, 1, sourceReads.Load(), "a broken body must not silently restart the source")
		require.Zero(t, completions.Load(), "partial source input must never reach sink publication")
		readback, err := independent.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: aws.String(destinationKey)})
		require.NoError(t, err)
		body, err := io.ReadAll(readback.Body)
		require.NoError(t, err)
		require.NoError(t, readback.Body.Close())
		require.Equal(t, priorGood, body)
		var unsealed int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM retained_file_streams WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND NOT sealed`, org).Scan(&unsealed))
		require.Zero(t, unsealed, "failed source chunks must be discarded")

		cutRead.Store(false)
		loseCompletion.Store(true)
		uncertain, err := runner.Run(ctx, pipelineruntime.RunRequest{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: "lost-completion", Pipeline: pipeline, PipelineVersionID: *version.CurrentVersionID})
		require.Error(t, err)
		require.Equal(t, "failed", uncertain.Timeline.Status)
		require.EqualValues(t, 2, sourceReads.Load(), "an explicit new run reads the source again")
		require.EqualValues(t, 1, completions.Load(), "a missing completion acknowledgement must not trigger another publication")
		var effectState string
		require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM pipeline_sink_effects WHERE run_slug='lost-completion' AND project_environment_id=(SELECT pe.id FROM project_environments pe JOIN environments e ON e.id=pe.environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND p.slug=$2 AND e.slug=$3)`, org, project, environment).Scan(&effectState))
		require.Equal(t, "in_progress", effectState, "uncertain acknowledgement must never mark the effect successful")
		readback, err = independent.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: aws.String(destinationKey)})
		require.NoError(t, err)
		actualHash, expectedHash := sha256.New(), sha256.New()
		n, err := io.Copy(actualHash, readback.Body)
		require.NoError(t, err)
		require.NoError(t, readback.Body.Close())
		_, err = io.Copy(expectedHash, io.NewSectionReader(seed, 0, faultSize))
		require.NoError(t, err)
		require.Equal(t, faultSize, n)
		require.Equal(t, expectedHash.Sum(nil), actualHash.Sum(nil), "provider committed the complete object despite losing its acknowledgement")
		uploads, err := independent.ListMultipartUploads(ctx, &awss3.ListMultipartUploadsInput{Bucket: aws.String("braidplane-fixtures"), Prefix: aws.String(prefix)})
		require.NoError(t, err)
		require.Empty(t, uploads.Uploads, "a committed object must not leave an open multipart upload")
		_, err = runner.Replay(ctx, pipelineruntime.ReplayRequest{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, SourceRunSlug: "lost-completion", ReplayRunSlug: "lost-completion-replay", FromStep: "read", PipelineVersionID: *version.CurrentVersionID})
		require.ErrorIs(t, err, pipelineruntime.ErrReplayUnsupportedMode)
		require.EqualValues(t, 2, sourceReads.Load(), "refused recovery must not reread a potentially changed source")
		require.EqualValues(t, 1, completions.Load(), "refused recovery must not repeat an uncertain publication")

		loseCompletion.Store(false)
		holdPart.Store(true)
		priorParts := partRequests.Load()
		cancelCtx, cancelTransfer := context.WithCancel(ctx)
		defer cancelTransfer()
		cancelled := make(chan error, 1)
		go func() {
			_, err := runner.Run(cancelCtx, pipelineruntime.RunRequest{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: "cancel-blocked-upload", Pipeline: pipeline, PipelineVersionID: *version.CurrentVersionID})
			cancelled <- err
		}()
		select {
		case <-partBlocked:
		case <-time.After(20 * time.Second):
			t.Fatal("native upload never reached the blocked destination")
		}
		select {
		case err := <-cancelled:
			t.Fatalf("blocked destination unexpectedly completed: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		require.EqualValues(t, 1, completions.Load(), "backpressure must prevent publication")
		require.Equal(t, priorParts+1, partRequests.Load(), "a blocked part must prevent dispatch of subsequent parts")
		cancelTransfer()
		select {
		case err := <-cancelled:
			require.Error(t, err)
		case <-time.After(20 * time.Second):
			t.Fatal("cancelled native upload failed to stop")
		}
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			uploads, err := independent.ListMultipartUploads(ctx, &awss3.ListMultipartUploadsInput{Bucket: aws.String("braidplane-fixtures"), Prefix: aws.String(prefix)})
			assert.NoError(c, err)
			if uploads != nil {
				assert.Empty(c, uploads.Uploads, "cancelled upload must abort provider-side partial state")
			}
			var active int
			err = pool.QueryRow(ctx, `SELECT count(*) FROM execution_requests WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND state='running'`, org).Scan(&active)
			assert.NoError(c, err)
			assert.Zero(c, active)
		}, 20*time.Second, 50*time.Millisecond)
		require.EqualValues(t, 1, completions.Load(), "cancellation must not publish the blocked object")
		readback, err = independent.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: aws.String(destinationKey)})
		require.NoError(t, err)
		actualHash.Reset()
		n, err = io.Copy(actualHash, readback.Body)
		require.NoError(t, err)
		require.NoError(t, readback.Body.Close())
		require.Equal(t, faultSize, n)
		require.Equal(t, expectedHash.Sum(nil), actualHash.Sum(nil), "cancelled upload must preserve the prior complete object")

		t.Run("ExecutorLossRefusesRedispatch", func(t *testing.T) {
			// Stop in-process pollers so only the owned child can claim this write.
			stopWorker()
			worker := startNativeFileExecutor(ctx, t)
			t.Cleanup(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
				defer cleanupCancel()
				uploads, err := independent.ListMultipartUploads(cleanupCtx, &awss3.ListMultipartUploadsInput{Bucket: aws.String("braidplane-fixtures"), Prefix: aws.String(prefix)})
				require.NoError(t, err)
				for _, upload := range uploads.Uploads {
					_, err := independent.AbortMultipartUpload(cleanupCtx, &awss3.AbortMultipartUploadInput{Bucket: aws.String("braidplane-fixtures"), Key: upload.Key, UploadId: upload.UploadId})
					require.NoError(t, err)
				}
			})
			readsBefore, partsBefore := sourceReads.Load(), partRequests.Load()
			crashPart := int32(1)
			if faultSize > 8<<20 {
				crashPart = 2
			}
			holdPartNumber.Store(crashPart)
			crashCtx, cancelCrash := context.WithCancel(ctx)
			defer cancelCrash()
			crashed := make(chan error, 1)
			go func() {
				_, err := runner.Run(crashCtx, pipelineruntime.RunRequest{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: "executor-lost", Pipeline: pipeline, PipelineVersionID: *version.CurrentVersionID})
				crashed <- err
			}()
			select {
			case <-partBlocked:
			case <-time.After(time.Minute):
				t.Fatal("child executor did not reach the blocked upload")
			}
			var requestID string
			var leaseExpiry time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT id::text,lease_expires_at FROM execution_requests WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND state='running' AND kind='query'`, org).Scan(&requestID, &leaseExpiry))
			require.Equal(t, readsBefore+1, sourceReads.Load())
			require.Equal(t, partsBefore+crashPart, partRequests.Load())
			require.NoError(t, worker.Process.Kill())
			require.Error(t, worker.Wait(), "the owned executor must actually die")
			_, err := independent.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: aws.String(sourceKey), Body: bytes.NewReader([]byte("changed-after-executor-loss"))})
			require.NoError(t, err)
			startNativeFileExecutor(ctx, t)
			// Keep the real thirty-second claim lease: a replacement must wait,
			// then fence the uncertain query rather than redispatch its side effect.
			select {
			case err := <-crashed:
				require.ErrorIs(t, err, executionbroker.ErrAmbiguousExecution)
				require.False(t, time.Now().Before(leaseExpiry), "replacement must respect the original live claim lease")
			case <-time.After(45 * time.Second):
				t.Fatal("replacement executor failed to resolve the expired claim")
			}
			var state string
			var hasResult bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT state,result_ciphertext IS NOT NULL FROM execution_requests WHERE id=$1`, requestID).Scan(&state, &hasResult))
			require.Equal(t, "ambiguous", state)
			require.False(t, hasResult)
			timeline, err := runs.Get(ctx, org, project, environment, "executor-lost")
			require.NoError(t, err)
			require.Equal(t, "failed", timeline.Status, "executor loss must never persist a successful run")
			require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM pipeline_sink_effects WHERE run_slug='executor-lost' AND project_environment_id=(SELECT pe.id FROM project_environments pe JOIN environments e ON e.id=pe.environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND p.slug=$2 AND e.slug=$3)`, org, project, environment).Scan(&state))
			require.Equal(t, "in_progress", state)
			require.Equal(t, readsBefore+1, sourceReads.Load(), "replacement must not reread the source")
			require.Equal(t, partsBefore+crashPart, partRequests.Load(), "replacement must not upload another part")
			require.EqualValues(t, 1, completions.Load(), "replacement must not publish the interrupted write")
			_, err = runner.Replay(ctx, pipelineruntime.ReplayRequest{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, SourceRunSlug: "executor-lost", ReplayRunSlug: "executor-lost-replay", FromStep: "read", PipelineVersionID: *version.CurrentVersionID})
			require.ErrorIs(t, err, pipelineruntime.ErrReplayUnsupportedMode)
			require.Equal(t, readsBefore+1, sourceReads.Load())
			require.Equal(t, partsBefore+crashPart, partRequests.Load())
			readback, err := independent.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: aws.String(destinationKey)})
			require.NoError(t, err)
			actualHash := sha256.New()
			n, err := io.Copy(actualHash, readback.Body)
			require.NoError(t, err)
			require.NoError(t, readback.Body.Close())
			require.Equal(t, faultSize, n)
			require.Equal(t, expectedHash.Sum(nil), actualHash.Sum(nil))
			uploads, err := independent.ListMultipartUploads(ctx, &awss3.ListMultipartUploadsInput{Bucket: aws.String("braidplane-fixtures"), Prefix: aws.String(prefix)})
			require.NoError(t, err)
			require.Len(t, uploads.Uploads, 1, "process death cannot run the in-process abort")
			var uploadedPartBytes int64
			for _, upload := range uploads.Uploads {
				require.Equal(t, destinationKey, aws.ToString(upload.Key))
				parts, err := independent.ListParts(ctx, &awss3.ListPartsInput{Bucket: aws.String("braidplane-fixtures"), Key: upload.Key, UploadId: upload.UploadId})
				require.NoError(t, err)
				require.Len(t, parts.Parts, int(crashPart-1))
				for _, part := range parts.Parts {
					uploadedPartBytes += aws.ToInt64(part.Size)
				}
				_, err = independent.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{Bucket: aws.String("braidplane-fixtures"), Key: upload.Key, UploadId: upload.UploadId})
				require.NoError(t, err, "explicit fixture cleanup substitutes for provider lifecycle cleanup")
			}
			require.Equal(t, int64(crashPart-1)*(8<<20), uploadedPartBytes)
			uploads, err = independent.ListMultipartUploads(ctx, &awss3.ListMultipartUploadsInput{Bucket: aws.String("braidplane-fixtures"), Prefix: aws.String(prefix)})
			require.NoError(t, err)
			require.Empty(t, uploads.Uploads)
		})
	})
}

type cutFileResponseBody struct {
	io.Reader
	io.Closer
}

type fileTransferQueryAdapter struct{ queriessvc.Service }

func (a fileTransferQueryAdapter) Execute(ctx context.Context, org, ds, query slug.Slug, params map[string]any) (*result.ExecutionResult, error) {
	return a.Service.Execute(ctx, org, ds, query, model.SystemEventActor(), params, queriessvc.WithoutExecutionRecording())
}
