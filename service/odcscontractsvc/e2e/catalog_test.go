package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/service/odcscontractsvc"
	"github.com/stretchr/testify/require"
)

const document = `{"kind":"DataContract","apiVersion":"v3.2.0","id":"orders","version":"1","status":"draft","schema":[{"id":"orders","name":"Orders","properties":[{"name":"amount","enum":[{"value":9007199254740993.30}]}]}]}`

func TestCatalogLifecycleAndTenantIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "ODCS catalog")
	org := slug.Slug(fmt.Sprintf("odcs-%d", time.Now().UnixNano()))
	other := slug.Slug(org.String() + "-other")
	for _, tenant := range []slug.Slug{org, other} {
		_, err := pool.Exec(t.Context(), `WITH org AS (INSERT INTO organizations(slug,name) VALUES($1,$1) RETURNING id)
		 INSERT INTO projects(organization_id,slug,name) SELECT id,'etl','ETL' FROM org`, tenant)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, err := pool.Exec(context.Background(), `DELETE FROM projects WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, tenant)
			require.NoError(t, err)
			_, err = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug=$1`, tenant)
			require.NoError(t, err)
		})
	}
	svc := odcscontractsvc.New(postgres.NewTransactionManager(pool), odcscontracts.NewRepository(), projects.NewRepository(), compiledplans.NewRepository())
	identity, err := svc.CreateContract(t.Context(), org, "etl", model.NewODCSContract{Slug: "orders", Name: "Orders", ODCSID: "orders"})
	require.NoError(t, err)
	require.Equal(t, "active", identity.Status)
	_, err = svc.CreateContract(t.Context(), org, "etl", model.NewODCSContract{Slug: "orders-copy", Name: "Orders", ODCSID: "orders"})
	require.ErrorIs(t, err, odcscontracts.ErrConflict)
	_, err = svc.CreateContract(t.Context(), other, "etl", model.NewODCSContract{Slug: "orders", Name: "Other", ODCSID: "orders"})
	require.NoError(t, err)
	_, err = svc.GetRevision(t.Context(), other, "etl", "orders", "v1")
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)
	_, err = svc.CreateContract(t.Context(), org, "missing", model.NewODCSContract{Slug: "absent", Name: "Absent", ODCSID: "absent"})
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)

	first, err := svc.CreateRevision(t.Context(), org, "etl", "orders", "v1", []byte(document))
	require.NoError(t, err)
	require.Equal(t, "recorded", first.Status)
	require.Equal(t, "draft", first.DeclaredStatus)
	reloaded, err := svc.GetRevision(t.Context(), org, "etl", "orders", "v1")
	require.NoError(t, err)
	require.Equal(t, first, reloaded)
	sum := sha256.Sum256(reloaded.Document)
	require.Equal(t, "sha256:"+hex.EncodeToString(sum[:]), reloaded.Digest)
	require.Contains(t, string(reloaded.Document), "9007199254740993.30")
	_, err = svc.CreateRevision(t.Context(), org, "etl", "orders", "copy", []byte(" \n"+document))
	require.ErrorIs(t, err, odcscontracts.ErrConflict)
	_, err = svc.CreateRevision(t.Context(), org, "etl", "orders", "v1", []byte(strings.Replace(document, `"version":"1"`, `"version":"2"`, 1)))
	require.ErrorIs(t, err, odcscontracts.ErrConflict)
	_, err = svc.CreateRevision(t.Context(), org, "etl", "orders", "bad", []byte(`{}`))
	require.ErrorIs(t, err, odcs.ErrDocument)

	// Real concurrent attempts must produce exactly one immutable revision.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := svc.CreateRevision(t.Context(), org, "etl", "orders", "v2", []byte(strings.Replace(document, `"version":"1"`, `"version":"2"`, 1)))
			results <- err
		})
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, odcscontracts.ErrConflict)
		}
	}
	require.Equal(t, 1, successes)

	page, err := svc.ListRevisions(t.Context(), org, "etl", "orders", model.ListRequest{Limit: 1, Order: "asc"})
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.Empty(t, page.Items[0].Document, "lists contain metadata only")
	require.Equal(t, slug.Slug("v1"), page.Items[0].Slug)
	next, err := svc.ListRevisions(t.Context(), org, "etl", "orders", model.ListRequest{Limit: 1, Order: "asc", Cursor: *page.NextCursor})
	require.NoError(t, err)
	require.Equal(t, slug.Slug("v2"), next.Items[0].Slug)
	prev, err := svc.ListRevisions(t.Context(), org, "etl", "orders", model.ListRequest{Limit: 1, Order: "asc", Direction: "prev", Cursor: *next.PrevCursor})
	require.NoError(t, err)
	require.Equal(t, slug.Slug("v1"), prev.Items[0].Slug)
	_, err = svc.ListContracts(t.Context(), org, "etl", model.ListRequest{Cursor: "invalid"})
	require.Error(t, err)
	_, err = svc.ListContracts(t.Context(), org, "missing", model.ListRequest{})
	require.ErrorIs(t, err, projects.ErrNotFound)

	_, err = pool.Exec(t.Context(), `UPDATE odcs_contract_revisions SET document='changed'::bytea WHERE slug='v1' AND organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
	require.Error(t, err)
	_, err = pool.Exec(t.Context(), `UPDATE odcs_contracts SET odcs_id='changed' WHERE slug='orders' AND organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
	require.Error(t, err)
	_, err = pool.Exec(t.Context(), `INSERT INTO odcs_contracts(organization_id,project_id,slug,name,odcs_id)
	 SELECT o.id,p.id,'cross','Cross','cross' FROM organizations o,projects p JOIN organizations owner ON owner.id=p.organization_id
	 WHERE o.slug=$1 AND owner.slug=$2`, org, other)
	require.Error(t, err, "database FK rejects cross-tenant ownership")
	_, err = pool.Exec(t.Context(), `UPDATE odcs_contract_revisions SET organization_id=(SELECT id FROM organizations WHERE slug=$2) WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org, other)
	require.Error(t, err)

	txm := postgres.NewTransactionManager(pool)
	rollbackService := odcscontractsvc.New(txm, odcscontracts.NewRepository(), projects.NewRepository(), compiledplans.NewRepository())
	err = txm.RunInTx(t.Context(), func(ctx context.Context, _ postgres.DBTX) error {
		_, err := rollbackService.CreateRevision(ctx, org, "etl", "orders", "rolled-back", []byte(strings.Replace(document, `"version":"1"`, `"version":"rollback"`, 1)))
		if err != nil {
			return err
		}
		return odcscontracts.ErrConflict
	})
	require.ErrorIs(t, err, odcscontracts.ErrConflict)
	_, err = svc.GetRevision(t.Context(), org, "etl", "orders", "rolled-back")
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)

	contracts, err := svc.ListContracts(t.Context(), org, "etl", model.ListRequest{})
	require.NoError(t, err)
	require.Len(t, contracts.Items, 1)
	require.Equal(t, "Orders", contracts.Items[0].Name)

	rich, err := os.ReadFile("../../../pkg/odcs/testdata/rich.json")
	require.NoError(t, err)
	richRevision, err := svc.CreateRevision(t.Context(), org, "etl", "orders", "rich", rich)
	require.NoError(t, err)
	richReloaded, err := svc.GetRevision(t.Context(), org, "etl", "orders", "rich")
	require.NoError(t, err)
	require.Equal(t, richRevision, richReloaded)
	_, err = svc.GetRevision(t.Context(), other, "etl", "orders", "rich")
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)
	projection, err := svc.ValidateRevision(t.Context(), org, "etl", "orders", "rich", "order")
	require.NoError(t, err)
	require.True(t, projection.DocumentValid)
	require.NotNil(t, projection.Shape)
	require.Equal(t, odcs.CompilerID, projection.CompilerID)
	require.NotEmpty(t, projection.Coverage)

	validationDocument := []byte(`{"kind":"DataContract","apiVersion":"v3.2.0","id":"orders","version":"supported","schema":[{"id":"order","name":"Orders","properties":[{"name":"id","logicalType":"integer","required":true}]}]}`)
	validatedRevision, err := svc.CreateRevision(t.Context(), org, "etl", "orders", "supported", validationDocument)
	require.NoError(t, err)
	converted, err := svc.ValidateRevision(t.Context(), org, "etl", "orders", "supported", "order")
	require.NoError(t, err)
	require.True(t, converted.DocumentValid)
	require.Empty(t, converted.Diagnostics)
	require.NotNil(t, converted.Shape)
	require.Equal(t, validatedRevision.Digest, converted.Digest)
	_, err = svc.ValidateRevision(t.Context(), other, "etl", "orders", "supported", "order")
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)
	_, err = svc.ValidateRevision(t.Context(), org, "missing", "orders", "supported", "order")
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)
	legacy, err := svc.ValidateRevision(t.Context(), org, "etl", "orders", "v1", "orders")
	require.NoError(t, err)
	require.True(t, legacy.DocumentValid, "admitted revisions can still lack an executable shape")
	require.Nil(t, legacy.Shape)
	require.NotEmpty(t, legacy.Diagnostics)
	require.Equal(t, first.Digest, legacy.Digest)

	_, err = svc.UpdateContract(t.Context(), org, "etl", "orders", model.UpdateODCSContract{Name: "Orders", Status: "archived"})
	require.NoError(t, err)
	_, err = svc.CreateRevision(t.Context(), org, "etl", "orders", "v3", []byte(document))
	require.ErrorIs(t, err, odcscontractsvc.ErrArchived)
	_, err = svc.GetRevision(t.Context(), org, "etl", "orders", "v1")
	require.NoError(t, err)
	converted, err = svc.ValidateRevision(t.Context(), org, "etl", "orders", "supported", "order")
	require.NoError(t, err)
	require.NotNil(t, converted.Shape, "archival preserves immutable revision validation")

	_, err = pool.Exec(t.Context(), `DELETE FROM projects WHERE slug='etl' AND organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
	require.NoError(t, err)
	var remaining int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM odcs_contract_revisions WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org).Scan(&remaining))
	require.Zero(t, remaining)
	_, err = svc.GetContract(t.Context(), other, "etl", "orders")
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `DELETE FROM organizations WHERE slug=$1`, other)
	require.NoError(t, err)
	_, err = svc.GetContract(t.Context(), other, "etl", "orders")
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)
}

func TestGeneratedCatalogIdentifiers(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "generated ODCS catalog identifiers")
	org := slug.Slug(fmt.Sprintf("odcs-generated-%d", time.Now().UnixNano()))
	_, err := pool.Exec(t.Context(), `WITH org AS (INSERT INTO organizations(slug,name) VALUES($1,$1) RETURNING id) INSERT INTO projects(organization_id,slug,name) SELECT id,'etl','ETL' FROM org`, org)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM projects WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug=$1`, org)
		require.NoError(t, err)
	})
	svc := odcscontractsvc.New(postgres.NewTransactionManager(pool), odcscontracts.NewRepository(), projects.NewRepository(), compiledplans.NewRepository())
	first, err := svc.CreateContract(t.Context(), org, "etl", model.NewODCSContract{Name: "Orders", ODCSID: "orders"})
	require.NoError(t, err)
	require.Equal(t, slug.Slug("orders"), first.Slug)
	second, err := svc.CreateContract(t.Context(), org, "etl", model.NewODCSContract{Name: "Orders", ODCSID: "other-orders"})
	require.NoError(t, err)
	require.NotEqual(t, first.Slug, second.Slug)
	readback, err := svc.GetContract(t.Context(), org, "etl", second.Slug)
	require.NoError(t, err)
	require.Equal(t, "other-orders", readback.ODCSID)
	_, err = svc.UpdateContract(t.Context(), org, "etl", first.Slug, model.UpdateODCSContract{Name: "Renamed", Status: "active"})
	require.NoError(t, err)
	readback, err = svc.GetContract(t.Context(), org, "etl", first.Slug)
	require.NoError(t, err)
	require.Equal(t, first.Slug, readback.Slug)
	revision, err := svc.CreateRevision(t.Context(), org, "etl", first.Slug, "", []byte(document))
	require.NoError(t, err)
	require.Equal(t, slug.Slug("revision-1"), revision.Slug)
	changed := strings.Replace(document, `"status":"draft"`, `"status":"active"`, 1)
	next, err := svc.CreateRevision(t.Context(), org, "etl", first.Slug, "", []byte(changed))
	require.NoError(t, err)
	require.NotEqual(t, revision.Slug, next.Slug)
	stored, err := svc.GetRevision(t.Context(), org, "etl", first.Slug, next.Slug)
	require.NoError(t, err)
	require.Equal(t, next.Digest, stored.Digest)
	_, err = svc.CreateRevision(t.Context(), org, "etl", first.Slug, "", []byte(changed))
	require.ErrorIs(t, err, odcscontracts.ErrConflict, "omission must not bypass document identity")
}
