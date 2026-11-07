package consumer

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
)

type candidateServiceFunc func(context.Context, notificationsvc.CreateCandidateParams) (*notificationsvc.CreateCandidateResult, error)

func (f candidateServiceFunc) CreateCandidate(ctx context.Context, p notificationsvc.CreateCandidateParams) (*notificationsvc.CreateCandidateResult, error) {
	return f(ctx, p)
}

func TestNotificationCandidateWorkerRetriesGenericCreation(t *testing.T) {
	candidate := model.NotificationCandidate{OrgSlug: "acme", RecordSlug: new(slug.Slug("observation"))}
	args := jobs.NotificationCandidateArgs{OrganizationSlug: candidate.OrgSlug, Candidate: candidate, ObservationSlug: *candidate.RecordSlug}
	calls := 0
	unavailable := xerror.Unavailable(xerror.Notification)
	worker := NewNotificationCandidateWorker(candidateServiceFunc(func(_ context.Context, got notificationsvc.CreateCandidateParams) (*notificationsvc.CreateCandidateResult, error) {
		calls++
		require.Equal(t, candidate, got)
		if calls == 1 {
			return nil, unavailable
		}
		return &notificationsvc.CreateCandidateResult{}, nil
	}))
	job := &river.Job[jobs.NotificationCandidateArgs]{Args: args}
	require.ErrorIs(t, worker.Work(t.Context(), job), unavailable)
	require.NoError(t, worker.Work(t.Context(), job))
	require.Equal(t, 2, calls)
	job.Args.OrganizationSlug = "other-tenant"
	var cancelled *rivertype.JobCancelError
	require.ErrorAs(t, worker.Work(t.Context(), job), &cancelled)
	require.Equal(t, 2, calls, "inconsistent tenant identity never reaches the notification service")
}

func TestNotificationCandidateWorkerCancelsPermanentInputErrors(t *testing.T) {
	worker := NewNotificationCandidateWorker(candidateServiceFunc(func(context.Context, notificationsvc.CreateCandidateParams) (*notificationsvc.CreateCandidateResult, error) {
		return nil, notificationsvc.ErrInvalidInput.Detail("candidate scope was deleted")
	}))
	candidate := model.NotificationCandidate{OrgSlug: "acme", RecordSlug: new(slug.Slug("observation"))}
	job := &river.Job[jobs.NotificationCandidateArgs]{Args: jobs.NotificationCandidateArgs{OrganizationSlug: candidate.OrgSlug, Candidate: candidate, ObservationSlug: *candidate.RecordSlug}}
	err := worker.Work(t.Context(), job)
	var cancelled *rivertype.JobCancelError
	require.ErrorAs(t, err, &cancelled)
	require.ErrorIs(t, err, notificationsvc.ErrInvalidInput)
}
