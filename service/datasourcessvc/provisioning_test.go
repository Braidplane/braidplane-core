package datasourcessvc

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/stretchr/testify/require"
)

type provisionFunc func(context.Context, postgres.DBTX, slug.Slug, *repods.CreateParams) error

func (f provisionFunc) Provision(ctx context.Context, db postgres.DBTX, org slug.Slug, p *repods.CreateParams) error {
	return f(ctx, db, org, p)
}

func TestProvisionedCreatorPersistsBeforeCanonicalReadAndPreservesFailure(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "Success", true: "InsertFailure"}[failed], func(t *testing.T) {
			t.Parallel()
			calls := []string{}
			expected := &repods.DataSource{Slug: "source"}
			failure := xerror.Unexpected().Detail("synthetic insert failure")
			params := &repods.CreateParams{Slug: "source", CredentialSlug: "credential"}
			repo := testutil.NewFakeDataSourceRepo()
			repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, org, source slug.Slug) (*repods.DataSource, error) {
				calls = append(calls, "read")
				require.Equal(t, slug.Slug("organization"), org)
				require.Equal(t, params.Slug, source)
				return expected, nil
			}
			creator := provisionedCreator{repository: repo, provisioner: provisionFunc(func(_ context.Context, _ postgres.DBTX, org slug.Slug, p *repods.CreateParams) error {
				calls = append(calls, "insert")
				require.Equal(t, slug.Slug("organization"), org)
				require.Same(t, params, p)
				if failed {
					return failure
				}
				return nil
			})}
			actual, err := creator.Create(t.Context(), nil, "organization", params)
			if failed {
				require.ErrorIs(t, err, failure)
				require.Nil(t, actual)
				require.Equal(t, []string{"insert"}, calls)
			} else {
				require.NoError(t, err)
				require.Same(t, expected, actual)
				require.Equal(t, []string{"insert", "read"}, calls)
			}
		})
	}
}

func TestCreateProvisionedRequiresExplicitPersistence(t *testing.T) {
	t.Parallel()
	svc := newTestService(testutil.NewFakeDataSourceRepo(), testutil.NewFakeSchemaMetadataRepo(), testutil.NewFakeEncryptor(), nil)
	_, err := svc.CreateProvisioned(t.Context(), "organization", model.SystemEventActor(), &CreateParams{}, nil)
	require.ErrorContains(t, err, "trusted source provisioner is required")
}
