package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// GenerateBearerToken returns a random prefixed token, its lookup hash, and a
// display prefix consisting of the token prefix plus eight random characters.
func GenerateBearerToken(prefix string) (string, string, string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", "", xerror.Unexpected().With(err)
	}

	token := prefix + base64.RawURLEncoding.EncodeToString(raw[:])
	return token, HashBearerToken(token), token[:len(prefix)+8], nil
}

// HashBearerToken returns the lookup hash for an API key or personal access token.
func HashBearerToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}
