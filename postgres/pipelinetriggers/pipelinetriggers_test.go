package pipelinetriggers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestTriggerFromFieldsRejectsInvalidActorTypes(t *testing.T) {
	t.Parallel()

	baseFields := func() triggerFields {
		now := time.Date(2026, time.June, 11, 12, 0, 0, 0, time.UTC)
		return triggerFields{
			Slug:                     slug.Slug("daily-users"),
			Name:                     "Daily users",
			TriggerType:              "schedule",
			Enabled:                  true,
			Revision:                 1,
			EnvironmentSlug:          slug.Slug("prod"),
			PipelineSlug:             slug.Slug("sync-users"),
			VersionPolicy:            "latest_deployed",
			RetryMaxAttempts:         1,
			RetryBackoffStrategy:     "none",
			RetryInitialDelaySeconds: 0,
			RetryMaxDelaySeconds:     0,
			RuntimeInputKeys:         []string{},
			RetryableFailureKinds:    []string{},
			ScheduleCron:             pgtype.Text{String: "0 * * * *", Valid: true},
			ScheduleTimezone:         pgtype.Text{String: "UTC", Valid: true},
			ScheduleMisfirePolicy:    pgtype.Text{String: "skip", Valid: true},
			CreatedByActorType:       "user",
			CreatedByActorSlug:       pgtype.Text{String: "creator", Valid: true},
			UpdatedByActorType:       "user",
			UpdatedByActorSlug:       pgtype.Text{String: "updater", Valid: true},
			CreatedAt:                pgtype.Timestamptz{Time: now, Valid: true},
			UpdatedAt:                pgtype.Timestamptz{Time: now, Valid: true},
		}
	}

	tests := []struct {
		name   string
		mutate func(*triggerFields)
	}{
		{
			name: "InvalidCreatedByActorType",
			mutate: func(fields *triggerFields) {
				fields.CreatedByActorType = "not-an-actor"
			},
		},
		{
			name: "InvalidUpdatedByActorType",
			mutate: func(fields *triggerFields) {
				fields.UpdatedByActorType = "not-an-actor"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fields := baseFields()
			test.mutate(&fields)

			_, err := triggerFromFields(fields)

			require.Error(t, err)
			require.ErrorContains(t, err, "does not belong to EventActorType values")
		})
	}
}

func TestTriggerFromFieldsBuildsWebhookConfig(t *testing.T) {
	t.Parallel()

	mappings, err := json.Marshal([]model.PipelineWebhookInputMapping{{JSONPointer: "/tenant/id", RuntimeInputKey: "tenant_id"}})
	require.NoError(t, err)
	now := time.Date(2026, time.June, 11, 12, 0, 0, 0, time.UTC)
	trigger, err := triggerFromFields(triggerFields{
		Slug: slug.Slug("incoming"), Name: "Incoming", TriggerType: "webhook", Revision: 1,
		EnvironmentSlug: slug.Slug("prod"), PipelineSlug: slug.Slug("sync-users"), VersionPolicy: "latest_deployed",
		RetryMaxAttempts: 1, RetryBackoffStrategy: "none", RuntimeInputKeys: []string{"tenant_id"},
		ReceiverID:                  pgtype.Text{String: "opaque-receiver", Valid: true},
		WebhookVerificationStrategy: pgtype.Text{String: "hmac_sha256", Valid: true}, WebhookInputMappings: mappings,
		EnabledKeyVersions: []int32{2}, CreatedByActorType: "system", UpdatedByActorType: "system",
		CreatedAt: pgtype.Timestamptz{Time: now, Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	})

	require.NoError(t, err)
	require.Equal(t, "opaque-receiver", trigger.WebhookConfig.ReceiverID)
	require.Equal(t, "hmac_sha256", trigger.WebhookConfig.VerificationStrategy)
	require.Equal(t, []int{2}, trigger.WebhookConfig.EnabledKeyVersions)
	require.True(t, trigger.WebhookConfig.SecretConfigured)
	require.Equal(t, []model.PipelineWebhookInputMapping{{JSONPointer: "/tenant/id", RuntimeInputKey: "tenant_id"}}, trigger.WebhookConfig.InputMappings)
	require.Nil(t, trigger.ScheduleConfig)
}
