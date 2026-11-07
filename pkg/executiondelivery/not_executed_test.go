package executiondelivery

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestNotExecutedPreservesErrorIdentity(t *testing.T) {
	t.Parallel()
	cause := xerror.Unavailable(xerror.DataSource)
	marked := MarkNotExecuted(cause)
	require.True(t, IsNotExecuted(xerror.Wrapf("connection test: %w", marked)))
	require.ErrorIs(t, marked, cause)
	require.False(t, IsNotExecuted(cause))
	require.False(t, IsNotExecuted(nil))
	require.NoError(t, MarkNotExecuted(nil))
}
