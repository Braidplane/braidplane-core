//go:build portable_executor

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/pipelinefixture"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/apikeys"
	"github.com/Braidplane/braidplane-core/postgres/environments"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/postgres/permissions"
	"github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/roles"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/apikeyssvc"
	"github.com/Braidplane/braidplane-core/service/environmentssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
)

// The protected path makes a successful query prove that its scoped credential reached the provider.
const canaryHTTPServer = `$SIG{CHLD}="IGNORE";
$s=IO::Socket::INET->new(LocalPort=>8080,Listen=>10,ReuseAddr=>1) or die;
while($c=$s->accept){
 next if fork;
 $request=<$c>; $protected=defined($request) && $request =~ m{ /protected(?:-rows)? }; $auth="";
 while(<$c>){$auth=$1 if /^Authorization:\s*Bearer\s+(\S+)/i; last if /^\r?\n$/}
 open(F,">>","/tmp/calls"); print F "call\n"; close F;
 sleep 120 if -e "/tmp/slow";
 $status=$protected && $auth ne "synthetic-route-only" ? "401 Unauthorized" : "200 OK";
 $body=$request =~ m{ /protected-rows } ? '[{"portable":true}]' : '{"portable":true}';
 print $c "HTTP/1.1 $status\r\nContent-Type: application/json\r\nContent-Length: ".length($body)."\r\nConnection: close\r\n\r\n$body";
 close $c; exit;
}`

type apiSnapshot struct {
	status int
	body   []byte
}

func verifyAuthenticatedRouting(t *testing.T, ctx context.Context, pool *pgxpool.Pool, apiURL string, provider testcontainers.Container, startWorker func(*testing.T)) {
	t.Helper()
	txm := postgres.NewTransactionManager(pool)
	admission := admissionsvc.New(txm, organizations.NewRepository())
	err := admission.InitializeOrganization(ctx, "portable")
	require.NoError(t, err)
	require.NoError(t, admission.DataSourceConnected(ctx, "portable", "source", time.Now()))
	enqueue, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	events, err := workerproducer.NewEventProducer(queue.NewEnqueuer(enqueue), clock.New())
	require.NoError(t, err)
	roleService := rolessvc.New(txm, roles.NewRepository(), permissions.NewRepository(), events)
	require.NoError(t, roleService.CreateDefaultRoles(ctx, "portable"))
	_, err = pool.Exec(ctx, `INSERT INTO users(slug,email,full_name,role,verified_at) VALUES('portable-user','portable@example.invalid','Portable Fixture','user',clock_timestamp());
 INSERT INTO organization_members(organization_id,user_id,role_id) SELECT o.id,u.id,r.id FROM organizations o JOIN roles r ON r.organization_id=o.id AND r.slug='admin' CROSS JOIN users u WHERE o.slug='portable' AND u.slug='portable-user'`)
	require.NoError(t, err)
	key, err := apikeyssvc.New(txm, apikeys.NewRepository(), roleService, events).CreateAPIKey(ctx, "portable", model.UserEventActor(slug.Slug("portable-user")), model.NewAPIKey{Name: "Portable fixture", Slug: "portable-fixture", Role: "admin"})
	require.NoError(t, err)
	request := func(t *testing.T, method, path, body, token string) apiSnapshot {
		t.Helper()
		response, err := routingRequest(ctx, apiURL, method, path, body, token)
		require.NoError(t, err)
		if response.status >= 400 {
			var diagnostic api.ErrorResponse
			if json.Unmarshal(response.body, &diagnostic) == nil {
				t.Logf("HTTP routing diagnostic: status=%d code=%s", response.status, diagnostic.Error.Code)
			}
		}
		return response
	}
	t.Run("queued pipeline connector and transform through executor", func(t *testing.T) {
		require.NoError(t, environmentssvc.New(txm, environments.NewRepository(), events).CreateDefaultEnvironments(ctx, "portable"))
		require.Equal(t, http.StatusCreated, request(t, http.MethodPost, "/organizations/portable/projects", `{"name":"Portable project","slug":"portable-project"}`, key.Token).status)
		require.Equal(t, http.StatusCreated, request(t, http.MethodPost, "/organizations/portable/data-sources", `{"name":"Pipeline HTTP","slug":"pipeline-http","type":"http","config":{"base_url":"http://provider:8080","allow_private_hosts":true},"credentials":{"bearer_token":"synthetic-route-only"}}`, key.Token).status)
		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "portable-pipeline", Name: "Portable pipeline"},
			Steps: []pipelinespec.Step{
				{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "pipeline-http", OperationType: "http", OperationInline: map[string]any{"type": "http", "method": "GET", "path": "/protected-rows"}, OutputContract: &payload.Contract{Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeArray, Fields: []payload.ContractField{{Name: "portable", Type: model.ContractFieldTypeValueBoolean, Required: true}}}}},
				{Kind: "transform", Slug: "count", Transform: &pipelinespec.TransformSpec{SQL: "SELECT COUNT(*) AS total FROM input_rows", OutputKind: model.TransformOutputKindRows, Inputs: map[string]pipelinespec.StepInput{"input_rows": {FromStep: "fetch"}}}},
			},
		}
		store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelines.New(), odcscontracts.NewRepository())
		require.NoError(t, err)
		require.NoError(t, pipelinefixture.Materialize(ctx, store, "portable", "portable-project", pipeline))
		require.Equal(t, http.StatusCreated, request(t, http.MethodPost, "/organizations/portable/projects/portable-project/pipelines/portable-pipeline/data-source-refs", `{"slug":"pipeline-http","name":"Pipeline HTTP","data_source_type":"http","required_capabilities":["pipeline_source"]}`, key.Token).status)
		require.Equal(t, http.StatusOK, request(t, http.MethodPut, "/organizations/portable/projects/portable-project/pipelines/portable-pipeline/environments/dev/data-source-bindings/pipeline-http", `{"data_source_slug":"pipeline-http"}`, key.Token).status)
		spec, err := store.FindSpec(ctx, "portable", "portable-project", pipeline.Metadata.Slug)
		require.NoError(t, err)
		require.NotNil(t, spec.CurrentVersionSpecHash)
		body, err := json.Marshal(api.PipelineDeploymentRequest{SpecHash: *spec.CurrentVersionSpecHash})
		require.NoError(t, err)
		path := "/organizations/portable/projects/portable-project/environments/dev/pipelines/portable-pipeline"
		require.Equal(t, http.StatusOK, request(t, http.MethodPost, path+":deploy", string(body), key.Token).status)
		var before int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM execution_requests`).Scan(&before))
		require.Equal(t, http.StatusAccepted, request(t, http.MethodPost, path+":execute", `{"run_slug":"portable-run"}`, key.Token).status)
		var runState, jobState string
		require.NoError(t, pool.QueryRow(ctx, `SELECT run.status,job.state::text FROM pipeline_runs run JOIN river_job job ON job.id=run.worker_job_id WHERE run.run_slug='portable-run'`).Scan(&runState, &jobState))
		require.Equal(t, "queued", runState)
		require.Equal(t, "available", jobState)
		startWorker(t)
		require.Eventually(t, func() bool {
			err := pool.QueryRow(ctx, `SELECT run.status,job.state::text FROM pipeline_runs run JOIN river_job job ON job.id=run.worker_job_id WHERE run.run_slug='portable-run'`).Scan(&runState, &jobState)
			return err == nil && runState == "succeeded" && jobState == "completed"
		}, 15*time.Second, 50*time.Millisecond)
		var after int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM execution_requests`).Scan(&after))
		require.Equal(t, before+2, after)
		var kinds []string
		var succeeded, encrypted bool
		require.NoError(t, pool.QueryRow(ctx, `SELECT array_agg(kind ORDER BY created_at),bool_and(state='succeeded'),bool_and(position('synthetic-route-only'::bytea IN request_ciphertext)=0) FROM (SELECT * FROM execution_requests ORDER BY created_at DESC LIMIT 2) requests`).Scan(&kinds, &succeeded, &encrypted))
		require.Equal(t, []string{"query", "transform"}, kinds)
		require.True(t, succeeded)
		require.True(t, encrypted)
		var raw []byte
		require.NoError(t, pool.QueryRow(ctx, `SELECT timeline FROM pipeline_runs WHERE run_slug='portable-run'`).Scan(&raw))
		var timeline pipelinerun.RunTimeline
		require.NoError(t, json.Unmarshal(raw, &timeline))
		require.Len(t, timeline.Snapshots, 2)
		output := timeline.Snapshots[1].Output
		require.NotNil(t, output)
		require.NotNil(t, output.Details)
		require.NotNil(t, output.Details.RowCount)
		require.Equal(t, 1, *output.Details.RowCount)
		require.Equal(t, []string{"total"}, output.Details.ColumnNames)
	})
	connectionBody := `{"type":"http","config":{"base_url":"http://provider:8080","allow_private_hosts":true},"credentials":{}}`
	connectionPath := "/organizations/portable/data-sources/test-connection"
	t.Run("authenticated HTTP connection routing", func(t *testing.T) {
		var before, after int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM execution_requests`).Scan(&before))
		require.Equal(t, http.StatusUnauthorized, request(t, http.MethodPost, connectionPath, connectionBody, "").status)
		require.Equal(t, http.StatusForbidden, request(t, http.MethodPost, "/organizations/foreign/data-sources/test-connection", connectionBody, key.Token).status)
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM execution_requests`).Scan(&after))
		require.Equal(t, before, after, "refused HTTP access must not dispatch an executor request")
		require.Equal(t, http.StatusNoContent, request(t, http.MethodPost, connectionPath, connectionBody, key.Token).status)
		var state, kind string
		require.NoError(t, pool.QueryRow(ctx, `SELECT kind,state FROM execution_requests ORDER BY created_at DESC LIMIT 1`).Scan(&kind, &state))
		require.Equal(t, "connection_test", kind)
		require.Equal(t, "succeeded", state)
	})
	t.Run("HTTP connection caller waits for durable failure", func(t *testing.T) {
		docker(t, ctx, "exec", provider.GetContainerID(), "touch", "/tmp/slow")
		type outcome struct {
			response apiSnapshot
			err      error
		}
		finished := make(chan outcome, 1)
		go func() {
			response, err := routingRequest(ctx, apiURL, http.MethodPost, connectionPath, `{"type":"http","config":{"base_url":"http://provider:8080","allow_private_hosts":true,"timeout_seconds":1},"credentials":{}}`, key.Token)
			finished <- outcome{response, err}
		}()
		require.Eventually(t, func() bool {
			var n int
			err := pool.QueryRow(ctx, `SELECT count(*) FROM execution_requests WHERE state='running'`).Scan(&n)
			return err == nil && n == 1
		}, 5*time.Second, 10*time.Millisecond)
		select {
		case <-finished:
			t.Fatal("HTTP caller returned before executor completion")
		default:
		}
		select {
		case result := <-finished:
			require.NoError(t, result.err)
			require.Equal(t, http.StatusUnprocessableEntity, result.response.status)
		case <-time.After(10 * time.Second):
			t.Fatal("HTTP caller did not receive a bounded executor failure")
		}
		docker(t, ctx, "exec", provider.GetContainerID(), "rm", "/tmp/slow")
		var state string
		require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM execution_requests ORDER BY created_at DESC LIMIT 1`).Scan(&state))
		require.Equal(t, "failed", state)
	})
	t.Run("authenticated immediate query through executor", func(t *testing.T) {
		response := request(t, http.MethodPost, "/organizations/portable/data-sources", `{"name":"HTTP fixture","slug":"http-fixture","type":"http","config":{"base_url":"http://provider:8080","allow_private_hosts":true},"credentials":{"bearer_token":"synthetic-route-only"}}`, key.Token)
		require.Equal(t, http.StatusCreated, response.status)
		require.False(t, strings.Contains(string(response.body), "synthetic-route-only"))
		response = request(t, http.MethodPost, "/organizations/portable/data-sources/http-fixture/queries", `{"name":"Protected read","slug":"protected-read","query_statement":{"type":"http","method":"GET","path":"/protected"}}`, key.Token)
		require.Equal(t, http.StatusCreated, response.status)
		response = request(t, http.MethodPost, "/organizations/portable/data-sources/http-fixture/queries/protected-read/execute", `{"parameters":{}}`, key.Token)
		require.Equal(t, http.StatusOK, response.status)
		var result api.HTTPExecutionResult
		require.NoError(t, json.Unmarshal(response.body, &result))
		require.True(t, result.Success)
		require.Equal(t, http.StatusOK, result.Payload.StatusCode)
		require.Equal(t, api.HTTPExecutionResultResultTypeHttp, result.ResultType)
		require.Equal(t, map[string]any{"portable": true}, result.Payload.Body)
		var kind, state string
		require.NoError(t, pool.QueryRow(ctx, `SELECT kind,state FROM execution_requests ORDER BY created_at DESC LIMIT 1`).Scan(&kind, &state))
		require.Equal(t, "query", kind)
		require.Equal(t, "succeeded", state)
	})
}

func routingRequest(ctx context.Context, apiURL, method, path, body, token string) (apiSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, method, apiURL+"/api"+path, strings.NewReader(body))
	if err != nil {
		return apiSnapshot{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return apiSnapshot{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	return apiSnapshot{response.StatusCode, data}, err
}
