package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"io"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var (
	ErrCredentialKeyUnavailable = xerror.New(xerror.KindInternal, "CREDENTIAL_KEY_UNAVAILABLE", "credential encryption key unavailable")
	ErrCredentialCiphertext     = xerror.New(xerror.KindInternal, "CREDENTIAL_CIPHERTEXT_INVALID", "credential ciphertext invalid")
	ErrCredentialScope          = xerror.New(xerror.KindInternal, "CREDENTIAL_SCOPE_INVALID", "credential encryption scope invalid")
	ErrCredentialKeyring        = xerror.New(xerror.KindInternal, "CREDENTIAL_KEYRING_INVALID", "credential encryption keyring invalid")
)

const envelopeMagic = "BP.CREDENTIAL\x00\x01"

// CredentialScope comes from the expected database record, never the ciphertext.
// SchemaVersion identifies the plaintext contract independently of the cipher format.
type CredentialScope struct {
	Tenant        string
	Resource      string
	Purpose       string
	SchemaVersion uint32
}

// CredentialCipher protects persisted product secrets. Queued email tokens and
// short-lived execution envelopes have separate keys and lifecycles.
type CredentialCipher interface {
	Seal(CredentialScope, []byte) ([]byte, error)
	Open(CredentialScope, []byte) ([]byte, error)
}

// CredentialKeyring encrypts with one key and decrypts with explicitly retained
// keys. The optional legacy key reads pre-versioning records only.
type CredentialKeyring struct {
	current string
	keys    map[string]cipher.AEAD
	legacy  AES256
}

func (r *CredentialKeyring) CurrentKeyID() string { return r.current }

func NewCredentialKeyring(current string, keys map[string][]byte, legacyKey []byte) (*CredentialKeyring, error) {
	if !validCredentialKeyID(current) || len(keys) == 0 {
		return nil, ErrCredentialKeyring
	}
	ring := &CredentialKeyring{current: current, keys: make(map[string]cipher.AEAD, len(keys))}
	for id, key := range keys {
		if !validCredentialKeyID(id) || len(key) != 32 {
			return nil, ErrCredentialKeyring
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, ErrCredentialKeyring
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, ErrCredentialKeyring
		}
		ring.keys[id] = aead
	}
	if ring.keys[current] == nil {
		return nil, ErrCredentialKeyUnavailable
	}
	if legacyKey != nil {
		if len(legacyKey) != 32 {
			return nil, ErrCredentialKeyring
		}
		ring.legacy, _ = NewAES256(bytes.Clone(legacyKey))
	}
	return ring, nil
}

func validCredentialKeyID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.", c) {
			continue
		}
		return false
	}
	return true
}

func credentialAAD(scope CredentialScope, header []byte) ([]byte, error) {
	if scope.Tenant == "" || scope.Resource == "" || scope.Purpose == "" || scope.SchemaVersion == 0 {
		return nil, ErrCredentialScope
	}
	// JSON keeps field boundaries unambiguous, including embedded delimiters.
	encoded, err := json.Marshal(scope)
	if err != nil {
		return nil, ErrCredentialScope
	}
	return append(encoded, header...), nil
}

func (r *CredentialKeyring) Seal(scope CredentialScope, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, ErrCredentialCiphertext
	}
	header := append([]byte(envelopeMagic), byte(len(r.current))) // #nosec G115 -- Constructor bounds the immutable ID to 64 bytes.
	header = append(header, r.current...)
	aad, err := credentialAAD(scope, header)
	if err != nil {
		return nil, err
	}
	aead := r.keys[r.current]
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, ErrCredentialCiphertext
	}
	return aead.Seal(append(header, nonce...), nonce, plaintext, aad), nil
}

func (r *CredentialKeyring) Open(scope CredentialScope, ciphertext []byte) ([]byte, error) {
	if _, err := credentialAAD(scope, nil); err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(ciphertext, []byte(envelopeMagic[:len(envelopeMagic)-1])) {
		if r.legacy == nil {
			return nil, ErrCredentialKeyUnavailable
		}
		plaintext, err := r.legacy.Decrypt(ciphertext)
		if err != nil {
			return nil, ErrCredentialCiphertext
		}
		return plaintext, nil
	}
	if len(ciphertext) < len(envelopeMagic)+1 || !bytes.HasPrefix(ciphertext, []byte(envelopeMagic)) {
		return nil, ErrCredentialCiphertext
	}
	headerSize := len(envelopeMagic) + 1 + int(ciphertext[len(envelopeMagic)])
	if headerSize > len(ciphertext) || !validCredentialKeyID(string(ciphertext[len(envelopeMagic)+1:headerSize])) {
		return nil, ErrCredentialCiphertext
	}
	aead := r.keys[string(ciphertext[len(envelopeMagic)+1:headerSize])]
	if aead == nil {
		return nil, ErrCredentialKeyUnavailable
	}
	if len(ciphertext)-headerSize < aead.NonceSize()+aead.Overhead() {
		return nil, ErrCredentialCiphertext
	}
	aad, err := credentialAAD(scope, ciphertext[:headerSize])
	if err != nil {
		return nil, err
	}
	nonce := ciphertext[headerSize : headerSize+aead.NonceSize()]
	plaintext, err := aead.Open(nil, nonce, ciphertext[headerSize+aead.NonceSize():], aad)
	if err != nil {
		return nil, ErrCredentialCiphertext
	}
	return plaintext, nil
}

// Rekey authenticates even current-key records. Unchanged records retain their
// original bytes so a resumable sweep can skip writes and preserve revisions.
func (r *CredentialKeyring) Rekey(scope CredentialScope, ciphertext []byte) ([]byte, bool, error) {
	plaintext, err := r.Open(scope, ciphertext)
	if err != nil {
		return nil, false, err
	}
	defer clear(plaintext)
	header := append([]byte(envelopeMagic), byte(len(r.current))) // #nosec G115 -- Constructor bounds the immutable ID to 64 bytes.
	header = append(header, r.current...)
	if bytes.HasPrefix(ciphertext, header) {
		return bytes.Clone(ciphertext), false, nil
	}
	sealed, err := r.Seal(scope, plaintext)
	return sealed, err == nil, err
}
