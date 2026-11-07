package http

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubstitutePath(t *testing.T) {
	path, err := substitutePath("/items/{{id}}/{{id}}/{{other}}", map[string]any{"id": "a/b?x=1#fragment", "other": "{{id}}"})
	require.NoError(t, err)
	require.Equal(t, "/items/a%2Fb%3Fx=1%23fragment/a%2Fb%3Fx=1%23fragment/%7B%7Bid%7D%7D", path)
	for _, values := range []map[string]any{{}, {"id": nil}, {"id": ".."}, {"id": "."}} {
		_, err := substitutePath("/items/{{id}}", values)
		require.Error(t, err)
	}
}
