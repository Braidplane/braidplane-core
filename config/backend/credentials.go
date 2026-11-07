package backend

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
)

// CredentialKeyring leaves the original application key available to queued
// emails and consumers with separate lifecycles when product keys rotate.
func (s SecurityConfig) CredentialKeyring() (*crypto.CredentialKeyring, error) {
	legacy, err := hex.DecodeString(strings.TrimSpace(s.EncryptionKeyHex))
	if err != nil || len(legacy) != 32 {
		return nil, crypto.ErrCredentialKeyring
	}
	current := s.CredentialCurrentKeyID
	keys := map[string][]byte{}
	if s.CredentialKeysHex == "" && current == "" {
		current = "initial"
		keys[current] = legacy
	} else {
		var encoded map[string]string
		if json.Unmarshal([]byte(s.CredentialKeysHex), &encoded) != nil || len(encoded) == 0 {
			return nil, crypto.ErrCredentialKeyring
		}
		for id, value := range encoded {
			value = strings.TrimSpace(value)
			key, err := hex.DecodeString(value)
			if err != nil || len(key) != 32 || isAllZeroHexKey(value) || strings.EqualFold(value, defaultEncryptionKeyHex) ||
				strings.EqualFold(value, strings.TrimSpace(s.ExecutionRequestKeyHex)) {
				return nil, crypto.ErrCredentialKeyring
			}
			keys[id] = key
		}
	}
	if s.CredentialDisableLegacy {
		legacy = nil
	}
	return crypto.NewCredentialKeyring(current, keys, legacy)
}

func (s SecurityConfig) credentialKeyMatches(value string) bool {
	if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(s.EncryptionKeyHex)) {
		return true
	}
	var keys map[string]string
	if json.Unmarshal([]byte(s.CredentialKeysHex), &keys) != nil {
		return false
	}
	for _, key := range keys {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(key)) {
			return true
		}
	}
	return false
}
