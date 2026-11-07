package repostore

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestNativeGitLimitsRejectMissingAllowances(t *testing.T) {
	t.Parallel()
	for _, maxBytes := range []int64{-1, 0} {
		_, err := WithNativeFileLimits(t.Context(), maxBytes)
		require.ErrorIs(t, err, xerror.ValidationFailed())
	}
	_, err := WithNativeFileLimits(context.Background(), 1)
	require.ErrorIs(t, err, xerror.ExecutionUnavailable())
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	_, err = WithNativeFileLimits(ctx, 1)
	if runtime.GOOS == "linux" {
		require.NoError(t, err)
	} else {
		require.ErrorIs(t, err, xerror.ExecutionUnavailable())
		require.Contains(t, xerror.PublicDetail(err), "Linux executor")
	}
}
