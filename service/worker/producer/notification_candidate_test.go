package producer

import (
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/stretchr/testify/require"
)

func TestNotificationCandidateProducerSharesHandoffAcrossTypes(t *testing.T) {
	for _, kind := range []model.NotificationType{model.NotificationTypePipelineContractFailure, model.NotificationTypePipelineRunSucceeded, model.NotificationTypePipelineRetryExhausted} {
		t.Run(kind.String(), func(t *testing.T) {
			queue := &recordingEnqueuer{}
			producer, err := NewNotificationCandidateProducer(queue)
			require.NoError(t, err)
			candidate := model.NotificationCandidate{
				OrgSlug: "acme", NotificationType: kind, Severity: model.NotificationSeverityWarning,
				DedupeFingerprint: "durable-observation", SourceEntityType: model.EventEntityTypePipelineRun, SourceAction: model.EventActionExecute,
				SourceRefs: map[string]string{"run_slug": "run-1"}, SafeMetadata: map[string]any{"reason_code": "contract_violation"},
			}
			require.NoError(t, producer.EnqueueCandidate(t.Context(), nil, candidate))
			args := queue.req.Args.(jobs.NotificationCandidateArgs)
			require.Equal(t, kind, args.Candidate.NotificationType)
			require.Equal(t, candidate.OrgSlug, args.OrganizationSlug)
			require.Equal(t, *args.Candidate.RecordSlug, args.ObservationSlug)
			require.Equal(t, jobs.NotificationDeliveryQueue, queue.req.Queue)
			require.NoError(t, producer.EnqueueCandidate(t.Context(), nil, candidate))
			require.Equal(t, args.ObservationSlug, queue.req.Args.(jobs.NotificationCandidateArgs).ObservationSlug)
			encoded, err := json.Marshal(args)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "credential")
			require.NotContains(t, string(encoded), "destination")
			candidate.SafeMetadata["raw_payload"] = "must never reach River"
			require.ErrorIs(t, producer.EnqueueCandidate(t.Context(), nil, candidate), notificationsvc.ErrInvalidInput)
			require.Equal(t, 2, queue.calls)
		})
	}
}
