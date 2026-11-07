package backend

import (
	"context"
	"log/slog"
	"time"

	"github.com/Braidplane/braidplane-core/service/pipelineschedulesvc"
	"github.com/Braidplane/braidplane-core/service/runrecencysvc"
)

// The existing periodic observer owns scheduling for both independent checks.
type recencyScheduleObserver struct {
	*pipelineschedulesvc.Service
	recency *runrecencysvc.Service
	logger  *slog.Logger
}

func (s recencyScheduleObserver) DispatchDueSchedules(ctx context.Context, now time.Time) (pipelineschedulesvc.DispatchResult, error) {
	result, err := s.Service.DispatchDueSchedules(ctx, now)
	if err := s.recency.Evaluate(ctx, now); err != nil {
		s.logger.ErrorContext(ctx, "successful-run recency evaluation failed", slog.Any("error", err))
	}
	return result, err
}
