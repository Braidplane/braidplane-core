package ftp

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStreamingReadCancellationInterruptsBlockedData(t *testing.T) {
	connector, reached, release := newCancellationFTP(t, t.Context(), "RETR-data")
	defer func() { _ = connector.Close() }()
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (ftpProvider{connector}).ReadFileTo(ctx, "/fixture", 64, io.Discard)
		done <- err
	}()
	select {
	case <-reached:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not reach the FTP data stall")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt the FTP stream")
	}
	require.Error(t, connector.TestConnection(t.Context()))
}
