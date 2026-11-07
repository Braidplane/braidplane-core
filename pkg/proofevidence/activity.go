package proofevidence

// ActivitySnapshot contains deployment-wide counts over the last ten minutes.
// Starts describe execution admission, not proof that a provider request was sent.
type ActivitySnapshot struct {
	Starts  int64
	Results map[Outcome]int64
}
