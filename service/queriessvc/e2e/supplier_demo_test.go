package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/testutil/supplierfixture"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/pipelinesinkeffects"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

func testSupplierPriceDeskUnionAll(t *testing.T, stack *testStack) {
	fixture := func(name string) []byte {
		data, err := supplierfixture.Files.ReadFile("data/" + name)
		require.NoError(t, err)
		return data
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	org, pgDS := createOrgAndPostgresDataSource(t, stack, "Supplier Price Desk UNION ALL")
	db, err := pgxpool.New(ctx, connectorDBConnString())
	require.NoError(t, err)
	t.Cleanup(db.Close)
	_, err = db.Exec(ctx, strings.Replace(string(fixture("catalog.sql")), "CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ", 1))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := db.Exec(context.Background(), "DELETE FROM supplier_price_offers WHERE org_slug=$1", org.String())
		require.NoError(t, err)
	})
	// Keep the rehearsal's product_code -> sku drift isolated from shared fixtures.
	pipelineYAML := bytes.Replace(fixture("supplier-price-desk.pipeline.yaml"), []byte("name: sku"), []byte("name: product_code"), 1)
	pipelineYAML = bytes.Replace(pipelineYAML, []byte("TRIM(sku)"), []byte("TRIM(product_code)"), 1)
	pipeline, err := pipelinevalidate.Load("supplier-price-desk.pipeline.yaml", pipelineYAML)
	require.NoError(t, err)
	providerConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"), awsconfig.WithCredentialsProvider(awscredentials.NewStaticCredentialsProvider("braidplane", "braidplanepass", "")))
	require.NoError(t, err)
	provider := awss3.NewFromConfig(providerConfig, func(options *awss3.Options) {
		options.BaseEndpoint = aws.String(depsports.S3ConnectorEndpoint())
		options.UsePathStyle = true
	})
	bucket := fmt.Sprintf("supplier-demo-%d", time.Now().UnixNano())
	_, err = provider.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	var keys []string
	t.Cleanup(func() {
		for _, key := range keys {
			_, err := provider.DeleteObject(context.Background(), &awss3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
			require.NoError(t, err)
		}
		_, err := provider.DeleteBucket(context.Background(), &awss3.DeleteBucketInput{Bucket: aws.String(bucket)})
		require.NoError(t, err)
	})
	put := func(name string, body []byte) {
		_, err := provider.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("pricing-demo/" + name + ".csv"), Body: bytes.NewReader(body)})
		require.NoError(t, err)
	}
	for _, step := range pipeline.Steps {
		if step.Query == nil {
			continue
		}
		name := strings.TrimPrefix(step.Slug.String(), "read-")
		body := fixture("sources/" + name + ".csv")
		if name == "northstar" {
			body = bytes.Replace(body, []byte("sku,"), []byte("product_code,"), 1)
		}
		put(name, body)
		keys = append(keys, "pricing-demo/"+name+".csv")
		config, err := json.Marshal(map[string]any{"endpoint": depsports.S3ConnectorEndpoint(), "region": "us-east-1", "bucket": bucket, "force_path_style": true, "allow_private_hosts": true})
		require.NoError(t, err)
		source := must.Slug(fmt.Sprintf("supplier-%s-%d", name, time.Now().UnixNano()))
		_, err = stack.datasourcesSvc.Create(ctx, org, model.SystemEventActor(), &datasourcessvc.CreateParams{Name: name, Slug: source, Type: model.DataSourceTypeS3, ConfigJSON: config, Credentials: map[string]any{"access_key_id": "braidplane", "secret_access_key": "braidplanepass"}})
		require.NoError(t, err)
		var statement map[string]any
		require.NoError(t, json.Unmarshal(fixture("operations/read-"+name+".json"), &statement))
		step.Query.DataSourceSlug = source
		step.Query.OperationRef = createQuery(t, stack, org, source, "Read "+name, statement, 30, nil)
	}
	var statement map[string]any
	require.NoError(t, json.Unmarshal(fixture("operations/upsert-offers.json"), &statement))
	sink := pipeline.Steps[len(pipeline.Steps)-1].Sink
	sink.DataSourceSlug = pgDS
	sink.OperationRef = createQuery(t, stack, org, pgDS, "Upsert combined supplier offers", statement, 30, []*queriessvc.QueryParameter{{Name: "org_slug", DataType: queriessvc.ParameterDataTypeText, IsRequired: true}})
	sink.Config["org_slug"] = org.String()
	txm := postgres.NewTransactionManager(stack.pool)
	store, err := pipelinesvc.NewPipelineStore(stack.pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	materializePipelines(t, ctx, store, org, defaultProjectSlug, pipeline)
	version, err := store.FindSpec(ctx, org, defaultProjectSlug, pipeline.Metadata.Slug)
	require.NoError(t, err)
	require.NotNil(t, version.CurrentVersionID)
	ledger, err := pipelinesvc.NewSinkEffectLedger(txm, pipelinesinkeffects.New())
	require.NoError(t, err)
	var candidateMu sync.Mutex
	var candidates []model.NotificationCandidate
	producer := testutil.FakeRunSucceededNotifier{CandidateFn: func(_ context.Context, _ postgres.DBTX, candidate model.NotificationCandidate) error {
		candidateMu.Lock()
		defer candidateMu.Unlock()
		candidates = append(candidates, candidate)
		return nil
	}}
	runner, err := pipelineruntime.NewRunner(runnerQueryExecutorAdapter{executor: stack.queriesSvc}, clock.New(), pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), producer, pipelineruns.New()), stack.datasourcesSvc, pipelineruntime.NewSinkRuntime(stack.queriesSvc, ledger))
	require.NoError(t, err)
	run := func() error {
		out, err := runner.Run(ctx, pipelineruntime.RunRequest{OrgSlug: org, ProjectSlug: defaultProjectSlug, EnvironmentSlug: "prod", RunSlug: fmt.Sprintf("supplier-demo-%d", time.Now().UnixNano()), Pipeline: pipeline, PipelineVersionID: *version.CurrentVersionID})
		if err != nil {
			return err
		}
		require.Equal(t, "succeeded", out.Metadata.Status)
		require.Len(t, out.Timeline.Snapshots, 22)
		return nil
	}
	verify := func(snapshot string) {
		rows, err := db.Query(ctx, "SELECT provider,product_code,title,currency,pack_units,quoted_price::text,unit_price::text,source_date::text FROM supplier_price_offers WHERE org_slug=$1", org.String())
		require.NoError(t, err)
		defer rows.Close()
		actual := make(map[string]map[string]any)
		for rows.Next() {
			var provider, product, title, currency, quoted, unit, date string
			var units int
			require.NoError(t, rows.Scan(&provider, &product, &title, &currency, &units, &quoted, &unit, &date))
			actual[provider+"/"+product] = map[string]any{"provider": provider, "product_code": product, "title": title, "currency": currency, "pack_units": units, "quoted_price": quoted, "unit_price": unit, "source_date": date}
		}
		require.NoError(t, rows.Err())
		require.Len(t, actual, 30)
		var expected []map[string]any
		decoder := json.NewDecoder(bytes.NewReader(fixture("expected-" + snapshot + ".json")))
		decoder.UseNumber()
		require.NoError(t, decoder.Decode(&expected))
		for _, row := range expected {
			got := actual[fmt.Sprint(row["provider"], "/", row["product_code"])]
			for field, value := range row {
				require.Equal(t, fmt.Sprint(value), fmt.Sprint(got[field]), "%s/%s %s", row["provider"], row["product_code"], field)
			}
		}
	}
	require.NoError(t, run())
	verify("baseline")
	require.NoError(t, run())
	verify("baseline")
	put("northstar", fixture("sources/northstar.csv"))
	require.Error(t, run())
	verify("baseline")
	candidateMu.Lock()
	var contractCandidate *model.NotificationCandidate
	for i := range candidates {
		if candidates[i].NotificationType == model.NotificationTypePipelineContractFailure {
			contractCandidate = &candidates[i]
			break
		}
	}
	candidateMu.Unlock()
	require.NotNil(t, contractCandidate)
	require.Equal(t, "product_code", contractCandidate.SafeMetadata["field"])
	require.Equal(t, "required", contractCandidate.SafeMetadata["rule"])
	require.Equal(t, "read-northstar", contractCandidate.SourceRefs["step_slug"])
	require.Equal(t, "supplier-price-desk", contractCandidate.SourceRefs["pipeline_slug"])
	require.NotEmpty(t, contractCandidate.SourceRefs["run_slug"])
	require.False(t, contractCandidate.ObservedAt.IsZero())
	candidateJSON, err := json.Marshal(contractCandidate)
	require.NoError(t, err)
	require.NotContains(t, string(candidateJSON), "Trail Bottle 750 ml")
	put("northstar", bytes.Replace(fixture("sources/northstar.csv"), []byte("sku,"), []byte("product_code,"), 1))
	put("cedar", bytes.Replace(fixture("sources/cedar.csv"), []byte("17.40"), []byte("invalid"), 1))
	require.Error(t, run())
	verify("baseline")
	put("cedar", fixture("updates/cedar.csv"))
	require.NoError(t, run())
	verify("update")
}
