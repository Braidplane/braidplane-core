package eventssvc

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	repoevents "github.com/Braidplane/braidplane-core/postgres/events"
)

var errUnexpectedEventsRepoCall = fmt.Errorf("unexpected events repository call")

type fakeEventsRepo struct {
	createFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		params *repoevents.CreateParams,
	) (*repoevents.Event, error)
	findBySlugFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, eventSlug slug.Slug,
	) (*repoevents.Event, error)
	listByOrganizationFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		filters *repoevents.ListFilters,
		req model.ListRequest,
	) ([]*repoevents.Event, error)
	listSourceRefsByOrganizationFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		filters repoevents.SourceMetadataFilters,
		limit int,
	) ([]model.EventSourceRefOption, error)
	pruneOlderThanFn func(
		ctx context.Context,
		tx postgres.DBTX,
		cutoff time.Time,
	) (int64, error)
}

func (f *fakeEventsRepo) Create(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	params *repoevents.CreateParams,
) (*repoevents.Event, error) {
	return f.createFn(ctx, tx, orgSlug, params)
}

func (f *fakeEventsRepo) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, eventSlug slug.Slug,
) (*repoevents.Event, error) {
	return f.findBySlugFn(ctx, tx, orgSlug, eventSlug)
}

func (f *fakeEventsRepo) ListByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	filters *repoevents.ListFilters,
	req model.ListRequest,
) ([]*repoevents.Event, error) {
	return f.listByOrganizationFn(ctx, tx, orgSlug, filters, req)
}

func (f *fakeEventsRepo) ListSourceRefsByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	filters repoevents.SourceMetadataFilters,
	limit int,
) ([]model.EventSourceRefOption, error) {
	if f.listSourceRefsByOrganizationFn == nil {
		return nil, errUnexpectedEventsRepoCall
	}
	return f.listSourceRefsByOrganizationFn(ctx, tx, orgSlug, filters, limit)
}

func (f *fakeEventsRepo) PruneOlderThan(
	ctx context.Context,
	tx postgres.DBTX,
	cutoff time.Time,
) (int64, error) {
	return f.pruneOlderThanFn(ctx, tx, cutoff)
}

type recordingEventsTransactionManager struct {
	testutil.FakeTransactionManager
	readCalls  int
	writeCalls int
}

func (m *recordingEventsTransactionManager) RunInTx(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	m.writeCalls++
	return m.FakeTransactionManager.RunInTx(ctx, fn)
}

func (m *recordingEventsTransactionManager) RunRead(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	m.readCalls++
	return m.FakeTransactionManager.RunRead(ctx, fn)
}

func TestService_CreateEventPersistsValidatedEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	eventSlug := must.Slug("evt-project-deleted-001")
	actorSlug := must.Slug("alice")
	entitySlug := must.Slug("ops")
	now := time.Date(2026, time.May, 16, 15, 0, 0, 0, time.UTC)
	metadata := map[string]any{
		"reason":         "cleanup",
		"resource_count": float64(3),
	}

	svc := New(testutil.FakeTransactionManager{}, &fakeEventsRepo{
		createFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			params *repoevents.CreateParams,
		) (*repoevents.Event, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, eventSlug, params.Slug)
			require.Equal(t, model.EventActorTypeUser, params.ActorType)
			require.Equal(t, actorSlug, *params.ActorSlug)
			require.Equal(t, model.EventEntityTypeProject, params.EntityType)
			require.Equal(t, entitySlug, *params.EntitySlug)
			require.Equal(t, model.EventActionDelete, params.Action)
			require.Equal(t, now, params.OccurredAt)
			require.JSONEq(t, `{"reason":"cleanup","resource_count":3}`, string(params.Metadata))

			return &repoevents.Event{
				Slug:       params.Slug,
				ActorType:  params.ActorType,
				ActorSlug:  params.ActorSlug,
				EntityType: params.EntityType,
				EntitySlug: params.EntitySlug,
				Action:     params.Action,
				OccurredAt: params.OccurredAt,
				Metadata:   params.Metadata,
				CreatedAt:  now,
			}, nil
		},
		findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repoevents.Event, error) {
			return nil, errUnexpectedEventsRepoCall
		},
		listByOrganizationFn: func(context.Context, postgres.DBTX, slug.Slug, *repoevents.ListFilters, model.ListRequest) ([]*repoevents.Event, error) {
			return nil, errUnexpectedEventsRepoCall
		},
		pruneOlderThanFn: func(context.Context, postgres.DBTX, time.Time) (int64, error) {
			return 0, errUnexpectedEventsRepoCall
		},
	}, testutil.FakeClock{NowFn: func() time.Time { return now }})

	got, err := svc.CreateEvent(context.Background(), CreateEventParams{
		OrgSlug:    orgSlug,
		Slug:       eventSlug,
		ActorType:  model.EventActorTypeUser,
		ActorSlug:  &actorSlug,
		EntityType: model.EventEntityTypeProject,
		EntitySlug: &entitySlug,
		Action:     model.EventActionDelete,
		Metadata:   metadata,
	})
	require.NoError(t, err)
	require.Equal(t, eventSlug, got.Slug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeProject, got.EntityType)
	require.Equal(t, entitySlug, *got.EntitySlug)
	require.Equal(t, model.EventActionDelete, got.Action)
	require.Equal(t, now, got.OccurredAt)
	require.Equal(t, now, got.CreatedAt)
	require.Equal(t, "cleanup", got.Metadata["reason"])
	require.InEpsilon(t, 3.0, got.Metadata["resource_count"], 0.0001)
}

func TestService_CreateEventAllowsExplicitlyShapedMetadata(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	eventSlug := must.Slug("evt-project-deleted-001")
	entitySlug := must.Slug("ops")
	now := time.Date(2026, time.May, 16, 15, 0, 0, 0, time.UTC)

	svc := New(testutil.FakeTransactionManager{}, &fakeEventsRepo{
		createFn: func(
			_ context.Context,
			_ postgres.DBTX,
			_ slug.Slug,
			params *repoevents.CreateParams,
		) (*repoevents.Event, error) {
			require.JSONEq(
				t,
				`{"request_headers":{"authorization":"redacted"},"typed_headers":{"token":"redacted"}}`,
				string(params.Metadata),
			)

			return &repoevents.Event{
				Slug:       params.Slug,
				ActorType:  params.ActorType,
				EntityType: params.EntityType,
				EntitySlug: params.EntitySlug,
				Action:     params.Action,
				OccurredAt: params.OccurredAt,
				Metadata:   params.Metadata,
				CreatedAt:  now,
			}, nil
		},
		findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repoevents.Event, error) {
			return nil, errUnexpectedEventsRepoCall
		},
		listByOrganizationFn: func(context.Context, postgres.DBTX, slug.Slug, *repoevents.ListFilters, model.ListRequest) ([]*repoevents.Event, error) {
			return nil, errUnexpectedEventsRepoCall
		},
		pruneOlderThanFn: func(context.Context, postgres.DBTX, time.Time) (int64, error) {
			return 0, errUnexpectedEventsRepoCall
		},
	}, testutil.FakeClock{NowFn: func() time.Time { return now }})

	_, err := svc.CreateEvent(context.Background(), CreateEventParams{
		OrgSlug:    orgSlug,
		Slug:       eventSlug,
		ActorType:  model.EventActorTypeSystem,
		EntityType: model.EventEntityTypeProject,
		EntitySlug: &entitySlug,
		Action:     model.EventActionDelete,
		Metadata: map[string]any{
			"request_headers": map[string]any{
				"authorization": "redacted",
			},
			"typed_headers": map[string]string{
				"token": "redacted",
			},
		},
	})
	require.NoError(t, err)
}

func TestService_CreateEventRejectsInvalidContracts(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	eventSlug := must.Slug("evt-invalid-001")
	entitySlug := must.Slug("ops")
	emptySlug := slug.Slug("")
	now := time.Date(2026, time.May, 16, 15, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		params    CreateEventParams
		assertErr func(t *testing.T, err error)
	}{
		{
			name: "RejectsUnsupportedActorType",
			params: CreateEventParams{
				OrgSlug:    orgSlug,
				Slug:       eventSlug,
				ActorType:  model.EventActorType(99),
				EntityType: model.EventEntityTypeProject,
				EntitySlug: &entitySlug,
				Action:     model.EventActionDelete,
			},
			assertErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, ErrInvalidInput)
				require.ErrorContains(t, err, "eventactortype")
			},
		},
		{
			name: "RejectsUserActorWithoutSlug",
			params: CreateEventParams{
				OrgSlug:    orgSlug,
				Slug:       eventSlug,
				ActorType:  model.EventActorTypeUser,
				EntityType: model.EventEntityTypeProject,
				EntitySlug: &entitySlug,
				Action:     model.EventActionDelete,
			},
			assertErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, ErrInvalidInput)
				require.ErrorContains(t, err, "actor_slug")
			},
		},
		{
			name: "RejectsUserActorWithEmptySlug",
			params: CreateEventParams{
				OrgSlug:    orgSlug,
				Slug:       eventSlug,
				ActorType:  model.EventActorTypeUser,
				ActorSlug:  &emptySlug,
				EntityType: model.EventEntityTypeProject,
				EntitySlug: &entitySlug,
				Action:     model.EventActionDelete,
			},
			assertErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, ErrInvalidInput)
				require.ErrorContains(t, err, "actor_slug")
			},
		},
		{
			name: "RejectsUnsupportedEntityType",
			params: CreateEventParams{
				OrgSlug:    orgSlug,
				Slug:       eventSlug,
				ActorType:  model.EventActorTypeSystem,
				EntityType: model.EventEntityType(99),
				Action:     model.EventActionDelete,
			},
			assertErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, ErrInvalidInput)
				require.ErrorContains(t, err, "evententitytype")
			},
		},
		{
			name: "RejectsEntityWithEmptySlug",
			params: CreateEventParams{
				OrgSlug:    orgSlug,
				Slug:       eventSlug,
				ActorType:  model.EventActorTypeSystem,
				EntityType: model.EventEntityTypeProject,
				EntitySlug: &emptySlug,
				Action:     model.EventActionDelete,
			},
			assertErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, ErrInvalidInput)
				require.ErrorContains(t, err, "entity_slug")
			},
		},
		{
			name: "RejectsUnsupportedAction",
			params: CreateEventParams{
				OrgSlug:    orgSlug,
				Slug:       eventSlug,
				ActorType:  model.EventActorTypeSystem,
				EntityType: model.EventEntityTypeProject,
				EntitySlug: &entitySlug,
				Action:     model.EventAction(99),
			},
			assertErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, ErrInvalidInput)
				require.ErrorContains(t, err, "action")
			},
		},
		{
			name: "RejectsUnmarshalableMetadata",
			params: CreateEventParams{
				OrgSlug:    orgSlug,
				Slug:       eventSlug,
				ActorType:  model.EventActorTypeSystem,
				EntityType: model.EventEntityTypeProject,
				EntitySlug: &entitySlug,
				Action:     model.EventActionDelete,
				Metadata: map[string]any{
					"bad": func() {},
				},
			},
			assertErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, ErrInvalidInput)
				require.ErrorContains(t, err, "metadata")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := New(testutil.FakeTransactionManager{}, &fakeEventsRepo{
				createFn: func(context.Context, postgres.DBTX, slug.Slug, *repoevents.CreateParams) (*repoevents.Event, error) {
					return nil, errUnexpectedEventsRepoCall
				},
				findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repoevents.Event, error) {
					return nil, errUnexpectedEventsRepoCall
				},
				listByOrganizationFn: func(context.Context, postgres.DBTX, slug.Slug, *repoevents.ListFilters, model.ListRequest) ([]*repoevents.Event, error) {
					return nil, errUnexpectedEventsRepoCall
				},
			}, testutil.FakeClock{NowFn: func() time.Time { return now }})

			_, err := svc.CreateEvent(context.Background(), tt.params)
			tt.assertErr(t, err)
		})
	}
}

func TestService_PruneExpiredEventsDeletesRowsOlderThanRetentionCutoff(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 20, 12, 0, 0, 0, time.UTC)
	retention := 180 * 24 * time.Hour
	expectedCutoff := now.Add(-retention)

	svc := New(testutil.FakeTransactionManager{}, &fakeEventsRepo{
		createFn: func(context.Context, postgres.DBTX, slug.Slug, *repoevents.CreateParams) (*repoevents.Event, error) {
			return nil, errUnexpectedEventsRepoCall
		},
		findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repoevents.Event, error) {
			return nil, errUnexpectedEventsRepoCall
		},
		listByOrganizationFn: func(context.Context, postgres.DBTX, slug.Slug, *repoevents.ListFilters, model.ListRequest) ([]*repoevents.Event, error) {
			return nil, errUnexpectedEventsRepoCall
		},
		pruneOlderThanFn: func(_ context.Context, _ postgres.DBTX, cutoff time.Time) (int64, error) {
			require.Equal(t, expectedCutoff, cutoff)
			return 7, nil
		},
	}, testutil.FakeClock{NowFn: func() time.Time { return now }})

	deletedCount, err := svc.PruneExpiredEvents(context.Background(), retention)
	require.NoError(t, err)
	require.EqualValues(t, 7, deletedCount)
}

func TestService_ListSourceMetadataReturnsCatalogAndScopedRefs(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("ops")
	environmentSlug := must.Slug("prod")
	sourceKind := model.EventEntityTypeProject
	sourceAction := model.EventActionCreate
	sourceRef := must.Slug("ops-project")

	txm := &recordingEventsTransactionManager{}
	svc := New(txm, &fakeEventsRepo{
		createFn: func(context.Context, postgres.DBTX, slug.Slug, *repoevents.CreateParams) (*repoevents.Event, error) {
			return nil, errUnexpectedEventsRepoCall
		},
		findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repoevents.Event, error) {
			return nil, errUnexpectedEventsRepoCall
		},
		listByOrganizationFn: func(context.Context, postgres.DBTX, slug.Slug, *repoevents.ListFilters, model.ListRequest) ([]*repoevents.Event, error) {
			return nil, errUnexpectedEventsRepoCall
		},
		listSourceRefsByOrganizationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			filters repoevents.SourceMetadataFilters,
			limit int,
		) ([]model.EventSourceRefOption, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, &sourceKind, filters.SourceKind)
			require.Equal(t, &sourceAction, filters.SourceAction)
			require.Equal(t, &projectSlug, filters.ProjectSlug)
			require.Equal(t, &environmentSlug, filters.EnvironmentSlug)
			require.Equal(t, alertPolicySourceRefOptionLimit, limit)
			return []model.EventSourceRefOption{{
				Value: sourceRef,
				Label: "Ops project",
			}}, nil
		},
	}, testutil.FakeClock{})

	got, err := svc.ListSourceMetadata(context.Background(), orgSlug, SourceMetadataFilters{
		Scope:           model.AlertPolicyScopeProjectEnvironment,
		ProjectSlug:     &projectSlug,
		EnvironmentSlug: &environmentSlug,
		SourceKind:      &sourceKind,
		SourceAction:    &sourceAction,
	})

	require.NoError(t, err)
	require.Equal(t, 1, txm.readCalls)
	require.Zero(t, txm.writeCalls)
	require.Contains(t, got.Sources, model.EventSourceMetadata{
		SourceKind:    model.EventEntityTypeProject,
		SourceActions: []model.EventAction{model.EventActionCreate, model.EventActionUpdate, model.EventActionDelete},
	})
	require.Contains(t, got.Sources, model.EventSourceMetadata{
		SourceKind:    model.EventEntityTypePipelineRun,
		SourceActions: []model.EventAction{model.EventActionExecute},
	})
	require.Equal(t, []model.EventSourceRefOption{{
		Value: sourceRef,
		Label: "Ops project",
	}}, got.SourceRefs)
}

func TestService_ListSourceMetadataReturnsCatalogWithoutSourceRefQuery(t *testing.T) {
	t.Parallel()

	svc := New(testutil.FakeTransactionManager{}, &fakeEventsRepo{
		createFn: func(context.Context, postgres.DBTX, slug.Slug, *repoevents.CreateParams) (*repoevents.Event, error) {
			return nil, errUnexpectedEventsRepoCall
		},
		findBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repoevents.Event, error) {
			return nil, errUnexpectedEventsRepoCall
		},
		listByOrganizationFn: func(context.Context, postgres.DBTX, slug.Slug, *repoevents.ListFilters, model.ListRequest) ([]*repoevents.Event, error) {
			return nil, errUnexpectedEventsRepoCall
		},
	}, testutil.FakeClock{})

	got, err := svc.ListSourceMetadata(context.Background(), must.Slug("acme"), SourceMetadataFilters{})

	require.NoError(t, err)
	require.NotEmpty(t, got.Sources)
	require.Empty(t, got.SourceRefs)
	require.Contains(t, got.Sources, model.EventSourceMetadata{
		SourceKind:    model.EventEntityTypePipelineRun,
		SourceActions: []model.EventAction{model.EventActionExecute},
	})
	require.Contains(t, got.Sources, model.EventSourceMetadata{
		SourceKind:    model.EventEntityTypeProject,
		SourceActions: []model.EventAction{model.EventActionCreate, model.EventActionUpdate, model.EventActionDelete},
	})
}

func TestService_ReadMethodsPreserveTenantAndFilterContracts(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	eventSlug := must.Slug("evt-query-executed-001")
	actorSlug := must.Slug("alice")
	entitySlug := must.Slug("daily-users")
	now := time.Date(2026, time.May, 16, 15, 0, 0, 0, time.UTC)
	metadata := json.RawMessage(`{"duration_ms":42}`)
	entityType := model.EventEntityTypeQuery
	action := model.EventActionExecute

	svc := New(testutil.FakeTransactionManager{}, &fakeEventsRepo{
		createFn: func(context.Context, postgres.DBTX, slug.Slug, *repoevents.CreateParams) (*repoevents.Event, error) {
			return nil, errUnexpectedEventsRepoCall
		},
		findBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotEventSlug slug.Slug) (*repoevents.Event, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, eventSlug, gotEventSlug)
			return &repoevents.Event{
				Slug:       eventSlug,
				ActorType:  model.EventActorTypeUser,
				ActorSlug:  &actorSlug,
				EntityType: model.EventEntityTypeQuery,
				EntitySlug: &entitySlug,
				Action:     model.EventActionExecute,
				OccurredAt: now,
				Metadata:   metadata,
				CreatedAt:  now,
			}, nil
		},
		listByOrganizationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug slug.Slug,
			filters *repoevents.ListFilters,
			req model.ListRequest,
		) ([]*repoevents.Event, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, entityType, *filters.EntityType)
			require.Equal(t, action, *filters.Action)
			require.Equal(t, 3, req.Limit)
			require.Equal(t, model.ListSortOccurredAt, req.SortBy)
			return []*repoevents.Event{
				{
					Slug:       eventSlug,
					ActorType:  model.EventActorTypeUser,
					ActorSlug:  &actorSlug,
					EntityType: entityType,
					EntitySlug: &entitySlug,
					Action:     action,
					OccurredAt: now,
					Metadata:   metadata,
					CreatedAt:  now,
				},
			}, nil
		},
	}, testutil.FakeClock{NowFn: func() time.Time { return now }})

	found, err := svc.GetEvent(context.Background(), orgSlug, eventSlug)
	require.NoError(t, err)
	require.Equal(t, eventSlug, found.Slug)
	require.InEpsilon(t, 42.0, found.Metadata["duration_ms"], 0.0001)

	listed, err := svc.ListEvents(context.Background(), orgSlug, ListEventsFilters{
		EntityType: &entityType,
		Action:     &action,
	}, model.ListRequest{Limit: 2})
	require.NoError(t, err)
	require.False(t, listed.HasMore)
	require.Len(t, listed.Items, 1)
	require.Equal(t, eventSlug, listed.Items[0].Slug)
}
