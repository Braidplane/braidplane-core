package eventrecorder

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

type testEvent struct {
	record Record
}

func (e testEvent) EventRecord() (Record, error) {
	return e.record, nil
}

type fakeEventService struct {
	calls []eventssvc.CreateEventParams
}

func (f *fakeEventService) CreateEvent(
	_ context.Context,
	params eventssvc.CreateEventParams,
) (*model.Event, error) {
	f.calls = append(f.calls, params)
	return &model.Event{Slug: params.Slug}, nil
}

func TestRecorderMapsDomainEventRecordToCanonicalEvent(t *testing.T) {
	t.Parallel()

	events := &fakeEventService{}
	recorder := New(events)
	orgSlug := must.Slug("acme")
	actor := model.APIKeyEventActor(must.Slug("etl-worker"))

	_, err := recorder.Record(context.Background(), orgSlug, actor, testEvent{record: Record{
		SlugPrefix: must.Slug("evt-query-execute"),
		EntityType: model.EventEntityTypeQueryExecution,
		EntitySlug: must.Slug("daily-users-exec-001"),
		Action:     model.EventActionExecute,
		Metadata: map[string]any{
			"query_slug":       "daily-users",
			"data_source_slug": "warehouse",
			"status":           "success",
			"parameter_keys":   []string{"customer_id"},
			"row_count":        3,
		},
	}})
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.NotEmpty(t, got.Slug)
	require.Equal(t, slug.Slug("acme"), got.OrgSlug)
	require.Equal(t, model.EventActorTypeAPIKey, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, slug.Slug("etl-worker"), *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeQueryExecution, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, slug.Slug("daily-users-exec-001"), *got.EntitySlug)
	require.Equal(t, model.EventActionExecute, got.Action)
	require.Equal(t, map[string]any{
		"query_slug":       "daily-users",
		"data_source_slug": "warehouse",
		"status":           "success",
		"parameter_keys":   []string{"customer_id"},
		"row_count":        3,
	}, got.Metadata)
}

func TestRecorderRejectsNilEvent(t *testing.T) {
	t.Parallel()

	events := &fakeEventService{}
	recorder := New(events)

	_, err := recorder.Record(
		context.Background(),
		must.Slug("acme"),
		model.SystemEventActor(),
		nil,
	)
	require.Error(t, err)
	require.Empty(t, events.calls)
}

func TestParamsForMapsSecurityAuditContract(t *testing.T) {
	t.Parallel()

	params, err := ParamsFor(
		must.Slug("acme"),
		model.UserEventActor(must.Slug("alice")),
		testEvent{record: Record{
			SlugPrefix:    must.Slug("evt-api-key-rotate"),
			EventClass:    model.EventClassSecurityAudit,
			EntityType:    model.EventEntityTypeAPIKey,
			EntitySlug:    must.Slug("deploy-key"),
			Action:        model.EventActionRotate,
			SchemaVersion: model.CurrentEventSchemaVersion,
			Metadata:      map[string]any{"outcome": "succeeded"},
		}},
	)

	require.NoError(t, err)
	require.Equal(t, model.EventClassSecurityAudit, params.EventClass)
	require.Equal(t, model.CurrentEventSchemaVersion, params.SchemaVersion)
}
