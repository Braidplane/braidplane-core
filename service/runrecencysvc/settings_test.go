package runrecencysvc

import (
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/postgres/runrecency"
	"github.com/stretchr/testify/require"
)

func TestSettingsRejectInvalidUpdatesBeforeDatabase(t *testing.T) {
	txm, repo := &evaluatorTransactions{}, &evaluatorRepository{}
	scope := runrecency.Scope{Org: "acme", Project: "ops", Environment: "prod", Pipeline: "orders"}
	for _, age := range []time.Duration{0, -time.Second, time.Nanosecond} {
		_, err := New(txm, repo, &evaluatorNotifications{}).Update(t.Context(), scope, 0, age, true, testNow)
		require.Error(t, err)
	}
	_, err := New(txm, repo, &evaluatorNotifications{}).Update(t.Context(), scope, -1, time.Hour, true, testNow)
	require.Error(t, err)
	scope.Org = "../other"
	_, err = New(txm, repo, &evaluatorNotifications{}).Update(t.Context(), scope, 0, time.Hour, true, testNow)
	require.Error(t, err)
	require.Zero(t, txm.calls)
}
