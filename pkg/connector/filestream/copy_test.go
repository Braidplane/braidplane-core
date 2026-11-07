package filestream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestCopyLimitAndChecksum(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		input   string
		limit   int64
		exceeds bool
	}{
		{"below", "abcd", 5, false}, {"at", "abcde", 5, false}, {"above", "abcdef", 5, true}, {"empty", "", 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			metadata, err := Copy(t.Context(), &output, strings.NewReader(tc.input), tc.limit)
			if tc.exceeds {
				require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
				require.Equal(t, tc.input[:tc.limit], output.String())
				require.Nil(t, metadata)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.input, output.String())
			sum := sha256.Sum256([]byte(tc.input))
			require.Equal(t, hex.EncodeToString(sum[:]), metadata.SHA256)
			require.Equal(t, int64(len(tc.input)), metadata.SizeBytes)
		})
	}
}

type cancellingWriter struct {
	cancel context.CancelFunc
	writes int
}

func (w *cancellingWriter) Write(p []byte) (int, error) { w.writes++; w.cancel(); return len(p), nil }

func TestCopyStopsAfterCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dst := &cancellingWriter{cancel: cancel}
	metadata, err := Copy(ctx, dst, strings.NewReader(strings.Repeat("x", 128*1024)), 1024*1024)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, metadata)
	require.Equal(t, 1, dst.writes)
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestCopyRejectsReadFailure(t *testing.T) {
	t.Parallel()
	metadata, err := Copy(t.Context(), io.Discard, brokenReader{}, 10)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Nil(t, metadata)
}

type probeCancelReader struct{ cancel context.CancelFunc }

func (r probeCancelReader) Read([]byte) (int, error) { r.cancel(); return 0, io.EOF }

func TestCopyDoesNotReportSuccessWhenEOFReadCancels(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	metadata, err := Copy(ctx, io.Discard, probeCancelReader{cancel: cancel}, 10)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, metadata)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type countedReader struct{ bytes int }

func (r *countedReader) Read(p []byte) (int, error) { r.bytes += len(p); clear(p); return len(p), nil }

func TestCopyDoesNotReadAheadOfFailedDestination(t *testing.T) {
	t.Parallel()
	source := &countedReader{}
	metadata, err := Copy(t.Context(), failingWriter{}, source, 10*1024*1024)
	require.ErrorIs(t, err, io.ErrClosedPipe)
	require.Nil(t, metadata)
	require.Equal(t, 64*1024, source.bytes)
}
