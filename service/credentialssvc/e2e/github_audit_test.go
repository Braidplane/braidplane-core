package e2e

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	githubprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/github"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/githubapps"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/githubappsvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
	"github.com/stretchr/testify/require"
)

type unavailableGitHubAudit struct{}

var errGitHubAuditFixture = xerror.Unavailable(xerror.Event)

func (unavailableGitHubAudit) CreateEvent(context.Context, eventssvc.CreateEventParams) (*model.Event, error) {
	return nil, errGitHubAuditFixture
}

func TestGitHubLifecycleCommitsWithAuditHandoff(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "GitHub App lifecycle credential audit")
	org := uniqueCredentialE2ESlug("github-audit")
	var installation int64
	require.NoError(t, pool.QueryRow(ctx, `
        WITH owner AS (INSERT INTO organizations(slug, name) VALUES ($1, 'GitHub audit') RETURNING id)
        INSERT INTO github_app_installations(organization_id, installation_id, account_id, account_login, account_type, repository_selection)
        SELECT id, id, id, 'synthetic', 'Organization', 'selected' FROM owner RETURNING installation_id`, org).Scan(&installation))
	txm := postgres.NewTransactionManager(pool)
	provider, err := githubappsvc.NewGitHubProvider(0, "", "", "", "")
	require.NoError(t, err)
	source := githubapps.New()
	svc, err := githubappsvc.New(txm, source, provider, eventtest.NewProducer(unavailableGitHubAudit{}), "")
	require.NoError(t, err)
	lifecycle := githubprovider.InstallationLifecycleEvent{Action: "suspend", InstallationID: installation}
	require.ErrorIs(t, svc.ApplyLifecycle(ctx, lifecycle), errGitHubAuditFixture)
	var suspended bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT suspended_at IS NOT NULL FROM github_app_installations WHERE installation_id=$1", installation).Scan(&suspended))
	require.False(t, suspended)

	queueClient, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	producer, err := workerproducer.NewEventProducer(queue.NewEnqueuer(queueClient), clock.New())
	require.NoError(t, err)
	svc, err = githubappsvc.New(txm, source, provider, producer, "")
	require.NoError(t, err)
	require.NoError(t, svc.ApplyLifecycle(ctx, lifecycle))

	var version, change string
	require.NoError(t, pool.QueryRow(ctx, `
        SELECT installation.suspended_at IS NOT NULL, job.args->'metadata'->>'version', job.args->'metadata'->>'change'
        FROM github_app_installations installation JOIN organizations org ON org.id=installation.organization_id
        JOIN river_job job ON job.args->>'org_slug'=org.slug AND job.kind='event_record'
        WHERE installation.installation_id=$1`, installation).Scan(&suspended, &version, &change))
	require.True(t, suspended)
	require.Equal(t, "suspend", change)
	require.Len(t, version, 64)
}
