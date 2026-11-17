package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/depsports"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	s3connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	"github.com/Braidplane/braidplane-core/service/executionbroker"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// This opt-in rehearsal reuses the package's broker, executor and multipart fault fixtures.
func qualifyLargeSQLFaults(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org, project, environment slug.Slug, pipeline *pipelinespec.Pipeline, version int64, runner *pipelineruntime.Runner, runs pipelinerun.RunStore, oracle *awss3.Client, key string, config *[]byte, stopWorker context.CancelFunc, expectedBytes int64, expectedDigest, qualificationOutput string) {
	t.Helper()
	originalConfig := append([]byte(nil), (*config)...)
	defer func() {
		*config = originalConfig
		_, err := pool.Exec(context.WithoutCancel(ctx), `UPDATE data_sources SET config=$2 WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug='objects'`, org, originalConfig)
		require.NoError(t, err)
	}()
	assertPrior := func() {
		object, err := oracle.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: &key})
		require.NoError(t, err)
		digest := sha256.New()
		n, err := io.Copy(digest, object.Body)
		require.NoError(t, err)
		require.NoError(t, object.Body.Close())
		require.Equal(t, expectedBytes, n)
		require.Equal(t, expectedDigest, hex.EncodeToString(digest.Sum(nil)))
	}
	run := func(ctx context.Context, name string) error {
		_, err := runner.Run(ctx, pipelineruntime.RunRequest{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: name, Pipeline: pipeline, PipelineVersionID: version})
		return err
	}
	t.Run("retained storage capacity error", func(t *testing.T) {
		// Inject PostgreSQL disk_full after real chunks; never exhaust the shared host disk.
		_, err := pool.Exec(ctx, `CREATE SEQUENCE qualification_disk_full_hits; CREATE FUNCTION qualification_disk_full() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF (SELECT count(*) FROM retained_file_chunks WHERE request_id=NEW.request_id)>=2 THEN PERFORM nextval('qualification_disk_full_hits'); RAISE EXCEPTION 'synthetic retained storage capacity exhausted' USING ERRCODE='53100'; END IF; RETURN NEW; END $$; CREATE TRIGGER qualification_disk_full BEFORE INSERT ON retained_file_chunks FOR EACH ROW EXECUTE FUNCTION qualification_disk_full()`)
		require.NoError(t, err)
		defer func() {
			_, err := pool.Exec(context.WithoutCancel(ctx), `DROP TRIGGER qualification_disk_full ON retained_file_chunks; DROP FUNCTION qualification_disk_full(); DROP SEQUENCE qualification_disk_full_hits`)
			require.NoError(t, err)
		}()
		require.Error(t, run(ctx, "large-storage-exhausted"))
		var faultObserved bool
		require.NoError(t, pool.QueryRow(ctx, `SELECT is_called FROM qualification_disk_full_hits`).Scan(&faultObserved))
		require.True(t, faultObserved, "the source must reach the injected capacity boundary")
		timeline, err := runs.Get(ctx, org, project, environment, "large-storage-exhausted")
		require.NoError(t, err)
		require.Equal(t, "failed", timeline.Status)
		var unsealed, active int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM retained_file_streams WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND NOT sealed`, org).Scan(&unsealed))
		require.Zero(t, unsealed)
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM execution_requests WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND state IN ('pending','running')`, org).Scan(&active))
		require.Zero(t, active)
		assertPrior()
	})
	upstream, err := url.Parse(depsports.S3ConnectorEndpoint())
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	blocked := make(chan struct{}, 1)
	release := make(chan struct{})
	var parts, completions atomic.Int32
	var hold atomic.Bool
	hold.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Query().Has("uploadId") {
			completions.Add(1)
		}
		if r.Method == http.MethodPut && r.URL.Query().Has("partNumber") {
			parts.Add(1)
			if hold.Load() && r.URL.Query().Get("partNumber") == "2" {
				select {
				case blocked <- struct{}{}:
				default:
				}
				select {
				case <-r.Context().Done():
				case <-release:
				}
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	defer func() { close(release); server.Close() }()
	*config, err = json.Marshal(s3connector.Config{Region: "us-east-1", Bucket: "braidplane-fixtures", Endpoint: server.URL, ForcePathStyle: true, AllowPrivateHosts: true, TimeoutSeconds: 300})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE data_sources SET config=$2 WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug='objects'`, org, *config)
	require.NoError(t, err)
	waitBlocked := func(done <-chan error) {
		select {
		case <-blocked:
		case err := <-done:
			t.Fatalf("write ended before fault boundary: %v", err)
		case <-time.After(5 * time.Minute):
			t.Fatal("second multipart part never reached fault boundary")
		}
	}
	t.Run("backpressure and cancellation", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- run(cancelCtx, "large-cancelled") }()
		waitBlocked(done)
		before := parts.Load()
		select {
		case err := <-done:
			t.Fatalf("blocked destination returned: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
		require.Equal(t, before, parts.Load())
		require.Zero(t, completions.Load())
		cancel()
		select {
		case err := <-done:
			require.Error(t, err)
		case <-time.After(20 * time.Second):
			t.Fatal("cancelled transfer did not release")
		}
		require.Eventually(t, func() bool {
			uploads, err := oracle.ListMultipartUploads(ctx, &awss3.ListMultipartUploadsInput{Bucket: aws.String("braidplane-fixtures"), Prefix: &key})
			var active int
			dbErr := pool.QueryRow(ctx, `SELECT count(*) FROM execution_requests WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND state IN ('pending','running')`, org).Scan(&active)
			return err == nil && len(uploads.Uploads) == 0 && dbErr == nil && active == 0
		}, 20*time.Second, 100*time.Millisecond)
		assertPrior()
	})
	t.Run("executor loss and supported file recovery", func(t *testing.T) {
		stopWorker()
		child := startNativeFileExecutor(ctx, t)
		done := make(chan error, 1)
		go func() { done <- run(ctx, "large-executor-lost") }()
		waitBlocked(done)
		var plainBytes, cipherBytes, relationBytes int64
		require.NoError(t, pool.QueryRow(ctx, `SELECT COALESCE(sum(size_bytes),0) FROM retained_file_streams WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org).Scan(&plainBytes))
		require.NoError(t, pool.QueryRow(ctx, `SELECT COALESCE(sum(octet_length(c.ciphertext)),0) FROM retained_file_chunks c JOIN retained_file_streams f ON f.request_id=c.request_id WHERE f.organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org).Scan(&cipherBytes))
		require.NoError(t, pool.QueryRow(ctx, `SELECT pg_total_relation_size('retained_file_chunks')`).Scan(&relationBytes))
		t.Logf("retained at crash boundary plaintext_bytes=%d ciphertext_bytes=%d chunk_relation_bytes=%d (includes indexes/TOAST; excludes WAL/backups)", plainBytes, cipherBytes, relationBytes)
		killed := time.Now()
		require.NoError(t, child.Process.Kill())
		require.Error(t, child.Wait())
		_, err := pool.Exec(ctx, `UPDATE large_recovery_source SET changed=true`)
		require.NoError(t, err)
		startNativeFileExecutor(ctx, t)
		select {
		case err := <-done:
			require.ErrorIs(t, err, executionbroker.ErrAmbiguousExecution)
		case <-time.After(45 * time.Second):
			t.Fatal("lost executor not fenced")
		}
		t.Logf("large executor fenced elapsed_since_kill=%s parts=%d completions=%d", time.Since(killed), parts.Load(), completions.Load())
		timeline, err := runs.Get(ctx, org, project, environment, "large-executor-lost")
		require.NoError(t, err)
		require.Equal(t, "failed", timeline.Status)

		original := *timeline
		// Advance the old source lease's retention only. The private checkpoint
		// pin must preserve bytes while the replacement uses its own live claim.
		_, err = pool.Exec(ctx, `UPDATE retained_file_streams f SET expires_at=clock_timestamp()-interval '1 second' FROM execution_requests r JOIN data_sources d ON d.id=r.data_source_id WHERE f.request_id=r.id AND r.organization_id=(SELECT id FROM organizations WHERE slug=$1) AND d.slug='sql-source'`, org)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `UPDATE execution_requests r SET expires_at=clock_timestamp()-interval '1 second' FROM data_sources d WHERE d.id=r.data_source_id AND r.organization_id=(SELECT id FROM organizations WHERE slug=$1) AND d.slug='sql-source'`, org)
		require.NoError(t, err)
		err = expireFileInputs(ctx, postgres.NewTransactionManager(pool))
		require.NoError(t, err)
		uploads, err := oracle.ListMultipartUploads(ctx, &awss3.ListMultipartUploadsInput{Bucket: aws.String("braidplane-fixtures"), Prefix: &key})
		require.NoError(t, err)
		require.Len(t, uploads.Uploads, 1)
		for _, upload := range uploads.Uploads {
			uploaded, err := oracle.ListParts(ctx, &awss3.ListPartsInput{Bucket: aws.String("braidplane-fixtures"), Key: upload.Key, UploadId: upload.UploadId})
			require.NoError(t, err)
			require.Len(t, uploaded.Parts, 1)
			require.EqualValues(t, 8<<20, aws.ToInt64(uploaded.Parts[0].Size))
			_, err = oracle.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{Bucket: aws.String("braidplane-fixtures"), Key: upload.Key, UploadId: upload.UploadId})
			require.NoError(t, err)
		}
		hold.Store(false)
		var sourceRequestsBefore, sourceRequestsAfter int
		const sourceRequests = `SELECT count(*) FROM execution_requests r JOIN data_sources d ON d.id=r.data_source_id WHERE r.organization_id=(SELECT id FROM organizations WHERE slug=$1) AND d.slug='sql-source'`
		require.NoError(t, pool.QueryRow(ctx, sourceRequests, org).Scan(&sourceRequestsBefore))
		repairStarted := time.Now()
		for index, run := range []string{"large-recovered", "large-recovered-again"} {
			priorParts := parts.Load()
			outcome, err := runner.Recover(ctx, pipelineruntime.ReplayRequest{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, SourceRunSlug: "large-executor-lost", ReplayRunSlug: run, PipelineVersionID: version})
			require.NoError(t, err)
			require.Equal(t, "succeeded", outcome.Metadata.Status)
			if index == 1 {
				require.Equal(t, priorParts, parts.Load(), "confirmed recovery must not upload again")
			}
			assertPrior()
		}
		t.Logf("supported large recovery and confirmed-repeat elapsed=%s parts=%d completions=%d", time.Since(repairStarted), parts.Load(), completions.Load())
		require.EqualValues(t, 1, completions.Load())
		uploads, err = oracle.ListMultipartUploads(ctx, &awss3.ListMultipartUploadsInput{Bucket: aws.String("braidplane-fixtures"), Prefix: &key})
		require.NoError(t, err)
		require.Empty(t, uploads.Uploads)
		require.NoError(t, pool.QueryRow(ctx, sourceRequests, org).Scan(&sourceRequestsAfter))
		require.Equal(t, sourceRequestsBefore, sourceRequestsAfter, "recovery must never reread changed SQL")
		retained, err := runs.Get(ctx, org, project, environment, "large-executor-lost")
		require.NoError(t, err)
		require.Equal(t, original, *retained, "recovery must preserve original failed history")
		object, err := oracle.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String("braidplane-fixtures"), Key: &key})
		require.NoError(t, err)
		// #nosec G703 -- operator-selected synthetic evidence path, not request input.
		output, err := os.Create(qualificationOutput + ".recovered")
		require.NoError(t, err)
		_, err = io.Copy(output, object.Body)
		require.NoError(t, err)
		require.NoError(t, object.Body.Close())
		require.NoError(t, output.Close())
		// Expired recovery associations cannot turn metadata into replay authority.
		_, err = pool.Exec(ctx, `UPDATE pipeline_checkpoint_files pin SET expires_at=clock_timestamp()-interval '1 second' FROM pipeline_checkpoint pc WHERE pc.id=pin.checkpoint_id AND pc.run_slug='large-executor-lost' AND pc.project_environment_id=(SELECT pe.id FROM project_environments pe JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id JOIN environments e ON e.id=pe.environment_id WHERE o.slug=$1 AND p.slug=$2 AND e.slug=$3)`, org, project, environment)
		require.NoError(t, err)
		_, err = runner.Recover(ctx, pipelineruntime.ReplayRequest{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, SourceRunSlug: "large-executor-lost", ReplayRunSlug: "large-expired-refused", PipelineVersionID: version})
		require.ErrorIs(t, err, pipelineruntime.ErrReplayUnsupportedMode)
		t.Logf("expired file recovery refusal=%s", xerror.PublicDetail(err))
	})
	_, err = pool.Exec(ctx, `UPDATE pipeline_checkpoint_files pin SET expires_at=clock_timestamp()-interval '1 second' FROM pipeline_checkpoint pc JOIN project_environments pe ON pe.id=pc.project_environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE pc.id=pin.checkpoint_id AND o.slug=$1`, org)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE retained_file_streams SET expires_at=clock_timestamp()-interval '1 second' WHERE request_id IN (SELECT r.id FROM execution_requests r JOIN data_sources d ON d.id=r.data_source_id WHERE r.organization_id=(SELECT id FROM organizations WHERE slug=$1) AND d.slug='sql-source')`, org)
	require.NoError(t, err)
	err = expireFileInputs(ctx, postgres.NewTransactionManager(pool))
	require.NoError(t, err)
	var remaining int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM retained_file_streams f JOIN execution_requests r ON r.id=f.request_id JOIN data_sources d ON d.id=r.data_source_id WHERE r.organization_id=(SELECT id FROM organizations WHERE slug=$1) AND d.slug='sql-source'`, org).Scan(&remaining))
	require.Zero(t, remaining, "maintenance must delete expired SQL retained bytes")
	t.Log("expired SQL retained files removed through normal maintenance query; expiry advanced only in fixture")
	_, err = pool.Exec(ctx, `UPDATE large_recovery_source SET changed=false`)
	require.NoError(t, err)
}
