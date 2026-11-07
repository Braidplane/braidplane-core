package as

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMustReturnsTypedValue(t *testing.T) {
	t.Parallel()

	require.Equal(t, "value", Must[string](t, "value"))
	require.Equal(t, 42, Must[int](t, 42))
}
