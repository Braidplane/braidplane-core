package consumer

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
	"github.com/stretchr/testify/require"
)

type failedMaintenanceTransaction struct{ t *testing.T }

func (f failedMaintenanceTransaction) RunRead(ctx context.Context, _ func(context.Context, postgres.DBTX) error) error {
	deadline, ok := ctx.Deadline()
	require.True(f.t, ok)
	require.LessOrEqual(f.t, time.Until(deadline), 30*time.Second)
	return xerror.Unexpected().Detail("private-database-diagnostic")
}

func (f failedMaintenanceTransaction) RunInTx(ctx context.Context, fn func(context.Context, postgres.DBTX) error) error {
	return f.RunRead(ctx, fn)
}

func TestProofMaintenanceWorkerBoundsAndSanitizesFailure(t *testing.T) {
	t.Parallel()
	maintenance, err := proofevidencesvc.NewMaintenance(failedMaintenanceTransaction{t: t}, proofobservations.New(), proofreceipts.New(), eventrecorder.New(&recordingEventWorkerService{}))
	require.NoError(t, err)
	var logs bytes.Buffer
	worker, err := NewProofObservationMaintenanceWorker(maintenance, slog.New(slog.NewTextHandler(&logs, nil)))
	require.NoError(t, err)
	err = worker.Work(t.Context(), nil)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private-database-diagnostic")
	require.NotContains(t, logs.String(), "private-database-diagnostic")
	require.Contains(t, logs.String(), "proof observation maintenance failed")
}
