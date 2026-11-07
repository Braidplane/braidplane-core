package producer

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/postgres"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestPipelineRunAdmissionCarriesAccount(t *testing.T) {
	t.Parallel()
	enqueuer := &recordingEnqueuer{queued: queue.QueuedJob{ID: 42}}
	producer, err := NewPipelineRunProducer(enqueuer, &recordingRunJobBinder{t: t})
	require.NoError(t, err)
	_, err = producer.EnqueuePipelineRunExecution(t.Context(), nil, jobs.PipelineRunExecutionArgs{Actor: model.UserEventActor("alice")})
	require.NoError(t, err)
	require.Equal(t, slug.Slug("alice"), enqueuer.req.InitiatingAccount)
}

type recordingRunJobBinder struct{ t *testing.T }

func (r *recordingRunJobBinder) BindWorkerJob(_ context.Context, _ postgres.DBTX, _, _, _ slug.Slug, _ string, id int64) error {
	require.EqualValues(r.t, 42, id)
	return nil
}
