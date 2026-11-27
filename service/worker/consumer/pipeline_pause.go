package consumer

import (
	"time"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

func snoozePausedPipeline(err error) error {
	if xerror.Is(err, pipelinesvc.ErrExecutionPaused) || xerror.Is(err, pipelinesvc.ErrExecutionControlRevision) {
		return xerror.Wrapf("hold pipeline execution: %w", river.JobSnooze(30*time.Second))
	}
	return nil
}
