package executiondelivery

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/stretchr/testify/require"
)

func TestRequestRequiresNonzeroClaim(t *testing.T) {
	t.Parallel()
	_, ok := Request(t.Context())
	require.False(t, ok)
	_, ok = Request(WithRequest(t.Context(), uuid.UUID{}))
	require.False(t, ok)
	id := uuid.New()
	got, ok := Request(WithRequest(t.Context(), id))
	require.True(t, ok)
	require.Equal(t, id, got)
}
