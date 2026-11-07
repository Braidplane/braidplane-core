package http

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/reqctx"
)

func TestAuthorizationErrorAttrsIncludeRequestContext(t *testing.T) {
	t.Parallel()

	ctx := reqctx.WithRequestID(context.Background(), "req-123")
	attrs := authorizationErrorAttrs(ctx, fmt.Errorf("policy backend unavailable"))
	byKey := map[string]slog.Attr{}
	for _, attr := range attrs {
		byKey[attr.Key] = attr
	}

	require.Contains(t, byKey, "error")
	require.Equal(t, "req-123", byKey["request_id"].Value.String())
}
