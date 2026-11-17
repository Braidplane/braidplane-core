package githubappsvc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/githubapps"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

type githubAuditRecorder func(context.Context, slug.Slug, model.EventActor, eventrecorder.Event) (*model.Event, error)

func (f githubAuditRecorder) Record(ctx context.Context, org slug.Slug, actor model.EventActor, event eventrecorder.Event) (*model.Event, error) {
	return f(ctx, org, actor, event)
}

var successfulGithubAudit = githubAuditRecorder(func(context.Context, slug.Slug, model.EventActor, eventrecorder.Event) (*model.Event, error) {
	return &model.Event{}, nil
})

func TestInstallationTokenReleaseRequiresAudit(t *testing.T) {
	t.Parallel()
	const marker = "synthetic-github-app-material"
	ctx := reqctx.WithAuditOperation(t.Context(), "server-job")
	ctx = reqctx.WithRequestID(ctx, marker)
	actor := model.APIKeyEventActor("actor")
	ctx = reqctx.WithAuditActor(ctx, actor)
	provider := &fakeProvider{token: InstallationToken{Token: marker}}
	svc, err := New(testutil.FakeTransactionManager{}, &fakeRepository{
		installation: &githubapps.Installation{InstallationID: 9},
		repositories: []githubapps.RepositoryBinding{{ID: 42}, {ID: 73}},
	}, provider, &fakeEvents{}, "")
	require.NoError(t, err)

	var previous slug.Slug
	auditErr := xerror.Unexpected().Detail(marker)
	svc.audit = githubAuditRecorder(func(ctx context.Context, org slug.Slug, gotActor model.EventActor, event eventrecorder.Event) (*model.Event, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, actor, gotActor)
		record, err := event.EventRecord()
		require.NoError(t, err)
		require.Equal(t, model.EventActionRead, record.Action)
		require.Equal(t, "released", record.Metadata["outcome"])
		require.Len(t, record.Metadata["version"], 64)
		payload, err := json.Marshal(record)
		require.NoError(t, err)
		require.NotContains(t, string(payload), marker)
		require.NotEqual(t, marker, *reqctx.GetRequestID(ctx))
		if previous == "" {
			previous = record.Slug
		}
		require.Equal(t, previous, record.Slug)
		return nil, auditErr
	})

	token, err := svc.Token(ctx, "acme", []int64{42, 73})
	require.ErrorIs(t, err, credentialssvc.ErrCredentialAuditUnavailable)
	require.NotContains(t, err.Error(), marker)
	require.Empty(t, token.Token)

	auditErr = eventssvc.ErrSlugTaken
	token, err = svc.Token(ctx, "acme", []int64{73, 42})
	require.NoError(t, err)
	require.Equal(t, marker, token.Token)
}
