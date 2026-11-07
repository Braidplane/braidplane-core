package accountclosuresvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/accountclosures"
)

type failingTransactions struct {
	testutil.FakeTransactionManager
	calls int
}

func (m *failingTransactions) RunInTx(context.Context, func(context.Context, postgres.DBTX) error) error {
	m.calls++
	if m.calls == 1 {
		return xerror.Unexpected().Detail("deletion failed")
	}
	return xerror.Unavailable(xerror.User).Detail("recording failure failed")
}

func TestCompletionPreservesDeletionAndFailureRecordingErrors(t *testing.T) {
	t.Parallel()
	txm := &failingTransactions{}
	svc := &Service{txm: txm}
	completed, err := svc.Complete(t.Context(), "delete-test")
	require.False(t, completed)
	require.Equal(t, 2, txm.calls)
	require.ErrorIs(t, err, xerror.Unexpected())
	require.ErrorIs(t, err, xerror.Unavailable(xerror.User))
}

func TestRequiresTransactionsAndAudit(t *testing.T) {
	t.Parallel()
	_, err := New(nil, accountclosures.Repository{}, eventtest.NewProducer(nil), &queue.Client{})
	require.ErrorIs(t, err, xerror.Unexpected())
	_, err = New(testutil.FakeTransactionManager{}, accountclosures.Repository{}, nil, &queue.Client{})
	require.ErrorIs(t, err, xerror.Unexpected())
	_, err = New(testutil.FakeTransactionManager{}, accountclosures.Repository{}, eventtest.NewProducer(nil), nil)
	require.ErrorIs(t, err, xerror.Unexpected())
}

func TestResultState(t *testing.T) {
	t.Parallel()
	result, err := toResult(accountclosures.Request{State: "pending"})
	require.NoError(t, err)
	require.Equal(t, StatePending, result.State)
	require.Equal(t, "pending", result.State.String())
	_, err = toResult(accountclosures.Request{State: "invalid"})
	require.ErrorIs(t, err, xerror.Unexpected())
}
