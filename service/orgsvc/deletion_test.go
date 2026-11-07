package orgsvc

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/stretchr/testify/require"
)

func TestDeleteOrganizationPreservesEvidenceBeforeDeletion(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		blocked    bool
		archiveErr error
		wantErr    error
	}{
		{name: "Deletes"},
		{name: "Blocked", blocked: true, wantErr: ErrDeletionBlocked},
		{name: "ArchiveFails", archiveErr: xerror.Unexpected(), wantErr: xerror.Unexpected()},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var calls []string
			repo := &testutil.FakeOrgsRepo{
				FindBySlugForUpdateFn: func(_ context.Context, _ postgres.DBTX, s slug.Slug) (*organizations.Organization, error) {
					require.Equal(t, slug.Slug("acme"), s)
					calls = append(calls, "lock")
					return &organizations.Organization{Slug: s}, nil
				},
				DeletionBlockedFn: func(context.Context, postgres.DBTX, slug.Slug) (bool, error) {
					calls = append(calls, "check")
					return test.blocked, nil
				},
				DeleteFn: func(context.Context, postgres.DBTX, slug.Slug) error {
					calls = append(calls, "delete")
					return nil
				},
			}
			svc := newTestService(repo, &fakes.MembersService{}).(*service)
			svc.beforeDelete = func(context.Context, postgres.DBTX, slug.Slug) error {
				calls = append(calls, "archive")
				return test.archiveErr
			}
			err := svc.DeleteOrganization(context.Background(), "acme")
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
			}
			want := []string{"lock", "check"}
			if !test.blocked {
				want = append(want, "archive")
			}
			if test.wantErr == nil {
				want = append(want, "delete")
			}
			require.Equal(t, want, calls)
		})
	}
}
