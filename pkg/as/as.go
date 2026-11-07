package as

import "testing"

// Must asserts that v is of type T and fails the test if not.
func Must[T any](t testing.TB, v any) T {
	t.Helper()

	value, ok := v.(T)
	if !ok {
		var zero T
		t.Fatalf("expected %T, got %T", zero, v)
	}

	return value
}
