package backend

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/stretchr/testify/require"
)

func TestCredentialKeyringConfiguration(t *testing.T) {
	t.Parallel()
	scope := crypto.CredentialScope{Tenant: "synthetic", Resource: "credential", Purpose: "static-secret", SchemaVersion: 1}
	initial := SecurityConfig{EncryptionKeyHex: testEncryptionKeyHex}
	ring, err := initial.CredentialKeyring()
	require.NoError(t, err)
	sealed, err := ring.Seal(scope, []byte("synthetic"))
	require.NoError(t, err)
	newKey := "0303030303030303030303030303030303030303030303030303030303030303"
	keys, err := json.Marshal(map[string]string{"initial": testEncryptionKeyHex, "rotated": newKey})
	require.NoError(t, err)
	rotation := initial
	rotation.CredentialCurrentKeyID, rotation.CredentialKeysHex = "rotated", string(keys)
	rotated, err := rotation.CredentialKeyring()
	require.NoError(t, err)
	_, err = rotated.Open(scope, sealed)
	require.NoError(t, err)
	legacyKey, err := hex.DecodeString(testEncryptionKeyHex)
	require.NoError(t, err)
	legacy, err := crypto.NewAES256(legacyKey)
	require.NoError(t, err)
	unversioned, err := legacy.Encrypt([]byte("synthetic"))
	require.NoError(t, err)
	_, err = rotated.Open(scope, unversioned)
	require.NoError(t, err)
	rotation.CredentialDisableLegacy = true
	strict, err := rotation.CredentialKeyring()
	require.NoError(t, err)
	_, err = strict.Open(scope, unversioned)
	require.ErrorIs(t, err, crypto.ErrCredentialKeyUnavailable)
	_, err = strict.Open(scope, sealed)
	require.NoError(t, err)
}

func TestCredentialKeyringRejectsInvalidConfigurationWithoutLeakingValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ current, keys string }{
		{"", "{}"},
		{"missing", ""},
		{"missing", "null"},
		{"missing", "not JSON"},
		{"valid", `{"valid":"secret-invalid-key"}`},
		{"valid", `{"valid":"0000000000000000000000000000000000000000000000000000000000000000"}`},
		{"valid", `{"valid":"0101010101010101010101010101010101010101010101010101010101010101"}`},
		{"valid", `{"valid":"0303030303030303030303030303030303030303030303030303030303030303"}`},
		{"bad/id", `{"bad/id":"0202020202020202020202020202020202020202020202020202020202020202"}`},
	} {
		cfg := SecurityConfig{EncryptionKeyHex: testEncryptionKeyHex, ExecutionRequestKeyHex: "0303030303030303030303030303030303030303030303030303030303030303", CredentialCurrentKeyID: tc.current, CredentialKeysHex: tc.keys}
		_, err := cfg.CredentialKeyring()
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret-invalid-key")
		require.NotContains(t, err.Error(), testEncryptionKeyHex)
	}
}

func TestProofEvidenceKeyCannotMatchRetainedProductKeys(t *testing.T) {
	t.Parallel()
	key := "0404040404040404040404040404040404040404040404040404040404040404"
	for _, current := range []string{"initial", "rotated"} {
		cfg := validBackendConfig()
		cfg.Security.CredentialCurrentKeyID = current
		encoded, err := json.Marshal(map[string]string{"initial": testEncryptionKeyHex, "rotated": key})
		require.NoError(t, err)
		cfg.Security.CredentialKeysHex = string(encoded)
		cfg.ProofObservations = ProofObservationsConfig{Enabled: true, EvidenceKeyHex: key}
		require.ErrorContains(t, cfg.validate(), "independent non-default evidence key")
	}
}
