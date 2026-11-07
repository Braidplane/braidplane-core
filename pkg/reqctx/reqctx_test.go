package reqctx

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestIDContextHelpers(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsNilWhenMissing", func(t *testing.T) {
		t.Parallel()

		require.Nil(t, GetRequestID(context.Background()))
	})

	t.Run("ReturnsStoredRequestID", func(t *testing.T) {
		t.Parallel()

		ctx := WithRequestID(context.Background(), "req-123")
		require.NotNil(t, GetRequestID(ctx))
		require.Equal(t, "req-123", *GetRequestID(ctx))
	})
}
