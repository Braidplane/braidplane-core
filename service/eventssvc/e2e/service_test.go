package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/environments"
	repoevents "github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/postgres/memberinvitations"
	"github.com/Braidplane/braidplane-core/postgres/members"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/postgres/permissions"
	"github.com/Braidplane/braidplane-core/postgres/roles"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/environmentssvc"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/memberssvc"
	"github.com/Braidplane/braidplane-core/service/orgsvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

func TestService_CreateGetAndListEventsScopesByOrganization(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres events service E2E test in short mode")
	}

	ctx := context.Background()
	pool := openEventsServiceE2EPool(t)
	svc := newEventsService(pool)

	orgASlug := uniqueEventsServiceE2ESlug("events-svc-org-a")
	orgBSlug := uniqueEventsServiceE2ESlug("events-svc-org-b")
	createEventsServiceOrganization(t, pool, orgASlug, "Events Svc Org A")
	createEventsServiceOrganization(t, pool, orgBSlug, "Events Svc Org B")

	eventSlug := uniqueEventsServiceE2ESlug("evt-svc-query-exec")
	actorSlug := must.Slug("alice")
	entitySlug := must.Slug("daily-users")
	requestID := "req-security-audit-tenant-isolation"
	occurredAt := time.Date(2026, time.May, 16, 14, 30, 0, 0, time.UTC)
	metadata := map[string]any{
		"duration_ms":    float64(42),
		"parameter_keys": []any{"customer_id"},
		"row_count":      float64(3),
	}

	created, err := svc.CreateEvent(ctx, eventssvc.CreateEventParams{
		OrgSlug:    orgASlug,
		Slug:       eventSlug,
		EventClass: model.EventClassSecurityAudit,
		ActorType:  model.EventActorTypeUser,
		ActorSlug:  &actorSlug,
		EntityType: model.EventEntityTypeQuery,
		EntitySlug: &entitySlug,
		Action:     model.EventActionExecute,
		OccurredAt: occurredAt,
		RequestID:  &requestID,
		Metadata:   metadata,
	})
	require.NoError(t, err)
	require.Equal(t, eventSlug, created.Slug)
	require.Equal(t, model.EventClassSecurityAudit, created.EventClass)
	require.Equal(t, model.CurrentEventSchemaVersion, created.SchemaVersion)
	require.Equal(t, requestID, *created.RequestID)
	require.Equal(t, model.EventActorTypeUser, created.ActorType)
	require.Equal(t, actorSlug, *created.ActorSlug)
	require.Equal(t, model.EventEntityTypeQuery, created.EntityType)
	require.Equal(t, entitySlug, *created.EntitySlug)
	require.Equal(t, model.EventActionExecute, created.Action)
	require.True(t, created.OccurredAt.Equal(occurredAt))
	require.Equal(t, metadata, created.Metadata)

	found, err := svc.GetEvent(ctx, orgASlug, eventSlug)
	require.NoError(t, err)
	require.Equal(t, created.Slug, found.Slug)
	require.Equal(t, model.EventClassSecurityAudit, found.EventClass)
	require.Equal(t, requestID, *found.RequestID)
	require.Equal(t, metadata, found.Metadata)

	_, err = svc.GetEvent(ctx, orgBSlug, eventSlug)
	require.True(t, xerror.Is(err, eventssvc.ErrEventNotFound))

	entityType := model.EventEntityTypeQuery
	action := model.EventActionExecute
	eventClass := model.EventClassSecurityAudit
	listed, err := svc.ListEvents(ctx, orgASlug, eventssvc.ListEventsFilters{
		EventClass: &eventClass,
		EntityType: &entityType,
		Action:     &action,
	}, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	require.Equal(t, eventSlug, listed.Items[0].Slug)

	listed, err = svc.ListEvents(ctx, orgBSlug, eventssvc.ListEventsFilters{
		EventClass: &eventClass,
	}, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Empty(t, listed.Items)

	_, err = svc.CreateEvent(ctx, eventssvc.CreateEventParams{
		OrgSlug:    orgASlug,
		Slug:       eventSlug,
		ActorType:  model.EventActorTypeSystem,
		EntityType: model.EventEntityTypeQuery,
		EntitySlug: &entitySlug,
		Action:     model.EventActionExecute,
		OccurredAt: occurredAt,
		Metadata:   map[string]any{},
	})
	require.True(t, xerror.Is(err, eventssvc.ErrSlugTaken))
}

func TestService_ListEventsPaginatesByOccurredAtCursor(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres events service E2E test in short mode")
	}

	ctx := context.Background()
	pool := openEventsServiceE2EPool(t)
	svc := newEventsService(pool)

	orgSlug := uniqueEventsServiceE2ESlug("events-svc-cursor-org")
	createEventsServiceOrganization(t, pool, orgSlug, "Events Svc Cursor Org")

	entitySlug := must.Slug("daily-users")
	for index := range 3 {
		eventSlug := uniqueEventsServiceE2ESlug(fmt.Sprintf("evt-svc-cursor-%d", index))
		_, err := svc.CreateEvent(ctx, eventssvc.CreateEventParams{
			OrgSlug:    orgSlug,
			Slug:       eventSlug,
			ActorType:  model.EventActorTypeSystem,
			EntityType: model.EventEntityTypeQuery,
			EntitySlug: &entitySlug,
			Action:     model.EventActionExecute,
			OccurredAt: time.Date(2026, time.May, 16, 14, 30+index, 0, 0, time.UTC),
			Metadata:   map[string]any{},
		})
		require.NoError(t, err)
	}

	firstPage, err := svc.ListEvents(ctx, orgSlug, eventssvc.ListEventsFilters{}, model.ListRequest{
		Limit: 1,
		Order: model.ListOrderDesc,
	})
	require.NoError(t, err)
	require.Len(t, firstPage.Items, 1)
	require.True(t, firstPage.HasMore)
	require.NotNil(t, firstPage.NextCursor)

	secondPage, err := svc.ListEvents(ctx, orgSlug, eventssvc.ListEventsFilters{}, model.ListRequest{
		Limit:  2,
		Cursor: *firstPage.NextCursor,
		Order:  model.ListOrderDesc,
	})
	require.NoError(t, err)
	require.NotEmpty(t, secondPage.Items)
	require.NotEqual(t, firstPage.Items[0].Slug, secondPage.Items[0].Slug)
}

func TestService_ListSourceMetadataReturnsScopedLatestRefs(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres events service E2E test in short mode")
	}

	ctx := context.Background()
	pool := openEventsServiceE2EPool(t)
	svc := newEventsService(pool)

	orgASlug := uniqueEventsServiceE2ESlug("events-svc-source-meta-a")
	orgBSlug := uniqueEventsServiceE2ESlug("events-svc-source-meta-b")
	createEventsServiceOrganization(t, pool, orgASlug, "Events Svc Source Meta A")
	createEventsServiceOrganization(t, pool, orgBSlug, "Events Svc Source Meta B")

	sourceKind := model.EventEntityTypeProject
	sourceAction := model.EventActionCreate
	projectSlug := must.Slug("ops-project")
	otherProjectSlug := must.Slug("other-project")

	events := []struct {
		orgSlug    slug.Slug
		eventSlug  slug.Slug
		entitySlug slug.Slug
		label      string
		occurredAt time.Time
	}{
		{
			orgSlug:    orgASlug,
			eventSlug:  uniqueEventsServiceE2ESlug("evt-svc-source-meta-old"),
			entitySlug: projectSlug,
			label:      "Old Ops Project",
			occurredAt: time.Date(2026, time.June, 1, 9, 0, 0, 0, time.UTC),
		},
		{
			orgSlug:    orgASlug,
			eventSlug:  uniqueEventsServiceE2ESlug("evt-svc-source-meta-new"),
			entitySlug: projectSlug,
			label:      "Ops Project",
			occurredAt: time.Date(2026, time.June, 2, 9, 0, 0, 0, time.UTC),
		},
		{
			orgSlug:    orgASlug,
			eventSlug:  uniqueEventsServiceE2ESlug("evt-svc-source-meta-other"),
			entitySlug: otherProjectSlug,
			label:      "Other Project",
			occurredAt: time.Date(2026, time.June, 3, 9, 0, 0, 0, time.UTC),
		},
		{
			orgSlug:    orgBSlug,
			eventSlug:  uniqueEventsServiceE2ESlug("evt-svc-source-meta-foreign"),
			entitySlug: projectSlug,
			label:      "Foreign Ops Project",
			occurredAt: time.Date(2026, time.June, 4, 9, 0, 0, 0, time.UTC),
		},
	}
	for _, event := range events {
		entitySlug := event.entitySlug
		_, err := svc.CreateEvent(ctx, eventssvc.CreateEventParams{
			OrgSlug:    event.orgSlug,
			Slug:       event.eventSlug,
			ActorType:  model.EventActorTypeSystem,
			EntityType: sourceKind,
			EntitySlug: &entitySlug,
			Action:     sourceAction,
			OccurredAt: event.occurredAt,
			Metadata: map[string]any{
				"project_slug": event.entitySlug.String(),
				"source_label": event.label,
				"source_ref":   event.entitySlug.String(),
			},
		})
		require.NoError(t, err)
	}

	got, err := svc.ListSourceMetadata(ctx, orgASlug, eventssvc.SourceMetadataFilters{
		Scope:        model.AlertPolicyScopeProject,
		ProjectSlug:  &projectSlug,
		SourceKind:   &sourceKind,
		SourceAction: &sourceAction,
	})
	require.NoError(t, err)
	require.Contains(t, got.Sources, model.EventSourceMetadata{
		SourceKind:    sourceKind,
		SourceActions: []model.EventAction{model.EventActionCreate, model.EventActionUpdate, model.EventActionDelete},
	})
	require.Contains(t, got.Sources, model.EventSourceMetadata{
		SourceKind:    model.EventEntityTypePipelineRun,
		SourceActions: []model.EventAction{model.EventActionExecute},
	})
	require.Equal(t, []model.EventSourceRefOption{{
		Value: projectSlug,
		Label: "Ops Project",
	}}, got.SourceRefs)
}

func TestService_ListSourceMetadataSkipsMalformedRefsAndAvoidsPIILabelFallback(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres events service E2E test in short mode")
	}

	ctx := context.Background()
	pool := openEventsServiceE2EPool(t)
	svc := newEventsService(pool)

	orgSlug := uniqueEventsServiceE2ESlug("events-svc-source-meta-pii")
	createEventsServiceOrganization(t, pool, orgSlug, "Events Svc Source Meta PII")

	sourceKind := model.EventEntityTypeMember
	sourceAction := model.EventActionCreate
	memberSlug := must.Slug("member-alice")

	events := make([]struct {
		slug      slug.Slug
		sourceRef string
	}, 0, 102)
	for i := range 101 {
		events = append(events, struct {
			slug      slug.Slug
			sourceRef string
		}{
			slug:      uniqueEventsServiceE2ESlug(fmt.Sprintf("evt-svc-source-meta-invalid-ref-%03d", i)),
			sourceRef: fmt.Sprintf("alice-%03d@example.com", i),
		})
	}
	events = append(events, struct {
		slug      slug.Slug
		sourceRef string
	}{
		slug:      uniqueEventsServiceE2ESlug("evt-svc-source-meta-valid-ref"),
		sourceRef: memberSlug.String(),
	})

	for _, event := range events {
		entitySlug := memberSlug
		_, err := svc.CreateEvent(ctx, eventssvc.CreateEventParams{
			OrgSlug:    orgSlug,
			Slug:       event.slug,
			ActorType:  model.EventActorTypeSystem,
			EntityType: sourceKind,
			EntitySlug: &entitySlug,
			Action:     sourceAction,
			OccurredAt: time.Date(2026, time.June, 5, 9, 0, 0, 0, time.UTC),
			Metadata: map[string]any{
				"source_ref": event.sourceRef,
				"name":       "Alice Example",
				"email":      "alice@example.com",
			},
		})
		require.NoError(t, err)
	}

	got, err := svc.ListSourceMetadata(ctx, orgSlug, eventssvc.SourceMetadataFilters{
		Scope:        model.AlertPolicyScopeOrganization,
		SourceKind:   &sourceKind,
		SourceAction: &sourceAction,
	})
	require.NoError(t, err)
	require.Equal(t, []model.EventSourceRefOption{{
		Value: memberSlug,
		Label: memberSlug.String(),
	}}, got.SourceRefs)
}

func TestService_PruneExpiredEventsDeletesRowsOlderThanRetentionCutoff(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres events service E2E test in short mode")
	}

	ctx := context.Background()
	pool := openEventsServiceE2EPool(t)
	svc := newEventsService(pool)

	orgKeepSlug := uniqueEventsServiceE2ESlug("events-svc-prune-keep")
	orgDeleteSlug := uniqueEventsServiceE2ESlug("events-svc-prune-delete")
	createEventsServiceOrganization(t, pool, orgKeepSlug, "Events Svc Prune Keep Org")
	createEventsServiceOrganization(t, pool, orgDeleteSlug, "Events Svc Prune Delete Org")

	entitySlug := must.Slug("ops")
	expiredAt := time.Date(2025, time.November, 1, 10, 0, 0, 0, time.UTC)
	freshAt := time.Date(2026, time.May, 1, 10, 0, 0, 0, time.UTC)

	_, err := svc.CreateEvent(ctx, eventssvc.CreateEventParams{
		OrgSlug:    orgKeepSlug,
		Slug:       uniqueEventsServiceE2ESlug("evt-svc-prune-fresh"),
		ActorType:  model.EventActorTypeSystem,
		EntityType: model.EventEntityTypeProject,
		EntitySlug: &entitySlug,
		Action:     model.EventActionUpdate,
		OccurredAt: freshAt,
		Metadata:   map[string]any{},
	})
	require.NoError(t, err)

	_, err = svc.CreateEvent(ctx, eventssvc.CreateEventParams{
		OrgSlug:    orgDeleteSlug,
		Slug:       uniqueEventsServiceE2ESlug("evt-svc-prune-expired"),
		ActorType:  model.EventActorTypeSystem,
		EntityType: model.EventEntityTypeProject,
		EntitySlug: &entitySlug,
		Action:     model.EventActionUpdate,
		OccurredAt: expiredAt,
		Metadata:   map[string]any{},
	})
	require.NoError(t, err)

	deletedCount, err := svc.PruneExpiredEvents(ctx, time.Since(expiredAt.Add(24*time.Hour)))
	require.NoError(t, err)
	require.EqualValues(t, 1, deletedCount)

	keptEvents, err := svc.ListEvents(ctx, orgKeepSlug, eventssvc.ListEventsFilters{}, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, keptEvents.Items, 1)

	deletedEvents, err := svc.ListEvents(ctx, orgDeleteSlug, eventssvc.ListEventsFilters{}, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Empty(t, deletedEvents.Items)
}

func newEventsService(pool *pgxpool.Pool) eventssvc.Service {
	return eventssvc.New(postgres.NewTransactionManager(pool), repoevents.New(), clock.New())
}

func openEventsServiceE2EPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connString := "postgres://postgres:postgres@localhost:35432/braidplane?sslmode=disable"
	if envConnString := os.Getenv("DATABASE_URL"); envConnString != "" {
		connString = envConnString
	}

	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("required real-Postgres events service E2E test: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("required real-Postgres events service E2E test: %v", err)
	}

	t.Cleanup(pool.Close)
	return pool
}

func createEventsServiceOrganization(t *testing.T, pool *pgxpool.Pool, orgSlug slug.Slug, name string) {
	t.Helper()

	ctx := context.Background()
	txm := postgres.NewTransactionManager(pool)
	eventProducer := eventtest.NewProducer(nil)
	usersSvc := userssvc.New(
		txm,
		&testutil.FakeHasher{},
		users.NewRepository(),
		&testutil.FakeEmailProducer{},
		eventProducer,
		"https://frontend.example.test",
	)
	rolesSvc := rolessvc.New(txm, roles.NewRepository(), permissions.NewRepository(), eventProducer)
	membersSvc := memberssvc.New(
		txm,
		members.NewRepository(),
		memberinvitations.NewRepository(),
		organizations.NewRepository(),
		rolesSvc,
		usersSvc,
		&testutil.FakeEmailProducer{},
		eventProducer,
		"https://frontend.example.test",
		clock.New(),
	)
	orgsSvc := orgsvc.New(
		txm,
		organizations.NewRepository(),
		environmentssvc.New(txm, environments.NewRepository(), eventProducer),
		membersSvc,
		rolesSvc,
		eventProducer,
		admissionsvc.New(txm, organizations.NewRepository()),
	)

	userSlug := uniqueEventsServiceE2ESlug("events-svc-user")
	user, err := usersSvc.CreateUser(ctx, model.NewUser{
		FullName: "Events Service Test User",
		Email:    userSlug.String() + "@example.test",
		Password: "password123",
		Role:     model.RoleSuperAdmin,
		Locale:   "en",
	})
	require.NoError(t, err)

	_, err = orgsSvc.CreateOrganization(ctx, user.Slug, model.UserEventActor(user.Slug), model.NewOrganization{
		Name: name,
		Slug: orgSlug,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err := pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE slug = $1`, orgSlug)
		require.NoError(t, err)

		_, err = pool.Exec(cleanupCtx, `DELETE FROM users WHERE slug = $1`, user.Slug)
		require.NoError(t, err)
	})
}

func uniqueEventsServiceE2ESlug(prefix string) slug.Slug {
	return must.Slug(fmt.Sprintf("%s-%s", prefix, uuid.NewString()))
}
