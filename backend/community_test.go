package backend

import (
	"bytes"
	"context"
	"testing"

	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestCommunityCompositionKeepsUncappedPolicyAndOperationErrors(t *testing.T) {
	t.Parallel()
	pool := &pgxpool.Pool{}
	distribution, err := NewCommunityDistribution(Resources{Config: &backendconfig.Config{}, Pool: pool, TransactionManager: postgres.NewTransactionManager(pool)})
	require.NoError(t, err)
	operationErr := xerror.Unexpected().Detail("synthetic operation failure")
	err = distribution.WithOperation(t.Context(), "organization", "source", func(ctx context.Context) error {
		require.Equal(t, t.Context(), ctx)
		return operationErr
	})
	require.ErrorIs(t, err, operationErr)
	calls := 0
	source := &datasourcessvc.DataSource{Slug: "source"}
	sourceParams := &datasourcessvc.CreateParams{Slug: "source"}
	actual, err := distribution.CreateRepositorySource(t.Context(), &fakes.DataSourceService{CreateFn: func(_ context.Context, org slug.Slug, actor model.EventActor, p *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
		calls++
		require.Equal(t, slug.Slug("organization"), org)
		require.Equal(t, model.UserEventActor("member"), actor)
		require.Same(t, sourceParams, p)
		return source, nil
	}}, "organization", "project", model.UserEventActor("member"), sourceParams)
	require.NoError(t, err)
	require.Same(t, source, actual)
	require.Equal(t, 1, calls)
}

func TestCommunityCLIExcludesPrivateCommandsBeforeLoadingRuntime(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	require.NoError(t, RunCLI([]string{"--help"}, &stdout, &stderr))
	require.Contains(t, stdout.String(), "executor")
	require.NotContains(t, stdout.String(), "admin-worker")
	require.NotContains(t, stdout.String(), "settlement-review")
	for _, command := range []string{"admin-worker", "settlement-review"} {
		stdout.Reset()
		stderr.Reset()
		require.ErrorContains(t, RunCLI([]string{command}, &stdout, &stderr), "parse backend command")
	}
}

func TestRunRejectsMissingDistributionAndInvalidModeBeforeStartup(t *testing.T) {
	t.Parallel()
	require.ErrorContains(t, Run(nil, ModeAPI, NewCommunityDistribution), "configuration and distribution factory are required")
	require.ErrorContains(t, Run(&backendconfig.Config{}, ModeAPI, nil), "configuration and distribution factory are required")
	calls := 0
	require.ErrorContains(t, Run(&backendconfig.Config{}, Mode(255), func(Resources) (Distribution, error) {
		calls++
		return nil, xerror.Unexpected().Detail("invalid mode must not initialize a distribution")
	}), "invalid backend runtime mode")
	require.Zero(t, calls)
}

func TestCommunityAccountPolicyRequiresNoAgreement(t *testing.T) {
	pool := &pgxpool.Pool{}
	distribution, err := NewCommunityDistribution(Resources{Config: &backendconfig.Config{}, Pool: pool, TransactionManager: postgres.NewTransactionManager(pool)})
	require.NoError(t, err)
	account := distribution.AccountPolicy(nil)
	require.Empty(t, account.Users)
	require.Empty(t, account.Members)
	require.Empty(t, account.Organizations)
	require.Empty(t, account.AccountClosure)
}
