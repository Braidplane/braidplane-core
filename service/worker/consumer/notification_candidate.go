package consumer

import (
	"context"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type notificationCandidateService interface {
	CreateCandidate(context.Context, notificationsvc.CreateCandidateParams) (*notificationsvc.CreateCandidateResult, error)
}

func NewNotificationCandidateWorker(service notificationCandidateService) river.Worker[jobs.NotificationCandidateArgs] {
	if service == nil {
		panic("nil notification candidate service")
	}
	return &notificationCandidateWorker{service: service}
}

type notificationCandidateWorker struct {
	river.WorkerDefaults[jobs.NotificationCandidateArgs]
	service notificationCandidateService
}

func (w *notificationCandidateWorker) Work(ctx context.Context, job *river.Job[jobs.NotificationCandidateArgs]) error {
	args := job.Args
	if args.Candidate.RecordSlug == nil || args.Candidate.OrgSlug != args.OrganizationSlug || *args.Candidate.RecordSlug != args.ObservationSlug {
		return xerror.Wrapf("cancel notification candidate: %w", river.JobCancel(xerror.InvalidInput(xerror.Notification).Detail("notification candidate identity mismatch")))
	}
	_, err := w.service.CreateCandidate(ctx, args.Candidate)
	if xerror.Is(err, notificationsvc.ErrInvalidInput) {
		return xerror.Wrapf("cancel notification candidate: %w", river.JobCancel(err))
	}
	return err
}
