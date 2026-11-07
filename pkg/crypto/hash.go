package crypto

// Hasher is the interface that wraps the basic Hash and Check methods
type Hasher interface {
	Hash(input string) (string, error)
	CompareHash(input, hash string) (bool, error)
}
