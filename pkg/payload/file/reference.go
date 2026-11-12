package filepayload

// Reference identifies encrypted retained input inside the execution protocol.
// Public result and run metadata must omit this capability-bearing reference.
type Reference struct {
	ID        string `json:"id"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// CloneReference keeps process-local payload clones independent.
func CloneReference(ref *Reference) *Reference {
	if ref == nil {
		return nil
	}
	cloned := *ref
	return &cloned
}
