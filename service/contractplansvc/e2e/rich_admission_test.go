package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/testutil/pipelinefixture"
	"github.com/Braidplane/braidplane-core/pkg/testutil/richcontractfixture"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/service/contractplansvc"
	"github.com/Braidplane/braidplane-core/service/odcscontractsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/stretchr/testify/require"
)

func TestRichRetainedVersionCompilationUsesScopedCatalogAndHistoricalBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "rich compiled ports")
	org := slug.Slug(fmt.Sprintf("rich-plan-%d", time.Now().UnixNano()))
	_, err := pool.Exec(ctx, `WITH org AS (INSERT INTO organizations(slug,name) VALUES($1,$1) RETURNING id) INSERT INTO projects(organization_id,slug,name) SELECT id,'etl','ETL' FROM org`, org)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug=$1`, org)
		require.NoError(t, err)
	})
	txm := postgres.NewTransactionManager(pool)
	revisions := odcscontracts.NewRepository()
	plans := compiledplans.NewRepository()
	catalog := odcscontractsvc.New(txm, revisions, projects.NewRepository(), plans)
	_, err = catalog.CreateContract(ctx, org, "etl", model.NewODCSContract{Slug: "orders", Name: "Orders", ODCSID: "orders"})
	require.NoError(t, err)
	revision, err := catalog.CreateRevision(ctx, org, "etl", "orders", "v1", json.RawMessage(richcontractfixture.Document))
	require.NoError(t, err)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelines.New(), revisions)
	require.NoError(t, err)
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "rich-orders", Name: "Rich orders"}, Steps: []pipelinespec.Step{{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "db", OperationType: "sql", OperationInline: map[string]any{"statement": `{"sql":"SELECT synthetic"}`}, OutputContractRef: richcontractfixture.Reference(model.ContractRepresentationRows, model.ContractRootShapeUnknown)}}}}
	require.NoError(t, pipelinefixture.Materialize(ctx, store, org, "etl", pipeline))
	retained, err := store.FindSpec(ctx, org, "etl", "rich-orders")
	require.NoError(t, err)
	svc, err := contractplansvc.New(pipelines.New(), txm, plans, queries.New(), revisions, store, compileBindings{})
	require.NoError(t, err)
	_, selection, err := richcontractfixture.Selection(model.ContractRepresentationRows, model.ContractRootShapeUnknown)
	require.NoError(t, err)
	for i := range selection.Inputs {
		selection.Inputs[i].Document = nil
	}
	summary, err := svc.CompileVersion(ctx, org, "etl", "rich-orders", *retained.CurrentVersionSpecHash, "", selection, nil)
	require.NoError(t, err)
	require.Equal(t, "available", summary.Status)
	require.Equal(t, contractplan.CompilerID, summary.CompilerID)
	require.Equal(t, revision.Digest, summary.Pins[0].Digest)
	require.Len(t, summary.Projections, 2)
	original, err := svc.Reload(ctx, org, "etl", "rich-orders", summary.Digest)
	require.NoError(t, err)
	_, err = original.RuntimePipeline()
	require.NoError(t, err)
	duplicate, err := svc.CompileVersion(ctx, org, "etl", "rich-orders", *retained.CurrentVersionSpecHash, "", selection, nil)
	require.NoError(t, err)
	require.Equal(t, summary.Digest, duplicate.Digest)
	for _, scope := range []struct{ org, project slug.Slug }{{org, "missing"}, {"missing", "etl"}} {
		_, err = svc.CompileVersion(ctx, scope.org, scope.project, "rich-orders", *retained.CurrentVersionSpecHash, "", selection, nil)
		require.ErrorIs(t, err, pipelines.ErrPipelineVersionNotFound)
		_, err = svc.Get(ctx, scope.org, scope.project, "rich-orders", summary.Digest)
		require.ErrorIs(t, err, compiledplans.ErrNotFound)
	}
	selection.Inputs[0].Digest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	_, err = svc.CompileVersion(ctx, org, "etl", "rich-orders", *retained.CurrentVersionSpecHash, "", selection, nil)
	require.ErrorIs(t, err, contractplan.ErrInvalid)
	selection.Inputs[0].Digest = revision.Digest
	selection.Inputs[1].Revision = "missing"
	_, err = svc.CompileVersion(ctx, org, "etl", "rich-orders", *retained.CurrentVersionSpecHash, "", selection, nil)
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)
	historical, err := svc.Reload(ctx, org, "etl", "rich-orders", summary.Digest)
	require.NoError(t, err)
	require.Equal(t, original, historical)
	readback, err := svc.Get(ctx, org, "etl", "rich-orders", summary.Digest)
	require.NoError(t, err)
	require.Equal(t, "available", readback.Status)
	require.True(t, readback.AdmissionEnabled)
}
