package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"io"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// AES256 is the interface for AES-256-GCM encryption/decryption.
type AES256 interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// aes256 implements AES256 interface.
type aes256 struct {
	key []byte
}

// NewAES256 creates a new AES256 encrypter with the given key.
// The key must be 32 bytes (256 bits).
func NewAES256(key []byte) (AES256, error) {
	if len(key) != 32 {
		return nil, xerror.Unexpected().Detailf(
			"encryption key must be 32 bytes (AES-256), got %d",
			len(key),
		)
	}
	return &aes256{key: key}, nil
}

// Encrypt implements AES256.Encrypt.
func (a *aes256) Encrypt(plaintext []byte) ([]byte, error) {
	if len(a.key) != 32 {
		return nil, xerror.Unexpected().Detailf(
			"encryption key must be 32 bytes (AES-256), got %d",
			len(a.key),
		)
	}

	if len(plaintext) == 0 {
		return nil, xerror.Unexpected().Detail("plaintext cannot be empty")
	}

	block, err := aes.NewCipher(a.key)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	// Create a nonce (12 bytes is standard for GCM)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	// Seal returns ciphertext + auth tag
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// Decrypt implements AES256.Decrypt.
func (a *aes256) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(a.key) != 32 {
		return nil, xerror.Unexpected().Detailf(
			"decryption key must be 32 bytes (AES-256), got %d",
			len(a.key),
		)
	}

	if len(ciphertext) == 0 {
		return nil, xerror.Unexpected().Detail("ciphertext cannot be empty")
	}

	block, err := aes.NewCipher(a.key)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, xerror.Unexpected().Detailf(
			"ciphertext too short: expected at least %d bytes, got %d",
			nonceSize,
			len(ciphertext),
		)
	}

	// Extract nonce from the beginning of ciphertext
	nonce := ciphertext[:nonceSize]
	encryptedData := ciphertext[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, encryptedData, nil)
	if err != nil {
		return nil, xerror.Unexpected().Detailf("failed to decrypt: %w", err)
	}

	return plaintext, nil
}
