package ptr

// Ptr returns a pointer address of given value
//
//go:fix inline
func Ptr[V any](v V) *V { return new(v) }

// Value returns a value of given pointer
func Value[V any](v *V) V {
	if v == nil {
		var noop V
		return noop
	}
	return *v
}

// StringPtrOrNil converts a pointer of any string-like type to a pointer of string type.
func StringPtrOrNil[T ~string](v *T) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}
