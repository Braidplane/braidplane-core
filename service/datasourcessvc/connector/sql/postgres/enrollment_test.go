package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestCachedDemoConnectorChecksAuthorizationBeforeIO(t *testing.T) {
	t.Parallel()
	revoked := xerror.ValidationFailed().Detail("demo retired")
	connector := &Connector{authorize: func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), 10*time.Second)
		return revoked
	}}
	require.ErrorIs(t, connector.TestConnection(t.Context()), revoked)
	_, err := connector.IntrospectSchema(t.Context())
	require.ErrorIs(t, err, revoked)
	_, err = connector.ExecuteStatement(t.Context(), "SELECT 1", nil)
	require.ErrorIs(t, err, revoked)
	result, err := connector.Execute(t.Context(), []byte(`{"type":"postgres","sql":"SELECT 1"}`), nil)
	require.NoError(t, err)
	require.ErrorIs(t, result.Error(), revoked)
}
