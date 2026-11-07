package eventssvc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repoevents "github.com/Braidplane/braidplane-core/postgres/events"
)

func TestService_CreateEventDefaultsContractAndCapturesRequestID(t *testing.T) {
	t.Parallel()

	requestID := "req-security-123"
	var captured *repoevents.CreateParams
	svc := New(testutil.FakeTransactionManager{}, securityAuditFakeRepo(func(params *repoevents.CreateParams) {
		captured = params
	}), testutil.FakeClock{})

	_, err := svc.CreateEvent(
		reqctx.WithRequestID(context.Background(), requestID),
		validSecurityAuditCreateParams(map[string]any{"outcome": "succeeded"}),
	)

	require.NoError(t, err)
	require.NotNil(t, captured)
	require.Equal(t, model.EventClassActivity, captured.EventClass)
	require.Equal(t, int32(model.CurrentEventSchemaVersion), captured.SchemaVersion)
	require.Equal(t, requestID, *captured.RequestID)
}

func TestService_CreateSecurityAuditEventRejectsUnsafeMetadataRecursively(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata map[string]any
	}{
		{name: "Token", metadata: map[string]any{"details": map[string]any{"access_token": "nope"}}},
		{name: "Password", metadata: map[string]any{"changes": []any{map[string]string{"new_password": "nope"}}}},
		{name: "Payload", metadata: map[string]any{"request_payload": map[string]any{"safe": false}}},
		{name: "Header", metadata: map[string]any{"request_headers": map[string]string{"x": "nope"}}},
		{name: "Session", metadata: map[string]any{"context": map[string]any{"session_id": "nope"}}},
		{name: "RawMessage", metadata: map[string]any{"details": json.RawMessage(`{"token":"nope"}`)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repoCalled := false
			svc := New(testutil.FakeTransactionManager{}, securityAuditFakeRepo(func(*repoevents.CreateParams) {
				repoCalled = true
			}), testutil.FakeClock{})
			params := validSecurityAuditCreateParams(tt.metadata)
			params.EventClass = model.EventClassSecurityAudit

			_, err := svc.CreateEvent(context.Background(), params)

			require.True(t, xerror.Is(err, ErrInvalidInput))
			require.ErrorContains(t, err, "unsafe key")
			require.False(t, repoCalled)
		})
	}
}

func TestService_CreateSecurityAuditEventRejectsCyclicMetadata(t *testing.T) {
	t.Parallel()

	metadata := map[string]any{}
	metadata["cycle"] = metadata
	repoCalled := false
	svc := New(testutil.FakeTransactionManager{}, securityAuditFakeRepo(func(*repoevents.CreateParams) {
		repoCalled = true
	}), testutil.FakeClock{})
	params := validSecurityAuditCreateParams(metadata)
	params.EventClass = model.EventClassSecurityAudit

	_, err := svc.CreateEvent(context.Background(), params)

	require.True(t, xerror.Is(err, ErrInvalidInput))
	require.ErrorContains(t, err, "metadata")
	require.False(t, repoCalled)
}

func TestService_CreateSecurityAuditEventBoundsMetadataAndRequestID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params CreateEventParams
		detail string
	}{
		{
			name: "Metadata",
			params: func() CreateEventParams {
				params := validSecurityAuditCreateParams(map[string]any{
					"summary": strings.Repeat("x", maxSecurityAuditMetadataBytes),
				})
				params.EventClass = model.EventClassSecurityAudit
				return params
			}(),
			detail: "metadata exceeds",
		},
		{
			name: "RequestID",
			params: func() CreateEventParams {
				params := validSecurityAuditCreateParams(nil)
				requestID := strings.Repeat("r", 256)
				params.RequestID = &requestID
				return params
			}(),
			detail: "request_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repoCalled := false
			svc := New(testutil.FakeTransactionManager{}, securityAuditFakeRepo(func(*repoevents.CreateParams) {
				repoCalled = true
			}), testutil.FakeClock{})

			_, err := svc.CreateEvent(context.Background(), tt.params)

			require.True(t, xerror.Is(err, ErrInvalidInput))
			require.ErrorContains(t, err, tt.detail)
			require.False(t, repoCalled)
		})
	}
}

func TestService_ListEventsPassesSecurityAuditFilters(t *testing.T) {
	t.Parallel()

	eventClass := model.EventClassSecurityAudit
	requestID := "req-security-123"
	occurredFrom := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	occurredBefore := occurredFrom.Add(24 * time.Hour)
	var captured *repoevents.ListFilters
	repo := securityAuditFakeRepo(nil)
	repo.listByOrganizationFn = func(
		_ context.Context,
		_ postgres.DBTX,
		_ slug.Slug,
		filters *repoevents.ListFilters,
		_ model.ListRequest,
	) ([]*repoevents.Event, error) {
		captured = filters
		return nil, nil
	}
	svc := New(testutil.FakeTransactionManager{}, repo, testutil.FakeClock{})

	_, err := svc.ListEvents(context.Background(), must.Slug("acme"), ListEventsFilters{
		EventClass:     &eventClass,
		RequestID:      &requestID,
		OccurredFrom:   &occurredFrom,
		OccurredBefore: &occurredBefore,
	}, model.ListRequest{Limit: 10})

	require.NoError(t, err)
	require.NotNil(t, captured)
	require.Equal(t, eventClass, *captured.EventClass)
	require.Equal(t, requestID, *captured.RequestID)
	require.Equal(t, occurredFrom, *captured.OccurredFrom)
	require.Equal(t, occurredBefore, *captured.OccurredBefore)
}

func TestService_ListEventsRejectsInvalidSecurityAuditFilters(t *testing.T) {
	t.Parallel()

	occurredBefore := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	occurredFrom := occurredBefore.Add(time.Hour)
	invalidClass := model.EventClassUnknown
	tests := []struct {
		name    string
		filters ListEventsFilters
		detail  string
	}{
		{
			name:    "EventClass",
			filters: ListEventsFilters{EventClass: &invalidClass},
			detail:  "event_class filter",
		},
		{
			name: "TimeWindow",
			filters: ListEventsFilters{
				OccurredFrom:   &occurredFrom,
				OccurredBefore: &occurredBefore,
			},
			detail: "occurred_from",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := New(testutil.FakeTransactionManager{}, securityAuditFakeRepo(nil), testutil.FakeClock{})

			_, err := svc.ListEvents(
				context.Background(),
				must.Slug("acme"),
				tt.filters,
				model.ListRequest{Limit: 10},
			)

			require.True(t, xerror.Is(err, ErrInvalidInput))
			require.ErrorContains(t, err, tt.detail)
		})
	}
}

func validSecurityAuditCreateParams(metadata map[string]any) CreateEventParams {
	actorSlug := must.Slug("alice")
	entitySlug := must.Slug("deploy-key")
	return CreateEventParams{
		OrgSlug:    must.Slug("acme"),
		Slug:       must.Slug("evt-api-key-rotate-001"),
		ActorType:  model.EventActorTypeUser,
		ActorSlug:  &actorSlug,
		EntityType: model.EventEntityTypeAPIKey,
		EntitySlug: &entitySlug,
		Action:     model.EventActionRotate,
		Metadata:   metadata,
	}
}

func securityAuditFakeRepo(capture func(*repoevents.CreateParams)) *fakeEventsRepo {
	return &fakeEventsRepo{
		createFn: func(
			_ context.Context,
			_ postgres.DBTX,
			_ slug.Slug,
			params *repoevents.CreateParams,
		) (*repoevents.Event, error) {
			if capture != nil {
				capture(params)
			}
			return &repoevents.Event{
				Slug:          params.Slug,
				EventClass:    params.EventClass,
				ActorType:     params.ActorType,
				ActorSlug:     params.ActorSlug,
				EntityType:    params.EntityType,
				EntitySlug:    params.EntitySlug,
				Action:        params.Action,
				OccurredAt:    params.OccurredAt,
				RequestID:     params.RequestID,
				SchemaVersion: int(params.SchemaVersion),
				Metadata:      params.Metadata,
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
	}
}
