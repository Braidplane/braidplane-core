package credentialssvc

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

type credentialAuditStub func(context.Context, slug.Slug, model.EventActor, eventrecorder.Event) (*model.Event, error)

func (f credentialAuditStub) Record(ctx context.Context, org slug.Slug, actor model.EventActor, event eventrecorder.Event) (*model.Event, error) {
	return f(ctx, org, actor, event)
}

func newUnitCredentialService(txm postgres.TransactionManager, repo repocredentials.Repository, cipher crypto.CredentialCipher, providers ProviderRegistry, clk clock.Clock, allowPrivate bool) (FullService, error) {
	svc, err := New(txm, repo, cipher, providers, clk, allowPrivate)
	if err != nil {
		return nil, err
	}
	concrete := svc.(*service)
	concrete.audit = credentialAuditStub(func(context.Context, slug.Slug, model.EventActor, eventrecorder.Event) (*model.Event, error) {
		return nil, nil
	})
	return concrete, nil
}

func TestCredentialReleaseRequiresAudit(t *testing.T) {
	t.Parallel()
	repo := &encryptionStaticRepo{credentialRepo: newCredentialRepo()}
	credentials, err := newUnitCredentialService(testutil.FakeTransactionManager{}, repo, realCredentialTestCipher(t, "current"), nil, testutil.FakeClock{NowFn: time.Now}, false)
	require.NoError(t, err)
	svc := credentials.(*service)
	owner := testOwner()
	const marker = "synthetic-runtime-audit-credential"
	_, err = svc.CreateStaticSecret(t.Context(), CreateStaticSecretParams{
		Owner: owner, CredentialType: StaticCredentialTypePostgres, PlaintextJSON: []byte(marker),
	})
	require.NoError(t, err)
	repo.record = repo.staticSecrets[0]

	ctx := reqctx.WithAuditOperation(t.Context(), "trusted-operation")
	actor := model.UserEventActor("operator")
	ctx = reqctx.WithAuditActor(ctx, actor)
	var eventSlug slug.Slug
	auditFailure := true
	svc.audit = credentialAuditStub(func(_ context.Context, org slug.Slug, actualActor model.EventActor, event eventrecorder.Event) (*model.Event, error) {
		record, err := event.EventRecord()
		require.NoError(t, err)
		require.Equal(t, owner.OrgSlug, org)
		require.Equal(t, actor, actualActor)
		require.Equal(t, owner.DataSourceSlug, record.EntitySlug)
		require.Equal(t, model.EventActionRead, record.Action)
		require.Equal(t, "released", record.Metadata["outcome"])
		require.NotContains(t, fmt.Sprint(record.Metadata), marker)
		if eventSlug == "" {
			eventSlug = record.Slug
		}
		require.Equal(t, eventSlug, record.Slug)
		if auditFailure {
			return nil, errors.New("synthetic audit recorder failure")
		}
		return nil, eventssvc.ErrSlugTaken
	})

	runtime, err := svc.ResolveRuntimeCredential(ctx, ResolveRuntimeCredentialParams{Owner: owner})
	require.ErrorIs(t, err, ErrCredentialAuditUnavailable)
	require.Empty(t, runtime.PlaintextJSON)

	auditFailure = false
	runtime, err = svc.ResolveRuntimeCredential(ctx, ResolveRuntimeCredentialParams{Owner: owner})
	require.NoError(t, err)
	require.Equal(t, marker, string(runtime.PlaintextJSON))
}
