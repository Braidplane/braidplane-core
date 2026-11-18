package passwordpolicy

import (
	"crypto/sha256"
	_ "embed"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const commonPasswordCount = 3000

//go:embed common-passwords.sha256
var commonPasswordData []byte

var commonPasswords = loadCommonPasswords()

func loadCommonPasswords() map[[sha256.Size]byte]struct{} {
	if len(commonPasswordData) != commonPasswordCount*sha256.Size {
		panic("invalid common password blocklist size")
	}
	set := make(map[[sha256.Size]byte]struct{}, commonPasswordCount)
	for offset := 0; offset < len(commonPasswordData); offset += sha256.Size {
		set[[sha256.Size]byte(commonPasswordData[offset:offset+sha256.Size])] = struct{}{}
	}
	if len(set) != commonPasswordCount {
		panic("duplicate common password blocklist entries")
	}
	return set
}

// ErrCommonPassword explains a blocklist rejection without echoing the password.
var ErrCommonPassword = xerror.New(xerror.KindInvalidInput, "PASSWORD_TOO_COMMON", "Password is too common").
	WithPublicDetail("Choose a less common password.")

// CheckCommon checks a new password without changing what the account will hash.
func CheckCommon(password string) error {
	key := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(password))))
	if _, found := commonPasswords[key]; found {
		return ErrCommonPassword
	}
	return nil
}
