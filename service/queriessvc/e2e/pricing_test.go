package e2e

import (
	"context"
	"embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/postgres/pipelinesinkeffects"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

//go:embed testdata/pricing/*
var pricingFixtures embed.FS

func testPricingPatchAndDeterministicExport(t *testing.T, stack *testStack) {
	fixture := func(name string) string {
		data, err := pricingFixtures.ReadFile("testdata/pricing/" + name)
		require.NoError(t, err)
		return string(data)
	}
	var supplier atomic.Value
	supplier.Store(fixture("supplier.csv"))
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		_, _ = io.WriteString(w, supplier.Load().(string))
	}))
	t.Cleanup(source.Close)
	org, pgDS := createOrgAndPostgresDataSource(t, stack, "E2E Pricing Patch Export")
	httpDS := createHTTPDataSource(t, stack, org, source.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, connectorDBConnString())
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(ctx, fixture("catalog.sql"))
	require.NoError(t, err)
	defer func() {
		_, cleanupErr := db.Exec(context.Background(), "DELETE FROM qualification_pricing_catalog WHERE org_slug=$1", org.String())
		require.NoError(t, cleanupErr)
	}()

	orgParameter := &queriessvc.QueryParameter{Name: "org_slug", DataType: queriessvc.ParameterDataTypeText, IsRequired: true}
	fetch := createQuery(t, stack, org, httpDS, "Supplier CSV", map[string]any{"type": "http", "method": "GET", "path": "/prices.csv"}, 30, nil)
	patch := createQuery(t, stack, org, pgDS, "Pricing patch", map[string]any{"type": "postgres", "sql": fixture("patch.sql")}, 30, []*queriessvc.QueryParameter{orgParameter})
	export := createQuery(t, stack, org, pgDS, "Catalog as-of export", map[string]any{"type": "postgres", "sql": fixture("export.sql")}, 30, []*queriessvc.QueryParameter{orgParameter, {Name: "as_of", DataType: queriessvc.ParameterDataTypeText, IsRequired: true}})

	providerConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"), awsconfig.WithCredentialsProvider(awscredentials.NewStaticCredentialsProvider("braidplane", "braidplanepass", "")))
	require.NoError(t, err)
	provider := awss3.NewFromConfig(providerConfig, func(options *awss3.Options) {
		options.BaseEndpoint = aws.String(depsports.S3ConnectorEndpoint())
		options.UsePathStyle = true
	})
	bucket := fmt.Sprintf("pricing-%d", time.Now().UnixNano())
	_, err = provider.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := provider.DeleteObject(context.Background(), &awss3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("catalog.csv")})
		require.NoError(t, cleanupErr)
		_, cleanupErr = provider.DeleteBucket(context.Background(), &awss3.DeleteBucketInput{Bucket: aws.String(bucket)})
		require.NoError(t, cleanupErr)
	})
	endpoint, err := url.Parse(depsports.S3ConnectorEndpoint())
	require.NoError(t, err)
	var loseAcknowledgement atomic.Bool
	var recoveryFault atomic.Int32
	var recoveryPuts sync.Map
	proxy := httputil.NewSingleHostReverseProxy(endpoint)
	proxy.ModifyResponse = func(response *http.Response) error {
		lostCatalogAck := loseAcknowledgement.Load() && strings.HasSuffix(response.Request.URL.Path, "/catalog.csv")
		lostRecoveryAck := recoveryFault.Load() == 2 && strings.HasSuffix(response.Request.URL.Path, "/second.csv")
		if (lostCatalogAck || lostRecoveryAck) && response.Request.Method == http.MethodPut && response.StatusCode < 300 {
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusBadGateway) }
	outputServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/recovery/") {
			counter, _ := recoveryPuts.LoadOrStore(r.URL.Path, &atomic.Int64{})
			counter.(*atomic.Int64).Add(1)
			if recoveryFault.Load() == 1 && strings.HasSuffix(r.URL.Path, "/second.csv") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(outputServer.Close)
	config, err := json.Marshal(map[string]any{"endpoint": outputServer.URL, "region": "us-east-1", "bucket": bucket, "force_path_style": true, "allow_private_hosts": true})
	require.NoError(t, err)
	s3DS := must.Slug(fmt.Sprintf("pricing-s3-%d", time.Now().UnixNano()))
	_, err = stack.datasourcesSvc.Create(ctx, org, model.SystemEventActor(), &datasourcessvc.CreateParams{Name: "Pricing output", Slug: s3DS, Type: model.DataSourceTypeS3, ConfigJSON: config, Credentials: map[string]any{"access_key_id": "braidplane", "secret_access_key": "braidplanepass"}})
	require.NoError(t, err)
	publish := createQuery(t, stack, org, s3DS, "Publish pricing CSV", map[string]any{"type": "s3", "operation": "write", "key": "catalog.csv", "format": "csv", "overwrite": true}, 30, nil)

	ledger, err := pipelinesvc.NewSinkEffectLedger(postgres.NewTransactionManager(stack.pool), pipelinesinkeffects.New())
	require.NoError(t, err)
	runner, err := pipelineruntime.NewRunner(runnerQueryExecutorAdapter{executor: stack.queriesSvc}, clock.New(), pipelineruntime.NewRunStore(postgres.NewTransactionManager(stack.pool), stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()), stack.datasourcesSvc, pipelineruntime.NewSinkRuntime(stack.queriesSvc, ledger))
	require.NoError(t, err)
	replacements := strings.NewReplacer("${HTTP_DATA_SOURCE_SLUG}", httpDS.String(), "${HTTP_QUERY_SLUG}", fetch.String(), "${PG_DATA_SOURCE_SLUG}", pgDS.String(), "${PATCH_QUERY_SLUG}", patch.String(), "${EXPORT_QUERY_SLUG}", export.String(), "${ORG_SLUG}", org.String(), "${AS_OF}", "2026-10-10", "${S3_DATA_SOURCE_SLUG}", s3DS.String(), "${S3_QUERY_SLUG}", publish.String(), "${EXPORT_KEY}", "catalog.csv")
	run := func(name, specText string, succeeded bool) string {
		specText = replacements.Replace(strings.ReplaceAll(specText, "${PIPELINE_SLUG}", fmt.Sprintf("pricing-%s-%d", name, time.Now().UnixNano())))
		spec, err := pipelinevalidate.Load(name+".pipeline.yaml", []byte(specText))
		require.NoError(t, err)
		runSlug := fmt.Sprintf("pricing-run-%d", time.Now().UnixNano())
		request := newPipelineRunRequest(t, stack, org, runSlug, spec) //nolint:contextcheck // Immutable-version setup owns a separate bounded context.
		result, err := runner.Run(ctx, request)
		if succeeded {
			require.NoError(t, err)
			require.Equal(t, "succeeded", result.Metadata.Status)
		} else {
			require.Error(t, err)
		}
		return runSlug
	}
	state := func() string {
		var state string
		err := db.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_array(sku,title,product_url,price::text,effective_from::text,effective_to::text,snapshot_date::text,category) ORDER BY sku,effective_from)::text,'[]') FROM qualification_pricing_catalog WHERE org_slug=$1`, org.String()).Scan(&state)
		require.NoError(t, err)
		return state
	}
	readObject := func() string {
		object, err := provider.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("catalog.csv")})
		require.NoError(t, err)
		defer func() { require.NoError(t, object.Body.Close()) }()
		data, err := io.ReadAll(object.Body)
		require.NoError(t, err)
		return string(data)
	}
	importSpec, exportSpec := fixture("import.pipeline.yaml"), fixture("export.pipeline.yaml")
	_, err = db.Exec(ctx, `INSERT INTO qualification_pricing_catalog
		(org_slug,sku,title,product_url,price,effective_from,effective_to,snapshot_date,category)
		VALUES ($1,'0003','retained','https://example.test/p?id=0003',9.99,'2026-10-01','2026-11-01','2026-10-09','standard')`, org.String())
	require.NoError(t, err)
	run("import", importSpec, true)
	baseline := state()
	require.JSONEq(t, `[
		["0001","zero","https://example.test/p?id=0001","0.00","2026-10-10","2026-11-01","2026-10-10","standard"],
		["0002","Żółć, \"duża\"\nlinia","https://example.test/p?id=0002&variant=a%2Bb","2.68","2026-10-10","2026-11-01","2026-10-10","promo"],
		["0003","retained","https://example.test/p?id=0003","9.99","2026-10-01","2026-11-01","2026-10-09","standard"]
	]`, baseline)
	run("export", exportSpec, true)
	want := "sku,title,product_url,price,effective_from,effective_to,category\n0001,zero,https://example.test/p?id=0001,0.00,2026-10-10,2026-11-01,standard\n0002,\"Żółć, \"\"duża\"\"\nlinia\",https://example.test/p?id=0002&variant=a%2Bb,2.68,2026-10-10,2026-11-01,promo\n0003,retained,https://example.test/p?id=0003,9.99,2026-10-01,2026-11-01,standard\n"
	require.Equal(t, want, readObject())
	records, err := csv.NewReader(strings.NewReader(readObject())).ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 4)
	run("repeat-import", importSpec, true)
	require.Equal(t, baseline, state())
	run("repeat-export", exportSpec, true)
	require.Equal(t, want, readObject())

	t.Run("renamed-supplier-price", func(t *testing.T) {
		require.Equal(t, 2, strings.Count(importSpec, "list_price"))
		renamedFeed := strings.Replace(fixture("supplier.csv"), "list_price", "supplier_price", 1)
		supplier.Store(renamedFeed)
		run("renamed-old-contract", importSpec, false)
		require.Equal(t, baseline, state())
		require.Equal(t, want, readObject())
		run("renamed-new-contract", strings.ReplaceAll(importSpec, "list_price", "supplier_price"), true)
		require.Equal(t, baseline, state())
		run("renamed-export", exportSpec, true)
		require.Equal(t, want, readObject())
	})

	for _, test := range []struct{ name, body string }{
		{"blank-price", strings.Replace(fixture("supplier.csv"), " 2.675 ", " ", 1)},
		{"invalid-url", strings.Replace(fixture("supplier.csv"), "https://example.test/p?id=0001", "relative/path", 1)},
		{"ambiguous-date", strings.Replace(fixture("supplier.csv"), "10.10.2026", "10/10/2026", 1)},
		{"invalid-price", strings.Replace(fixture("supplier.csv"), " 2.675 ", "$2.67", 1)},
		{"duplicate-key", strings.Replace(fixture("supplier.csv"), "0001,zero", "0002,zero", 1)},
		{"impossible-date", strings.Replace(fixture("supplier.csv"), "10.10.2026", "31.02.2026", 1)},
		{"stale-patch", strings.ReplaceAll(fixture("supplier.csv"), `10.10.2026," PROMO "`, `09.10.2026," PROMO "`)},
		{"mixed-snapshots-ascending", strings.Replace(fixture("supplier.csv"), `10.10.2026," PROMO "`, `11.10.2026," PROMO "`, 1)},
		{"mixed-snapshots-descending", strings.Replace(fixture("supplier.csv"), `10.10.2026," "`, `11.10.2026," "`, 1)},
		{"zero-length-interval", strings.Replace(fixture("supplier.csv"), "10.10.2026,01.11.2026", "01.11.2026,01.11.2026", 1)},
		{"reversed-interval", strings.Replace(fixture("supplier.csv"), "10.10.2026,01.11.2026", "01.12.2026,01.11.2026", 1)},
		{"overlap", strings.Replace(strings.Replace(fixture("supplier.csv"), "10.10.2026,01.11.2026", "11.10.2026,02.11.2026", 1), ",0,10.10.2026", ",1,10.10.2026", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			supplier.Store(test.body)
			run(test.name, importSpec, false)
			require.Equal(t, baseline, state())
			require.Equal(t, want, readObject())
		})
	}
	supplier.Store(strings.SplitN(fixture("supplier.csv"), "\n", 2)[0] + "\n")
	run("empty-patch", importSpec, true)
	require.Equal(t, baseline, state())
	// Omitted SKUs survive; future intervals are stored and excluded from the as-of export.
	supplier.Store("supplier_sku,title,product_url,list_price,valid_from,valid_to,source_date,tag\n0001,future,https://example.test/p?id=0001,1.10,01.11.2026,01.12.2026,11.10.2026,promo\n")
	run("future-patch", importSpec, true)
	require.Contains(t, state(), "future")
	require.Contains(t, state(), "0002")
	run("future-export", exportSpec, true)
	require.Equal(t, want, readObject())
	// A failed export is a separate delivery failure after the catalog commit.
	run("failed-export", strings.Replace(exportSpec, "${EXPORT_KEY}", "../invalid.csv", 1), false)
	require.Contains(t, state(), "future")
	require.Equal(t, want, readObject())
	run("recovered-export", exportSpec, true)
	require.Equal(t, want, readObject())
	loseAcknowledgement.Store(true)
	lostRun := run("lost-acknowledgement", strings.Replace(exportSpec, "${AS_OF}", "2020-01-01", 1), false)
	require.Equal(t, strings.SplitN(want, "\n", 2)[0]+"\n", readObject())
	var effectState string
	err = stack.pool.QueryRow(ctx, `SELECT effects.state FROM pipeline_sink_effects effects
 JOIN project_environments environments ON environments.id = effects.project_environment_id
 JOIN projects ON projects.id = environments.project_id
 JOIN organizations ON organizations.id = projects.organization_id
 WHERE organizations.slug = $1 AND effects.run_slug = $2 AND effects.sink_step_slug = 'publish-export'`, org.String(), lostRun).Scan(&effectState)
	require.NoError(t, err)
	require.Equal(t, "in_progress", effectState)
	loseAcknowledgement.Store(false)
	run("lost-ack-recovery", exportSpec, true)
	require.Equal(t, want, readObject())
	run("header-only-export", strings.Replace(exportSpec, "${AS_OF}", "2020-01-01", 1), true)
	require.Equal(t, strings.SplitN(want, "\n", 2)[0]+"\n", readObject())

	for _, fault := range []struct {
		name string
		mode int32
	}{
		{name: "sibling-provider-failure", mode: 1},
		{name: "lost-acknowledgement-recovery", mode: 2},
	} {
		t.Run(fault.name, func(t *testing.T) {
			spec, err := pipelinevalidate.Load("recovery.pipeline.yaml", []byte(replacements.Replace(strings.ReplaceAll(exportSpec, "${PIPELINE_SLUG}", fmt.Sprintf("pricing-recovery-%d", time.Now().UnixNano())))))
			require.NoError(t, err)
			first := spec.Steps[len(spec.Steps)-1]
			first.Slug = "publish-first"
			firstSink := *first.Sink
			firstSink.Config = map[string]any{"key": "recovery/{{runtime.sink.replay_identity}}/first.csv", "format": "csv", "overwrite": true}
			first.Sink = &firstSink
			second := spec.Steps[len(spec.Steps)-1]
			second.Slug = "publish-second"
			second.Sink.Config = map[string]any{"key": "recovery/{{runtime.sink.replay_identity}}/second.csv", "format": "csv", "overwrite": true}
			spec.Steps = append(spec.Steps[:len(spec.Steps)-1], first, second)
			spec.Steps[0].Query.Parameters["as_of"] = "{{runtime.input.as_of}}"
			require.NoError(t, pipelinevalidate.Validate(spec))
			originalSlug := fmt.Sprintf("pricing-recovery-run-%d", time.Now().UnixNano())
			request := newPipelineRunRequest(t, stack, org, originalSlug, spec) //nolint:contextcheck // Immutable-version setup owns a separate bounded context.
			request.RuntimeInput = map[string]any{"as_of": "2026-10-10"}
			firstKey, secondKey := "recovery/"+originalSlug+"/first.csv", "recovery/"+originalSlug+"/second.csv"
			t.Cleanup(func() {
				recoveryFault.Store(0)
				for _, key := range []string{firstKey, secondKey} {
					_, cleanupErr := provider.DeleteObject(context.WithoutCancel(ctx), &awss3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
					require.NoError(t, cleanupErr)
				}
			})
			read := func(key string) string {
				object, readErr := provider.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
				require.NoError(t, readErr)
				defer func() { require.NoError(t, object.Body.Close()) }()
				body, readErr := io.ReadAll(object.Body)
				require.NoError(t, readErr)
				return string(body)
			}
			puts := func(key string) int64 {
				counter, found := recoveryPuts.Load("/" + bucket + "/" + key)
				require.True(t, found)
				return counter.(*atomic.Int64).Load()
			}
			completedRecord := func() string {
				var record string
				readErr := stack.pool.QueryRow(ctx, `SELECT row_to_json(effects)::text FROM pipeline_sink_effects effects
 JOIN project_environments environments ON environments.id=effects.project_environment_id
 JOIN projects ON projects.id=environments.project_id
 JOIN organizations ON organizations.id=projects.organization_id
 WHERE organizations.slug=$1 AND effects.run_slug=$2 AND effects.sink_step_slug='publish-first'`, org.String(), originalSlug).Scan(&record)
				require.NoError(t, readErr)
				return record
			}
			recoveryFault.Store(fault.mode)
			original, runErr := runner.Run(ctx, request)
			require.Error(t, runErr)
			require.NotNil(t, original)
			require.Equal(t, "confirmed", original.Timeline.Snapshots[2].Input.SinkDeliveryOutcome)
			require.Equal(t, "unknown", original.Timeline.Snapshots[3].Input.SinkDeliveryOutcome)
			require.Equal(t, "in_progress", original.Timeline.Snapshots[3].Input.SinkEffectState.String())
			require.Equal(t, want, read(firstKey))
			require.Equal(t, int64(1), puts(firstKey))
			if fault.mode == 2 {
				require.Equal(t, want, read(secondKey), "independent provider read proves the lost-ack write happened")
			} else {
				_, readErr := provider.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(secondKey)})
				var missing *awss3types.NoSuchKey
				require.ErrorAs(t, readErr, &missing)
			}
			firstRecord := completedRecord()
			secondPuts := puts(secondKey)
			recoveryFault.Store(0)
			// A newer catalog row must not enter recovery's retained CSV payload.
			_, err = db.Exec(ctx, `INSERT INTO qualification_pricing_catalog
 (org_slug,sku,title,product_url,price,effective_from,effective_to,snapshot_date,category)
 VALUES ($1,'changed-after-failure','new source','https://example.test/new',99,'2026-10-10','2026-11-01','2026-10-11','standard')`, org.String())
			require.NoError(t, err)
			defer func() {
				_, cleanupErr := db.Exec(context.WithoutCancel(ctx), "DELETE FROM qualification_pricing_catalog WHERE org_slug=$1 AND sku='changed-after-failure'", org.String())
				require.NoError(t, cleanupErr)
			}()
			for attempt := 0; attempt < 2; attempt++ {
				recovered, recoverErr := runner.Recover(ctx, pipelineruntime.ReplayRequest{
					OrgSlug: org, ProjectSlug: request.ProjectSlug, EnvironmentSlug: request.EnvironmentSlug,
					SourceRunSlug: originalSlug, ReplayRunSlug: fmt.Sprintf("pricing-recovered-%d", time.Now().UnixNano()),
					InputMode: pipelineruntime.ReplayInputModePinned, PipelineVersionID: request.PipelineVersionID,
				})
				require.NoError(t, recoverErr)
				require.Equal(t, "succeeded", recovered.Metadata.Status)
				require.Equal(t, request.PipelineVersionID, recovered.Metadata.PipelineVersionID)
				require.Equal(t, originalSlug, *recovered.Metadata.ReplayOfRunSlug)
				require.Equal(t, "skip_ledger_succeeded", recovered.Timeline.Snapshots[2].Input.SinkReplayDecision)
				if attempt == 1 {
					require.Equal(t, "skip_ledger_succeeded", recovered.Timeline.Snapshots[3].Input.SinkReplayDecision)
				}
				require.Equal(t, firstRecord, completedRecord(), "completed sibling's entire ledger record remains unchanged")
				require.Equal(t, want, read(firstKey))
				require.Equal(t, want, read(secondKey))
				require.Equal(t, int64(1), puts(firstKey))
				require.Equal(t, secondPuts+1, puts(secondKey), "only the unknown effect is replayed, once")
			}
		})
	}

	t.Run("concurrent-overlap", func(t *testing.T) {
		first, err := db.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = first.Rollback(context.WithoutCancel(ctx)) }()
		second, err := db.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = second.Rollback(context.WithoutCancel(ctx)) }()
		insert := `INSERT INTO qualification_pricing_catalog
   (org_slug,sku,title,product_url,price,effective_from,effective_to,snapshot_date,category)
   VALUES ($1,'concurrent','fixture','https://example.test',1,$2::date,$3::date,'2026-10-11','standard')`
		_, err = first.Exec(ctx, insert, org.String(), "2026-11-01", "2026-12-01")
		require.NoError(t, err)
		var pid int
		require.NoError(t, second.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid))
		finished := make(chan error, 1)
		go func() { _, err := second.Exec(ctx, insert, org.String(), "2026-11-15", "2026-12-15"); finished <- err }()
		require.Eventually(t, func() bool {
			var blocked bool
			err := db.QueryRow(ctx, "SELECT COALESCE(wait_event = 'advisory', false) FROM pg_stat_activity WHERE pid=$1", pid).Scan(&blocked)
			return err == nil && blocked
		}, 5*time.Second, 10*time.Millisecond)
		require.NoError(t, first.Commit(ctx))
		require.ErrorContains(t, <-finished, "pricing effective intervals overlap")
		var count int
		require.NoError(t, db.QueryRow(ctx, "SELECT count(*) FROM qualification_pricing_catalog WHERE org_slug=$1 AND sku='concurrent'", org.String()).Scan(&count))
		require.Equal(t, 1, count)
	})
}
