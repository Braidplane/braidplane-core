package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/stretchr/testify/require"
)

type artifactSource struct {
	org    slug.Slug
	source *pipelinegit.Source
}

func (s artifactSource) Resolve(_ context.Context, org slug.Slug, req pipelinegit.SourceRequest) (*pipelinegit.Source, error) {
	if org != s.org || req.ProjectSlug != "etl" {
		return nil, odcscontracts.ErrNotFound
	}
	return s.source, nil
}

func TestArtifactImportLifecycle(t *testing.T) {
	pool := e2edb.OpenPostgresPool(t, "ODCS artifacts")
	org := slug.Slug(fmt.Sprintf("artifacts-%d", time.Now().UnixNano()))
	_, err := pool.Exec(t.Context(), `INSERT INTO organizations(slug,name) VALUES($1,$1)`, org)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM projects WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug=$1`, org)
		require.NoError(t, err)
	})
	_, err = pool.Exec(t.Context(), `WITH credential AS (INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload) SELECT id,'git-credential','static_secret','git','active',''::bytea FROM organizations WHERE slug=$1 RETURNING id,organization_id), source AS (INSERT INTO data_sources(organization_id,credential_id,name,slug,type,config,is_active) SELECT organization_id,id,'Git','git','git','{}',true FROM credential RETURNING id,organization_id) INSERT INTO projects(organization_id,slug,name,pipeline_backend_data_source_id,pipeline_backend_repo,pipeline_backend_tracked_branch) SELECT organization_id,'etl','ETL',id,'example.test/org/repository','main' FROM source`, org)
	require.NoError(t, err)
	repoURL, ref, commit := "example.test/org/repository", "refs/heads/main", strings.Repeat("a", 40)
	raw := []byte("# original comment\nkind: DataContract\napiVersion: v3.2.0\nid: orders\nname: Orders\nversion: '1'\nschema: []\ndescription:\n  purpose: '${UNRESOLVED}'\n")
	source := &pipelinegit.Source{RepoStore: pipelinegit.NewSnapshotRepoStore(map[string][]byte{"contracts/orders.yaml": raw, "outside/ignored.yaml": []byte("bad")}), DataSourceSlug: "git", SourceRepo: &repoURL, SourceRef: &ref, LastAppliedCommit: &commit}
	txm := postgres.NewTransactionManager(pool)
	importer := odcscontractsvc.NewArtifactImporter(txm, odcscontracts.NewRepository(), projects.NewRepository(), artifactSource{org, source})
	catalog := odcscontractsvc.New(txm, odcscontracts.NewRepository(), projects.NewRepository(), compiledplans.NewRepository())
	preview, err := importer.Import(t.Context(), org, "etl", false, "")
	require.NoError(t, err)
	require.False(t, preview.Applied)
	require.Len(t, preview.Artifacts, 1)
	a := preview.Artifacts[0]

	err = txm.RunInTx(t.Context(), func(ctx context.Context, db postgres.DBTX) error {
		absent := a
		absent.ContractSlug = "missing-contract"
		return odcscontracts.NewRepository().CreateArtifact(ctx, db, org, "etl", absent)
	})
	require.ErrorIs(t, err, odcscontracts.ErrConflict)
	_, err = catalog.GetContract(t.Context(), org, "etl", a.ContractSlug)
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)
	_, err = importer.Import(t.Context(), org, "etl", true, strings.Repeat("b", 40))
	require.ErrorIs(t, err, odcscontractsvc.ErrImportConflict)
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := importer.Import(t.Context(), org, "etl", true, commit); errors <- err })
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	stored, err := catalog.GetRevision(t.Context(), org, "etl", a.ContractSlug, a.RevisionSlug)
	require.NoError(t, err)
	require.Contains(t, string(stored.Document), "${UNRESOLVED}")
	require.Equal(t, a.Digest, stored.Digest)
	artifacts, err := importer.Get(t.Context(), org, "etl", a.ContractSlug, a.RevisionSlug, repoURL, commit, "contracts/orders.yaml")
	require.NoError(t, err)
	require.NotNil(t, artifacts)
	require.Equal(t, raw, artifacts.Original)
	require.Equal(t, a.ArtifactDigest, artifacts.ArtifactDigest)
	_, err = importer.Get(t.Context(), "other", "etl", a.ContractSlug, a.RevisionSlug, repoURL, commit, "contracts/orders.yaml")
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)
	// A rename and comment-only change retain semantic revision identity and add provenance.
	commit = strings.Repeat("b", 40)
	source.RepoStore = pipelinegit.NewSnapshotRepoStore(map[string][]byte{"contracts/renamed.yml": append([]byte("# renamed\n"), raw...)})
	renamed, err := importer.Import(t.Context(), org, "etl", true, commit)
	require.NoError(t, err)
	require.Equal(t, a.RevisionSlug, renamed.Artifacts[0].RevisionSlug)
	require.Equal(t, a.ContractSlug, renamed.Artifacts[0].ContractSlug)
	// Removal retains catalog history. Archival and reactivation are explicit catalog actions.
	source.RepoStore = pipelinegit.NewSnapshotRepoStore(map[string][]byte{})
	removed, err := importer.Import(t.Context(), org, "etl", true, commit)
	require.NoError(t, err)
	require.Empty(t, removed.Artifacts)
	_, err = catalog.UpdateContract(t.Context(), org, "etl", a.ContractSlug, model.UpdateODCSContract{Name: "Orders", Status: "archived"})
	require.NoError(t, err)
	source.RepoStore = pipelinegit.NewSnapshotRepoStore(map[string][]byte{"contracts/orders.yaml": raw})
	_, err = importer.Import(t.Context(), org, "etl", true, commit)
	require.ErrorIs(t, err, odcscontractsvc.ErrArchived)
	_, err = catalog.UpdateContract(t.Context(), org, "etl", a.ContractSlug, model.UpdateODCSContract{Name: "Orders", Status: "active"})
	require.NoError(t, err)
	_, err = importer.Import(t.Context(), org, "etl", true, commit)
	require.NoError(t, err)
	source.RepoStore = pipelinegit.NewSnapshotRepoStore(map[string][]byte{"contracts/one.yaml": raw, "contracts/two.yaml": raw})
	_, err = importer.Import(t.Context(), org, "etl", true, commit)
	require.ErrorIs(t, err, odcscontractsvc.ErrImportConflict)
	source.RepoStore = pipelinegit.NewSnapshotRepoStore(map[string][]byte{"contracts/invalid.yaml": []byte("id: orders\nid: duplicate")})
	_, err = importer.Import(t.Context(), org, "etl", true, commit)
	require.ErrorIs(t, err, odcs.ErrDocument)

	for _, id := range []string{"", " ", strings.Repeat("x", 256)} {
		invalid := fmt.Sprintf("kind: DataContract\napiVersion: v3.2.0\nversion: '1'\nid: %q\nschema: []\n", id)
		source.RepoStore = pipelinegit.NewSnapshotRepoStore(map[string][]byte{"contracts/invalid-id.yaml": []byte(invalid)})
		_, err = importer.Import(t.Context(), org, "etl", true, commit)
		require.ErrorIs(t, err, odcscontractsvc.ErrArtifact)
	}
	// A dishonest or corrupt snapshot cannot rewrite provenance at an already recorded commit/path.
	source.RepoStore = pipelinegit.NewSnapshotRepoStore(map[string][]byte{"contracts/orders.yaml": append([]byte("# changed\n"), raw...)})
	_, err = importer.Import(t.Context(), org, "etl", true, commit)
	require.ErrorIs(t, err, odcscontractsvc.ErrImportConflict)
	storedAgain, err := catalog.GetRevision(t.Context(), org, "etl", a.ContractSlug, a.RevisionSlug)
	require.NoError(t, err)
	require.Equal(t, stored, storedAgain)

	// Rich imported declarations retain exactly the existing admission result.
	rich, err := os.ReadFile("../../../pkg/odcs/testdata/rich.json")
	require.NoError(t, err)
	var richID struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rich, &richID))
	canonical, err := odcs.Record(rich, richID.ID)
	require.NoError(t, err)
	commit = strings.Repeat("c", 40)
	source.RepoStore = pipelinegit.NewSnapshotRepoStore(map[string][]byte{"contracts/rich.json": rich})
	manifest, err := importer.Import(t.Context(), org, "etl", true, commit)
	require.NoError(t, err)
	richRevision, err := catalog.GetRevision(t.Context(), org, "etl", manifest.Artifacts[0].ContractSlug, manifest.Artifacts[0].RevisionSlug)
	require.NoError(t, err)
	require.Equal(t, canonical.Document, richRevision.Document)

	var longPath strings.Builder
	longPath.WriteString("contracts/")
	for i := range 60 {
		sum := sha256.Sum256([]byte(fmt.Sprintf("path-segment-%d", i)))
		fmt.Fprintf(&longPath, "%x/", sum)
	}
	longPath.WriteString("orders.yaml")
	commit = strings.Repeat("d", 40)
	source.RepoStore = pipelinegit.NewSnapshotRepoStore(map[string][]byte{longPath.String(): raw})
	_, err = importer.Import(t.Context(), org, "etl", true, commit)
	require.ErrorIs(t, err, odcscontractsvc.ErrArtifact, "oversized provenance must fail admission before reaching the database index")

	for _, key := range []struct{ repo, commit, path string }{
		{strings.Repeat("r", 1025), commit, "contracts/orders.yaml"},
		{repoURL, commit, "contracts/invalid\x00.yaml"},
		{repoURL, commit, "contracts/invalid" + string([]byte{0xff}) + ".yaml"},
		{repoURL, "short", "contracts/orders.yaml"},
		{repoURL, commit, "contracts/../outside.yaml"},
	} {
		_, err = importer.Get(t.Context(), org, "etl", a.ContractSlug, a.RevisionSlug, key.repo, key.commit, key.path)
		require.ErrorIs(t, err, odcscontractsvc.ErrArtifact)
	}
	_, err = pool.Exec(t.Context(), `UPDATE odcs_artifacts SET original='' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
	require.Error(t, err)
}
