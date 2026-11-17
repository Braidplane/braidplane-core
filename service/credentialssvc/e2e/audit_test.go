package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	repoevents "github.com/Braidplane/braidplane-core/postgres/events"
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

	auditClass := model.EventClassSecurityAudit
	action := model.EventActionRotate
	eventService := eventssvc.New(postgres.NewTransactionManager(pool), repoevents.New(), nil)
	filters := eventssvc.ListEventsFilters{EventClass: &auditClass, EntitySlug: &source, Action: &action}
	own, err := eventService.ListEvents(ctx, org, filters, model.ListRequest{})
	require.NoError(t, err)
	require.Len(t, own.Items, 1)
	event := own.Items[0]
	require.Equal(t, model.EventActorTypeUser, event.ActorType)
	require.Equal(t, actor, *event.ActorSlug)
	require.Equal(t, model.EventEntityTypeDataSource, event.EntityType)
	require.Equal(t, source, *event.EntitySlug)
	require.IsType(t, "", event.Metadata["version"])
	require.Len(t, event.Metadata["version"], 64)
	require.NotContains(t, fmt.Sprint(event.Metadata), marker)
	require.NotNil(t, event.RequestID)
	require.Equal(t, reqctx.AuditOperation(ctx), *event.RequestID)
	require.Len(t, *event.RequestID, 64)
	require.NotContains(t, *event.RequestID, marker)
	peer, err := eventService.ListEvents(ctx, other, filters, model.ListRequest{})
	require.NoError(t, err)
	require.Empty(t, peer.Items)
	verifyCredentialAuditExport(t, eventService, org, other, event.Slug, marker)
}
