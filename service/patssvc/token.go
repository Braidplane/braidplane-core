package patssvc

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	tokenByteLength = 32
	TokenPrefix     = "xto_pat_"
	tokenPrefixLen  = 16
)

func generateToken() (string, string, string, error) {
	raw := make([]byte, tokenByteLength)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", xerror.Unexpected().With(err)
	}

	token := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	hash := hashToken(token)
	prefix := token
	if len(prefix) > tokenPrefixLen {
		prefix = prefix[:tokenPrefixLen]
	}

	return token, hash, prefix, nil
}

func hashToken(token string) string {
	hash := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(hash[:])
}
