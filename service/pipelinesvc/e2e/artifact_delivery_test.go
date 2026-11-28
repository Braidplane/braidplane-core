package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinesinkeffects"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/ftp"
	"github.com/Braidplane/braidplane-core/service/executionbroker"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

type artifactFixtureQuery struct {
	request queriessvc.ConnectorExecutionRequest
}

func (q artifactFixtureQuery) ResolveFTPArtifactWrite(context.Context, slug.Slug, slug.Slug, contractplan.OperationContent) (queriessvc.ConnectorExecutionRequest, error) {
	return q.request, nil
}

type artifactFixtureBroker struct {
	prepares, publishes, renames int
	publish                      func(context.Context, executionbroker.FTPArtifactRequest) error
}

func (b *artifactFixtureBroker) PrepareFTPArtifact(_ context.Context, req executionbroker.FTPArtifactRequest) (ftp.ArtifactEvidence, error) {
	b.prepares++
	var rows []any
	if err := json.Unmarshal(req.Content, &rows); err != nil {
		return ftp.ArtifactEvidence{}, err
	}
	evidence, _, err := ftp.PrepareArtifactCSV(ftp.Config{}, req.Directory, "orders.csv", req.DeliveryID, 1024, rows, filedata.ParseCSVOptions{})
	return evidence, err
}

func (b *artifactFixtureBroker) PublishFTPArtifact(ctx context.Context, req executionbroker.FTPArtifactRequest) error {
	b.publishes++
	return b.publish(ctx, req)
}

type artifactFixtureTx struct {
	postgres.TransactionManager
	confirmMode string
	calls       int
}

var errArtifactFixtureCommit = xerror.New(xerror.KindInternal, "FIXTURE_COMMIT", "fixture confirmation commit failed")

func (m *artifactFixtureTx) RunInTx(ctx context.Context, f func(context.Context, postgres.DBTX) error) error {
	m.calls++
	if m.calls != 2 || m.confirmMode == "" {
		return m.TransactionManager.RunInTx(ctx, f)
	}
	err := m.TransactionManager.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := f(ctx, tx); err != nil {
			return err
		}
		if m.confirmMode == "rollback" {
			return errArtifactFixtureCommit
		}
		return nil
	})
	if err != nil {
		return err
	}
	return errArtifactFixtureCommit
}

func TestArtifactDelivery(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	pool := openIntegrationPool(t)
	ctx := t.Context()
	org := slug.Slug(fmt.Sprintf("artifact-delivery-%d", time.Now().UnixNano()))
	const project, env, source, query = slug.Slug("orders"), slug.Slug("test"), slug.Slug("ftp-source"), slug.Slug("publish")
	const statement = `{"operation":"write","format":"csv","path":"orders.csv"}`
	pin, err := json.Marshal(map[string]any{"statement": statement, "row_limit": 10})
	require.NoError(t, err)
	config := []byte(`{"host":"ftp.example.test","port":21}`)
	digest, err := connector.PublicConfigDigest(model.DataSourceTypeFTP, config)
	require.NoError(t, err)
	var orgID, projectID, envID, sourceID, versionID int64
	var credentialRevision, queryRevision time.Time
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
		q AS (INSERT INTO queries (data_source_id,slug,name,data) SELECT id,'publish','Publish',$3 FROM source RETURNING updated_at),
		spec AS (INSERT INTO pipeline_specs (project_id,slug,name,spec_jsonb,spec_hash,source_path,source_ref,last_applied_commit)
			SELECT id,'orders','Orders','{}','fixture-spec','orders.yaml','refs/heads/main',repeat('a',40) FROM project RETURNING project_id,slug),
		v AS (INSERT INTO pipeline_versions (project_id,pipeline_slug,spec_jsonb,spec_hash)
			SELECT project_id,slug,'{}','fixture-version' FROM spec RETURNING id)
		SELECT org.id,project.id,scope.id,source.id,v.id,cred.updated_at,q.updated_at FROM org,project,scope,source,v,cred,q
	`, org, config, pin).Scan(&orgID, &projectID, &envID, &sourceID, &versionID, &credentialRevision, &queryRevision))
	credential := "ftp-credential/" + credentialRevision.UTC().Format(time.RFC3339Nano)
	qualification := pipelinesvc.QualifiedFTPStorage{
		Organization: org, Project: project, Environment: env, Source: source,
		EnvironmentID: envID, BindingRevision: "approved-revision", ConfigDigest: digest, CredentialReference: credential, Directory: "reserved",
	}
	request := artifactFixtureQuery{queriessvc.ConnectorExecutionRequest{
		OrganizationSlug: org, DataSourceSlug: source,
		DataSourceType: model.DataSourceTypeFTP, PinnedConfigDigest: digest, CredentialReference: credential,
		ConfigJSON: config, StatementJSON: json.RawMessage(statement), SinkExecution: true,
	}}
	manual := pipelinerun.RunSourceSummary{Type: model.PipelineRunSourceTypeManual, Entrypoint: model.PipelineRunSourceEntrypointUI}
	api := pipelinerun.RunSourceSummary{Type: model.PipelineRunSourceTypeAPI, Entrypoint: model.PipelineRunSourceEntrypointAPI}

	for _, tc := range []struct {
		name, confirm, outcome       string
		source                       pipelinerun.RunSourceSummary
		empty, unqualified, stalePin bool
		publishes, renames           int
	}{
		{name: "manual", source: manual, outcome: "confirmed", publishes: 1, renames: 1},
		{name: "api", source: api, outcome: "confirmed", publishes: 1, renames: 1},
		{name: "before-dispatch", source: manual, outcome: "failed", publishes: 1},
		{name: "submission-commit-unknown", source: manual, outcome: "unknown", publishes: 1},
		{name: "rename-reply-lost", source: manual, outcome: "unknown", publishes: 1, renames: 1},
		{name: "commit-rollback", source: manual, confirm: "rollback", outcome: "unknown", publishes: 1, renames: 1},
		{name: "commit-reply-lost", source: manual, confirm: "lost", outcome: "confirmed", publishes: 1, renames: 1},
		{name: "empty", source: api, empty: true, outcome: "no_op"},
		{name: "unqualified", source: manual, unqualified: true, outcome: "failed"},
		{name: "intent-refused", source: manual, stalePin: true, outcome: "failed"},
		{name: "schedule", source: pipelinerun.RunSourceSummary{Type: model.PipelineRunSourceTypeSchedule}, outcome: "failed"},
		{name: "webhook", source: pipelinerun.RunSourceSummary{Type: model.PipelineRunSourceTypeWebhook}, outcome: "failed"},
		{name: "completion", source: pipelinerun.RunSourceSummary{Type: model.PipelineRunSourceTypeCompletion}, outcome: "failed"},
		{name: "retry", source: pipelinerun.RunSourceSummary{Type: model.PipelineRunSourceTypeAPI, Entrypoint: model.PipelineRunSourceEntrypointAPI, AttemptNumber: 2}, outcome: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
				INSERT INTO pipeline_runs (project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,
					pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,deployment_id,deployment_resolution_digest)
				VALUES ($1,$2,$3,'fixture-version','v1','orders',$4,'test','running',0,'sink','{}',9,'resolution')
			`, projectID, envID, versionID, tc.name)
			require.NoError(t, err)
			txm := &artifactFixtureTx{TransactionManager: postgres.NewTransactionManager(pool), confirmMode: tc.confirm}
			broker := &artifactFixtureBroker{}
			broker.publish = func(ctx context.Context, req executionbroker.FTPArtifactRequest) error {
				// A phase A test exercises the actual rename and encrypted broker. Here
				// inject its terminal outcomes at the producer boundary and use real SQL.
				var claimed, intended bool
				require.NoError(t, pool.QueryRow(ctx, `SELECT
					EXISTS(SELECT 1 FROM pipeline_sink_effects WHERE project_environment_id=$1 AND run_slug=$2 AND state='in_progress'),
					EXISTS(SELECT 1 FROM pipeline_artifact_deliveries WHERE project_environment_id=$1 AND producer_attempt_slug=$2 AND id=$3)
				`, envID, tc.name, req.DeliveryID).Scan(&claimed, &intended))
				require.True(t, claimed && intended)
				switch tc.name {
				case "before-dispatch":
					return xerror.ExecutionUnavailable()
				case "submission-commit-unknown":
					return executionbroker.ErrFTPArtifactSubmissionUnknown.With(executionbroker.ErrAmbiguousExecution)
				}
				broker.renames++
				state := "succeeded"
				if tc.name == "rename-reply-lost" {
					state = "failed"
				}
				require.NoError(t, txm.TransactionManager.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
					_, err := tx.Exec(ctx, `UPDATE pipeline_artifact_deliveries
						SET submitted_at=clock_timestamp(), submission_token=$2,permit_spent_at=clock_timestamp() WHERE id=$1`,
						req.DeliveryID, req.PublishRequestID)
					if err != nil {
						return err
					}
					_, err = tx.Exec(ctx, `INSERT INTO execution_requests
						(id,organization_id,data_source_id,kind,state,request_ciphertext,result_ciphertext,deadline_at,expires_at,completed_at)
						VALUES ($1,$2,$3,'ftp_artifact_publish',$4,'fixture','fixture',clock_timestamp()+interval '1 minute',clock_timestamp()+interval '1 hour',clock_timestamp())
					`, req.PublishRequestID, orgID, sourceID, state)
					return err
				}))
				if state == "failed" {
					return executionbroker.ErrAmbiguousExecution
				}
				return nil
			}
			producer := pipelinesvc.NewFTPArtifactProducer(txm, pipelinesinkeffects.New(), request, broker,
				func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (pipelinesvc.QualifiedFTPStorage, error) {
					q := qualification
					if tc.unqualified {
						q.BindingRevision = "another-revision"
					}
					return q, nil
				})
			runtime := pipelineruntime.NewSinkRuntimeWithArtifactProducer(nil, nil, producer)
			rows := []map[string]any{{"id": "102"}}
			if tc.empty {
				rows = nil
			}
			selectedPin := pin
			if tc.stalePin {
				selectedPin, err = json.Marshal(map[string]any{"statement": statement, "row_limit": 11})
				require.NoError(t, err)
			}
			input := pipelineruntime.SinkRuntimeInput{
				ArtifactDelivery: true, RunSource: tc.source, ExclusiveEffectClaim: true, OrgSlug: org, ProjectSlug: project, EnvironmentSlug: env,
				RunSlug: tc.name, ReplayIdentity: tc.name, SinkStepSlug: "sink", SinkBackend: model.SinkBackendFTP,
				SinkDataSourceSlug: source, SinkOperationRef: query, SinkConfig: map[string]any{"path": "reserved/orders.csv"},
				IdempotencyClass: pipelinespec.SinkIdempotencyClassUnsafe, IdempotencyKey: "effect", BindingRevision: "approved-revision",
				OperationRevision: queryRevision.Format(time.RFC3339Nano), OperationSnapshot: &contractplan.OperationContent{Data: selectedPin},
				EmptyInputPolicy: "skip", SourceResult: pipelinerun.NewStepResult(result.NewSQLResult("postgres", &result.SQLResult{
					Rows: rows, RowCount: len(rows), ColumnNames: []string{"id"},
				}, 0, nil)),
			}
			err = runtime.Execute(ctx, &input)
			if tc.outcome == "confirmed" || tc.outcome == "no_op" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, tc.outcome, input.DeliveryOutcome)
			if tc.outcome == "unknown" {
				require.Equal(t, "artifact_publication_uncertain", input.ReplayDecision)
			}
			switch {
			case tc.publishes > 0:
				require.Error(t, runtime.Execute(ctx, &input))
			case tc.stalePin:
				require.Equal(t, 1, broker.prepares)
			default:
				require.Zero(t, broker.prepares)
			}
			require.Equal(t, tc.publishes, broker.publishes)
			require.Equal(t, tc.renames, broker.renames)

			var receipts, effects, confirmed, succeeded int
			require.NoError(t, pool.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM pipeline_artifact_deliveries WHERE project_environment_id=$1 AND producer_attempt_slug=$2),
				(SELECT count(*) FROM pipeline_sink_effects WHERE project_environment_id=$1 AND run_slug=$2),
				(SELECT count(*) FROM pipeline_artifact_deliveries WHERE project_environment_id=$1 AND producer_attempt_slug=$2 AND confirmed_at IS NOT NULL),
				(SELECT count(*) FROM pipeline_sink_effects WHERE project_environment_id=$1 AND run_slug=$2 AND state='succeeded')
			`, envID, tc.name).Scan(&receipts, &effects, &confirmed, &succeeded))
			require.Equal(t, tc.publishes, receipts)
			require.Equal(t, receipts, effects)
			require.Equal(t, confirmed, succeeded)
			if tc.outcome == "confirmed" {
				require.Equal(t, 1, confirmed)
			} else {
				require.Zero(t, confirmed)
			}
		})
	}
}
