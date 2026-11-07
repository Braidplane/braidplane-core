package worker

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/accountclosures"
	"github.com/Braidplane/braidplane-core/service/accountclosuresvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

func TestAccountDeletionLogsCauseWithoutPersistingItInRiver(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	cause := xerror.Unexpected().Detail("archive account acceptance: constraint failed password=fixture-password")
	svc, err := accountclosuresvc.New(failedAccountDeletionTransaction{cause}, accountclosures.Repository{}, struct{ workerproducer.EventProducer }{}, &queue.Client{})
	require.NoError(t, err)
	worker := AccountDeletionWorker{Service: svc}
	err = worker.Work(t.Context(), &river.Job[jobs.AccountDeletionArgs]{Args: jobs.AccountDeletionArgs{Request: "delete-fixture"}})
	require.EqualError(t, err, "user unavailable: account deletion failed")
	require.Contains(t, logs.String(), "archive account acceptance: constraint failed")
	require.NotContains(t, logs.String(), "fixture-password")
}

type failedAccountDeletionTransaction struct{ err error }

func (m failedAccountDeletionTransaction) RunInTx(context.Context, func(context.Context, postgres.DBTX) error) error {
	return m.err
}

func (m failedAccountDeletionTransaction) RunRead(context.Context, func(context.Context, postgres.DBTX) error) error {
	return m.err
}
