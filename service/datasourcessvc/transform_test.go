package datasourcessvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

// TestToCredentialMetadataFromRepoRejectsInvalidLifecycleValues verifies persisted corruption returns a typed internal error.
func TestToCredentialMetadataFromRepoRejectsInvalidLifecycleValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata *repods.CredentialMetadata
	}{
		{
			name: "InvalidKind",
			metadata: &repods.CredentialMetadata{
				Slug:   must.Slug("hubspot-credential"),
				Kind:   "unknown_kind",
				Status: credentialssvc.CredentialStatusActive.String(),
			},
		},
		{
			name: "InvalidStatus",
			metadata: &repods.CredentialMetadata{
				Slug:   must.Slug("hubspot-credential"),
				Kind:   credentialssvc.CredentialKindOAuth2Grant.String(),
				Status: "unknown_status",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := toCredentialMetadataFromRepo(test.metadata)

			require.Error(t, err)
			assert.Nil(t, got)
			var typedErr *xerror.Error
			require.ErrorAs(t, err, &typedErr)
			assert.Equal(t, xerror.KindInternal, typedErr.Kind())
		})
	}
}

// TestServiceReadsPropagateCredentialMetadataConversionErrors verifies find and list fail safely on malformed persisted metadata.
func TestServiceReadsPropagateCredentialMetadataConversionErrors(t *testing.T) {
	t.Parallel()

	malformed := &repods.DataSource{
		Slug: must.Slug("hubspot-crm"),
		Credential: &repods.CredentialMetadata{
			Slug:   must.Slug("hubspot-credential"),
			Kind:   "unknown_kind",
			Status: credentialssvc.CredentialStatusActive.String(),
		},
	}
	tests := []struct {
		name string
		run  func(Service) error
	}{
		{
			name: "FindBySlug",
			run: func(service Service) error {
				_, err := service.FindBySlug(context.Background(), must.Slug("acme"), malformed.Slug)
				return err
			},
		},
		{
			name: "ListByOrg",
			run: func(service Service) error {
				_, err := service.ListByOrg(context.Background(), must.Slug("acme"), model.ListRequest{})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			repo := testutil.NewFakeDataSourceRepo()
			repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
				return malformed, nil
			}
			repo.ListByOrgFn = func(context.Context, postgres.DBTX, slug.Slug, model.ListRequest) ([]*repods.DataSource, error) {
				return []*repods.DataSource{malformed}, nil
			}
			service := newTestService(repo, testutil.NewFakeSchemaMetadataRepo(), nil, nil)

			err := test.run(service)

			require.Error(t, err)
			var typedErr *xerror.Error
			require.ErrorAs(t, err, &typedErr)
			assert.Equal(t, xerror.KindInternal, typedErr.Kind())
		})
	}
}

// TestToCredentialMetadataFromRepoPreservesSafeMetadata verifies valid persisted metadata remains unchanged.
func TestToCredentialMetadataFromRepoPreservesSafeMetadata(t *testing.T) {
	t.Parallel()

	updatedAt := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	metadata := &repods.CredentialMetadata{
		Slug:      must.Slug("hubspot-credential"),
		Kind:      credentialssvc.CredentialKindOAuth2Grant.String(),
		Status:    credentialssvc.CredentialStatusActive.String(),
		Provider:  "hubspot",
		Scopes:    []string{"crm.objects.contacts.read"},
		UpdatedAt: updatedAt,
	}

	got, err := toCredentialMetadataFromRepo(metadata)

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, credentialssvc.CredentialKindOAuth2Grant, got.Kind)
	assert.Equal(t, credentialssvc.CredentialStatusActive, got.Status)
	assert.Equal(t, updatedAt, got.UpdatedAt)
	assert.Equal(t, []string{"crm.objects.contacts.read"}, got.OAuth.Scopes)
}
