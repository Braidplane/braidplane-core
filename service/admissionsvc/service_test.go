package admissionsvc

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/stretchr/testify/require"
)

func TestCommunityAdmissionOnlyRequiresExistingOrganization(t *testing.T) {
	t.Parallel()
	for _, failure := range []error{nil, organizations.ErrNotFound} {
		repo := &testutil.FakeOrgsRepo{FindBySlugFn: func(_ context.Context, _ postgres.DBTX, org slug.Slug) (*organizations.Organization, error) {
			require.Equal(t, slug.Slug("acme"), org)
			return &organizations.Organization{Slug: org}, failure
		}}
		for range 2 {
			svc := New(testutil.FakeTransactionManager{}, repo)
			require.NoError(t, svc.InitializeOrganization(t.Context(), "acme"))
			require.NoError(t, svc.DataSourceConnected(t.Context(), "acme", "source", time.Now()))
			for _, check := range []func(context.Context, slug.Slug) error{
				svc.CheckWriteAccess, svc.CheckMemberAdmission, svc.CheckProjectAdmission, svc.CheckDataSourceAdmission,
				svc.CheckDeploymentAdmission, svc.CheckRunAccess, svc.CheckPipelineRunAdmission,
			} {
				require.ErrorIs(t, check(t.Context(), "acme"), failure)
			}
			require.ErrorIs(t, svc.CheckPipelineDeploymentAdmission(t.Context(), "acme", "project", "pipeline"), failure)
			called := false
			err := svc.WithPipelineExecution(t.Context(), "acme", false, func(context.Context) error { called = true; return nil })
			require.ErrorIs(t, err, failure)
			require.Equal(t, failure == nil, called)
		}
	}
}
