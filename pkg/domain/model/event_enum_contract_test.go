package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestEventEnumContracts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		text      string
		parse     func(string) (any, error)
		marshal   func() ([]byte, error)
		unmarshal func([]byte) (any, error)
		values    []string
		valid     func() bool
		invalid   func() bool
		want      any
	}{
		{
			name: "event actor type",
			text: EventActorTypeUser.String(),
			parse: func(raw string) (any, error) {
				return EventActorTypeString(raw)
			},
			marshal: EventActorTypeUser.MarshalText,
			unmarshal: func(raw []byte) (any, error) {
				var value EventActorType
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  EventActorTypeStrings(),
			valid:   EventActorTypeUser.IsAEventActorType,
			invalid: EventActorType(99).IsAEventActorType,
			want:    EventActorTypeUser,
		},
		{
			name: "event class",
			text: EventClassSecurityAudit.String(),
			parse: func(raw string) (any, error) {
				return EventClassString(raw)
			},
			marshal: EventClassSecurityAudit.MarshalText,
			unmarshal: func(raw []byte) (any, error) {
				var value EventClass
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  EventClassStrings(),
			valid:   EventClassSecurityAudit.IsAEventClass,
			invalid: EventClass(99).IsAEventClass,
			want:    EventClassSecurityAudit,
		},
		{
			name: "event entity type",
			text: EventEntityTypeProject.String(),
			parse: func(raw string) (any, error) {
				return EventEntityTypeString(raw)
			},
			marshal: EventEntityTypeProject.MarshalText,
			unmarshal: func(raw []byte) (any, error) {
				var value EventEntityType
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  EventEntityTypeStrings(),
			valid:   EventEntityTypeProject.IsAEventEntityType,
			invalid: EventEntityType(99).IsAEventEntityType,
			want:    EventEntityTypeProject,
		},
		{
			name: "event action",
			text: EventActionExecute.String(),
			parse: func(raw string) (any, error) {
				return EventActionString(raw)
			},
			marshal: EventActionExecute.MarshalText,
			unmarshal: func(raw []byte) (any, error) {
				var value EventAction
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  EventActionStrings(),
			valid:   EventActionExecute.IsAEventAction,
			invalid: EventAction(99).IsAEventAction,
			want:    EventActionExecute,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Contains(t, tc.values, tc.text)
			require.True(t, tc.valid())
			require.False(t, tc.invalid())
			require.Equal(t, tc.text, string(mustMarshalText(t, tc.marshal)))

			parsed, err := tc.parse(tc.text)
			require.NoError(t, err)
			require.Equal(t, tc.want, parsed)
			parsed, err = tc.parse(upperASCII(tc.text))
			require.NoError(t, err)
			require.Equal(t, tc.want, parsed)
			_, err = tc.parse("definitely-not-valid")
			require.Error(t, err)

			encoded, err := json.Marshal(tc.want)
			require.NoError(t, err)
			require.JSONEq(t, `"`+tc.text+`"`, string(encoded))
			decoded, err := tc.unmarshal(encoded)
			require.NoError(t, err)
			require.Equal(t, tc.want, decoded)
			_, err = tc.unmarshal([]byte(`123`))
			require.Error(t, err)
			_, err = tc.unmarshal([]byte(`"definitely-not-valid"`))
			require.Error(t, err)
		})
	}
}

func TestEventEnumContractsDoNotExposeSpeculativeTypes(t *testing.T) {
	t.Parallel()

	require.NotContains(t, EventActorTypeStrings(), "service_account")
	require.NotContains(t, EventEntityTypeStrings(), "pipeline_draft")
	require.NotContains(t, EventEntityTypeStrings(), "approval")
}

func TestEventActorUserSlugValueOnlyReturnsUserActorSlug(t *testing.T) {
	t.Parallel()

	userSlug := slug.Slug("alice")
	apiKeySlug := slug.Slug("automation-key")

	require.Equal(t, userSlug, UserEventActor(userSlug).UserSlugValue())
	require.Empty(t, APIKeyEventActor(apiKeySlug).UserSlugValue())
	require.Empty(t, SystemEventActor().UserSlugValue())
	require.Empty(t, ExternalEventActor().UserSlugValue())
}
