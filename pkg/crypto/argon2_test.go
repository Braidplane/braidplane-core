package crypto

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/argon2"
)

func TestNewArgon2(t *testing.T) {
	t.Parallel()

	t.Run("CreatesHasherWithDefaultParams", func(t *testing.T) {
		t.Parallel()

		hasher := NewArgon2()
		require.NotNil(t, hasher)

		// Verify it's an argon2Hasher
		a2, ok := hasher.(*argon2Hasher)
		require.True(t, ok)
		assert.Equal(t, defaultParams.memory, a2.params.memory)
		assert.Equal(t, defaultParams.iterations, a2.params.iterations)
		assert.Equal(t, defaultParams.parallelism, a2.params.parallelism)
		assert.Equal(t, defaultParams.saltLength, a2.params.saltLength)
		assert.Equal(t, defaultParams.keyLength, a2.params.keyLength)
	})
}

func TestNewArgon2WithParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		memory      uint32
		iterations  uint32
		parallelism uint8
		saltLength  uint32
		keyLength   uint32
	}{
		{
			name:        "CustomParamsHighSecurity",
			memory:      128 * 1024,
			iterations:  4,
			parallelism: 4,
			saltLength:  32,
			keyLength:   64,
		},
		{
			name:        "CustomParamsLowSecurity",
			memory:      32 * 1024,
			iterations:  1,
			parallelism: 1,
			saltLength:  8,
			keyLength:   16,
		},
		{
			name:        "CustomParamsDefaultValues",
			memory:      64 * 1024,
			iterations:  3,
			parallelism: 2,
			saltLength:  16,
			keyLength:   32,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hasher := NewArgon2WithParams(
				tt.memory,
				tt.iterations,
				tt.parallelism,
				tt.saltLength,
				tt.keyLength,
			)
			require.NotNil(t, hasher)

			a2, ok := hasher.(*argon2Hasher)
			require.True(t, ok)
			assert.Equal(t, tt.memory, a2.params.memory)
			assert.Equal(t, tt.iterations, a2.params.iterations)
			assert.Equal(t, tt.parallelism, a2.params.parallelism)
			assert.Equal(t, tt.saltLength, a2.params.saltLength)
			assert.Equal(t, tt.keyLength, a2.params.keyLength)
		})
	}
}

func TestArgon2Hasher_Hash(t *testing.T) {
	t.Parallel()

	hasher := NewArgon2()

	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "SimplePassword",
			input: "password123",
		},
		{
			name:  "ComplexPasswordWithSpecialChars",
			input: "P@ssw0rd!#$%^&*()",
		},
		{
			name:  "LongPassword",
			input: "ThisIsAVeryLongPasswordThatContainsLotsOfCharactersToTestTheLimits",
		},
		{
			name:  "EmptyString",
			input: "",
		},
		{
			name:  "UnicodeCharacters",
			input: "пароль密码🔒",
		},
		{
			name:  "PasswordWithSpaces",
			input: "my secure password",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hash, err := hasher.Hash(tt.input)
			require.NoError(t, err)
			assert.NotEmpty(t, hash)

			// Verify hash format: $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
			parts := strings.Split(hash, "$")
			assert.Len(t, parts, 6)
			assert.Empty(t, parts[0]) // First part is empty due to leading $
			assert.Equal(t, "argon2id", parts[1])
			assert.Contains(t, parts[2], "v=")
			assert.Contains(t, parts[3], "m=")
			assert.Contains(t, parts[3], "t=")
			assert.Contains(t, parts[3], "p=")
			assert.NotEmpty(t, parts[4]) // salt
			assert.NotEmpty(t, parts[5]) // hash
		})
	}
}

func TestArgon2Hasher_Hash_Uniqueness(t *testing.T) {
	t.Parallel()

	t.Run("SameInputProducesDifferentHashesDueToRandomSalt", func(t *testing.T) {
		t.Parallel()

		hasher := NewArgon2()
		password := "testPassword123"

		hash1, err := hasher.Hash(password)
		require.NoError(t, err)

		hash2, err := hasher.Hash(password)
		require.NoError(t, err)

		// Hashes should be different due to different salts
		assert.NotEqual(t, hash1, hash2)

		// But both should verify against the original password
		match1, err := hasher.CompareHash(password, hash1)
		require.NoError(t, err)
		assert.True(t, match1)

		match2, err := hasher.CompareHash(password, hash2)
		require.NoError(t, err)
		assert.True(t, match2)
	})
}

func TestArgon2Hasher_CompareHash(t *testing.T) {
	t.Parallel()

	hasher := NewArgon2()

	tests := []struct {
		name        string
		password    string
		compareWith string
		shouldMatch bool
	}{
		{
			name:        "MatchingPasswords",
			password:    "correctPassword",
			compareWith: "correctPassword",
			shouldMatch: true,
		},
		{
			name:        "DifferentPasswords",
			password:    "correctPassword",
			compareWith: "wrongPassword",
			shouldMatch: false,
		},
		{
			name:        "CaseSensitive",
			password:    "Password",
			compareWith: "password",
			shouldMatch: false,
		},
		{
			name:        "EmptyStringMatchesEmptyString",
			password:    "",
			compareWith: "",
			shouldMatch: true,
		},
		{
			name:        "EmptyVsNonEmpty",
			password:    "",
			compareWith: "password",
			shouldMatch: false,
		},
		{
			name:        "WhitespaceMatters",
			password:    "password ",
			compareWith: "password",
			shouldMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// First, hash the password
			hash, err := hasher.Hash(tt.password)
			require.NoError(t, err)

			// Then compare with the compareWith value
			match, err := hasher.CompareHash(tt.compareWith, hash)
			require.NoError(t, err)
			assert.Equal(t, tt.shouldMatch, match)
		})
	}
}

func TestArgon2Hasher_CompareHash_WithCustomParams(t *testing.T) {
	t.Parallel()
	t.Run("CanVerifyHashCreatedWithCustomParams", func(t *testing.T) {
		t.Parallel()

		hasher := NewArgon2WithParams(32*1024, 2, 1, 16, 32)
		password := "customParamsPassword"

		hash, err := hasher.Hash(password)
		require.NoError(t, err)

		match, err := hasher.CompareHash(password, hash)
		require.NoError(t, err)
		assert.True(t, match)

		// Wrong password should not match
		match, err = hasher.CompareHash("wrongPassword", hash)
		require.NoError(t, err)
		assert.False(t, match)
	})

	t.Run("HashCreatedWithDifferentParamsCanBeVerified", func(t *testing.T) {
		t.Parallel()

		// Create hash with one set of params
		hasher1 := NewArgon2WithParams(32*1024, 2, 1, 16, 32)
		password := "testPassword"
		hash, err := hasher1.Hash(password)
		require.NoError(t, err)

		// Verify with different hasher instance (should still work because params are in hash)
		hasher2 := NewArgon2()
		match, err := hasher2.CompareHash(password, hash)
		require.NoError(t, err)
		assert.True(t, match)
	})
}

func TestDecodeHash_InvalidFormats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		encodedHash string
		wantErr     error
	}{
		{
			name:        "TooFewParts",
			encodedHash: "$argon2id$v=19$m=65536",
			wantErr:     ErrInvalidHash,
		},
		{
			name:        "TooManyParts",
			encodedHash: "$argon2id$v=19$m=65536,t=3,p=2$salt$hash$extra",
			wantErr:     ErrInvalidHash,
		},
		{
			name:        "EmptyString",
			encodedHash: "",
			wantErr:     ErrInvalidHash,
		},
		{
			name:        "NoDollarSigns",
			encodedHash: "argon2id-v19-m65536-t3-p2-salt-hash",
			wantErr:     ErrInvalidHash,
		},
		{
			name:        "IncompatibleVersion",
			encodedHash: "$argon2id$v=18$m=65536,t=3,p=2$c2FsdA$aGFzaA",
			wantErr:     ErrIncompatibleVersion,
		},
		{
			name:        "InvalidVersionFormat",
			encodedHash: "$argon2id$version=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
			wantErr:     nil, // Will fail at parsing stage
		},
		{
			name:        "InvalidMemoryFormat",
			encodedHash: "$argon2id$v=19$memory=65536,t=3,p=2$c2FsdA$aGFzaA",
			wantErr:     nil, // Will fail at parsing stage
		},
		{
			name:        "InvalidBase64Salt",
			encodedHash: "$argon2id$v=19$m=65536,t=3,p=2$invalid!!!$aGFzaA",
			wantErr:     nil, // Will fail at base64 decode
		},
		{
			name:        "InvalidBase64Hash",
			encodedHash: "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$invalid!!!",
			wantErr:     nil, // Will fail at base64 decode
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, _, err := decodeHash(tt.encodedHash)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				require.Error(t, err) // Should error but not necessarily the specific error
			}
		})
	}
}

func TestDecodeHash_ValidHash(t *testing.T) {
	t.Parallel()
	t.Run("DecodesValidHashCorrectly", func(t *testing.T) {
		t.Parallel()
		// Create a valid hash first
		hasher := NewArgon2()
		password := "testPassword"
		hash, err := hasher.Hash(password)
		require.NoError(t, err)

		// Decode it
		p, salt, decodedHash, err := decodeHash(hash)
		require.NoError(t, err)

		// Verify params match defaults
		assert.Equal(t, defaultParams.memory, p.memory)
		assert.Equal(t, defaultParams.iterations, p.iterations)
		assert.Equal(t, defaultParams.parallelism, p.parallelism)
		assert.Len(t, salt, int(defaultParams.saltLength))
		assert.Len(t, decodedHash, int(defaultParams.keyLength))
	})
}

func TestComparePasswordAndHash(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		password    string
		setupHash   func() string
		shouldMatch bool
		wantErr     error
	}{
		{
			name:     "ValidMatchingPassword",
			password: "correctPassword",
			setupHash: func() string {
				hasher := NewArgon2()
				hash, _ := hasher.Hash("correctPassword")
				return hash
			},
			shouldMatch: true,
			wantErr:     nil,
		},
		{
			name:     "ValidNonMatchingPassword",
			password: "wrongPassword",
			setupHash: func() string {
				hasher := NewArgon2()
				hash, _ := hasher.Hash("correctPassword")
				return hash
			},
			shouldMatch: false,
			wantErr:     nil,
		},
		{
			name:     "InvalidHashFormat",
			password: "anyPassword",
			setupHash: func() string {
				return "not-a-valid-hash"
			},
			shouldMatch: false,
			wantErr:     ErrInvalidHash,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hash := tt.setupHash()
			match, err := comparePasswordAndHash(tt.password, hash)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.shouldMatch, match)
			}
		})
	}
}

func TestArgon2Hasher_Integration(t *testing.T) {
	t.Parallel()
	t.Run("CompleteAuthenticationFlow", func(t *testing.T) {
		t.Parallel()
		hasher := NewArgon2()

		// User registration: hash password
		password := "userPassword123!"
		hashedPassword, err := hasher.Hash(password)
		require.NoError(t, err)
		assert.NotEmpty(t, hashedPassword)

		// User login: verify password
		match, err := hasher.CompareHash(password, hashedPassword)
		require.NoError(t, err)
		assert.True(t, match, "correct password should match")

		// Failed login: wrong password
		match, err = hasher.CompareHash("wrongPassword", hashedPassword)
		require.NoError(t, err)
		assert.False(t, match, "wrong password should not match")
	})
}

func TestArgon2Hasher_EdgeCases(t *testing.T) {
	t.Parallel()
	hasher := NewArgon2()

	t.Run("VeryLongPassword", func(t *testing.T) {
		t.Parallel()
		// Create a very long password (1KB)
		longPassword := strings.Repeat("a", 1024)
		hash, err := hasher.Hash(longPassword)
		require.NoError(t, err)

		match, err := hasher.CompareHash(longPassword, hash)
		require.NoError(t, err)
		assert.True(t, match)
	})

	t.Run("PasswordWithNullBytes", func(t *testing.T) {
		t.Parallel()
		password := "password\x00withNull"
		hash, err := hasher.Hash(password)
		require.NoError(t, err)

		match, err := hasher.CompareHash(password, hash)
		require.NoError(t, err)
		assert.True(t, match)
	})

	t.Run("MultipleSequentialHashes", func(t *testing.T) {
		t.Parallel()
		password := "testPassword"
		hashes := make([]string, 10)

		// Create multiple hashes
		for i := range 10 {
			hash, err := hasher.Hash(password)
			require.NoError(t, err)
			hashes[i] = hash
		}

		// All should be unique (due to random salt)
		for i := range hashes {
			for j := i + 1; j < len(hashes); j++ {
				assert.NotEqual(t, hashes[i], hashes[j])
			}
		}

		// All should verify correctly
		for _, hash := range hashes {
			match, err := hasher.CompareHash(password, hash)
			require.NoError(t, err)
			assert.True(t, match)
		}
	})
}

func TestArgon2Version(t *testing.T) {
	t.Parallel()
	t.Run("UsesCorrectArgon2Version", func(t *testing.T) {
		t.Parallel()
		hasher := NewArgon2()
		hash, err := hasher.Hash("testPassword")
		require.NoError(t, err)

		// Check that the version in the hash matches the library version
		parts := strings.Split(hash, "$")
		require.Len(t, parts, 6)

		var version int
		_, err = fmt.Sscanf(parts[2], "v=%d", &version)
		require.NoError(t, err)
		assert.Equal(t, argon2.Version, version)
	})
}
