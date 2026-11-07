package crypto

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func credentialTestScope() CredentialScope {
	return CredentialScope{Tenant: "synthetic-tenant", Resource: "synthetic-record", Purpose: "static-secret", SchemaVersion: 1}
}

func TestCredentialKeyringScopeAndTampering(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{2}, 32)
	ring, err := NewCredentialKeyring("current", map[string][]byte{"current": key, "another": key}, nil)
	require.NoError(t, err)
	scope := credentialTestScope()
	plaintext := []byte("synthetic credential only")
	sealed, err := ring.Seal(scope, plaintext)
	require.NoError(t, err)
	require.NotContains(t, string(sealed), string(plaintext))
	opened, err := ring.Open(scope, sealed)
	require.NoError(t, err)
	require.Equal(t, plaintext, opened)
	second, err := ring.Seal(scope, plaintext)
	require.NoError(t, err)
	require.NotEqual(t, sealed, second)
	for _, change := range []func(*CredentialScope){
		func(s *CredentialScope) { s.Tenant = "foreign-tenant" },
		func(s *CredentialScope) { s.Resource = "foreign-record" },
		func(s *CredentialScope) { s.Purpose = "oauth-token-set" },
		func(s *CredentialScope) { s.SchemaVersion++ },
		func(s *CredentialScope) { s.Tenant += s.Resource; s.Resource = "record" },
	} {
		other := scope
		change(&other)
		opened, err := ring.Open(other, sealed)
		require.ErrorIs(t, err, ErrCredentialCiphertext)
		require.Nil(t, opened)
	}
	for i := range sealed {
		tampered := bytes.Clone(sealed)
		tampered[i] ^= 1
		opened, err := ring.Open(scope, tampered)
		require.Error(t, err, "changed byte %d", i)
		require.Nil(t, opened)
		require.NotContains(t, err.Error(), string(plaintext))
	}
	// A different known ID with identical material must still fail authentication.
	tampered := bytes.Replace(sealed, []byte("current"), []byte("another"), 1)
	_, err = ring.Open(scope, tampered)
	require.ErrorIs(t, err, ErrCredentialCiphertext)
	for i := range sealed {
		_, err := ring.Open(scope, sealed[:i])
		require.Error(t, err, "truncated at byte %d", i)
	}
}

func TestCredentialKeyringRotationRestoreAndRetirement(t *testing.T) {
	t.Parallel()
	oldKey, newKey := bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32)
	scope := credentialTestScope()
	plaintext := []byte("synthetic backup credential")
	old, err := NewCredentialKeyring("old", map[string][]byte{"old": oldKey}, nil)
	require.NoError(t, err)
	backup, err := old.Seal(scope, plaintext)
	require.NoError(t, err)
	rotated, err := NewCredentialKeyring("new", map[string][]byte{"old": oldKey, "new": newKey}, nil)
	require.NoError(t, err)
	current, changed, err := rotated.Rekey(scope, backup)
	require.NoError(t, err)
	require.True(t, changed)
	opened, err := rotated.Open(scope, current)
	require.NoError(t, err)
	require.Equal(t, plaintext, opened)
	unchanged, changed, err := rotated.Rekey(scope, current)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, current, unchanged)
	// Restoring an older database still requires the old key after current rows rekey.
	opened, err = rotated.Open(scope, backup)
	require.NoError(t, err)
	require.Equal(t, plaintext, opened)
	retired, err := NewCredentialKeyring("new", map[string][]byte{"new": newKey}, nil)
	require.NoError(t, err)
	_, err = retired.Open(scope, backup)
	require.ErrorIs(t, err, ErrCredentialKeyUnavailable)
	_, err = retired.Open(scope, current)
	require.NoError(t, err)
	// Roll back the write key while retaining read keys for both generations.
	rollback, err := NewCredentialKeyring("old", map[string][]byte{"old": oldKey, "new": newKey}, nil)
	require.NoError(t, err)
	_, err = rollback.Open(scope, current)
	require.NoError(t, err)
	wrong, err := NewCredentialKeyring("new", map[string][]byte{"new": oldKey}, nil)
	require.NoError(t, err)
	_, _, err = wrong.Rekey(scope, current)
	require.ErrorIs(t, err, ErrCredentialCiphertext)
	// Constructor owns the key bytes, so caller mutation cannot change an active ring.
	clear(newKey)
	_, err = rotated.Open(scope, current)
	require.NoError(t, err)
}

func TestCredentialKeyringLegacyMigration(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{4}, 32)
	legacy, err := NewAES256(key)
	require.NoError(t, err)
	backup, err := legacy.Encrypt([]byte("synthetic legacy secret"))
	require.NoError(t, err)
	ring, err := NewCredentialKeyring("new", map[string][]byte{"new": bytes.Repeat([]byte{5}, 32)}, key)
	require.NoError(t, err)
	clear(key)
	current, changed, err := ring.Rekey(credentialTestScope(), backup)
	require.NoError(t, err)
	require.True(t, changed)
	opened, err := ring.Open(credentialTestScope(), current)
	require.NoError(t, err)
	require.Equal(t, []byte("synthetic legacy secret"), opened)
	_, err = legacy.Decrypt(current)
	require.Error(t, err)
	// Tampered versioned records must not fall back to legacy decryption.
	current[len(envelopeMagic)-1]++
	_, err = ring.Open(credentialTestScope(), current)
	require.ErrorIs(t, err, ErrCredentialCiphertext)
	_, _, err = ring.Rekey(credentialTestScope(), []byte("not a ciphertext"))
	require.ErrorIs(t, err, ErrCredentialCiphertext)
}

func TestCredentialKeyringValidation(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{6}, 32)
	for _, id := range []string{"", "has space", "has/slash", "\x00", "é", string(bytes.Repeat([]byte{'a'}, 65))} {
		_, err := NewCredentialKeyring(id, map[string][]byte{id: key}, nil)
		require.ErrorIs(t, err, ErrCredentialKeyring)
	}
	_, err := NewCredentialKeyring("missing", map[string][]byte{"valid": key}, nil)
	require.ErrorIs(t, err, ErrCredentialKeyUnavailable)
	_, err = NewCredentialKeyring("valid", map[string][]byte{"valid": key, "bad": {1}}, nil)
	require.ErrorIs(t, err, ErrCredentialKeyring)
	_, err = NewCredentialKeyring("valid", map[string][]byte{"valid": key}, []byte{1})
	require.ErrorIs(t, err, ErrCredentialKeyring)
	ring, err := NewCredentialKeyring("valid", map[string][]byte{"valid": key}, nil)
	require.NoError(t, err)
	for _, scope := range []CredentialScope{
		{},
		{Tenant: "t", Resource: "r", Purpose: "p"},
		{Resource: "r", Purpose: "p", SchemaVersion: 1},
		{Tenant: "t", Purpose: "p", SchemaVersion: 1},
		{Tenant: "t", Resource: "r", SchemaVersion: 1},
	} {
		_, err := ring.Seal(scope, []byte("synthetic"))
		require.ErrorIs(t, err, ErrCredentialScope)
		_, err = ring.Open(scope, []byte("synthetic"))
		require.ErrorIs(t, err, ErrCredentialScope)
	}
	_, err = ring.Seal(credentialTestScope(), nil)
	require.ErrorIs(t, err, ErrCredentialCiphertext)
}

func FuzzCredentialKeyringOpen(f *testing.F) {
	ring, err := NewCredentialKeyring("test", map[string][]byte{"test": bytes.Repeat([]byte{7}, 32)}, bytes.Repeat([]byte{8}, 32))
	if err != nil {
		f.Fatal(err)
	}
	scope := credentialTestScope()
	sealed, err := ring.Seal(scope, []byte("synthetic"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(sealed)
	f.Add([]byte(envelopeMagic))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		opened, err := ring.Open(scope, raw)
		if err != nil && opened != nil {
			t.Fatal("failed authentication returned plaintext")
		}
	})
}
