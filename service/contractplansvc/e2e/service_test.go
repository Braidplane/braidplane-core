package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/containerfixture"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/testutil/pipelinefixture"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/service/contractplansvc"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/odcscontractsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/stretchr/testify/require"
)

type compileBindings struct{ datasourcerefssvc.Service }

func (compileBindings) ResolvePipelineDataSourceRefs(_ context.Context, _, _, _ slug.Slug, pipeline *pipelinespec.Pipeline) (*datasourcerefssvc.ResolvedPipeline, error) {
	return &datasourcerefssvc.ResolvedPipeline{Pipeline: pipeline, Resolutions: []model.ResolvedPipelineDataSourceRef{{RefSlug: "db", DataSourceSlug: "db"}}}, nil
}

func TestMain(m *testing.M) { os.Exit(containerfixture.Run(m, "database")) }

func TestCompileDedupAndReloadAfterAuthoringChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "compiled contract plan service")
	org := slug.Slug(fmt.Sprintf("contract-plan-%d", time.Now().UnixNano()))
	other := slug.Slug(org.String() + "-other")
	for _, tenant := range []slug.Slug{org, other} {
		_, err := pool.Exec(ctx, `WITH org AS (INSERT INTO organizations(slug,name) VALUES($1,$1) RETURNING id)
		INSERT INTO projects(organization_id,slug,name) SELECT id,'etl','ETL' FROM org`, tenant)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, err := pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug=$1`, tenant)
			require.NoError(t, err)
		})
	}
	_, err := pool.Exec(ctx, `WITH credential AS (
	 INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload)
	 SELECT id,'db','static_secret','postgres','active',''::bytea FROM organizations WHERE slug=$1 RETURNING id,organization_id
	) INSERT INTO data_sources(organization_id,credential_id,slug,name,type,config,is_active)
	 SELECT organization_id,id,'db','DB','postgres','{}',true FROM credential`, org)
	require.NoError(t, err)
	operations := queries.New()
	firstDefault := "one"
	parameters := []*queries.QueryParameterParams{{Slug: "selection", Name: "selection", DataType: "text", DefaultValue: &firstDefault}}
	_, _, err = operations.Create(ctx, pool, org, "db", &queries.CreateParams{Slug: "orders", Name: "Orders", Data: json.RawMessage(`{"statement":"{\"sql\":\"SELECT id FROM orders\"}"}`), Parameters: parameters})
	require.NoError(t, err)
	txm := postgres.NewTransactionManager(pool)
	revisions := odcscontracts.NewRepository()
	catalog := odcscontractsvc.New(txm, revisions, projects.NewRepository(), compiledplans.NewRepository())
	_, err = catalog.CreateContract(ctx, org, "etl", model.NewODCSContract{Slug: "orders", Name: "Orders", ODCSID: "orders"})
	require.NoError(t, err)
	document := json.RawMessage(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"row","name":"Orders","properties":[{"id":"id","name":"id","logicalType":"integer","required":true}]}]}`)
	revision, err := catalog.CreateRevision(ctx, org, "etl", "orders", "v1", document)
	require.NoError(t, err)
	_, _, err = operations.Update(ctx, pool, org, "db", "orders", &queries.UpdateParams{Name: "Orders", Data: json.RawMessage(`{"statement":"{\"sql\":\"SELECT id FROM orders\"}"}`), Parameters: parameters, OutputContractRef: json.RawMessage(`{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row","representation":"rows"}`)})
	require.NoError(t, err)
	resolver, err := pipelinesvc.NewPipelineStore(pool, txm, pipelines.New(), revisions)
	require.NoError(t, err)
	svc, err := contractplansvc.New(pipelines.New(), txm, compiledplans.NewRepository(), operations, revisions, resolver, compileBindings{})
	require.NoError(t, err)
	// Compile the same current attachment format loaded from a synthetic Git snapshot.
	authored := []byte(`{"metadata":{"slug":"orders-sync","name":"Orders"},"steps":[{"kind":"query","slug":"fetch","query":{"data_source_ref":"db","operation_type":"sql","operation_ref":"orders","output_contract_ref":{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row","representation":"rows"}}}]}`)
	git := pipelinegit.NewSnapshotRepoStore(map[string][]byte{"pipelines/orders.json": authored})
	data, err := git.ReadFile(ctx, "pipelines/orders.json", 1<<20)
	require.NoError(t, err)
	pipeline, err := pipelinespec.Load("orders.json", data)
	require.NoError(t, err)
	projection := &odcs.ProjectionRequest{Inputs: []odcs.ProjectionInput{{Contract: "orders", Revision: "v1", ObjectID: "row", Digest: revision.Digest, Mapping: odcs.ProjectionMapping{ID: "synthetic/json/v1", Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject, Types: map[payload.StructuralKind]bool{payload.StructuralObject: true, payload.StructuralInteger: true}}}}}
	publicProjection := *projection
	publicProjection.Inputs = append([]odcs.ProjectionInput(nil), projection.Inputs...)
	publicProjection.Inputs[0].Mapping.Representation = model.ContractRepresentationRows
	publicProjection.Inputs[0].Mapping.RootShape = model.ContractRootShapeUnknown
	require.NoError(t, pipelinefixture.Materialize(ctx, resolver, org, "etl", pipeline))
	materialized, err := resolver.FindSpec(ctx, org, "etl", pipeline.Metadata.Slug)
	require.NoError(t, err)
	compiledVersion, err := svc.CompileVersion(ctx, org, "etl", pipeline.Metadata.Slug, *materialized.CurrentVersionSpecHash, "dev", &publicProjection, nil)
	require.NoError(t, err)
	require.Len(t, compiledVersion.Projections, 1)
	require.Equal(t, "synthetic/json/v1", compiledVersion.Projections[0].MappingID)
	require.Equal(t, revision.Digest, compiledVersion.Projections[0].Digest)
	reloadedVersion, err := svc.Reload(ctx, org, "etl", pipeline.Metadata.Slug, compiledVersion.Digest)
	require.NoError(t, err)
	require.NotNil(t, reloadedVersion.Projection)
	require.Equal(t, revision.Document, reloadedVersion.ProjectionInputs.Inputs[0].Document)
	publicProjection.Inputs[0].Mapping.ID = "synthetic/json/changed"
	changedProjection, err := svc.CompileVersion(ctx, org, "etl", pipeline.Metadata.Slug, *materialized.CurrentVersionSpecHash, "dev", &publicProjection, nil)
	require.NoError(t, err)
	require.NotEqual(t, compiledVersion.Digest, changedProjection.Digest)
	require.Equal(t, compiledVersion.SpecHash, changedProjection.SpecHash)
	publicProjection.Inputs[0].Mapping.ID = "synthetic/json/v1"
	countPlans := func() int {
		var count int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_compiled_plans plans JOIN projects project ON project.id=plans.project_id JOIN organizations org ON org.id=project.organization_id WHERE org.slug=$1`, org).Scan(&count))
		return count
	}
	beforeInvalid := countPlans()
	_, err = svc.CompileVersion(ctx, org, "etl", pipeline.Metadata.Slug, *materialized.CurrentVersionSpecHash, "dev", projection, nil)
	require.ErrorIs(t, err, contractplan.ErrInvalid, "JSON inspection mapping cannot replace a rows port check")
	require.Equal(t, beforeInvalid, countPlans(), "invalid public compilation must roll back its stored plan")

	projection = &publicProjection
	first, err := svc.Compile(ctx, org, "etl", contractplansvc.Request{OperationSources: map[slug.Slug]slug.Slug{"db": "db"}, Pipeline: pipeline, Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/orders.json"}, Projection: projection})
	require.NoError(t, err)
	duplicate, err := svc.Compile(ctx, org, "etl", contractplansvc.Request{OperationSources: map[slug.Slug]slug.Slug{"db": "db"}, Pipeline: pipeline, Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/orders.json"}, Projection: projection})
	require.NoError(t, err)
	require.Equal(t, first, duplicate)
	original, err := svc.Reload(ctx, org, "etl", "orders-sync", first.Digest)
	require.NoError(t, err)
	require.Len(t, original.Pins, 1)
	require.Len(t, original.Operations, 1)
	var originalContent contractplan.OperationContent
	require.NoError(t, json.Unmarshal(original.Operations[0].Content, &originalContent))
	require.Len(t, originalContent.Parameters, 1)
	require.Equal(t, firstDefault, *originalContent.Parameters[0].Default)
	require.NotNil(t, originalContent.OutputContractRef)
	require.Len(t, originalContent.ContractPins, 1)
	_, savedOutput, err := contractplan.OperationContracts(original.Operations[0])
	require.NoError(t, err)
	require.Equal(t, "id", savedOutput.Fields[0].Name)

	_, err = svc.Reload(ctx, other, "etl", "orders-sync", first.Digest)
	require.ErrorIs(t, err, compiledplans.ErrNotFound)
	_, err = svc.Compile(ctx, other, "etl", contractplansvc.Request{OperationSources: map[slug.Slug]slug.Slug{"db": "db"}, Pipeline: pipeline, Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/orders.json"}, Projection: projection})
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)

	secondDefault := "two"
	parameters[0].DefaultValue = &secondDefault
	_, _, err = operations.Update(ctx, pool, org, "db", "orders", &queries.UpdateParams{Name: "Changed", Data: json.RawMessage(`{"statement":"{\"sql\":\"SELECT changed FROM orders\"}"}`), Parameters: parameters})
	require.NoError(t, err)
	second, err := svc.Compile(ctx, org, "etl", contractplansvc.Request{OperationSources: map[slug.Slug]slug.Slug{"db": "db"}, Pipeline: pipeline, Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/orders.json"}, Projection: projection})
	require.NoError(t, err)
	require.NotEqual(t, first.Digest, second.Digest)
	require.Equal(t, first.AuthoredSpecHash, second.AuthoredSpecHash)
	impact, err := catalog.PreviewRevisionImpact(ctx, org, "etl", "orders", odcscontractsvc.RevisionImpactRequest{From: "v1", To: "v1", FromPlan: &odcscontractsvc.ImpactPlanSelection{Pipeline: "orders-sync", Digest: first.Digest}, ToPlan: &odcscontractsvc.ImpactPlanSelection{Pipeline: "orders-sync", Digest: second.Digest}})
	require.NoError(t, err)
	require.Equal(t, "selected_projection", impact.Objects[0].Scope)
	require.Empty(t, impact.Objects[0].Changes, "a changed saved operation cannot invent contract changes")
	_, err = catalog.PreviewRevisionImpact(ctx, org, "etl", "orders", odcscontractsvc.RevisionImpactRequest{From: "v1", To: "v1", FromPlan: &odcscontractsvc.ImpactPlanSelection{Pipeline: "other-pipeline", Digest: first.Digest}, ToPlan: &odcscontractsvc.ImpactPlanSelection{Pipeline: "orders-sync", Digest: second.Digest}})
	require.ErrorIs(t, err, odcscontractsvc.ErrImpactSelection)
	secondPlan, err := svc.Reload(ctx, org, "etl", "orders-sync", second.Digest)
	require.NoError(t, err)
	var secondContent contractplan.OperationContent
	require.NoError(t, json.Unmarshal(secondPlan.Operations[0].Content, &secondContent))
	require.Equal(t, secondDefault, *secondContent.Parameters[0].Default)
	// Two first-time identical compilations must deduplicate even when concurrent.
	projection.Inputs[0].Mapping.ID = "synthetic/json/v2"
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			err := txm.RunInTx(ctx, func(ctx context.Context, _ postgres.DBTX) error {
				_, err := svc.Compile(ctx, org, "etl", contractplansvc.Request{OperationSources: map[slug.Slug]slug.Slug{"db": "db"}, Pipeline: pipeline, Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/orders.json"}, Projection: projection})
				return err
			})
			errors <- err
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}

	_, err = catalog.CreateRevision(ctx, org, "etl", "orders", "v2", json.RawMessage(strings.Replace(string(document), `"version":"1"`, `"version":"2"`, 1)))
	require.NoError(t, err)
	// A caller explicitly selects a portable S3 policy; imported server metadata
	// is never read to construct a data source or credential.
	pipeline.Steps[0].Query.OperationType = "s3"
	_, _, err = operations.Update(ctx, pool, org, "db", "orders", &queries.UpdateParams{Name: "S3 snapshot", Data: json.RawMessage(`{"statement":"{\"type\":\"s3\",\"operation\":\"read\",\"key\":\"orders.json\",\"format\":\"json\"}"}`), Parameters: parameters})
	require.NoError(t, err)
	request := contractplansvc.Request{OperationSources: map[slug.Slug]slug.Slug{"db": "db"}, Pipeline: pipeline, Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/orders.json"}, ResourceScopes: []contractplansvc.OperationScope{{Step: "fetch", Role: "query", Scope: resourcescope.Scope{PolicyID: resourcescope.S3PolicyID, DataSource: "db", Operation: "orders", Contract: "orders", Revision: "v1", ObjectID: "row", Representation: model.ContractRepresentationRows, S3: &resourcescope.S3{Endpoint: "https://storage.example", Region: "test", Bucket: "fixed-bucket", Prefix: "imports", DynamicKey: true}}}}}
	_, err = svc.Compile(ctx, org, "etl", request)
	require.ErrorIs(t, err, contractplan.ErrInvalid, "authored S3 type cannot relabel a PostgreSQL source")
	_, err = pool.Exec(ctx, `UPDATE data_sources SET type='s3', config='{"endpoint":"https://storage.example","region":"test","bucket":"fixed-bucket","prefix":"imports"}' WHERE slug='db' AND organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
	require.NoError(t, err)
	scoped, err := svc.Compile(ctx, org, "etl", request)
	require.NoError(t, err)
	scopedPlan, err := svc.Reload(ctx, org, "etl", "orders-sync", scoped.Digest)
	require.NoError(t, err)
	var scopedContent contractplan.OperationContent
	require.NoError(t, json.Unmarshal(scopedPlan.Operations[0].Content, &scopedContent))
	require.Equal(t, request.ResourceScopes[0].Scope, *scopedContent.ResourceScope)
	// The supported retained-version surface must capture the exact same policy.
	require.NoError(t, pipelinefixture.Materialize(ctx, resolver, org, "etl", pipeline))
	materialized, err = resolver.FindSpec(ctx, org, "etl", pipeline.Metadata.Slug)
	require.NoError(t, err)
	publicScoped, err := svc.CompileVersion(ctx, org, "etl", pipeline.Metadata.Slug, *materialized.CurrentVersionSpecHash, "dev", nil, request.ResourceScopes)
	require.NoError(t, err)
	require.Equal(t, resourcescope.S3PolicyID, publicScoped.Operations[0].ResourceScope.PolicyID)
	publicPlan, err := svc.Reload(ctx, org, "etl", pipeline.Metadata.Slug, publicScoped.Digest)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(publicPlan.Operations[0].Content, &scopedContent))
	require.Equal(t, request.ResourceScopes[0].Scope, *scopedContent.ResourceScope)
	for _, scenario := range []string{"duplicate", "step", "role", "source", "operation", "contract", "revision", "object", "policy", "endpoint", "region", "bucket", "prefix", "dynamic selector", "other tenant", "other project"} {
		t.Run(scenario, func(t *testing.T) {
			selection := request.ResourceScopes[0]
			s3 := *selection.Scope.S3
			selection.Scope.S3 = &s3
			selections := []contractplansvc.OperationScope{selection}
			tenant, project := org, slug.Slug("etl")
			switch scenario {
			case "duplicate":
				selections = append(selections, selection)
			case "step":
				selections[0].Step = "unknown"
			case "role":
				selections[0].Role = "sink"
			case "source":
				selections[0].Scope.DataSource = "other"
			case "operation":
				selections[0].Scope.Operation = "other"
			case "contract":
				selections[0].Scope.Contract = "other"
			case "revision":
				selections[0].Scope.Revision = "v2"
			case "object":
				selections[0].Scope.ObjectID = "other"
			case "policy":
				selections[0].Scope.PolicyID = "unknown"
			case "endpoint":
				s3.Endpoint = "https://user:secret@storage.example"
			case "region":
				s3.Region = "{{region}}"
			case "bucket":
				s3.Bucket = "{{bucket}}"
			case "prefix":
				s3.Prefix = "../escape"
			case "dynamic selector":
				s3.DynamicKey = false
				_, _, err := operations.Update(ctx, pool, org, "db", "orders", &queries.UpdateParams{Name: "S3 snapshot", Data: json.RawMessage(`{"statement":"{\"type\":\"s3\",\"operation\":\"read\",\"key\":\"{{selection}}/orders.json\",\"format\":\"json\"}"}`), Parameters: parameters})
				require.NoError(t, err)
			case "other tenant":
				tenant = other
			case "other project":
				project = "other"
			}
			before := countPlans()
			_, err := svc.CompileVersion(ctx, tenant, project, pipeline.Metadata.Slug, *materialized.CurrentVersionSpecHash, "dev", nil, selections)
			require.Error(t, err)
			require.Equal(t, before, countPlans(), "failed selection cannot leave a plan")
		})
	}
	request.ResourceScopes = append(request.ResourceScopes, request.ResourceScopes[0])
	_, err = svc.Compile(ctx, org, "etl", request)
	require.ErrorIs(t, err, contractplan.ErrInvalid)
	// Remove original catalog material and replace the Git snapshot entirely.
	_, err = pool.Exec(ctx, `DELETE FROM odcs_contract_revisions WHERE slug='v1' AND contract_id IN (SELECT c.id FROM odcs_contracts c JOIN projects p ON p.id=c.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1)`, org)
	require.NoError(t, err)
	git = pipelinegit.NewSnapshotRepoStore(map[string][]byte{"pipelines/orders.json": []byte(`corrupt upstream`)})
	require.NotNil(t, git)
	_, err = pool.Exec(ctx, `DELETE FROM queries WHERE slug='orders' AND data_source_id IN (SELECT ds.id FROM data_sources ds JOIN organizations o ON ds.organization_id=o.id WHERE o.slug=$1)`, org)
	require.NoError(t, err)
	reloaded, err := svc.Reload(ctx, org, "etl", "orders-sync", first.Digest)
	require.NoError(t, err)
	require.Equal(t, original, reloaded)
	reloaded, err = svc.Reload(ctx, org, "etl", "orders-sync", scoped.Digest)
	require.NoError(t, err)
	require.Equal(t, scopedPlan, reloaded)
}
