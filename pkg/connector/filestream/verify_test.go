package filestream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifiedReaderRefusesIncompleteOrChangedInput(t *testing.T) {
	t.Parallel()
	sum := sha256.Sum256([]byte("good"))
	digest := hex.EncodeToString(sum[:])
	for _, body := range []string{"good", "bad!", "goo", "good!"} {
		t.Run(body, func(t *testing.T) {
			reader, err := NewVerifiedReader(t.Context(), strings.NewReader(body), 4, digest)
			require.NoError(t, err)
			content, err := io.ReadAll(reader)
			if body == "good" {
				require.NoError(t, err)
				require.Equal(t, body, string(content))
			} else {
				require.ErrorIs(t, err, ErrUnavailable)
				require.Less(t, len(content), 4)
				_, err = reader.Read(make([]byte, 1))
				require.ErrorIs(t, err, ErrUnavailable)
			}
		})
	}
	empty := sha256.Sum256(nil)
	reader, err := NewVerifiedReader(t.Context(), strings.NewReader(""), 0, hex.EncodeToString(empty[:]))
	require.NoError(t, err)
	n, err := reader.Read(make([]byte, 1))
	require.Zero(t, n)
	require.Equal(t, io.EOF, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader, err = NewVerifiedReader(ctx, strings.NewReader("good"), 4, digest)
	require.NoError(t, err)
	_, err = reader.Read(make([]byte, 4))
	require.ErrorIs(t, err, context.Canceled)
}
