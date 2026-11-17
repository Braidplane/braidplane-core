package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

func TestStaticCredentialLifecycleRequiresTenantAudit(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "transactional credential audit")
	svc := newCredentialService(t, pool, time.Now(), &fakeProviderAdapter{})
	org := uniqueCredentialE2ESlug("credential-audit")
	other := uniqueCredentialE2ESlug("other-credential-audit")
	source := uniqueCredentialE2ESlug("source-credential-audit")
	seedCredentialLifecycleDataSource(t, pool, org, source)
	seedCredentialLifecycleDataSource(t, pool, other, source)

	const marker = "synthetic-credential-marker"
	ctx = reqctx.WithRequestID(ctx, marker)
	ctx = reqctx.WithAuditOperation(ctx, "server-owned-audit-operation")
	actor := uniqueCredentialE2ESlug("audit-actor")
	params := credentialssvc.CreateStaticSecretParams{
		Owner:          credentialOwner(org, source),
		CredentialType: credentialssvc.StaticCredentialTypePostgres,
		PlaintextJSON:  []byte(`{"password":"` + marker + `"}`),
		Actor:          model.EventActor{Type: model.EventActorTypeUser},
		Replace:        true,
	}
	_, err := svc.CreateStaticSecret(ctx, params)
	require.ErrorIs(t, err, eventssvc.ErrInvalidInput)

	var credentials int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM credentials c
        JOIN organizations o ON o.id = c.organization_id WHERE o.slug = $1`, org).Scan(&credentials))
	require.Equal(t, 1, credentials, "failed audit must roll back the replacement credential")

	params.Actor = model.UserEventActor(actor)
	_, err = svc.CreateStaticSecret(ctx, params)
	require.NoError(t, err)

	var version, metadata, correlation string
	require.NoError(t, pool.QueryRow(ctx, `SELECT e.metadata->>'version', e.metadata::text, e.request_id
        FROM events e JOIN organizations o ON o.id = e.organization_id
        WHERE o.slug = $1 AND e.actor_type = 'user' AND e.actor_slug = $2
          AND e.entity_type = 'data_source' AND e.entity_slug = $3
          AND e.event_class = 'security_audit' AND e.action = 'rotate'`, org, actor, source).Scan(&version, &metadata, &correlation))
	require.Len(t, version, 64)
	require.NotContains(t, metadata, marker)
	require.Len(t, correlation, 64)
	require.NotContains(t, correlation, marker)
	var unrelated int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM events e
        JOIN organizations o ON o.id = e.organization_id WHERE o.slug = $1 AND e.entity_slug = $2
          AND e.slug LIKE 'evt-credential-lifecycle-%'`, other, source).Scan(&unrelated))
	require.Zero(t, unrelated)
}
