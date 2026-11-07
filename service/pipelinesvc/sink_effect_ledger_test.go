package pipelinesvc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinesinkeffects"
)

// TestNewSinkEffectLedger validates required durable-ledger dependencies at startup.
func TestNewSinkEffectLedger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		txm        postgres.TransactionManager
		repo       pipelinesinkeffects.Repository
		wantDetail string
	}{
		{
			name:       "RejectsMissingTransactionManager",
			repo:       pipelinesinkeffects.New(),
			wantDetail: "sink effect ledger transaction manager is required",
		},
		{
			name:       "RejectsMissingRepository",
			txm:        testutil.FakeTransactionManager{},
			wantDetail: "sink effect ledger repository is required",
		},
		{
			name: "CreatesLedger",
			txm:  testutil.FakeTransactionManager{},
			repo: pipelinesinkeffects.New(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ledger, err := NewSinkEffectLedger(tc.txm, tc.repo)
			if tc.wantDetail != "" {
				require.Nil(t, ledger)
				require.Error(t, err)
				require.True(t, xerror.Is(err, xerror.ExecutionUnavailable()))
				require.ErrorContains(t, err, tc.wantDetail)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, ledger)
		})
	}
}
