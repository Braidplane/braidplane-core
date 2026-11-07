package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/stretchr/testify/require"
)

func TestCommunityRunsWithoutCommercialSchema(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "Community admission without commercial schema")
	var commercialTable *string
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('public.organization_entitlements')::text`).Scan(&commercialTable))
	require.Nil(t, commercialTable)
	org := slug.Slug(fmt.Sprintf("community-admission-%d", time.Now().UnixNano()))
	_, err := pool.Exec(ctx, `INSERT INTO organizations(name, slug) VALUES ('Synthetic Community', $1)`, org)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug=$1`, org)
		require.NoError(t, err)
	})
	for range 2 {
		svc := admissionsvc.New(postgres.NewTransactionManager(pool), organizations.NewRepository())
		require.NoError(t, svc.InitializeOrganization(ctx, org))
		for _, check := range []func(context.Context, slug.Slug) error{
			svc.CheckWriteAccess, svc.CheckMemberAdmission, svc.CheckProjectAdmission, svc.CheckDataSourceAdmission,
			svc.CheckDeploymentAdmission, svc.CheckRunAccess, svc.CheckPipelineRunAdmission,
		} {
			require.NoError(t, check(ctx, org))
			require.Error(t, check(ctx, "missing-community-organization"))
		}
		require.NoError(t, svc.CheckPipelineDeploymentAdmission(ctx, org, "project", "pipeline"))
		called := false
		require.NoError(t, svc.WithPipelineExecution(ctx, org, false, func(context.Context) error { called = true; return nil }))
		require.True(t, called)
	}
}
