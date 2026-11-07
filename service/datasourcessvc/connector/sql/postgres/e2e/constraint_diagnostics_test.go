package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/postgres"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/require"
)

func TestNativeConstraintRejectionRetainsSafeDiagnosis(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL fixture")
	}
	t.Parallel()
	ctx := transportsecurity.WithPlaintext(t.Context())
	connector, err := postgres.New(ctx, getTestConfig())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connector.Close()) })
	_, err = executeSQL(ctx, connector, "CREATE TABLE fixture_constraint_diagnosis (id INTEGER PRIMARY KEY, payload TEXT NOT NULL, quantity INTEGER CHECK (quantity > 0))", nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, err := executeSQL(cleanupCtx, connector, "DROP TABLE fixture_constraint_diagnosis", nil)
		require.NoError(t, err)
	})
	_, err = executeSQL(ctx, connector, "INSERT INTO fixture_constraint_diagnosis VALUES (1, 'prior-good', 1)", nil)
	require.NoError(t, err)
	for _, tc := range []struct{ name, sql string }{
		{"check", "INSERT INTO fixture_constraint_diagnosis VALUES (2, 'private-row-value', 0)"},
		{"unique", "INSERT INTO fixture_constraint_diagnosis VALUES (1, 'private-row-value', 1)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statement, err := json.Marshal(map[string]string{"type": "postgres", "sql": tc.sql})
			require.NoError(t, err)
			output, err := connector.Execute(ctx, statement, nil, types.ExecutionLimits{})
			if err == nil && output != nil {
				err = output.Error()
			}
			require.ErrorIs(t, err, xerror.DestinationConstraintViolation())
			require.NotContains(t, err.Error(), "private-row-value")
			require.Contains(t, xerror.PublicDetail(err), "destination constraints")
			encoded, marshalErr := json.Marshal(output)
			require.NoError(t, marshalErr)
			require.NotContains(t, string(encoded), "private-row-value")
			rows, err := executeSQL(ctx, connector, "SELECT payload FROM fixture_constraint_diagnosis ORDER BY id", nil)
			require.NoError(t, err)
			require.Len(t, rows.Rows, 1)
			require.Equal(t, "prior-good", rows.Rows[0]["payload"])
		})
	}
}
