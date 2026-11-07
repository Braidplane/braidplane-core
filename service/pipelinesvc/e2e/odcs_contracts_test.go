package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/service/odcscontractsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/stretchr/testify/require"
)

type odcsAcceptanceSource struct {
	source *pipelinegit.Source
	org    slug.Slug
}

func (s odcsAcceptanceSource) Resolve(_ context.Context, org slug.Slug, req pipelinegit.SourceRequest) (*pipelinegit.Source, error) {
	if org != s.org || req.ProjectSlug != defaultProjectSlug {
		return nil, odcscontracts.ErrNotFound
	}
	return s.source, nil
}

func TestODCSReferencesApplyDeployReloadAndTenantIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	ctx := context.Background()
	pool := openIntegrationPool(t)
	org := must.Slug(fmt.Sprintf("odcs-pipeline-%d", time.Now().UnixNano()))
	other := must.Slug(org.String() + "-other")
	seedLifecycleIntegrationOrg(t, pool, org, "ODCS Pipeline")
	seedLifecycleIntegrationOrg(t, pool, other, "Other ODCS Pipeline")
	seedPipelineDeploymentEnvironment(t, pool, org, defaultProjectSlug, "dev")
	txm := postgres.NewTransactionManager(pool)
	catalog := odcscontractsvc.New(txm, odcscontracts.NewRepository(), projects.NewRepository(), compiledplans.NewRepository())
	_, err := catalog.CreateContract(ctx, org, defaultProjectSlug, model.NewODCSContract{Slug: "orders", Name: "Orders", ODCSID: "orders"})
	require.NoError(t, err)
	document := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","status":"active","schema":[{"id":"order","name":"Orders","properties":[{"name":"id","logicalType":"integer","required":true}]}]}`
	document = strings.Replace(document, `"status":"active"`, `"status":"active","slaProperties":[{"property":"latency","value":1}],"team":{"name":"Operations"}`, 1)
	revision, err := catalog.CreateRevision(ctx, org, defaultProjectSlug, "orders", "v1", json.RawMessage(document))
	require.NoError(t, err)

	// Import the same semantic revision, then inspect the exact selection used by both ports.
	repoURL, sourceRef, commit := "example.test/odcs-acceptance", "refs/heads/main", strings.Repeat("a", 40)
	_, err = pool.Exec(ctx, `WITH credential AS (
      INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload)
      SELECT id,'odcs-git','static_secret','git','active',''::bytea FROM organizations WHERE slug=$1 RETURNING id,organization_id
    ), source AS (
      INSERT INTO data_sources(organization_id,credential_id,name,slug,type,config,is_active)
      SELECT organization_id,id,'ODCS Git','odcs-git','git','{}',true FROM credential RETURNING id,organization_id
    ) UPDATE projects SET pipeline_backend_data_source_id=source.id,pipeline_backend_repo=$3,pipeline_backend_tracked_branch='main'
      FROM source WHERE projects.organization_id=source.organization_id AND projects.slug=$2`, org, defaultProjectSlug, repoURL)
	require.NoError(t, err)
	source := &pipelinegit.Source{RepoStore: pipelinegit.NewSnapshotRepoStore(map[string][]byte{"contracts/orders.json": []byte(document)}), DataSourceSlug: "odcs-git", SourceRepo: &repoURL, SourceRef: &sourceRef, LastAppliedCommit: &commit}
	importer := odcscontractsvc.NewArtifactImporter(txm, odcscontracts.NewRepository(), projects.NewRepository(), odcsAcceptanceSource{source, org})
	preview, err := importer.Import(ctx, org, defaultProjectSlug, false, "")
	require.NoError(t, err)
	require.Equal(t, commit, preview.Commit)
	applied, err := importer.Import(ctx, org, defaultProjectSlug, true, preview.Commit)
	require.NoError(t, err)
	require.Len(t, applied.Artifacts, 1)
	artifact := applied.Artifacts[0]
	require.Equal(t, slug.Slug("v1"), artifact.RevisionSlug)
	original, err := importer.Get(ctx, org, defaultProjectSlug, artifact.ContractSlug, artifact.RevisionSlug, repoURL, commit, artifact.Path)
	require.NoError(t, err)
	require.Equal(t, []byte(document), original.Original)
	require.Equal(t, artifact.ArtifactDigest, original.ArtifactDigest)
	inspected, err := catalog.InspectRevision(ctx, org, defaultProjectSlug, artifact.ContractSlug, artifact.RevisionSlug, "order")
	require.NoError(t, err)
	require.Equal(t, revision.Document, inspected.Revision.Document)
	require.True(t, inspected.Projection.CoverageComplete)
	require.NotNil(t, inspected.Projection.Shape)
	require.Contains(t, string(inspected.Revision.Document), "Operations")
	require.Contains(t, inspected.Projection.Coverage, model.ODCSDeclarationCoverage{Path: "$.slaProperties", State: "unsupported", Reason: "no_runtime_evaluator", Applicability: "document"})
	_, err = catalog.InspectRevision(ctx, other, defaultProjectSlug, artifact.ContractSlug, artifact.RevisionSlug, "order")
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)
	_, err = catalog.InspectRevision(ctx, org, "absent-project", artifact.ContractSlug, artifact.RevisionSlug, "order")
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	pipeline := integrationPipeline("orders-sync")
	pipeline.Steps[0].Query.OutputContractRef = &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order"}
	pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "transform", Slug: "normalize", Transform: &pipelinespec.TransformSpec{SQL: "select id from input_rows", OutputKind: model.TransformOutputKindRows, Inputs: map[string]pipelinespec.StepInput{"input_rows": {FromStep: "fetch-data"}}, InputContractRef: &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order"}}})
	_, _, err = store.ResolvePipelineContracts(ctx, other, defaultProjectSlug, pipeline)
	require.ErrorIs(t, err, pipelinesvc.ErrODCSContractReference)
	_, _, err = store.ResolvePipelineContracts(ctx, org, "absent-project", pipeline)
	require.ErrorIs(t, err, pipelinesvc.ErrODCSContractReference)
	specJSON, err := json.Marshal(pipeline)
	require.NoError(t, err)
	git := &lifecycleRepoStore{listFilesFn: func(context.Context, string) ([]repostore.FileEntry, error) {
		return []repostore.FileEntry{{Path: "pipelines/orders.pipeline.json"}}, nil
	}, readFileFn: func(context.Context, string) ([]byte, error) { return specJSON, nil }}
	for _, dryRun := range []bool{true, false} {
		result, err := store.ReconcileFromGit(ctx, org, defaultProjectSlug, git, pipelinesvc.ReconcileFromGitParams{DryRun: dryRun, SourceRef: &sourceRef, LastAppliedCommit: &commit})
		require.NoError(t, err)
		require.Zero(t, result.Failed)
	}
	versions, err := store.ListVersions(ctx, org, defaultProjectSlug, "orders-sync")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	version := versions[0]
	var pins []model.PinnedODCSPort
	require.NoError(t, json.Unmarshal(version.ResolvedContracts, &pins))
	require.Len(t, pins, 2)
	require.Equal(t, revision.Digest, pins[0].Digest)
	require.Equal(t, odcs.CompilerID, pins[0].CompilerID)
	require.Equal(t, inspected.Projection.Coverage, pins[0].Coverage)
	require.Equal(t, slug.Slug("v1"), pins[0].RevisionSlug)
	require.Equal(t, "order", pins[0].SchemaObjectID)
	require.Equal(t, "id", pins[0].Shape.Fields[0].Name)
	require.Equal(t, model.ContractFieldTypeValueInteger, pins[0].Shape.Fields[0].Type)
	_, err = catalog.CreateRevision(ctx, org, defaultProjectSlug, "orders", "v2", json.RawMessage(strings.Replace(document, `"version":"1"`, `"version":"2"`, 1)))
	require.NoError(t, err)
	_, err = catalog.CreateRevision(ctx, org, defaultProjectSlug, "orders", "breaking", json.RawMessage(strings.Replace(document, `"integer"`, `"string"`, 1)))
	require.NoError(t, err)
	_, err = catalog.UpdateContract(ctx, org, defaultProjectSlug, "orders", model.UpdateODCSContract{Name: "Orders", Status: "archived"})
	require.NoError(t, err)
	svc := newPipelineDeploymentIntegrationService(t, pool, txm, store)
	deployment, err := svc.Deploy(ctx, org, defaultProjectSlug, "dev", "orders-sync", model.SystemEventActor(), pipelinesvc.DeployPipelineRequest{SpecHash: version.SpecHash})
	require.NoError(t, err)
	require.Equal(t, version.SpecHash, deployment.CurrentVersionSpecHash)
	impact, err := catalog.PreviewRevisionImpact(ctx, org, defaultProjectSlug, "orders", odcscontractsvc.RevisionImpactRequest{From: "v1", To: "v2"})
	require.NoError(t, err)
	require.Len(t, impact.Consumers, 4)
	for _, consumer := range impact.Consumers {
		require.Equal(t, "supported", consumer.Comparison)
		require.Nil(t, consumer.Impact)
		if consumer.Origin == "deployed" {
			require.Equal(t, "dev", consumer.Environment)
		}
	}
	member := slug.Slug(org.String() + "-editor")
	seedE2EMember(t, pool, org, member)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM users WHERE slug=$1`, member)
		require.NoError(t, err)
	})
	err = store.SaveDraft(ctx, org, defaultProjectSlug, member, pipeline, nil)
	require.NoError(t, err)
	impact, err = catalog.PreviewRevisionImpact(ctx, org, defaultProjectSlug, "orders", odcscontractsvc.RevisionImpactRequest{From: "v1", To: "v2"})
	require.NoError(t, err)
	require.Len(t, impact.Consumers, 6)
	savedDraft, err := store.FindDraft(ctx, org, defaultProjectSlug, "orders-sync", member)
	require.NoError(t, err)
	expectedDraftHash := fmt.Sprintf("%x", sha256.Sum256(savedDraft.SpecJSONB))
	drafts := 0
	for _, consumer := range impact.Consumers {
		if consumer.Origin == "draft" {
			require.Equal(t, expectedDraftHash, consumer.SpecHash)
			drafts++
		}
	}
	require.Equal(t, 2, drafts)
	_, err = catalog.PreviewRevisionImpact(ctx, other, defaultProjectSlug, "orders", odcscontractsvc.RevisionImpactRequest{From: "v1", To: "v2"})
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)
	_, err = catalog.PreviewRevisionImpact(ctx, org, "absent-project", "orders", odcscontractsvc.RevisionImpactRequest{From: "v1", To: "v2"})
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)

	reloaded, err := store.FindVersionBySpecHash(ctx, org, defaultProjectSlug, "orders-sync", version.SpecHash)
	require.NoError(t, err)
	require.JSONEq(t, string(version.ResolvedContracts), string(reloaded.ResolvedContracts))
	require.JSONEq(t, string(specJSON), string(reloaded.SpecJSONB))
	var reloadedPins []model.PinnedODCSPort
	require.NoError(t, json.Unmarshal(reloaded.ResolvedContracts, &reloadedPins))
	require.Equal(t, pins[1].Coverage, reloadedPins[1].Coverage)
	require.Equal(t, odcs.CompilerID, reloadedPins[1].CompilerID)
	require.Equal(t, slug.Slug("normalize"), reloadedPins[1].StepSlug)
	require.Equal(t, "input", reloadedPins[1].Port)
	runtime := transformsql.NewRuntime()
	transform := transformsql.Transform{SQL: "select id from input_rows", OutputKind: model.TransformOutputKindRows, InputContract: reloadedPins[1].Shape}
	for _, rows := range [][]map[string]any{{{"id": nil}}, {{"id": int64(1)}, {}}} {
		_, err := runtime.Execute(transform, map[string]*payload.Payload{"input_rows": {Representation: model.ContractRepresentationRows, Rows: rows}})
		require.ErrorIs(t, err, xerror.TransformInputContractViolation())
	}
	output, err := runtime.Execute(transform, map[string]*payload.Payload{"input_rows": {Representation: model.ContractRepresentationRows, Rows: []map[string]any{{"id": int64(1)}}}})
	require.NoError(t, err)
	require.Len(t, output.Rows, 1)
	_, err = store.FindVersionBySpecHash(ctx, other, defaultProjectSlug, "orders-sync", version.SpecHash)
	require.ErrorIs(t, err, pipelinesvc.ErrPipelineVersionNotFound)

	// An incompatible revision blocks the same apply paths as an inline edge mismatch.
	pipeline.Steps[0].Query.OutputContractRef.RevisionSlug = "breaking"
	specJSON, err = json.Marshal(pipeline)
	require.NoError(t, err)
	for _, dryRun := range []bool{true, false} {
		result, err := store.ReconcileFromGit(ctx, org, defaultProjectSlug, git, pipelinesvc.ReconcileFromGitParams{DryRun: dryRun, SourceRef: &sourceRef, LastAppliedCommit: &commit})
		require.NoError(t, err)
		require.Equal(t, 1, result.Failed)
	}
	versions, err = store.ListVersions(ctx, org, defaultProjectSlug, "orders-sync")
	require.NoError(t, err)
	require.Len(t, versions, 1)

	// Missing references must fail the dry run too, without creating another version.
	pipeline.Steps[0].Query.OutputContractRef.RevisionSlug = "missing"
	specJSON, err = json.Marshal(pipeline)
	require.NoError(t, err)
	result, err := store.ReconcileFromGit(ctx, org, defaultProjectSlug, git, pipelinesvc.ReconcileFromGitParams{DryRun: true, SourceRef: &sourceRef, LastAppliedCommit: &commit})
	require.NoError(t, err)
	require.Equal(t, 1, result.Failed)
	versions, err = store.ListVersions(ctx, org, defaultProjectSlug, "orders-sync")
	require.NoError(t, err)
	require.Len(t, versions, 1)
}
