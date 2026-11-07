package pipelinesvc

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
)

func TestNewPipelineStore_ValidatesDependencies(t *testing.T) {
	t.Parallel()

	txm := fakePipelineStoreTxManager{tx: &fakeDBTX{}}
	repo := &fakePipelineReferenceRepo{}

	_, err := NewPipelineStore(nil, txm, repo, odcscontracts.NewRepository())
	require.ErrorContains(t, err, "dbtx is required")

	_, err = NewPipelineStore(&fakeDBTX{}, nil, repo, odcscontracts.NewRepository())
	require.ErrorContains(t, err, "txm is required")

	_, err = NewPipelineStore(&fakeDBTX{}, txm, nil, odcscontracts.NewRepository())
	require.ErrorContains(t, err, "pipeline reference repository is required")
	_, err = NewPipelineStore(&fakeDBTX{}, txm, repo, nil)
	require.ErrorContains(t, err, "ODCS repository is required")

	store, err := NewPipelineStore(&fakeDBTX{}, txm, repo, odcscontracts.NewRepository())
	require.NoError(t, err)
	require.NotNil(t, store)
}

func TestPipelineStore_ReadMethodsUseRepositoryContracts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := slug.Slug("org-a")
	pipelineSlug := slug.Slug("pipeline-a")
	tx := &fakeDBTX{}
	version := &pipelinesrepo.PipelineVersion{ID: 42, PipelineSlug: pipelineSlug}
	repo := &fakePipelineReferenceRepo{
		findSpecFn: func(_ context.Context, gotOrgSlug, gotPipelineSlug slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, pipelineSlug, gotPipelineSlug)
			return nil, pipelinesrepo.ErrPipelineReferenceNotFound
		},
		findSpecForUpdateFn: func(_ context.Context, gotOrgSlug, gotPipelineSlug slug.Slug) (*pipelinesrepo.PipelineSpec, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, pipelineSlug, gotPipelineSlug)
			return nil, pipelinesrepo.ErrPipelineReferenceNotFound
		},
		listSpecsTxFn: func(_ context.Context, gotTx postgres.DBTX, gotOrgSlug slug.Slug, req model.ListRequest) ([]*pipelinesrepo.PipelineSpec, error) {
			require.Same(t, tx, gotTx)
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, model.ListRequest{Limit: 3}, req)
			return []*pipelinesrepo.PipelineSpec{{Slug: pipelineSlug}}, nil
		},
		listVersionsFn: func(_ context.Context, gotOrgSlug, gotPipelineSlug slug.Slug) ([]*pipelinesrepo.PipelineVersion, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, pipelineSlug, gotPipelineSlug)
			return []*pipelinesrepo.PipelineVersion{version}, nil
		},
		findVersionBySpecHashFn: func(_ context.Context, gotOrgSlug, gotPipelineSlug slug.Slug, specHash string) (*pipelinesrepo.PipelineVersion, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, pipelineSlug, gotPipelineSlug)
			require.Equal(t, "abc123", specHash)
			return version, nil
		},
		findVersionBySpecHashPrefixFn: func(_ context.Context, gotOrgSlug, gotPipelineSlug slug.Slug, specHashPrefix string) (*pipelinesrepo.PipelineVersion, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, pipelineSlug, gotPipelineSlug)
			require.Equal(t, "abc", specHashPrefix)
			return version, nil
		},
	}
	store := mustNewDependencyFreePipelineStore(t, tx, repo)

	_, err := store.FindSpec(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
	require.ErrorIs(t, err, ErrPipelineNotFound)

	_, err = store.FindSpecForUpdate(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
	require.ErrorIs(t, err, ErrPipelineNotFound)

	exists, err := store.Exists(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
	require.NoError(t, err)
	require.False(t, exists)

	specs, err := store.List(ctx, orgSlug, defaultProjectSlug, model.ListRequest{Limit: 3})
	require.NoError(t, err)
	require.Equal(t, pipelineSlug, specs[0].Slug)

	versions, err := store.ListVersions(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
	require.NoError(t, err)
	require.Equal(t, []*pipelinesrepo.PipelineVersion{version}, versions)

	gotVersion, err := store.FindVersionBySpecHash(ctx, orgSlug, defaultProjectSlug, pipelineSlug, "abc123")
	require.NoError(t, err)
	require.Same(t, version, gotVersion)

	gotVersion, err = store.FindVersionBySpecHashPrefix(ctx, orgSlug, defaultProjectSlug, pipelineSlug, "abc")
	require.NoError(t, err)
	require.Same(t, version, gotVersion)
}

func mustNewDependencyFreePipelineStore(
	t *testing.T,
	tx postgres.DBTX,
	repo pipelinesrepo.Repository,
) PipelineStore {
	t.Helper()

	store, err := NewPipelineStore(tx, fakePipelineStoreTxManager{tx: tx}, repo, odcscontracts.NewRepository())
	require.NoError(t, err)
	return store
}

type fakePipelineStoreTxManager struct {
	tx postgres.DBTX
}

func (m fakePipelineStoreTxManager) RunInTx(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	return fn(ctx, m.tx)
}

func (m fakePipelineStoreTxManager) RunRead(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	return fn(ctx, m.tx)
}
