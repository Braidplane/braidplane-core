package odcscontractsvc

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/stretchr/testify/require"
)

type fakeRepository struct {
	odcscontracts.Repository
	createContract func(model.NewODCSContract) (*model.ODCSContract, error)
	lock           func() (*model.ODCSContract, error)
	create         func(model.ODCSRevision) (*model.ODCSRevision, error)
	get            func(slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.ODCSRevision, error)
}

func (f fakeRepository) LockContract(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) (*model.ODCSContract, error) {
	return f.lock()
}

func (f fakeRepository) CreateRevision(_ context.Context, _ postgres.DBTX, _, _, _ slug.Slug, revision model.ODCSRevision) (*model.ODCSRevision, error) {
	return f.create(revision)
}

func TestRevisionValidationAndDependencyFailures(t *testing.T) {
	document := []byte(`{"kind":"DataContract","apiVersion":"v3.2.0","version":"1","id":"orders","schema":[{"id":"orders","name":"Orders"}]}`)
	for _, tc := range []struct {
		name, status        string
		raw                 []byte
		dependencyErr, want error
	}{
		{"valid", "active", document, nil, nil},
		{"archived", "archived", document, nil, ErrArchived},
		{"missing", "active", document, odcscontracts.ErrNotFound, odcscontracts.ErrNotFound},
		{"invalid", "active", []byte(`{}`), nil, odcs.ErrDocument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created := false
			repo := fakeRepository{lock: func() (*model.ODCSContract, error) {
				return &model.ODCSContract{ODCSID: "orders", Status: tc.status}, tc.dependencyErr
			}, create: func(revision model.ODCSRevision) (*model.ODCSRevision, error) {
				created = true
				require.Equal(t, slug.Slug("v1"), revision.Slug)
				require.Equal(t, "recorded", revision.Status)
				return &revision, nil
			}}
			svc := New(testutil.FakeTransactionManager{}, repo, projects.NewRepository(), compiledplans.NewRepository())
			result, err := svc.CreateRevision(t.Context(), "acme", "etl", "orders", "v1", tc.raw)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
				require.Nil(t, result)
				require.False(t, created)
			} else {
				require.NoError(t, err)
				require.True(t, created)
			}
		})
	}
}

func TestRejectsInvalidCatalogMetadata(t *testing.T) {
	svc := New(testutil.FakeTransactionManager{}, fakeRepository{}, projects.NewRepository(), compiledplans.NewRepository())
	for _, input := range []model.NewODCSContract{{Slug: "orders", Name: " ", ODCSID: "orders"}, {Slug: "orders", Name: "Orders", ODCSID: ""}, {Slug: "INVALID", Name: "Orders", ODCSID: "orders"}} {
		_, err := svc.CreateContract(t.Context(), "acme", "etl", input)
		require.ErrorIs(t, err, xerror.ValidationFailed())
	}
	for _, input := range []model.UpdateODCSContract{{Name: "Orders", Status: "draft"}, {Name: " ", Status: "active"}} {
		_, err := svc.UpdateContract(t.Context(), "acme", "etl", "orders", input)
		require.ErrorIs(t, err, xerror.ValidationFailed())
	}
	require.Panics(t, func() {
		New(nil, odcscontracts.NewRepository(), projects.NewRepository(), compiledplans.NewRepository())
	})
}

func (f fakeRepository) GetRevision(_ context.Context, _ postgres.DBTX, org, project, identity, revisionSlug slug.Slug) (*model.ODCSRevision, error) {
	return f.get(org, project, identity, revisionSlug)
}

func TestValidatePinnedRevision(t *testing.T) {
	raw := []byte(`{"kind":"DataContract","apiVersion":"v3.2.0","version":"1","id":"orders","schema":[{"id":"order","name":"Orders","properties":[{"name":"id","logicalType":"integer","required":true}]}]}`)
	recorded, err := odcs.Record(raw, "orders")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, digest, version, object string
		dependencyErr, want           error
	}{
		{"valid", recorded.Digest, odcs.PinnedVersion, "order", nil, nil},
		{"unknown object", recorded.Digest, odcs.PinnedVersion, "latest", nil, nil},
		{"missing revision", recorded.Digest, odcs.PinnedVersion, "order", odcscontracts.ErrNotFound, odcscontracts.ErrNotFound},
		{"digest mismatch", "sha256:incorrect", odcs.PinnedVersion, "order", nil, xerror.Unexpected()},
		{"version mismatch", recorded.Digest, "v3.1.0", "order", nil, xerror.Unexpected()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := fakeRepository{get: func(org, project, identity, revisionSlug slug.Slug) (*model.ODCSRevision, error) {
				require.Equal(t, slug.Slug("acme"), org)
				require.Equal(t, slug.Slug("etl"), project)
				require.Equal(t, slug.Slug("orders"), identity)
				require.Equal(t, slug.Slug("v1"), revisionSlug)
				return &model.ODCSRevision{Document: recorded.Document, Digest: tc.digest, ODCSVersion: tc.version}, tc.dependencyErr
			}}
			svc := New(testutil.FakeTransactionManager{}, repo, projects.NewRepository(), compiledplans.NewRepository())
			result, err := svc.ValidateRevision(t.Context(), "acme", "etl", "orders", "v1", tc.object)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.Equal(t, recorded.Digest, result.Digest)
			if tc.object == "order" {
				require.Empty(t, result.Diagnostics)
				require.NotNil(t, result.Shape)
			} else {
				require.Nil(t, result.Shape)
				require.Equal(t, "object_not_found", result.Diagnostics[0].Code)
			}
		})
	}
}

func (f fakeRepository) CreateContract(_ context.Context, _ postgres.DBTX, _, _ slug.Slug, input model.NewODCSContract) (*model.ODCSContract, error) {
	return f.createContract(input)
}

func TestGeneratedContractIdentifiers(t *testing.T) {
	for _, custom := range []slug.Slug{"", "custom"} {
		t.Run(string(custom), func(t *testing.T) {
			calls := 0
			repo := fakeRepository{createContract: func(input model.NewODCSContract) (*model.ODCSContract, error) {
				calls++
				if calls == 1 {
					return nil, odcscontracts.ErrConflict
				}
				require.Contains(t, input.Slug.String(), "orders-")
				return &model.ODCSContract{Slug: input.Slug}, nil
			}}
			svc := New(testutil.FakeTransactionManager{}, repo, projects.NewRepository(), compiledplans.NewRepository())
			created, err := svc.CreateContract(t.Context(), "acme", "etl", model.NewODCSContract{Slug: custom, Name: "Orders", ODCSID: "orders"})
			if custom != "" {
				require.ErrorIs(t, err, odcscontracts.ErrConflict)
				require.Equal(t, 1, calls)
			} else {
				require.NoError(t, err)
				require.NotEmpty(t, created.Slug)
				require.Equal(t, 2, calls)
			}
		})
	}
}

func TestGeneratedRevisionIdentifiers(t *testing.T) {
	document := []byte(`{"kind":"DataContract","apiVersion":"v3.2.0","version":"1.2","id":"orders","schema":[{"id":"orders","name":"Orders"}]}`)
	for _, collision := range []bool{false, true} {
		repo := fakeRepository{
			lock: func() (*model.ODCSContract, error) {
				return &model.ODCSContract{ODCSID: "orders", Status: "active"}, nil
			},
			get: func(_, _, _, identity slug.Slug) (*model.ODCSRevision, error) {
				require.Equal(t, slug.Slug("revision-1-2"), identity)
				if collision {
					return &model.ODCSRevision{}, nil
				}
				return nil, odcscontracts.ErrNotFound
			},
			create: func(input model.ODCSRevision) (*model.ODCSRevision, error) {
				if collision {
					require.Contains(t, input.Slug.String(), "revision-1-2-")
				} else {
					require.Equal(t, slug.Slug("revision-1-2"), input.Slug)
				}
				return &input, nil
			},
		}
		svc := New(testutil.FakeTransactionManager{}, repo, projects.NewRepository(), compiledplans.NewRepository())
		_, err := svc.CreateRevision(t.Context(), "acme", "etl", "orders", "", document)
		require.NoError(t, err)
	}
}
