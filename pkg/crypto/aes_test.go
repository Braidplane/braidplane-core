package crypto

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewAES256(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		keySize       int
		wantErr       error
		wantErrString string
	}{
		{
			name:    "ValidKey32Bytes",
			keySize: 32,
			wantErr: nil,
		},
		{
			name:          "InvalidKeyTooShort",
			keySize:       16,
			wantErrString: "encryption key must be 32 bytes (AES-256), got 16",
		},
		{
			name:          "InvalidKeyTooLong",
			keySize:       64,
			wantErrString: "encryption key must be 32 bytes (AES-256), got 64",
		},
		{
			name:          "InvalidKeyEmpty",
			keySize:       0,
			wantErrString: "encryption key must be 32 bytes (AES-256), got 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			key := make([]byte, tt.keySize)
			if tt.keySize > 0 {
				_, err := rand.Read(key)
				require.NoError(t, err)
			}

			enc, err := NewAES256(key)

			if tt.wantErrString != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.wantErrString)
				require.Nil(t, enc)
			} else {
				require.NoError(t, err)
				require.NotNil(t, enc)
			}
		})
	}
}

func TestAES256_Encrypt(t *testing.T) {
	t.Parallel()

	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	enc, err := NewAES256(key)
	require.NoError(t, err)

	tests := []struct {
		name          string
		plaintext     []byte
		wantErrString string
	}{
		{
			name:      "ValidPlaintextShort",
			plaintext: []byte("hello"),
		},
		{
			name:      "ValidPlaintextLong",
			plaintext: bytes.Repeat([]byte("a"), 1000),
		},
		{
			name:      "ValidPlaintextSingleByte",
			plaintext: []byte("x"),
		},
		{
			name:          "EmptyPlaintext",
			plaintext:     []byte{},
			wantErrString: "plaintext cannot be empty",
		},
		{
			name:          "NilPlaintext",
			plaintext:     nil,
			wantErrString: "plaintext cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ciphertext, err := enc.Encrypt(tt.plaintext)

			if tt.wantErrString != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.wantErrString)
				require.Nil(t, ciphertext)
			} else {
				require.NoError(t, err)
				require.NotNil(t, ciphertext)
				// Ciphertext should be longer than plaintext (nonce + ciphertext + auth tag)
				// GCM adds 12 bytes (nonce) + 16 bytes (auth tag) = 28 bytes overhead
				require.Greater(t, len(ciphertext), len(tt.plaintext))
			}
		})
	}
}

func TestAES256_Decrypt(t *testing.T) {
	t.Parallel()

	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	enc, err := NewAES256(key)
	require.NoError(t, err)

	// Create valid ciphertext for some tests
	validPlaintext := []byte("test message")
	validCiphertext, err := enc.Encrypt(validPlaintext)
	require.NoError(t, err)

	tests := []struct {
		name          string
		ciphertext    []byte
		wantErrString string
	}{
		{
			name:       "ValidCiphertext",
			ciphertext: validCiphertext,
		},
		{
			name:          "EmptyCiphertext",
			ciphertext:    []byte{},
			wantErrString: "ciphertext cannot be empty",
		},
		{
			name:          "NilCiphertext",
			ciphertext:    nil,
			wantErrString: "ciphertext cannot be empty",
		},
		{
			name:          "CiphertextTooShort",
			ciphertext:    []byte{1, 2, 3},
			wantErrString: "ciphertext too short",
		},
		{
			name:          "CorruptedCiphertext",
			ciphertext:    append([]byte{}, validCiphertext...),
			wantErrString: "failed to decrypt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Corrupt the ciphertext for the corruption test
			if tt.name == "CorruptedCiphertext" {
				tt.ciphertext[len(tt.ciphertext)-1] ^= 0xFF
			}

			plaintext, err := enc.Decrypt(tt.ciphertext)

			if tt.wantErrString != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.wantErrString)
				require.Nil(t, plaintext)
			} else {
				require.NoError(t, err)
				require.Equal(t, validPlaintext, plaintext)
			}
		})
	}
}

func TestAES256_RoundTrip(t *testing.T) {
	t.Parallel()

	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	enc, err := NewAES256(key)
	require.NoError(t, err)

	tests := []struct {
		name      string
		plaintext []byte
	}{
		{
			name:      "ShortMessage",
			plaintext: []byte("hello world"),
		},
		{
			name:      "LongMessage",
			plaintext: bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog. "), 100),
		},
		{
			name:      "BinaryData",
			plaintext: []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD},
		},
		{
			name:      "UTF8Characters",
			plaintext: []byte("Hello 世界 🌍"),
		},
		{
			name:      "SingleByte",
			plaintext: []byte("x"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ciphertext, err := enc.Encrypt(tt.plaintext)
			require.NoError(t, err)
			require.NotNil(t, ciphertext)

			decrypted, err := enc.Decrypt(ciphertext)
			require.NoError(t, err)
			require.Equal(t, tt.plaintext, decrypted)
		})
	}
}

func TestAES256_DifferentKeys(t *testing.T) {
	t.Parallel()

	key1 := make([]byte, 32)
	_, err := rand.Read(key1)
	require.NoError(t, err)

	key2 := make([]byte, 32)
	_, err = rand.Read(key2)
	require.NoError(t, err)

	enc1, err := NewAES256(key1)
	require.NoError(t, err)

	enc2, err := NewAES256(key2)
	require.NoError(t, err)

	plaintext := []byte("secret message")

	ciphertext, err := enc1.Encrypt(plaintext)
	require.NoError(t, err)

	// Attempting to decrypt with wrong key should fail
	_, err = enc2.Decrypt(ciphertext)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to decrypt")
}

func TestAES256_UniqueNonces(t *testing.T) {
	t.Parallel()

	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	enc, err := NewAES256(key)
	require.NoError(t, err)

	plaintext := []byte("same message")

	// Encrypt the same plaintext multiple times
	ciphertext1, err := enc.Encrypt(plaintext)
	require.NoError(t, err)

	ciphertext2, err := enc.Encrypt(plaintext)
	require.NoError(t, err)

	// Ciphertexts should be different due to random nonces
	require.NotEqual(t, ciphertext1, ciphertext2)

	// But both should decrypt to the same plaintext
	decrypted1, err := enc.Decrypt(ciphertext1)
	require.NoError(t, err)
	require.Equal(t, plaintext, decrypted1)

	decrypted2, err := enc.Decrypt(ciphertext2)
	require.NoError(t, err)
	require.Equal(t, plaintext, decrypted2)
}

func TestAES256_TamperedCiphertext(t *testing.T) {
	t.Parallel()

	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	enc, err := NewAES256(key)
	require.NoError(t, err)

	plaintext := []byte("authenticated message")
	ciphertext, err := enc.Encrypt(plaintext)
	require.NoError(t, err)

	tests := []struct {
		name          string
		tamperFn      func([]byte) []byte
		wantErrString string
	}{
		{
			name: "ModifyFirstByte",
			tamperFn: func(ct []byte) []byte {
				modified := append([]byte{}, ct...)
				modified[0] ^= 0xFF
				return modified
			},
			wantErrString: "failed to decrypt",
		},
		{
			name: "ModifyLastByte",
			tamperFn: func(ct []byte) []byte {
				modified := append([]byte{}, ct...)
				modified[len(modified)-1] ^= 0xFF
				return modified
			},
			wantErrString: "failed to decrypt",
		},
		{
			name: "ModifyMiddleByte",
			tamperFn: func(ct []byte) []byte {
				modified := append([]byte{}, ct...)
				modified[len(modified)/2] ^= 0xFF
				return modified
			},
			wantErrString: "failed to decrypt",
		},
		{
			name: "TruncateCiphertext",
			tamperFn: func(ct []byte) []byte {
				return ct[:len(ct)-1]
			},
			wantErrString: "failed to decrypt",
		},
		{
			name: "AppendByte",
			tamperFn: func(ct []byte) []byte {
				return append(ct, 0x00)
			},
			wantErrString: "failed to decrypt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tamperedCiphertext := tt.tamperFn(ciphertext)

			_, err := enc.Decrypt(tamperedCiphertext)
			require.Error(t, err)
			require.ErrorContains(t, err, tt.wantErrString)
		})
	}
}
