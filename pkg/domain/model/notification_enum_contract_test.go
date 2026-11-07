package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNotificationTypeUsesPipelineContractNames(t *testing.T) {
	t.Parallel()

	require.Equal(t, "pipeline.run_failed", NotificationTypePipelineRunFailed.String())
	require.Contains(t, NotificationTypeStrings(), "pipeline.run_failed")
	require.NotContains(t, NotificationTypeStrings(), "pipeline_run_failed")

	parsed, err := NotificationTypeString("pipeline.run_failed")
	require.NoError(t, err)
	require.Equal(t, NotificationTypePipelineRunFailed, parsed)
	_, err = NotificationTypeString("pipeline_run_failed")
	require.Error(t, err)

	encoded, err := json.Marshal(NotificationTypePipelineContractFailure)
	require.NoError(t, err)
	require.JSONEq(t, `"pipeline.contract_failure"`, string(encoded))
}

func TestProducerBackedNotificationTypesIncludeSuccessfulRunRecency(t *testing.T) {
	require.ElementsMatch(t, []NotificationType{NotificationTypePipelineRunSucceeded, NotificationTypePipelineRunFailed, NotificationTypePipelineRetryExhausted, NotificationTypePipelineScheduledRunDelayed, NotificationTypePipelineScheduledRunMissing, NotificationTypePipelineContractFailure, NotificationTypePipelineFreshnessSLABreached}, ProducerBackedNotificationTypes())
}

func TestScheduleNotificationSourceSupportsScopedPolicies(t *testing.T) {
	t.Parallel()
	action := EventActionExecute
	require.True(t, AlertPolicySupportsEventSource(EventEntityTypePipelineTrigger, &action))
}
