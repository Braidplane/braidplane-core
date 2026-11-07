package datasourcessvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
)

func TestConnectionSuccessDelegatesSourcePolicyAndPreservesFailure(t *testing.T) {
	for _, failure := range []error{nil, xerror.Unexpected().Detail("synthetic policy failure")} {
		repo := testutil.NewFakeDataSourceRepo()
		finalized := &repods.DataSource{Slug: "ledger"}
		testedAt := time.Now().UTC()
		repo.MarkTestSuccessFn = func(_ context.Context, _ postgres.DBTX, org, source slug.Slug, params *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
			require.Equal(t, slug.Slug("tester"), org)
			require.Equal(t, finalized.Slug, source)
			require.Equal(t, testedAt, params.TestedAt)
			return finalized, nil
		}
		calls := 0
		svc := newTestService(repo, testutil.NewFakeSchemaMetadataRepo(), testutil.NewFakeEncryptor(), nil).(*service)
		svc.admission = &activationPolicy{activate: func(_ context.Context, org, source slug.Slug, at time.Time) error {
			calls++
			require.Equal(t, slug.Slug("tester"), org)
			require.Equal(t, finalized.Slug, source)
			require.Equal(t, testedAt, at)
			return failure
		}}
		actual, err := svc.recordConnectionSuccess(t.Context(), nil, "tester", finalized.Slug, &repods.MarkTestSuccessParams{TestedAt: testedAt})
		require.Equal(t, 1, calls)
		if failure != nil {
			require.ErrorIs(t, err, failure)
			require.Nil(t, actual)
		} else {
			require.NoError(t, err)
			require.Same(t, finalized, actual)
		}
	}
}

func TestConnectionSuccessRejectsConflictBeforeTrialActivation(t *testing.T) {
	repo := testutil.NewFakeDataSourceRepo()
	repo.MarkTestSuccessFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
		return nil, repods.ErrConnectionTestConflict
	}
	svc := newTestService(repo, testutil.NewFakeSchemaMetadataRepo(), testutil.NewFakeEncryptor(), nil).(*service)
	svc.admission = &activationPolicy{activate: func(context.Context, slug.Slug, slug.Slug, time.Time) error {
		t.Fatal("a stale connection test must not invoke source-connected policy")
		return nil
	}}

	_, err := svc.recordConnectionSuccess(t.Context(), nil, "tester", "ledger", &repods.MarkTestSuccessParams{})
	require.ErrorIs(t, err, repods.ErrConnectionTestConflict)
}

type activationPolicy struct {
	admissionsvc.Service
	activate func(context.Context, slug.Slug, slug.Slug, time.Time) error
}

func (p *activationPolicy) DataSourceConnected(ctx context.Context, org, source slug.Slug, at time.Time) error {
	return p.activate(ctx, org, source, at)
}
