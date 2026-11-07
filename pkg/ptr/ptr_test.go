package ptr

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPtrValueAndStringPtrOrNil(t *testing.T) {
	t.Parallel()

	value := Ptr("hello")
	require.NotNil(t, value)
	require.Equal(t, "hello", *value)
	require.Equal(t, "hello", Value(value))
	require.Empty(t, Value[string](nil))

	type alias string
	raw := alias("world")
	converted := StringPtrOrNil(&raw)
	require.NotNil(t, converted)
	require.Equal(t, "world", *converted)
	require.Nil(t, StringPtrOrNil[alias](nil))
}
