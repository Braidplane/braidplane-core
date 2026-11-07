package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var (
	ErrInvalidHash         = xerror.InvalidInput(xerror.PasswordHash)
	ErrIncompatibleVersion = xerror.Unsupported(xerror.PasswordHash)
)

type params struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
	saltLength  uint32
	keyLength   uint32
}

var defaultParams = &params{
	memory:      64 * 1024,
	iterations:  3,
	parallelism: 2,
	saltLength:  16,
	keyLength:   32,
}

type argon2Hasher struct {
	params *params
}

func NewArgon2() Hasher {
	return &argon2Hasher{params: defaultParams}
}

// NewArgon2WithParams creates a new Argon2 hasher with custom parameters
func NewArgon2WithParams(
	memory, iterations uint32,
	parallelism uint8,
	saltLength, keyLength uint32,
) Hasher {
	return &argon2Hasher{
		params: &params{
			memory:      memory,
			iterations:  iterations,
			parallelism: parallelism,
			saltLength:  saltLength,
			keyLength:   keyLength,
		},
	}
}

func (h *argon2Hasher) Hash(input string) (string, error) {
	salt := make([]byte, h.params.saltLength)
	_, err := rand.Read(salt)
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}

	hash := argon2.IDKey(
		[]byte(input),
		salt,
		h.params.iterations,
		h.params.memory,
		h.params.parallelism,
		h.params.keyLength,
	)

	// Base64 encode the salt and hashed password.
	b64Salt := base64.RawStdEncoding.Strict().EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.Strict().EncodeToString(hash)

	// Return a string using the standard encoded hash representation.
	encodedHash := fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		h.params.memory,
		h.params.iterations,
		h.params.parallelism,
		b64Salt,
		b64Hash,
	)

	return encodedHash, nil
}

func (h *argon2Hasher) CompareHash(input, encodedHash string) (bool, error) {
	match, err := comparePasswordAndHash(input, encodedHash)
	if err != nil {
		return false, xerror.Wrapf("failed to compare password and hash: %w", err)
	}

	return match, nil
}

func comparePasswordAndHash(password, encodedHash string) (match bool, err error) {
	// Extract the parameters, salt and derived key from the encoded password
	// hash.
	p, salt, hash, err := decodeHash(encodedHash)
	if err != nil {
		return false, xerror.Wrapf("failed to decode hash: %w", err)
	}

	// Derive the key from the other password using the same parameters.
	otherHash := argon2.IDKey(
		[]byte(password),
		salt,
		p.iterations,
		p.memory,
		p.parallelism,
		p.keyLength,
	)

	// Check that the contents of the hashed passwords are identical. Note
	// that we are using the subtle.ConstantTimeCompare() function for this
	// to help prevent timing attacks.
	if subtle.ConstantTimeCompare(hash, otherHash) == 1 {
		return true, nil
	}
	return false, nil
}

func decodeHash(encodedHash string) (*params, []byte, []byte, error) {
	vals := strings.Split(encodedHash, "$")
	if len(vals) != 6 {
		return nil, nil, nil, ErrInvalidHash
	}

	var version int
	_, err := fmt.Sscanf(vals[2], "v=%d", &version)
	if err != nil {
		return nil, nil, nil, ErrInvalidHash.With(err)
	}
	if version != argon2.Version {
		return nil, nil, nil, ErrIncompatibleVersion
	}

	var (
		memory      uint32
		iterations  uint32
		parallelism uint8
	)

	_, err = fmt.Sscanf(vals[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism)
	if err != nil {
		return nil, nil, nil, ErrInvalidHash.With(err)
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(vals[4])
	if err != nil {
		return nil, nil, nil, ErrInvalidHash.With(err)
	}

	hash, err := base64.RawStdEncoding.Strict().DecodeString(vals[5])
	if err != nil {
		return nil, nil, nil, ErrInvalidHash.With(err)
	}
	if len(salt) > math.MaxUint32 || len(hash) > math.MaxUint32 {
		return nil, nil, nil, ErrInvalidHash.Detail("salt or hash length exceeds supported range")
	}

	p := &params{
		memory:      memory,
		iterations:  iterations,
		parallelism: parallelism,
		saltLength:  uint32(len(salt)),
		keyLength:   uint32(len(hash)),
	}

	return p, salt, hash, nil
}
