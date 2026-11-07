package pipelineruntime

import (
	"context"
	"time"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func (r *Runner) observeWorker(ctx context.Context, req RunRequest, lastSuccess time.Time, interval time.Duration) (context.Context, func() error) {
	executionCtx, cancelExecution := context.WithCancelCause(ctx)
	observationCtx, cancelObservation := context.WithCancel(executionCtx)
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-observationCtx.Done():
				done <- nil
				return
			case <-ticker.C:
				writeCtx, cancel := context.WithTimeout(observationCtx, interval)
				err := r.store.ObserveWorker(writeCtx, req.OrgSlug, req.ProjectSlug, req.EnvironmentSlug, req.RunSlug, r.clock.Now().UTC())
				cancel()
				if err == nil {
					lastSuccess = r.clock.Now()
					continue
				}
				if observationCtx.Err() != nil {
					done <- nil
					return
				}
				if !xerror.Is(err, pipelinerun.ErrTimelineNotFound) && r.clock.Now().Sub(lastSuccess) < pipelinerun.WorkerObservationStaleAfter {
					continue
				}
				// Ownership loss is immediate; transient reporting failures get one stale window.
				err = errCheckPersistence.With(err)
				cancelExecution(err)
				done <- err
				return
			}
		}
	}()
	return executionCtx, func() error {
		cancelObservation()
		err := <-done
		cancelExecution(nil)
		return err
	}
}
